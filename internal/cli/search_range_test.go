package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// The header above a printed snippet must describe the lines printed under it.
// It used to print the ranked REGION, which can start above the definition (a
// blank line, a doc comment), so one symbol appeared with two different
// definition lines in one session: `:779-848` from search against `:781` from a
// relation answer, with nothing saying which was which.
func TestSearchTextHeaderReportsThePrintedRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result sem.SearchResult
		want   string
	}{
		{
			name: "complete symbol body reports the symbol span, not the region",
			result: sem.SearchResult{
				Rank: 1, FilePath: "strings.rs", Score: 50.6,
				StartLine: 779, EndLine: 848,
				SnippetStartLine: 781, SnippetEndLine: 848,
				Snippet:  "pub(crate) fn target(",
				Signals:  []string{"body", sem.CompleteSymbolSignal},
				SymbolID: "sym", SymbolName: "target",
			},
			want: "1. strings.rs:781-848 score=50.6000",
		},
		{
			name: "a tersified snippet reports what it printed",
			result: sem.SearchResult{
				Rank: 2, FilePath: "expression.rs", Score: 46.07,
				StartLine: 400, EndLine: 500,
				SnippetStartLine: 474, SnippetEndLine: 478,
				Snippet: "line", Signals: []string{"body"},
			},
			want: "2. expression.rs:474-478 score=46.0700",
		},
		{
			name: "no snippet range falls back to the region",
			result: sem.SearchResult{
				Rank: 3, FilePath: "codes.rs", Score: 1,
				StartLine: 160, EndLine: 165,
				Snippet: "line", Signals: []string{"path"},
			},
			want: "3. codes.rs:160-165 score=1.0000",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			writeTextSearchResult(&out, testCase.result, true)
			if !strings.HasPrefix(out.String(), testCase.want) {
				t.Fatalf("header = %q, want prefix %q", out.String(), testCase.want)
			}
		})
	}
}

// The definition line a relation answer reports and the range a search result
// prints must agree for the same symbol, so a reader never has to reconcile two
// unexplained numbers.
func TestSearchAndRelationAgreeOnOneSymbolsLines(t *testing.T) {
	t.Parallel()
	symbol := sem.SymbolRecord{
		ID: "sym", Name: "target", Kind: "function",
		FilePath: "strings.rs", StartLine: 781, EndLine: 848, Language: "Rust",
	}
	var searchOut bytes.Buffer
	writeTextSearchResult(&searchOut, sem.SearchResult{
		Rank: 1, FilePath: symbol.FilePath, Score: 1,
		// The ranked region deliberately starts above the definition.
		StartLine: 779, EndLine: symbol.EndLine,
		SnippetStartLine: symbol.StartLine, SnippetEndLine: symbol.EndLine,
		Snippet: "body", Signals: []string{sem.CompleteSymbolSignal},
	}, true)
	relation := formatNeighborFocus(endpointForSymbol(symbol))

	if !strings.Contains(searchOut.String(), "strings.rs:781-848") {
		t.Fatalf("search header = %q", searchOut.String())
	}
	if !strings.Contains(relation, "strings.rs:781") ||
		!strings.Contains(relation, "def=781 span=781-848") {
		t.Fatalf("relation focus = %q", relation)
	}
	if strings.Contains(searchOut.String(), ":779-") {
		t.Fatalf("search still reports a line it did not print: %q", searchOut.String())
	}
}
