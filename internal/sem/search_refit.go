package sem

import "slices"

// The budget re-fit
// =================
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
// after the last pass that can grow `results` or the two reference blocks (the signature types),
// and BEFORE the anchored blocks (closed set, literal cluster, file outline, VERIFY, container map)
// pick the result they describe, so none of them can describe a result the re-fit removed. On a
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
//     the head only when nothing else is left. The ranked hit the covering test was chosen for is
//     kept with the head, so a covering test never outlives its fix site.
//
// It removes WHOLE results and never edits a snippet. That is deliberate: a clipped body would keep
// its complete-symbol or full-unit signal and claim a completeness it no longer has, which is the
// false-positive class the fitter's own owner has documented (see
// search_agent_marker_sweep_test.go). Every removal is reported (stats.results_dropped_by_budget and
// the per-block counters), so a payload that came back smaller says so, and the all-docs fallback
// of the section pass is applied again to what is left (settleSearchBudgetRefit).
//
// Two passes run after the anchored blocks by design: the prose promotion (a promoted passage must
// never become an anchor) and the completeness certificate. Shedding in yield order after them could
// remove a result a block describes, so their overshoot is handled differently: both only enrich a
// ranking the re-fit has already fitted, and when they overshoot, the response falls back to that
// ranking (searchPayloadOverBudget).
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

// searchBudgetRefit is the payload after the budget re-fit, and what the re-fit had to remove.
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
	// protected names the ranked results that leave only with the head: the fix site the covering
	// test was chosen for (searchCoveredAnchorKeys).
	protected map[searchRefitKey]bool
}

// searchRefitKey identifies one result across the re-fit's renumbering.
type searchRefitKey struct {
	filePath, symbolID string
	startLine, endLine int
}

func searchRefitKeyOf(result SearchResult) searchRefitKey {
	return searchRefitKey{
		filePath: result.FilePath, symbolID: result.SymbolID,
		startLine: result.StartLine, endLine: result.EndLine,
	}
}

// searchCoveredAnchorKeys names the ranked results the covering test in `results` was chosen for:
// the primary hits whose symbol is the contract anchor, resolved the way searchTypeCardAnchor
// resolved it. That anchor is one of the first primary hits, which is not the same as the first
// positions: docs hits can hold the head, putting the anchor below it. With no covering test in the
// payload there is nothing to keep it for, and the result is nil.
func searchCoveredAnchorKeys(
	results []SearchResult,
	anchor SymbolRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
) map[searchRefitKey]bool {
	if anchor.ID == "" || !slices.ContainsFunc(results, func(result SearchResult) bool {
		return result.Section == searchSectionCoveringTest
	}) {
		return nil
	}
	keys := map[searchRefitKey]bool{}
	for _, result := range results {
		if result.Section != searchSectionPrimary {
			continue
		}
		if symbol, ok := searchTypeCardResultSymbol(result, symbolsByID, symbolsByFile); ok && symbol.ID == anchor.ID {
			keys[searchRefitKeyOf(result)] = true
		}
	}
	return keys
}

// shed is the number of removals the re-fit made. Zero means every pass held its own share.
func (refit searchBudgetRefit) shed() int {
	return len(refit.dropped) + refit.typeCardEntries + refit.signatureSteps
}

// fundedBytes is what Validate charges against the ceiling: the ranking plus the two
// displacement-funded blocks outside it. An empty block costs nothing, matching how the response
// reports it (its *Bytes counter stays 0 and the field is omitted).
func (refit searchBudgetRefit) fundedBytes() int {
	return searchFundedPayloadBytes(refit.results, refit.typeCard, refit.signatureTypes)
}

// searchFundedPayloadBytes is the funded payload's size: the ranking plus the type card and the
// signature-type block, each counted only when it is not empty.
func searchFundedPayloadBytes(
	results []SearchResult, typeCard []TypeCardEntry, signatureTypes []SearchSignatureType,
) int {
	funded := serializedSearchResultBytes(results)
	if len(typeCard) > 0 {
		funded += serializedSearchResultBytes(typeCard)
	}
	if len(signatureTypes) > 0 {
		funded += serializedSearchResultBytes(signatureTypes)
	}
	return funded
}

// searchPayloadOverBudget reports whether the funded payload breaks the ceiling Validate holds it to.
// It is the check behind the fallback for the passes that run after the anchored blocks. budget <= 0
// means unbounded.
func searchPayloadOverBudget(
	results []SearchResult, typeCard []TypeCardEntry, signatureTypes []SearchSignatureType, budget int,
) bool {
	return budget > 0 &&
		searchFundedPayloadBytes(results, typeCard, signatureTypes) > searchFundedCeiling(budget)
}

