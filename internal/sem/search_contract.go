package sem

// Contract context
// ================
//
// Two blocks answer the same question from opposite ends — "what is this code SUPPOSED to do"
// (search_covertest.go) and "what ARE the names it uses" (search_typecard.go) — and they are
// budgeted together here, because they compete for the same scarce resource: bytes that are
// replayed into the model on every subsequent turn of the session.
//
// The accounting rule:
//
//   - Each block has its own fixed allowance, and neither can spend the other's. A repo with a
//     rich type graph must not be able to crowd out the covering test, and a repo with a huge
//     test suite must not be able to crowd out the declarations.
//   - Growth beyond those allowances is impossible: a block that does not fit is smaller (fewer
//     card entries, a narrower test window) or absent. There is no degraded-but-oversized form.
//   - `--max-context-bytes` still binds absolutely. When the payload is already at the ceiling,
//     the blocks are funded by DISPLACING tail locators — the least valuable bytes in the
//     payload, since a bare `path:line` in the tail names a file the payload already names
//     elsewhere. The protections the related-site block established hold unchanged: the head is
//     never displaced, and the LAST mention of a file is not for sale at any price, so file
//     recall cannot be traded for contract context.
const searchContractAllowanceBytes = searchCoveringTestGrowthBytes + searchTypeCardBytes

// searchContractContext is what the two blocks contribute to one payload.
type searchContractContext struct {
	// test is the covering-test entry, already rendered and already inside its allowance.
	test *SearchResult
	// card is the type/signature card, already inside its allowance.
	card []TypeCardEntry
}

// buildSearchContractContext computes both blocks for a payload. It is a pure read of the
// snapshot's relations plus the anchors' own neighbourhood of files; it builds no index and adds
// no analysis pass to the search.
func buildSearchContractContext(
	results []SearchResult,
	anchors []SymbolRecord,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	includeCard bool,
) searchContractContext {
	context := searchContractContext{}
	if len(anchors) == 0 {
		return context
	}
	cache := newSearchRelatedFileCache(read, searchCoveringTestFileReads)
	// The walk down the head is what the type card does for the same reason: rank 1 is often a
	// one-line field stub or a whole class, and "the test that covers a class" is not a question
	// with one answer. Stopping at the first anchor that HAS a covering test keeps the block one
	// entry about one body, at no extra cost — the search stops as soon as it succeeds.
	for _, anchor := range anchors {
		test, found := selectSearchCoveringTest(
			results, anchor, q, relations, symbolsByID, symbolsByFile, cache,
		)
		if !found {
			continue
		}
		lines, ok := cache.get(test.symbol.FilePath)
		if !ok {
			continue
		}
		entry, rendered := searchCoveringTestResult(test, lines, searchCoveringTestGrowthBytes)
		if !rendered {
			continue
		}
		context.test = &entry
		break
	}
	// The declaration card is a REFERENCE block and is off unless the caller asked for it; the
	// covering test above is not, and stays. See SearchOptions in search.go for the measurement that
	// separates them, and search_verify.go for the block that now depends on the test's path.
	if includeCard {
		context.card = selectSearchTypeCard(
			results, anchors, relations, symbolsByID, symbolsByFile, read, searchTypeCardBytes,
		)
	}
	return context
}

// mergeSearchContractContext attaches both blocks to a ranked payload and reports what each
// contributed.
//
// Contract:
//   - The covering-test entry is appended after every ranked hit and every related site, so it
//     can never displace a candidate fix site by position and can never enter the top of the
//     list an agent reads as "where do I edit".
//   - The payload grows by at most searchContractAllowanceBytes, and never past hardBudget.
//   - When hardBudget forces a choice, tail locators are displaced to pay — fewest first, and
//     only ones whose file the payload still names somewhere else.
//   - Ranks are renumbered so the payload keeps its 1..N invariant.
func mergeSearchContractContext(
	results []SearchResult,
	context searchContractContext,
	hardBudget int,
) ([]SearchResult, []TypeCardEntry, int, int) {
	if context.test == nil && len(context.card) == 0 {
		return results, nil, 0, 0
	}
	baseline := serializedSearchResultBytes(results)
	ceiling := baseline + searchContractAllowanceBytes
	if hardBudget > 0 && hardBudget < ceiling {
		ceiling = hardBudget
	}
	// The card lives outside `results`, so it is charged against the same ceiling explicitly
	// rather than being invisible to it.
	card := context.card
	cardBytes := serializedSearchResultBytes(card)
	if len(card) == 0 {
		cardBytes = 0
	}
	floor := minInt(len(results), searchEnclosureHeadRanks)
	order := searchRelatedDisplacementOrder(results, floor)
	for {
		for drop := 0; drop <= len(order); drop++ {
			merged := searchContractMergedPlan(results, context.test, order[:drop])
			if serializedSearchResultBytes(merged)+cardBytes <= ceiling {
				tests := 0
				if context.test != nil {
					tests = 1
				}
				return merged, card, tests, len(card)
			}
		}
		// Nothing fits with the card at this size. Give up the card's LAST entry first: it is
		// the weakest declaration in it, and the covering test is the block with the larger
		// measured effect.
		if len(card) > 0 {
			card = card[:len(card)-1]
			cardBytes = 0
			if len(card) > 0 {
				cardBytes = serializedSearchResultBytes(card)
			}
			continue
		}
		if context.test != nil {
			context.test = nil
			continue
		}
		return results, nil, 0, 0
	}
}

// searchContractMergedPlan builds one merged payload: the ranking minus the displaced indices,
// then the covering-test entry, renumbered 1..N.
func searchContractMergedPlan(results []SearchResult, test *SearchResult, displaced []int) []SearchResult {
	dropped := make(map[int]bool, len(displaced))
	for _, index := range displaced {
		dropped[index] = true
	}
	merged := make([]SearchResult, 0, len(results)+1)
	for index, result := range results {
		if !dropped[index] {
			merged = append(merged, result)
		}
	}
	if test != nil {
		merged = append(merged, *test)
	}
	for index := range merged {
		merged[index].Rank = index + 1
	}
	return merged
}
