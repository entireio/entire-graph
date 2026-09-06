package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// enclosureTestFile builds a synthetic source file plus the symbol record for one callable
// occupying [start, end]. Line N of the file is literally "line N ..." so a test can assert
// that a returned snippet is a verbatim, correctly numbered slice of the file.
func enclosureTestFile(lineCount, start, end int, kind string) ([]string, SymbolRecord) {
	lines := make([]string, lineCount)
	for index := range lines {
		lines[index] = "line " + strconv.Itoa(index+1) + " of the file"
	}
	return lines, SymbolRecord{
		RecordType:    "symbol",
		ID:            "sym-1",
		Kind:          kind,
		Name:          "target",
		QualifiedName: "pkg.target",
		FilePath:      "pkg/file.go",
		StartLine:     start,
		EndLine:       end,
	}
}

func enclosureTestReader(lines []string) contentReader {
	content := strings.Join(lines, "\n")
	return func(path string) (string, bool) {
		if path != "pkg/file.go" {
			return "", false
		}
		return content, true
	}
}

func TestPlanSearchEnclosuresResolvesTrueSymbolBounds(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(200, 40, 90, "method")
	reader := enclosureTestReader(lines)

	cases := []struct {
		name       string
		result     SearchResult
		symbolByID bool
		byFile     bool
		kind       string
		maxLines   int
		wantStart  int
		wantEnd    int
	}{
		{
			// The hit carries the symbol's identity: its span is authoritative even though
			// the ranker clipped the reported region to a five-line window inside it.
			name:       "snaps to span when symbol id is known",
			result:     SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: "sym-1"},
			symbolByID: true,
			wantStart:  40,
			wantEnd:    90,
		},
		{
			// No symbol identity (a region or sparse-window hit): the smallest enclosing
			// callable in the same file is what recovers the body.
			name:      "falls back to smallest enclosing callable",
			result:    SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64},
			byFile:    true,
			wantStart: 40,
			wantEnd:   90,
		},
		{
			name:   "no enclosure without symbol records",
			result: SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64},
		},
		{
			// A container is not a callable: returning a whole class spends the budget on
			// members that are not the fix.
			name:       "container kinds are not enclosed",
			result:     SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: "sym-1"},
			symbolByID: true,
			byFile:     true,
			kind:       "class",
		},
		{
			name:       "body past the line cap degrades to the window",
			result:     SearchResult{FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: "sym-1"},
			symbolByID: true,
			maxLines:   10,
		},
		{
			name:       "hit outside the symbol is not enclosed",
			result:     SearchResult{FilePath: "pkg/file.go", StartLine: 120, EndLine: 124, FocusLine: 122, SnippetStartLine: 120, SnippetEndLine: 124, SymbolID: "sym-1"},
			symbolByID: true,
		},
		{
			name:       "already complete needs no upgrade",
			result:     SearchResult{FilePath: "pkg/file.go", StartLine: 40, EndLine: 90, FocusLine: 62, SnippetStartLine: 40, SnippetEndLine: 90, SymbolID: "sym-1"},
			symbolByID: true,
		},
		{
			name:       "unreadable file yields no enclosure",
			result:     SearchResult{FilePath: "pkg/missing.go", StartLine: 60, EndLine: 64, FocusLine: 62, SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: "sym-1"},
			symbolByID: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := symbol
			if testCase.kind != "" {
				record.Kind = testCase.kind
			}
			byID := map[string]SymbolRecord{}
			if testCase.symbolByID {
				byID[record.ID] = record
			}
			byFile := map[string][]SymbolRecord{}
			if testCase.byFile {
				byFile[record.FilePath] = []SymbolRecord{record}
			}
			enclosures := planSearchEnclosures(
				[]SearchResult{testCase.result}, byID, byFile, reader, testCase.maxLines, 0, 0, 0, 0, false,
			)
			if len(enclosures) != 1 {
				t.Fatalf("enclosures = %d, want 1", len(enclosures))
			}
			got := enclosures[0]
			if testCase.wantStart == 0 {
				if got.available() {
					t.Fatalf("enclosure available: %+v", got)
				}
				return
			}
			if !got.available() {
				t.Fatalf("enclosure unavailable, want %d-%d", testCase.wantStart, testCase.wantEnd)
			}
			if got.start != testCase.wantStart || got.end != testCase.wantEnd {
				t.Fatalf("enclosure = %d-%d, want %d-%d", got.start, got.end, testCase.wantStart, testCase.wantEnd)
			}
		})
	}
}

