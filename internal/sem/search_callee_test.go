package sem

import (
	"strconv"
	"strings"
	"testing"
)

// THE CALLEE HOP
// ==============
//
// These pin the route that closes the miss --full-unit-top could not: the ranked hit is a thin entry
// point and the gold is the helper it CALLS, one outgoing edge away. The fixture is the measured shape
// verbatim — redis__redis-10095's `lpopCommand` delegating to `popGenericCommand`, in a file with far
// more than searchRelatedUnitMemberLimit top-level functions so the related block's co-member route
// (its stated substitute for outgoing CALLS) is switched off exactly as it is in the real repo.

// calleeHopFixture builds a file of `members` top-level functions where member 1 is a 3-line wrapper
// that calls member 2, plus the ranked payload that shows only the wrapper.
func calleeHopFixture(members int) (
	[]SearchResult, map[string]SymbolRecord, map[string][]SymbolRecord, []RelationRecord, contentReader,
) {
	var source strings.Builder
	symbols := make([]SymbolRecord, 0, members)
	line := 1
	for member := 1; member <= members; member++ {
		name := "helper" + strconv.Itoa(member)
		length := 8
		switch member {
		case 1:
			name = "lpopCommand"
			length = 3
		case 2:
			name = "popGenericCommand"
			length = 20
		}
		start := line
		source.WriteString("void " + name + "(client *c) {\n")
		line++
		for filler := 1; filler < length-1; filler++ {
			if member == 1 {
				source.WriteString("    popGenericCommand(c, LIST_HEAD);\n")
			} else if member == 2 && filler == 6 {
				source.WriteString("    reply = shared.null[c->resp]; /* the line an edit replaces */\n")
			} else {
				source.WriteString("    body_of_" + name + "();\n")
			}
			line++
		}
		source.WriteString("}\n\n")
		line += 2
		symbols = append(symbols, SymbolRecord{
			RecordType: "symbol", ID: "sym:" + name, Kind: "function", Name: name,
			QualifiedName: name, FilePath: "src/t_list.c", StartLine: start, EndLine: start + length - 1,
		})
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	byFile := map[string][]SymbolRecord{"src/t_list.c": symbols}
	relations := []RelationRecord{{
		RecordType: "relation", FromID: "sym:lpopCommand", ToID: "sym:popGenericCommand",
		Type: "CALLS", Confidence: 0.9,
		Evidence: []Evidence{{Kind: "call", FilePath: "src/t_list.c", StartLine: 2}},
	}}
	wrapper := symbols[0]
	results := []SearchResult{{
		Rank: 1, Score: 27.0447, FilePath: "src/t_list.c",
		StartLine: wrapper.StartLine, EndLine: wrapper.EndLine, FocusLine: wrapper.StartLine,
		SnippetStartLine: wrapper.StartLine, SnippetEndLine: wrapper.EndLine,
		SymbolID: wrapper.ID, SymbolName: wrapper.Name, Kind: "function", Signals: []string{"body"},
	}}
	content := source.String()
	read := func(path string) (string, bool) {
		if path != "src/t_list.c" {
			return "", false
		}
		return content, true
	}
	return results, byID, byFile, relations, read
}

func calleeHopSites(t *testing.T, members int, query string) ([]searchCalleeHopSite, *searchRelatedFileCache) {
	t.Helper()
	results, byID, byFile, relations, read := calleeHopFixture(members)
	cache := newSearchRelatedFileCache(read, searchRelatedCandidateLimit)
	sites := selectSearchCalleeHopSites(
		results, buildSearchQuery(query), relations, byID, byFile, cache,
		searchCalleeHopRanks(results), searchCalleeHopSiteLimit,
	)
	return sites, cache
}

// TestSelectSearchCalleeHopSitesReachesTheHelperTheRelatedBlockCannot is the measured case. The unit
// holds 40 top-level functions, so searchRelatedUnitCoMembers returns NOTHING (its limit is 12) and no
// route in the related block can produce this site — which is precisely the hole the exclusion comment
// at search_related.go:41-43 assumes is covered.
func TestSelectSearchCalleeHopSitesReachesTheHelperTheRelatedBlockCannot(t *testing.T) {
	t.Parallel()
	members := searchRelatedUnitMemberLimit * 3
	sites, _ := calleeHopSites(t, members, "LPOP key count returns null bulk reply instead of null array reply")
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want exactly the one callee", len(sites))
	}
	if sites[0].symbol.Name != "popGenericCommand" {
		t.Fatalf("site = %s, want popGenericCommand", sites[0].symbol.Name)
	}
	if !sites[0].sameFile {
		t.Fatal("a callee in the anchor's own file was not marked sameFile")
	}
	if sites[0].callLine != 2 {
		t.Fatalf("callLine = %d, want 2 (the relation's own evidence)", sites[0].callLine)
	}
	if sites[0].anchorIndex != 0 {
		t.Fatalf("anchorIndex = %d, want 0", sites[0].anchorIndex)
	}

	// The premise: the related block really cannot reach it at this unit size.
	_, byID, byFile, _, _ := calleeHopFixture(members)
	if members := searchRelatedUnitCoMembers(byID["sym:lpopCommand"], byFile); len(members) != 0 {
		t.Fatalf("co-member route returned %d members — the hole this route exists for is gone, "+
			"so the exclusion in search_related.go may now be sound", len(members))
	}
}

