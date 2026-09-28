package sem

import (
	"strings"
	"testing"
)

func TestSearchAuxiliaryBlocksShareResultSlots(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		coveringFirst, distinctRelatedFiles bool
		wantRelated                         int
	}{
		{name: "related first with distinct files", distinctRelatedFiles: true, wantRelated: 2},
		{name: "covering first with distinct files", coveringFirst: true, distinctRelatedFiles: true, wantRelated: 2},
		{name: "covering may replace a redundant related locator", wantRelated: 1},
		{name: "related cannot replace covering test", coveringFirst: true, wantRelated: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := mergeTestResults(10, "head source")
			for _, index := range []int{5, 7, 8} {
				results[index].FilePath = results[0].FilePath
				results[index].Snippet = strings.Repeat("x", 1200)
			}
			entry := contractTestEntry()
			sites := []searchRelatedSite{mergeTestSite("alpha", 3), mergeTestSite("beta", 11)}
			fixture := cloneFamilyFixture()
			if tc.distinctRelatedFiles {
				sites[1].symbol.FilePath = "src/text/other.go"
				fixture.files = append(fixture.files, cloneFamilyFile("src/text/other.go"))
			}
			original := append([]SearchResult(nil), results...)
			addCovering := func() {
				var count int
				results, _, _, count, _ = mergeSearchContractContext(results, searchContractContext{test: &entry}, 0, 10)
				if count != 1 || len(results) > 10 {
					t.Errorf("covering merge: tests=%d results=%d, want 1 and <=10", count, len(results))
				}
			}
			if tc.coveringFirst {
				addCovering()
			}
			var count int
			results, count = mergeSearchRelatedSites(results, sites, fixture.reader(), 0, 10)
			if count != 2 || len(results) > 10 {
				t.Errorf("related merge: sites=%d results=%d, want 2 and <=10", count, len(results))
			}
			if !tc.coveringFirst {
				addCovering()
			}
			sections := map[string]int{}
			for index, result := range results {
				sections[result.Section]++
				if result.Rank != index+1 {
					t.Errorf("rank %d at index %d", result.Rank, index)
				}
			}
			if len(results) != 10 || sections[searchSectionCoveringTest] != 1 || sections[searchSectionRelated] != tc.wantRelated {
				t.Errorf("final results=%d sections=%v, want 10 with 1 covering test and %d related sites", len(results), sections, tc.wantRelated)
			}
			for index := 0; index < 5; index++ {
				if results[index].FilePath != original[index].FilePath || results[index].Snippet != original[index].Snippet {
					t.Errorf("protected head changed at index %d", index)
				}
			}
			for _, before := range original {
				found := false
				for _, result := range results {
					found = found || result.FilePath == before.FilePath
				}
				if !found {
					t.Errorf("last mention of %s lost", before.FilePath)
				}
			}
		})
	}
}

// The tests in this file exist because of the integration, not because of any one feature. Four
// branches each added a context block, each satisfied its own byte ceiling, and none of them could
// see the others. Every test here fails if the blocks are recombined in the wrong order — and every
// one of them passed on all four branches individually, which is exactly why they are here.

// TestSignatureTypeFundingNeverDisplacesTheCoveringTest pins the first interaction bug.
//
// The covering test is appended to the END of `results`, which makes it the first index the shared
// displacement pool would otherwise offer to the block funded NEXT. Signature types are funded by
// displacement, so without the guard they buy themselves by evicting the covering test — silently
// inverting the documented yield order (search_blocks.go), in which the covering test yields LAST
// and the signature types yield third.
func TestSignatureTypeFundingNeverDisplacesTheCoveringTest(t *testing.T) {
	t.Parallel()
	// A payload whose covering test's file is mentioned TWICE, so the "last mention of a file is
	// not for sale" rule cannot be what protects it — only the covering-test guard can.
	results := contractPayload()
	results = append(results,
		SearchResult{Rank: 8, FilePath: "src/head_test.go", StartLine: 5, EndLine: 5, FocusLine: 5,
			SnippetStartLine: 5, SnippetEndLine: 5, Snippet: "other test mention", Signals: []string{"body"}},
	)
	test := contractTestEntry()
	test.Rank = 9
	results = append(results, test)

	order := searchRelatedDisplacementOrder(results, searchEnclosureHeadRanks)
	for _, index := range order {
		if results[index].Section == searchSectionCoveringTest {
			t.Fatalf("the covering test at index %d is offered for displacement: order=%v", index, order)
		}
	}
	// ... and the guard must not have protected everything: a redundant locator is still for sale,
	// or the funders below it can never seat a block at all.
	if len(order) == 0 {
		t.Fatal("no locator is displaceable; the guard is over-broad and starves every funded block")
	}

	// End to end: fund the signature types off this payload and the test entry must survive.
	block := []SearchSignatureType{{
		Name: "Edit", FilePath: "src/edit.go", StartLine: 3,
		Fields: []string{"content string", "span TextRange"}, FieldsTotal: 2,
	}}
	kept, seated := fundSearchSignatureTypes(results, block, searchEnclosureHeadRanks)
	if len(seated) == 0 {
		t.Fatal("the signature-type block could not be funded from a redundant tail locator")
	}
	found := false
	for _, result := range kept {
		if result.Section == searchSectionCoveringTest {
			found = true
		}
	}
	if !found {
		t.Fatal("funding the signature-type block evicted the covering test")
	}
}