func TestWidenSearchResultToEnclosureReturnsVerbatimBody(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(200, 40, 90, "method")
	enclosure := searchEnclosure{start: symbol.StartLine, end: symbol.EndLine, lines: lines}
	result := SearchResult{
		FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62,
		SnippetStartLine: 60, SnippetEndLine: 64, Snippet: strings.Join(lines[59:64], "\n"),
		Signals: []string{"body"},
	}

	widened := widenSearchResultToEnclosure(result, enclosure)
	if widened.SnippetStartLine != 40 || widened.SnippetEndLine != 90 {
		t.Fatalf("snippet span = %d-%d, want 40-90", widened.SnippetStartLine, widened.SnippetEndLine)
	}
	if widened.Snippet != strings.Join(lines[39:90], "\n") {
		t.Fatalf("snippet is not the verbatim body")
	}
	// The response invariant is StartLine <= SnippetStartLine <= FocusLine <= SnippetEndLine
	// <= EndLine; widening the snippet must widen the reported region with it.
	if widened.StartLine > widened.SnippetStartLine || widened.EndLine < widened.SnippetEndLine {
		t.Fatalf("region %d-%d does not contain snippet %d-%d",
			widened.StartLine, widened.EndLine, widened.SnippetStartLine, widened.SnippetEndLine)
	}
	if widened.FocusLine < widened.StartLine || widened.FocusLine > widened.EndLine {
		t.Fatalf("focus %d outside region %d-%d", widened.FocusLine, widened.StartLine, widened.EndLine)
	}
	if !containsString(widened.Signals, searchCompleteSymbolSignal) {
		t.Fatalf("missing %q signal: %#v", searchCompleteSymbolSignal, widened.Signals)
	}
	// An unavailable enclosure must leave the result exactly as it was.
	if unchanged := widenSearchResultToEnclosure(result, searchEnclosure{}); unchanged.Snippet != result.Snippet {
		t.Fatalf("zero enclosure changed the result: %#v", unchanged)
	}
}

func TestTersifySearchResultKeepsSnippetVerbatim(t *testing.T) {
	t.Parallel()
	lines, _ := enclosureTestFile(200, 40, 90, "method")

	cases := []struct {
		name      string
		result    SearchResult
		maxLines  int
		wantStart int
		wantEnd   int
	}{
		{
			name:      "demotes to a locator window around the focus",
			result:    SearchResult{SnippetStartLine: 50, SnippetEndLine: 59, FocusLine: 55, Snippet: strings.Join(lines[49:59], "\n")},
			maxLines:  2,
			wantStart: 54,
			wantEnd:   55,
		},
		{
			name:      "already shorter than the window is untouched",
			result:    SearchResult{SnippetStartLine: 50, SnippetEndLine: 51, FocusLine: 50, Snippet: strings.Join(lines[49:51], "\n")},
			maxLines:  2,
			wantStart: 50,
			wantEnd:   51,
		},
		{
			name:      "non-positive window is a no-op",
			result:    SearchResult{SnippetStartLine: 50, SnippetEndLine: 59, FocusLine: 55, Snippet: strings.Join(lines[49:59], "\n")},
			maxLines:  0,
			wantStart: 50,
			wantEnd:   59,
		},
		{
			// A snippet the byte fitter already rewrote is no longer a verbatim line range;
			// re-slicing it by line would report line numbers it cannot back up.
			name:      "byte-truncated snippet is left alone",
			result:    SearchResult{SnippetStartLine: 50, SnippetEndLine: 59, FocusLine: 55, Snippet: "... truncated ..."},
			maxLines:  2,
			wantStart: 50,
			wantEnd:   59,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := tersifySearchResult(testCase.result, testCase.maxLines)
			if got.SnippetStartLine != testCase.wantStart || got.SnippetEndLine != testCase.wantEnd {
				t.Fatalf("span = %d-%d, want %d-%d",
					got.SnippetStartLine, got.SnippetEndLine, testCase.wantStart, testCase.wantEnd)
			}
			gotLines := strings.Split(got.Snippet, "\n")
			if want := testCase.wantEnd - testCase.wantStart + 1; len(gotLines) != want && got.Snippet != "... truncated ..." {
				t.Fatalf("snippet has %d lines, want %d", len(gotLines), want)
			}
			if got.Snippet == "... truncated ..." {
				return
			}
			for offset, line := range gotLines {
				if want := lines[testCase.wantStart-1+offset]; line != want {
					t.Fatalf("line %d = %q, want %q", testCase.wantStart+offset, line, want)
				}
			}
		})
	}
}

// makeAllocatorResults builds `count` uniform results, each with a `window`-line snippet drawn
// from a file whose enclosing callable spans `bodyLines` lines.
func makeAllocatorResults(count, window, bodyLines int) ([]SearchResult, []searchEnclosure, []string) {
	lines, _ := enclosureTestFile(bodyLines+40, 20, 19+bodyLines, "function")
	results := make([]SearchResult, count)
	enclosures := make([]searchEnclosure, count)
	for index := range results {
		start := 25 + index
		results[index] = SearchResult{
			Rank: index + 1, FilePath: "pkg/file.go", StartLine: start, EndLine: start + window - 1,
			FocusLine: start, SnippetStartLine: start, SnippetEndLine: start + window - 1,
			Snippet: strings.Join(lines[start-1:start+window-1], "\n"), Signals: []string{"body"},
		}
		enclosures[index] = searchEnclosure{start: 20, end: 19 + bodyLines, lines: lines}
	}
	return results, enclosures, lines
}

