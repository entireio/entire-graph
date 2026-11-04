package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// statuslineRepo is a committed git repository holding one 4,000-byte Go file, a.go.
func statuslineRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "statusline-trunk")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	head := "package main\n\nfunc A() {}\n// "
	write(t, repo, "a.go", head+strings.Repeat("x", 4000-len(head)))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "one")
	return repo
}

// statuslineTranscript is one session whose only call is a graph query against repo, answered by
// a 400-byte result naming a.go.
func statuslineTranscript(t *testing.T, repo string) string {
	t.Helper()
	return writeGrossTranscript(t, repo, repo, grossResult(t, 400, "1. a.go:10 func A()"))
}

// statuslineStdin is the Claude Code status JSON. contextWindow is the raw `context_window` value,
// or "" to leave the key out.
func statuslineStdin(t *testing.T, transcript, projectDir, contextWindow string) string {
	t.Helper()
	fields := map[string]json.RawMessage{
		"transcript_path": json.RawMessage(strconv.Quote(transcript)),
		"workspace":       json.RawMessage(`{"project_dir":` + strconv.Quote(projectDir) + `}`),
	}
	if contextWindow != "" {
		fields["context_window"] = json.RawMessage(contextWindow)
	}
	return encodeLine(t, fields)
}

func runStatuslineForTest(t *testing.T, columns int, cacheDir, stdin string) string {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", strconv.Itoa(columns))
	var out, errOut bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader(stdin)},
		[]string{"statusline", "--cache-dir", cacheDir})
	if err != nil {
		t.Fatalf("statusline returned %v\nstderr: %s", err, errOut.String())
	}
	return out.String()
}

func statuslineLines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

func statuslineSegments(line string) []string {
	return strings.Split(line, " · ")
}

// assertSegmentsInOrder checks each wanted segment is present on the line, after the previous one.
func assertSegmentsInOrder(t *testing.T, line string, want ...string) {
	t.Helper()
	segments := statuslineSegments(line)
	next := 0
	for _, segment := range want {
		found := -1
		for index := next; index < len(segments); index++ {
			if segments[index] == segment {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("line %q lacks segment %q in order %q", line, segment, want)
		}
		next = found + 1
	}
}

// TestStatuslineShowsLookupsGrossNetAndContext pins the figures to what `stats` computes for the
// same transcript: 1 lookup, (4,000 - 400) / 4 = 900 gross tokens, and stats' own modeled balance.
func TestStatuslineShowsLookupsGrossNetAndContext(t *testing.T) {
	repo := statuslineRepo(t)
	transcript := statuslineTranscript(t, repo)
	out := runStatuslineForTest(t, 120, t.TempDir(),
		statuslineStdin(t, transcript, repo, `{"used_percentage":41.6}`))

	lines := statuslineLines(out)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), out)
	}
	if lines[1] != "▸ ctx 42%" {
		t.Fatalf("line 2 = %q, want %q", lines[1], "▸ ctx 42%")
	}
	report := runStatsRawJSON(t, "--repo", repo, "--transcript", transcript, "--format", "json",
		"--cache-dir", t.TempDir())
	wantNet := "net n/a"
	if rawInt(t, report, "sessions_with_savings_comparison") != 0 {
		wantNet = formatStatuslineTokens(rawInt(t, report, "modeled_balance_est_tokens")) + " net"
	}
	assertSegmentsInOrder(t, lines[0], "1 lookups", "~900 tok gross", wantNet)
}

func TestStatuslineShowsIndexedAfterIndex(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	var indexOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &indexOut, Stderr: &indexOut},
		[]string{"index", "--repo", repo, "--profile", "fast", "--cache-dir", cacheDir, "--format", "json"}); err != nil {
		t.Fatalf("index: %v\n%s", err, indexOut.String())
	}
	out := runStatuslineForTest(t, 120, cacheDir, statuslineStdin(t, statuslineTranscript(t, repo), repo, ""))
	lines := statuslineLines(out)
	if len(lines) == 0 || !strings.Contains(lines[0], "✓ indexed") {
		t.Fatalf("want line 1 to contain %q, got %q", "✓ indexed", out)
	}
}

