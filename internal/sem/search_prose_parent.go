package sem

import (
	"sort"
	"strings"
)

const (
	proseParentRetrievalSignal        = "retrieval_mode=prose-parent"
	defaultProseParentHeadWindowLines = 80
)

type proseParent struct {
	path       string
	best       *searchCandidate
	candidates []*searchCandidate
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
) []searchCandidate {
	baseline := selectDiverseCandidates(candidates, topK, maxPerFile)
	parents := proseParents(candidates)
	if !proseParentCorpus(parents) {
		return baseline
	}
	terms := proseParentQueryTerms(q)
	if len(terms) == 0 {
		return baseline
	}

	selectedPaths := map[string]bool{}
	selected := make([]searchCandidate, 0, minInt(topK, len(parents)))
	appendParent := func(parent *proseParent, fallback searchCandidate) bool {
		if parent == nil || selectedPaths[parent.path] || len(selected) == topK {
			return false
		}
		candidate := fallback
		if parent.best != nil {
			candidate = *parent.best
		}
		candidate.result.Signals = appendUnique(candidate.result.Signals, proseParentRetrievalSignal)
		candidate.prosePassagePlan = planProseParentPassages(parent, candidate, q, maxInt(0, topK-1))
		selectedPaths[parent.path] = true
		selected = append(selected, candidate)
		return true
	}

	headParents := proseParentHeadCount(q, topK)
	for _, candidate := range baseline {
		if len(selected) == headParents {
			break
		}
		appendParent(parents[candidate.result.FilePath], candidate)
	}

	termLists := proseParentTermLists(candidates, parents, terms, topK)
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
		appendParent(parents[candidate.result.FilePath], candidate)
	}
	for _, candidate := range candidates {
		if len(selected) == topK {
			break
		}
		appendParent(parents[candidate.result.FilePath], candidate)
	}
	return selected
}

func proseQueryRequestsMultipleParents(q searchQuery) bool {
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	interrogative := words["what"] || words["which"] || words["where"]
	if !interrogative {
		return false
	}
	if words["else"] || (words["other"] && words["than"]) {
		return true
	}
	written := q.wordSequence
	if len(written) == 0 {
		written = searchQueryWordSequence(q.rawLower)
	}
	for _, word := range written {
		if len(word) <= 4 || !strings.HasSuffix(word, "s") || strings.HasSuffix(word, "ss") {
			continue
		}
		singular := strings.TrimSuffix(word, "s")
		if plural, ok := safeASCIIPlural(singular); ok && plural == word {
			return true
		}
	}
	return false
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

func proseParents(candidates []searchCandidate) map[string]*proseParent {
	parents := make(map[string]*proseParent)
	for index := range candidates {
		candidate := &candidates[index]
		path := candidate.result.FilePath
		parent := parents[path]
		if parent == nil {
			parent = &proseParent{path: path, best: candidate}
			parents[path] = parent
		}
		parent.candidates = append(parent.candidates, candidate)
		if searchCandidateScoreLess(*candidate, *parent.best) {
			parent.best = candidate
		}
	}
	return parents
}

func proseParentCorpus(parents map[string]*proseParent) bool {
	if len(parents) < 4 {
		return false
	}
	prose := 0
	for path := range parents {
		if proseParentPath(path) {
			prose++
		}
	}
	return prose*5 >= len(parents)*4
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
) [][]proseParentSeed {
	bestByTerm := make([]map[string]*searchCandidate, len(terms))
	for termIndex := range bestByTerm {
		bestByTerm[termIndex] = make(map[string]*searchCandidate, len(parents))
	}
	matched := make([]bool, len(terms))
	for candidateIndex := range candidates {
		candidate := &candidates[candidateIndex]
		path := candidate.result.FilePath
		if !proseParentPath(path) {
			continue
		}
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
			best := bestByTerm[termIndex][path]
			if best == nil || searchCandidateScoreLess(*candidate, *best) {
				bestByTerm[termIndex][path] = candidate
			}
		}
	}

	termLists := make([][]proseParentSeed, len(terms))
	for termIndex, bestByParent := range bestByTerm {
		for path, candidate := range bestByParent {
			termLists[termIndex] = insertProseParentSeed(termLists[termIndex], proseParentSeed{
				parent: parents[path], candidate: candidate,
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
	return safeASCIIProsePrefixFamily(left, right)
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

func safeASCIIProsePrefixFamily(left, right string) bool {
	if len(left) < 6 || len(right) < 6 || !proseASCIIWord(left) || !proseASCIIWord(right) {
		return false
	}
	difference := len(left) - len(right)
	if difference < 0 {
		difference = -difference
	}
	if difference > 5 {
		return false
	}
	common := 0
	for common < len(left) && common < len(right) && left[common] == right[common] {
		common++
	}
	shorter := minInt(len(left), len(right))
	return shorter >= 8 && common >= 5 && common*3 >= shorter*2
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
	if left.parent.path != right.parent.path {
		return left.parent.path < right.parent.path
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
