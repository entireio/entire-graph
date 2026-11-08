package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// statuslineRepo is a committed git repository holding one 4,000-byte Go file, a.go. Its index is
// statuslineRepoSize: one node, func A, and one edge, a.go DEFINES A.
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

// statuslineRepoSize is the size segment statuslineRepo's index shows, derived by hand above.
const statuslineRepoSize = "1 nodes | 1 edges"

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

// TestStatuslineShowsGrossNetAndContext pins the figures of an indexed HEAD to what `stats` computes
// for the same transcript: (4,000 - 400) / 4 = 900 gross tokens and stats' own modeled balance, with
// no net segment when stats has no comparison, after the fixture's graph size. It
// catches a filler segment returning (an indexed mark, the lookup count, "net n/a") and a size out
// of place.
func TestStatuslineShowsGrossNetAndContext(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndexAs(t, repo, cacheDir, "full")
	transcript := statuslineTranscript(t, repo)
	out := runStatuslineForTest(t, 120, cacheDir,
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
	want := "◤ entire graph · " + statuslineRepoSize + " · ~900 tok gross"
	if rawInt(t, report, "sessions_with_savings_comparison") != 0 {
		want += " · " + formatStatuslineTokens(rawInt(t, report, "modeled_balance_est_tokens")) + " net"
	}
	if lines[0] != want {
		t.Fatalf("line 1 = %q, want %q", lines[0], want)
	}
}

// TestStatuslineShowsOnlyTheSizeWithNoLookups catches a segment shown with nothing to act on: an
// indexed HEAD and a session with no graph lookups render the product mark and the graph's size
// alone.
func TestStatuslineShowsOnlyTheSizeWithNoLookups(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndexAs(t, repo, cacheDir, "full")
	transcript := writeGrossCommandTranscript(t, repo, "ls", "a.go")
	want := "◤ entire graph · " + statuslineRepoSize + "\n"
	if out := runStatuslineForTest(t, 120, cacheDir, statuslineStdin(t, transcript, repo, "")); out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

// statuslineSizedRepo is a committed git repository holding one generated Go file, funcs.go: a type
// and 1,000 one-line functions, each but the last calling the next with that type, so the node
// count crosses 1,000 and the full profile records type relations the fast one does not.
func statuslineSizedRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "statusline-trunk")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	var source strings.Builder
	source.WriteString("package main\n\ntype T struct{}\n\n")
	for index := range 1000 {
		if index == 999 {
			source.WriteString("func F0999(t T) T { return t }\n")
			continue
		}
		source.WriteString("func F" + statuslineFourDigits(index) + "(t T) T { return F" + statuslineFourDigits(index+1) + "(t) }\n")
	}
	write(t, repo, "funcs.go", source.String())
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "one")
	return repo
}

func statuslineFourDigits(value int) string {
	text := strconv.Itoa(value)
	return strings.Repeat("0", 4-len(text)) + text
}

// statuslineCounts is what `entire graph index --format json` reported as counts.
type statuslineCounts struct {
	Symbols   int `json:"symbols"`
	Relations int `json:"relations"`
}

// assertSizeAfterMark fails unless the segment right after the mark on line is a size whose numbers,
// read back with their separators removed, are want. It compares numbers rather than re-spelling
// them, so no copy of the formatter is needed here: TestStatuslineShowsTheGraphSize pins the
// spelling to a literal.
func assertSizeAfterMark(t *testing.T, line string, want statuslineCounts) {
	t.Helper()
	segments := statuslineSegments(line)
	if len(segments) < 2 || segments[0] != "◤ entire graph" {
		t.Fatalf("line %q has no size segment after the mark", line)
	}
	var got statuslineCounts
	plain := strings.ReplaceAll(segments[1], ",", "")
	if _, err := fmt.Sscanf(plain, "%d nodes | %d edges", &got.Symbols, &got.Relations); err != nil ||
		plain != fmt.Sprintf("%d nodes | %d edges", got.Symbols, got.Relations) {
		t.Fatalf("line %q: segment %q after the mark is not a size", line, segments[1])
	}
	if got != want {
		t.Fatalf("line %q shows %d nodes and %d edges, want the %d and %d index reported",
			line, got.Symbols, got.Relations, want.Symbols, want.Relations)
	}
}