// TestContextBlockBudgetRejectsCombinedOverspend pins the second interaction bug: the ceiling has to
// be checked against the SUM of the funded blocks. Each funder checks only its own bytes against
// `--max-context-bytes`, so a payload where every block passes its own check can still overshoot.
// This is the assertion that fails if the construction order in SearchRepository is reversed.
func TestContextBlockBudgetRejectsCombinedOverspend(t *testing.T) {
	t.Parallel()
	card := contractCard()
	signature := []SearchSignatureType{{
		Name: "Edit", FilePath: "src/edit.go", StartLine: 3,
		Fields: []string{"content string"}, FieldsTotal: 1,
	}}
	cardBytes := serializedSearchResultBytes(card)
	signatureBytes := serializedSearchResultBytes(signature)

	response := SearchResponse{
		Results:        []SearchResult{},
		TypeCard:       card,
		SignatureTypes: signature,
		Stats: SearchStats{
			ResultBytes:        1000,
			TypeCardBytes:      cardBytes,
			SignatureTypeBytes: signatureBytes,
		},
	}
	response.Stats.ContextBlockBytes = searchContextBlockBytes(response.Stats)

	// A ceiling that each block clears on its own (1000+card <= budget, and the signature block
	// is byte-neutral by construction) but that the SUM does not.
	response.Stats.ContextBudgetBytes = 1000 + cardBytes + signatureBytes - 1
	if err := validateSearchContextBlockBudget(response); err == nil {
		t.Fatal("combined overspend accepted: the ceiling is being checked per block, not in total")
	} else if !strings.Contains(err.Error(), "exceeds byte budget") {
		t.Fatalf("unexpected error for combined overspend: %v", err)
	}

	// One more byte and the same payload is legal.
	response.Stats.ContextBudgetBytes = 1000 + cardBytes + signatureBytes
	if err := validateSearchContextBlockBudget(response); err != nil {
		t.Fatalf("a payload exactly at the ceiling was rejected: %v", err)
	}
}

// TestContextBlockBudgetExcludesTheAdditiveContainerMap pins the ONE documented exception. The map
// is additive on purpose — a payload that spent its budget on complete head bodies must not lose one
// to buy a navigation aid — so it is held to its own cap instead of to the ceiling. If that ever
// silently changes, this test says so.
func TestContextBlockBudgetExcludesTheAdditiveContainerMap(t *testing.T) {
	t.Parallel()
	response := SearchResponse{
		Results: []SearchResult{},
		Stats: SearchStats{
			ResultBytes:        1000,
			ContextBudgetBytes: 1000,
			ContainerMapBytes:  searchContextBlockAdditiveCap,
		},
	}
	response.Stats.ContextBlockBytes = searchContextBlockBytes(response.Stats)
	if err := validateSearchContextBlockBudget(response); err != nil {
		t.Fatalf("the additive container map was charged against the ceiling: %v", err)
	}

	// Its own cap is real, though.
	response.Stats.ContainerMapBytes = searchContextBlockAdditiveCap + 1
	response.Stats.ContextBlockBytes = searchContextBlockBytes(response.Stats)
	if err := validateSearchContextBlockBudget(response); err == nil {
		t.Fatal("a container map over its cap was accepted")
	}
}

// TestContextBlockBytesAccountsEveryBlock pins that the one summary counter really is the sum, so a
// reader who trusts stats.context_block_bytes is not being told a partial truth.
func TestContextBlockBytesAccountsEveryBlock(t *testing.T) {
	t.Parallel()
	stats := SearchStats{SignatureTypeBytes: 11, TypeCardBytes: 22, ContainerMapBytes: 33}
	if total := searchContextBlockBytes(stats); total != 66 {
		t.Fatalf("context block bytes = %d, want 66 (11+22+33)", total)
	}
	// A mismatched summary must be caught, not tolerated: it is the number that makes the cost
	// of the integration attributable.
	response := SearchResponse{
		Results: []SearchResult{},
		Stats:   SearchStats{ResultBytes: 0, ContainerMapBytes: 33, ContextBlockBytes: 99},
	}
	if err := validateSearchContextBlockBudget(response); err == nil {
		t.Fatal("a wrong context_block_bytes summary was accepted")
	}
}
