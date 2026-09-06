package sem

import (
	"fmt"
	"strings"
	"testing"
)

// spanMergeFile builds a deterministic file whose Nth line names itself, so an assertion on the
// merged snippet is an assertion that the range is VERBATIM and unelided rather than stitched.
func spanMergeFile(path string, lineCount int) contentReader {
	lines := make([]string, lineCount)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index+1)
	}
	content := strings.Join(lines, "\n")
	return func(requested string) (string, bool) {
		if requested != path {
			return "", false
		}
		return content, true
	}
}

func spanMergeBody(rank int, path string, start, end int) SearchResult {
	return SearchResult{
		Rank:             rank,
		Score:            float64(100 - rank),
		FilePath:         path,
		StartLine:        start,
		EndLine:          end,
		FocusLine:        start,
		SnippetStartLine: start,
		SnippetEndLine:   end,
		SymbolID:         fmt.Sprintf("sym-%d", rank),
		SymbolName:       fmt.Sprintf("symbol%d", rank),
		Signals:          []string{"body", searchCompleteSymbolSignal},
		Snippet:          strings.Repeat("x", 8),
	}
}

func wantLineRange(t *testing.T, snippet string, start, end int) {
	t.Helper()
	lines := strings.Split(snippet, "\n")
	if len(lines) != end-start+1 {
		t.Fatalf("merged span holds %d lines, want %d", len(lines), end-start+1)
	}
	for offset, line := range lines {
		if want := fmt.Sprintf("line %d", start+offset); line != want {
			t.Fatalf("merged span line %d is %q, want %q (the range must be contiguous and verbatim)", offset, line, want)
		}
	}
}

// Two printed bodies of one file with a small hole between them are one region as far as the
// reader is concerned. Measured over 113 Opus payloads, 40% carry such a pair, and the agent
// closes the hole itself: fluentd-3328 had gold at rank 1 (complete-symbol, "Top hit is exact")
// and still cost 1.64x baseline because turn 2 was `sed -n '330,470p'` over a contiguous superset
// of ranks 1/2/3. The merge has to deliver the bridge, and has to say nothing was elided.
func TestMergeSameFileSearchSpansFoldsNearHitsIntoOneContiguousSpan(t *testing.T) {
	t.Parallel()
	read := spanMergeFile("in_tail.rb", 500)
	results := []SearchResult{
		spanMergeBody(1, "in_tail.rb", 10, 20),
		spanMergeBody(2, "in_tail.rb", 45, 55),
		spanMergeBody(3, "other.rb", 5, 9),
	}
	merged, spans, absorbed := mergeSameFileSearchSpans(results, read, 0)
	if spans != 1 || absorbed != 1 {
		t.Fatalf("merged %d spans absorbing %d hits, want 1 and 1", spans, absorbed)
	}
	if len(merged) != 2 {
		t.Fatalf("ranking holds %d results after the merge, want 2", len(merged))
	}
	span := merged[0]
	if span.SnippetStartLine != 10 || span.SnippetEndLine != 55 {
		t.Fatalf("merged span is %d-%d, want 10-55", span.SnippetStartLine, span.SnippetEndLine)
	}
	wantLineRange(t, span.Snippet, 10, 55)
	if got := fmt.Sprint(span.MergedRanks); got != "[1 2]" {
		t.Fatalf("merged span reports ranks %s, want [1 2]", got)
	}
	if !hasSearchSignal(span, searchSpanMergedSignal) {
		t.Fatalf("merged span is missing the %s signal: %v", searchSpanMergedSignal, span.Signals)
	}
	// The signal every renderer keys off to print a block in full must survive, or the header
	// would name a range whose text is not shown.
	if !hasSearchSignal(span, searchCompleteSymbolSignal) {
		t.Fatalf("merged span lost %s: %v", searchCompleteSymbolSignal, span.Signals)
	}
	// Order is untouched and ranks stay dense, which is the response contract Validate enforces.
	if merged[1].FilePath != "other.rb" {
		t.Fatalf("survivor order changed: rank 2 is %s, want other.rb", merged[1].FilePath)
	}
	for index, result := range merged {
		if result.Rank != index+1 {
			t.Fatalf("rank %d at index %d: the ranking must stay 1..N and dense", result.Rank, index)
		}
	}
	// StartLine <= SnippetStartLine <= SnippetEndLine <= EndLine, the invariant Validate checks.
	if span.StartLine > span.SnippetStartLine || span.SnippetEndLine > span.EndLine ||
		span.FocusLine < span.StartLine || span.FocusLine > span.EndLine {
		t.Fatalf("merged span breaks the region invariant: %+v", span)
	}
}