// statuslineLine1 renders with a lookup transcript and returns line 1.
func statuslineLine1(t *testing.T, repo, cacheDir string) string {
	t.Helper()
	out := runStatuslineForTest(t, 120, cacheDir, statuslineStdin(t, statuslineTranscript(t, repo), repo, ""))
	lines := statuslineLines(out)
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("the render printed no line 1: %q", out)
	}
	return lines[0]
}

// assertNoSizeSegment fails when any segment of line is a graph size.
func assertNoSizeSegment(t *testing.T, line string) {
	t.Helper()
	for _, segment := range statuslineSegments(line) {
		if strings.Contains(segment, " nodes | ") {
			t.Fatalf("line %q shows a size segment %q, want none", line, segment)
		}
	}
}

// TestStatuslineShowsTheGraphSize catches a missing or abbreviated size, a misplaced or missing
// thousands separator, and counts that disagree with the fixture: the segment after the mark is the
// full index's symbol and relation counts, written in full with comma separators.
//
// The fixture's full index, derived by hand from statuslineSizedRepo: 1,001 nodes are T and the
// 1,000 functions. 6,998 edges are 1,001 DEFINES (funcs.go defines each node), 999 CALLS
// (F0000-F0998 each call the next), 1,000 PARAM_TYPE and 1,000 RETURNS_TYPE (every function takes
// and returns T), 1,000 USES_TYPE (every function uses T), and 1,998 DATA_FLOWS (each of the 999
// calls forwards t into the callee's argument and returns the callee's result).
func TestStatuslineShowsTheGraphSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	statuslineIndexAs(t, repo, cacheDir, "full")
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", "1,001 nodes | 6,998 edges")
}

// TestStatuslinePrefersTheFullIndexSize catches a fast entry winning over full: with only a fast
// index the size is the fast entry's, and once a full index exists it is the full entry's.
func TestStatuslinePrefersTheFullIndexSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	fast := statuslineIndexAs(t, repo, cacheDir, "fast")
	assertSizeAfterMark(t, statuslineLine1(t, repo, cacheDir), fast)
	full := statuslineIndexAs(t, repo, cacheDir, "full")
	if full.Relations == fast.Relations {
		t.Fatalf("the fixture has %d relations under both profiles, want them to differ", full.Relations)
	}
	assertSizeAfterMark(t, statuslineLine1(t, repo, cacheDir), full)
}

// TestHeadQueryWritesTheSize catches a complete-entry writer without the counts helper: a cold
// `query --head --profile fast` builds the complete fast entry, and the next render shows the size
// that `index --profile fast` then reports for that entry.
func TestHeadQueryWritesTheSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"query", "--repo", repo, "--head", "--profile", "fast", "--cache-dir", cacheDir,
			"--format", "json", "--query", "F0500 calls F0501"}); err != nil {
		t.Fatalf("query: %v\n%s", err, out.String())
	}
	line := statuslineLine1(t, repo, cacheDir)
	fast := statuslineIndexAs(t, repo, cacheDir, "fast")
	assertSizeAfterMark(t, line, fast)
}

// TestHeadLookupWritesTheSize catches the cold lookup writer without the counts helper: a cold
// `def --head --profile fast` builds the complete fast entry, and the next render shows the size
// that `index --profile fast` then reports for that entry.
func TestHeadLookupWritesTheSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &out},
		[]string{"def", "--repo", repo, "--head", "--profile", "fast", "--cache-dir", cacheDir,
			"--format", "json", "F0500"}); err != nil {
		t.Fatalf("def: %v\n%s", err, out.String())
	}
	line := statuslineLine1(t, repo, cacheDir)
	fast := statuslineIndexAs(t, repo, cacheDir, "fast")
	assertSizeAfterMark(t, line, fast)
}

// TestStatuslineShowsTheSizeOnlyForTheCurrentIndex catches a size shown beside a stale state, where
// it would read as the current code's size: one commit after the index there is no size, and an
// index at the new HEAD brings it back.
func TestStatuslineShowsTheSizeOnlyForTheCurrentIndex(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	statuslineIndexAs(t, repo, cacheDir, "full")
	statuslineCommit(t, repo, "two")
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "behind 1" {
		t.Fatalf("one commit after the index the render shows %q, want behind 1", got)
	}
	assertNoSizeSegment(t, statuslineLine1(t, repo, cacheDir))
	counts := statuslineIndexAs(t, repo, cacheDir, "full")
	assertSizeAfterMark(t, statuslineLine1(t, repo, cacheDir), counts)
}

