package sem

import "fmt"

// The search payload's context-block budget
// =========================================
//
// A search response is a ranked list of code regions PLUS a set of context blocks that live
// outside `results` and answer questions the ranking cannot: what container the top hit sits in,
// what its signature is written in terms of, what names its body uses, and what test says what it
// is supposed to do. Four independently developed features contribute one block each, and they
// all draw on the same scarce resource — bytes that are replayed into the model on every
// subsequent turn of the session. This file is the single place their budget is stated.
//
// There is ONE ceiling: `--max-context-bytes` (default 24576). Everything except the container
// map is funded from inside it. The container map is the single, deliberate, documented exception:
// it is additive and separately capped, because a payload that spent its budget on complete head
// bodies must not lose one to buy a navigation aid. Its cost is stated
// (`stats.container_map_bytes`), never hidden, and `stats.context_block_bytes` reports the total
// of everything outside `results` so the true payload size never has to be re-derived.
//
// Section order (the order the text renderer prints, and the order the JSON fields are declared)
// -----------------------------------------------------------------------------------------------
//
//  0. LOW CONFIDENCE            — is this payload worth reading at all
//  1. CONTAINER MAP             — what to read, and how much of it (sizes a range read)
//  2. candidate fix sites       — where to edit (the ranking; complete bodies at the head)
//  3. COVERING TEST             — what the edit has to achieve
//  4. TYPES IN THIS SIGNATURE   — the anchor's own contract: what callers depend on
//  5. DECLARATIONS              — the names the anchor's body uses
//  6. RELATED SITES             — where else the same change lands
//  7. DOCS & FIXTURES           — matched the query, but are not fix sites
//
// The order is a reading order, not a value order: navigate, edit, check the goal, check the
// contract, check the names, check the neighbours. Blocks 3-5 are all about hit 1 and therefore
// stay together and stay ahead of 6-7, which are about other places; 4 precedes 5 because a
// signature is what callers can see and a patch must not break, while the declaration card is
// about identifiers internal to the body.
//
// Yield order — which section gives up its bytes first, and why
// -------------------------------------------------------------
//
// One rule decides it: **a block yields in proportion to how cheaply the agent could get it
// back.** Bytes buy avoided turns, so the block worth keeping is the one whose absence costs the
// most extra tool calls, not the one that looks most informative.
//
//	yields 1st  DECLARATIONS (type card)     — every entry is one `def NAME` away, and it is the
//	                                           block with the loosest relation to the edit: it
//	                                           names identifiers the snippet already shows in use.
//	                                           It also sheds gradually (last entry first), so the
//	                                           first thing that happens under pressure is a
//	                                           shorter card, not a missing one.
//	yields 2nd  CONTAINER MAP                — reproducible by one `def`/`neighbors` on the
//	                                           container. It is the only additive block, so it is
//	                                           also the only one whose loss returns bytes without
//	                                           taking anything from the ranking. Degrades
//	                                           full -> names-and-ranges -> absent.
//	yields 3rd  TYPES IN THIS SIGNATURE      — one `def` per named type recovers it, and it is
//	                                           small to begin with (~286 B median), so yielding it
//	                                           buys little. Shrinks per-type member lists first
//	                                           (15 -> 8 -> 5 -> 3 -> 2), then drops entries.
//	yields 4th  COVERING TEST                — yields LAST of the blocks. It is the only one that
//	                                           names a file and range nothing else in the payload
//	                                           names, so recovering it costs a search AND a read,
//	                                           and it is a single entry — cheap to keep, expensive
//	                                           to lose.
//	never       candidate fix sites (head)   — ranks 1..searchEnclosureHeadRanks, and the last
//	                                           mention of any file at any rank. A location the
//	                                           agent never sees is a file it never opens; that is
//	                                           the one loss no other block can compensate for.
//
// Honest truncation, not silent dropping: a block that does not fit is SMALLER first (fewer
// entries, narrower windows, shorter member lists) and states what it omitted; only a block that
// cannot shrink further is dropped whole, and its counter in `stats` then reads 0 so the absence
// is visible rather than inferred. No block is ever silently traded for another.
//
// Construction order (fixed, and not the same as either order above)
// ------------------------------------------------------------------
//
// Data dependencies, not policy, fix this — see the comment at the seam in SearchRepository:
//
//  1. contract context (covering test + type card) — the only block that GROWS the payload, so it
//     must measure itself against an untouched ceiling.
//  2. signature types — strictly byte-neutral (displacement-funded), so it cannot breach the
//     ceiling step 1 just satisfied. Reversing 1 and 2 DOES breach it: step 1's check does not
//     know about bytes already committed outside `results`, so it re-spends them.
//  3. container map — needs the final ranking, because its anchor must be the hit the caller
//     will actually read.
//
// The shared displacement pool (searchRelatedDisplacementOrder) is what makes the order matter at
// all: three blocks draw tail locators from it, and it protects the head, the last mention of any
// file, and the covering test.
//
// NOT part of this budget
// -----------------------
//
// The relation commands (`neighbors`, `impact`) have their own, separate byte budget for
// call-site guard blocks — 6 guards, a 10-line window, 3 blocks, 2000 B total, and impact quotes
// no source at all. That budget is not funded from, and does not draw on, the search payload's
// ceiling: a different command, a different response, a different set of blocks. The two are
// deliberately kept apart, and a single combined "context bytes" number across both would be
// meaningless.
// searchContextBlockAdditiveCap is the container map's cap: the one block outside the ceiling.
// Kept at ~1% of one agent turn.
//
// The funded blocks' own allowance — what they may add before the hard ceiling is consulted at all
// — is searchContractAllowanceBytes in search_contract.go; the signature-type block has no
// allowance of its own because it is displacement-funded and so can never grow a payload.
const searchContextBlockAdditiveCap = searchContainerMapMaxBytes

