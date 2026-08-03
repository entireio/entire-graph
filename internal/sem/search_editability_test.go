package sem

import (
	"strconv"
	"strings"
	"testing"
)

// EDITABILITY TESTS
// =================
//
// These pin the two payload levers that exist to raise EDITABILITY — the share of needed edits whose
// replaced text is present VERBATIM in the payload, as opposed to RECALL, which only asks whether the
// right file was ranked. The two diverge sharply: on the R30PUB benchmark payloads the gold file is
// ranked for 8 of 12 gold files while the gold LINES are printed for none of them, because the
// payload prints a six-line window (or, below the text renderer's second rank, no source at all).
//
//   --full-unit-top N   (L1) the first N ranks come back as their whole enclosing unit
//   --edit-site-bodies  (L2) the literal block's EDIT sites come back with source
//
// Both are off by default and every test below that asserts new behaviour turns one of them on, which
// is what keeps the default payload byte-for-byte unchanged.

// editabilityFile builds a file of `lineCount` numbered lines plus a symbol record over [start,end].
func editabilityFile(lineCount, start, end int, kind string) ([]string, SymbolRecord) {
	lines := make([]string, lineCount)
	for index := range lines {
		lines[index] = "line " + strconv.Itoa(index+1) + " of the file"
	}
	return lines, SymbolRecord{
		RecordType: "symbol", ID: "unit-1", Kind: kind, Name: "Target", QualifiedName: "pkg.Target",
		FilePath: "pkg/file.go", StartLine: start, EndLine: end,
	}
}

func editabilityReader(lines []string) contentReader {
	content := strings.Join(lines, "\n")
	return func(path string) (string, bool) {
		if path != "pkg/file.go" {
			return "", false
		}
		return content, true
	}
}

