package sem

import (
	"sort"
	"strings"
)

// The callee hop (--callee-hop, off by default)
// =============================================
//
// MEASURED DEFECT THIS EXISTS FOR. With the editability levers in search_enclosure.go on, the
// payload's remaining misses are not truncated units — they are the WRONG unit of the right file.
// Across eight R30PUB instances, 8 of 12 code-gold files were ranked and 0 had the gold hunk
// verbatim; in every ranked case the printed unit and the gold unit were different callables in the
// same file. The commonest shape is that the ranked hit is the thin public entry point and the gold
// is the helper it CALLS:
//
//	redis__redis-10095   ranked  src/t_list.c:577-581   lpopCommand (3-line wrapper)
//	                     gold    src/t_list.c:501       inside popGenericCommand (489-535)
//	                     edge    lpopCommand --CALLS--> popGenericCommand
//
// One outgoing CALLS edge away, in the same file, already in the graph.
//
// WHY search_related.go DOES NOT REACH IT. That block excludes outgoing CALLS deliberately
// (search_related.go:41-43) on the stated grounds that such cases "are already reached as co-members
// of the same unit". They are not, and the hole is visible in the code: searchRelatedUnitCoMembers
// requires member.ContainerID == anchor.ContainerID and returns NOTHING AT ALL once the unit holds
// more than searchRelatedUnitMemberLimit (12) members. `src/t_list.c` has ~40 top-level functions, so
// the co-member route switches off entirely and the callee is unreachable by any route in the block.
// The exclusion's reasoning holds for the block it was written about — a fourth kind would cost a
// slot in every rotation of a four-slot block — so this is a SEPARATE, gated route rather than a
// change to that rotation, and the related-sites block is left exactly as it is.
//
// WHY THE GATE IS NARROW. It fires for the top-ranked hit only (plus rank 2 when the ranking did not
// separate the two, the same searchFullUnitGapRatio test --full-unit-top uses), and it fires
// REGARDLESS of the anchor's unit size — because the measured miss is in a large unit, which is
// precisely where the co-member limit above turns off. A hop from deeper in the ranking would be a
// second ranking rather than a hop, and the ranking has already priced those files.
const (
	// searchCalleeHopSiteLimit is how many callees one search may admit, across all anchors. Three is
	// the widest fan-out that still names a specific place: an entry point that delegates to one
	// helper, plus the two-stage pipeline shape (parse then apply). Past that the answer is "read the
	// file", and `entire-graph impact` is the right command for a full callee list.
	searchCalleeHopSiteLimit = 3

	// searchCalleeHopSignal marks a result admitted by this route. It is reported so a payload is
	// attributable: every measurement of this lever has to be able to say which entries it added.
	searchCalleeHopSignal = "callee-hop"

	// searchCalleeHopCandidateLimit bounds how many outgoing edges are inspected per anchor, and with
	// them how many files may be hydrated to score overlap. A dispatcher with 200 calls is not a hop.
	searchCalleeHopCandidateLimit = 24

	// searchCalleeHopWindowLines is the window a callee body falls back to when its unit is past
	// searchFullUnitMaxLines: a bounded read window around the declaration, which still shows the
	// signature and the first statements.
	searchCalleeHopWindowLines = 60
)

// CalleeHopSignal is searchCalleeHopSignal exported for renderers and for the benchmark harness,
// which has to be able to count the entries this route added.
const CalleeHopSignal = searchCalleeHopSignal

// searchCalleeHopSite is one callee admitted as a candidate fix site.
type searchCalleeHopSite struct {
	// anchorIndex is the ranked result whose enclosing callable makes the call. The entry is
	// inserted directly after it, because "the helper this does its work in" is only useful next to
	// the thing that delegates to it.
	anchorIndex int
	symbol      SymbolRecord
	// callLine is where the anchor makes the call, from the relation's own evidence. It is reported
	// so the reader can see WHY this unit is here; it is not where the entry points (that is the
	// callee's declaration, which is what has to be edited).
	callLine int
	// sameFile is priority (a): a callee declared beside its caller is the likeliest second site, and
	// it is also free to print — the file is already hydrated.
	sameFile bool
	// overlap is priority (b): how much of the QUERY the callee's name and body spell out. A hop is
	// only worth bytes if the helper is plausibly what the issue is about.
	overlap float64
	// nameHits is how many query words the callee's NAME spells out. It is kept separately from
	// overlap because it is an ADMISSION test for a cross-file callee, not just an ordering signal:
	// see searchCalleeHopAdmissible.
	nameHits   int
	confidence float64
}