// searchContextBlockBytes totals every byte a response carries outside `results`. It is what
// stats.context_block_bytes reports, and it is the number Validate checks the ceiling against.
func searchContextBlockBytes(stats SearchStats) int {
	return stats.SignatureTypeBytes + stats.TypeCardBytes + stats.ContainerMapBytes
}

// validateSearchContextBlockBudget enforces the policy above on a finished response.
//
// It is a contract check, not a fitter: by the time it runs, every block has already been sized
// by its own funder. It exists because three separately developed funders each satisfied their
// own ceiling and none of them could see the others — the exact shape of bug that ships as "each
// feature was fine on its branch".
func validateSearchContextBlockBudget(response SearchResponse) error {
	stats := response.Stats
	// Byte accounting first: a block whose reported cost does not match its serialized size
	// can break every check below it without tripping any of them.
	cardBytes := 0
	if len(response.TypeCard) > 0 {
		cardBytes = serializedSearchResultBytes(response.TypeCard)
	}
	if stats.TypeCardBytes != cardBytes {
		return fmt.Errorf("search type card byte accounting mismatch: %d != %d", stats.TypeCardBytes, cardBytes)
	}
	if cardBytes > searchTypeCardBytes {
		return fmt.Errorf("search type card exceeds its allowance: %d > %d", cardBytes, searchTypeCardBytes)
	}
	signatureBytes := 0
	if len(response.SignatureTypes) > 0 {
		signatureBytes = serializedSearchResultBytes(response.SignatureTypes)
	}
	if stats.SignatureTypeBytes != signatureBytes {
		return fmt.Errorf("search signature type byte accounting mismatch: %d != %d",
			stats.SignatureTypeBytes, signatureBytes)
	}
	if stats.ContainerMapBytes > searchContextBlockAdditiveCap {
		return fmt.Errorf("search container map exceeds its allowance: %d > %d",
			stats.ContainerMapBytes, searchContextBlockAdditiveCap)
	}
	if total := searchContextBlockBytes(stats); stats.ContextBlockBytes != total {
		return fmt.Errorf("search context block byte accounting mismatch: %d != %d",
			stats.ContextBlockBytes, total)
	}
	// The ceiling. The container map is excluded by policy (it is the additive block, capped
	// above); every other block is funded from inside the ceiling, so their combined cost plus
	// the ranking must fit. Checking them one at a time — which is what each funder does on its
	// own — passes while the sum overshoots.
	if stats.ContextBudgetBytes > 0 {
		funded := stats.ResultBytes + stats.TypeCardBytes + stats.SignatureTypeBytes
		if funded > stats.ContextBudgetBytes {
			return fmt.Errorf("search context exceeds byte budget: %d > %d",
				funded, stats.ContextBudgetBytes)
		}
	}
	return nil
}
