package sem

import (
	"strings"
	"testing"
)

// TestAllocateSearchSnippetsProtectsTheLocatorHeadIndependently pins the split that --body-head-ranks
// depends on.
//
// The BODY head ("who may be shown a whole callable") and the LOCATOR head ("who must at least keep
// the snippet the ranker produced") answer different questions, and --body-head-ranks moves only the
// first. The measurement it was added for — on sonnet, ranks 3-5 were read 0%/7%/0% while being
// charged for complete bodies — says nothing about demoting those ranks to two-line locators, and
// the opposite measurement applies there: a tail entry is the only mention of its file and the gold
// file sits at rank 6-8 on 17% of instances.
//
// Passing one value for both limits made `--body-head-ranks=2` tersify ranks 3-5 as a side effect,
// which is why the two are separate parameters.
func TestAllocateSearchSnippetsProtectsTheLocatorHeadIndependently(t *testing.T) {
	t.Parallel()
	results, enclosures, _ := makeAllocatorResults(8, 6, 30)
	// A ceiling that seats both head bodies only by demoting a tail, so the allocator is actually
	// choosing a cut rather than taking the no-demotion plan.
	const hardBudget, growth = 3200, 100_000

	split, _, _ := allocateSearchSnippets(results, enclosures, nil, hardBudget, growth, 2, 5, 2)
	for index := 0; index < 5; index++ {
		if lines := snippetLineCount(split[index]); lines < 6 {
			t.Fatalf("rank %d was demoted to %d lines: --body-head-ranks must not move the locator head",
				index+1, lines)
		}
	}

	// The parameter is load-bearing rather than incidental: with the same budget and the same body
	// head, a protected head of 2 does demote inside 3-5. If this stops holding the assertion above
	// has stopped testing anything.
	collapsed, _, _ := allocateSearchSnippets(results, enclosures, nil, hardBudget, growth, 2, 2, 2)
	demoted := false
	for index := 2; index < 5; index++ {
		if snippetLineCount(collapsed[index]) < 6 {
			demoted = true
		}
	}
	if !demoted {
		t.Fatal("the budget no longer forces demotion inside ranks 3-5, so the guard above is vacuous")
	}
}

func snippetLineCount(result SearchResult) int {
	if result.Snippet == "" {
		return 0
	}
	return len(strings.Split(result.Snippet, "\n"))
}