func TestStatuslineReportsMissingIndex(t *testing.T) {
	repo := statuslineRepo(t)
	out := runStatuslineForTest(t, 120, t.TempDir(), statuslineStdin(t, statuslineTranscript(t, repo), repo, ""))
	lines := statuslineLines(out)
	if len(lines) == 0 || !strings.Contains(lines[0], "not indexed") {
		t.Fatalf("want line 1 to contain %q, got %q", "not indexed", out)
	}
}

// Windows PowerShell 5.1 prefixes piped text with a UTF-8 BOM, so `'<json>' | powershell -Command
// "entire graph statusline"` used to print nothing.
func TestStatuslineAcceptsAUTF8BOM(t *testing.T) {
	repo := statuslineRepo(t)
	stdin := "\xef\xbb\xbf" + statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	if lines := statuslineLines(runStatuslineForTest(t, 120, t.TempDir(), stdin)); len(lines) == 0 {
		t.Fatal("a BOM-prefixed status JSON rendered nothing")
	}
}

func TestStatuslinePrintsNothingOnBadInput(t *testing.T) {
	repo := statuslineRepo(t)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	for name, stdin := range map[string]string{
		"empty stdin":        "",
		"truncated json":     "{",
		"missing transcript": statuslineStdin(t, missing, repo, `{"used_percentage":10}`),
	} {
		if out := runStatuslineForTest(t, 120, t.TempDir(), stdin); out != "" {
			t.Errorf("%s: want empty stdout, got %q", name, out)
		}
	}
}

func TestStatuslineOmitsContextWhenNull(t *testing.T) {
	repo := statuslineRepo(t)
	out := runStatuslineForTest(t, 120, t.TempDir(),
		statuslineStdin(t, statuslineTranscript(t, repo), repo, `{"used_percentage":null}`))
	if lines := statuslineLines(out); len(lines) != 1 {
		t.Fatalf("want exactly 1 line, got %d: %q", len(lines), out)
	}
}

func TestStatuslineDropsWholeSegmentsToFit(t *testing.T) {
	repo := statuslineRepo(t)
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, `{"used_percentage":41.6}`)
	wide := statuslineLines(runStatuslineForTest(t, 120, t.TempDir(), stdin))
	narrow := statuslineLines(runStatuslineForTest(t, 40, t.TempDir(), stdin))
	if len(wide) == 0 || len(narrow) == 0 {
		t.Fatalf("want output at both widths, got wide %q narrow %q", wide, narrow)
	}
	known := map[string]bool{}
	for _, line := range wide {
		for _, segment := range statuslineSegments(line) {
			known[segment] = true
		}
	}
	for _, line := range narrow {
		if width := utf8.RuneCountInString(line); width > 40 {
			t.Errorf("line %q is %d columns wide, want <= 40", line, width)
		}
		for _, segment := range statuslineSegments(line) {
			if !known[segment] {
				t.Errorf("segment %q of %q is not a whole segment of the 120-column render %q", segment, line, wide)
			}
		}
	}
}

// statuslineIndex runs `entire graph index` for repo's HEAD into cacheDir.
func statuslineIndex(t *testing.T, repo, cacheDir string) {
	t.Helper()
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"index", "--repo", repo, "--profile", "fast", "--cache-dir", cacheDir, "--format", "json"}); err != nil {
		t.Fatalf("index: %v\n%s", err, out.String())
	}
}

// statuslineIndexedState renders and returns "✓ indexed" or "not indexed", failing on neither.
func statuslineIndexedState(t *testing.T, cacheDir, stdin string) string {
	t.Helper()
	out := runStatuslineForTest(t, 120, cacheDir, stdin)
	lines := statuslineLines(out)
	for _, state := range []string{"✓ indexed", "not indexed"} {
		if len(lines) > 0 && strings.Contains(lines[0], state) {
			return state
		}
	}
	t.Fatalf("line 1 shows no indexed state: %q", out)
	return ""
}

