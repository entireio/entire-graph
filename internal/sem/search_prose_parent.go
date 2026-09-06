package sem

import (
	"sort"
	"strconv"
	"strings"
)

const (
	proseParentRetrievalSignal        = "retrieval_mode=prose-parent"
	defaultProseParentHeadWindowLines = 80
)

// proseParent is one retrievable unit of a prose corpus: the regions that share it are ranked
// against each other and only its best one is returned. Which regions share a unit is the whole
// question this file answers — see proseParentKey.
type proseParent struct {
	key        string
	path       string
	best       *searchCandidate
	candidates []*searchCandidate
}

// proseParentKey names the unit a prose region belongs to.
//
// THE UNIT OF RETRIEVAL FOR PROSE IS THE SECTION, NOT THE FILE. Ranking returns one region per
// unit, which is right for code — a second region of a function the caller can already see is
// worth less than the first region of one it cannot — and wrong for prose. A markdown document is
// not one indivisible thing: it is a sequence of headed sections, and a reader asking a question
// about a 400-turn chat log or a long design note wants the sections that answer it, not the one
// span the ranker liked best in each file. Every comparable document retriever splits a document
// on its headings for exactly this reason; keying the unit by file made entire-graph the outlier,
// and it imposed a hard cap: a corpus of N documents could never return more than N regions
// however large --top-k was, because there were only N units to rank.
//
// So a region that the parser recognized as belonging to a distinct section (markdown headings are
// parsed as `section` symbols) is its own unit, and competes on its own score exactly as an
// independent file would. A region with no symbol identity has no section to belong to, so it
// falls back to the file — which is the previous behaviour, and is what prose formats with no
// heading structure keep getting.
//
// The line is part of the key because a symbol ID names a heading, not an OCCURRENCE of one: a
// document that heads twelve entries "## note" carries twelve identically-identified sections, and
// keying by ID alone would collapse them back into the single unit this change exists to split.
//
// The unit is keyed by section only for regions of a DOCUMENT. The corpus gate is counted over the
// whole candidate path mix, so a docs-heavy repository opens it while still containing source
// files; without this check those files would be keyed by symbol too, and since a source file has
// many symbols they would each contribute many units. That is not merely off-model — one region per
// unit is what enforces MaxRegionsPerFile for code, so removing the rule silently uncaps it. A
// source region therefore keeps falling back to its file, which is exactly its previous behaviour.
func proseParentKey(candidate searchCandidate, sectionUnits bool) string {
	if !sectionUnits || candidate.result.SymbolID == "" || !proseParentPath(candidate.result.FilePath) {
		return candidate.result.FilePath
	}
	return candidate.result.SymbolID + ":" + strconv.Itoa(candidate.result.StartLine)
}

type proseParentSeed struct {
	parent    *proseParent
	candidate *searchCandidate
}

// resolvedSearchHeadWindowLines preserves an explicit caller choice. Otherwise,
// a native prose-parent result receives enough bounded source to answer from the
// selected session instead of returning only its best local locator.
func resolvedSearchHeadWindowLines(results []SearchResult, requested int) int {
	if requested > 0 {
		return requested
	}
	for _, result := range results {
		if hasSearchSignal(result, proseParentRetrievalSignal) {
			return defaultProseParentHeadWindowLines
		}
	}
	return 0
}

// resolvedSearchSnippetGrowth keeps the measured code-search growth allowance
// for ordinary results. Prose-parent retrieval may spend the caller's explicit
// hard ceiling because a useful session can be larger than a code body, while
// the same ceiling still bounds the serialized response.
func resolvedSearchSnippetGrowth(results []SearchResult, hardBudget int) int {
	if hardBudget <= 0 {
		return searchEnclosureGrowthBytes
	}
	for _, result := range results {
		if hasSearchSignal(result, proseParentRetrievalSignal) {
			return hardBudget
		}
	}
	return searchEnclosureGrowthBytes
}

