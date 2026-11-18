package sem

import (
	"fmt"
	"strings"
	"testing"
)

// refitFixtureResult is one ranked hit whose serialized size is dominated by its snippet, so a test
// can reason about bytes in whole results.
func refitFixtureResult(rank int, path string, snippetBytes int) SearchResult {
	return SearchResult{
		Rank: rank, FilePath: path,
		StartLine: 1, EndLine: 1, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 1,
		Signals: []string{},
		Snippet: strings.Repeat("x", snippetBytes),
	}
}

// refitFixtureRanking is eight hits: a five-rank head, then a redundant second fragment of the
// rank-1 file at rank 6, and two last mentions of their files at ranks 7 and 8.
func refitFixtureRanking() []SearchResult {
	paths := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "a.go", "f.go", "g.go"}
	results := make([]SearchResult, len(paths))
	for index, path := range paths {
		results[index] = refitFixtureResult(index+1, path, 400)
	}
	return results
}

func refitPaths(results []SearchResult) string {
	paths := make([]string, len(results))
	for index, result := range results {
		paths[index] = result.FilePath
	}
	return strings.Join(paths, ",")
}

func assertRefitRanks(t *testing.T, results []SearchResult) {
	t.Helper()
	for index, result := range results {
		if result.Rank != index+1 {
			t.Fatalf("rank %d at index %d after the re-fit: %s", result.Rank, index, refitPaths(results))
		}
	}
}

// A payload that already fits is returned untouched: the re-fit is a net, and the default output
// must stay byte-identical whenever every pass held its own share.
func TestRefitSearchPayloadLeavesAFittingPayloadAlone(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()
	card := []TypeCardEntry{{Name: "Ledger", Decl: "type Ledger struct{}"}}
	budget := serializedSearchResultBytes(results) + serializedSearchResultBytes(card)
	for _, ceiling := range []int{0, budget, budget + 1} {
		refit := refitSearchPayloadToBudget(results, card, nil, ceiling, searchEnclosureHeadRanks, nil)
		if refit.shed() != 0 || len(refit.results) != len(results) || len(refit.typeCard) != 1 {
			t.Fatalf("ceiling %d: a fitting payload was changed: shed %d, %s",
				ceiling, refit.shed(), refitPaths(refit.results))
		}
	}
}

// Over the ceiling, the first bytes back come from the redundant fragment the displacement-funded
// blocks may already reclaim, ahead of any block and ahead of the last mention of a file.
func TestRefitSearchPayloadDropsARedundantFragmentFirst(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()
	card := []TypeCardEntry{{Name: "Ledger", Decl: "type Ledger struct{ Lantern string }"}}
	over := serializedSearchResultBytes(results) + serializedSearchResultBytes(card) - 10
	refit := refitSearchPayloadToBudget(results, card, nil, over, searchEnclosureHeadRanks, nil)
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go,f.go,g.go"; got != want {
		t.Fatalf("re-fit kept %s, want %s (the rank-6 second fragment of a.go goes first)", got, want)
	}
	if len(refit.typeCard) != 1 || refit.typeCardEntries != 0 {
		t.Fatalf("the card yielded before the redundant fragment: %+v", refit.typeCard)
	}
	if refit.fundedBytes() > over {
		t.Fatalf("re-fit left %d funded bytes over the %d ceiling", refit.fundedBytes(), over)
	}
	assertRefitRanks(t, refit.results)
	// The caller's slice is not edited in place: its ranks still read 1..8.
	if results[6].Rank != 7 {
		t.Fatalf("the re-fit renumbered the caller's ranking in place")
	}
}