// searchCalleeHopAdmissible is the last gate, and it is asymmetric on purpose.
//
// A SAME-FILE callee is admitted unconditionally: it is structurally adjacent to the unit the agent is
// already reading, its file is already hydrated so it is free to print, and it is the shape the
// measured miss actually has (redis__redis-10095: lpopCommand -> popGenericCommand, 88 lines apart in
// src/t_list.c).
//
// A CROSS-FILE callee must have the query in its NAME. Without that test the slots go to the
// language's plumbing — the first cross-file hop this route produced on redis__redis-10095 was
// `zcalloc`, an allocator whose name shares no word with an issue about LPOP returning the wrong null
// reply. Body-mention alone is not enough: every helper in a file about replies mentions replies.
func searchCalleeHopAdmissible(site searchCalleeHopSite) bool {
	return site.sameFile || site.nameHits > 0
}

// searchCalleeHopRanks is how deep the hop reaches: rank 1 always, rank 2 only when the ranking did
// not separate it from rank 1. It is the same gate --full-unit-top uses, deliberately — a payload
// that shows two units because the scores tied should hop from both, and one that shows a clear
// winner should hop only from the winner.
func searchCalleeHopRanks(results []SearchResult) int {
	return searchFullUnitForceRanks(results, 2)
}

// selectSearchCalleeHopSites resolves the callees of the head's enclosing callables.
//
// Admission is deliberately strict, because an unresolved or out-of-repo callee is a slot spent on
// something the agent cannot edit:
//
//   - the edge is an outgoing CALLS/ASYNC_CALLS (searchRelatedCallRelation, shared with the related
//     block so "X's code runs Y" means one thing in this package);
//   - the target RESOLVES to a symbol this snapshot holds, which is what "same-repo, resolved" is:
//     an unresolved call has no body to print and no file to edit;
//   - the target is an enclosable callable, not a container;
//   - its file passes searchRelatedSiteEditable — the RANKER's own file-class and test-artifact
//     priors, reused rather than reinvented, so this route cannot recommend a file the ranking has
//     already ruled out as a fix site;
//   - the payload does not already print it (a hop onto a unit already on screen buys nothing).
func selectSearchCalleeHopSites(
	results []SearchResult,
	q searchQuery,
	relations []RelationRecord,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	cache *searchRelatedFileCache,
	hopRanks, limit int,
) []searchCalleeHopSite {
	if hopRanks <= 0 || limit <= 0 || len(results) == 0 {
		return nil
	}
	anchors := searchCalleeHopAnchors(results, symbolsByID, symbolsByFile, hopRanks)
	if len(anchors) == 0 {
		return nil
	}
	byAnchor := make(map[string]int, len(anchors))
	for index, anchor := range anchors {
		byAnchor[anchor.symbol.ID] = index
	}
	seen := map[string]bool{}
	inspected := make([]int, len(anchors))
	var sites []searchCalleeHopSite
	for _, relation := range relations {
		if !searchRelatedCallRelation(relation.Type) || relation.ToID == "" {
			continue
		}
		position, isAnchor := byAnchor[relation.FromID]
		if !isAnchor {
			continue
		}
		if inspected[position] >= searchCalleeHopCandidateLimit {
			continue
		}
		inspected[position]++
		callee, resolved := symbolsByID[relation.ToID]
		if !resolved || callee.ID == "" || seen[callee.ID] {
			continue
		}
		anchor := anchors[position]
		if callee.ID == anchor.symbol.ID || !searchEnclosableSymbolKind(callee.Kind) {
			continue
		}
		if !searchRelatedSiteEditable(q, callee.FilePath) {
			continue
		}
		if searchCalleeAlreadyPrinted(results, callee) {
			continue
		}
		seen[callee.ID] = true
		nameHits, overlap := searchCalleeQueryOverlap(q, callee, cache)
		site := searchCalleeHopSite{
			anchorIndex: anchor.resultIndex,
			symbol:      callee,
			callLine:    searchCalleeCallLine(relation, anchor.symbol),
			sameFile:    callee.FilePath == anchor.symbol.FilePath,
			overlap:     overlap,
			nameHits:    nameHits,
			confidence:  relation.Confidence,
		}
		if !searchCalleeHopAdmissible(site) {
			continue
		}
		sites = append(sites, site)
	}
	sort.SliceStable(sites, func(left, right int) bool {
		return lessSearchCalleeHopSite(sites[left], sites[right])
	})
	if len(sites) > limit {
		sites = sites[:limit]
	}
	return sites
}

// searchCalleeHopAnchor pairs a head result with the callable that makes the calls, keeping the
// result's index so the entry can be inserted next to it.
type searchCalleeHopAnchor struct {
	resultIndex int
	symbol      SymbolRecord
}