// selectSearchCandidates preserves the ordinary region ranking unless the
// existing candidate universe is overwhelmingly prose. In a prose corpus it
// protects the baseline head and spends only the tail on deterministic
// per-query-term parent coverage.
func selectSearchCandidates(
	candidates []searchCandidate,
	q searchQuery,
	topK int,
	maxPerFile int,
	sectionUnits bool,
) []searchCandidate {
	baseline := selectDiverseCandidates(candidates, topK, maxPerFile)
	parents := proseParents(candidates, sectionUnits)
	if !proseParentCorpus(parents) {
		return baseline
	}
	// Passages are planned over the whole DOCUMENT even when the ranked unit is a section, because
	// what a passage is for has not changed: it carries the distant same-document regions the
	// one-region-per-unit rule discarded. Under section units those are the sections that lost the
	// slot competition — dropping them would have taken evidence away from a caller reading a
	// ten-result payload of an eighteen-document corpus, which is a docs repo, not a benchmark.
	// dropSelectedProsePassages then removes the ones that won a slot after all.
	documents := parents
	if sectionUnits {
		documents = proseParents(candidates, false)
	}
	terms := proseParentQueryTerms(q)
	if len(terms) == 0 {
		return baseline
	}

	selectedKeys := map[string]bool{}
	selectedPaths := map[string]bool{}
	// documentTarget is how many distinct documents this selection can represent at all. Until it
	// is met, EVERY pass below admits only units from documents that have none yet — see the
	// breadth rule at appendParent.
	documentTarget := minInt(topK, proseParentDocumentCount(parents))
	selected := make([]searchCandidate, 0, minInt(topK, len(parents)))
	appendParent := func(parent *proseParent, fallback searchCandidate) bool {
		if parent == nil || selectedKeys[parent.key] || len(selected) == topK {
			return false
		}
		// BREADTH BEFORE DEPTH. A question is often answered by the one document nobody's terms
		// ranked highly, so no document gets a SECOND region until every document that fits has
		// one. The file-keyed unit got this for free — one unit per document meant selection could
		// not return to a document — and it is the one property section units could lose: a pure
		// score ranking hands every slot to whichever document's sections dominate the score list.
		// It binds every pass, not just the first, because the per-term coverage pass is where a
		// weakly-scoring document has always been admitted.
		if sectionUnits && len(selectedPaths) < documentTarget && selectedPaths[parent.path] {
			return false
		}
		candidate := fallback
		if parent.best != nil {
			candidate = *parent.best
		}
		candidate.result.Signals = appendUnique(candidate.result.Signals, proseParentRetrievalSignal)
		// Passages, and therefore multi-resolution promotion, are for DOCUMENTS. The corpus gate is
		// counted over the whole path mix, so a docs-heavy repository admits its source files here
		// too; planning passages for them would hand a code file extra result slots through
		// promotion, which is the same uncapping that section keying caused and which
		// MaxRegionsPerFile is supposed to prevent. A code region stays a single ranked region.
		if proseParentPath(candidate.result.FilePath) {
			candidate.prosePassagePlan = planProseParentPassages(documents[parent.path], candidate, q, maxInt(0, topK-1))
		}
		selectedKeys[parent.key] = true
		selectedPaths[parent.path] = true
		selected = append(selected, candidate)
		return true
	}

	headParents := proseParentHeadCount(q, topK)
	// The head: the best region of each document, in the ordinary region ranking's order.
	// Unchanged — with section units the breadth rule already restricts it to one region per
	// document, so `continue` only skips a candidate this pass was never going to take.
	for _, candidate := range baseline {
		if len(selected) >= headParents {
			break
		}
		if sectionUnits && selectedPaths[candidate.result.FilePath] {
			continue
		}
		appendParent(parents[proseParentKey(candidate, sectionUnits)], candidate)
	}
	// THEN MERIT: the best remaining sections overall, in score order (`candidates` is already
	// sorted). This is the pass the file-keyed unit could not have: it is what lets the four
	// sections of the one document that actually discusses the question all come back, and it
	// ranks them against sections of every other document on their own scores rather than on
	// their document's. It spends only the head budget, so the per-term coverage pass below keeps
	// the tail it has always had.
	if sectionUnits {
		for _, candidate := range candidates {
			if len(selected) >= headParents {
				break
			}
			appendParent(parents[proseParentKey(candidate, sectionUnits)], candidate)
		}
	}

	termLists := proseParentTermLists(candidates, parents, terms, topK, sectionUnits)
	for depth := 0; len(selected) < topK; depth++ {
		sawDepth := false
		for _, list := range termLists {
			if depth >= len(list) {
				continue
			}
			sawDepth = true
			seed := list[depth]
			appendParent(seed.parent, *seed.candidate)
			if len(selected) == topK {
				break
			}
		}
		if !sawDepth {
			break
		}
	}
	for _, candidate := range baseline {
		if len(selected) == topK {
			break
		}
		appendParent(parents[proseParentKey(candidate, sectionUnits)], candidate)
	}
	for _, candidate := range candidates {
		if len(selected) == topK {
			break
		}
		appendParent(parents[proseParentKey(candidate, sectionUnits)], candidate)
	}
	return dropSelectedProsePassages(selected)
}