// With no redundant fragment left, the type card yields (last entry first), then the signature-type
// block shrinks its member lists before it loses an entry, and only then does the ranking lose a
// last mention, from the bottom.
func TestRefitSearchPayloadFollowsTheBlockYieldOrder(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()[:5]
	results = append(results, refitFixtureResult(6, "f.go", 400), refitFixtureResult(7, "g.go", 400))
	card := []TypeCardEntry{
		{Name: "First", Decl: strings.Repeat("f", 120)},
		{Name: "Second", Decl: strings.Repeat("s", 120)},
	}
	methods := make([]string, 10)
	for index := range methods {
		methods[index] = fmt.Sprintf("Method%02d()", index)
	}
	signature := []SearchSignatureType{
		{Name: "Ledger", FilePath: "a.go", StartLine: 3, Methods: methods, MethodsTotal: len(methods)},
		{Name: "Vessel", FilePath: "a.go", StartLine: 9, Methods: methods[:2], MethodsTotal: 2},
	}
	full := refitSearchPayloadToBudget(results, card, signature, 0, searchEnclosureHeadRanks, nil).fundedBytes()

	// One byte over: the card's LAST entry is the first thing to go.
	refit := refitSearchPayloadToBudget(results, card, signature, full-1, searchEnclosureHeadRanks, nil)
	if len(refit.typeCard) != 1 || refit.typeCard[0].Name != "First" || len(refit.dropped) != 0 ||
		refit.signatureSteps != 0 {
		t.Fatalf("one byte over: card %+v, dropped %d, signature steps %d",
			refit.typeCard, len(refit.dropped), refit.signatureSteps)
	}

	// Past the whole card: the signature block shrinks 10 -> 8 members before it drops an entry.
	cardBytes := serializedSearchResultBytes(card)
	refit = refitSearchPayloadToBudget(results, card, signature, full-cardBytes-1, searchEnclosureHeadRanks, nil)
	if len(refit.typeCard) != 0 || len(refit.signatureTypes) != 2 || len(refit.signatureTypes[0].Methods) != 8 ||
		refit.signatureTypes[0].MethodsTotal != 10 || len(refit.dropped) != 0 {
		t.Fatalf("past the card: card %d, signature %+v, dropped %d",
			len(refit.typeCard), refit.signatureTypes, len(refit.dropped))
	}

	// Past both blocks: the ranking loses its bottom entry, never the head.
	ranking := serializedSearchResultBytes(results)
	refit = refitSearchPayloadToBudget(results, card, signature, ranking-1, searchEnclosureHeadRanks, nil)
	if len(refit.typeCard) != 0 || len(refit.signatureTypes) != 0 {
		t.Fatalf("a block outlived a dropped ranked hit: card %d, signature %d",
			len(refit.typeCard), len(refit.signatureTypes))
	}
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go,f.go"; got != want {
		t.Fatalf("ranking after the blocks yielded: %s, want %s", got, want)
	}
	assertRefitRanks(t, refit.results)
}

// The covering test yields after the other non-head hits, and the head goes only when nothing else
// is left, from the bottom.
func TestRefitSearchPayloadDropsTheCoveringTestLastAndTheHeadFromTheBottom(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()[:5]
	covering := refitFixtureResult(6, "a_test.go", 400)
	covering.Section = searchSectionCoveringTest
	results = append(results, covering, refitFixtureResult(7, "g.go", 400))
	one := serializedSearchResultBytes(results[:1])

	refit := refitSearchPayloadToBudget(results, nil, nil, serializedSearchResultBytes(results)-1, searchEnclosureHeadRanks, nil)
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go,a_test.go"; got != want {
		t.Fatalf("first drop: %s, want %s", got, want)
	}
	refit = refitSearchPayloadToBudget(results, nil, nil, serializedSearchResultBytes(results[:5])+1, searchEnclosureHeadRanks, nil)
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go"; got != want {
		t.Fatalf("covering test: %s, want %s", got, want)
	}
	refit = refitSearchPayloadToBudget(results, nil, nil, one, searchEnclosureHeadRanks, nil)
	if got, want := refitPaths(refit.results), "a.go"; got != want {
		t.Fatalf("head: %s, want %s", got, want)
	}
	assertRefitRanks(t, refit.results)
}

