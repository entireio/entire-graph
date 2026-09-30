package cli

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// This supplements the unknown-coordinate regression with truthful parser
// metadata. Both authored declaration names are on file line 1. The observable
// requirement remains preservation of row 2's previously visible source, not
// merely its locator, within the same final escaped-byte budget.
func TestReview303ParsedNameEscapedDeclarationPreservesOtherRow(t *testing.T) {
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
			results[0].SymbolNameLine = 1
			results[1].SymbolNameLine = 1
			secondDeclaration := "func second() {} // " + strings.Repeat("x", 180)

			// Use the actual old focus-window helper at the actual ranked
			// shares to establish the visible-source premise independently.
			shares := rankedAgentSearchBudgets(len(results), budget-1)
			var blocks [][]byte
			for i, result := range results {
				view := agentSearchBlockViewOf(searchResultOnOneLine(result))
				plain, _, _ := agentSearchFocusWindow(view, shares[i])
				if len(plain) == 0 {
					t.Fatalf("old focus window for row %d is empty at %d bytes", i+1, shares[i])
				}
				blocks = append(blocks, plain)
			}
			previous := termsafe.Bytes(bytes.Join(blocks, []byte("\n")))
			t.Logf("old windows, actual ranked shares %v: %d/%d escaped bytes\n%s", shares, len(previous), budget, previous)
			if len(previous) > budget || !bytes.Contains(previous, []byte("\n"+secondDeclaration+"\n")) {
				t.Fatalf("premise failed: old windows must show row 2's declaration within %d bytes:\n%s", budget, previous)
			}

			got := fitAgentSearchResults(results, budget)
			t.Logf("parsed-name final ranking: %d/%d bytes\n%s", len(got), budget, got)
			if len(got) == 0 || len(got) > budget {
				t.Fatalf("expected a nonempty ranking within %d bytes, got %d", budget, len(got))
			}
			if bytes.ContainsRune(got, '\x1b') || !bytes.Equal(got, termsafe.Bytes(got)) {
				t.Errorf("final ranking is not terminal-escaped: %q", got)
			}
			for _, locator := range []string{"src/a.go:", "src/b.go:1"} {
				if !bytes.Contains(got, []byte(locator)) {
					t.Fatalf("expected retained row locator %q:\n%s", locator, got)
				}
			}
			if !bytes.Contains(got, []byte("\n"+secondDeclaration+"\n")) {
				t.Errorf("row 2's locator survived but its previously visible source declaration did not:\n%s", got)
			}
		})
	}
}

// The parser coordinate is file line 3, not the focused raw-string line 54.
// The literal source, budgets, coordinates, and positive control are unchanged
// from the unknown-coordinate regression. Use PR303's existing recovery helper
// and also assert the observable source/tool-record byte collision directly.
func TestReview303ParsedNameLiteralElisionPreservesSourceCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string
	}{
		{name: "ordinary_raw_string_control", marker: "literal marker text"},
		{name: "literal_elision_record", marker: "... 50 lines elided"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// File lines: declaration 3; long body 4..52; raw-string open
			// 53; marker 54; following literal 55; closing string/function
			// 56/57. A real parser checks that the authored source is valid.
			lines := []string{"func target() {"}
			for i := 0; i < 49; i++ {
				lines = append(lines, "\t_ = 0 // "+strings.Repeat("x", 300))
			}
			lines = append(lines, "\t_ = `"+strings.Repeat("opening", 50),
				tc.marker, "marker payload continues", "`", "}")
			snippet := strings.Join(lines, "\n") + "\n"
			if _, err := parser.ParseFile(token.NewFileSet(), "src/marker.go", "package fixture\n\n"+snippet, parser.AllErrors); err != nil {
				t.Fatalf("fixture must be syntactically valid Go: %v", err)
			}
			result := sem.SearchResult{
				Rank: 1, Score: 10, FilePath: "src/marker.go",
				StartLine: 3, EndLine: 57, FocusLine: 54,
				SnippetStartLine: 3, SnippetEndLine: 57,
				SymbolStartLine: 3, SymbolEndLine: 57, SymbolNameLine: 3,
				SymbolName: "target", QualifiedName: "target", Snippet: snippet,
			}
			quarantined, changed := searchQuarantineBody(snippet)
			t.Logf("production quarantine changed=%v; column-0 fixture marker retained=%v",
				changed, strings.Contains(quarantined, "\n"+tc.marker+"\n"))
			for _, width := range []struct {
				name   string
				budget int
			}{
				{name: "rich", budget: 180},
				{name: "compact", budget: 125},
				{name: "minimal", budget: 110},
			} {
				t.Run(width.name, func(t *testing.T) {
					got := fitAgentSearchResults([]sem.SearchResult{result}, width.budget)
					t.Logf("parsed-name delivered block: %d/%d bytes\n%s", len(got), width.budget, got)
					if len(got) == 0 || len(got) > width.budget {
						t.Fatalf("expected a nonempty block within %d bytes, got %d", width.budget, len(got))
					}
					if !bytes.Contains(got, []byte("\nfunc target() {\n")) ||
						!bytes.Contains(got, []byte("\nmarker payload continues\n")) {
						t.Fatalf("fixture must print both the declaration and focused literal:\n%s", got)
					}
					records := 0
					for _, line := range strings.Split(string(got), "\n") {
						if line == "... 50 lines elided" {
							records++
						}
					}
					if records > 1 {
						t.Errorf("renderer metadata and file line 54 have byte-identical column-0 gap records; source and elision are indistinguishable:\n%s", got)
					}
					numbers, sources, last, ok := declRecoverLineNumbers(got)
					if !ok {
						t.Fatalf("delivered header does not permit source coordinate recovery:\n%s", got)
					}
					markerLine, followingLine := -1, -1
					for i, source := range sources {
						// Permit the existing quarantine's disclosed leading space.
						if source == tc.marker || source == " "+tc.marker {
							markerLine = numbers[i]
						}
						if source == "marker payload continues" {
							followingLine = numbers[i]
						}
					}
					if markerLine != 54 || followingLine != 55 {
						t.Errorf("source provenance lost: marker/following recovered at %d/%d, want literal file lines 54/55:\n%s", markerLine, followingLine, got)
					}
					if last >= 0 && last != 57 {
						t.Errorf("range header ends at %d, want actual file line 57", last)
					}
				})
			}
		})
	}
}