// dropSelectedProsePassages keeps every returned region unique. Passages are planned per selected
// unit over the whole document, so the same unselected region is planned once for EVERY selected
// section of that document, and a region that won a slot of its own is also still planned as a
// passage of its neighbours. Both are the same fault — the payload would show one region twice —
// so both are dropped here: a region is returned as a ranked result if it earned a slot, otherwise
// as a passage of exactly one result, the highest-ranked one that planned it.
func dropSelectedProsePassages(selected []searchCandidate) []searchCandidate {
	claimed := make(map[string][]SearchPassage, len(selected))
	for index := range selected {
		path := selected[index].result.FilePath
		claimed[path] = append(claimed[path], primarySearchPassage(selected[index].result))
	}
	for index := range selected {
		plan := selected[index].prosePassagePlan
		if len(plan) == 0 {
			continue
		}
		path := selected[index].result.FilePath
		kept := plan[:0]
		for _, passage := range plan {
			taken := false
			for _, existing := range claimed[path] {
				if passagesOverlap(existing, passage) {
					taken = true
					break
				}
			}
			if taken {
				continue
			}
			claimed[path] = append(claimed[path], passage)
			kept = append(kept, passage)
		}
		if len(kept) == 0 {
			kept = nil
		}
		selected[index].prosePassagePlan = kept
	}
	return selected
}

// dropContainedProseResults removes a prose result whose PRINTED span lies entirely inside the
// printed span of a higher-ranked result from the same file.
//
// dropSelectedProsePassages already keeps the ranked regions disjoint, but it runs on the spans as
// SELECTED. allocateSearchSnippets then grows them — a prose head takes an 80-line window — and
// growth is what creates the containment: a head window that starts at line 1 and ends at line 63
// swallows the sections at lines 8, 13, 18 and 23 that legitimately won their own slots. Nothing
// re-checked the spans after they grew, so the payload printed that text once per contained result.
// The cost is paid twice over, because a search payload is replayed into a model on every later
// turn.
//
// Only prose results are considered. A contained CODE region is a different statement about the
// same lines (an inner function inside an outer one, say), and one region per unit already bounds
// how many of those a file can contribute.
func dropContainedProseResults(results []SearchResult) []SearchResult {
	kept := make([]SearchResult, 0, len(results))
	for index := range results {
		result := results[index]
		contained := false
		if searchResultIsProse(result) && result.SnippetStartLine > 0 && result.SnippetEndLine >= result.SnippetStartLine {
			for _, prior := range kept {
				if prior.FilePath != result.FilePath || prior.SnippetStartLine <= 0 {
					continue
				}
				if prior.SnippetStartLine <= result.SnippetStartLine && result.SnippetEndLine <= prior.SnippetEndLine {
					contained = true
					break
				}
			}
		}
		if !contained {
			kept = append(kept, result)
		}
	}
	if len(kept) == len(results) {
		return results
	}
	for index := range kept {
		kept[index].Rank = index + 1
	}
	return kept
}