// TestIndexBackfillsTheSizeOnAHit catches an `index` hit that never writes the counts file, and a
// render that keeps a stale "no size": with the counts files deleted the render shows no size, and
// after a second `index` (a hit) it shows that run's counts.
func TestIndexBackfillsTheSizeOnAHit(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	statuslineIndexAs(t, repo, cacheDir, "full")
	removed := 0
	if err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".counts.json") {
			removed++
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		t.Fatalf("remove the counts files: %v", err)
	}
	if removed == 0 {
		t.Fatal("index wrote no *.counts.json file to remove")
	}
	assertNoSizeSegment(t, statuslineLine1(t, repo, cacheDir))
	counts := statuslineIndexAs(t, repo, cacheDir, "full")
	assertSizeAfterMark(t, statuslineLine1(t, repo, cacheDir), counts)
}

// TestStatuslineShowsHowFarBehindTheIndexIs catches a stale index falling back to "not indexed", and
// a stale state that stays cached after `index` catches up.
func TestStatuslineShowsHowFarBehindTheIndexIs(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndex(t, repo, cacheDir)
	statuslineCommit(t, repo, "two")
	statuslineCommit(t, repo, "three")
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	lines := statuslineLines(runStatuslineForTest(t, 120, cacheDir, stdin))
	if want := "index 2 commits behind · run entire graph index"; len(lines) == 0 || !strings.Contains(lines[0], want) {
		t.Fatalf("line 1 = %q, want it to contain %q", lines, want)
	}
	statuslineIndex(t, repo, cacheDir)
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "current" {
		t.Fatalf("after index the render shows %q, want current", got)
	}
}

// TestStatuslineSaysNotIndexedBeyondTheAncestorBound catches a walk one commit short: an index five
// commits back (sem.MaxStoreContextAncestorDistance) is found, one six back is not.
func TestStatuslineSaysNotIndexedBeyondTheAncestorBound(t *testing.T) {
	for _, tc := range []struct {
		commits int
		want    string
	}{
		{commits: 5, want: "behind 5"},
		{commits: 6, want: "not indexed"},
	} {
		t.Run(strconv.Itoa(tc.commits), func(t *testing.T) {
			repo := statuslineRepo(t)
			cacheDir := t.TempDir()
			statuslineIndex(t, repo, cacheDir)
			for index := range tc.commits {
				statuslineCommit(t, repo, "commit-"+strconv.Itoa(index))
			}
			stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
			if got := statuslineIndexedState(t, cacheDir, stdin); got != tc.want {
				t.Fatalf("%d commits after the index the render shows %q, want %q", tc.commits, got, tc.want)
			}
		})
	}
}

