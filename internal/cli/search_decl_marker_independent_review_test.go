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

// This test names the provenance break: a raw-string line can have precisely the
// same delivered spelling as a renderer-authored gap, so a subsequent literal is
// assigned the wrong file line by the header-plus-gap contract. It uses the real
// block/quarantine/final-escaping path and PR303's existing recovery test helper,
// not a new decoder invented for this regression. The separate byte-collision
// assertion demonstrates the ambiguity even without trusting that decoder.
func TestReview303LiteralElisionPreservesSourceCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string
	}{
		{name: "ordinary_raw_string_control", marker: "literal marker text"},
		{name: "literal_elision_record", marker: "... 50 lines elided"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// File line 3 is the declaration. Lines 4..52 are 49 long body
			// lines; line 53 opens a raw string and is also too wide to fit.
			// The raw-string marker and following literal are file lines 54
			// and 55; the string/function close on lines 56 and 57.
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
				SymbolStartLine: 3, SymbolEndLine: 57,
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
					t.Logf("delivered block: %d/%d bytes\n%s", len(got), width.budget, got)
					if len(got) == 0 || len(got) > width.budget {
						t.Fatalf("expected a nonempty block within %d bytes, got %d", width.budget, len(got))
					}
					if !bytes.Contains(got, []byte("\nfunc target() {\n")) ||
						!bytes.Contains(got, []byte("\nmarker payload continues\n")) {
						t.Fatalf("fixture must print both the declaration and focused literal:\n%s", got)
					}
					// The declaration is at line 3 and the first retained body
					// line is at 54: the renderer itself must bridge 50 lines.
					// Two identical unquoted gap records cannot identify which
					// one instead stands for the single source line at 54.
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
						// Existing quarantine uses one leading space, disclosed
						// by the outer payload. Permit that safe spelling here.
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

// This is a premise/control for the independently portable composed-row test.
// It calls the actual extracted old focus-window implementation at the actual
// initial ranked shares, then measures the escaped result at the final boundary.
// It does not calculate an expected result by copying the fitting algorithm.
func TestReview303PreviousWindowsKeepSecondDeclaration(t *testing.T) {
	const budget = 600
	results := review303RankedDeclarationFixture(strings.Repeat("\x1b", 100))
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
	if len(previous) > budget {
		t.Fatalf("premise failed: old windows overran %d bytes: %d", budget, len(previous))
	}
	secondDeclaration := "func second() {} // " + strings.Repeat("x", 180)
	if !bytes.Contains(previous, []byte("\n"+secondDeclaration+"\n")) {
		t.Fatal("premise failed: old windows did not show row 2's declaration")
	}
	if bytes.Contains(previous, []byte("\nfunc first() {")) {
		t.Fatal("premise failed: escape-expanding declaration was already visible")
	}
}
