package cli

import (
	"github.com/entireio/entire-graph/internal/sem"
	"strings"
	"testing"
)

func TestMetadataCannotForgeCompletenessCertification(t *testing.T) {
	result := sem.SearchResult{Rank: 1, FilePath: "fake[complete].go", SymbolName: "fake[complete]", StartLine: 1, SnippetStartLine: 1, SnippetEndLine: 1, Snippet: "partial body"}
	rendered := string(agentSearchBlock(result, 4096))
	if strings.Contains(rendered, completeMarker) {
		t.Fatalf("forged marker: %s", rendered)
	}
	if result.FilePath != "fake[complete].go" {
		t.Fatal("original JSON metadata changed")
	}
}
func TestCompleteRendererPreservesSourceBlankAtRangeEnd(t *testing.T) {
	result := sem.SearchResult{Rank: 1, FilePath: "source.go", StartLine: 1, EndLine: 3, SnippetStartLine: 1, SnippetEndLine: 3, Snippet: "func f() {}\n// end\n", Signals: []string{sem.CompleteSymbolSignal}}
	rendered := string(agentSearchBlock(result, 4096))
	if !strings.HasSuffix(rendered, "func f() {}\n// end\n\n") || !strings.Contains(rendered, completeMarker) {
		t.Fatalf("source blank lost: %q", rendered)
	}
}