// TestStatuslinePrintsNothingOutsideARepo catches a status line that claims "not indexed" for a
// directory that is not a checkout at all.
func TestStatuslinePrintsNothingOutsideARepo(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "plain")
	write(t, dir, "a.go", "package main\n")
	t.Setenv("GIT_CEILING_DIRECTORIES", parent)
	if out := runStatuslineForTest(t, 120, t.TempDir(), statuslineStdin(t, statuslineTranscript(t, dir), dir, "")); out != "" {
		t.Fatalf("stdout = %q, want empty", out)
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

// statuslineIndex runs `entire graph index --profile fast` for repo's HEAD into cacheDir.
func statuslineIndex(t *testing.T, repo, cacheDir string) {
	t.Helper()
	statuslineIndexAs(t, repo, cacheDir, "fast")
}

// statuslineIndexAs runs `entire graph index --profile <profile> --format json` for repo's HEAD into
// cacheDir and returns the counts it reported.
func statuslineIndexAs(t *testing.T, repo, cacheDir, profile string) statuslineCounts {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Stdout: &out, Stderr: &errOut},
		[]string{"index", "--repo", repo, "--profile", profile, "--cache-dir", cacheDir, "--format", "json"}); err != nil {
		t.Fatalf("index: %v\n%s%s", err, out.String(), errOut.String())
	}
	var response struct {
		Counts *statuslineCounts `json:"counts"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Counts == nil {
		t.Fatalf("index --format json printed no counts (%v): %s", err, out.String())
	}
	return *response.Counts
}

// statuslineIndexedState renders and classifies line 1: "not indexed", "behind N" for an
// `index N commit(s) behind` segment, or "current" for a line with neither. Empty output fails.
func statuslineIndexedState(t *testing.T, cacheDir, stdin string) string {
	t.Helper()
	out := runStatuslineForTest(t, 120, cacheDir, stdin)
	lines := statuslineLines(out)
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("the render printed no line 1: %q", out)
	}
	for _, segment := range statuslineSegments(lines[0]) {
		if segment == "not indexed" {
			return segment
		}
		count, ok := strings.CutPrefix(segment, "index ")
		if !ok {
			continue
		}
		for _, suffix := range []string{" commits behind", " commit behind"} {
			if number, cut := strings.CutSuffix(count, suffix); cut {
				if (number == "1") != (suffix == " commit behind") {
					t.Fatalf("segment %q pluralises wrongly", segment)
				}
				return "behind " + number
			}
		}
	}
	return "current"
}

// statuslineCommit commits a new a.go, which changes HEAD's tree.
func statuslineCommit(t *testing.T, checkout, content string) {
	t.Helper()
	write(t, checkout, "a.go", "package main\n\nfunc A() {}\n// "+content+"\n")
	git(t, checkout, "add", "a.go")
	git(t, checkout, "commit", "-m", content)
}

// TestStatuslineWarmRenderRunsNoGit catches a warm render that still needs git: after two renders
// of an indexed HEAD, a render with no git anywhere on PATH must still report the current state. An
// empty PATH directory is used because Git for Windows ships git.exe in several directories.
func TestStatuslineWarmRenderRunsNoGit(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndex(t, repo, cacheDir)
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	statuslineIndexedState(t, cacheDir, stdin)
	statuslineIndexedState(t, cacheDir, stdin)

	t.Setenv("PATH", t.TempDir())
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "current" {
		t.Fatalf("a warm render without git shows %q, want current", got)
	}
}

// TestStatuslineNoticesKeyInputChanges catches a warm render that serves a stale current state after
// a change to something the indexed state depends on: the branch ref a commit moves, the GitHub
// origin URL in the repository key, the .graphignore policy, a ref reached through a symbolic-ref
// chain, and a commit inside a linked worktree. A commit leaves the index one commit behind; an
// origin or .graphignore change addresses other entries, so nothing is indexed.
func TestStatuslineNoticesKeyInputChanges(t *testing.T) {
	cases := []struct {
		name string
		// setup prepares the repository before it is indexed and returns the checkout to anchor on.
		setup  func(t *testing.T, repo string) string
		mutate func(t *testing.T, checkout string)
		want   string
	}{
		{
			name:   "new commit",
			setup:  func(t *testing.T, repo string) string { return repo },
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
			want:   "behind 1",
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
			want: "not indexed",
		},
		{
			name:   "graphignore",
			setup:  func(t *testing.T, repo string) string { return repo },
			mutate: func(t *testing.T, checkout string) { write(t, checkout, ".graphignore", "a.go\n") },
			want:   "not indexed",
		},
		{
			name: "symbolic-ref chain",
			setup: func(t *testing.T, repo string) string {
				git(t, repo, "symbolic-ref", "refs/heads/alias", "refs/heads/statusline-trunk")
				git(t, repo, "symbolic-ref", "HEAD", "refs/heads/alias")
				return repo
			},
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
			want:   "behind 1",
		},
		{
			name: "linked worktree",
			setup: func(t *testing.T, repo string) string {
				worktree := filepath.Join(t.TempDir(), "linked")
				git(t, repo, "worktree", "add", "-b", "linked-branch", worktree)
				return worktree
			},
			mutate: func(t *testing.T, checkout string) { statuslineCommit(t, checkout, "two") },
			want:   "behind 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkout := tc.setup(t, statuslineRepo(t))
			cacheDir := t.TempDir()
			statuslineIndex(t, checkout, cacheDir)
			stdin := statuslineStdin(t, statuslineTranscript(t, checkout), checkout, "")
			statuslineIndexedState(t, cacheDir, stdin)
			if got := statuslineIndexedState(t, cacheDir, stdin); got != "current" {
				t.Fatalf("before the change the render shows %q, want current", got)
			}
			tc.mutate(t, checkout)
			if got := statuslineIndexedState(t, cacheDir, stdin); got != tc.want {
				t.Fatalf("after the change the render shows %q, want %q", got, tc.want)
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
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "current" {
		t.Fatalf("after index the render shows %q, want current", got)
	}
}

// BenchmarkStatuslineAppendRender is Task S1's gate: one render after one appended graph query
// and its result, on a transcript of about 50 MB whose earlier render is cached. Every assistant
// message pays this.
//
//	go test ./internal/cli -run '^$' -bench BenchmarkStatuslineAppendRender -benchtime 20x -count 5
func BenchmarkStatuslineAppendRender(b *testing.B) {
	b.Setenv("NO_COLOR", "1")
	b.Setenv("COLUMNS", "120")
	repo := b.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "bench"},
		{"-c", "user.name=bench", "-c", "user.email=bench@example.com", "-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", "one"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	command := "entire graph query --repo " + filepath.ToSlash(repo) + " --query 'where is A'"
	result := "1. a.go:10 func A()\n// " + strings.Repeat("p", 2048)
	pair := func(id int) []byte {
		var out []byte
		toolID := "toolu_" + strconv.Itoa(id)
		for _, record := range []map[string]any{
			{"type": "assistant", "cwd": repo, "message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": toolID, "name": "Bash", "input": map[string]any{"command": command}}}}},
			{"type": "user", "cwd": repo, "message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": result}}}},
		} {
			line, err := json.Marshal(record)
			if err != nil {
				b.Fatal(err)
			}
			out = append(append(out, line...), '\n')
		}
		return out
	}
	transcript := filepath.Join(b.TempDir(), "bench-session.jsonl")
	var body []byte
	for id := 0; len(body) < 50<<20; id++ {
		body = append(body, pair(id)...)
	}
	if err := os.WriteFile(transcript, body, 0o644); err != nil {
		b.Fatal(err)
	}
	stdin := `{"transcript_path":` + strconv.Quote(transcript) + `,"workspace":{"project_dir":` + strconv.Quote(repo) + `}}`
	cacheDir := b.TempDir()
	render := func() {
		var out bytes.Buffer
		if err := Run(b.Context(), Options{Version: "bench", Stdout: &out, Stderr: &out, Stdin: strings.NewReader(stdin)},
			[]string{"statusline", "--cache-dir", cacheDir}); err != nil || out.Len() == 0 {
			b.Fatalf("render: %v %q", err, out.String())
		}
	}
	render()
	handle, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer handle.Close()
	b.ResetTimer()
	for index := range b.N {
		if _, err := handle.Write(pair(1_000_000 + index)); err != nil {
			b.Fatal(err)
		}
		render()
	}
}

// appendTranscriptText appends text to the file at path.
func appendTranscriptText(t *testing.T, path, text string) {
	t.Helper()
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString(text); err != nil {
		_ = handle.Close()
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestStatuslineResumedRenderMatchesAFreshOne catches a render resumed after an append that drifts
// from a full parse. A 1 MiB filler record puts the transcript over the size from which renders
// resume; the transcript then grows a part at a time, and after every write the render must print
// what a render with an empty cache directory prints, and `stats --transcript` served from the
// summary that render stored must report the follow-up classes a fresh `stats` reports. The
// session: a Grep and its result, a graph query run after `cd sub` (sub/a.go is 2,000 bytes, a.go
// 4,000) with its result in two writes, the query's tool_use again (a re-serialised turn, one
// call), five unrelated calls that close the query's follow-up window with no follow-up, then two
// parallel graph queries, a text answer naming a.go and then, in a later write, a JSON answer
// delivering a.go's lines 1-3, a Read of a.go's line 2 that both answers' windows hold, and one
// assistant message whose usage is written twice. The read goes to the later-delivered JSON answer
// (overlapping; the text answer's read is shared), so a checkpoint that lost the delivery count, the
// delivered spans or the read's span reports another read class. The writes split a tool_use from
// its result, a line in two, and the follow-up window, so a checkpoint that lost a pending call, the
// call's directory, the tool_use ids already seen or the timeline prints other figures. Then the
// transcript is rewritten in place with other, longer content, and truncated, and both must still
// match.
func TestStatuslineResumedRenderMatchesAFreshOne(t *testing.T) {
	repo := statuslineRepo(t)
	write(t, repo, "sub/a.go", strings.Repeat("s", 2000))
	record := func(recordType string, block map[string]any) string {
		return grossRecord(t, recordType, repo, []any{block}) + "\n"
	}
	toolUse := func(id, name string, input map[string]any) string {
		return record("assistant", map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
	}
	toolResult := func(id, content string) string {
		return record("user", map[string]any{"type": "tool_result", "tool_use_id": id, "content": content})
	}
	usage := func(output int) string {
		return encodeLine(t, map[string]any{
			"type": "assistant", "timestamp": statsTime(0), "cwd": repo, "sessionId": "gross-session",
			"message": map[string]any{
				"id": "msg_resume", "role": "assistant",
				"usage":   map[string]any{"input_tokens": 100, "output_tokens": output},
				"content": []any{map[string]any{"type": "text", "text": "reading"}},
			},
		}) + "\n"
	}
	session := func(resultBytes int) []string {
		query := toolUse("toolu_q", "Bash", map[string]any{"command": "cd sub && entire graph query --repo . --query 'where is A'"})
		result := toolResult("toolu_q", grossResult(t, resultBytes, "1. a.go:10 func A()"))
		parts := []string{
			toolUse("toolu_g", "Grep", map[string]any{"pattern": "A"}) + toolResult("toolu_g", strings.Repeat("g", 200)),
			query,
			result[:len(result)/2], result[len(result)/2:],
			query,
		}
		for index := range followUpWindow {
			id := "toolu_o" + strconv.Itoa(index)
			parts = append(parts, toolUse(id, "Bash", map[string]any{"command": "go vet ./..."})+toolResult(id, "ok"))
		}
		delivered, err := json.Marshal(map[string]any{"results": []any{map[string]any{
			"file_path": "a.go", "snippet_start_line": 1, "snippet_end_line": 3, "snippet": "package main\n\nfunc A() {}",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts,
			toolUse("toolu_t", "Bash", map[string]any{"command": "entire graph query --repo . --query 'who calls A'"})+
				toolUse("toolu_j", "Bash", map[string]any{"command": "entire graph query --repo . --query 'A' --format json"}),
			toolResult("toolu_t", grossResult(t, resultBytes/2, "1. a.go:3 func A()")),
			toolResult("toolu_j", string(delivered)),
			toolUse("toolu_r", "Read", map[string]any{"file_path": filepath.Join(repo, "a.go"), "offset": 2, "limit": 1})+
				toolResult("toolu_r", "func A() {}"),
		)
		return append(parts, usage(50), usage(5))
	}
	filler := record("user", map[string]any{"type": "text", "text": strings.Repeat("f", 1<<20)})
	dir := t.TempDir()
	transcript := filepath.Join(dir, "resume-session.jsonl")
	cacheDir := t.TempDir()
	followUpClasses := func(cache string) string {
		report := runStatsRawJSON(t, "--repo", repo, "--transcript", transcript, "--format", "json",
			"--since", "all", "--cache-dir", cache)
		var classes []string
		for _, key := range []string{
			"graph_locate_calls", "graph_locate_no_follow_up", "graph_locate_requery",
			"graph_locate_follow_up_read_overlapping", "graph_locate_follow_up_read_different_region",
			"graph_locate_follow_up_read_unknown_span", "graph_locate_follow_up_read_shared",
			"graph_locate_censored", "graph_locate_error", "graph_locate_ineligible",
		} {
			classes = append(classes, strings.TrimPrefix(key, "graph_locate_")+"="+strconv.FormatInt(rawInt(t, report, key), 10))
		}
		return strings.Join(classes, " ")
	}
	assertMatchesFresh := func(step string) {
		t.Helper()
		stdin := statuslineStdin(t, transcript, repo, "")
		got := runStatuslineForTest(t, 120, cacheDir, stdin)
		if want := runStatuslineForTest(t, 120, t.TempDir(), stdin); got != want {
			t.Fatalf("%s: the resumed render printed %q, a fresh one %q", step, got, want)
		}
		if got, want := followUpClasses(cacheDir), followUpClasses(t.TempDir()); got != want {
			t.Fatalf("%s: stats served from the resumed render's summary reports %s, a fresh one %s", step, got, want)
		}
	}

	write(t, dir, "resume-session.jsonl", filler)
	assertMatchesFresh("filler only")
	for index, part := range session(400) {
		appendTranscriptText(t, transcript, part)
		assertMatchesFresh("after part " + strconv.Itoa(index))
	}
	write(t, dir, "resume-session.jsonl", filler+strings.Join(session(600), ""))
	assertMatchesFresh("rewritten in place, longer")
	write(t, dir, "resume-session.jsonl", filler+session(400)[0])
	assertMatchesFresh("truncated")
}

// TestStatuslineShowsNoIndexStateWithoutACommit catches a remedy that cannot work: in a repository
// with no commit yet `entire graph index` has nothing to index, so line 1 says neither "not
// indexed" nor "run entire graph index", on the cold render and on the warm one served from the
// git record, and still shows the session's figures.
func TestStatuslineShowsNoIndexStateWithoutACommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-b", "statusline-trunk")
	head := "package main\n\nfunc A() {}\n// "
	write(t, repo, "a.go", head+strings.Repeat("x", 4000-len(head)))
	cacheDir := t.TempDir()
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	for _, render := range []string{"cold", "warm"} {
		lines := statuslineLines(runStatuslineForTest(t, 120, cacheDir, stdin))
		if len(lines) == 0 {
			t.Fatalf("%s render printed nothing", render)
		}
		segments := statuslineSegments(lines[0])
		if segments[0] != "◤ entire graph" {
			t.Fatalf("%s render: line 1 = %q, want it to start with the mark", render, lines[0])
		}
		for _, segment := range segments {
			if segment == "not indexed" || segment == "run entire graph index" {
				t.Fatalf("%s render: line 1 = %q shows %q with no commit to index", render, lines[0], segment)
			}
		}
		assertSegmentsInOrder(t, lines[0], "◤ entire graph", "~900 tok gross")
	}
}

// cacheFileTimes maps every regular file under dir, by its path relative to dir, to its mtime.
func cacheFileTimes(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	files := map[string]time.Time{}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[relative] = info.ModTime()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

// TestStatuslineSweepsCacheFilesUnusedForAWeek catches a status line cache that only grows: after
// every file in the cache directory is aged by eight days, the next render (another session)
// leaves no file a render wrote at that age, removes nothing `index` wrote, and still reads the
// index.
func TestStatuslineSweepsCacheFilesUnusedForAWeek(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	statuslineIndex(t, repo, cacheDir)
	index := cacheFileTimes(t, cacheDir)
	runStatuslineForTest(t, 120, cacheDir, statuslineStdin(t, statuslineTranscript(t, repo), repo, ""))
	weekAgo := time.Now().Add(-8 * 24 * time.Hour)
	written := 0
	for path := range cacheFileTimes(t, cacheDir) {
		if _, ok := index[path]; !ok {
			written++
		}
		if err := os.Chtimes(filepath.Join(cacheDir, path), weekAgo, weekAgo); err != nil {
			t.Fatal(err)
		}
	}
	if written == 0 {
		t.Fatal("the render wrote no cache file to age")
	}
	stdin := statuslineStdin(t, statuslineTranscript(t, repo), repo, "")
	if got := statuslineIndexedState(t, cacheDir, stdin); got != "current" {
		t.Fatalf("a render over a week-old cache shows %q, want current", got)
	}
	for path, modTime := range cacheFileTimes(t, cacheDir) {
		if _, ok := index[path]; !ok && modTime.Before(weekAgo.Add(time.Hour)) {
			t.Errorf("%s went a week unused and survived the sweep", path)
		}
	}
	for path := range index {
		if _, err := os.Stat(filepath.Join(cacheDir, path)); err != nil {
			t.Errorf("the sweep removed %s, which index wrote: %v", path, err)
		}
	}
}
