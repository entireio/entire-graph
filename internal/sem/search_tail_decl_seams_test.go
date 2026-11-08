package sem

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TAIL DECLARATION SEAMS
// ======================
//
// planWithDemotionFrom cuts a demoted tail row with tersifySearchResultKeepingDeclaration, so a row cut
// to searchEnclosureTailSnippetLines keeps the line that NAMES its symbol when that line was in its
// snippet. Two other producers cut the same kind of row (below the rendered head, no source-paying
// signal) and must keep the declaration the same way:
//
//   (a) seatForcedSearchUnits' dead-weight reclaim (--full-unit-top), and
//   (b) planSearchCalleeHopRanking's tail demotion (--callee-hop).
//
// THE ORACLE IS INDEPENDENT. Every expected tail snippet, span and both byte ceilings are built here
// from literal fixture text: expected rows are written out by hand and sized with encoding/json plus
// the JSON array arithmetic ("[" + elements joined by "," + "]"). Nothing expected is derived from
// tersifySearchResult, tersifySearchResultKeepingDeclaration, widenSearchResultToEnclosure or either
// producer.

// tailSeamCase is one tail row of pkg/tail.go and the literal span either producer must cut it to.
type tailSeamCase struct {
	name                    string
	first                   int      // file line of lines[0]
	lines                   []string // the row's whole snippet, verbatim
	focus, symStart, symEnd int
	nameLine                int
	plainStart, plainEnd    int // the focus window (documentation + discrimination check only)
	wantStart, wantEnd      int
	wantSnippet             string
}

func tailSeamFill(first, last int) []string {
	lines := make([]string, 0, last-first+1)
	for n := first; n <= last; n++ {
		lines = append(lines, "line "+strconv.Itoa(n)+" of the tail")
	}
	return lines
}

func tailSeamCases() []tailSeamCase {
	sameNameCall := []string{
		"\ttotal += tailTarget(n - 1)",
		"\ttotal *= 2",
		"\treturn total",
		"}",
	}
	return []tailSeamCase{
		// POSITIVE, parser name line, equal line lengths (the predicted witness).
		{
			name: "parsed_name_line_equal_lengths", first: 10, lines: tailSeamFill(10, 13),
			focus: 12, symStart: 10, symEnd: 13, nameLine: 10,
			plainStart: 11, plainEnd: 12, wantStart: 10, wantEnd: 11,
			wantSnippet: "line 10 of the tail\nline 11 of the tail",
		},
		// POSITIVE, parser name line, declaration much longer than the line it displaces: the kept row
		// is LARGER than the focus window, so the byte ceilings below are exercised for real.
		{
			name: "parsed_name_line_long_declaration", first: 10,
			lines: []string{
				"func tailTarget(ctx context.Context, items []Item, limit int) (int, error) {",
				"\tn := len(items)",
				"\treturn n, nil",
				"}",
			},
			focus: 12, symStart: 10, symEnd: 13, nameLine: 10,
			plainStart: 11, plainEnd: 12, wantStart: 10, wantEnd: 11,
			wantSnippet: "func tailTarget(ctx context.Context, items []Item, limit int) (int, error) {\n\tn := len(items)",
		},
		// POSITIVE, no parser metadata: searchSymbolDeclarationLine falls back to the lexical finder, which
		// takes the definition-shaped `func tailTarget(` line.
		{
			name: "lexical_fallback_declaration", first: 10,
			lines: []string{
				"func tailTarget(n int) int {",
				"\tif n < 2 {",
				"\t\treturn n",
				"\t}",
			},
			focus: 12, symStart: 10, symEnd: 13, nameLine: 0,
			plainStart: 11, plainEnd: 12, wantStart: 10, wantEnd: 11,
			wantSnippet: "func tailTarget(n int) int {\n\tif n < 2 {",
		},
		// CONTROL: no metadata and no line naming tailTarget, so the fallback finds nothing.
		{
			name: "missing_name_line_no_name_text", first: 10, lines: tailSeamFill(10, 13),
			focus: 12, symStart: 10, symEnd: 13, nameLine: 0,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "line 11 of the tail\nline 12 of the tail",
		},
		// CONTROL: name line inside the snippet but outside the symbol's span [11,13] is invalid.
		{
			name: "invalid_name_line_outside_symbol", first: 10, lines: tailSeamFill(10, 13),
			focus: 12, symStart: 11, symEnd: 13, nameLine: 10,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "line 11 of the tail\nline 12 of the tail",
		},
		// CONTROL: negative name line is invalid.
		{
			name: "invalid_negative_name_line", first: 10, lines: tailSeamFill(10, 13),
			focus: 12, symStart: 10, symEnd: 13, nameLine: -1,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "line 11 of the tail\nline 12 of the tail",
		},
		// CONTROL: valid name line (7, inside symbol [5,13]) above the snippet: no declaration here.
		{
			name: "name_line_above_snippet", first: 10, lines: tailSeamFill(10, 13),
			focus: 12, symStart: 5, symEnd: 13, nameLine: 7,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "line 11 of the tail\nline 12 of the tail",
		},
		// CONTROL: annotation-protected. The focus window 9-10 shows the annotation lines; declaration 11
		// plus that region is 3 lines > 2, so the focus window stays.
		{
			name: "annotation_region_protected", first: 9,
			lines: []string{
				"//go:noinline",
				"//go:nosplit",
				"func tailTarget(n int) int {",
				"\treturn n * 2",
				"}",
			},
			focus: 10, symStart: 9, symEnd: 13, nameLine: 11,
			plainStart: 9, plainEnd: 10, wantStart: 9, wantEnd: 10,
			wantSnippet: "//go:noinline\n//go:nosplit",
		},
		// CONTROL: nonzero INVALID metadata (20 > symbol end 13) must refuse the lexical finder, which
		// would otherwise take line 10's same-name CALL as the declaration and keep 10-11.
		{
			name: "invalid_name_line_refuses_same_name_call", first: 10, lines: sameNameCall,
			focus: 12, symStart: 10, symEnd: 13, nameLine: 20,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "\ttotal *= 2\n\treturn total",
		},
		// CONTROL: nonzero VALID metadata outside the snippet (15, inside symbol [10,20]) must refuse the
		// lexical finder too; the symbol starts inside the snippet, so the finder WOULD reach line 10.
		{
			name: "valid_name_line_below_snippet_refuses_same_name_call", first: 10, lines: sameNameCall,
			focus: 12, symStart: 10, symEnd: 20, nameLine: 15,
			plainStart: 11, plainEnd: 12, wantStart: 11, wantEnd: 12,
			wantSnippet: "\ttotal *= 2\n\treturn total",
		},
	}
}