// searchCalleeHopAnchors walks only the FIRST hopRanks results, unlike searchRelatedAnchors which
// walks the whole primary list until it has collected its quota. The difference is the point: the gap
// test decided that ranks 1..hopRanks are the ambiguous head, and letting the walk slide down to rank
// 5 to fill a quota would hop from a rank the gate had already excluded.
func searchCalleeHopAnchors(
	results []SearchResult,
	symbolsByID map[string]SymbolRecord,
	symbolsByFile map[string][]SymbolRecord,
	hopRanks int,
) []searchCalleeHopAnchor {
	anchors := make([]searchCalleeHopAnchor, 0, hopRanks)
	seen := make(map[string]bool, hopRanks)
	for index := 0; index < minInt(hopRanks, len(results)); index++ {
		if results[index].Section != searchSectionPrimary {
			continue
		}
		symbol, ok := enclosingCallableForResult(results[index], symbolsByID, symbolsByFile)
		if !ok || symbol.ID == "" || seen[symbol.ID] {
			continue
		}
		seen[symbol.ID] = true
		anchors = append(anchors, searchCalleeHopAnchor{resultIndex: index, symbol: symbol})
	}
	return anchors
}

// lessSearchCalleeHopSite is the priority order the slots are spent in: same file, then query
// overlap, then edge confidence, then the anchor's rank, the call's own position and the symbol ID so
// the result is deterministic.
//
// QUALITY OUTRANKS THE ANCHOR. Sorting by anchor first would let rank 2's plumbing take a slot ahead
// of rank 1's same-file helper whenever rank 1 happens to make its calls in a different order; the
// three slots belong to the three best second sites, not one per anchor.
func lessSearchCalleeHopSite(left, right searchCalleeHopSite) bool {
	if left.sameFile != right.sameFile {
		return left.sameFile
	}
	if left.overlap != right.overlap {
		return left.overlap > right.overlap
	}
	if left.confidence != right.confidence {
		return left.confidence > right.confidence
	}
	if left.anchorIndex != right.anchorIndex {
		return left.anchorIndex < right.anchorIndex
	}
	if left.callLine != right.callLine {
		return left.callLine < right.callLine
	}
	return left.symbol.ID < right.symbol.ID
}

// searchCalleeCallLine reads the call site out of the relation's own evidence, falling back to the
// anchor's declaration. It is reported, never used for navigation: the line to EDIT is in the callee.
func searchCalleeCallLine(relation RelationRecord, anchor SymbolRecord) int {
	for _, evidence := range relation.Evidence {
		if evidence.FilePath == anchor.FilePath && evidence.StartLine > 0 {
			return evidence.StartLine
		}
	}
	return anchor.StartLine
}

// searchCalleeQueryOverlap scores how much of the query the callee spells out, as the share of the
// caller's own WORDS that appear in the callee's name or body. Words, not terms: `terms` also holds
// camelCase fragments and morphological variants mined out of identifiers, which are evidence for
// ranking but too loose to decide which of three helpers the issue is about.
//
// The name is worth double the body. A helper NAMED after the concept is the concept's implementation;
// one that merely mentions it in a comment or a call is not. The name-hit COUNT is returned alongside
// the score because it is also the admission test for a cross-file callee.
func searchCalleeQueryOverlap(
	q searchQuery, callee SymbolRecord, cache *searchRelatedFileCache,
) (int, float64) {
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	if len(words) == 0 {
		return 0, 0
	}
	name := strings.ToLower(callee.Name + " " + callee.QualifiedName)
	body := ""
	// Body text is free only when the file is already hydrated. A hop must not spend the hydration
	// allowance on scoring: the callees worth printing are overwhelmingly same-file, and that file is
	// already in the cache because the ranking printed a hit from it.
	if cache != nil && cache.cached(callee.FilePath) {
		if lines, ok := cache.get(callee.FilePath); ok {
			body = strings.ToLower(symbolBlockFromLines(lines, callee))
		}
	}
	nameHits, score := 0, 0.0
	for word := range words {
		// Two letters match everything. The floor is the same idea as searchLiteralMinLength: below it
		// a token is not a concept name.
		if len(word) < 3 {
			continue
		}
		switch {
		case strings.Contains(name, word):
			nameHits++
			score += 2
		case body != "" && strings.Contains(body, word):
			score += 1
		}
	}
	return nameHits, score / float64(2*len(words))
}

// searchCalleeAlreadyPrinted reports whether the payload already shows this unit — same symbol, or a
// snippet that already covers its declaration. Reuses the same test the related block applies for the
// same reason: a slot spent re-listing something on screen is a slot spent on nothing.
func searchCalleeAlreadyPrinted(results []SearchResult, callee SymbolRecord) bool {
	for _, result := range results {
		if result.SymbolID != "" && result.SymbolID == callee.ID {
			return true
		}
		if result.FilePath != callee.FilePath {
			continue
		}
		if result.SnippetStartLine <= callee.StartLine && result.SnippetEndLine >= callee.StartLine {
			return true
		}
	}
	return false
}