func searchResultIsProse(result SearchResult) bool {
	return hasSearchSignal(result, proseParentRetrievalSignal) || hasSearchSignal(result, proseResolutionSignal)
}

func proseQueryRequestsMultipleParents(q searchQuery) bool {
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	if words["else"] || (words["other"] && words["than"]) {
		return true
	}
	written := q.wordSequence
	if len(written) == 0 {
		written = searchQueryWordSequence(q.rawLower)
	}
	for index, word := range written {
		if word == "which" && proseWhichListFrame(written, index) {
			return true
		}
		if (word == "what" || word == "where") && index+1 < len(written) && written[index+1] == "are" &&
			safeASCIIWrittenPlural(written[len(written)-1]) {
			return true
		}
	}
	return false
}

func proseWhichListFrame(written []string, whichIndex int) bool {
	for headOffset := 1; headOffset <= 2; headOffset++ {
		headIndex := whichIndex + headOffset
		predicateIndex := headIndex + 1
		if predicateIndex >= len(written) || !safeASCIIWrittenPlural(written[headIndex]) {
			continue
		}
		if proseListPredicate(written[predicateIndex]) {
			return true
		}
	}
	return false
}

func proseListPredicate(word string) bool {
	switch word {
	case "are", "contain", "cover", "describe", "have", "hold", "include", "list", "match", "mention", "show", "store", "use":
		return true
	default:
		return false
	}
}

func proseParentHeadCount(q searchQuery, topK int) int {
	if topK <= 0 {
		return 0
	}
	if proseQueryRequestsMultipleParents(q) {
		return maxInt(1, topK/3)
	}
	return (topK + 1) / 2
}

func proseParents(candidates []searchCandidate, sectionUnits bool) map[string]*proseParent {
	parents := make(map[string]*proseParent)
	for index := range candidates {
		candidate := &candidates[index]
		key := proseParentKey(*candidate, sectionUnits)
		parent := parents[key]
		if parent == nil {
			parent = &proseParent{key: key, path: candidate.result.FilePath, best: candidate}
			parents[key] = parent
		}
		parent.candidates = append(parent.candidates, candidate)
		if searchCandidateScoreLess(*candidate, *parent.best) {
			parent.best = candidate
		}
	}
	return parents
}

// proseParentCorpus decides whether this is a prose corpus at all, and it is deliberately counted
// over distinct FILES rather than over units: what makes a corpus prose is what the documents are,
// not how finely they are subdivided, so the gate reads identically whichever unit is in force.
func proseParentDocumentCount(parents map[string]*proseParent) int {
	paths := make(map[string]bool, len(parents))
	for _, parent := range parents {
		paths[parent.path] = true
	}
	return len(paths)
}

func proseParentCorpus(parents map[string]*proseParent) bool {
	paths := make(map[string]bool, len(parents))
	for _, parent := range parents {
		paths[parent.path] = true
	}
	if len(paths) < 4 {
		return false
	}
	prose := 0
	for path := range paths {
		if proseParentPath(path) {
			prose++
		}
	}
	return prose*5 >= len(paths)*4
}