// The contract anchor is one of the first PRIMARY hits, which docs hits holding the head can push
// below the protected positions. The covering test was chosen for that hit, so the hit leaves only
// with the head, after the test: a covering test never outlives its fix site.
func TestRefitSearchPayloadKeepsTheCoveredAnchorUntilItsTest(t *testing.T) {
	t.Parallel()
	var results []SearchResult
	for rank := 1; rank <= 5; rank++ {
		result := refitFixtureResult(rank, fmt.Sprintf("notes/%d.md", rank), 400)
		result.Section = searchSectionDocs
		results = append(results, result)
	}
	anchor := refitFixtureResult(6, "pkg/anchor.go", 400)
	covering := refitFixtureResult(8, "pkg/anchor_test.go", 400)
	covering.Section = searchSectionCoveringTest
	results = append(results, anchor, refitFixtureResult(7, "pkg/tail.go", 400), covering)
	protected := map[searchRefitKey]bool{searchRefitKeyOf(anchor): true}
	head := serializedSearchResultBytes(results[:5])

	for _, tc := range []struct {
		budget int
		want   string
	}{
		{serializedSearchResultBytes(results) - 1, "pkg/anchor.go,pkg/anchor_test.go"},
		{head + 2*serializedSearchResultBytes(results[5:6]), "pkg/anchor.go"},
		{head + 1, ""},
	} {
		refit := refitSearchPayloadToBudget(results, nil, nil, tc.budget, searchEnclosureHeadRanks, protected)
		if got := refitPaths(refit.results[5:]); got != tc.want {
			t.Fatalf("budget %d: below the head %q, want %q", tc.budget, got, tc.want)
		}
		assertRefitRanks(t, refit.results)
	}
}

// The redundant-fragment stage spares the covered anchor too: a sourceless second mention of a file
// can still be the hit the covering test was chosen for.
func TestRefitSearchPayloadSparesACoveredRedundantFragment(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()
	protected := map[searchRefitKey]bool{searchRefitKeyOf(results[5]): true}
	refit := refitSearchPayloadToBudget(
		results, nil, nil, serializedSearchResultBytes(results)-1, searchEnclosureHeadRanks, protected,
	)
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go,a.go,f.go"; got != want {
		t.Fatalf("first drop with the fragment covered: %s, want %s", got, want)
	}
}

// A covered fragment still names its file: when the other fragment of that file sits above it,
// that one is the redundant fragment to drop, not the type card or another file's only hit.
func TestRefitSearchPayloadCountsACoveredFragmentAsAMention(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()[:5]
	results = append(results,
		refitFixtureResult(6, "f.go", 400), refitFixtureResult(7, "f.go", 400), refitFixtureResult(8, "g.go", 400))
	results[6].StartLine, results[6].EndLine = 20, 24
	protected := map[searchRefitKey]bool{searchRefitKeyOf(results[6]): true}
	card := []TypeCardEntry{{Name: "Ledger", Decl: "type Ledger struct{}"}}
	full := refitSearchPayloadToBudget(results, card, nil, 0, searchEnclosureHeadRanks, protected).fundedBytes()

	refit := refitSearchPayloadToBudget(results, card, nil, full-1, searchEnclosureHeadRanks, protected)
	if got, want := refitPaths(refit.results), "a.go,b.go,c.go,d.go,e.go,f.go,g.go"; got != want {
		t.Fatalf("first drop with the lower f.go fragment covered: %s, want %s", got, want)
	}
	if len(refit.typeCard) != 1 || !refit.protected[searchRefitKeyOf(refit.results[5])] {
		t.Fatalf("card %+v, covered fragment kept %v", refit.typeCard, refit.protected[searchRefitKeyOf(refit.results[5])])
	}
	assertRefitRanks(t, refit.results)
}