// TestSearchFullUnitForceRanksGatesRankTwoOnTheScoreGap pins the flag's arithmetic. Rank 1 is
// unconditional; every rank past it is admitted only while the ranking has NOT separated it from
// rank 1, so a search with one clear answer pays for one body and not for N.
func TestSearchFullUnitForceRanksGatesRankTwoOnTheScoreGap(t *testing.T) {
	t.Parallel()
	scored := func(scores ...float64) []SearchResult {
		results := make([]SearchResult, 0, len(scores))
		for index, score := range scores {
			results = append(results, SearchResult{Rank: index + 1, Score: score})
		}
		return results
	}
	for _, testCase := range []struct {
		name    string
		results []SearchResult
		top     int
		want    int
	}{
		{name: "off by default", results: scored(10, 10), top: 0, want: 0},
		{name: "negative is off", results: scored(10, 10), top: -3, want: 0},
		{name: "rank one is unconditional even when rank two is far behind",
			results: scored(100, 1), top: 1, want: 1},
		{name: "rank two joins when the ranking did not separate them",
			results: scored(100, 95), top: 2, want: 2},
		{name: "rank two is refused when rank one is clearly ahead",
			results: scored(100, 80), top: 2, want: 1},
		{name: "the gap is measured against rank one, so admission stops at the first break",
			// 0.042, 0.070 are inside the 15% band; 0.153 is outside, so the run stops at 3.
			results: scored(35.58, 34.09, 33.09, 30.14, 29.66), top: 5, want: 3},
		{name: "a request deeper than the ranking is clamped to it",
			results: scored(10, 10), top: 9, want: 2},
		{name: "an empty ranking forces nothing", results: nil, top: 2, want: 0},
		{name: "a zero top score carries no separation information, so the request stands",
			results: scored(0, 0), top: 2, want: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchFullUnitForceRanks(testCase.results, testCase.top); got != testCase.want {
				t.Fatalf("searchFullUnitForceRanks = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestPlanForcedSearchUnitBypassesTheOpportunisticConditions is the core L1 test: each case is a
// condition that makes the DEFAULT allocator return a six-line window, and the forced planner has to
// return source anyway. Each case therefore also asserts what the default path does, so the test
// fails if the two ever stop differing (which would mean the flag has become a no-op).
func TestPlanForcedSearchUnitBypassesTheOpportunisticConditions(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		lineCount    int
		unitStart    int
		unitEnd      int
		kind         string
		result       SearchResult
		wantStart    int
		wantEnd      int
		wantElidedOf [2]int
	}{
		{
			// A CONTAINER. searchEnclosableSymbolKind excludes it on purpose, so the default path has
			// no body to offer — and a hit inside a class body, a constant table or a type declaration
			// is exactly where the measured editability misses are.
			name:      "a container kind the default path refuses",
			lineCount: 200, unitStart: 40, unitEnd: 120, kind: "class",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 65, FocusLine: 62,
				SnippetStartLine: 60, SnippetEndLine: 65, SymbolID: "unit-1"},
			wantStart: 40, wantEnd: 120,
		},
		{
			// PAST THE 160-LINE CAP. The default path degrades to a window here, which is the case
			// where reading the file back costs the most.
			name:      "a callable past defaultSearchEnclosureMaxLines",
			lineCount: 400, unitStart: 20, unitEnd: 20 + defaultSearchEnclosureMaxLines, kind: "function",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 100, EndLine: 105, FocusLine: 102,
				SnippetStartLine: 100, SnippetEndLine: 105, SymbolID: "unit-1"},
			wantStart: 20, wantEnd: 20 + defaultSearchEnclosureMaxLines,
		},
		{
			// ALREADY COMPLETE. The default path short-circuits ("nothing to gain") and the result
			// ships a whole function that says nowhere that it is whole — redis__redis-10095 rank 1.
			// The forced planner still marks it, because the claim is the thing the caller asked for.
			name:      "a unit the ranked snippet already covers",
			lineCount: 100, unitStart: 30, unitEnd: 34, kind: "function",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 30, EndLine: 34, FocusLine: 31,
				SnippetStartLine: 30, SnippetEndLine: 34, SymbolID: "unit-1"},
			wantStart: 30, wantEnd: 34,
		},
		{
			// THE UNION. A ranked region routinely starts above the symbol's declaration (doc comment,
			// decorator, attribute) and that preamble is often the most useful line on the screen. A
			// forced unit must never print LESS than the ranking already did.
			name:      "the ranked snippet reaches above the unit and is kept",
			lineCount: 100, unitStart: 30, unitEnd: 34, kind: "function",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 28, EndLine: 34, FocusLine: 31,
				SnippetStartLine: 28, SnippetEndLine: 34, SymbolID: "unit-1"},
			wantStart: 28, wantEnd: 34,
		},
		{
			// PAST THE SAFETY CAP. Clipped, anchored at the unit's own start, and the elision recorded
			// so a reader is told rather than left to discover it.
			name:      "a unit past searchFullUnitMaxLines is clipped and reports it",
			lineCount: 1200, unitStart: 100, unitEnd: 900, kind: "function",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 150, EndLine: 155, FocusLine: 152,
				SnippetStartLine: 150, SnippetEndLine: 155, SymbolID: "unit-1"},
			wantStart: 100, wantEnd: 100 + searchFullUnitMaxLines - 1,
			wantElidedOf: [2]int{100, 900},
		},
		{
			// The clip slides only when the hit would otherwise fall outside the anchored window, and
			// then it is centred on the hit and clamped to the unit.
			name:      "a clip slides to keep the hit inside it",
			lineCount: 1200, unitStart: 100, unitEnd: 900, kind: "function",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 800, EndLine: 805, FocusLine: 802,
				SnippetStartLine: 800, SnippetEndLine: 805, SymbolID: "unit-1"},
			// Centring on 802 would run past the unit's end, so the window is pushed back to end
			// exactly at it: 501-900, still containing the hit and never leaving the unit.
			wantStart: 501, wantEnd: 900,
			wantElidedOf: [2]int{100, 900},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			lines, symbol := editabilityFile(testCase.lineCount, testCase.unitStart, testCase.unitEnd, testCase.kind)
			byID := map[string]SymbolRecord{symbol.ID: symbol}
			byFile := map[string][]SymbolRecord{symbol.FilePath: {symbol}}
			reader := editabilityReader(lines)
			results := []SearchResult{testCase.result}

			forced := planSearchEnclosures(results, byID, byFile, reader, 0, 0, 0, 0, 1)
			if !forced[0].available() || !forced[0].forced {
				t.Fatalf("forced enclosure = %+v, want an available forced unit", forced[0])
			}
			if forced[0].start != testCase.wantStart || forced[0].end != testCase.wantEnd {
				t.Fatalf("forced span = %d-%d, want %d-%d",
					forced[0].start, forced[0].end, testCase.wantStart, testCase.wantEnd)
			}
			if got := [2]int{forced[0].unitStart, forced[0].unitEnd}; got != testCase.wantElidedOf {
				t.Fatalf("recorded unit span = %v, want %v", got, testCase.wantElidedOf)
			}
			if forced[0].end-forced[0].start+1 > searchFullUnitMaxLines {
				t.Fatalf("forced span %d-%d exceeds the safety cap of %d lines",
					forced[0].start, forced[0].end, searchFullUnitMaxLines)
			}

			// The flag is only a lever if the default path does something DIFFERENT here. Without it
			// the payload either keeps the ranker's window or offers no body at all.
			plain := planSearchEnclosures(results, byID, byFile, reader, 0, 0, 0, 0, 0)
			if plain[0].available() &&
				plain[0].start == testCase.wantStart && plain[0].end == testCase.wantEnd {
				t.Fatalf("the default path already returns %d-%d, so --full-unit-top is a no-op here",
					plain[0].start, plain[0].end)
			}
		})
	}
}

