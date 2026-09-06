package sem

import (
	"testing"
)

// A query word the corpus cannot match must contribute ZERO — it may not revoke a bonus the
// words that did match already earned. These tests pin the three places that used to be
// all-or-nothing over every token the caller typed: the exact-symbol name test, the
// all-query-terms/coverage normalizer, and code-likeness read out of capitalization.

func TestMatchesExactSymbolFormIgnoresUnmatchableWords(t *testing.T) {
	t.Parallel()
	// Document frequencies of a corpus that contains none of the identifier words but does
	// contain the polite filler, so "please" cannot be dismissed as corpus-absent and
	// "xyzzy" can. Both must be inert all the same.
	corpus := map[string]int{
		"build": 3, "group": 3, "index": 3, "buildgroupindex": 1,
		"update": 5, "market": 2, "sizes": 2, "size": 2,
		"get": 9, "value": 9, "parser": 4, "fails": 2,
		"please": 1,
	}
	for _, testCase := range []struct {
		name    string
		query   string
		symbol  string
		matched bool
	}{
		{"identifier alone", "buildGroupIndex", "buildGroupIndex", true},
		{"identifier plus present filler", "buildGroupIndex please", "buildGroupIndex", true},
		{"identifier plus absent token", "buildGroupIndex xyzzy", "buildGroupIndex", true},
		{"identifier inside a sentence", "the bug in buildGroupIndex", "buildGroupIndex", true},
		{"spelled out as words", "update market sizes", "updateMarketSizes", true},
		{"spelled out with article", "update the market sizes", "updateMarketSizes", true},
		{"spelled out with article and filler", "update the market sizes please", "updateMarketSizes", true},
		{"spelled out with absent token", "update market sizes xyzzy", "updateMarketSizes", true},
		{"spelled out inside a sentence", "we should update market sizes on save", "updateMarketSizes", true},
		{"stop word inside the name", "get the value", "get_the_value", true},
		{"stop word inside the name plus absent token", "get the value xyzzy", "get_the_value", true},
		{"single word query", "parser", "parser", true},

		{"camel token inside a sentence", "NonNull on a record is ignored", "NonNull", true},
		{"snake token inside a sentence", "the in_tail plugin stalls", "in_tail", true},

		// Precision: widening must not turn every query word into an exact name match.
		{"single prose word inside a sentence", "the parser fails", "parser", false},
		{"shouted prose word is not an identifier", "[BUG] the parser fails", "bug", false},
		{"capitalized prose word is not an identifier", "Reproduce the parser bug", "reproduce", false},
		{"prefix of a longer typed identifier", "buildGroupIndexEntry", "buildGroupIndex", false},
		{"name longer than the phrase", "update market sizes", "updateMarketSizesNow", false},
		// A shorter adjacent phrase is still the caller spelling that name out; the
		// longer name additionally wins on term coverage, so the fuller match still leads.
		{"name is a shorter adjacent phrase", "update market sizes", "updateMarket", true},
		{"words out of order", "sizes market update", "updateMarketSizes", false},
		{"non-adjacent words", "update sizes of the market", "updateMarketSizes", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			q := buildSearchQuery(testCase.query).withCorpusPresence(corpus)
			got := matchesExactSymbolForm(q, compactSearchIdentifier(testCase.symbol))
			if got != testCase.matched {
				t.Fatalf("matchesExactSymbolForm(%q, %q) = %v, want %v (forms: words=%v matchable=%v identifiers=%v)",
					testCase.query, testCase.symbol, got, testCase.matched,
					q.wordSequence, q.matchableWords, q.identifierTokens)
			}
		})
	}
}

func TestSpellsCompactIdentifierRequiresWholeAdjacentWords(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		words   []string
		compact string
		want    bool
	}{
		{"exact run", []string{"update", "market", "sizes"}, "updatemarketsizes", true},
		{"run at the head", []string{"update", "market", "sizes", "please"}, "updatemarket", true},
		{"run at the tail", []string{"please", "update", "market"}, "updatemarket", true},
		{"run in the middle", []string{"fix", "update", "market", "now"}, "updatemarket", true},
		{"single word never spells a name", []string{"parser", "fails"}, "parser", false},
		{"partial word rejected", []string{"update", "market"}, "updatemark", false},
		{"trailing remainder rejected", []string{"update", "market"}, "updatemarketsizes", false},
		{"gap rejected", []string{"update", "of", "market"}, "updatemarket", false},
		{"empty sequence", nil, "updatemarket", false},
		{"repeated word run", []string{"set", "set", "value"}, "setsetvalue", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := spellsCompactIdentifier(testCase.words, testCase.compact); got != testCase.want {
				t.Fatalf("spellsCompactIdentifier(%v, %q) = %v, want %v",
					testCase.words, testCase.compact, got, testCase.want)
			}
		})
	}
}

