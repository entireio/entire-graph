package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestHeadSnapshotLineReaderPreservesCommittedFileContract(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "tracked.go", "committed\r\nline\r\n")
	write(t, repo, "contains-nul.go", "before\x00after\n")
	write(t, repo, "oversized.go", strings.Repeat("x", callSiteMaxFileBytes+1))
	// A newline in a path is legal in Git and drives the one-shot `git show`
	// fallback, but Windows cannot create such a file at all. Everything else
	// this test pins — CRLF, NUL, the size ceiling, worktree isolation — is
	// cross-platform, so only the newline case is skipped rather than the test.
	newlinePaths := runtime.GOOS != "windows"
	if newlinePaths {
		write(t, repo, "line\nbreak.go", "committed-newline-path\n")
		write(t, repo, "trail.go\r", "committed-carriage-path\n")
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "committed reader fixture")

	write(t, repo, "tracked.go", "dirty\nline\n")
	if newlinePaths {
		write(t, repo, "line\nbreak.go", "dirty-newline-path\n")
		write(t, repo, "trail.go\r", "dirty-carriage-path\n")
	}
	write(t, repo, "dirty-only.go", "must not be visible from HEAD\n")

	read, closeReader, err := openSnapshotLineReader(t.Context(), sem.ProviderSnapshot{
		Header: sem.SnapshotHeader{RepoRoot: repo, Commit: rev(t, repo, "HEAD")},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if closeReader == nil {
		t.Fatal("committed reader has no close function")
	}
	defer func() {
		if closeReader != nil {
			_ = closeReader()
		}
	}()

	lines, ok := read("tracked.go")
	if !ok || strings.Join(lines, "|") != "committed|line|" {
		t.Fatalf("committed CRLF source = %q, ok=%v", lines, ok)
	}
	if newlinePaths {
		lines, ok = read("line\nbreak.go")
		if !ok || strings.Join(lines, "|") != "committed-newline-path|" {
			t.Fatalf("committed newline-path source = %q, ok=%v", lines, ok)
		}
		lines, ok = read("trail.go\r")
		if !ok || strings.Join(lines, "|") != "committed-carriage-path|" {
			t.Fatalf("committed trailing-CR path source = %q, ok=%v", lines, ok)
		}
	}
	for _, path := range []string{"dirty-only.go", "contains-nul.go", "oversized.go"} {
		if lines, ok := read(path); ok {
			t.Errorf("committed reader accepted %q: %q", path, lines)
		}
	}
	if err := closeReader(); err != nil {
		t.Fatalf("close committed reader: %v", err)
	}
	closeReader = nil
}

// A committed reader that cannot be opened must cost the answer its source
// windows, not the answer itself.
func TestSnapshotLineReaderDegradesWhenCommittedReaderCannotOpen(t *testing.T) {
	var warn bytes.Buffer
	read, closeReader := openSnapshotLineReaderOrDegrade(t.Context(), sem.ProviderSnapshot{
		Header: sem.SnapshotHeader{
			RepoRoot: filepath.Join(t.TempDir(), "not-a-directory"),
			Commit:   "0000000000000000000000000000000000000000",
		},
	}, false, &warn)
	if read == nil {
		t.Fatal("degraded reader is nil, but call-site annotation reads through it unconditionally")
	}
	if closeReader != nil {
		t.Fatal("degraded reader reported a close function for a process that never started")
	}
	if lines, ok := read("anything.go"); ok || lines != nil {
		t.Fatalf("degraded reader returned source: %q ok=%v", lines, ok)
	}
	if !strings.Contains(warn.String(), "committed source unavailable") {
		t.Fatalf("degradation was silent, so a missing source view looks like a file with no source: %q", warn.String())
	}
}

func TestDefSourceBodiesFollowSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)

	for _, format := range []string{"text", "agent"} {
		format := format
		t.Run(format, func(t *testing.T) {
			head := runSourceViewCommand(t, repo, cacheDir, "", "def", "--symbol", "InspectDefinition", "--head", "--format", format)
			requireSourceView(t, head,
				[]string{"committed-def-body"},
				[]string{"dirty-def-body"},
			)

			worktree := runSourceViewCommand(t, repo, cacheDir, "", "def", "--symbol", "InspectDefinition", "--format", format)
			requireSourceView(t, worktree,
				[]string{"dirty-def-body"},
				[]string{"committed-def-body"},
			)
		})
	}
}

func TestNeighborsSourceAnnotationsFollowSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)

	headCallSite := runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Target", "--direction", "in", "--head", "--format", "text")
	requireSourceView(t, headCallSite,
		[]string{"calls.go:7, def :5", "if committedGuard {", "committed-call-window"},
		[]string{"calls.go:8, def :5", "dirtyGuard", "dirty-before-call", "dirty-call-window"},
	)

	worktreeCallSite := runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Target", "--direction", "in", "--format", "text")
	requireSourceView(t, worktreeCallSite,
		[]string{"calls.go:8, def :5", "if dirtyGuard {", "dirty-before-call", "dirty-call-window"},
		[]string{"calls.go:7, def :5", "committedGuard", "committed-call-window"},
	)

	headMatches := runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Ambiguous", "--head", "--format", "text")
	requireSourceView(t, headMatches,
		[]string{"committed-alpha-body", "committed-beta-body"},
		[]string{"dirty-alpha-body", "dirty-beta-body"},
	)

	worktreeMatches := runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Ambiguous", "--format", "text")
	requireSourceView(t, worktreeMatches,
		[]string{"dirty-alpha-body", "dirty-beta-body"},
		[]string{"committed-alpha-body", "committed-beta-body"},
	)
}

// The JSON answer carries call_site, resolved by reading the caller's source, so
// the wired-up reader has to be right on the machine-readable path too — not
// only on the path the text renderer takes.
func TestNeighborsJSONCallSitesFollowSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)

	callSiteLines := func(output string) []int {
		t.Helper()
		var response neighborResponse
		if err := json.Unmarshal([]byte(output), &response); err != nil {
			t.Fatalf("decode neighbors json: %v\n%s", err, output)
		}
		var lines []int
		for _, match := range response.Matches {
			for _, edge := range match.Incoming {
				if edge.CallSite != nil {
					lines = append(lines, edge.CallSite.Line)
				}
			}
		}
		if len(lines) == 0 {
			t.Fatalf("no call site resolved, so the reader is untested:\n%s", output)
		}
		return lines
	}

	head := callSiteLines(runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Target", "--direction", "in", "--head", "--format", "json"))
	for _, line := range head {
		if line != 7 {
			t.Errorf("--head call site at line %d, want the committed line 7", line)
		}
	}

	worktree := callSiteLines(runSourceViewCommand(t, repo, cacheDir, "", "neighbors",
		"--symbol", "Target", "--direction", "in", "--format", "json"))
	for _, line := range worktree {
		if line != 8 {
			t.Errorf("working-tree call site at line %d, want the dirty line 8", line)
		}
	}
}

func TestImpactSourceAnnotationsFollowSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)

	headCallSite := runSourceViewCommand(t, repo, cacheDir, "", "impact",
		"--symbol", "Target", "--head", "--format", "text")
	requireSourceView(t, headCallSite,
		[]string{"calls.go:7, def :5"},
		[]string{"calls.go:8, def :5"},
	)

	worktreeCallSite := runSourceViewCommand(t, repo, cacheDir, "", "impact",
		"--symbol", "Target", "--format", "text")
	requireSourceView(t, worktreeCallSite,
		[]string{"calls.go:8, def :5"},
		[]string{"calls.go:7, def :5"},
	)

	headMatches := runSourceViewCommand(t, repo, cacheDir, "", "impact",
		"--symbol", "Ambiguous", "--head", "--format", "text")
	requireSourceView(t, headMatches,
		[]string{"committed-alpha-body", "committed-beta-body"},
		[]string{"dirty-alpha-body", "dirty-beta-body"},
	)

	worktreeMatches := runSourceViewCommand(t, repo, cacheDir, "", "impact",
		"--symbol", "Ambiguous", "--format", "text")
	requireSourceView(t, worktreeMatches,
		[]string{"dirty-alpha-body", "dirty-beta-body"},
		[]string{"committed-alpha-body", "committed-beta-body"},
	)
}

func TestExplainHeaderReportsSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)
	const buildFailure = "./definition.go:3:1: undefined: InspectDefinition\n"

	worktree := runSourceViewCommand(t, repo, cacheDir, buildFailure, "explain", "--no-echo", "--format", "text")
	if !strings.Contains(worktree, "from the working tree") {
		t.Fatalf("worktree explain output does not identify its source view:\n%s", worktree)
	}

	head := runSourceViewCommand(t, repo, cacheDir, buildFailure, "explain", "--no-echo", "--head", "--format", "text")
	if strings.Contains(head, "from the working tree") || !strings.Contains(strings.ToLower(head), "committed") {
		t.Fatalf("--head explain output does not identify its committed source view:\n%s", head)
	}
	if firstLine(head) == firstLine(worktree) {
		t.Fatalf("explain used the same provenance header for --head and worktree:\n%s", head)
	}
}