// TestSelectSearchCalleeHopSitesRefusesWhatItCannotJustify pins every admission gate.
func TestSelectSearchCalleeHopSitesRefusesWhatItCannotJustify(t *testing.T) {
	t.Parallel()
	base := func() ([]SearchResult, map[string]SymbolRecord, map[string][]SymbolRecord, []RelationRecord, contentReader) {
		return calleeHopFixture(searchRelatedUnitMemberLimit * 3)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*[]SearchResult, map[string]SymbolRecord, map[string][]SymbolRecord, *[]RelationRecord)
		query   string
		wantAny bool
	}{
		{
			name:    "the measured case is admitted",
			query:   "lpop null reply",
			wantAny: true,
		},
		{
			// An unresolved call has no body to print and no file to edit.
			name: "an unresolved target is refused",
			mutate: func(_ *[]SearchResult, byID map[string]SymbolRecord, _ map[string][]SymbolRecord, _ *[]RelationRecord) {
				delete(byID, "sym:popGenericCommand")
			},
			query: "lpop null reply",
		},
		{
			// Only "X's code runs Y" can force a change in Y's own body.
			name: "a non-call relation is refused",
			mutate: func(_ *[]SearchResult, _ map[string]SymbolRecord, _ map[string][]SymbolRecord, relations *[]RelationRecord) {
				(*relations)[0].Type = "REFERENCES"
			},
			query: "lpop null reply",
		},
		{
			// Returning a whole container spends the slot on the members that are not the fix.
			name: "a container target is refused",
			mutate: func(_ *[]SearchResult, byID map[string]SymbolRecord, _ map[string][]SymbolRecord, _ *[]RelationRecord) {
				symbol := byID["sym:popGenericCommand"]
				symbol.Kind = "class"
				byID[symbol.ID] = symbol
			},
			query: "lpop null reply",
		},
		{
			// A slot spent re-listing something already on screen is a slot spent on nothing.
			name: "a callee the payload already prints is refused",
			mutate: func(results *[]SearchResult, byID map[string]SymbolRecord, _ map[string][]SymbolRecord, _ *[]RelationRecord) {
				callee := byID["sym:popGenericCommand"]
				(*results)[0].SnippetStartLine = callee.StartLine
				(*results)[0].SnippetEndLine = callee.EndLine
			},
			query: "lpop null reply",
		},
		{
			// searchRelatedSiteEditable — the RANKER's own priors, reused. A patch does not land in a
			// vendored file.
			name: "a callee in a file the ranker rules out is refused",
			mutate: func(_ *[]SearchResult, byID map[string]SymbolRecord, byFile map[string][]SymbolRecord, _ *[]RelationRecord) {
				callee := byID["sym:popGenericCommand"]
				callee.FilePath = "vendor/thirdparty/pop.c"
				byID[callee.ID] = callee
				byFile[callee.FilePath] = []SymbolRecord{callee}
			},
			query: "lpop null reply",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			results, byID, byFile, relations, read := base()
			if testCase.mutate != nil {
				testCase.mutate(&results, byID, byFile, &relations)
			}
			sites := selectSearchCalleeHopSites(
				results, buildSearchQuery(testCase.query), relations, byID, byFile,
				newSearchRelatedFileCache(read, searchRelatedCandidateLimit),
				searchCalleeHopRanks(results), searchCalleeHopSiteLimit,
			)
			if (len(sites) > 0) != testCase.wantAny {
				t.Fatalf("sites = %d, want any = %v", len(sites), testCase.wantAny)
			}
		})
	}
}

