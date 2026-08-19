package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	for _, ref := range []string{
		"--upload-pack=touch x", "-o", "--exec=x", "\x00",
		// Refspec syntax: `git fetch origin <this>` is a write, not a read.
		"+refs/heads/evil:refs/heads/injected",
		"refs/heads/*:refs/remotes/origin/*",
		"main:main",
		"+main",
	} {
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
	// Git parses an object name case-insensitively, so the uppercase and
	// mixed-case spellings of the same id are object ids too.
	for _, ref := range []string{
		"abcdef1", sha1ID, sha1ID[:12], sha256ID, sha256ID[:41],
		"ABCDEF1", strings.ToUpper(sha1ID), strings.ToUpper(sha256ID), "aBcDeF1", "DeadBeef",
	} {
		if !looksLikeSHA(ref) {
			t.Fatalf("looksLikeSHA(%q) (len %d) = false, want true", ref, len(ref))
		}
	}
	// Still not object ids: too short, too long, and non-hex branch names in
	// either case.
	for _, ref := range []string{"abcdef", "ABCDEF", strings.Repeat("c3", 33), strings.ToUpper(strings.Repeat("c3", 33)), "main", "release/v1", "deadbeefz", "DEADBEEFZ"} {
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

// A branch may legitimately be named in lowercase hex — release automation and
// content-addressed tooling produce such names — and the object-id heuristic
// cannot tell a 41-to-64 character hex branch from an abbreviated or full
// SHA-256 id. When the heuristic guesses "object id", the fresh clone omits
// --branch, so the branch exists only in FETCH_HEAD afterwards and checkout by
// name fails. ensureRepo must still land on that branch's commit.
func TestEnsureRepoChecksOutHexShapedBranchNames(t *testing.T) {
	for _, name := range []string{
		strings.Repeat("ab", 20) + "c", // 41 chars: longer than SHA-1, shorter than SHA-256
		strings.Repeat("dc", 32),       // 64 chars: indistinguishable from a full SHA-256 id
	} {
		t.Run(strconv.Itoa(len(name)), func(t *testing.T) {
			// Pin the fixture's object format instead of inheriting the
			// caller's GIT_DEFAULT_HASH: in a SHA-256 repository a 64-hex
			// string *is* an object name, so git resolves the branch name to an
			// object id and no tool can address that branch by name there. The
			// case is only representable under SHA-1, which is also every
			// git older than 2.29, where this variable is simply ignored.
			t.Setenv("GIT_DEFAULT_HASH", "sha1")
			upstream := newUpstreamRepo(t)
			benchGit(t, upstream, "checkout", "--quiet", "-b", name)
			if err := os.WriteFile(filepath.Join(upstream, "branch.go"), []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			benchGit(t, upstream, "add", ".")
			benchGit(t, upstream, "commit", "--quiet", "-m", "on the hex-named branch")
			want := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
			benchGit(t, upstream, "checkout", "--quiet", "main")

			dir := filepath.Join(t.TempDir(), "clone")
			sha, err := ensureRepo(t.Context(), upstream, name, dir, 1)
			if err != nil {
				t.Fatalf("ensureRepo(%d-char hex branch): %v", len(name), err)
			}
			if sha != want {
				t.Fatalf("sha = %q, want the branch tip %q", sha, want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "branch.go")); statErr != nil {
				t.Fatalf("hex-named branch did not produce its checkout: %v", statErr)
			}
		})
	}
}

// A ref is not only option-shaped input, it is also refspec-shaped input: the
// ref lands in the positional slot of `git fetch <remote> <refspec>`, where a
// value such as `+refs/heads/evil:refs/heads/injected` is a *write*. That fetch
// succeeds, so the subsequent checkout-by-name is the only thing that fails,
// and the FETCH_HEAD fallback then reports success for a commit nobody asked
// for -- after the refspec has already created a ref inside the cached clone.
// ensureRepo must refuse refspec syntax before any git process runs.
func TestEnsureRepoRefusesRefspecShapedRef(t *testing.T) {
	upstream := newUpstreamRepo(t)
	benchGit(t, upstream, "checkout", "--quiet", "-b", "evil")
	if err := os.WriteFile(filepath.Join(upstream, "evil.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "on the evil branch")
	benchGit(t, upstream, "checkout", "--quiet", "main")

	dir := filepath.Join(t.TempDir(), "clone")
	// The realistic state: the repo is already in -cache from an earlier run,
	// so ensureRepo skips the clone and the ref reaches `git fetch` directly.
	want, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("benign pre-clone: %v", err)
	}

	for _, ref := range []string{
		"+refs/heads/evil:refs/heads/injected",
		"refs/heads/*:refs/remotes/origin/*",
	} {
		t.Run(ref, func(t *testing.T) {
			sha, err := ensureRepo(t.Context(), upstream, ref, dir, 1)
			if err == nil || !strings.Contains(err.Error(), "invalid git ref") {
				t.Fatalf("ensureRepo(%q) = %q, %v; want an invalid-git-ref rejection", ref, sha, err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "injected")); statErr == nil {
				t.Fatalf("refspec ref wrote refs/heads/injected into the cached clone")
			}
			head := strings.TrimSpace(benchGitOutput(t, dir, "rev-parse", "HEAD"))
			if head != want {
				t.Fatalf("cached clone HEAD = %q after a rejected ref, want the untouched %q", head, want)
			}
		})
	}
}

// A lowercase-hex ref is ambiguous: it can name an object *and* a branch in the
// same repository, and those can point at different commits. The object-id
// shape guess makes the two paths disagree -- `git clone --branch <name>`
// lands on the branch tip, while omitting --branch makes the later fetch and
// checkout resolve the same string as the object -- and the disagreement is
// silent, because resolving the object succeeds. ensureRepo must ask the remote
// which one it publishes instead of guessing.
//
// The fixture pins SHA-1 so the collision is representable: it needs a branch
// whose name is a full object id of the repository's own hash format.
func TestEnsureRepoPrefersRemoteBranchOverAmbiguousObjectID(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	upstream := newUpstreamRepo(t)
	first := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
	if len(first) != 40 {
		t.Skipf("upstream HEAD = %q, want a 40-char SHA-1 id (this git ignores GIT_DEFAULT_HASH)", first)
	}
	if err := os.WriteFile(filepath.Join(upstream, "branch.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "second")
	want := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))

	// A branch whose name is the first commit's object id, pointing at the
	// second commit. Both resolutions exist; only one is the branch.
	benchGit(t, upstream, "branch", first, want)

	dir := filepath.Join(t.TempDir(), "clone")
	sha, err := ensureRepo(t.Context(), upstream, first, dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo(ambiguous hex ref): %v", err)
	}
	if sha != want {
		t.Fatalf("sha = %q, want the branch tip %q (the object of the same name is %q)", sha, want, first)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "branch.go")); statErr != nil {
		t.Fatalf("ambiguous hex ref did not produce the branch's checkout: %v", statErr)
	}
}

// remoteRefFor is what stops ensureRepo guessing which of the two resolutions a
// hex-shaped ref means, so a failed lookup must not be reported as an answer.
// Collapsing a transport or authentication failure into "the remote publishes
// no such ref" reinstates the guess -- and the guess is then checked out as a
// success: with the object already in a warm cache, `git checkout <ref>`
// resolves it locally (verified against git 2.54.0: with no local ref of that
// name, an ambiguous 40-hex string detaches onto the object, and with one, git
// warns "refname is ambiguous" and takes the branch instead), so ensureRepo
// returned that commit with a nil error while the ref named a branch pointing
// somewhere else.
//
// Failing closed here costs no supported workflow: measuring an existing cache
// without a network is `-skip-clone`, which never reaches ensureRepo.
func TestEnsureRepoFailsWhenTheRemoteLookupFails(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	upstream := newUpstreamRepo(t)
	first := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
	if len(first) != 40 {
		t.Skipf("upstream HEAD = %q, want a 40-char SHA-1 id (this git ignores GIT_DEFAULT_HASH)", first)
	}

	// A warm cache from an earlier run, holding the object named `first`.
	dir := filepath.Join(t.TempDir(), "clone")
	if _, err := ensureRepo(t.Context(), upstream, "main", dir, 1); err != nil {
		t.Fatalf("pre-clone at main: %v", err)
	}

	// The remote now also publishes a branch of that name, pointing elsewhere.
	if err := os.WriteFile(filepath.Join(upstream, "branch.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "second")
	want := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
	benchGit(t, upstream, "branch", first, want)

	// ... and the lookup fails for a transport reason.
	if err := os.Rename(upstream, upstream+".unreachable"); err != nil {
		t.Fatal(err)
	}

	sha, err := ensureRepo(t.Context(), upstream, first, dir, 1)
	if err == nil {
		t.Fatalf("ensureRepo with an unreachable remote = %q, <nil>; want the lookup failure, not a checkout of the local object (the ref names a branch at %q)", sha, want)
	}
	if sha != "" {
		t.Fatalf("ensureRepo returned sha %q alongside the error %v; want no commit", sha, err)
	}
}

// The bound on the check above: the remote is consulted only for a hex-shaped
// ref, so an ordinary ref in a warm cache must still resolve with the remote
// down, exactly as it did before.
func TestEnsureRepoResolvesOrdinaryRefWithTheRemoteUnreachable(t *testing.T) {
	upstream := newUpstreamRepo(t)
	dir := filepath.Join(t.TempDir(), "clone")
	want, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("pre-clone at main: %v", err)
	}
	if err := os.Rename(upstream, upstream+".unreachable"); err != nil {
		t.Fatal(err)
	}
	sha, err := ensureRepo(t.Context(), upstream, "main", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on a warm cache with the remote down: %v", err)
	}
	if sha != want {
		t.Fatalf("sha = %q, want the cached tip %q", sha, want)
	}
}

// The FETCH_HEAD fallback exists for a legitimate case: a clone already in
// -cache from an earlier run has no local branch for a ref the manifest names
// now, so `git checkout <ref>` fails even though the fetch resolved it. The
// refspec guard and the single-entry FETCH_HEAD check must not close that path.
func TestEnsureRepoResolvesNewRefInCachedClone(t *testing.T) {
	upstream := newUpstreamRepo(t)
	benchGit(t, upstream, "checkout", "--quiet", "-b", "release")
	if err := os.WriteFile(filepath.Join(upstream, "release.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "on release")
	want := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
	benchGit(t, upstream, "checkout", "--quiet", "main")

	dir := filepath.Join(t.TempDir(), "clone")
	if _, err := ensureRepo(t.Context(), upstream, "main", dir, 1); err != nil {
		t.Fatalf("pre-clone at main: %v", err)
	}
	sha, err := ensureRepo(t.Context(), upstream, "release", dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo on a cached clone with a new ref: %v", err)
	}
	if sha != want {
		t.Fatalf("sha = %q, want the release tip %q", sha, want)
	}
}

// Git parses an object name case-insensitively: `git rev-parse`, `git cat-file`
// and `git checkout` all accept an uppercase or mixed-case id and answer with
// the lowercase one (verified against git 2.54.0). A lock file or manifest may
// therefore pin a perfectly valid uppercase commit id. Recognising only a-f
// classified it as a branch name, so the clone became `git clone --branch <id>`
// and died with "fatal: Remote branch <id> not found in upstream origin" -- the
// same failure as the SHA-256 width bug, from the same guess.
func TestEnsureRepoClonesPinnedUppercaseCommitID(t *testing.T) {
	mixedCase := func(s string) string {
		out := []byte(strings.ToLower(s))
		for i := 0; i < len(out); i += 2 {
			out[i] = strings.ToUpper(string(out[i]))[0]
		}
		return string(out)
	}
	for _, tc := range []struct {
		name      string
		upstream  func(t *testing.T) string
		transform func(string) string
	}{
		{"sha1-upper", func(t *testing.T) string { t.Setenv("GIT_DEFAULT_HASH", "sha1"); return newUpstreamRepo(t) }, strings.ToUpper},
		{"sha1-mixed", func(t *testing.T) string { t.Setenv("GIT_DEFAULT_HASH", "sha1"); return newUpstreamRepo(t) }, mixedCase},
		{"sha256-upper", func(t *testing.T) string {
			if out, err := exec.CommandContext(t.Context(), "git", "init", "--object-format=sha256", "-h").CombinedOutput(); err != nil && strings.Contains(string(out), "unknown option") {
				t.Skipf("this git has no --object-format: %s", out)
			}
			return newUpstreamRepoWithObjectFormat(t, "sha256")
		}, strings.ToUpper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := tc.upstream(t)
			pinned := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
			ref := tc.transform(pinned)
			if ref == pinned {
				t.Fatalf("fixture produced no case change: %q", ref)
			}
			// No assertion on looksLikeSHA here: the classifier has its own
			// table test, and letting this one run all the way to git makes
			// the observable the tool's real failure.
			dir := filepath.Join(t.TempDir(), "clone")
			sha, err := ensureRepo(t.Context(), upstream, ref, dir, 1)
			if err != nil {
				t.Fatalf("ensureRepo on a pinned %s commit id: %v", tc.name, err)
			}
			if !strings.EqualFold(sha, pinned) {
				t.Fatalf("resolved sha = %q, want the pinned %q", sha, pinned)
			}
			assertFullObjectID(t, sha)
			if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
				t.Fatalf("pinned %s id did not produce a checkout: %v", tc.name, statErr)
			}
		})
	}
}

// The control for the widening above: accepting A-F makes an uppercase-hex
// *branch* name look like an object id too, so this pins that such a branch is
// still reachable. It passes both before and after the widening -- before it,
// the name is classified as a branch and cloned with --branch; after it, the
// name is classified as an object id and remoteRefFor asks the remote, which
// publishes it as a branch. Either way ensureRepo must land on the branch tip.
func TestEnsureRepoChecksOutUppercaseHexShapedBranchName(t *testing.T) {
	name := strings.Repeat("AB", 20) // 40 chars: the shape of a full SHA-1 id
	upstream := newUpstreamRepo(t)
	benchGit(t, upstream, "checkout", "--quiet", "-b", name)
	if err := os.WriteFile(filepath.Join(upstream, "branch.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	benchGit(t, upstream, "add", ".")
	benchGit(t, upstream, "commit", "--quiet", "-m", "on the uppercase-hex branch")
	want := strings.TrimSpace(benchGitOutput(t, upstream, "rev-parse", "HEAD"))
	benchGit(t, upstream, "checkout", "--quiet", "main")

	dir := filepath.Join(t.TempDir(), "clone")
	sha, err := ensureRepo(t.Context(), upstream, name, dir, 1)
	if err != nil {
		t.Fatalf("ensureRepo(uppercase-hex branch): %v", err)
	}
	if sha != want {
		t.Fatalf("sha = %q, want the branch tip %q", sha, want)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "branch.go")); statErr != nil {
		t.Fatalf("uppercase-hex branch did not produce its working tree: %v", statErr)
	}
}
