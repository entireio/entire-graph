package cli

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
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
	// Rank 1 is either whole or holds at least half the spare bytes.
	headNeed := len(agentSearchBlock(results[0], 0))
	if headExtra := budgets[0] - agentSearchLocationCost(results[0]); budgets[0] < headNeed && headExtra*2 < 1000 {
		t.Fatalf("rank 1 got %d of 1000 spare bytes and is not whole (%d < %d): %v", headExtra, budgets[0], headNeed, budgets)
	}
	// No bytes are parked on a result that cannot use them while another is short.
	for index, got := range budgets {
		if need := len(agentSearchBlock(results[index], 0)); got > need+len(budgets) {
			for other, short := range budgets {
				if short < len(agentSearchBlock(results[other], 0)) {
					t.Fatalf("rank %d holds %d > its need %d while rank %d is short: %v", index+1, got, need, other+1, budgets)
				}
			}
		}
	}
	// And the rendered block really keeps every location at that budget.
	rendered := string(renderAgentSearchResults(results, budgets))
	for rank := 1; rank <= 10; rank++ {
		if !strings.Contains(rendered, fmt.Sprintf("\n%d. pkg/", rank)) && !strings.HasPrefix(rendered, fmt.Sprintf("%d. pkg/", rank)) {
			t.Fatalf("rank %d location missing at budget %d:\n%s", rank, budget, rendered)
		}
	}
}

// Section grouping keeps rank labels but reorders blocks: a rank-1 docs hit prints after a
// rank-2 primary fix site. The adaptive render must keep the ABSOLUTE rank 1 whole, not merely
// the first block printed. (Regression supplied by peer review at 14111e30: a 941-byte render was
// accepted with rank 1 clipped.)
func TestAgentSearchAdaptiveKeepsAbsoluteRankOneWholeAfterSectionOrdering(t *testing.T) {
	t.Parallel()
	docsBody := strings.Repeat("documented behavior with enough detail to exceed the smallest adaptive share\n", 160)
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{
			Rank: 1, FilePath: "docs/guide.md", SymbolName: "Guide", Score: 90,
			StartLine: 1, EndLine: 160, SnippetStartLine: 1, SnippetEndLine: 160, FocusLine: 1,
			Snippet: docsBody, Section: sem.SearchSectionDocs,
		},
		{
			Rank: 2, FilePath: "pkg/fix.go", SymbolName: "Fix", Score: 80,
			StartLine: 1, EndLine: 1, SnippetStartLine: 1, SnippetEndLine: 1, FocusLine: 1,
			Snippet: "func Fix() {}\n",
		},
	}}
	if first := orderAgentSearchResults(response.Results)[0]; first.Rank == 1 {
		t.Fatal("fixture no longer reorders rank 1 behind a lower rank; test proves nothing")
	}
	rankOne := termsafe.Bytes(agentSearchBlock(response.Results[0], 0))
	var small bytes.Buffer
	if err := writeAgentSearch(&small, response, agentSearchAdaptiveBudgets[0]); err != nil {
		t.Fatal(err)
	}
	if agentSearchPayloadCarriesBlock(small.Bytes(), rankOne) {
		t.Fatal("smallest budget already carries rank 1 whole; test proves nothing")
	}
	var got bytes.Buffer
	if err := writeAgentSearchAdaptive(&got, response, defaultSearchContextBytes); err != nil {
		t.Fatal(err)
	}
	if !agentSearchPayloadCarriesBlock(got.Bytes(), rankOne) {
		t.Fatalf("adaptive output (%d bytes) clipped absolute rank 1", got.Len())
	}
}

// The share arithmetic must not overflow for the largest budget the flag parser accepts, and the
// byte-at-a-time top-up must stay bounded (it ran for billions of iterations after an overflow).
func TestAgentSearchResultBudgetsSurviveTheLargestBudget(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 2, 3, 10} {
		results := adaptiveFixture(6).Results[:count]
		budgets := agentSearchResultBudgets(results, math.MaxInt)
		total := 0
		for index, got := range budgets {
			if got < agentSearchLocationCost(results[index]) {
				t.Fatalf("count %d rank %d: budget %d below its location cost", count, index+1, got)
			}
			if total > math.MaxInt-got {
				t.Fatalf("count %d: budgets overflow when summed: %v", count, budgets)
			}
			total += got
		}
		if total != math.MaxInt {
			t.Fatalf("count %d: budgets sum to %d, want %d", count, total, math.MaxInt)
		}
		if count > 1 && budgets[0] <= budgets[1] {
			t.Fatalf("count %d: head not favoured: %v", count, budgets)
		}
	}
}

// Through the real command: with no --max-context-bytes the agent render stays at the smallest
// budget, and an explicit --max-context-bytes of the same ceiling value is honoured instead.
func TestSearchCommandExplicitBudgetBypassesAdaptive(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for i := 0; i < 12; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "package p\n\n// refreshToken%d renews the session token.\nfunc refreshToken%d() {\n", i, i)
		for j := 0; j < 40; j++ {
			fmt.Fprintf(&b, "\tstep%02d := renewSessionToken(%d, %d)\n\t_ = step%02d\n", j, i, j, j)
		}
		b.WriteString("}\n")
		write(t, repo, fmt.Sprintf("f%02d.go", i), b.String())
	}
	run := func(extra ...string) string {
		t.Helper()
		var out bytes.Buffer
		args := append([]string{"search", "--repo", repo, "--query", "refresh session token",
			"--format", "agent", "--profile", "syntax-only", "--worktree"}, extra...)
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	adaptive := run()
	explicit := run("--max-context-bytes", fmt.Sprint(defaultSearchContextBytes))
	if len(adaptive) > agentSearchAdaptiveBudgets[0] {
		t.Fatalf("unset budget was not adaptive: %d bytes", len(adaptive))
	}
	if len(explicit) <= agentSearchAdaptiveBudgets[0] {
		t.Fatalf("explicit %d-byte budget rendered only %d bytes; fixture does not exercise dispatch", defaultSearchContextBytes, len(explicit))
	}
}

// With no spare bytes every result gets exactly its location floor, and at that size each must
// still print as a ranked location — rank, name, score — rather than a bare `path:line *` over one
// line of source, which fits the same bytes but drops the result's identity.
func TestAgentSearchFloorOnlyBudgetKeepsEveryRankedLocation(t *testing.T) {
	t.Parallel()
	results := adaptiveFixture(6).Results
	floors := 0
	for _, result := range results {
		floors += agentSearchLocationCost(result)
	}
	budgets := agentSearchResultBudgets(results, floors)
	for index, got := range budgets {
		if want := agentSearchLocationCost(results[index]); got != want {
			t.Fatalf("rank %d: budget %d, want exactly its floor %d", index+1, got, want)
		}
	}
	// Non-vacuity: a bare locator plus one source line fits the tail floor, so the renderer has
	// a real choice to get wrong.
	tail := results[len(results)-1]
	if bare := len(fmt.Sprintf("%s:1 *\n", tail.FilePath)) + len("func Tail10() {\n"); bare > budgets[len(budgets)-1] {
		t.Fatalf("fixture drift: bare locator + one line (%d) no longer fits the floor %d", bare, budgets[len(budgets)-1])
	}
	rendered := string(renderAgentSearchResults(results, budgets))
	for rank := 1; rank <= len(results); rank++ {
		prefix := fmt.Sprintf("%d. pkg/", rank)
		if !strings.HasPrefix(rendered, prefix) && !strings.Contains(rendered, "\n"+prefix) {
			t.Fatalf("rank %d lost its ranked location at its floor:\n%s", rank, rendered)
		}
	}
}