// TestSearchCalleeHopAdmissibleIsAsymmetricAcrossFiles pins the cross-file name test. Without it the
// slots go to the language's plumbing: the first cross-file hop this route produced on
// redis__redis-10095 was `zcalloc`, an allocator with no word in common with an issue about LPOP.
func TestSearchCalleeHopAdmissibleIsAsymmetricAcrossFiles(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		site searchCalleeHopSite
		want bool
	}{
		{name: "same file needs no name evidence", site: searchCalleeHopSite{sameFile: true}, want: true},
		{name: "cross file with the query in its name", site: searchCalleeHopSite{nameHits: 1}, want: true},
		{name: "cross file plumbing is refused", site: searchCalleeHopSite{overlap: 0.4}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := searchCalleeHopAdmissible(testCase.site); got != testCase.want {
				t.Fatalf("admissible = %v, want %v", got, testCase.want)
			}
		})
	}
	// And the scorer has to separate a concept-named helper from plumbing on real names.
	q := buildSearchQuery("LPOP count returns null bulk reply instead of null array reply")
	concept := SymbolRecord{Name: "addReplyNullArray", QualifiedName: "addReplyNullArray", FilePath: "src/networking.c"}
	alloc := SymbolRecord{Name: "zcalloc", QualifiedName: "zcalloc", FilePath: "src/zmalloc.c"}
	conceptHits, conceptScore := searchCalleeQueryOverlap(q, concept, nil)
	allocHits, allocScore := searchCalleeQueryOverlap(q, alloc, nil)
	if conceptHits == 0 {
		t.Fatalf("addReplyNullArray scored 0 name hits against %q", q.rawLower)
	}
	if allocHits != 0 {
		t.Fatalf("zcalloc scored %d name hits, want 0", allocHits)
	}
	if conceptScore <= allocScore {
		t.Fatalf("overlap: concept=%f alloc=%f, want the concept ahead", conceptScore, allocScore)
	}
	// AND the measured case shows why the same-file rule must be UNCONDITIONAL rather than a
	// same-file bonus on top of the name test: `popGenericCommand` shares no word with an issue
	// phrased "LPOP ... null array reply" (`lpop` is not a substring of `popgenericcommand`), so a
	// name-gated route would have refused the one site that carries the gold.
	pop := SymbolRecord{Name: "popGenericCommand", QualifiedName: "popGenericCommand", FilePath: "src/t_list.c"}
	if hits, _ := searchCalleeQueryOverlap(q, pop, nil); hits != 0 {
		t.Fatalf("popGenericCommand now scores %d name hits; the same-file rationale needs rewriting", hits)
	}
	if !searchCalleeHopAdmissible(searchCalleeHopSite{symbol: pop, sameFile: true}) {
		t.Fatal("the measured gold-carrying site is not admissible")
	}
}

// TestSearchCalleeHopRanksFollowsTheSameGapGate pins that the hop reaches rank 2 exactly when
// --full-unit-top would: a payload showing two units because the scores tied should hop from both, one
// showing a clear winner should hop only from the winner.
func TestSearchCalleeHopRanksFollowsTheSameGapGate(t *testing.T) {
	t.Parallel()
	tied := []SearchResult{{Rank: 1, Score: 100}, {Rank: 2, Score: 95}, {Rank: 3, Score: 90}}
	if got := searchCalleeHopRanks(tied); got != 2 {
		t.Fatalf("tied head ranks = %d, want 2 (never deeper than rank 2)", got)
	}
	clear := []SearchResult{{Rank: 1, Score: 100}, {Rank: 2, Score: 70}}
	if got := searchCalleeHopRanks(clear); got != 1 {
		t.Fatalf("separated head ranks = %d, want 1", got)
	}
	if got := searchCalleeHopRanks(nil); got != 0 {
		t.Fatalf("empty ranking = %d, want 0", got)
	}
}