// TestPlanForcedSearchUnitFallsBackToAWindowWithoutASymbol pins the last resort. A forced rank whose
// file the graph holds no symbol for still must not come back as six lines, and the window has to be
// marked forced so the allocator seats it with the rest of the prefix — the bug this pins cost
// fmtlib__fmt-2457 its whole rank-3 window.
func TestPlanForcedSearchUnitFallsBackToAWindowWithoutASymbol(t *testing.T) {
	t.Parallel()
	lines, _ := editabilityFile(300, 0, 0, "")
	result := SearchResult{FilePath: "pkg/file.go", StartLine: 100, EndLine: 105, FocusLine: 102,
		SnippetStartLine: 100, SnippetEndLine: 105}

	got := planSearchEnclosures([]SearchResult{result}, nil, nil, editabilityReader(lines), 0, 0, 0, 0, 1)
	if !got[0].available() {
		t.Fatal("a forced rank with no symbol must still get a read window")
	}
	if !got[0].window {
		t.Fatalf("enclosure = %+v, want a window (it is not a whole unit and must not claim to be)", got[0])
	}
	if !got[0].forced {
		t.Fatal("the fallback window must be marked forced, or the allocator never seats it")
	}
	if span := got[0].end - got[0].start + 1; span < searchHeadWindowLines {
		t.Fatalf("window span = %d lines, want at least %d", span, searchHeadWindowLines)
	}
	// And it says head-window, never complete-symbol: a window cannot make the no-follow-up promise.
	widened := widenSearchResultToEnclosure(result, got[0])
	if !hasSearchSignal(widened, searchHeadWindowSignal) {
		t.Fatalf("signals = %v, want %s", widened.Signals, searchHeadWindowSignal)
	}
	for _, forbidden := range []string{searchCompleteSymbolSignal, searchFullUnitSignal} {
		if hasSearchSignal(widened, forbidden) {
			t.Fatalf("a window claimed %s: %v", forbidden, widened.Signals)
		}
	}
}

// TestWidenSearchResultToEnclosureReportsWhatAForcedUnitActuallyIs pins the signals and the reported
// span, because those are the only things a reader has to tell a whole unit from a clipped one.
func TestWidenSearchResultToEnclosureReportsWhatAForcedUnitActuallyIs(t *testing.T) {
	t.Parallel()
	lines, symbol := editabilityFile(200, 40, 90, "class")
	result := SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 65, FocusLine: 62,
		SnippetStartLine: 60, SnippetEndLine: 65, SymbolID: "other", Kind: "region"}

	whole := widenSearchResultToEnclosure(result, searchEnclosure{
		start: 40, end: 90, lines: lines, symbol: symbol, forced: true,
	})
	for _, want := range []string{searchFullUnitSignal, searchCompleteSymbolSignal} {
		if !hasSearchSignal(whole, want) {
			t.Fatalf("whole unit signals = %v, want %s", whole.Signals, want)
		}
	}
	if hasSearchSignal(whole, searchFullUnitElidedSignal) {
		t.Fatalf("an unclipped unit reported an elision: %v", whole.Signals)
	}
	if whole.UnitStartLine != 0 || whole.UnitEndLine != 0 {
		t.Fatalf("unit span reported on an unclipped unit: %d-%d", whole.UnitStartLine, whole.UnitEndLine)
	}
	// A whole body must NAME the body it shows: reporting the ranked region's identity above a
	// different unit's source is wrong in the one field an agent uses to decide what it is reading.
	if whole.SymbolID != symbol.ID || whole.Kind != symbol.Kind || whole.QualifiedName != symbol.QualifiedName {
		t.Fatalf("identity = %s/%s/%s, want the unit's own", whole.SymbolID, whole.Kind, whole.QualifiedName)
	}
	if lineCount := len(strings.Split(whole.Snippet, "\n")); lineCount != whole.SnippetEndLine-whole.SnippetStartLine+1 {
		t.Fatalf("snippet is %d lines for span %d-%d — the snippet must stay a verbatim slice",
			lineCount, whole.SnippetStartLine, whole.SnippetEndLine)
	}

	clipped := widenSearchResultToEnclosure(result, searchEnclosure{
		start: 40, end: 60, lines: lines, symbol: symbol, forced: true, unitStart: 40, unitEnd: 90,
	})
	for _, want := range []string{searchFullUnitSignal, searchFullUnitElidedSignal} {
		if !hasSearchSignal(clipped, want) {
			t.Fatalf("clipped unit signals = %v, want %s", clipped.Signals, want)
		}
	}
	if hasSearchSignal(clipped, searchCompleteSymbolSignal) {
		t.Fatalf("a clipped unit claimed complete-symbol: %v", clipped.Signals)
	}
	if clipped.UnitStartLine != 40 || clipped.UnitEndLine != 90 {
		t.Fatalf("clipped unit span = %d-%d, want 40-90", clipped.UnitStartLine, clipped.UnitEndLine)
	}
}

