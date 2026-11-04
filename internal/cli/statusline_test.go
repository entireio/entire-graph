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