// TestSearchCalleeHopAnchorsDoNotSlideDownTheRanking pins the difference from searchRelatedAnchors:
// that function walks the whole primary list to fill its quota, which would hop from a rank the gap
// gate had already excluded.
func TestSearchCalleeHopAnchorsDoNotSlideDownTheRanking(t *testing.T) {
	t.Parallel()
	_, byID, byFile, _, _ := calleeHopFixture(4)
	wrapper, helper := byID["sym:lpopCommand"], byID["sym:helper3"]
	results := []SearchResult{
		// Rank 1 carries no resolvable callable at all.
		{Rank: 1, FilePath: "README.md", StartLine: 1, EndLine: 2, FocusLine: 1},
		{Rank: 2, FilePath: wrapper.FilePath, StartLine: wrapper.StartLine, EndLine: wrapper.EndLine,
			FocusLine: wrapper.StartLine, SymbolID: wrapper.ID},
		{Rank: 3, FilePath: helper.FilePath, StartLine: helper.StartLine, EndLine: helper.EndLine,
			FocusLine: helper.StartLine, SymbolID: helper.ID},
	}
	anchors := searchCalleeHopAnchors(results, byID, byFile, 2)
	if len(anchors) != 1 || anchors[0].symbol.ID != wrapper.ID || anchors[0].resultIndex != 1 {
		t.Fatalf("anchors = %+v, want only rank 2's callable — the walk must not reach rank 3", anchors)
	}
}

// TestSearchCalleeHopResultFollowsTheBodyFlags pins that this route invents no third body policy.
func TestSearchCalleeHopResultFollowsTheBodyFlags(t *testing.T) {
	t.Parallel()
	sites, cache := calleeHopSites(t, searchRelatedUnitMemberLimit*3,
		"LPOP count returns null bulk reply instead of null array reply")
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1", len(sites))
	}
	lines, ok := cache.get("src/t_list.c")
	if !ok {
		t.Fatal("fixture file unreadable")
	}

	locator, ok := searchCalleeHopResult(sites[0], lines, false)
	if !ok {
		t.Fatal("no locator entry")
	}
	if locator.SnippetStartLine != locator.SnippetEndLine {
		t.Fatalf("locator span = %d-%d, want one line", locator.SnippetStartLine, locator.SnippetEndLine)
	}
	if !hasSearchSignal(locator, searchCalleeHopSignal) {
		t.Fatalf("signals = %v, want %s", locator.Signals, searchCalleeHopSignal)
	}
	for _, forbidden := range []string{searchCompleteSymbolSignal, searchFullUnitSignal} {
		if hasSearchSignal(locator, forbidden) {
			t.Fatalf("a locator claimed %s: %v", forbidden, locator.Signals)
		}
	}

	body, ok := searchCalleeHopResult(sites[0], lines, true)
	if !ok {
		t.Fatal("no body entry")
	}
	callee := sites[0].symbol
	if body.SnippetStartLine != callee.StartLine || body.SnippetEndLine != callee.EndLine {
		t.Fatalf("body span = %d-%d, want the whole unit %d-%d",
			body.SnippetStartLine, body.SnippetEndLine, callee.StartLine, callee.EndLine)
	}
	// The whole point: the line an edit replaces is now in the payload verbatim.
	if !strings.Contains(body.Snippet, "the line an edit replaces") {
		t.Fatalf("body does not carry the gold line:\n%s", body.Snippet)
	}
	for _, want := range []string{searchCalleeHopSignal, searchFullUnitSignal, searchCompleteSymbolSignal} {
		if !hasSearchSignal(body, want) {
			t.Fatalf("signals = %v, want %s", body.Signals, want)
		}
	}
	if body.Section != searchSectionPrimary {
		t.Fatalf("section = %q, want primary: a callee hop IS a candidate fix site", body.Section)
	}
	if lineCount := len(strings.Split(body.Snippet, "\n")); lineCount != body.SnippetEndLine-body.SnippetStartLine+1 {
		t.Fatalf("snippet is %d lines for span %d-%d — it must stay a verbatim slice",
			lineCount, body.SnippetStartLine, body.SnippetEndLine)
	}
}

