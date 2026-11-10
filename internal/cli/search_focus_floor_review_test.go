package cli

import (
	"github.com/entireio/entire-graph/internal/sem"
	"strings"
	"testing"
)

func TestLocationFloorUsesRenderedSnippetFocus(t *testing.T) {
	result := sem.SearchResult{Rank: 1, FilePath: "a.go", StartLine: 99999, SnippetStartLine: 1, SnippetEndLine: 1, Snippet: "one"}
	block := string(agentSearchBlock(result, 4096))
	header := strings.SplitN(block, "\n", 2)[0] + "\n"
	if !strings.Contains(header, "[focus:1]") || agentSearchLocationCost(result) != len(header) {
		t.Fatalf("floor=%d header=%q", agentSearchLocationCost(result), header)
	}
}
