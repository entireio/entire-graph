package sem

import "testing"

// contractPayload is a ranked payload with a protected head and two tail locators, one of which is
// the LAST mention of its file (so it may never be displaced) and one of which is redundant.
func contractPayload() []SearchResult {
	results := make([]SearchResult, 0, 8)
	for rank := 1; rank <= searchEnclosureHeadRanks; rank++ {
		results = append(results, SearchResult{
			Rank: rank, FilePath: "src/head.go", StartLine: rank * 10, EndLine: rank*10 + 4,
			FocusLine: rank * 10, SnippetStartLine: rank * 10, SnippetEndLine: rank*10 + 4,
			Snippet: "head body", Signals: []string{"body"},
		})
	}
	results = append(results,
		SearchResult{Rank: 6, FilePath: "src/head.go", StartLine: 900, EndLine: 900, FocusLine: 900,
			SnippetStartLine: 900, SnippetEndLine: 900, Snippet: "redundant tail", Signals: []string{"body"}},
		SearchResult{Rank: 7, FilePath: "src/only.go", StartLine: 12, EndLine: 12, FocusLine: 12,
			SnippetStartLine: 12, SnippetEndLine: 12, Snippet: "only mention", Signals: []string{"body"}},
	)
	return results
}

func contractTestEntry() SearchResult {
	return SearchResult{
		FilePath: "src/head_test.go", StartLine: 40, EndLine: 48, FocusLine: 44,
		SnippetStartLine: 43, SnippetEndLine: 44, Kind: "function",
		QualifiedName: "TestHead", Section: searchSectionCoveringTest,
		Signals: []string{searchRelatedSignalPrefix + searchCoveringTestSignal},
		Snippet: "\tgot := head()\n\trequire.Equal(t, 1, got)",
	}
}

func contractCard() []TypeCardEntry {
	return []TypeCardEntry{
		{Name: "series.headChunks", FilePath: "src/head.go", Line: 91, Decl: "headChunks *memChunk"},
		{Name: "memChunk", FilePath: "src/head.go", Line: 120, Decl: "type memChunk struct { prev *memChunk }"},
	}
}

// contractTotalBytes is what the merge itself is budgeted against: the ranking's bytes plus the
// card's, with an absent card costing nothing (a nil slice still serializes to `null`).
func contractTotalBytes(results []SearchResult, card []TypeCardEntry) int {
	total := serializedSearchResultBytes(results)
	if len(card) > 0 {
		total += serializedSearchResultBytes(card)
	}
	return total
}

// TestMergeSearchContractContextAppendsAfterEverything pins that the covering test never enters the
// part of the list an agent reads as candidate fix sites, and that ranks stay 1..N.
func TestMergeSearchContractContextAppendsAfterEverything(t *testing.T) {
	t.Parallel()
	results := contractPayload()
	entry := contractTestEntry()
	merged, card, _, tests, cardEntries := mergeSearchContractContext(
		results, searchContractContext{test: &entry, card: contractCard()}, 0,
	)
	if tests != 1 || cardEntries != len(contractCard()) {
		t.Fatalf("counts = %d tests / %d card entries, want 1 / %d", tests, cardEntries, len(contractCard()))
	}
	if len(card) != len(contractCard()) {
		t.Fatalf("card shrank to %d entries with no ceiling forcing it", len(card))
	}
	if len(merged) != len(results)+1 {
		t.Fatalf("merged has %d entries, want %d", len(merged), len(results)+1)
	}
	if last := merged[len(merged)-1]; last.Section != searchSectionCoveringTest {
		t.Fatalf("last entry section = %q, want the covering test", last.Section)
	}
	for index, result := range merged {
		if result.Rank != index+1 {
			t.Fatalf("rank %d at index %d breaks the 1..N invariant", result.Rank, index)
		}
	}
}

// TestMergeSearchContractContextRespectsAllowance pins the byte contract: growth is capped at the
// two blocks' own allowances, never more.
func TestMergeSearchContractContextRespectsAllowance(t *testing.T) {
	t.Parallel()
	results := contractPayload()
	baseline := serializedSearchResultBytes(results)
	entry := contractTestEntry()
	merged, card, _, _, _ := mergeSearchContractContext(
		results, searchContractContext{test: &entry, card: contractCard()}, 0,
	)
	growth := contractTotalBytes(merged, card) - baseline
	if growth > searchContractAllowanceBytes {
		t.Fatalf("payload grew %d bytes, over the %d allowance", growth, searchContractAllowanceBytes)
	}
	if growth <= 0 {
		t.Fatalf("payload grew %d bytes: the blocks were not attached at all", growth)
	}
}

// TestMergeSearchContractContextFundsFromRedundantTail pins what happens when the caller's own byte
// ceiling leaves no room: a redundant tail fragment pays, the last mention of a file never does,
// and when even that is not enough the blocks are given up rather than the ceiling broken.
func TestMergeSearchContractContextFundsFromRedundantTail(t *testing.T) {
	t.Parallel()
	results := contractPayload()
	baseline := serializedSearchResultBytes(results)
	entry := contractTestEntry()
	blocks := searchContractContext{test: &entry, card: contractCard()}

	// A ceiling exactly at the baseline: nothing may be added without something being given up.
	merged, card, _, tests, entries := mergeSearchContractContext(results, blocks, baseline)
	total := contractTotalBytes(merged, card)
	if total > baseline {
		t.Fatalf("payload is %d bytes (%d tests, %d card entries), over the %d ceiling",
			total, tests, entries, baseline)
	}
	paths := map[string]int{}
	for _, result := range merged {
		paths[result.FilePath]++
	}
	if paths["src/only.go"] != 1 {
		t.Fatalf("the only mention of src/only.go was displaced: %v", paths)
	}

	// A ceiling far below the baseline cannot be met at all: the blocks are dropped and the
	// ranking is returned untouched rather than mutilated.
	untouched, none, _, tests, entries := mergeSearchContractContext(results, blocks, baseline/2)
	if len(none) != 0 || tests != 0 || entries != 0 {
		t.Fatalf("blocks were attached under an impossible ceiling: %d tests, %d entries", tests, entries)
	}
	if serializedSearchResultBytes(untouched) != baseline {
		t.Fatalf("the ranking was modified while giving up on the blocks")
	}
}

// TestMergeSearchContractContextNoBlocksIsIdentity pins that a payload with nothing to add pays
// nothing: same slice, no card, no counts.
func TestMergeSearchContractContextNoBlocksIsIdentity(t *testing.T) {
	t.Parallel()
	results := contractPayload()
	merged, card, _, tests, entries := mergeSearchContractContext(results, searchContractContext{}, 0)
	if len(card) != 0 || tests != 0 || entries != 0 {
		t.Fatalf("empty context produced %d card entries and %d tests", entries, tests)
	}
	if len(merged) != len(results) {
		t.Fatalf("merged has %d entries, want the original %d", len(merged), len(results))
	}
}
