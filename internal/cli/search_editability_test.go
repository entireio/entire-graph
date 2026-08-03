package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// TestParseSearchFlagsLeavesTheEditabilityLeversOff is the regression guard on the default payload:
// both levers must be inert unless asked for, because every measurement of the shipped payload was
// taken with them off.
func TestParseSearchFlagsLeavesTheEditabilityLeversOff(t *testing.T) {
	t.Parallel()
	flags, rest, err := parseSearchFlags([]string{"--query", "x"})
	if err != nil || len(rest) != 0 {
		t.Fatalf("parseSearchFlags err=%v rest=%v", err, rest)
	}
	if flags.FullUnitTop != 0 {
		t.Fatalf("FullUnitTop = %d, want 0 (today's behaviour)", flags.FullUnitTop)
	}
	if flags.EditSiteBodies {
		t.Fatal("EditSiteBodies is on by default")
	}
}

func TestParseSearchFlagsReadsTheEditabilityLevers(t *testing.T) {
	t.Parallel()
	flags, _, err := parseSearchFlags([]string{
		"--query", "x", "--full-unit-top", "2", "--edit-site-bodies",
	})
	if err != nil {
		t.Fatalf("parseSearchFlags: %v", err)
	}
	if flags.FullUnitTop != 2 {
		t.Fatalf("FullUnitTop = %d, want 2", flags.FullUnitTop)
	}
	if !flags.EditSiteBodies {
		t.Fatal("--edit-site-bodies did not take")
	}
	// 0 is meaningful (explicitly off), so the flag takes a non-negative int rather than a positive
	// one — and a negative value is still an error rather than a silent no-op.
	zero, _, err := parseSearchFlags([]string{"--query", "x", "--full-unit-top", "0"})
	if err != nil || zero.FullUnitTop != 0 {
		t.Fatalf("--full-unit-top 0: err=%v value=%d", err, zero.FullUnitTop)
	}
	if _, _, err := parseSearchFlags([]string{"--query", "x", "--full-unit-top", "-1"}); err == nil {
		t.Fatal("--full-unit-top -1 was accepted")
	}
	if _, _, err := parseSearchFlags([]string{"--query", "x", "--full-unit-top"}); err == nil {
		t.Fatal("--full-unit-top with no value was accepted")
	}
}

// TestSearchTextPrintsTheUnitElisionNoteAfterTheBody pins where the note goes. It is its OWN line
// after the source, never an inline marker inside it: agents copy body text verbatim as the
// `old_string` anchor of an edit, so decorating or interleaving the source turns a navigation aid
// into a broken patch (the same reason focus= rides in the header).
func TestSearchTextPrintsTheUnitElisionNoteAfterTheBody(t *testing.T) {
	t.Parallel()
	clipped := sem.SearchResult{
		Rank: 1, FilePath: "py/mpz.c", Score: 12, StartLine: 100, EndLine: 900, FocusLine: 152,
		SnippetStartLine: 100, SnippetEndLine: 499, Snippet: "int mpz_and_inpl(void) {\n  body;\n}",
		Signals: []string{sem.FullUnitSignal, "unit-elided"}, SymbolName: "mpz_and_inpl",
		UnitStartLine: 100, UnitEndLine: 900,
	}
	var out bytes.Buffer
	writeTextSearchResult(&out, clipped, true)
	rendered := out.String()

	note := "…elided lines 500–900 (unit continues)"
	if !strings.Contains(rendered, note) {
		t.Fatalf("render omits %q:\n%s", note, rendered)
	}
	body := strings.Index(rendered, "int mpz_and_inpl")
	if body < 0 || strings.Index(rendered, note) < body {
		t.Fatalf("the note precedes or replaces the body:\n%s", rendered)
	}
	for _, line := range strings.Split(clipped.Snippet, "\n") {
		if !strings.Contains(rendered, "\n"+line+"\n") {
			t.Fatalf("body line %q is not printed verbatim on its own line:\n%s", line, rendered)
		}
	}

	// A result that elided nothing prints no note, so the note's presence always means something.
	whole := clipped
	whole.UnitStartLine, whole.UnitEndLine = 0, 0
	whole.Signals = []string{sem.FullUnitSignal, sem.CompleteSymbolSignal}
	var clean bytes.Buffer
	writeTextSearchResult(&clean, whole, true)
	if strings.Contains(clean.String(), "elided") {
		t.Fatalf("an unclipped unit printed an elision note:\n%s", clean.String())
	}
}