// The anchor keys come from the contract anchor resolved the way the card resolved it, and only
// while a covering test is there to be kept for.
func TestSearchCoveredAnchorKeysNeedsTheCoveringTest(t *testing.T) {
	t.Parallel()
	anchor := SymbolRecord{ID: "settle", Name: "Settle", FilePath: "pkg/ledger.go", StartLine: 1, EndLine: 1}
	symbolsByID := map[string]SymbolRecord{anchor.ID: anchor}
	hit := refitFixtureResult(1, anchor.FilePath, 10)
	hit.SymbolID = anchor.ID
	covering := refitFixtureResult(2, "pkg/ledger_test.go", 10)
	covering.Section = searchSectionCoveringTest

	keys := searchCoveredAnchorKeys([]SearchResult{hit, covering}, anchor, symbolsByID, nil)
	if len(keys) != 1 || !keys[searchRefitKeyOf(hit)] {
		t.Fatalf("with the covering test: keys %v, want the anchor's hit", keys)
	}
	if keys := searchCoveredAnchorKeys([]SearchResult{hit}, anchor, symbolsByID, nil); keys != nil {
		t.Fatalf("without a covering test: keys %v, want none", keys)
	}
	if keys := searchCoveredAnchorKeys([]SearchResult{hit, covering}, SymbolRecord{}, symbolsByID, nil); keys != nil {
		t.Fatalf("without an anchor: keys %v, want none", keys)
	}
}

// A budget below the empty ranking stops at the empty ranking instead of looping: `[]` is the
// floor every fitter returns, and nothing is smaller.
func TestRefitSearchPayloadStopsAtTheEmptyRanking(t *testing.T) {
	t.Parallel()
	refit := refitSearchPayloadToBudget(refitFixtureRanking(), nil, nil, 1, searchEnclosureHeadRanks, nil)
	if len(refit.results) != 0 || len(refit.dropped) != 8 {
		t.Fatalf("budget 1: kept %d, dropped %d", len(refit.results), len(refit.dropped))
	}
	if got := refit.fundedBytes(); got != searchEmptyRankingBytes {
		t.Fatalf("empty ranking costs %d, want %d", got, searchEmptyRankingBytes)
	}
}

// Each counter a whole-result removal can falsify follows the payload that goes out.
func TestApplySearchBudgetRefitStatsCountsWhatWasRemoved(t *testing.T) {
	t.Parallel()
	complete := refitFixtureResult(1, "a.go", 10)
	complete.Signals = []string{searchCompleteSymbolSignal}
	// A body the callee hop printed carries complete-symbol, but the counter never included it, so
	// dropping it must leave the allocator's count alone.
	hop := refitFixtureResult(2, "b.go", 10)
	hop.Signals = []string{searchCalleeHopSignal, searchFullUnitSignal, searchCompleteSymbolSignal}
	related := refitFixtureResult(3, "c.go", 10)
	related.Section = searchSectionRelated
	covering := refitFixtureResult(4, "c_test.go", 10)
	covering.Section = searchSectionCoveringTest
	refit := searchBudgetRefit{
		dropped:        []SearchResult{complete, hop, related, covering},
		typeCard:       []TypeCardEntry{{Name: "Ledger", Decl: "type Ledger struct{}"}},
		signatureTypes: []SearchSignatureType{{Name: "Vessel", FilePath: "a.go", StartLine: 1}},
	}
	stats := SearchStats{
		ResultsDropped: 2, CompleteSymbols: 3, CalleeHopSites: 1, RelatedSites: 2, CoveringTests: 1,
		TypeCardEntries: 4, SignatureTypes: 3, SignatureTypeBytes: 999,
	}
	applySearchBudgetRefitStats(&stats, refit)
	want := SearchStats{
		ResultsDropped: 6, CompleteSymbols: 2, CalleeHopSites: 0, RelatedSites: 1, CoveringTests: 0,
		TypeCardEntries: 1, SignatureTypes: 1,
		SignatureTypeBytes: serializedSearchResultBytes(refit.signatureTypes),
	}
	if stats != want {
		t.Fatalf("stats after the re-fit:\n got %+v\nwant %+v", stats, want)
	}
}