func TestAllocateSearchSnippetsFallsBackToLargestFittingHeadWindow(t *testing.T) {
	t.Parallel()
	lines := make([]string, 80)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%02d %s", index+1, strings.Repeat("archive detail ", 18))
	}
	result := SearchResult{
		Rank: 1, FilePath: "sessions/long.md", StartLine: 40, EndLine: 40,
		FocusLine: 40, SnippetStartLine: 40, SnippetEndLine: 40,
		Snippet: lines[39], Signals: []string{proseParentRetrievalSignal},
	}
	enclosure := searchEnclosure{start: 1, end: len(lines), lines: lines, window: true}
	before := serializedSearchResultBytes([]SearchResult{result})
	hardBudget := before + 3_000

	allocated, bodies, _ := allocateSearchSnippets(
		[]SearchResult{result}, []searchEnclosure{enclosure}, nil, hardBudget, hardBudget, 1, 1,
	)
	if bodies != 0 {
		t.Fatalf("adaptive read window counted as %d complete bodies", bodies)
	}
	got := allocated[0]
	if !containsString(got.Signals, searchHeadWindowSignal) {
		t.Fatalf("oversized window degraded to locator: %#v", got.Signals)
	}
	if got.SnippetStartLine >= got.SnippetEndLine {
		t.Fatalf("adaptive window did not add readable lines: %d-%d", got.SnippetStartLine, got.SnippetEndLine)
	}
	if got.SnippetStartLine > result.SnippetStartLine || got.SnippetEndLine < result.SnippetEndLine {
		t.Fatalf("adaptive window %d-%d dropped original snippet %d-%d",
			got.SnippetStartLine, got.SnippetEndLine, result.SnippetStartLine, result.SnippetEndLine)
	}
	if size := serializedSearchResultBytes(allocated); size > hardBudget {
		t.Fatalf("adaptive payload %d exceeds hard budget %d", size, hardBudget)
	}
	assertVerbatimSnippet(t, got, lines)
}