// row is the ranked tail row the case describes.
func (tc tailSeamCase) row(rank int) SearchResult {
	last := tc.first + len(tc.lines) - 1
	return SearchResult{
		Rank: rank, FilePath: "pkg/tail.go", Kind: "function", SymbolID: "tail-1",
		SymbolName: "tailTarget", QualifiedName: "pkg.tailTarget",
		StartLine: tc.first, EndLine: last, FocusLine: tc.focus,
		SnippetStartLine: tc.first, SnippetEndLine: last, Snippet: strings.Join(tc.lines, "\n"),
		SymbolStartLine: tc.symStart, SymbolEndLine: tc.symEnd, SymbolNameLine: tc.nameLine,
		Signals: []string{},
	}
}

// expected is the hand-built row the producer must emit: the input row with only its snippet and
// snippet span replaced by the literal expectation.
func (tc tailSeamCase) expected(rank int) SearchResult {
	want := tc.row(rank)
	want.Snippet = tc.wantSnippet
	want.SnippetStartLine, want.SnippetEndLine = tc.wantStart, tc.wantEnd
	return want
}

// checkFixture validates the case against its own literal text only.
func (tc tailSeamCase) checkFixture(t *testing.T) {
	t.Helper()
	if len(tc.lines) <= searchEnclosureTailSnippetLines {
		t.Fatalf("%s: fixture needs no cut", tc.name)
	}
	if slice := strings.Join(tc.lines[tc.wantStart-tc.first:tc.wantEnd-tc.first+1], "\n"); slice != tc.wantSnippet {
		t.Fatalf("%s: wantSnippet %q is not fixture lines %d-%d %q", tc.name, tc.wantSnippet, tc.wantStart, tc.wantEnd, slice)
	}
	if tc.wantEnd-tc.wantStart+1 != searchEnclosureTailSnippetLines || tc.plainEnd-tc.plainStart+1 != searchEnclosureTailSnippetLines {
		t.Fatalf("%s: literal spans are not %d lines", tc.name, searchEnclosureTailSnippetLines)
	}
}