// TestSearchUnitElisionNoteDescribesOnlyWhatIsMissing pins the note. It is rendered from the printed
// span against the unit's span, so it can never claim an elision that did not happen.
func TestSearchUnitElisionNoteDescribesOnlyWhatIsMissing(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name                                         string
		printedStart, printedEnd, unitStart, unitEnd int
		want                                         string
	}{
		{name: "nothing recorded", printedStart: 10, printedEnd: 20, want: ""},
		{name: "printed span is the whole unit",
			printedStart: 10, printedEnd: 20, unitStart: 10, unitEnd: 20, want: ""},
		{name: "the tail was clipped",
			printedStart: 10, printedEnd: 20, unitStart: 10, unitEnd: 40,
			want: "…elided lines 21–40 (unit continues)"},
		{name: "the head was clipped",
			printedStart: 30, printedEnd: 40, unitStart: 10, unitEnd: 40,
			want: "…elided lines 10–29 (unit continues)"},
		{name: "both ends were clipped",
			printedStart: 30, printedEnd: 40, unitStart: 10, unitEnd: 60,
			want: "…elided lines 10–29, 41–60 (unit continues)"},
		{name: "an unusable printed span says nothing",
			printedStart: 0, printedEnd: 0, unitStart: 10, unitEnd: 60, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := SearchUnitElisionNote(
				testCase.printedStart, testCase.printedEnd, testCase.unitStart, testCase.unitEnd,
			)
			if got != testCase.want {
				t.Fatalf("note = %q, want %q", got, testCase.want)
			}
		})
	}
}

// forcedAllocationFixture is five ranked results in one file plus a forced unit for rank 1, sized so
// the unit is much larger than the window the ranker produced.
func forcedAllocationFixture() ([]SearchResult, []searchEnclosure) {
	lines, symbol := editabilityFile(600, 10, 300, "function")
	results := make([]SearchResult, 0, 5)
	for rank := 1; rank <= 5; rank++ {
		start := 20 + rank*40
		results = append(results, SearchResult{
			Rank: rank, FilePath: "pkg/file.go", StartLine: start, EndLine: start + 9,
			FocusLine: start + 2, SnippetStartLine: start, SnippetEndLine: start + 9,
			Snippet: strings.Join(lines[start-1:start+9], "\n"), Signals: []string{},
		})
	}
	enclosures := make([]searchEnclosure, len(results))
	enclosures[0] = searchEnclosure{start: 10, end: 300, lines: lines, symbol: symbol, forced: true}
	return results, enclosures
}