// The bounds are the whole reason this is safe to ship against a payload that already works
// (Haiku, -25.0%): filling gaps adds bytes, and added bytes under a fixed ceiling evict hits.
func TestMergeSameFileSearchSpansRespectsItsBounds(t *testing.T) {
	t.Parallel()
	read := spanMergeFile("in_tail.rb", 500)
	cases := []struct {
		name    string
		results []SearchResult
		budget  int
	}{
		{
			name: "a hole wider than the gap cap is not bridged",
			results: []SearchResult{
				spanMergeBody(1, "in_tail.rb", 10, 20),
				spanMergeBody(2, "in_tail.rb", 62, 72),
			},
		},
		{
			name: "a span longer than the span cap is not emitted",
			results: []SearchResult{
				spanMergeBody(1, "in_tail.rb", 10, 20),
				spanMergeBody(2, "in_tail.rb", 55, 95),
			},
		},
		{
			name: "a locator is never folded into a body",
			results: []SearchResult{
				spanMergeBody(1, "in_tail.rb", 10, 20),
				func() SearchResult {
					locator := spanMergeBody(2, "in_tail.rb", 45, 46)
					locator.Signals = []string{"path"}
					return locator
				}(),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			merged, spans, absorbed := mergeSameFileSearchSpans(testCase.results, read, testCase.budget)
			if spans != 0 || absorbed != 0 {
				t.Fatalf("merged %d spans absorbing %d hits, want none", spans, absorbed)
			}
			if len(merged) != len(testCase.results) {
				t.Fatalf("ranking holds %d results, want %d untouched", len(merged), len(testCase.results))
			}
			for index, result := range merged {
				if len(result.MergedRanks) > 0 {
					t.Fatalf("result %d was merged: %v", index, result.MergedRanks)
				}
			}
		})
	}
}

// The byte ceiling is checked against the WHOLE prospective ranking before a merge is committed,
// so a bridge can never be paid for by evicting the hit at the end of the payload.
func TestMergeSameFileSearchSpansFallsBackWhenTheBridgeWouldNotFit(t *testing.T) {
	t.Parallel()
	read := spanMergeFile("in_tail.rb", 500)
	results := []SearchResult{
		spanMergeBody(1, "in_tail.rb", 10, 20),
		spanMergeBody(2, "in_tail.rb", 45, 55),
	}
	unmergedBytes := serializedSearchResultBytes(results)
	if _, spans, _ := mergeSameFileSearchSpans(results, read, unmergedBytes); spans != 0 {
		t.Fatalf("merged under a budget that only fits the unmerged ranking (%d bytes)", unmergedBytes)
	}
	merged, spans, _ := mergeSameFileSearchSpans(results, read, 0)
	if spans != 1 {
		t.Fatalf("merged %d spans with no ceiling, want 1", spans)
	}
	if got := serializedSearchResultBytes(merged); got <= unmergedBytes {
		t.Fatalf("merged ranking is %d bytes against %d unmerged: the test's budget proves nothing", got, unmergedBytes)
	}
}

// Merging must not be able to make a file harder to find than it already was. The rank at which a
// file first appears is what the outcome is a step function of, so it may improve (its hits are
// now inside one earlier span) but never worsen.
func TestMergeSameFileSearchSpansNeverWorsensAFilesRank(t *testing.T) {
	t.Parallel()
	read := spanMergeFile("in_tail.rb", 500)
	results := []SearchResult{
		spanMergeBody(1, "parser.rb", 10, 20),
		spanMergeBody(2, "in_tail.rb", 10, 20),
		spanMergeBody(3, "in_tail.rb", 45, 55),
		spanMergeBody(4, "gold.rb", 30, 40),
	}
	before := firstSpanMergeRankByFile(results)
	merged, spans, _ := mergeSameFileSearchSpans(results, read, 0)
	if spans != 1 {
		t.Fatalf("merged %d spans, want 1", spans)
	}
	after := firstSpanMergeRankByFile(merged)
	for path, rank := range before {
		got, present := after[path]
		if !present {
			t.Fatalf("%s disappeared from the ranking", path)
		}
		if got > rank {
			t.Fatalf("%s moved from rank %d to rank %d", path, rank, got)
		}
	}
}

func firstSpanMergeRankByFile(results []SearchResult) map[string]int {
	first := map[string]int{}
	for _, result := range results {
		if _, seen := first[result.FilePath]; !seen {
			first[result.FilePath] = result.Rank
		}
	}
	return first
}