func jsonBytes(t *testing.T, value any) int {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

// jsonArrayBytes is the size json.Marshal gives a slice of these rows: "[" + rows joined by "," + "]".
func jsonArrayBytes(t *testing.T, rows ...SearchResult) int {
	t.Helper()
	total := 2 + len(rows) - 1
	for _, row := range rows {
		total += jsonBytes(t, row)
	}
	return total
}

// assertTailRow: literal span and text first (this is the predicted RED line), then every other field
// unchanged (FocusLine included), the line budget, and byte-for-byte equality with the oracle.
func assertTailRow(t *testing.T, producer string, tc tailSeamCase, got, want SearchResult) {
	t.Helper()
	if got.SnippetStartLine != tc.wantStart || got.SnippetEndLine != tc.wantEnd || got.Snippet != tc.wantSnippet {
		t.Fatalf("%s/%s: tail row = %d-%d %q, want %d-%d %q", producer, tc.name,
			got.SnippetStartLine, got.SnippetEndLine, got.Snippet, tc.wantStart, tc.wantEnd, tc.wantSnippet)
	}
	if got.FocusLine != tc.focus {
		t.Fatalf("%s/%s: focus moved %d -> %d", producer, tc.name, tc.focus, got.FocusLine)
	}
	if n := len(strings.Split(got.Snippet, "\n")); n > searchEnclosureTailSnippetLines {
		t.Fatalf("%s/%s: tail holds %d lines, budget is %d", producer, tc.name, n, searchEnclosureTailSnippetLines)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s/%s: tail row\n%+v\nwant\n%+v", producer, tc.name, got, want)
	}
	if g, w := jsonBytes(t, got), jsonBytes(t, want); g != w {
		t.Fatalf("%s/%s: tail row is %d bytes, oracle %d", producer, tc.name, g, w)
	}
}

// seamFile is pkg/file.go: 120 numbered lines, a forced unit over 10-60.
func seamFile() []string {
	lines := make([]string, 120)
	for index := range lines {
		lines[index] = "line " + strconv.Itoa(index+1) + " of the file"
	}
	return lines
}

// seamHead is a 4-line row of pkg/file.go: wide enough that any cut would visibly change it.
func seamHead(lines []string, rank, first int) SearchResult {
	return SearchResult{
		Rank: rank, FilePath: "pkg/file.go", StartLine: first, EndLine: first + 3,
		FocusLine: first + 2, SnippetStartLine: first, SnippetEndLine: first + 3,
		Snippet: strings.Join(lines[first-1:first+3], "\n"), Signals: []string{},
	}
}

// (a) --full-unit-top dead-weight reclaim: seatForcedSearchUnits (search_enclosure.go:1240).
func TestSeatForcedSearchUnitsKeepsTailDeclaration(t *testing.T) {
	t.Parallel()
	lines := seamFile()
	unitSymbol := SymbolRecord{
		RecordType: "symbol", ID: "unit-1", Kind: "function", Name: "Target", QualifiedName: "pkg.Target",
		FilePath: "pkg/file.go", StartLine: 10, EndLine: 60,
	}
	// The seated unit, written out by hand: rank 1 (20-23, focus 22) widened to the whole 10-60 unit and
	// renamed to it, with the forced + complete signals.
	wantUnit := SearchResult{
		Rank: 1, FilePath: "pkg/file.go", StartLine: 10, EndLine: 60, FocusLine: 22,
		SnippetStartLine: 10, SnippetEndLine: 60, Snippet: strings.Join(lines[9:60], "\n"),
		SymbolStartLine: 10, SymbolEndLine: 60, Kind: "function", SymbolID: "unit-1",
		SymbolName: "Target", QualifiedName: "pkg.Target",
		Signals: []string{"full-unit", "complete-symbol"},
	}
	for _, tc := range tailSeamCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.checkFixture(t)
			head1, head2, tail := seamHead(lines, 1, 20), seamHead(lines, 2, 100), tc.row(3)
			ranked := []SearchResult{head1, head2, tail}
			enclosures := make([]searchEnclosure, len(ranked))
			enclosures[0] = searchEnclosure{start: 10, end: 60, lines: lines, symbol: unitSymbol, forced: true}

			// Ceiling = the exact size of the oracle plan [unit, rank 2 untouched, rank 3 cut as expected].
			wantTail := tc.expected(3)
			budget := jsonArrayBytes(t, wantUnit, head2, wantTail)
			if jsonArrayBytes(t, wantUnit, head2, tail) <= budget {
				t.Fatal("ceiling holds the untouched tail, so the reclaim would never run")
			}

			// control == ranked: what allocateSearchSnippets passes here (protectedRanks 5 >= 3 results cuts
			// nothing; the blanked enclosures buy nothing).
			plan, bodies, demoted := seatForcedSearchUnits(ranked, ranked, enclosures, budget, 1, 0, 0)
			if len(plan) != 3 {
				t.Fatalf("seat: %d rows, want 3", len(plan))
			}
			// Non-vacuity: the body was seated AND the tail was reclaimed. A no-op returns control.
			if !reflect.DeepEqual(plan[0], wantUnit) {
				t.Fatalf("seat: rank 1 =\n%+v\nwant the seated unit\n%+v", plan[0], wantUnit)
			}
			if bodies != 1 || demoted != 1 {
				t.Fatalf("seat: bodies=%d demoted=%d, want 1 and 1", bodies, demoted)
			}
			if !reflect.DeepEqual(plan[1], head2) {
				t.Fatal("seat: rank 2 sits inside the rendered head and was changed")
			}
			if got := jsonBytes(t, plan); got > budget {
				t.Fatalf("seat: plan is %d bytes over a %d-byte ceiling", got, budget)
			}
			assertTailRow(t, "seatForcedSearchUnits", tc, plan[2], wantTail)

			// Same seam through its production caller (search_enclosure.go:1030).
			viaAlloc, allocBodies, allocDemoted := allocateSearchSnippets(ranked, enclosures, nil, budget, 0, 5, 5, 2)
			if len(viaAlloc) != 3 || !reflect.DeepEqual(viaAlloc[0], wantUnit) || allocBodies != 1 || allocDemoted != 1 {
				t.Fatalf("allocate: len=%d bodies=%d demoted=%d, want the seated unit, 1 and 1",
					len(viaAlloc), allocBodies, allocDemoted)
			}
			if got := jsonBytes(t, viaAlloc); got > budget {
				t.Fatalf("allocate: plan is %d bytes over a %d-byte ceiling", got, budget)
			}
			assertTailRow(t, "allocateSearchSnippets", tc, viaAlloc[2], wantTail)
		})
	}
}

