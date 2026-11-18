package sem

// The final re-fit
// ================
//
// Every pass after the byte fitter is responsible for its own share of the ceiling: the allocator,
// the span merge, the callee hop, the related sites, the contract context, the prose promotion and
// the completeness certificate each measure the payload they produce against
// `--max-context-bytes`. SearchResponse.Validate then checks the finished response, and when any
// one of them got its arithmetic wrong it REJECTS the whole response. The command fails for a
// caller who asked for a smaller answer, instead of handing back a smaller one (issue 208: a
// section label added after the allocator had spent the ceiling came back 6362 > 6000).
//
// refitSearchPayloadToBudget is the one place that holds the ceiling for all of them. It runs once,
// after the last pass that can grow `results` and before the response is accounted, and on a
// payload that already fits it changes nothing: today every pass holds its own share, so the
// default output is byte-identical. What it adds is the behaviour when a pass does not. The
// response then degrades, in the yield order search_blocks.go states, rather than failing:
//
//  1. redundant tail fragments: the ranked hits every displacement-funded block is already allowed
//     to reclaim (searchRelatedDisplacementOrder: below the head, carrying no source, and never the
//     last mention of a file);
//  2. the type card, last entry first;
//  3. the signature-type block, shorter member lists first, then its last entry;
//  4. the rest of the ranking from the bottom, the covering test last of the non-head entries, and
//     the head only when nothing else is left.
//
// It removes WHOLE results and never edits a snippet. That is deliberate: a clipped body would keep
// its complete-symbol or full-unit signal and claim a completeness it no longer has, which is the
// false-positive class the fitter's own owner has documented (see
// search_agent_marker_sweep_test.go). Every removal is reported (stats.results_dropped_by_budget and
// the per-block counters), so a payload that came back smaller says so.
//
// It is a safety net, not a fitter of record. A pass that overshoots is still a bug in that pass, and
// TestSearchRepositoryPostAllocatorPassesHoldTheCeiling fails on it, because a net that quietly
// caught every overshoot would hide the next one.

// searchEmptyRankingBytes is what an empty ranking costs on the wire: `[]`. It is the floor every
// fitter returns when nothing fits, so no response can be smaller.
var searchEmptyRankingBytes = serializedSearchResultBytes([]SearchResult{})

// searchFundedCeiling is the ceiling Validate holds the funded payload to. It is the caller's
// budget, except that a budget below the empty ranking is held to the empty ranking: the payload
// cannot shrink below it, and rejecting it made `--max-context-bytes 1` fail with "2 > 1" where
// every larger budget that seats nothing returns an empty answer.
func searchFundedCeiling(budget int) int {
	return maxInt(budget, searchEmptyRankingBytes)
}

// searchBudgetRefit is the payload after the final re-fit, and what the re-fit had to remove.
type searchBudgetRefit struct {
	results        []SearchResult
	typeCard       []TypeCardEntry
	signatureTypes []SearchSignatureType
	// dropped are the whole results removed, in removal order.
	dropped []SearchResult
	// typeCardEntries and signatureSteps count the block reductions: card entries removed, and
	// signature-type shrink or drop steps taken.
	typeCardEntries int
	signatureSteps  int
}

// shed is the number of removals the re-fit made. Zero means every pass held its own share.
func (refit searchBudgetRefit) shed() int {
	return len(refit.dropped) + refit.typeCardEntries + refit.signatureSteps
}

// fundedBytes is what Validate charges against the ceiling: the ranking plus the two
// displacement-funded blocks outside it. An empty block costs nothing, matching how the response
// reports it (its *Bytes counter stays 0 and the field is omitted).
func (refit searchBudgetRefit) fundedBytes() int {
	funded := serializedSearchResultBytes(refit.results)
	if len(refit.typeCard) > 0 {
		funded += serializedSearchResultBytes(refit.typeCard)
	}
	if len(refit.signatureTypes) > 0 {
		funded += serializedSearchResultBytes(refit.signatureTypes)
	}
	return funded
}

