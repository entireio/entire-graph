package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/bench"
	"github.com/entireio/entire-graph/internal/sem"
)

func TestFormatProgressIncludesTypedPhaseElapsedAndTotalElapsed(t *testing.T) {
	got := formatProgress("owner/repo", sem.ProgressEvent{
		Phase:        sem.BuildPhaseParse,
		FilesDone:    3,
		FilesTotal:   5,
		PhaseElapsed: 12 * time.Millisecond,
		Elapsed:      30 * time.Millisecond,
	})
	if !strings.Contains(got, "phase=parse") || !strings.Contains(got, "phase_elapsed=12ms") || !strings.Contains(got, "elapsed=30ms") {
		t.Fatalf("progress = %q", got)
	}
}

func TestWriteSummaryPrintsPhaseSharesAndArtifactMetrics(t *testing.T) {
	report := bench.Report{Totals: bench.Aggregate{
		Repos: 1, Files: 2, LOC: 10, Symbols: 3, Relations: 4, WallMS: 100,
		PhaseMS:        map[string]float64{"inventory": 10, "parse": 50, "relations": 30, "finalize": 10},
		NDJSONRawBytes: 1000, CompactRawBytes: 500, CompactDictionaryBytes: 100,
		ProjectedFacts: 10, NDJSONBytesPerProjectedFact: 100, CompactBytesPerProjectedFact: 50,
	}}
	var out bytes.Buffer
	writeSummary(&out, report)
	got := out.String()
	for _, want := range []string{"PHASE", "inventory", "50.00%", "native_raw=1000", "compact_raw=500", "native_bytes/fact=100.00", "compact_bytes/fact=50.00"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestValidateExecutionModeRejectsParentOnlyCPUProfile(t *testing.T) {
	err := validateExecutionMode("cpu.out")
	if err == nil || !strings.Contains(err.Error(), "isolated") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("CPU profile validation error = %v", err)
	}
	if err := validateExecutionMode(""); err != nil {
		t.Fatalf("empty CPU profile unexpectedly rejected: %v", err)
	}
}

func TestMaxRSSGuardFailsRunEvenWhenViolatingRowIsExcludedFromAggregates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process RSS is not available on this platform")
	}
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"languages":{"Go":["owner/repo"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache")
	repoDir := filepath.Join(cacheDir, "Go", "owner__repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENTIRE_GRAPH_BENCH_MAIN_TEST_WORKER", "1")
	worker := []string{os.Args[0], "-test.run=^TestGraphBenchMeasureWorker$"}
	outDir := filepath.Join(dir, "out")
	err := runWithWorkerCommand(manifestPath, cacheDir, outDir, filepath.Join(dir, "lock.json"), "", "fast", 0, 1, 1, true, false, "test", false, 0, 1, false, worker)
	if err == nil || !strings.Contains(err.Error(), "memory guardrail failed") {
		t.Fatalf("run guard error = %v", err)
	}
	report := readOnlyReport(t, outDir)
	if len(report.Repos) != 1 || report.Repos[0].Error == "" || report.Repos[0].MaxRSSBytes <= 1 {
		t.Fatalf("guard failure row lost from report: %#v", report.Repos)
	}
	if report.Totals.Repos != 0 {
		t.Fatalf("guard failure row should remain excluded from aggregates: %#v", report.Totals)
	}
}

func TestGraphBenchMeasureWorker(t *testing.T) {
	if os.Getenv("ENTIRE_GRAPH_BENCH_MAIN_TEST_WORKER") != "1" {
		return
	}
	os.Exit(bench.RunMeasureWorker(context.Background(), os.Stdin, os.Stdout))
}

func TestParseProfile(t *testing.T) {
	cases := map[string]sem.Profile{
		"":            sem.ProfileFull,
		"full":        sem.ProfileFull,
		"fast":        sem.ProfileFast,
		"syntax-only": sem.ProfileSyntaxOnly,
	}
	for input, want := range cases {
		got, err := parseProfile(input)
		if err != nil {
			t.Fatalf("parseProfile(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseProfile(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseProfile("bogus"); err == nil {
		t.Fatalf("parseProfile(bogus) should error")
	}
}

// A repo that is not cloned is skipped, but its metrics row must still report
// the run's selected profile so a fast/syntax-only report does not leave the
// skipped rows' profile blank.
func TestSkippedRepoReportsSelectedProfile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := `{"languages":{"Go":["owner/not-cloned"]}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(dir, "cache") // empty: the repo is "not cloned"
	outDir := filepath.Join(dir, "out")
	lockPath := filepath.Join(dir, "lock.json")

	// skip-clone so no network is touched; syntax-only is the selected profile.
	err := run(manifestPath, cacheDir, outDir, lockPath, "", "syntax-only", 0, 1, 1, true, false, "bench-test", false, 0, 0, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	report := readOnlyReport(t, outDir)
	if report.Profile != "syntax-only" {
		t.Fatalf("report profile = %q, want syntax-only", report.Profile)
	}
	if len(report.Repos) != 1 {
		t.Fatalf("repos = %#v, want one skipped row", report.Repos)
	}
	got := report.Repos[0]
	if got.Error == "" {
		t.Fatalf("skipped repo should record an error: %#v", got)
	}
	if got.Profile != "syntax-only" {
		t.Fatalf("skipped repo profile = %q, want syntax-only", got.Profile)
	}
}

func TestGuardrailFailureAfterReport(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"languages":{"Go":["owner/not-cloned"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	err := run(manifestPath, filepath.Join(dir, "cache"), outDir, filepath.Join(dir, "lock.json"), "", "syntax-only", 0, 1, 1, true, false, "bench-test", false, 1, 0, false)
	if err == nil {
		t.Fatalf("expected guardrail failure")
	}
	if report := readOnlyReport(t, outDir); report.Profile != "syntax-only" {
		t.Fatalf("report was not written before guardrail failure: %#v", report)
	}
}

func readOnlyReport(t *testing.T, outDir string) bench.Report {
	t.Helper()
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one report file, got %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(outDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var report bench.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return report
}

func benchGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// newUpstreamRepo builds a one-commit local repository that ensureRepo can
// clone and fetch from with no network, keeping the bench tool's no-egress
// property intact under test.
func newUpstreamRepo(t *testing.T) string {
	t.Helper()
	upstream := t.TempDir()
	benchGit(t, upstream, "init", "--quiet", "--initial-branch", "main", ".")
	benchGit(t, upstream, "config", "user.name", "Entire Graph Test")
	benchGit(t, upstream, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(upstream, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "init")
	return upstream
}

// A ref is argv input: it comes from the manifest (`owner/name@<ref>`) or the
// commit lock file, and it lands in a positional slot of `git fetch`. Git's
// parse-options permutes arguments, so an option-shaped ref there is parsed as
// an option. ensureRepo must refuse such a ref before any git process runs.
//
// The upstream in this fixture is a filesystem path, which is the transport
// where Git runs upload-pack locally and `--upload-pack=<cmd>` therefore
// executes <cmd>; that gives the test an unambiguous observable. graph-bench's
// own cloneURL builds an https URL, where Git ignores the option, so the marker
// assertion demonstrates the guard rather than production severity.
func TestEnsureRepoRefusesOptionShapedRefBeforeGitRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The payload is a POSIX command line: `touch <path>` is not a Windows
		// executable, so the exploit's observable side effect (the marker file)
		// is unrepresentable there and the negative assertion would pass
		// vacuously. The shape guard itself is covered by
		// TestValidateRefRejectsOptionShapedRefs, which runs everywhere.
		t.Skip("the `touch` marker payload is POSIX-only")
	}
	upstream := newUpstreamRepo(t)
	dir := filepath.Join(t.TempDir(), "clone")

	// Pre-clone with a benign ref: this is the realistic state, a repo already
	// in -cache from an earlier run, so ensureRepo skips cloning and goes
	// straight to the fetch that carries the ref as a positional.
	if _, err := ensureRepo(t.Context(), upstream, "main", dir, 1); err != nil {
		t.Fatalf("benign pre-clone: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "pwned")
	_, err := ensureRepo(t.Context(), upstream, "--upload-pack=touch "+marker, dir, 1)
	// Asserted before the error shape: this is the security property. Without
	// the guard, git runs `touch <marker>` and the file appears.
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("option-shaped ref executed an arbitrary command: %s exists", marker)
	}
	if err == nil || !strings.Contains(err.Error(), "invalid git ref") {
		t.Fatalf("ensureRepo error = %v, want an invalid-git-ref rejection", err)
	}
}

// The guard must not break the ordinary path: a real ref still fetches and
// checks out, and ensureRepo still reports that commit.
func TestEnsureRepoStillFetchesOrdinaryRef(t *testing.T) {
	upstream := newUpstreamRepo(t)
	dir := filepath.Join(t.TempDir(), "clone")

	sha, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	assertFullObjectID(t, sha)
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("ordinary ref did not produce a checkout: %v", statErr)
	}

	// Second call takes the already-cloned branch: fetch + checkout of the same
	// ref must still succeed with --end-of-options in the argv.
	again, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on existing clone: %v", err)
	}
	if again != sha {
		t.Fatalf("sha = %q on re-fetch, want %q", again, sha)
	}
}

func TestValidateRefRejectsOptionShapedRefs(t *testing.T) {
	for _, ref := range []string{"--upload-pack=touch x", "-o", "--exec=x", "\x00"} {
		if err := validateRef(ref); err == nil {
			t.Fatalf("validateRef(%q) = nil, want rejection", ref)
		}
	}
	for _, ref := range []string{"", "main", "v1.2.3", "refs/heads/main", "1a2b3c4d5e6f7890abcdef1234567890abcdef12"} {
		if err := validateRef(ref); err != nil {
			t.Fatalf("validateRef(%q) = %v, want nil", ref, err)
		}
	}
}

// Git supports two object formats, SHA-1 (40 hex chars) and SHA-256 (64). A
// repository created under GIT_DEFAULT_HASH=sha256 or
// init.defaultObjectFormat=sha256 yields the longer id, so assert the shape Git
// can actually produce rather than SHA-1's length alone.
func assertFullObjectID(t *testing.T, sha string) {
	t.Helper()
	if len(sha) != 40 && len(sha) != 64 {
		t.Fatalf("resolved sha = %q (len %d), want a full commit id (40 hex for SHA-1, 64 for SHA-256)", sha, len(sha))
	}
	for _, r := range sha {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("resolved sha = %q, want lowercase hex", sha)
		}
	}
}

// newUpstreamRepoWithObjectFormat is newUpstreamRepo with an explicit Git object
// format, so the SHA-256 case is exercised without depending on the ambient
// GIT_DEFAULT_HASH / init.defaultObjectFormat of the machine running the test.
func newUpstreamRepoWithObjectFormat(t *testing.T, format string) string {
	t.Helper()
	upstream := t.TempDir()
	benchGit(t, upstream, "init", "--quiet", "--initial-branch", "main", "--object-format", format, ".")
	benchGit(t, upstream, "config", "user.name", "Entire Graph Test")
	benchGit(t, upstream, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(upstream, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "init")
	return upstream
}

// ensureRepo must report the commit it checked out under either object format.
// Under SHA-256 the clone and checkout succeed and only the reported id gets
// longer, so a test that pins the id to SHA-1's 40 characters fails on a
// correctly working tool.
func TestEnsureRepoResolvesSHA256ObjectFormat(t *testing.T) {
	if out, err := exec.CommandContext(t.Context(), "git", "init", "--object-format=sha256", "-h").CombinedOutput(); err != nil && strings.Contains(string(out), "unknown option") {
		t.Skipf("this git has no --object-format: %s", out)
	}
	upstream := newUpstreamRepoWithObjectFormat(t, "sha256")
	dir := filepath.Join(t.TempDir(), "clone")

	sha, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	assertFullObjectID(t, sha)
	if len(sha) != 64 {
		t.Fatalf("resolved sha = %q, want a 64-char SHA-256 commit id", sha)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("sha256 upstream did not produce a checkout: %v", statErr)
	}
}

// writeGit223Shim puts a `git` earlier on PATH that behaves like Git 2.23: it
// reports that version and rejects --end-of-options the way parse-options did
// before Git 2.24 learned the flag. Everything else is delegated to the real
// git, so the repository operations under test are genuine.
func writeGit223Shim(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	shimDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ] || [ \"$1\" = --version ]; then echo 'git version 2.23.0'; exit 0; fi\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = --end-of-options ]; then\n" +
		"    echo \"error: unknown option \\`end-of-options'\" >&2\n" +
		"    exit 129\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// --end-of-options reached parse-options in Git 2.24 (2019-11). On 2.23 and
// earlier every git invocation carrying it dies with "unknown option", which
// would break every benchmark clone -- including ordinary, trusted refs -- on
// those versions. ensureRepo must therefore only pass the flag to a git that
// understands it, and keep working when it does not. validateRef is the guard
// that always runs; --end-of-options is defence in depth layered on top.
//
// `--` is not a substitute: for `git checkout` it means "everything after this
// is a pathspec", so `git checkout --quiet -- main` looks for a *file* named
// main and fails with "pathspec 'main' did not match any file(s) known to git"
// (verified against git 2.54.0).
func TestEnsureRepoWorksOnGitWithoutEndOfOptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The shim is a POSIX /bin/sh script; Windows resolves `git` through
		// PATHEXT to git.exe and will not execute an extensionless shell
		// script, so an old-git PATH cannot be represented here.
		t.Skip("the git shim is a POSIX /bin/sh script")
	}
	// Build the fixture with the real git, before the shim shadows it.
	upstream := newUpstreamRepo(t)
	writeGit223Shim(t)

	dir := filepath.Join(t.TempDir(), "clone")
	sha, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on a Git without --end-of-options: %v", err)
	}
	assertFullObjectID(t, sha)
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("no checkout produced on a Git without --end-of-options: %v", statErr)
	}

	// The already-cloned branch -- fetch + checkout -- must survive it too.
	again, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on existing clone without --end-of-options: %v", err)
	}
	if again != sha {
		t.Fatalf("sha = %q on re-fetch, want %q", again, sha)
	}

	// And the shape guard, which is what actually stops an option-shaped ref,
	// still rejects one when --end-of-options is unavailable.
	if _, err := ensureRepo(t.Context(), upstream, "--upload-pack=touch /dev/null", dir, 1); err == nil ||
		!strings.Contains(err.Error(), "invalid git ref") {
		t.Fatalf("ensureRepo error = %v, want an invalid-git-ref rejection", err)
	}
}
func TestGitVersionHasEndOfOptions(t *testing.T) {
	for out, want := range map[string]bool{
		"git version 2.54.0":                 true,
		"git version 2.24.0":                 true,
		"git version 2.24.0.rc1":             true,
		"git version 2.23.0":                 false,
		"git version 2.9.5":                  false,
		"git version 1.9.1":                  false,
		"git version 3.0.0":                  true,
		"git version 2.39.5 (Apple Git-154)": true,
		"git version 2.45.2.windows.1":       true,
		"":                                   false,
		"not git output":                     false,
		"git version banana":                 false,
		"git version 2":                      false,
	} {
		if got := gitVersionHasEndOfOptions(out); got != want {
			t.Fatalf("gitVersionHasEndOfOptions(%q) = %v, want %v", out, got, want)
		}
	}
}