// (b) --callee-hop tail demotion: planSearchCalleeHopRanking (search_callee.go:500).
func TestPlanSearchCalleeHopRankingKeepsTailDeclaration(t *testing.T) {
	t.Parallel()
	lines := seamFile()
	hop := SearchResult{
		FilePath: "pkg/callee.go", StartLine: 1, EndLine: 2, FocusLine: 1,
		SnippetStartLine: 1, SnippetEndLine: 2, Snippet: "func helper() {\n}",
		Signals: []string{"callee-hop"},
	}
	for _, tc := range tailSeamCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.checkFixture(t)
			results := []SearchResult{seamHead(lines, 1, 20), seamHead(lines, 2, 100), tc.row(3)}

			// Oracle plan, by hand: hop inserted after rank 1, everything renumbered 1..4, tail cut.
			wantHead1, wantHop, wantHead2 := seamHead(lines, 1, 20), hop, seamHead(lines, 3, 100)
			wantHop.Rank = 2
			wantTail := tc.expected(4)
			untouchedTail := tc.row(4)

			// demoteFrom 2, tailLines 2: only rank 3 (index 2) may be cut.
			plan := planSearchCalleeHopRanking(results, []SearchResult{hop}, []int{0}, 2, 2)
			if len(plan) != 4 {
				t.Fatalf("plan: %d rows, want 4", len(plan))
			}
			// Non-vacuity: the hop was inserted after its anchor; the head is untouched.
			if !reflect.DeepEqual(plan[1], wantHop) {
				t.Fatalf("plan: row 2 = %+v, want the hop %+v", plan[1], wantHop)
			}
			if !reflect.DeepEqual(plan[0], wantHead1) || !reflect.DeepEqual(plan[2], wantHead2) {
				t.Fatal("plan: a rendered-head rank was changed to pay for a callee hop")
			}
			assertTailRow(t, "planSearchCalleeHopRanking", tc, plan[3], wantTail)

			// Through its production caller (search.go:1831): a ceiling equal to the oracle plan, which the
			// uncut plan (demoteFrom 3) must exceed, so the merge has to run the line-500 cut.
			budget := jsonArrayBytes(t, wantHead1, wantHop, wantHead2, wantTail)
			if jsonArrayBytes(t, wantHead1, wantHop, wantHead2, untouchedTail) <= budget {
				t.Fatal("ceiling holds the untouched tail, so the merge would never cut rank 3")
			}
			merged, seated := mergeSearchCalleeHopSites(results, []SearchResult{hop}, []int{0}, budget, 2, 1)
			if seated != 1 || len(merged) != 4 || !reflect.DeepEqual(merged[1], wantHop) {
				t.Fatalf("merge: seated=%d len=%d, want the hop seated at row 2", seated, len(merged))
			}
			if got := jsonBytes(t, merged); got > budget {
				t.Fatalf("merge: %d bytes over a %d-byte ceiling", got, budget)
			}
			assertTailRow(t, "mergeSearchCalleeHopSites", tc, merged[3], wantTail)
		})
	}
}
