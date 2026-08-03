package cli

import (
	"bytes"
	"strconv"
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

func TestParseSearchFlagsLeavesTheCalleeHopOff(t *testing.T) {
	t.Parallel()
	off, _, err := parseSearchFlags([]string{"--query", "x"})
	if err != nil {
		t.Fatalf("parseSearchFlags: %v", err)
	}
	if off.CalleeHop {
		t.Fatal("--callee-hop is on by default")
	}
	on, _, err := parseSearchFlags([]string{"--query", "x", "--callee-hop"})
	if err != nil || !on.CalleeHop {
		t.Fatalf("--callee-hop did not take: err=%v value=%v", err, on.CalleeHop)
	}
}

// TestSearchTextPrintsNoScoreOnACalleeHop pins the header. A callee-hop entry was admitted by a CALLS
// edge, not by relevance, so it carries no ranked score — and `score=0.0000` beside a real fix site
// reads as "worthless" rather than "not applicable", which is exactly why the covering test is
// excluded from the score column too.
func TestSearchTextPrintsNoScoreOnACalleeHop(t *testing.T) {
	t.Parallel()
	hop := sem.SearchResult{
		Rank: 2, FilePath: "src/t_list.c", StartLine: 489, EndLine: 535, FocusLine: 489,
		SnippetStartLine: 489, SnippetEndLine: 535, Snippet: "void popGenericCommand(client *c) {\n}",
		SymbolName: "popGenericCommand", Kind: "function",
		Signals: []string{sem.CalleeHopSignal, sem.FullUnitSignal, sem.CompleteSymbolSignal},
	}
	var out bytes.Buffer
	writeTextSearchResult(&out, hop, true)
	rendered := out.String()
	if strings.Contains(rendered, "score=") {
		t.Fatalf("a callee hop printed a ranked score:\n%s", rendered)
	}
	for _, want := range []string{
		"2. src/t_list.c:489-535 symbol=popGenericCommand", sem.CalleeHopSignal, "void popGenericCommand",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render is missing %q:\n%s", want, rendered)
		}
	}
	// It also survives the tier a rank past the second gets — the entry was paid for, so it must
	// never come back as a bare locator.
	var terse bytes.Buffer
	writeTextSearchResult(&terse, hop, false)
	if !strings.Contains(terse.String(), "void popGenericCommand") {
		t.Fatalf("a callee hop past the full-snippet tier lost its body:\n%s", terse.String())
	}
}

// TestSearchCommandCalleeHopReachesTheCalledHelper runs the whole command on the measured shape: a
// thin entry point ranked, the gold inside the helper it calls, in a file with far more top-level
// members than searchRelatedUnitMemberLimit so the related block's co-member route is off.
func TestSearchCommandCalleeHopReachesTheCalledHelper(t *testing.T) {
	repo := t.TempDir()
	// The entry point first, the helper it delegates to at the far end of the file, and enough
	// unrelated members between them that (a) the co-member route switches off — its limit is
	// searchRelatedUnitMemberLimit — and (b) the span merge cannot bridge the two, which is the
	// arrangement the measured instance has (88 lines and ~40 functions apart in src/t_list.c).
	var source strings.Builder
	source.WriteString("package pop\n\n")
	source.WriteString("func lpopCommand() string {\n\treturn popGenericCommand(0)\n}\n\n")
	for filler := 0; filler < 40; filler++ {
		source.WriteString("func filler" + strconv.Itoa(filler) + "() int {\n\treturn " +
			strconv.Itoa(filler) + "\n}\n\n")
	}
	source.WriteString("// popGenericCommand does the work for every pop entry point.\n")
	source.WriteString("func popGenericCommand(where int) string {\n")
	source.WriteString("\tif where == 0 {\n")
	source.WriteString("\t\treturn sharedNullBulk // the line an edit has to replace\n")
	source.WriteString("\t}\n")
	source.WriteString("\treturn sharedNullArray\n")
	source.WriteString("}\n")
	write(t, repo, "pop.go", source.String())
	write(t, repo, "shared.go", "package pop\n\nvar sharedNullBulk = \"$-1\"\nvar sharedNullArray = \"*-1\"\n")

	run := func(extra ...string) string {
		var out bytes.Buffer
		args := append([]string{
			"search", "--repo", repo, "--query", "lpopCommand returns null bulk instead of null array",
			// --top-k 1 is the point of the test, not a convenience: on a two-file fixture the
			// ranker's own graph:calls expansion reaches the callee by itself, so the only way to
			// exercise THIS route is a ranking that has room for the entry point and nothing else —
			// which is also the real shape, where the expansion is candidate-limited and the callee
			// did not survive it.
			"--format", "text", "--profile", "full", "--worktree", "--top-k", "1",
			"--index-all-files", "--max-snippet-lines", "2",
		}, extra...)
		if err := Run(t.Context(), Options{
			Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out,
		}, args); err != nil {
			t.Fatalf("search %v: %v", extra, err)
		}
		return out.String()
	}

	hopped := run("--full-unit-top", "1", "--callee-hop")
	if !strings.Contains(hopped, sem.CalleeHopSignal) {
		t.Fatalf("--callee-hop admitted nothing:\n%s", hopped)
	}
	// The whole point: the line an edit replaces is in the payload verbatim.
	if !strings.Contains(hopped, "return sharedNullBulk // the line an edit has to replace") {
		t.Fatalf("the called helper's gold line is absent:\n%s", hopped)
	}
	if strings.Contains(run("--full-unit-top", "1"), sem.CalleeHopSignal) {
		t.Fatal("a callee hop appeared without --callee-hop")
	}
}