// searchCalleeHopResult renders one admitted callee as a candidate fix site.
//
// `withBody` follows the other editability levers rather than inventing a third policy: when the
// caller has asked for source (--full-unit-top or --edit-site-bodies) the entry carries the callee's
// unit, and otherwise it is a declaration locator. --callee-hop on its own therefore only ever adds
// the pointer, which is the cheap half of the lever and can be measured separately from the bytes.
func searchCalleeHopResult(site searchCalleeHopSite, lines []string, withBody bool) (SearchResult, bool) {
	start, end := clampRegion(site.symbol.StartLine, site.symbol.EndLine, len(lines))
	if start == 0 {
		return SearchResult{}, false
	}
	result := SearchResult{
		FilePath: site.symbol.FilePath, StartLine: start, EndLine: end, FocusLine: start,
		Kind: site.symbol.Kind, SymbolID: site.symbol.ID, SymbolName: site.symbol.Name,
		QualifiedName: site.symbol.QualifiedName, Signature: site.symbol.Signature,
		Section: searchSectionPrimary,
		Signals: []string{searchCalleeHopSignal},
	}
	if !withBody {
		result.SnippetStartLine, result.SnippetEndLine = start, start
		result.Snippet = lines[start-1]
		return result, true
	}
	printedStart, printedEnd := start, end
	if end-start+1 > searchFullUnitMaxLines {
		printedStart, printedEnd = clipSearchUnitToCap(start, end, start, searchCalleeHopWindowLines)
		result.UnitStartLine, result.UnitEndLine = start, end
		result.Signals = appendUnique(result.Signals, searchFullUnitSignal, searchFullUnitElidedSignal)
	} else {
		result.Signals = appendUnique(result.Signals, searchFullUnitSignal, searchCompleteSymbolSignal)
	}
	result.SnippetStartLine, result.SnippetEndLine = printedStart, printedEnd
	result.Snippet = strings.Join(lines[printedStart-1:printedEnd], "\n")
	result.FocusLine = minInt(maxInt(printedStart, result.FocusLine), printedEnd)
	return result, true
}

// mergeSearchCalleeHopSites seats the callee entries inside the ranking and reports how many it
// seated.
//
// FUNDING ORDER, and it is the contract:
//
//  1. the head the editability levers already seated is NEVER touched. A callee body is worth less
//     than the unit the agent is actually looking at, and this route exists to add a second site, not
//     to trade away the first.
//  2. callee bodies are paid for by TERSIFYING the tail — deepest rank first, down to but never into
//     the protected head. Nothing is DROPPED: the last mention of a file is not for sale, which is
//     the same rule searchRelatedDisplacementOrder enforces by a different mechanism, and shrinking a
//     snippet keeps path, line and symbol name where dropping a result loses the file entirely.
//  3. if the ceiling still cannot hold them, FEWER callees are seated, down to none. --max-context-bytes
//     is exact at every step: the returned plan is only ever one that measured under it.
func mergeSearchCalleeHopSites(
	results []SearchResult,
	entries []SearchResult,
	anchorIndexes []int,
	hardBudget, tailLines, protectRanks int,
) ([]SearchResult, int) {
	if len(results) == 0 || len(entries) == 0 || len(entries) != len(anchorIndexes) {
		return results, 0
	}
	if protectRanks < 1 {
		protectRanks = 1
	}
	floor := minInt(protectRanks, len(results))
	for count := len(entries); count >= 1; count-- {
		for demoteFrom := len(results); demoteFrom >= floor; demoteFrom-- {
			plan := planSearchCalleeHopRanking(results, entries[:count], anchorIndexes[:count], demoteFrom, tailLines)
			if hardBudget <= 0 || serializedSearchResultBytes(plan) <= hardBudget {
				return plan, count
			}
		}
	}
	return results, 0
}

// planSearchCalleeHopRanking builds one seating plan: the ranking with `demoteFrom` onwards tersified,
// each entry inserted directly after its anchor, renumbered 1..N.
func planSearchCalleeHopRanking(
	results, entries []SearchResult,
	anchorIndexes []int,
	demoteFrom, tailLines int,
) []SearchResult {
	after := make(map[int][]SearchResult, len(entries))
	for position, entry := range entries {
		index := anchorIndexes[position]
		after[index] = append(after[index], entry)
	}
	plan := make([]SearchResult, 0, len(results)+len(entries))
	for index, result := range results {
		if index >= demoteFrom {
			result = tersifySearchResult(result, tailLines)
		}
		plan = append(plan, result)
		plan = append(plan, after[index]...)
	}
	for index := range plan {
		plan[index].Rank = index + 1
	}
	return plan
}
