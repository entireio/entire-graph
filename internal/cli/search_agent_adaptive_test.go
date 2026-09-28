package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// adaptiveFixture is a ranking whose head body is headLines long, followed by nine results each
// carrying a body large enough that, together, they would fill any budget they were offered.
func adaptiveFixture(headLines int) sem.SearchResponse {
	body := func(name string, lines int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "func %s() {\n", name)
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, "\tstep%02d := compute(%q, %d)\n", i, name, i)
		}
		b.WriteString("}\n")
		return b.String()
	}
	results := []sem.SearchResult{{
		Rank: 1, FilePath: "pkg/head.go", SymbolName: "Head", Score: 90,
		StartLine: 10, EndLine: 10 + headLines + 1, SnippetStartLine: 10, FocusLine: 10,
		Snippet: body("Head", headLines),
	}}
	for rank := 2; rank <= 10; rank++ {
		name := fmt.Sprintf("Tail%d", rank)
		results = append(results, sem.SearchResult{
			Rank: rank, FilePath: fmt.Sprintf("pkg/tail%d.go", rank), SymbolName: name, Score: float64(60 - rank),
			StartLine: 1, EndLine: 42, SnippetStartLine: 1, FocusLine: 1, Snippet: body(name, 40),
		})
	}
	return sem.SearchResponse{Results: results}
}

func adaptiveHeadBlock(t *testing.T, response sem.SearchResponse) []byte {
	t.Helper()
	head := agentSearchBlock(orderAgentSearchResults(response.Results)[0], 0)
	if !bytes.HasPrefix(head, []byte("1. pkg/head.go:10-")) {
		t.Fatalf("fixture head block is not the rank-1 result: %q", head)
	}
	return head
}

// A short head hit is rendered at the smallest budget, with the head exactly as an unbounded
// render shows it, and far below the ceiling the tail bodies would otherwise have filled.
func TestAgentSearchAdaptiveUsesSmallestBudgetThatKeepsHeadWhole(t *testing.T) {
	t.Parallel()
	response := adaptiveFixture(6)
	head := adaptiveHeadBlock(t, response)

	var adaptive, ceiling bytes.Buffer
	if err := writeAgentSearchAdaptive(&adaptive, response, defaultSearchContextBytes); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentSearch(&ceiling, response, defaultSearchContextBytes); err != nil {
		t.Fatal(err)
	}
	if adaptive.Len() > agentSearchAdaptiveBudgets[0] {
		t.Fatalf("short head used %d bytes, want <= %d", adaptive.Len(), agentSearchAdaptiveBudgets[0])
	}
	if !agentSearchPayloadCarriesBlock(adaptive.Bytes(), head) {
		t.Fatalf("head not whole:\n%s", adaptive.String())
	}
	// Non-vacuity: the ceiling render really is larger, so choosing it would have cost bytes.
	if ceiling.Len() <= agentSearchAdaptiveBudgets[0] {
		t.Fatalf("fixture tail too small to exercise the choice: ceiling render %d bytes", ceiling.Len())
	}
	// Every ranked location survives the smaller budget.
	for rank := 2; rank <= 10; rank++ {
		if !strings.Contains(adaptive.String(), fmt.Sprintf("%d. pkg/tail%d.go:", rank, rank)) {
			t.Fatalf("rank %d location dropped:\n%s", rank, adaptive.String())
		}
	}
}