// The version probe must not silently drop the defence-in-depth flag on a Git
// that does have it: on 2.24+ clone, fetch and checkout all still carry
// --end-of-options. A recording shim captures the real argv.
func TestEnsureRepoPassesEndOfOptionsOnModernGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The recording shim is a POSIX /bin/sh script; Windows resolves `git`
		// through PATHEXT to git.exe and will not run an extensionless script.
		t.Skip("the git shim is a POSIX /bin/sh script")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	version, err := exec.CommandContext(t.Context(), realGit, "version").Output()
	if err != nil {
		t.Fatalf("git version: %v", err)
	}
	if !gitVersionHasEndOfOptions(string(version)) {
		t.Skipf("this git predates --end-of-options: %s", version)
	}

	upstream := newUpstreamRepo(t)
	shimDir := t.TempDir()
	log := filepath.Join(shimDir, "argv.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := filepath.Join(t.TempDir(), "clone")
	if _, err := ensureRepo(t.Context(), upstream, "main", dir, 1); err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	// Second call so the fetch/checkout branch runs too.
	if _, err := ensureRepo(t.Context(), upstream, "main", dir, 1); err != nil {
		t.Fatalf("ensureRepo on existing clone: %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read shim log: %v", err)
	}
	var sawClone, sawFetch, sawCheckout bool
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "--end-of-options") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "clone "):
			sawClone = true
		case strings.HasPrefix(line, "fetch "):
			sawFetch = true
		case strings.HasPrefix(line, "checkout "):
			sawCheckout = true
		}
	}
	if !sawClone || !sawFetch || !sawCheckout {
		t.Fatalf("--end-of-options reached clone=%v fetch=%v checkout=%v; argv log:\n%s", sawClone, sawFetch, sawCheckout, data)
	}
}