func TestAllocateSearchSnippetsSpendsBudgetByRank(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                       string
		count                      int
		window                     int
		bodyLines                  int
		hardBudget                 int
		growth                     int
		headRanks                  int
		tailLines                  int
		withEnclosure              bool
		wantBodies                 int
		wantBodiesExact            bool // wantBodies is the exact count, not a floor
		wantDemoted                int
		wantCompleteRanksArePrefix bool // completed ranks must be 1..k, never a gap
		wantGrowth                 bool // payload may exceed the ranker's size
	}{
		{
			// Nothing to buy: the payload must come back byte-for-byte unchanged.
			name: "no enclosures leaves the payload untouched", count: 6, window: 6, bodyLines: 30,
			growth: 4096, headRanks: 3, tailLines: 2,
		},
		{
			// The head — and only the head — comes back as N complete bodies. This is the
			// property an agent depends on: it can edit rank 1..N without a follow-up read.
			name: "buys a complete body for every head rank", count: 10, window: 6, bodyLines: 30,
			growth: 16384, headRanks: 5, tailLines: 2, withEnclosure: true,
			wantBodies: 5, wantBodiesExact: true, wantGrowth: true,
		},
		{
			// Below the head the agent is deciding "is this worth opening", so a whole
			// callable there would spend the head's budget on code that is not read. Even
			// with a budget large enough for all ten, only the head is upgraded.
			name: "never buys a body below the head", count: 10, window: 6, bodyLines: 30,
			hardBudget: 1 << 20, growth: 1 << 20, headRanks: 2, tailLines: 2, withEnclosure: true,
			wantBodies: 2, wantBodiesExact: true, wantGrowth: true,
		},
		{
			// With no allowance the tail has to pay, so at least one result is demoted and
			// the payload cannot grow.
			name: "tail funds the head when growth is zero", count: 8, window: 8, bodyLines: 20,
			growth: 0, headRanks: 3, tailLines: 1, withEnclosure: true,
			wantBodies: 1, wantDemoted: 5,
		},
		{
			// A symbol larger than anything the allowance can buy must degrade to its
			// window rather than blow the budget.
			name: "symbol larger than the budget degrades gracefully", count: 4, window: 4, bodyLines: 400,
			growth: 512, headRanks: 3, tailLines: 2, withEnclosure: true,
			wantBodiesExact: true,
		},
		{
			// Regression: a ranking shorter than the protected head must still be allocated.
			name: "ranking shorter than the head is still allocated", count: 2, window: 4, bodyLines: 20,
			growth: 4096, headRanks: 3, tailLines: 2, withEnclosure: true,
			wantBodies: 2, wantBodiesExact: true, wantGrowth: true,
		},
		{
			// Budget exhaustion: the ceiling wins over an unlimited growth allowance, so the
			// head is completed top-down until the ceiling is reached and not one byte further.
			name: "hard budget clamps the allowance", count: 4, window: 4, bodyLines: 20,
			hardBudget: 2000, growth: 100000, headRanks: 3, tailLines: 2, withEnclosure: true,
			wantBodies: 1, wantGrowth: true,
		},
		{
			// Degradation order: when the budget can seat only some of the head's bodies, the
			// ones it buys are the shallowest ranks (asserted below via wantCompleteRanks).
			name: "spends the shallowest ranks first", count: 8, window: 4, bodyLines: 40,
			hardBudget: 4200, growth: 100000, headRanks: 5, tailLines: 2, withEnclosure: true,
			wantBodies: 1, wantCompleteRanksArePrefix: true, wantGrowth: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			results, enclosures, lines := makeAllocatorResults(testCase.count, testCase.window, testCase.bodyLines)
			if !testCase.withEnclosure {
				enclosures = make([]searchEnclosure, len(results))
			}
			before := serializedSearchResultBytes(results)
			allocated, bodies, demoted := allocateSearchSnippets(
				results, enclosures, nil, testCase.hardBudget, testCase.growth, testCase.headRanks, testCase.tailLines,
			)
			after := serializedSearchResultBytes(allocated)

			if bodies < testCase.wantBodies {
				t.Fatalf("complete bodies = %d, want >= %d", bodies, testCase.wantBodies)
			}
			if testCase.wantBodiesExact && bodies != testCase.wantBodies {
				t.Fatalf("complete bodies = %d, want exactly %d", bodies, testCase.wantBodies)
			}
			if demoted < testCase.wantDemoted {
				t.Fatalf("demoted = %d, want >= %d", demoted, testCase.wantDemoted)
			}
			// A body is never granted below the head, whatever the budget allows.
			completed := 0
			for index, result := range allocated {
				if !containsString(result.Signals, searchCompleteSymbolSignal) {
					continue
				}
				completed++
				if index >= testCase.headRanks {
					t.Fatalf("rank %d is below the head (%d) but carries a complete body",
						index+1, testCase.headRanks)
				}
				if testCase.wantCompleteRanksArePrefix && index != completed-1 {
					t.Fatalf("completed rank %d leaves a shallower rank incomplete", index+1)
				}
			}
			if completed != bodies {
				t.Fatalf("allocator reported %d bodies but marked %d results", bodies, completed)
			}
			if len(allocated) != len(results) {
				t.Fatalf("allocator changed the result count: %d != %d", len(allocated), len(results))
			}
			// Contract: the payload never grows past what the ranker produced plus the
			// allowance, and never past the hard ceiling.
			// (The allocator runs after the byte fitter, so `before` is always already
			// within the hard ceiling in production; maxInt keeps the assertion meaningful
			// rather than accidentally requiring the allocator to shrink a payload.)
			ceiling := before + testCase.growth
			if testCase.hardBudget > 0 && testCase.hardBudget < ceiling {
				ceiling = maxInt(before, testCase.hardBudget)
			}
			if after > ceiling {
				t.Fatalf("payload %d exceeds ceiling %d (before %d)", after, ceiling, before)
			}
			if !testCase.wantGrowth && after > before {
				t.Fatalf("payload grew from %d to %d without an allowance to spend", before, after)
			}
			if bodies == 0 && after != before {
				t.Fatalf("payload changed (%d -> %d) without buying a body", before, after)
			}
			// The head is never demoted, and every snippet stays a verbatim slice.
			for index, result := range allocated {
				if index < testCase.headRanks {
					original := results[index]
					isBody := containsString(result.Signals, searchCompleteSymbolSignal)
					if !isBody && result.SnippetStartLine != original.SnippetStartLine {
						t.Fatalf("head rank %d was demoted", index+1)
					}
				}
				assertVerbatimSnippet(t, result, lines)
			}
		})
	}
}