// TestAllocateForcedSearchUnitsPaysOutOfTheTail pins the funding order: the tail is demoted to
// locators to seat the forced unit, and the forced rank itself is never the thing that yields first.
//
// It also pins the reason the flag was needed at all. The opportunistic allocator only evaluates cuts
// down to minInt(headRanks, len(results)), so with --top-k 5 and headRanks 5 the ONLY cut it can
// consider is "demote nothing" — the entire body budget is the growth allowance. The forced path can
// reach the tail.
func TestAllocateForcedSearchUnitsPaysOutOfTheTail(t *testing.T) {
	t.Parallel()
	results, enclosures := forcedAllocationFixture()
	rankedBytes := 0
	for _, result := range results {
		rankedBytes += serializedSearchResultBytes(result)
	}
	// A ceiling that cannot hold the unit alongside five full windows, but can hold it alongside
	// five locators.
	budget := serializedSearchResultBytes(widenSearchResultToEnclosure(results[0], enclosures[0])) + 900

	plan, bodies, demoted := allocateSearchSnippets(results, enclosures, budget, 0, 5, 2)
	if bodies != 1 {
		t.Fatalf("bodies = %d, want 1 (the forced unit)", bodies)
	}
	if plan[0].SnippetStartLine != 10 || plan[0].SnippetEndLine != 300 {
		t.Fatalf("rank 1 = %d-%d, want the whole forced unit 10-300",
			plan[0].SnippetStartLine, plan[0].SnippetEndLine)
	}
	if demoted == 0 {
		t.Fatal("nothing was demoted, so the forced unit was funded out of thin air")
	}
	if got := searchResultsSize(planSizes(plan)); got > budget {
		t.Fatalf("plan is %d bytes over a %d-byte ceiling — --max-context-bytes must be exact", got, budget)
	}
	// The tail keeps its locator: path, line and symbol name survive demotion, which is a tail
	// result's whole job.
	if plan[4].FilePath == "" || plan[4].SnippetStartLine == 0 {
		t.Fatalf("rank 5 lost its locator: %+v", plan[4])
	}

	// With no ceiling at all the same call must still seat the unit and demote nothing.
	unbounded, bodiesFree, demotedFree := allocateSearchSnippets(results, enclosures, 0, 0, 5, 2)
	if bodiesFree != 1 || demotedFree != 0 {
		t.Fatalf("unbounded: bodies=%d demoted=%d, want 1 and 0", bodiesFree, demotedFree)
	}
	if unbounded[0].SnippetEndLine != 300 {
		t.Fatalf("unbounded rank 1 ends at %d, want 300", unbounded[0].SnippetEndLine)
	}
}

// TestAllocateForcedSearchUnitsYieldsToTheCeilingLast pins the other half of the contract:
// --max-context-bytes is a promise, not a preference. When the ceiling cannot hold the forced unit
// even with the whole tail reduced to locators, the forced rank hands its ranked snippet back rather
// than the payload breaching its budget.
func TestAllocateForcedSearchUnitsYieldsToTheCeilingLast(t *testing.T) {
	t.Parallel()
	results, enclosures := forcedAllocationFixture()
	ranked := searchResultsSize(planSizes(results))

	plan, bodies, _ := allocateSearchSnippets(results, enclosures, ranked, 0, 5, 2)
	if got := searchResultsSize(planSizes(plan)); got > ranked {
		t.Fatalf("plan is %d bytes over a %d-byte ceiling", got, ranked)
	}
	if bodies != 0 {
		t.Fatalf("bodies = %d, want 0: the unit does not fit and must not be reported as delivered", bodies)
	}
	if plan[0].SnippetEndLine != results[0].SnippetEndLine {
		t.Fatalf("rank 1 = %d-%d, want its ranked snippet back",
			plan[0].SnippetStartLine, plan[0].SnippetEndLine)
	}
	if hasSearchSignal(plan[0], searchFullUnitSignal) {
		t.Fatalf("a revoked unit still claimed full-unit: %v", plan[0].Signals)
	}
}

func planSizes(plan []SearchResult) []int {
	sizes := make([]int, len(plan))
	for index := range plan {
		sizes[index] = serializedSearchResultBytes(plan[index])
	}
	return sizes
}

// TestClipSearchUnitToBytesKeepsTheHitLine pins the byte backstop. A 25-line window is 700 bytes in
// ordinary source and 9.5 kB in a generated table (redis's src/commands.c is one command per line at
// ~400 B), so the line cap alone is not a budget.
func TestClipSearchUnitToBytesKeepsTheHitLine(t *testing.T) {
	t.Parallel()
	lines := make([]string, 40)
	for index := range lines {
		lines[index] = strings.Repeat("x", 400)
	}
	start, end := clipSearchUnitToBytes(lines, 1, 40, 20, 1000)
	if start > 20 || end < 20 {
		t.Fatalf("clip = %d-%d, want a window containing the hit line 20", start, end)
	}
	if size := len(strings.Join(lines[start-1:end], "\n")); size > 1000 {
		t.Fatalf("clip is %d bytes over a 1000-byte cap", size)
	}
	// A window already inside the budget is returned untouched.
	if a, b := clipSearchUnitToBytes(lines, 5, 6, 5, 4000); a != 5 || b != 6 {
		t.Fatalf("clip = %d-%d, want 5-6 unchanged", a, b)
	}
	// One line over budget beats no line at all.
	if a, b := clipSearchUnitToBytes(lines, 1, 40, 7, 10); a != 7 || b != 7 {
		t.Fatalf("clip = %d-%d, want the hit line 7 alone", a, b)
	}
}

