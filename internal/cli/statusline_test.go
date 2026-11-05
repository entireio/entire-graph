package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
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

// TestStatuslineShowsGrossNetAndContext pins the figures of an indexed HEAD to what `stats` computes
// for the same transcript: (4,000 - 400) / 4 = 900 gross tokens and stats' own modeled balance, with
// no net segment when stats has no comparison, after the graph's size as `index` reported it. It
// catches a filler segment returning (an indexed mark, the lookup count, "net n/a") and a size out
// of place.
func TestStatuslineShowsGrossNetAndContext(t *testing.T) {
	repo := statuslineRepo(t)
	cacheDir := t.TempDir()
	counts := statuslineIndexAs(t, repo, cacheDir, "full")
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
	want := "◤ entire graph · " + counts.segment() + " · ~900 tok gross"
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
	counts := statuslineIndexAs(t, repo, cacheDir, "full")
	transcript := writeGrossCommandTranscript(t, repo, "ls", "a.go")
	want := "◤ entire graph · " + counts.segment() + "\n"
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

// segment is the size segment the status line must show for these counts, written out here with
// its own thousands separators rather than through the code under test.
func (c statuslineCounts) segment() string {
	return statuslineThousands(c.Symbols) + " nodes | " + statuslineThousands(c.Relations) + " edges"
}

func statuslineThousands(value int) string {
	digits := strconv.Itoa(value)
	for cut := len(digits) - 3; cut > 0; cut -= 3 {
		digits = digits[:cut] + "," + digits[cut:]
	}
	return digits
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

// TestStatuslineShowsTheGraphSize catches a missing or abbreviated size, and counts that disagree
// with what `index` reported: the segment after the mark is the symbol and relation counts of
// `index --format json`, written in full with comma separators.
func TestStatuslineShowsTheGraphSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	counts := statuslineIndexAs(t, repo, cacheDir, "full")
	if counts.Symbols < 1000 {
		t.Fatalf("the fixture indexed %d symbols, want at least 1,000 to exercise the separator", counts.Symbols)
	}
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", counts.segment())
}

// TestStatuslinePrefersTheFullIndexSize catches a fast entry winning over full: with only a fast
// index the size is the fast entry's, and once a full index exists it is the full entry's.
func TestStatuslinePrefersTheFullIndexSize(t *testing.T) {
	repo := statuslineSizedRepo(t)
	cacheDir := t.TempDir()
	fast := statuslineIndexAs(t, repo, cacheDir, "fast")
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", fast.segment())
	full := statuslineIndexAs(t, repo, cacheDir, "full")
	if full.Relations == fast.Relations {
		t.Fatalf("the fixture has %d relations under both profiles, want them to differ", full.Relations)
	}
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", full.segment())
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
	assertSegmentsInOrder(t, line, "◤ entire graph", fast.segment())
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
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", counts.segment())
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
	assertSegmentsInOrder(t, statuslineLine1(t, repo, cacheDir), "◤ entire graph", counts.segment())
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