// The text header names the tree it read. A JSON consumer has no header to read,
// so the same fact has to be a field.
func TestExplainJSONReportsSelectedTree(t *testing.T) {
	repo, cacheDir := newDirtySourceViewRepo(t)
	const buildFailure = "./definition.go:3:1: undefined: InspectDefinition\n"

	decode := func(output string) ExplainResponse {
		t.Helper()
		var response ExplainResponse
		if err := json.Unmarshal([]byte(output), &response); err != nil {
			t.Fatalf("decode explain json: %v\n%s", err, output)
		}
		if len(response.Symbols) == 0 {
			t.Fatalf("explain resolved nothing, so provenance is untested:\n%s", output)
		}
		return response
	}

	worktree := decode(runSourceViewCommand(t, repo, cacheDir, buildFailure, "explain", "--no-echo", "--format", "json"))
	if !worktree.WorktreeSnapshot {
		t.Error("working-tree explain does not report worktree_snapshot")
	}

	head := decode(runSourceViewCommand(t, repo, cacheDir, buildFailure, "explain", "--no-echo", "--head", "--format", "json"))
	if head.WorktreeSnapshot {
		t.Error("--head explain reports its answer as working-tree")
	}
	if head.Commit == "" || head.Tree == "" {
		t.Errorf("--head explain omits its provenance: commit=%q tree=%q", head.Commit, head.Tree)
	}
	if head.Commit != rev(t, repo, "HEAD") {
		t.Errorf("--head explain reports commit %q, want HEAD %q", head.Commit, rev(t, repo, "HEAD"))
	}
}

func newDirtySourceViewRepo(t *testing.T) (repo, cacheDir string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")

	write(t, repo, "definition.go", `package fixture

func InspectDefinition() string {
	return "committed-def-body"
}
`)
	write(t, repo, "calls.go", `package fixture

func Target() {}

func Caller(committedGuard bool) {
	if committedGuard {
		Target()
	}
	_ = "committed-call-window"
}
`)
	write(t, repo, "alpha.go", `package fixture

func Ambiguous() string { return "committed-alpha-body" }
`)
	write(t, repo, "beta.go", `package fixture

func Ambiguous() string { return "committed-beta-body" }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "committed source view")

	write(t, repo, "definition.go", `package fixture

func InspectDefinition() string {
	return "dirty-def-body"
}
`)
	write(t, repo, "calls.go", `package fixture

func Target() {}

func Caller(dirtyGuard bool) {
	_ = "dirty-before-call"
	if dirtyGuard {
		Target()
	}
	_ = "dirty-call-window"
}
`)
	write(t, repo, "alpha.go", `package fixture

func Ambiguous() string { return "dirty-alpha-body" }
`)
	write(t, repo, "beta.go", `package fixture

func Ambiguous() string { return "dirty-beta-body" }
`)
	return repo, t.TempDir()
}

func runSourceViewCommand(t *testing.T, repo, cacheDir, stdin string, args ...string) string {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("runSourceViewCommand requires a command")
	}
	commandArgs := []string{args[0], "--repo", repo, "--cache-dir", cacheDir}
	commandArgs = append(commandArgs, args[1:]...)
	var stdout, stderr bytes.Buffer
	options := Options{
		Version: "0.1.0",
		Env: EntireEnv{
			RepoRoot:      repo,
			PluginDataDir: cacheDir,
		},
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if stdin != "" {
		options.Stdin = strings.NewReader(stdin)
	}
	if err := Run(t.Context(), options, commandArgs); err != nil {
		t.Fatalf("entire graph %s: %v\nstderr:\n%s\nstdout:\n%s", strings.Join(commandArgs, " "), err, stderr.String(), stdout.String())
	}
	return stdout.String()
}

func requireSourceView(t *testing.T, output string, wants, rejects []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing selected-tree source %q:\n%s", want, output)
		}
	}
	for _, reject := range rejects {
		if strings.Contains(output, reject) {
			t.Fatalf("output leaked other-tree source %q:\n%s", reject, output)
		}
	}
}

func firstLine(text string) string {
	if end := strings.IndexByte(text, '\n'); end >= 0 {
		return text[:end]
	}
	return text
}