// editSiteFixture is a cluster with one site of each role in one file, plus the file's symbols and a
// needle index that can read it.
func editSiteFixture() (*SearchLiteralCluster, map[string][]SymbolRecord, *searchNeedleIndex) {
	source := strings.Builder{}
	for line := 1; line <= 120; line++ {
		source.WriteString("src line " + strconv.Itoa(line) + "\n")
	}
	cluster := &SearchLiteralCluster{
		Literal: "CONCEPT", HitsTotal: 4, FilesTotal: 2,
		Hits: []SearchLiteralHit{
			{FilePath: "pkg/table.go", Line: 12, Symbol: "pkg.Table", Role: SearchLiteralRoleEdit},
			{FilePath: "pkg/table.go", Line: 60, Symbol: "pkg.use", Role: SearchLiteralRoleConsumer},
			{FilePath: "docs/guide.md", Line: 3, Role: SearchLiteralRoleDoc},
			{FilePath: "pkg/table.go", Line: 100, Role: SearchLiteralRoleEdit},
		},
	}
	symbols := map[string][]SymbolRecord{"pkg/table.go": {
		{ID: "table", Kind: "type", Name: "Table", QualifiedName: "pkg.Table",
			FilePath: "pkg/table.go", StartLine: 8, EndLine: 20},
		{ID: "use", Kind: "function", Name: "use", QualifiedName: "pkg.use",
			FilePath: "pkg/table.go", StartLine: 55, EndLine: 70},
	}}
	content := source.String()
	index := &searchNeedleIndex{read: func(path string) (string, bool) {
		if path != "pkg/table.go" {
			return "", false
		}
		return content, true
	}}
	return cluster, symbols, index
}

// TestAttachSearchLiteralEditBodiesGivesSourceToEditSitesOnly is the core L2 test. A CONSUMER is
// listed precisely so an agent can decide NOT to open it, so printing its body would spend bytes
// arguing against the block's own advice; a DOC site cannot execute. Only EDIT sites get source.
func TestAttachSearchLiteralEditBodiesGivesSourceToEditSitesOnly(t *testing.T) {
	t.Parallel()
	cluster, symbols, index := editSiteFixture()
	attachSearchLiteralEditBodies(cluster, symbols, index)

	if body := cluster.Hits[0].Body; body == "" {
		t.Fatal("the EDIT site inside a type declaration got no body")
	}
	// The unit wins when the graph has one — including a CONTAINER, which is by construction what an
	// EDIT site sits in.
	if cluster.Hits[0].BodyStartLine != 8 || cluster.Hits[0].BodyEndLine != 20 {
		t.Fatalf("EDIT body span = %d-%d, want the enclosing unit 8-20",
			cluster.Hits[0].BodyStartLine, cluster.Hits[0].BodyEndLine)
	}
	if !strings.Contains(cluster.Hits[0].Body, "src line 12") {
		t.Fatalf("EDIT body does not contain its own site line:\n%s", cluster.Hits[0].Body)
	}
	// No line-number prefixes. Agents copy this text verbatim as an Edit anchor, and inline numbering
	// breaks the anchor.
	for _, line := range strings.Split(cluster.Hits[0].Body, "\n") {
		if line != "" && !strings.HasPrefix(line, "src line ") {
			t.Fatalf("body line is decorated, not verbatim: %q", line)
		}
	}
	// No unit in the graph covers line 100, so that site falls back to a bounded window around it.
	if cluster.Hits[3].BodyStartLine != 100-searchLiteralEditBodyContextLines ||
		cluster.Hits[3].BodyEndLine != 100+searchLiteralEditBodyContextLines {
		t.Fatalf("fallback window = %d-%d, want +/-%d around line 100",
			cluster.Hits[3].BodyStartLine, cluster.Hits[3].BodyEndLine, searchLiteralEditBodyContextLines)
	}
	for _, position := range []int{1, 2} {
		if cluster.Hits[position].Body != "" {
			t.Fatalf("hit %d (%s) got a body; only EDIT sites may",
				position, cluster.Hits[position].Role)
		}
	}

	rendered := string(RenderSearchLiteralCluster(cluster))
	// A site with a body prints its RANGE, because that is what describes the source underneath.
	if !strings.Contains(rendered, "pkg/table.go:8-20 pkg.Table EDIT") {
		t.Fatalf("render is missing the ranged EDIT header:\n%s", rendered)
	}
	// A site without one keeps the single-line locator form exactly as before.
	if !strings.Contains(rendered, "pkg/table.go:60 pkg.use CONSUMER") ||
		!strings.Contains(rendered, "docs/guide.md:3 DOC") {
		t.Fatalf("render changed the locator form of a non-EDIT site:\n%s", rendered)
	}
}