// refitSearchPayloadToBudget brings the funded payload back under budget by whole removals, in the
// order the file comment gives. budget <= 0 means unbounded. headRanks is the protected head.
//
// The empty ranking is the floor: it is what the fitter itself returns when nothing fits, and it
// cannot get smaller, so a budget below its two bytes stops the re-fit there rather than looping.
func refitSearchPayloadToBudget(
	results []SearchResult,
	typeCard []TypeCardEntry,
	signatureTypes []SearchSignatureType,
	budget, headRanks int,
) searchBudgetRefit {
	refit := searchBudgetRefit{results: results, typeCard: typeCard, signatureTypes: signatureTypes}
	if budget <= 0 {
		return refit
	}
	for refit.fundedBytes() > budget {
		if !refit.shedOne(headRanks) {
			break
		}
	}
	return refit
}

// shedOne makes the next removal in yield order and reports whether there was anything left to
// remove.
func (refit *searchBudgetRefit) shedOne(headRanks int) bool {
	floor := minInt(len(refit.results), headRanks)
	if order := searchRelatedDisplacementOrder(refit.results, floor); len(order) > 0 {
		refit.dropResult(order[0])
		return true
	}
	if len(refit.typeCard) > 0 {
		refit.typeCard = refit.typeCard[:len(refit.typeCard)-1]
		refit.typeCardEntries++
		return true
	}
	if len(refit.signatureTypes) > 0 {
		refit.signatureTypes = shrinkSearchSignatureTypesOneStep(refit.signatureTypes)
		refit.signatureSteps++
		return true
	}
	if index, ok := searchRefitTailIndex(refit.results, floor); ok {
		refit.dropResult(index)
		return true
	}
	return false
}

// dropResult removes one whole result and renumbers the ranking 1..N.
func (refit *searchBudgetRefit) dropResult(index int) {
	refit.dropped = append(refit.dropped, refit.results[index])
	refit.results = searchRelatedMergedPlan(refit.results, nil, []int{index})
}

// searchRefitTailIndex picks the next result to drop once no redundant fragment is left: the
// lowest-ranked entry below the head that is not the covering test, then the covering test (the
// block that yields last, see search_blocks.go), then the head from the bottom.
func searchRefitTailIndex(results []SearchResult, floor int) (int, bool) {
	for index := len(results) - 1; index >= floor; index-- {
		if results[index].Section != searchSectionCoveringTest {
			return index, true
		}
	}
	for index := len(results) - 1; index >= 0; index-- {
		if results[index].Section == searchSectionCoveringTest {
			return index, true
		}
	}
	if len(results) == 0 {
		return 0, false
	}
	return len(results) - 1, true
}

// shrinkSearchSignatureTypesOneStep takes the signature-type block one step down its documented
// degradation (search_blocks.go): the member lists shrink 15 -> 8 -> 5 -> 3 -> 2 first, and only a
// block already at the smallest list loses its last entry.
func shrinkSearchSignatureTypesOneStep(block []SearchSignatureType) []SearchSignatureType {
	widest := 0
	for _, entry := range block {
		widest = maxInt(widest, maxInt(len(entry.Methods), len(entry.Fields)))
	}
	for _, cap := range []int{8, 5, 3, 2} {
		if widest > cap {
			return shrinkSearchSignatureTypes(block, cap)
		}
	}
	return block[:len(block)-1]
}

// applySearchBudgetRefitStats keeps the counters describing the payload that is going out. Only
// counters a whole-result removal can falsify are touched: each dropped result is counted as
// dropped by the budget, and stops counting as a complete body, a callee-hop site, a related site
// or the covering test.
func applySearchBudgetRefitStats(stats *SearchStats, refit searchBudgetRefit) {
	for _, result := range refit.dropped {
		stats.ResultsDropped++
		if hasSearchSignal(result, searchCompleteSymbolSignal) {
			stats.CompleteSymbols = maxInt(0, stats.CompleteSymbols-1)
		}
		if hasSearchSignal(result, searchCalleeHopSignal) {
			stats.CalleeHopSites = maxInt(0, stats.CalleeHopSites-1)
		}
		switch result.Section {
		case searchSectionRelated:
			stats.RelatedSites = maxInt(0, stats.RelatedSites-1)
		case searchSectionCoveringTest:
			stats.CoveringTests = maxInt(0, stats.CoveringTests-1)
		}
	}
	stats.TypeCardEntries = len(refit.typeCard)
	stats.SignatureTypes = len(refit.signatureTypes)
	stats.SignatureTypeBytes = 0
	if len(refit.signatureTypes) > 0 {
		stats.SignatureTypeBytes = serializedSearchResultBytes(refit.signatureTypes)
	}
}