func TestMatchableQueryWordsDropsOnlyUnmatchableWords(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("update the market sizes xyzzy")
	corpus := map[string]int{"update": 4, "market": 2, "sizes": 2, "size": 2}
	// Before corpus statistics exist only the never-indexed words (stop words) are known.
	if got := q.matchableWords; !equalStrings(got, []string{"update", "market", "sizes", "xyzzy"}) {
		t.Fatalf("matchable words before corpus statistics = %v", got)
	}
	if got := q.withCorpusPresence(corpus).matchableWords; !equalStrings(got, []string{"update", "market", "sizes"}) {
		t.Fatalf("matchable words after corpus statistics = %v", got)
	}
	// The written order is never mutated: it is what keeps `get_the_value` reachable.
	if got := q.wordSequence; !equalStrings(got, []string{"update", "the", "market", "sizes", "xyzzy"}) {
		t.Fatalf("written word sequence = %v", got)
	}
}

func TestScoreSearchCandidatesChargesOnlyTermsTheCorpusHas(t *testing.T) {
	t.Parallel()
	newCandidate := func(counts map[string]int) []searchCandidate {
		return []searchCandidate{{
			result:     SearchResult{FilePath: "lib/alpha.go", StartLine: 1, EndLine: 4, SymbolID: "s1"},
			termCounts: counts,
			docLength:  40,
		}}
	}
	corpus := map[string]int{"alpha": 2, "beta": 3}
	baseline := newCandidate(map[string]int{"alpha": 2, "beta": 1})
	scoreSearchCandidates(baseline, buildSearchQuery("alpha beta"), corpus, 20)

	for _, testCase := range []struct {
		name  string
		query string
	}{
		{"nonsense token", "alpha beta xyzzy"},
		{"polite filler absent from the corpus", "alpha beta please"},
		{"two absent tokens", "alpha beta xyzzy frobnicate"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			perturbed := newCandidate(map[string]int{"alpha": 2, "beta": 1})
			// The corpus map is the authority on presence: the extra token is missing
			// from it, exactly as an unmatched term is missing from every document.
			scoreSearchCandidates(perturbed, buildSearchQuery(testCase.query), corpus, 20)
			if perturbed[0].score != baseline[0].score {
				t.Fatalf("score with %q = %.6f, want %.6f (unmatched terms must contribute zero)",
					testCase.query, perturbed[0].score, baseline[0].score)
			}
			if !containsString(perturbed[0].result.Signals, "all-query-terms") {
				t.Fatalf("all-query-terms revoked by an unmatchable token: %v", perturbed[0].result.Signals)
			}
		})
	}
}

// File preselection decides which files get indexed at all, so the same rule has to hold
// there: a term no file matched may not shrink the coverage share of the files that did.
func TestApplySearchFileCoverageChargesOnlyMatchedTerms(t *testing.T) {
	t.Parallel()
	files := func() []searchFileCandidate {
		return []searchFileCandidate{
			{path: "market/sizes.go", score: 4, matchedWeight: 2},
			{path: "docs/market.md", score: 6, matchedWeight: 1},
		}
	}
	baseline := files()
	q := buildSearchQuery("alpha beta")
	applySearchFileCoverage(baseline, q, []bool{true, true})

	for _, testCase := range []struct {
		name  string
		query string
	}{
		{"nonsense token", "alpha beta xyzzy"},
		{"two absent tokens", "alpha beta xyzzy frobnicate"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			perturbed := files()
			perturbedQuery := buildSearchQuery(testCase.query)
			// Only the first two terms were matched by any file, exactly as the scan
			// would report; the rest are dead weight the denominator must not carry.
			matched := make([]bool, len(perturbedQuery.terms))
			for index, term := range perturbedQuery.terms {
				matched[index] = q.termSet[term]
			}
			applySearchFileCoverage(perturbed, perturbedQuery, matched)
			for index := range perturbed {
				if perturbed[index].score != baseline[index].score {
					t.Fatalf("%s scored %.6f with %q, want %.6f",
						perturbed[index].path, perturbed[index].score, testCase.query,
						baseline[index].score)
				}
			}
		})
	}
	// It also has to actually do its job: coverage must still separate the files.
	if baseline[0].score <= 4 || baseline[1].score <= 6 {
		t.Fatalf("coverage share was not applied: %#v", baseline)
	}
	if baseline[0].score-4 <= baseline[1].score-6 {
		t.Fatalf("file matching more query weight did not gain more coverage: %#v", baseline)
	}
	matchedByFirst := []bool{true, false}
	owned := []searchFileCandidate{{
		path: "market/owned.go", score: 2, matchedWeight: 1, matchedTerms: matchedByFirst,
	}}
	applySearchFileCoverage(owned, q, []bool{true, true})
	matchedByFirst[0] = false
	if !owned[0].matchedTerms[0] {
		t.Fatalf("coverage did not retain an owned matched-term slice: %#v", owned[0])
	}
}