// TestAttachSearchLiteralEditBodiesRespectsItsCaps pins the two per-site caps and the site count.
func TestAttachSearchLiteralEditBodiesRespectsItsCaps(t *testing.T) {
	t.Parallel()
	// A unit far past the line cap, and lines long enough that the byte cap binds first.
	content := strings.Repeat(strings.Repeat("y", 300)+"\n", 500)
	symbols := map[string][]SymbolRecord{"pkg/wide.go": {
		{ID: "huge", Kind: "type", Name: "Huge", FilePath: "pkg/wide.go", StartLine: 1, EndLine: 480},
	}}
	index := &searchNeedleIndex{read: func(string) (string, bool) { return content, true }}
	cluster := &SearchLiteralCluster{Literal: "CONCEPT", HitsTotal: 1, FilesTotal: 1,
		Hits: []SearchLiteralHit{{FilePath: "pkg/wide.go", Line: 240, Role: SearchLiteralRoleEdit}}}

	attachSearchLiteralEditBodies(cluster, symbols, index)
	hit := cluster.Hits[0]
	if hit.Body == "" {
		t.Fatal("no body at all: an over-cap unit must clip, not vanish")
	}
	if span := hit.BodyEndLine - hit.BodyStartLine + 1; span > searchLiteralEditBodyMaxLines {
		t.Fatalf("body span = %d lines, over the %d-line cap", span, searchLiteralEditBodyMaxLines)
	}
	if len(hit.Body) > searchLiteralEditBodyMaxBytes {
		t.Fatalf("body = %d bytes, over the %d-byte cap", len(hit.Body), searchLiteralEditBodyMaxBytes)
	}
	if hit.BodyStartLine > 240 || hit.BodyEndLine < 240 {
		t.Fatalf("clip = %d-%d, want a window containing the site line 240", hit.BodyStartLine, hit.BodyEndLine)
	}
	if hit.BodyUnitStartLine != 1 || hit.BodyUnitEndLine != 480 {
		t.Fatalf("recorded unit = %d-%d, want 1-480 so the elision can be reported",
			hit.BodyUnitStartLine, hit.BodyUnitEndLine)
	}
	if note := SearchUnitElisionNote(
		hit.BodyStartLine, hit.BodyEndLine, hit.BodyUnitStartLine, hit.BodyUnitEndLine,
	); !strings.Contains(string(RenderSearchLiteralCluster(cluster)), note) {
		t.Fatalf("render omits the elision note %q", note)
	}

	// The site cap bounds how many bodies are attached however many sites are listed.
	many := &SearchLiteralCluster{Literal: "CONCEPT", HitsTotal: 20, FilesTotal: 1}
	for site := 0; site < searchLiteralEditBodySites+4; site++ {
		many.Hits = append(many.Hits, SearchLiteralHit{
			FilePath: "pkg/wide.go", Line: 10 + site, Role: SearchLiteralRoleEdit,
		})
	}
	attachSearchLiteralEditBodies(many, symbols, index)
	bodied := 0
	for _, hit := range many.Hits {
		if hit.Body != "" {
			bodied++
		}
	}
	if bodied != searchLiteralEditBodySites {
		t.Fatalf("bodied sites = %d, want the cap of %d", bodied, searchLiteralEditBodySites)
	}
}