// TestSearchEnclosureHeadRanksIsFiveDeep pins the shipped head depth, not just the mechanism.
// The number is the product measurement: first-hit is the edited file 35% of the time and one
// of the first three 46%, so a head of two or three leaves most searches one Read short of an
// edit. Five is the shipped judgement; changing it is a product decision and should break here.
func TestSearchEnclosureHeadRanksIsFiveDeep(t *testing.T) {
	t.Parallel()
	if searchEnclosureHeadRanks != 5 {
		t.Fatalf("searchEnclosureHeadRanks = %d, want 5", searchEnclosureHeadRanks)
	}
	results, enclosures, _ := makeAllocatorResults(8, 6, 30)
	_, bodies, _ := allocateSearchSnippets(
		results, enclosures, nil, 1<<20, searchEnclosureGrowthBytes,
		searchEnclosureHeadRanks, searchEnclosureTailSnippetLines,
	)
	if bodies != searchEnclosureHeadRanks {
		t.Fatalf("a budget-unconstrained allocation completed %d bodies, want the whole head (%d)",
			bodies, searchEnclosureHeadRanks)
	}
}

// TestWidenSearchResultToEnclosureReportsTheBodyItReturns covers the identity half of the
// upgrade: ranking may have attributed a hit to a container, but a result that SHOWS one
// method's source must NAME that method, or the agent reads `class Foo` above a body that
// is not the class.
func TestWidenSearchResultToEnclosureReportsTheBodyItReturns(t *testing.T) {
	t.Parallel()
	lines, _ := enclosureTestFile(200, 40, 90, "method")
	result := SearchResult{
		Rank: 1, FilePath: "pkg/file.go", StartLine: 1, EndLine: 200, FocusLine: 55,
		SnippetStartLine: 54, SnippetEndLine: 56, Snippet: strings.Join(lines[53:56], "\n"),
		Kind: "class", SymbolID: "container", SymbolName: "Ledger",
		QualifiedName: "Ledger", Signature: "class Ledger", Signals: []string{"body"},
	}
	inner := SymbolRecord{
		ID: "inner", Kind: "method", Name: "post", QualifiedName: "Ledger.post",
		Signature: "func (l *Ledger) post()", StartLine: 40, EndLine: 90,
	}
	widened := widenSearchResultToEnclosure(result, searchEnclosure{start: 40, end: 90, lines: lines, symbol: inner})

	if widened.SymbolID != inner.ID || widened.SymbolName != inner.Name ||
		widened.QualifiedName != inner.QualifiedName || widened.Kind != inner.Kind ||
		widened.Signature != inner.Signature {
		t.Fatalf("widened result still reports the container: %#v", widened)
	}
	if widened.SnippetStartLine != 40 || widened.SnippetEndLine != 90 {
		t.Fatalf("widened snippet spans %d-%d, want 40-90", widened.SnippetStartLine, widened.SnippetEndLine)
	}
	// Re-identification is an upgrade, not truncation: the stats counter must not report it.
	if truncated := countBudgetTruncatedResults([]SearchResult{result}, []SearchResult{widened}); truncated != 0 {
		t.Fatalf("completed body counted as %d truncated result(s)", truncated)
	}
	// A result the allocator did not re-identify keeps whatever ranking gave it.
	same := widenSearchResultToEnclosure(result, searchEnclosure{start: 40, end: 90, lines: lines})
	if same.SymbolName != "Ledger" {
		t.Fatalf("a body with no symbol record rewrote the result identity: %#v", same)
	}
}

func assertVerbatimSnippet(t *testing.T, result SearchResult, lines []string) {
	t.Helper()
	snippetLines := strings.Split(result.Snippet, "\n")
	if want := result.SnippetEndLine - result.SnippetStartLine + 1; len(snippetLines) != want {
		t.Fatalf("snippet claims %d lines but returned %d", want, len(snippetLines))
	}
	for offset, line := range snippetLines {
		if want := lines[result.SnippetStartLine-1+offset]; line != want {
			t.Fatalf("snippet line %d = %q, want %q", result.SnippetStartLine+offset, line, want)
		}
	}
	if result.SnippetStartLine < result.StartLine || result.SnippetEndLine > result.EndLine {
		t.Fatalf("snippet %d-%d escapes region %d-%d",
			result.SnippetStartLine, result.SnippetEndLine, result.StartLine, result.EndLine)
	}
}