// refitSearchPayloadToBudget brings the funded payload back under budget by whole removals, in the
// order the file comment gives. budget <= 0 means unbounded. headRanks is the protected head, and
// protected names the ranked results that are removed only with it.
//
// The empty ranking is the floor: it is what the fitter itself returns when nothing fits, and it
// cannot get smaller, so a budget below its two bytes stops the re-fit there rather than looping.
func refitSearchPayloadToBudget(
	results []SearchResult,
	typeCard []TypeCardEntry,
	signatureTypes []SearchSignatureType,
	budget, headRanks int,
	protected map[searchRefitKey]bool,
) searchBudgetRefit {
	refit := searchBudgetRefit{
		results: results, typeCard: typeCard, signatureTypes: signatureTypes, protected: protected,
	}
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
	// A redundant fragment can still be the hit the covering test was chosen for. It stays, and it
	// still counts as a mention of its file, so another fragment of that file can go first.
	covered := func(result SearchResult) bool { return refit.protected[searchRefitKeyOf(result)] }
	if order := searchRelatedDisplacementOrderSparing(refit.results, floor, covered); len(order) > 0 {
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
	if index, ok := searchRefitTailIndex(refit.results, floor, refit.protected); ok {
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
// lowest-ranked entry below the head that is neither the covering test nor protected, then the
// covering test (the block that yields last, see search_blocks.go), then the head and the protected
// results from the bottom.
func searchRefitTailIndex(results []SearchResult, floor int, protected map[searchRefitKey]bool) (int, bool) {
	for index := len(results) - 1; index >= floor; index-- {
		if results[index].Section != searchSectionCoveringTest && !protected[searchRefitKeyOf(results[index])] {
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
	for _, limit := range []int{8, 5, 3, 2} {
		if widest > limit {
			return shrinkSearchSignatureTypes(block, limit)
		}
	}
	return block[:len(block)-1]
}

// settleSearchBudgetRefit adopts a re-fit that removed something: the counters are brought in line
// with the payload that is going out, and the section pass's all-docs fallback is applied again.
func settleSearchBudgetRefit(
	refit searchBudgetRefit, stats *SearchStats,
) ([]SearchResult, []TypeCardEntry, []SearchSignatureType) {
	applySearchBudgetRefitStats(stats, refit)
	return keepSearchAllDocsFallback(refit.results), refit.typeCard, refit.signatureTypes
}

// keepSearchAllDocsFallback re-applies the one decision of assignSearchSections a removal can
// invalidate. A ranking whose hits are all docs stays primary, because those hits are the only
// answer there is; when the re-fit removed the last primary hit, the docs hits left are that answer
// again. It only promotes. It never labels: re-running assignSearchSections itself could, because
// the related sites and the covering test seated after it change how many results it counts, and a
// label is bytes the ceiling was not asked to fund.
func keepSearchAllDocsFallback(results []SearchResult) []SearchResult {
	docs := 0
	for _, result := range results {
		switch result.Section {
		case searchSectionPrimary:
			return results
		case searchSectionDocs:
			docs++
		}
	}
	if docs == 0 {
		return results
	}
	promoted := append([]SearchResult(nil), results...)
	for index := range promoted {
		if promoted[index].Section == searchSectionDocs {
			promoted[index].Section = searchSectionPrimary
		}
	}
	return promoted
}

// applySearchBudgetRefitStats keeps the counters describing the payload that is going out. Only
// counters a whole-result removal can falsify are touched: each dropped result is counted as
// dropped by the budget, and stops counting as a complete body, a callee-hop site, a related site
// or the covering test.
//
// stats.complete_symbol_snippets counts the bodies the allocator seated (less the ones the span
// merge absorbed). A body the callee hop printed carries complete-symbol too but was never added to
// it, so removing one must not subtract a body that is still there.
func applySearchBudgetRefitStats(stats *SearchStats, refit searchBudgetRefit) {
	for _, result := range refit.dropped {
		stats.ResultsDropped++
		if hasSearchSignal(result, searchCalleeHopSignal) {
			stats.CalleeHopSites = maxInt(0, stats.CalleeHopSites-1)
		} else if hasSearchSignal(result, searchCompleteSymbolSignal) {
			stats.CompleteSymbols = maxInt(0, stats.CompleteSymbols-1)
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