// benchGitOutput is benchGit for a command whose stdout is the point.
func benchGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// A pinned commit id must be recognised as an object id under either object
// format. looksLikeSHA decides whether ensureRepo may hand the ref to
// `git clone --branch`; a full SHA-256 id is 64 hex chars, so an upper bound of
// 40 turned it into a branch name and the clone died with "Remote branch <id>
// not found in upstream origin".
func TestLooksLikeSHAAcceptsBothObjectFormats(t *testing.T) {
	sha1ID := strings.Repeat("a1", 20)   // 40 hex, SHA-1
	sha256ID := strings.Repeat("b2", 32) // 64 hex, SHA-256
	for _, ref := range []string{"abcdef1", sha1ID, sha1ID[:12], sha256ID, sha256ID[:41]} {
		if !looksLikeSHA(ref) {
			t.Fatalf("looksLikeSHA(%q) (len %d) = false, want true", ref, len(ref))
		}
	}
	// Still not object ids: too short, too long, and non-hex branch names.
	for _, ref := range []string{"abcdef", strings.Repeat("c3", 33), "main", "release/v1", "deadbeefz"} {
		if looksLikeSHA(ref) {
			t.Fatalf("looksLikeSHA(%q) (len %d) = true, want false", ref, len(ref))
		}
	}
}

// End to end for the same defect: ensureRepo must clone and check out a ref
// pinned to a full SHA-256 commit id, the form a lock file records for a
// SHA-256 repository.
func TestEnsureRepoClonesPinnedSHA256CommitID(t *testing.T) {
	if out, err := exec.CommandContext(t.Context(), "git", "init", "--object-format=sha256", "-h").CombinedOutput(); err != nil && strings.Contains(string(out), "unknown option") {
		t.Skipf("this git has no --object-format: %s", out)
	}
	upstream := newUpstreamRepoWithObjectFormat(t, "sha256")
	pinned := benchGitOutput(t, upstream, "rev-parse", "HEAD")
	if len(pinned) != 64 {
		t.Fatalf("upstream HEAD = %q (len %d), want a 64-char SHA-256 id", pinned, len(pinned))
	}

	dir := filepath.Join(t.TempDir(), "clone")
	sha, err := ensureRepo(t.Context(), upstream, pinned, dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on a pinned SHA-256 commit id: %v", err)
	}
	if sha != pinned {
		t.Fatalf("resolved sha = %q, want the pinned %q", sha, pinned)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("pinned SHA-256 id did not produce a checkout: %v", statErr)
	}
}