// TestSearchRepositoryReturnsCompleteEnclosingBody exercises the whole path end to end: the
// ranked hit is a single line deep inside a function, and the payload comes back carrying that
// function's complete source so the agent does not have to open the file.
func TestSearchRepositoryReturnsCompleteEnclosingBody(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString("package billing\n\n// ApplyProrationDiscount adjusts a subscription charge.\nfunc ApplyProrationDiscount(amount int, days int) int {\n")
	for index := 0; index < 30; index++ {
		body.WriteString(fmt.Sprintf("\tamount += %d // step %d\n", index, index))
	}
	body.WriteString("\treturn amount\n}\n")
	write(t, repo, "billing/proration.go", body.String())
	write(t, repo, "docs/notes.md", "unrelated notes about billing\n")

	response, err := SearchRepository(t.Context(), repo, "test", "ApplyProrationDiscount proration discount", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	if response.Stats.CompleteSymbols < 1 {
		t.Fatalf("complete_symbol_snippets = %d, want >= 1", response.Stats.CompleteSymbols)
	}
	source, readErr := os.ReadFile(filepath.Join(repo, "billing/proration.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	fileLines := strings.Split(string(source), "\n")
	found := false
	for _, result := range response.Results {
		if !containsString(result.Signals, searchCompleteSymbolSignal) {
			continue
		}
		found = true
		assertVerbatimSnippet(t, result, fileLines)
		if !strings.Contains(result.Snippet, "func ApplyProrationDiscount") ||
			!strings.Contains(result.Snippet, "return amount") {
			t.Fatalf("complete body is missing its signature or its end:\n%s", result.Snippet)
		}
	}
	if !found {
		t.Fatal("no result carried the complete-symbol signal")
	}
}

// TestSearchRepositorySurfacesSimilarSibling covers the SIMILAR_TO expansion: a near-duplicate
// of a strong hit is the classic "second site that needs the same change", so it has to appear
// in the first search rather than after the patch review.
func TestSearchRepositorySurfacesSimilarSibling(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// Two near-identical bodies. Only the first shares vocabulary with the query, so the
	// second can only be reached through the near-duplicate relation.
	clone := func(name string) string {
		return fmt.Sprintf(`
func %s(values []int, offset int) int {
	total := 0
	for index, value := range values {
		if value < 0 {
			continue
		}
		total += value * (index + offset)
	}
	if total > 1000 {
		total = 1000
	}
	return total
}
`, name)
	}
	write(t, repo, "pkg/weighting.go", "package pkg\n"+clone("WeightedTotalForInvoice"))
	write(t, repo, "pkg/mirror.go", "package pkg\n"+clone("qqzzTotal"))

	response, err := SearchRepository(t.Context(), repo, "test", "WeightedTotalForInvoice", SearchOptions{
		Worktree: true,
		Profile:  ProfileFull,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	sibling := false
	for _, result := range response.Results {
		if result.SymbolName == "qqzzTotal" && containsString(result.Signals, "graph:similar_to") {
			sibling = true
		}
	}
	if !sibling {
		for _, result := range response.Results {
			t.Logf("rank %d %s %s %v", result.Rank, result.FilePath, result.SymbolName, result.Signals)
		}
		t.Fatal("near-duplicate sibling was not surfaced")
	}
}

// TestFitSearchResultsToBudgetPerResultFloor pins the allocation contract the byte fitter
// enforces, and which allocateSearchSnippets is built on top of: the per-result share has a
// FIXED 256-byte floor, not budget/count. Below that floor a result degrades to one truncated
// line and stops being usable, so the fitter drops whole results — and reports the drop in
// results_dropped_by_budget — rather than seating more results than the budget can pay for.
//
// The middle case is the discriminating one: budget/count is deliberately just under 256, so
// replacing maxInt(256, ...) with minInt(256, ...) would seat every result at ~248 bytes
// instead of dropping one, and the per-result floor assertion below fails.
func TestFitSearchResultsToBudgetPerResultFloor(t *testing.T) {
	t.Parallel()
	query := buildSearchQuery("target function")
	newResult := func(index, snippetLines int) SearchResult {
		return SearchResult{
			Rank: index + 1, FilePath: fmt.Sprintf("pkg/f%d.go", index), StartLine: 1, EndLine: snippetLines,
			FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: snippetLines,
			Snippet: strings.TrimSuffix(strings.Repeat("target function body line\n", snippetLines), "\n"),
			Signals: []string{"body"},
		}
	}
	// Four results of ~300 bytes each. Three fit the budget untouched; four only fit if the
	// per-result share is allowed to fall below the 256-byte floor.
	small := make([]SearchResult, 4)
	for index := range small {
		small[index] = newResult(index, 5)
	}
	unitSize := serializedSearchResultBytes(small[0])
	// Each result must be bigger than the floor, so a below-floor share would visibly shrink
	// it rather than leave it alone.
	if unitSize <= 256 {
		t.Fatalf("fixture drifted: result is %d bytes, expected > 256", unitSize)
	}
	// A budget whose per-result share at four results (~148 bytes) is far below the floor:
	// honouring the floor seats fewer, whole results; a min(256, budget/count) share would
	// seat more results each compacted below 256 bytes.
	tightBudget := 600
	if perResultAtFour := (tightBudget - 2 - 3) / 4; perResultAtFour >= 256 {
		t.Fatalf("fixture drifted: budget/count is %d, must be well under the 256 floor", perResultAtFour)
	}

	big := make([]SearchResult, 8)
	for index := range big {
		big[index] = newResult(index, 40)
	}

	cases := []struct {
		name           string
		results        []SearchResult
		budget         int
		wantSeated     int // -1: any count, only the floor contract is asserted
		wantMinDropped int
	}{
		{name: "generous budget seats everything untouched", results: big, budget: 100000, wantSeated: 8},
		{name: "floor forces a drop instead of starving every result", results: small, budget: tightBudget, wantSeated: -1, wantMinDropped: 1},
		{name: "budget too small for one whole result seats nothing", results: big, budget: 200, wantSeated: 0, wantMinDropped: 8},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			seated, bytes, dropped, _ := fitSearchResultsToBudget(testCase.results, query, testCase.budget)
			if testCase.wantSeated >= 0 && len(seated) != testCase.wantSeated {
				t.Fatalf("seated %d results, want %d", len(seated), testCase.wantSeated)
			}
			if dropped < testCase.wantMinDropped {
				t.Fatalf("dropped %d results, want at least %d", dropped, testCase.wantMinDropped)
			}
			if dropped+len(seated) != len(testCase.results) {
				t.Fatalf("accounting mismatch: %d seated + %d dropped != %d",
					len(seated), dropped, len(testCase.results))
			}
			if bytes != serializedSearchResultBytes(seated) {
				t.Fatalf("reported %d bytes, actual %d", bytes, serializedSearchResultBytes(seated))
			}
			if len(seated) > 0 && bytes > testCase.budget {
				t.Fatalf("seated payload %d exceeds budget %d", bytes, testCase.budget)
			}
			// The floor itself: no seated result is ever compacted below 256 bytes.
			for index, result := range seated {
				size := serializedSearchResultBytes(result)
				original := serializedSearchResultBytes(testCase.results[index])
				if size < 256 && size < original {
					t.Fatalf("result %d compacted to %d bytes, below the 256-byte floor", index, size)
				}
			}
		})
	}
}

// TestPlanSearchEnclosuresBodyHeadRanksBoundsBodies pins the split between the BODY head and the
// locator head. Measured on sonnet (config 9, 20 instances): of the files a payload showed, rank 1
// was later read 62% / edited 54% and rank 2 read 27% / edited 20%, but ranks 3, 4 and 5 were read
// 0% / 7% / 0% and edited 11% / 7% / 0% — while still being charged for 21 complete bodies whose
// bytes are replayed on every subsequent turn. Narrowing the body head must therefore stop planning
// enclosures past the cap, WITHOUT dropping the results themselves: a tail entry is the only mention
// of its file, and the gold file sits at rank 6-8 on 17% of instances.
func TestPlanSearchEnclosuresBodyHeadRanksBoundsBodies(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(200, 40, 90, "function")
	reader := enclosureTestReader(lines)
	byID := map[string]SymbolRecord{symbol.ID: symbol}
	result := SearchResult{
		FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62,
		SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: symbol.ID,
	}
	results := []SearchResult{result, result, result, result, result}

	enclosures := planSearchEnclosures(results, byID, nil, reader, 0, 0, 2, 0, 0, false)
	if len(enclosures) != len(results) {
		t.Fatalf("enclosures = %d, want %d — narrowing the body head must not drop results",
			len(enclosures), len(results))
	}
	for index, enclosure := range enclosures {
		if index < 2 {
			if !enclosure.available() {
				t.Fatalf("rank %d: want a body inside the head", index+1)
			}
			continue
		}
		if enclosure.available() {
			t.Fatalf("rank %d: got a body past bodyHeadRanks=2, want none", index+1)
		}
	}

	// 0 means "use the built-in depth", so the default behaviour is unchanged.
	for index, enclosure := range planSearchEnclosures(results, byID, nil, reader, 0, 0, 0, 0, 0, false) {
		if !enclosure.available() {
			t.Fatalf("rank %d: default depth should still plan a body", index+1)
		}
	}
}

// TestPlanSearchEnclosuresContextLinesPadsRankOneOnly pins the margin. Measured over 65 sessions: of
// the reads that re-open a file whose complete body the payload ALREADY printed, only 3.8% ask for
// lines inside that body while 80% ask for lines outside it (median gap 42 lines). The body is the
// right unit to edit and the wrong unit to understand. The margin is rank-1 only, is capped by
// maxLines like any other body, and never renames the symbol.
func TestPlanSearchEnclosuresContextLinesPadsRankOneOnly(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(200, 40, 90, "function")
	reader := enclosureTestReader(lines)
	byID := map[string]SymbolRecord{symbol.ID: symbol}
	result := SearchResult{
		FilePath: "pkg/file.go", StartLine: 60, EndLine: 64, FocusLine: 62,
		SnippetStartLine: 60, SnippetEndLine: 64, SymbolID: symbol.ID,
	}

	got := planSearchEnclosures([]SearchResult{result, result}, byID, nil, reader, 0, 10, 0, 0, 0, false)
	if got[0].start != 30 || got[0].end != 100 {
		t.Fatalf("rank 1 = %d-%d, want 30-100 (symbol 40-90 padded by 10)", got[0].start, got[0].end)
	}
	if got[0].symbol.Name != symbol.Name {
		t.Fatalf("padded body renamed the symbol to %q", got[0].symbol.Name)
	}
	if got[1].start != 40 || got[1].end != 90 {
		t.Fatalf("rank 2 = %d-%d, want the unpadded 40-90", got[1].start, got[1].end)
	}

	// A margin that would push the body past the line cap is refused, not truncated: the cap is
	// what guarantees a padded body can never cost more than an unpadded one was allowed to.
	capped := planSearchEnclosures([]SearchResult{result}, byID, nil, reader, 60, 40, 0, 0, 0, false)
	if capped[0].start != 40 || capped[0].end != 90 {
		t.Fatalf("capped = %d-%d, want the unpadded 40-90", capped[0].start, capped[0].end)
	}

	// The margin clamps at the file edges rather than running off them.
	edgeLines, edgeSymbol := enclosureTestFile(50, 3, 47, "function")
	edge := planSearchEnclosures(
		[]SearchResult{{
			FilePath: "pkg/file.go", StartLine: 20, EndLine: 24, FocusLine: 22,
			SnippetStartLine: 20, SnippetEndLine: 24, SymbolID: edgeSymbol.ID,
		}},
		map[string]SymbolRecord{edgeSymbol.ID: edgeSymbol}, nil,
		enclosureTestReader(edgeLines), 0, 25, 0, 0, 0, false,
	)
	if edge[0].start != 1 || edge[0].end != 50 {
		t.Fatalf("edge = %d-%d, want 1-50 (clamped to the file)", edge[0].start, edge[0].end)
	}
}

// TestPlanSearchEnclosuresHeadWindowNeverClaimsCompleteSymbol pins the fallback for a head rank that
// has no enclosable callable. Measured over 79 agent sessions: the gold file appeared in the payload
// as a LOCATOR ONLY (zero code) in 20 of 74 sessions, 24 of those at rank <= 3, and 61 post-search
// Reads targeted a ranked file that carried no code — each costing a turn (~17,000 tokens) to recover
// what ~2 kB of window would have carried.
//
// The window must be readable code AND must never claim `complete-symbol`, because that signal is the
// promise "no follow-up read needed" and a window cannot make it.
func TestPlanSearchEnclosuresHeadWindowNeverClaimsCompleteSymbol(t *testing.T) {
	t.Parallel()
	lines, symbol := enclosureTestFile(400, 40, 90, "class") // a CONTAINER: not enclosable
	reader := enclosureTestReader(lines)
	byID := map[string]SymbolRecord{symbol.ID: symbol}
	byFile := map[string][]SymbolRecord{symbol.FilePath: {symbol}}
	result := SearchResult{
		FilePath: "pkg/file.go", StartLine: 60, EndLine: 61, FocusLine: 60,
		SnippetStartLine: 60, SnippetEndLine: 61, SymbolID: symbol.ID,
	}

	// windowLines=0 keeps the old behaviour: a container yields no enclosure at all.
	if got := planSearchEnclosures([]SearchResult{result}, byID, byFile, reader, 0, 0, 0, 0, 0, false); got[0].available() {
		t.Fatalf("windowLines=0 must not synthesise an enclosure: %+v", got[0])
	}

	got := planSearchEnclosures([]SearchResult{result}, byID, byFile, reader, 0, 0, 0, 60, 0, false)
	if !got[0].available() {
		t.Fatal("want a window enclosure for an unenclosable head rank")
	}
	if !got[0].window {
		t.Fatal("the fallback must be marked as a window, not as a body")
	}
	if got[0].start != 30 || got[0].end != 89 {
		t.Fatalf("window = %d-%d, want 30-89 (60 lines centred on focus 60)", got[0].start, got[0].end)
	}
	widened := widenSearchResultToEnclosure(result, got[0])
	if containsString(widened.Signals, searchCompleteSymbolSignal) {
		t.Fatalf("a window must NEVER claim %q: %#v", searchCompleteSymbolSignal, widened.Signals)
	}
	if !containsString(widened.Signals, searchHeadWindowSignal) {
		t.Fatalf("a window must carry %q: %#v", searchHeadWindowSignal, widened.Signals)
	}
	if widened.Snippet != strings.Join(lines[29:89], "\n") {
		t.Fatal("window snippet is not a verbatim slice of the file")
	}

	// A result that already shows MORE than the window must be left alone — a window may only add
	// readable lines, never re-cut a wider snippet.
	wide := result
	wide.SnippetStartLine, wide.SnippetEndLine = 1, 200
	if got := planSearchEnclosures([]SearchResult{wide}, byID, byFile, reader, 0, 0, 0, 60, 0, false); got[0].available() {
		t.Fatalf("window must not shrink an already-wider snippet: %+v", got[0])
	}
}