// TestFitSearchLiteralClusterDropsBodiesBeforeSites pins the yield order and the cap switch. A site is
// a grep the agent does not have to run whether or not its source is printed, so a body is the
// cheaper thing to lose — and a cluster that loses its LAST body must fall back under the location
// cap, or it ships over a ceiling the contract check then rejects.
func TestFitSearchLiteralClusterDropsBodiesBeforeSites(t *testing.T) {
	t.Parallel()
	// Bodies that fit the edit-body cap survive untouched, and the cap they are measured against is
	// the edit-body one rather than the location one.
	small, symbols, index := editSiteFixture()
	attachSearchLiteralEditBodies(small, symbols, index)
	if searchLiteralClusterCap(small, searchLiteralClusterMaxBytes) != searchLiteralEditBodyClusterMaxBytes {
		t.Fatal("a cluster carrying bodies must be held to the edit-body cap")
	}
	if fitted := fitSearchLiteralCluster(small, searchLiteralClusterMaxBytes); fitted == nil ||
		!searchLiteralClusterHasBodies(fitted) {
		t.Fatal("bodies that fit the edit-body cap were dropped anyway")
	}

	// Now overshoot the edit-body cap: eight sites of ~3 kB each. Bodies yield from the END, and every
	// SITE stays — a site is a grep the agent does not have to run whether or not its source is
	// printed, so a body is the cheaper thing to lose.
	big := &SearchLiteralCluster{Literal: "CONCEPT", HitsTotal: 30, FilesTotal: 1}
	for site := 0; site < searchLiteralEditBodySites; site++ {
		big.Hits = append(big.Hits, SearchLiteralHit{
			FilePath: "pkg/wide.go", Line: 10 + site, Role: SearchLiteralRoleEdit,
			BodyStartLine: 1, BodyEndLine: 10,
			Body: strings.Repeat(strings.Repeat("z", 299)+"\n", 10),
		})
	}
	sites := len(big.Hits)
	fitted := fitSearchLiteralCluster(big, searchLiteralClusterMaxBytes)
	if fitted == nil {
		t.Fatal("fitter dropped the whole block")
	}
	if len(fitted.Hits) != sites {
		t.Fatalf("hits = %d, want all %d kept: bodies must yield before sites do", len(fitted.Hits), sites)
	}
	if got := searchLiteralClusterCost(fitted); got > searchLiteralEditBodyClusterMaxBytes {
		t.Fatalf("fitted block = %d bytes, over the %d-byte edit-body cap",
			got, searchLiteralEditBodyClusterMaxBytes)
	}
	bodied := 0
	for _, hit := range fitted.Hits {
		if hit.Body != "" {
			bodied++
		}
	}
	if bodied == 0 || bodied == sites {
		t.Fatalf("bodied sites = %d of %d, want a partial reduction", bodied, sites)
	}
	// They went from the end, so the earliest (path-ordered, hence definition-first) survive.
	for position := 0; position < bodied; position++ {
		if fitted.Hits[position].Body == "" {
			t.Fatalf("body %d was dropped ahead of a deeper one", position)
		}
	}

	// THE INVARIANT validateSearchContextBlockBudget relies on: whatever the fitter returns is within
	// the cap DERIVED FROM WHAT IT RETURNS. A cluster sized under the edit-body cap and then stripped
	// of its last body must fall back under the location cap in the same call, or the contract check
	// rejects a block the fitter just declared fitted.
	//
	// The long paths are what make the body-less list overflow 560 B on its own, which is the only way
	// the fall-back branch can be reached at all.
	long := &SearchLiteralCluster{Literal: "CONCEPT", HitsTotal: 40, FilesTotal: 6}
	for site := 0; site < 6; site++ {
		long.Hits = append(long.Hits, SearchLiteralHit{
			FilePath: strings.Repeat("deeply/nested/", 8) + "file" + strconv.Itoa(site) + ".go",
			Line:     10 + site, Role: SearchLiteralRoleEdit,
			BodyStartLine: 1, BodyEndLine: 12,
			Body: strings.Repeat(strings.Repeat("w", 299)+"\n", 12),
		})
	}
	fittedLong := fitSearchLiteralCluster(long, searchLiteralClusterMaxBytes)
	if fittedLong == nil {
		t.Fatal("fitter dropped a block that a shorter list would have fitted")
	}
	if got, cap := searchLiteralClusterCost(fittedLong),
		searchLiteralClusterCap(fittedLong, searchLiteralClusterMaxBytes); got > cap {
		t.Fatalf("fitted block = %d bytes over its own applicable cap of %d — "+
			"validateSearchContextBlockBudget would reject it", got, cap)
	}
}

// TestBuildSearchLiteralClusterLeavesEditBodiesOffByDefault is the regression guard on the default
// payload: the flag has to be the only thing that can add source to this block.
func TestBuildSearchLiteralClusterLeavesEditBodiesOffByDefault(t *testing.T) {
	t.Parallel()
	results, q, symbolsByFile, index := searchLiteralMagnetFixture(4)
	off := buildSearchLiteralCluster(results, q, symbolsByFile, index, searchLiteralClusterMaxBytes, false)
	if off == nil {
		t.Fatal("fixture produced no cluster")
	}
	if searchLiteralClusterHasBodies(off) {
		t.Fatalf("the default block carries source:\n%s", RenderSearchLiteralCluster(off))
	}
}