// TestSearchTextKeepsSourceTheAllocatorPaidFor pins the renderer tier against the bug that made
// fmtlib__fmt-2457 lose its whole rank-3 window: the allocator spent 1,709 B widening that rank, the
// tier asked only "does it carry complete-symbol", and the answer for a WINDOW is no — so the rank
// came out as `include/fmt/ranges.h:682` and the bytes were charged for nothing.
//
// The test is "did the allocator deliberately widen this", and there are three ways it can have.
func TestSearchTextKeepsSourceTheAllocatorPaidFor(t *testing.T) {
	t.Parallel()
	base := sem.SearchResult{
		Rank: 3, FilePath: "include/fmt/ranges.h", Score: 33, StartLine: 652, EndLine: 712,
		FocusLine: 682, SnippetStartLine: 652, SnippetEndLine: 712,
		Snippet: "struct formatter {\n  // widened\n};",
	}
	for _, signal := range []string{sem.CompleteSymbolSignal, sem.FullUnitSignal, sem.HeadWindowSignal} {
		result := base
		result.Signals = []string{"body", signal}
		var out bytes.Buffer
		// full=false is the tier a rank past the second gets. A widened result must survive it.
		writeTextSearchResult(&out, result, false)
		if !strings.Contains(out.String(), "struct formatter") {
			t.Fatalf("signal %s: the widened source was discarded:\n%s", signal, out.String())
		}
	}
	// An ordinary un-widened window past the tier still collapses to a locator, which is the
	// behaviour the tier exists for.
	plain := base
	plain.Signals = []string{"body"}
	var out bytes.Buffer
	writeTextSearchResult(&out, plain, false)
	if strings.Contains(out.String(), "struct formatter") {
		t.Fatalf("an un-widened result past the tier printed its snippet:\n%s", out.String())
	}
}

// TestSearchCommandFullUnitTopReturnsTheWholeUnitEndToEnd runs the real command so the flag is pinned
// through the whole path — parser, options, planner, allocator, renderer — not just in the unit that
// implements it. The class here is the case the default payload cannot serve: searchEnclosableSymbolKind
// excludes containers, so without the flag the hit inside `Registry` comes back as a window.
func TestSearchCommandFullUnitTopReturnsTheWholeUnitEndToEnd(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "registry.py", `HEADER = 1


class Registry:
    """Every known transport, by name."""

    SFTP_TRANSPORT = "sftp"
    HTTP_TRANSPORT = "http"
    RSYNC_TRANSPORT = "rsync"
    UNIQUE_MARKER_LINE = "the line an edit has to replace"
    WEBDAV_TRANSPORT = "webdav"


def unrelated():
    return Registry.SFTP_TRANSPORT
`)

	run := func(extra ...string) string {
		var out bytes.Buffer
		args := append([]string{
			"search", "--repo", repo, "--query", "unique marker line transport registry",
			"--format", "text", "--profile", "syntax-only", "--worktree",
			"--top-k", "3", "--index-all-files", "--max-snippet-lines", "2",
		}, extra...)
		if err := Run(t.Context(), Options{
			Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out,
		}, args); err != nil {
			t.Fatalf("search %v: %v", extra, err)
		}
		return out.String()
	}

	forced := run("--full-unit-top", "1")
	if !strings.Contains(forced, sem.FullUnitSignal) {
		t.Fatalf("--full-unit-top 1 produced no full-unit signal:\n%s", forced)
	}
	// The whole class is present, so the edit anchor is in the payload verbatim.
	for _, want := range []string{"class Registry:", `UNIQUE_MARKER_LINE = "the line an edit has to replace"`} {
		if !strings.Contains(forced, want) {
			t.Fatalf("--full-unit-top 1 payload is missing %q:\n%s", want, forced)
		}
	}
	if strings.Contains(run(), sem.FullUnitSignal) {
		t.Fatal("the default payload carries a full-unit signal")
	}
}

// TestSearchCommandEditSiteBodiesIsOffByDefault pins the other lever end-to-end at the level that
// matters for the default payload: the block's rendered form must not change unless asked.
func TestSearchCommandEditSiteBodiesIsOffByDefault(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "codes.go", `package codes

const RetryBudgetExceeded = "retry_budget_exceeded"

func classify(code string) bool {
	return code == RetryBudgetExceeded
}
`)
	write(t, repo, "handler.go", `package codes

func handle(code string) string {
	if code == RetryBudgetExceeded {
		return "retry"
	}
	return "drop"
}
`)
	run := func(extra ...string) string {
		var out bytes.Buffer
		args := append([]string{
			"search", "--repo", repo, "--query", "retry budget exceeded classify",
			"--format", "text", "--profile", "syntax-only", "--worktree",
			"--top-k", "3", "--index-all-files",
		}, extra...)
		if err := Run(t.Context(), Options{
			Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out,
		}, args); err != nil {
			t.Fatalf("search %v: %v", extra, err)
		}
		return out.String()
	}
	// Whatever the ranker does with this fixture, the two payloads may differ ONLY in the literal
	// block — and the default one may never carry a ranged EDIT header, which is the form a site with
	// a body takes.
	base := run()
	if strings.Contains(base, sem.LiteralClusterBlockName) {
		for _, line := range strings.Split(base, "\n") {
			if strings.HasSuffix(line, " EDIT") && strings.Contains(line, "-") &&
				strings.HasPrefix(line, "  ") {
				t.Fatalf("the default literal block printed a ranged EDIT header: %q", line)
			}
		}
	}
	// And the flag must be accepted and change nothing else about the run.
	if got := run("--edit-site-bodies"); got == "" {
		t.Fatal("--edit-site-bodies produced no payload")
	}
}