func TestSearchRepositoryIgnoresCorpusAbsentQueryTokens(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "market/sizes.go", `package market

// UpdateMarketSizes recomputes the stored market sizes for every tracked region.
func UpdateMarketSizes(regions []string) int {
	total := 0
	for range regions {
		total++
	}
	return total
}
`)
	write(t, repo, "market/regions.go", `package market

// ListRegions returns the tracked regions used when we update market sizes.
func ListRegions() []string {
	return []string{"emea"}
}
`)

	search := func(query string) SearchResult {
		t.Helper()
		response, err := SearchRepository(t.Context(), repo, "test", query, SearchOptions{
			Worktree: true,
			Profile:  ProfileSyntaxOnly,
			TopK:     5,
		})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(response.Results) == 0 {
			t.Fatalf("search %q returned no results", query)
		}
		return response.Results[0]
	}

	baseline := search("update market sizes")
	if baseline.SymbolName != "UpdateMarketSizes" || !containsString(baseline.Signals, "exact-symbol") {
		t.Fatalf("baseline top hit = %s %v", baseline.SymbolName, baseline.Signals)
	}
	for _, query := range []string{
		"update market sizes xyzzy",
		"update market sizes please",
		"update the market sizes",
		"please update the market sizes",
	} {
		got := search(query)
		if got.SymbolName != baseline.SymbolName || got.Score != baseline.Score {
			t.Fatalf("search %q top hit = %s score %.4f, want %s score %.4f",
				query, got.SymbolName, got.Score, baseline.SymbolName, baseline.Score)
		}
		if !containsString(got.Signals, "exact-symbol") {
			t.Fatalf("search %q lost exact-symbol: %v", query, got.Signals)
		}
	}
}

// Case folding is score-neutral: a shouted query must not outscore the identical lowercase
// one. Capitals only carry identifier evidence when something in the query is NOT shouted.
func TestSearchQueryCaseNeutrality(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		left  string
		right string
		equal bool
	}{
		{"shouted sentence folds", "UPDATE MARKET SIZES", "update market sizes", true},
		{"shouted pair folds", "MARKET SIZES", "market sizes", true},
		{"mixed case keeps its evidence", "update MARKET sizes", "update market sizes", false},
		{"single all-caps token keeps its evidence", "ENOENT", "enoent", false},
		{"screaming snake keeps its evidence", "MAX_SIZE MIN_SIZE", "max size min size", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			left := buildSearchQuery(testCase.left)
			right := buildSearchQuery(testCase.right)
			if equal := equalTermWeights(left.weights, right.weights); equal != testCase.equal {
				t.Fatalf("weights(%q)=%v vs weights(%q)=%v: equal=%v want %v",
					testCase.left, left.weights, testCase.right, right.weights, equal, testCase.equal)
			}
		})
	}
}

func TestSearchRepositoryScoresShoutedQueryLikeLowercase(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "market/sizes.go", `package market

// UpdateMarketSizes recomputes the stored market sizes for every tracked region.
func UpdateMarketSizes(regions []string) int {
	return len(regions)
}
`)
	search := func(query string) SearchResult {
		t.Helper()
		response, err := SearchRepository(t.Context(), repo, "test", query, SearchOptions{
			Worktree: true,
			Profile:  ProfileSyntaxOnly,
			TopK:     5,
		})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(response.Results) == 0 {
			t.Fatalf("search %q returned no results", query)
		}
		return response.Results[0]
	}
	lower := search("update market sizes")
	shouted := search("UPDATE MARKET SIZES")
	if shouted.Score != lower.Score || shouted.SymbolName != lower.SymbolName {
		t.Fatalf("shouted query scored %s %.4f, lowercase scored %s %.4f",
			shouted.SymbolName, shouted.Score, lower.SymbolName, lower.Score)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalTermWeights(left, right map[string]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for term, weight := range left {
		if other, ok := right[term]; !ok || other != weight {
			return false
		}
	}
	return true
}