// The section pass keeps an all-docs payload primary, because those hits are the only answer there
// is. A re-fit that removed the last primary hit has to take that decision again, or the answer that
// is left goes out relabelled as "not fix sites"; while a primary hit is left, the labels stand.
func TestSettleSearchBudgetRefitRedecidesTheDocsSection(t *testing.T) {
	t.Parallel()
	docs := func(rank int, path string) SearchResult {
		result := refitFixtureResult(rank, path, 10)
		result.Section = searchSectionDocs
		return result
	}
	covering := func(rank int) SearchResult {
		result := refitFixtureResult(rank, "notes_test.go", 10)
		result.Section = searchSectionCoveringTest
		return result
	}
	for _, tc := range []struct {
		name    string
		results []SearchResult
		want    []string
	}{
		{
			name:    "only docs left",
			results: []SearchResult{docs(1, "notes/a.md"), docs(2, "notes/b.md")},
			want:    []string{searchSectionPrimary, searchSectionPrimary},
		},
		{
			name:    "a primary hit left",
			results: []SearchResult{refitFixtureResult(1, "pkg/a.go", 10), docs(2, "notes/b.md")},
			want:    []string{searchSectionPrimary, searchSectionDocs},
		},
		{
			// The section pass promoted an all-docs ranking, and a covering test was seated after it.
			// Re-running that pass would now count three results and label the two notes, adding
			// bytes after the re-fit; the fallback only ever promotes.
			name: "a promoted all-docs ranking with a covering test",
			results: []SearchResult{
				refitFixtureResult(1, "notes/a.md", 10), refitFixtureResult(2, "notes/b.md", 10), covering(3),
			},
			want: []string{searchSectionPrimary, searchSectionPrimary, searchSectionCoveringTest},
		},
	} {
		refit := searchBudgetRefit{
			results: tc.results, dropped: []SearchResult{refitFixtureResult(3, "pkg/c.go", 10)},
		}
		var stats SearchStats
		results, _, _ := settleSearchBudgetRefit(refit, &stats)
		if len(results) != len(tc.want) {
			t.Fatalf("%s: %d results, want %d", tc.name, len(results), len(tc.want))
		}
		for index, result := range results {
			if result.Section != tc.want[index] {
				t.Fatalf("%s: result %d is in section %q, want %q", tc.name, index, result.Section, tc.want[index])
			}
		}
		if stats.ResultsDropped != 1 {
			t.Fatalf("%s: results_dropped_by_budget = %d, want 1", tc.name, stats.ResultsDropped)
		}
	}
}

