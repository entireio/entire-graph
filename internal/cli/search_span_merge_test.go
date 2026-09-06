package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A merged span is only worth its bytes if the header says the range is contiguous. Measured on
// fluentd-3328: gold was at rank 1, complete-symbol, and the agent said "Top hit is exact" — then
// spent turn 2 on `sed -n '200,270p'; sed -n '330,470p'`, 6.9 kB re-reading a superset of ranks
// 1/2/3, because three disjoint spans of one file look like an excerpt with holes in it.
func TestSearchTextLabelsAMergedSpanAsContiguous(t *testing.T) {
	t.Parallel()
	result := sem.SearchResult{
		Rank: 1, FilePath: "lib/fluent/plugin/in_tail.rb", Score: 135.8,
		StartLine: 349, EndLine: 425,
		SnippetStartLine: 349, SnippetEndLine: 425,
		FocusLine:   395,
		Snippet:     "def receive_lines",
		Signals:     []string{"body", sem.CompleteSymbolSignal, "contiguous-span"},
		SymbolName:  "receive_lines",
		MergedRanks: []int{1, 2},
	}
	var out bytes.Buffer
	writeTextSearchResult(&out, result, true)
	want := "1. lib/fluent/plugin/in_tail.rb:349-425 [contains ranks 1,2 - contiguous, nothing elided] score=135.8000"
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("merged span header is\n%s\nwant prefix\n%s", out.String(), want)
	}
}

// An ordinary hit must be byte-identical to what it was before merging existed: the arm this
// change is measured against has to differ only where a merge actually happened.
func TestSearchTextLeavesAnUnmergedHitUnannotated(t *testing.T) {
	t.Parallel()
	result := sem.SearchResult{
		Rank: 2, FilePath: "pylint/lint/expand_modules.py", Score: 46.07,
		StartLine: 400, EndLine: 500,
		SnippetStartLine: 474, SnippetEndLine: 478,
		FocusLine: 474,
		Snippet:   "def expand_modules(",
		Signals:   []string{"body"},
	}
	var out bytes.Buffer
	writeTextSearchResult(&out, result, true)
	if strings.Contains(out.String(), "contains ranks") {
		t.Fatalf("an unmerged hit was annotated:\n%s", out.String())
	}
	want := "2. pylint/lint/expand_modules.py:474-478 score=46.0700"
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("unmerged header is\n%s\nwant prefix\n%s", out.String(), want)
	}
}