func proseParentPath(path string) bool {
	lower := strings.ToLower(path)
	for _, extension := range searchDocExtensions {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

func proseParentQueryTerms(q searchQuery) []string {
	written := q.wordSequence
	if len(written) == 0 {
		written = q.matchableWords
	}
	terms := make([]string, 0, len(written))
	seen := map[string]bool{}
	for _, term := range written {
		term = strings.ToLower(term)
		if seen[term] || !q.termSet[term] || len(term) < 2 || !proseASCIIWord(term) {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms
}

func proseParentTermLists(
	candidates []searchCandidate,
	parents map[string]*proseParent,
	terms []string,
	limit int,
	sectionUnits bool,
) [][]proseParentSeed {
	bestByTerm := make([]map[string]*searchCandidate, len(terms))
	for termIndex := range bestByTerm {
		bestByTerm[termIndex] = make(map[string]*searchCandidate, len(parents))
	}
	matched := make([]bool, len(terms))
	for candidateIndex := range candidates {
		candidate := &candidates[candidateIndex]
		if !proseParentPath(candidate.result.FilePath) {
			continue
		}
		key := proseParentKey(*candidate, sectionUnits)
		clear(matched)
		allMatched := true
		for termIndex, term := range terms {
			matched[termIndex] = candidate.termCounts[term] > 0
			allMatched = allMatched && matched[termIndex]
		}
		if !allMatched {
			for _, token := range proseASCIITokens(candidate.result.Snippet + "\n" + candidate.result.Signature) {
				for termIndex, term := range terms {
					if !matched[termIndex] && safeProseInflectionMatch(term, token) {
						matched[termIndex] = true
					}
				}
			}
		}
		for termIndex, matches := range matched {
			if !matches {
				continue
			}
			best := bestByTerm[termIndex][key]
			if best == nil || searchCandidateScoreLess(*candidate, *best) {
				bestByTerm[termIndex][key] = candidate
			}
		}
	}

	termLists := make([][]proseParentSeed, len(terms))
	for termIndex, bestByParent := range bestByTerm {
		for key, candidate := range bestByParent {
			termLists[termIndex] = insertProseParentSeed(termLists[termIndex], proseParentSeed{
				parent: parents[key], candidate: candidate,
			}, limit)
		}
	}
	return termLists
}

func safeProseInflectionMatch(left, right string) bool {
	if left == right {
		return true
	}
	if plural, ok := safeASCIIPlural(left); ok && plural == right {
		return true
	}
	if plural, ok := safeASCIIPlural(right); ok && plural == left {
		return true
	}
	for _, form := range safeASCIIProseVerbForms(left) {
		if form == right {
			return true
		}
	}
	for _, form := range safeASCIIProseVerbForms(right) {
		if form == left {
			return true
		}
	}
	if safeASCIIProseDerivation(left, right) || safeASCIIProseDerivation(right, left) {
		return true
	}
	return safeASCIIProseEdgeCompound(left, right)
}

func safeASCIIProseVerbForms(word string) []string {
	if len(word) < 5 || !proseASCIIWord(word) {
		return nil
	}
	if strings.HasSuffix(word, "e") {
		return []string{word + "d", strings.TrimSuffix(word, "e") + "ing"}
	}
	return []string{word + "ed", word + "ing"}
}

func safeASCIIProseDerivation(base, derived string) bool {
	if len(base) < 6 || !proseASCIIWord(base) || !proseASCIIWord(derived) {
		return false
	}
	if strings.HasSuffix(base, "ate") {
		return derived == strings.TrimSuffix(base, "ate")+"ation"
	}
	if strings.HasSuffix(base, "ize") {
		return derived == strings.TrimSuffix(base, "e")+"ation"
	}
	return false
}

// safeASCIIProseEdgeCompound is directional: queryTerm is the word the caller
// wrote and evidenceToken is the corpus token. A longer query compound may
// expose an exact standalone evidence word at either edge; a longer evidence
// token cannot manufacture coverage for a shorter query.
func safeASCIIProseEdgeCompound(queryTerm, evidenceToken string) bool {
	if !proseASCIIWord(queryTerm) || !proseASCIIWord(evidenceToken) {
		return false
	}
	difference := len(queryTerm) - len(evidenceToken)
	if len(evidenceToken) < 8 || difference < 2 || difference > 5 || len(evidenceToken)*3 < len(queryTerm)*2 {
		return false
	}
	if strings.HasPrefix(queryTerm, evidenceToken) {
		suffix := strings.TrimPrefix(queryTerm, evidenceToken)
		return !safeASCIIProseOpposingSuffix(suffix)
	}
	if !strings.HasSuffix(queryTerm, evidenceToken) {
		return false
	}
	prefix := strings.TrimSuffix(queryTerm, evidenceToken)
	return !safeASCIIProseOpposingPrefix(prefix)
}

func safeASCIIProseOpposingSuffix(suffix string) bool {
	switch suffix {
	case "free", "less":
		return true
	default:
		return false
	}
}

func safeASCIIProseOpposingPrefix(prefix string) bool {
	switch prefix {
	case "anti", "counter", "de", "dis", "il", "im", "in", "ir", "mis", "non", "un":
		return true
	default:
		return false
	}
}

// safeASCIIWrittenPlural recognizes only forms that can be reversed without
// treating common singular s-ending words as list intent. It is deliberately
// conservative: missing an irregular plural keeps the ordinary allocation,
// while a false plural silently changes how much of top-k is reserved.
func safeASCIIWrittenPlural(word string) bool {
	if len(word) <= 4 || !proseASCIIWord(word) || !strings.HasSuffix(word, "s") || strings.HasSuffix(word, "ss") {
		return false
	}
	for _, singularSuffix := range []string{"as", "is", "us"} {
		if strings.HasSuffix(word, singularSuffix) {
			return false
		}
	}
	singular := strings.TrimSuffix(word, "s")
	if len(singular) == 0 || strings.ContainsRune("aiu", rune(singular[len(singular)-1])) {
		return false
	}
	plural, ok := safeASCIIPlural(singular)
	return ok && plural == word
}

func safeASCIIPlural(word string) (string, bool) {
	if len(word) < 4 || !proseASCIIWord(word) {
		return "", false
	}
	if strings.HasSuffix(word, "y") && !strings.ContainsRune("aeiou", rune(word[len(word)-2])) {
		return word[:len(word)-1] + "ies", true
	}
	for _, suffix := range []string{"ch", "sh", "x", "z"} {
		if strings.HasSuffix(word, suffix) {
			return word + "es", true
		}
	}
	if strings.HasSuffix(word, "s") {
		return "", false
	}
	return word + "s", true
}

func proseASCIITokens(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	start := -1
	for index := 0; index <= len(text); index++ {
		letter := index < len(text) && text[index] >= 'a' && text[index] <= 'z'
		if letter && start < 0 {
			start = index
			continue
		}
		if !letter && start >= 0 {
			tokens = append(tokens, text[start:index])
			start = -1
		}
	}
	return tokens
}

func proseASCIIWord(word string) bool {
	if word == "" {
		return false
	}
	for index := range word {
		if word[index] < 'a' || word[index] > 'z' {
			return false
		}
	}
	return true
}

func proseParentSeedLess(left, right proseParentSeed) bool {
	if left.candidate.score != right.candidate.score {
		return left.candidate.score > right.candidate.score
	}
	if left.parent.key != right.parent.key {
		return left.parent.key < right.parent.key
	}
	return searchCandidateLess(*left.candidate, *right.candidate)
}

func insertProseParentSeed(list []proseParentSeed, seed proseParentSeed, limit int) []proseParentSeed {
	if limit <= 0 {
		return list
	}
	// Selection cannot observe a term seed beyond top-k: reaching that depth
	// means the preceding top-k distinct parents were already admitted.
	position := sort.Search(len(list), func(index int) bool {
		return proseParentSeedLess(seed, list[index])
	})
	if len(list) == limit && position == limit {
		return list
	}
	if len(list) < limit {
		list = append(list, proseParentSeed{})
	}
	copy(list[position+1:], list[position:])
	list[position] = seed
	return list
}

func searchCandidateScoreLess(left, right searchCandidate) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	return searchCandidateLess(left, right)
}
