package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// This file deliberately uses only helpers available before PR303, so the same
// test can be run byte-identically on PR302. The independent expectation is the
// visible second declaration, not a copy of either version's fitting algorithm.
// The break it catches is a newly included, escape-expanding declaration making
// another row lose source that previously fitted within the same final byte cap.
func TestReview303EscapedDeclarationPreservesOtherRow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		comment string
	}{
		{name: "ascii_control", comment: strings.Repeat("x", 100)},
		{name: "escaped_declaration", comment: strings.Repeat("\x1b", 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const budget = 600
			results := review303RankedDeclarationFixture(tc.comment)
			got := fitAgentSearchResults(results, budget)
			t.Logf("final ranking: %d/%d bytes\n%s", len(got), budget, got)
			if len(got) == 0 || len(got) > budget {
				t.Fatalf("expected a nonempty ranking within %d bytes, got %d", budget, len(got))
			}
			if bytes.ContainsRune(got, '\x1b') || !bytes.Equal(got, termsafe.Bytes(got)) {
				t.Errorf("final ranking is not terminal-escaped: %q", got)
			}
			// Losing a declaration is distinct from dropping its entire row. Both
			// locators must survive before the source-preservation assertion below.
			for _, locator := range []string{"src/a.go:", "src/b.go:1"} {
				if !bytes.Contains(got, []byte(locator)) {
					t.Fatalf("expected retained row locator %q:\n%s", locator, got)
				}
			}
			secondDeclaration := "func second() {} // " + strings.Repeat("x", 180)
			if !bytes.Contains(got, []byte("\n"+secondDeclaration+"\n")) {
				t.Errorf("row 2's locator survived but its previously visible source declaration did not:\n%s", got)
			}
		})
	}
}

func review303RankedDeclarationFixture(comment string) []sem.SearchResult {
	first := "func first() { // " + comment + "\n"
	first += strings.Repeat("\t_ = 0 // "+strings.Repeat("x", 50)+"\n", 10)
	first += "}\n"
	return []sem.SearchResult{
		{
			Rank: 1, Score: 10, FilePath: "src/a.go",
			StartLine: 1, EndLine: 12, FocusLine: 9,
			SnippetStartLine: 1, SnippetEndLine: 12,
			SymbolStartLine: 1, SymbolEndLine: 12,
			SymbolName: "first", QualifiedName: "first", Snippet: first,
		},
		{
			Rank: 2, Score: 10, FilePath: "src/b.go",
			StartLine: 1, EndLine: 1, FocusLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 1,
			SymbolStartLine: 1, SymbolEndLine: 1,
			SymbolName: "second", QualifiedName: "second",
			Snippet: "func second() {} // " + strings.Repeat("x", 180) + "\n",
		},
	}
}