// A head hit too long for the smallest budget is NOT clipped: the next budget that shows it
// whole is used instead.
func TestAgentSearchAdaptiveGrowsRatherThanClipTheHead(t *testing.T) {
	t.Parallel()
	response := adaptiveFixture(150)
	head := adaptiveHeadBlock(t, response)
	if len(head) <= agentSearchAdaptiveBudgets[0] {
		t.Fatalf("fixture head (%d bytes) fits the smallest budget; test proves nothing", len(head))
	}
	var small bytes.Buffer
	if err := writeAgentSearch(&small, response, agentSearchAdaptiveBudgets[0]); err != nil {
		t.Fatal(err)
	}
	if agentSearchPayloadCarriesBlock(small.Bytes(), head) {
		t.Fatal("smallest budget already carries the whole head; test proves nothing")
	}

	var adaptive bytes.Buffer
	if err := writeAgentSearchAdaptive(&adaptive, response, defaultSearchContextBytes); err != nil {
		t.Fatal(err)
	}
	if !agentSearchPayloadCarriesBlock(adaptive.Bytes(), head) {
		t.Fatalf("head clipped (%d bytes):\n%s", adaptive.Len(), adaptive.String())
	}
	if adaptive.Len() > defaultSearchContextBytes {
		t.Fatalf("used %d bytes, ceiling %d", adaptive.Len(), defaultSearchContextBytes)
	}
}

// A clipped head is not "whole": its header names the shorter displayed range, so it cannot
// match the unbounded block byte for byte even though every line it shows is the head's.
func TestAgentSearchPayloadCarriesBlockRejectsClippedHead(t *testing.T) {
	t.Parallel()
	block := []byte("1. a.go:1-3 F s=1.0 [focus:1]\nfunc F() {\n\tx()\n}\n")
	clipped := []byte("I:hit/1\n1. a.go:1-2 F s=1.0 [focus:1]\nfunc F() {\n\tx()\n")
	if agentSearchPayloadCarriesBlock(clipped, block) {
		t.Fatal("clipped head accepted as whole")
	}
	if !agentSearchPayloadCarriesBlock(append([]byte("I:hit/1\n"), block...), block) {
		t.Fatal("whole head at a line start rejected")
	}
	if agentSearchPayloadCarriesBlock([]byte("x"+string(block)), block) {
		t.Fatal("head accepted mid-line")
	}
}

// An explicit --max-context-bytes is honoured exactly; only the unset default is adaptive.
func TestSearchExplicitContextBudgetIsNotAdaptive(t *testing.T) {
	t.Parallel()
	flags, _, err := parseSearchFlags([]string{"--query", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.MaxContextBytesSet {
		t.Fatal("unset budget reported as explicit")
	}
	flags, _, err = parseSearchFlags([]string{"--query", "x", "--max-context-bytes", "24576"})
	if err != nil {
		t.Fatal(err)
	}
	if !flags.MaxContextBytesSet || flags.MaxContextBytes != 24576 {
		t.Fatalf("explicit budget not recorded: %+v", flags)
	}
}

// Every ranked result keeps its rich location line, and what is left goes to rank 1 first.
func TestAgentSearchResultBudgetsFloorEveryLocationAndFavourTheHead(t *testing.T) {
	t.Parallel()
	results := adaptiveFixture(6).Results
	floors := 0
	for _, result := range results {
		floors += agentSearchLocationCost(result)
	}
	budget := floors + 1000
	budgets := agentSearchResultBudgets(results, budget)
	total := 0
	for index, got := range budgets {
		total += got
		if floor := agentSearchLocationCost(results[index]); got < floor {
			t.Fatalf("rank %d budget %d below its location cost %d", index+1, got, floor)
		}
	}
	if total != budget {
		t.Fatalf("budgets sum to %d, want %d", total, budget)
	}
	headExtra := budgets[0] - agentSearchLocationCost(results[0])
	if headExtra*2 < 1000 {
		t.Fatalf("rank 1 got %d of 1000 spare bytes, want at least half: %v", headExtra, budgets)
	}
	// And the rendered block really keeps every location at that budget.
	rendered := string(renderAgentSearchResults(results, budgets))
	for rank := 1; rank <= 10; rank++ {
		if !strings.Contains(rendered, fmt.Sprintf("\n%d. pkg/", rank)) && !strings.HasPrefix(rendered, fmt.Sprintf("%d. pkg/", rank)) {
			t.Fatalf("rank %d location missing at budget %d:\n%s", rank, budget, rendered)
		}
	}
}
