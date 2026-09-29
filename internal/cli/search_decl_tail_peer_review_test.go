package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// Derived from Claude's TestRev2TailRowMinimalRungNamesUnprintedFocus, with
// explicit coverage checks. An empty/header-only answer is not a recovery fix.
func TestReview303PeerTailMinimalHeaderCoordinates(t *testing.T) {
	result := sem.SearchResult{
		Rank: 7, Score: 30, FilePath: "internal/indexer/indexer.go",
		StartLine: 191, EndLine: 199, FocusLine: 198,
		SnippetStartLine: 194, SnippetEndLine: 195,
		SymbolStartLine: 194, SymbolEndLine: 199, SymbolName: "resolveNamespace", QualifiedName: "Indexer.resolveNamespace",
		Snippet: "func (i *Indexer) resolveNamespace(repoID string) string {\n\tif i.override != \"\" {",
	}
	file := strings.Split(result.Snippet, "\n")
	bad, checked, minimal := 0, 0, 0
	for budget := 30; budget <= 400; budget++ {
		block := agentSearchPrimaryBlock(searchResultOnOneLine(result), budget)
		numbers, sources, _, ok := declRecoverLineNumbers(block)
		if !ok || len(numbers) == 0 {
			continue
		}
		checked++
		if declHeaderMinimal.MatchString(strings.SplitN(string(block), "\n", 2)[0]) {
			minimal++
		}
		for i, n := range numbers {
			idx := n - result.SnippetStartLine
			if idx < 0 || idx >= len(file) || file[idx] != sources[i] {
				if bad == 0 {
					t.Logf("budget %d: source %q is attributed to line %d:\n%s", budget, sources[i], n, block)
				}
				bad++
				break
			}
		}
	}
	if checked == 0 || minimal == 0 {
		t.Fatalf("recovery check did not exercise source-bearing output: checked=%d minimal=%d", checked, minimal)
	}
	t.Logf("checked %d source-bearing outputs, %d minimal headers, %d misnumbered", checked, minimal, bad)
	if bad > 0 {
		t.Errorf("%d budgets assign incorrect file coordinates to printed source", bad)
	}
}