// The fallback for the passes after the anchored blocks fires on the funded payload, at the same
// ceiling Validate holds, and never on an unbounded search or on the empty ranking's two bytes.
func TestSearchPayloadOverBudgetHoldsTheFundedCeiling(t *testing.T) {
	t.Parallel()
	results := refitFixtureRanking()
	card := []TypeCardEntry{{Name: "Ledger", Decl: "type Ledger struct{}"}}
	funded := searchFundedPayloadBytes(results, card, nil)
	if funded != serializedSearchResultBytes(results)+serializedSearchResultBytes(card) {
		t.Fatalf("funded bytes %d do not count the ranking and the card", funded)
	}
	for _, tc := range []struct {
		name    string
		results []SearchResult
		budget  int
		want    bool
	}{
		{name: "fits exactly", results: results, budget: funded, want: false},
		{name: "one byte over", results: results, budget: funded - 1, want: true},
		{name: "unbounded", results: results, budget: 0, want: false},
		{name: "empty ranking under a one-byte budget", results: []SearchResult{}, budget: 1, want: false},
	} {
		typeCard := card
		if len(tc.results) == 0 {
			typeCard = nil
		}
		if got := searchPayloadOverBudget(tc.results, typeCard, nil, tc.budget); got != tc.want {
			t.Fatalf("%s: over budget = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// `--max-context-bytes 1` used to fail the command: the fitter returns the empty ranking when
// nothing fits, and `[]` is two bytes, so Validate rejected "2 > 1". The empty ranking is the floor,
// and the response must validate exactly as it does at every larger budget that seats nothing.
func TestSearchRepositoryABudgetBelowTheEmptyRankingValidates(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "pkg/ledger.go", "package pkg\n\n"+
		"// AmberLedger returns the amber ledger vessel.\n"+
		"func AmberLedger() int {\n\treturn 1\n}\n")
	for _, ceiling := range []int{1, 2} {
		response, err := SearchRepository(t.Context(), repo, "test", "amber ledger vessel", SearchOptions{
			Worktree: true, Profile: ProfileFast, MaxContextBytes: ceiling,
		})
		if err != nil {
			t.Fatalf("budget %d: %v", ceiling, err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("budget %d: %v", ceiling, err)
		}
		if len(response.Results) != 0 || response.Stats.ResultsDropped == 0 {
			t.Fatalf("budget %d: %d results, %d dropped", ceiling, len(response.Results), response.Stats.ResultsDropped)
		}
	}
}

// The regression net the re-fit must not replace. Every pass after the fitter is responsible for
// its own share of the ceiling, and the re-fit only exists so a miss degrades instead of failing. A
// payload that NEEDED it is still a bug in some pass, so this sweeps the ceilings and the levers that
// switch on each post-allocator pass, on corpora that give each of them something to add, and fails
// if Validate rejects a response or if the re-fit had to remove anything. The fired counters make the
// sweep non-vacuous: a corpus on which a pass never added anything proves nothing about that pass.
func TestSearchRepositoryPostAllocatorPassesHoldTheCeiling(t *testing.T) {
	t.Parallel()
	levers := map[string]SearchOptions{
		"default":        {},
		"callee-hop":     {CalleeHop: true, FullUnitTop: 2, EditSiteBodies: true},
		"reference":      {IncludeTypeCard: true, IncludeSignatureTypes: true, IncludeContainerMap: true},
		"documents":      {DocumentResolution: true, TopK: 20},
		"deep-window":    {Deep: true, HeadWindowLines: 40},
		"single-outline": {SingleResolution: true, IncludeFileOutline: true},
		"everything-on": {
			CalleeHop: true, FullUnitTop: 2, IncludeTypeCard: true, IncludeSignatureTypes: true,
			DocumentResolution: true,
		},
	}
	for _, corpus := range []refitSweepCorpus{
		{
			name: "code", repo: refitCodeCorpus(t), query: "settle the amber ledger vessel count",
			fired: map[string]func(SearchResponse) bool{
				"callee hop":      func(r SearchResponse) bool { return r.Stats.CalleeHopSites > 0 },
				"related sites":   func(r SearchResponse) bool { return r.Stats.RelatedSites > 0 },
				"covering test":   func(r SearchResponse) bool { return r.Stats.CoveringTests > 0 },
				"type card":       func(r SearchResponse) bool { return r.Stats.TypeCardEntries > 0 },
				"signature types": func(r SearchResponse) bool { return r.Stats.SignatureTypes > 0 },
				"span merge":      func(r SearchResponse) bool { return r.Stats.MergedSpans > 0 },
				"complete bodies": func(r SearchResponse) bool { return r.Stats.CompleteSymbols > 0 },
			},
		},
		{
			name: "labelled notes", repo: refitLabelledNotesCorpus(t), query: "amber lantern orchard ledger vessel",
			fired: map[string]func(SearchResponse) bool{
				"docs label": searchResponseHasDocsSection,
			},
		},
		{
			name: "prose", repo: refitProseCorpus(t), query: "amber lantern purchase cost",
			fired: map[string]func(SearchResponse) bool{
				"prose passages": func(r SearchResponse) bool { return r.Stats.ProsePassages > 0 },
			},
		},
	} {
		sweepRefitCorpus(t, corpus, levers)
	}
}

// refitSweepCorpus is one repository the ceiling sweep runs on, with the passes it must see fire.
type refitSweepCorpus struct {
	name  string
	repo  string
	query string
	fired map[string]func(SearchResponse) bool
}

// sweepRefitCorpus runs every lever at every swept ceiling on one corpus, then fails if a pass the
// corpus exists for never added anything.
func sweepRefitCorpus(t *testing.T, corpus refitSweepCorpus, levers map[string]SearchOptions) {
	t.Helper()
	seen := map[string]bool{}
	for name, lever := range levers {
		for ceiling := 160; ceiling <= 9000; ceiling += 487 {
			options := lever
			options.Worktree, options.Profile, options.MaxContextBytes = true, ProfileFast, ceiling
			label := fmt.Sprintf("%s/%s at %d", corpus.name, name, ceiling)
			response, err := SearchRepository(t.Context(), corpus.repo, "test", corpus.query, options)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			assertSearchRefitIdle(t, label, response)
			for pass, fired := range corpus.fired {
				seen[pass] = seen[pass] || fired(response)
			}
		}
	}
	for pass := range corpus.fired {
		if !seen[pass] {
			t.Fatalf("%s corpus: %s never added anything, so the sweep proves nothing about it",
				corpus.name, pass)
		}
	}
}

// assertSearchRefitIdle fails when the budget re-fit had to remove anything. The re-fit keeps a
// caller from seeing an overshoot, so a response it rescued would otherwise pass every budget check
// while some post-allocator pass still spends bytes it was never given.
func assertSearchRefitIdle(t *testing.T, label string, response SearchResponse) {
	t.Helper()
	if response.budgetRefitShed != 0 {
		t.Fatalf("%s: a post-allocator pass overshot its share of the ceiling and the budget re-fit had "+
			"to remove %d item(s); fix that pass, the re-fit only keeps callers from seeing the failure",
			label, response.budgetRefitShed)
	}
}

// searchResponseHasDocsSection reports whether any result went out labelled docs-and-fixtures.
func searchResponseHasDocsSection(response SearchResponse) bool {
	for _, result := range response.Results {
		if result.Section == SearchSectionDocs {
			return true
		}
	}
	return false
}

// refitCodeCorpus is a Go package whose top hit calls three helpers, is called from four binaries,
// takes a struct, and has a covering test, beside markdown notes that match the same query.
func refitCodeCorpus(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.com/ledger\n\ngo 1.22\n")
	write(t, repo, "ledger/ledger.go", "package ledger\n\n"+
		"// AmberLedger holds the orchard ledger vessel state.\n"+
		"type AmberLedger struct {\n\tLantern string\n\tVessel  int\n\tOrchard []string\n}\n\n"+
		"// Settle settles the amber ledger vessel for the orchard.\n"+
		"func Settle(l AmberLedger, rows []string) AmberLedger {\n"+
		"\tl = normalizeLantern(l)\n\tl.Vessel = countVessel(rows)\n\tl.Orchard = orchardRows(rows)\n\treturn l\n}\n\n"+
		"func normalizeLantern(l AmberLedger) AmberLedger {\n\tl.Lantern = \"amber \" + l.Lantern\n\treturn l\n}\n\n"+
		"func countVessel(rows []string) int {\n\tcount := 0\n\tfor _, row := range rows {\n"+
		"\t\tif row != \"\" {\n\t\t\tcount++\n\t\t}\n\t}\n\treturn count\n}\n\n"+
		"func orchardRows(rows []string) []string {\n\tout := make([]string, 0, len(rows))\n"+
		"\tfor _, row := range rows {\n\t\tout = append(out, \"orchard \"+row)\n\t}\n\treturn out\n}\n")
	write(t, repo, "ledger/ledger_test.go", "package ledger\n\nimport \"testing\"\n\n"+
		"func TestSettleCountsTheAmberLedgerVessel(t *testing.T) {\n"+
		"\tif got := Settle(AmberLedger{}, []string{\"a\", \"\"}).Vessel; got != 1 {\n"+
		"\t\tt.Fatalf(\"vessel = %d\", got)\n\t}\n}\n")
	for caller := 0; caller < 4; caller++ {
		write(t, repo, fmt.Sprintf("cmd/settle%d/main.go", caller), fmt.Sprintf("package main\n\n"+
			"import \"example.com/ledger/ledger\"\n\n"+
			"// main settles amber ledger vessel batch %d.\n"+
			"func main() {\n\t_ = ledger.Settle(ledger.AmberLedger{Lantern: \"batch %d\"}, nil)\n}\n", caller, caller))
	}
	for document := 0; document < 4; document++ {
		var body strings.Builder
		fmt.Fprintf(&body, "# Amber ledger notes %d\n\n", document)
		for section := 0; section < 4; section++ {
			fmt.Fprintf(&body, "## settle the vessel %d\n\n", section)
			fmt.Fprintf(&body, "how Settle counts the amber ledger vessel in note %d turn %d.\n\n", document, section)
			for filler := 0; filler < 12; filler++ {
				fmt.Fprintf(&body, "filler %d-%d unrelated.\n", section, filler)
			}
			body.WriteString("\n")
		}
		write(t, repo, fmt.Sprintf("docs/note-%02d.md", document), body.String())
	}
	return repo
}

// refitProseCorpus is markdown sessions only, with the answer far from the heading that matches, so
// the prose lane promotes passages out of the parent documents.
func refitProseCorpus(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for session := 0; session < 3; session++ {
		write(t, repo, fmt.Sprintf("sessions/focus-%d.md", session), fmt.Sprintf("# Amber travel archive %d\n\n"+
			"The amber trip %d began in Lisbon.\n\n", session, session)+
			strings.Repeat("Neutral travel note.\n", 100)+
			fmt.Sprintf("\nThe lantern purchase in trip %d cost exactly %d dollars.\n", session, 185+session))
	}
	for peer := 0; peer < 4; peer++ {
		write(t, repo, fmt.Sprintf("sessions/peer-%d.md", peer), fmt.Sprintf(
			"\n# Amber lantern archive %d\n\nThis unrelated note records marker %d.\n", peer, peer))
	}
	return repo
}

// refitLabelledNotesCorpus is the corpus issue 208 was measured on: prose notes that every payload
// labels docs-and-fixtures, beside the one Go function the query names, so the section label is
// bytes on nearly every result.
func refitLabelledNotesCorpus(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for document := 0; document < 6; document++ {
		var body strings.Builder
		fmt.Fprintf(&body, "# Ledger notes %d\n\n", document)
		for section := 0; section < 6; section++ {
			fmt.Fprintf(&body, "## amber section %d\n\n", section)
			fmt.Fprintf(&body, "the amber lantern orchard ledger vessel in note %d turn %d.\n\n", document, section)
			for filler := 0; filler < 24; filler++ {
				fmt.Fprintf(&body, "filler %d-%d unrelated.\n", section, filler)
			}
			body.WriteString("\n")
		}
		write(t, repo, fmt.Sprintf("notes/note-%02d.md", document), body.String())
	}
	write(t, repo, "pkg/ledger.go", "package pkg\n\n"+
		"// AmberLanternOrchardLedgerVessel returns the amber lantern orchard ledger vessel.\n"+
		"func AmberLanternOrchardLedgerVessel(vessel int) int {\n\treturn vessel + 1\n}\n")
	return repo
}
