package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// TestWriteSymbolMatchBodiesNumbersEveryLine pins the gutter. `def`/`callers`/`neighbors` answer "where
// is it", and the measured failure is navigational: carbon's agent got an unnumbered body, could not
// tell which line was which, piped it through `head -80` — which cut at the bug line — and spent 87
// turns grepping. The SEARCH payload's bodies stay unnumbered, because an agent copies those verbatim as
// an Edit anchor and a gutter breaks the anchor.
func TestWriteSymbolMatchBodiesNumbersEveryLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writeSymbolMatchBodies(&out, []symbolMatchBody{{
		Name: "Comparison.isLongYear", Kind: "method", FilePath: "src/Comparison.php",
		StartLine: 583, EndLine: 586,
		Source: "public function isLongYear()\n{\n    return $this->weekOfYear === 53;\n}",
	}})
	rendered := out.String()
	for _, want := range []string{
		"src/Comparison.php:583-586 Comparison.isLongYear [method]",
		"  583→ public function isLongYear()",
		"  584→ {",
		"  585→     return $this->weekOfYear === 53;",
		"  586→ }",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
	// The gutter is right-aligned on the widest number, so the code column does not ratchet.
	var wide bytes.Buffer
	writeNumberedSource(&wide, "a\nb", 999)
	if !strings.Contains(wide.String(), "   999→ a\n  1000→ b\n") {
		t.Fatalf("gutter is not right-aligned:\n%q", wide.String())
	}
}

// TestSymbolMatchBodiesNeverStopMidSymbolSilently pins the cap and its resume note: 400 lines, and when
// a body is longer the note says where it continues AND the invocation that resumes it. "Output
// truncated" with no coordinates is what sent carbon to grep.
func TestSymbolMatchBodiesNeverStopMidSymbolSilently(t *testing.T) {
	t.Parallel()
	if symbolMatchBodyMaxLines < 400 {
		t.Fatalf("cap is %d; a navigation answer that stops at 40 lines looks complete and is not",
			symbolMatchBodyMaxLines)
	}
	// A 500-line unit: clipped at the cap, with the true end recorded.
	huge := make([]string, 600)
	for index := range huge {
		huge[index] = "line " + strconv.Itoa(index+1)
	}
	root := t.TempDir()
	write(t, root, "big.go", strings.Join(huge, "\n"))
	bodies := symbolMatchBodies(root, []sem.SymbolRecord{{
		Name: "Huge", Kind: "function", FilePath: "big.go", StartLine: 1, EndLine: 500,
	}}, 1)
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d, want 1", len(bodies))
	}
	if !bodies[0].Elided || bodies[0].UnitEndLine != 500 {
		t.Fatalf("clip not recorded: %+v", bodies[0])
	}
	if got := bodies[0].EndLine - bodies[0].StartLine + 1; got != symbolMatchBodyMaxLines {
		t.Fatalf("printed %d lines, want the %d-line cap", got, symbolMatchBodyMaxLines)
	}
	var out bytes.Buffer
	writeSymbolMatchBodies(&out, bodies)
	want := "…continues to line 500 — rerun with --from " + strconv.Itoa(bodies[0].EndLine+1)
	if !strings.Contains(out.String(), want) {
		t.Fatalf("resume note missing %q:\n%s", want, out.String()[len(out.String())-200:])
	}
	// A unit that FITS says nothing: the note's presence has to mean something.
	small := symbolMatchBodies(root, []sem.SymbolRecord{{
		Name: "Small", FilePath: "big.go", StartLine: 1, EndLine: 3,
	}}, 1)
	var fits bytes.Buffer
	writeSymbolMatchBodies(&fits, small)
	if strings.Contains(fits.String(), "continues to line") {
		t.Fatalf("a complete body claimed a continuation:\n%s", fits.String())
	}
}

// TestSearchLocatorFollowUpNamesTheFetchingVerb pins fix 2, including the case where it must stay
// silent: a hit with no symbol name has no `def` argument, and naming a verb that cannot be run is
// worse than saying nothing.
func TestSearchLocatorFollowUpNamesTheFetchingVerb(t *testing.T) {
	t.Parallel()
	named := sem.SearchResult{
		Rank: 5, FilePath: "src/t_zset.c", StartLine: 3788, FocusLine: 3788,
		SymbolName: "genericZpopCommand",
	}
	var out bytes.Buffer
	writeTextSearchLocator(&out, named)
	want := "5. src/t_zset.c:3788 genericZpopCommand  [body: def genericZpopCommand]\n"
	if out.String() != want {
		t.Fatalf("locator = %q, want %q", out.String(), want)
	}
	// ~22 bytes on a bodyless hit, nothing on a bodied one.
	if cost := len(searchLocatorFollowUp(named)); cost > 40 {
		t.Fatalf("suffix costs %d bytes; it is meant to be cheap", cost)
	}
	if got := searchLocatorFollowUp(sem.SearchResult{FilePath: "a.go", StartLine: 1}); got != "" {
		t.Fatalf("a nameless hit suggested %q", got)
	}
	// A BODIED hit carries no suffix: it already has what the verb would fetch.
	var bodied bytes.Buffer
	writeTextSearchResult(&bodied, sem.SearchResult{
		Rank: 1, FilePath: "a.go", StartLine: 1, EndLine: 2, FocusLine: 1,
		SnippetStartLine: 1, SnippetEndLine: 2, Snippet: "func a() {}\n}", SymbolName: "a",
		Signals: []string{sem.CompleteSymbolSignal},
	}, true)
	if strings.Contains(bodied.String(), "[body: def") {
		t.Fatalf("a bodied hit carried a follow-up suffix:\n%s", bodied.String())
	}
}