// statuslineCommit commits a new a.go, which changes HEAD's tree.
func statuslineCommit(t *testing.T, checkout, content string) {
	t.Helper()
	write(t, checkout, "a.go", "package main\n\nfunc A() {}\n// "+content+"\n")
	git(t, checkout, "add", "a.go")
	git(t, checkout, "commit", "-m", content)
}

// TestStatuslineWarmRenderRunsNoGit catches a warm render that still needs git: after two renders
// of an indexed HEAD, a render with no git anywhere on PATH must still report the indexed state. An
// empty PATH directory is used because Git for Windows ships git.exe in several directories.
func TestStatuslineWarmRenderRunsNoGit(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndex(t, repo, cacheDir)
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	statuslineIndexedState(t, cacheDir, stdin)
	statuslineIndexedState(t, cacheDir, stdin)

	t.Setenv("PATH", t.TempDir())
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "✓ indexed" {
		t.Fatalf("a warm render without git shows %q, want %q", got, "✓ indexed")
	}
}

// TestStatuslineNoticesKeyInputChanges catches a warm render that serves a stale "✓ indexed" after
// a change to something the indexed state depends on: the branch ref a commit moves, the GitHub
// origin URL in the repository key, the .graphignore policy, a ref reached through a symbolic-ref
// chain, and a commit inside a linked worktree.
func TestStatuslineNoticesKeyInputChanges(t *testing.T) {
	cases := []struct {
		name string
		// setup prepares the repository before it is indexed and returns the checkout to anchor on.
		setup  func(t *testing.T, repo string) string
		mutate func(t *testing.T, checkout string)
	}{
		{
			name:   "new commit",
			setup:  func(t *testing.T, repo string) string { return repo },
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
		},
		{
			name: "github origin url",
			setup: func(t *testing.T, repo string) string {
				git(t, repo, "remote", "add", "origin", "https://github.com/example/one.git")
				return repo
			},
			mutate: func(t *testing.T, checkout string) {
				git(t, checkout, "remote", "set-url", "origin", "https://github.com/example/two.git")
			},
		},
		{
			name:   "graphignore",
			setup:  func(t *testing.T, repo string) string { return repo },
			mutate: func(t *testing.T, checkout string) { write(t, checkout, ".graphignore", "a.go\n") },
		},
		{
			name: "symbolic-ref chain",
			setup: func(t *testing.T, repo string) string {
				git(t, repo, "symbolic-ref", "refs/heads/alias", "refs/heads/statusline-trunk")
				git(t, repo, "symbolic-ref", "HEAD", "refs/heads/alias")
				return repo
			},
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
		},
		{
			name: "linked worktree",
			setup: func(t *testing.T, repo string) string {
				worktree := filepath.Join(t.TempDir(), "linked")
				git(t, repo, "worktree", "add", "-b", "linked-branch", worktree)
				return worktree
			},
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkout := tc.setup(t, statuslineRepo(t))
			cacheDir := t.TempDir()
			statuslineIndex(t, checkout, cacheDir)
			stdin := statuslineStdin(t, statuslineTranscript(t, checkout), checkout, "")
			statuslineIndexedState(t, cacheDir, stdin)
			if got := statuslineIndexedState(t, cacheDir, stdin); got != "✓ indexed" {
				t.Fatalf("before the change the render shows %q, want %q", got, "✓ indexed")
			}
			tc.mutate(t, checkout)
			if got := statuslineIndexedState(t, cacheDir, stdin); got != "not indexed" {
				t.Fatalf("after the change the render shows %q, want %q", got, "not indexed")
			}
		})
	}
}

// TestStatuslineNoticesALaterIndex catches a cached "not indexed": an `index` at an unchanged HEAD
// must show on the next render.
func TestStatuslineNoticesALaterIndex(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	statuslineIndexedState(t, cacheDir, stdin)
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "not indexed" {
		t.Fatalf("before index the render shows %q, want %q", got, "not indexed")
	}
	statuslineIndex(t, repo, cacheDir)
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "✓ indexed" {
		t.Fatalf("after index the render shows %q, want %q", got, "✓ indexed")
	}
}