// TestMergeSearchCalleeHopSitesHonoursTheFundingOrder pins the contract: the protected head is never
// touched, the tail pays by SHRINKING rather than disappearing, and --max-context-bytes is exact.
func TestMergeSearchCalleeHopSitesHonoursTheFundingOrder(t *testing.T) {
	t.Parallel()
	lines, symbol := editabilityFile(600, 10, 300, "function")
	results := make([]SearchResult, 0, 5)
	for rank := 1; rank <= 5; rank++ {
		start := 20 + rank*40
		results = append(results, SearchResult{
			Rank: rank, FilePath: "pkg/file.go", StartLine: start, EndLine: start + 9,
			FocusLine: start + 2, SnippetStartLine: start, SnippetEndLine: start + 9,
			Snippet: strings.Join(lines[start-1:start+9], "\n"), Signals: []string{},
		})
	}
	entry := widenSearchResultToEnclosure(SearchResult{
		FilePath: "pkg/file.go", StartLine: 10, EndLine: 300, FocusLine: 10,
		SnippetStartLine: 10, SnippetEndLine: 300, Signals: []string{searchCalleeHopSignal},
	}, searchEnclosure{start: 10, end: 300, lines: lines, symbol: symbol, forced: true})

	// The budget is the EXACT size of the plan that keeps the head whole and tersifies everything
	// below it, so the merge can only succeed by making that trade and no looser one.
	budget := serializedSearchResultBytes(
		planSearchCalleeHopRanking(results, []SearchResult{entry}, []int{0}, 1, 2),
	)

	plan, seated := mergeSearchCalleeHopSites(
		results, []SearchResult{entry}, []int{0}, budget, 2, 1,
	)
	if seated != 1 {
		t.Fatalf("seated = %d, want 1", seated)
	}
	// The tail paid, by shrinking rather than by disappearing.
	if len(plan[len(plan)-1].Snippet) >= len(results[len(results)-1].Snippet) {
		t.Fatal("the deepest rank did not yield, so the body was funded out of thin air")
	}
	if serializedSearchResultBytes(plan) > budget {
		t.Fatalf("plan is %d bytes over a %d-byte ceiling — --max-context-bytes must be exact",
			serializedSearchResultBytes(plan), budget)
	}
	// Inserted directly after its anchor, and the anchor itself is untouched.
	if plan[1].SnippetStartLine != 10 || plan[1].SnippetEndLine != 300 {
		t.Fatalf("entry landed at %d-%d in position 2", plan[1].SnippetStartLine, plan[1].SnippetEndLine)
	}
	if plan[0].Snippet != results[0].Snippet {
		t.Fatal("the protected head was shrunk to pay for a callee body")
	}
	// Nothing was DROPPED: every ranked file keeps a mention, and ranks are contiguous.
	if len(plan) != len(results)+1 {
		t.Fatalf("plan has %d entries, want %d — a hop must never drop a ranked result",
			len(plan), len(results)+1)
	}
	for index := range plan {
		if plan[index].Rank != index+1 {
			t.Fatalf("rank %d at index %d", plan[index].Rank, index)
		}
	}

	// A ceiling that cannot hold the body even with the whole tail tersified seats nothing and
	// returns the ranking untouched.
	tight := serializedSearchResultBytes(results[0]) * 2
	untouched, none := mergeSearchCalleeHopSites(results, []SearchResult{entry}, []int{0}, tight, 2, 1)
	if none != 0 || len(untouched) != len(results) {
		t.Fatalf("seated = %d with %d entries under a %d-byte ceiling", none, len(untouched), tight)
	}
}
