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
	if len(sha) != 40 {
		t.Fatalf("resolved sha = %q, want a full commit id", sha)
	}
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
