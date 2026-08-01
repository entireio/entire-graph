package sem

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseSearchConstraintsFindsGeneralNamesAndOrderedWindows(t *testing.T) {
	q := `What movie did Avery recommend after the conference?`
	got := parseSearchConstraints(q)
	if !slices.Contains(got.Entities, searchEntityConstraint{Raw: "Avery", Normalized: "avery"}) ||
		!containsSearchConstraintPhrase(got.Phrases, []string{"movie", "avery", "recommend"}) || got.Truncated {
		t.Fatalf("constraints = %#v", got)
	}
}

func TestOrderedSearchPhraseMatchRequiresAdjacentWrittenOrder(t *testing.T) {
	phrase := searchPhraseConstraint{Words: []string{"stale", "token", "order"}}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"the stale token order is rejected", true},
		{"token stale order", false},
		{"stale token insertion order", false},
	} {
		if got := orderedSearchPhraseMatch(tc.text, phrase); got != tc.want {
			t.Fatalf("match(%q) = %t, want %t", tc.text, got, tc.want)
		}
	}
}

func TestSearchCandidateAgreementRequiresEntityAndRelationshipOnOneCandidate(t *testing.T) {
	constraints := parseSearchConstraints(`What movie did Avery recommend?`)
	matching := searchCandidate{result: SearchResult{Snippet: "Avery recommended the movie Arrival."}}
	wrongSubject := searchCandidate{result: SearchResult{Snippet: "Blake recommended the movie Arrival."}}
	entityOnly := searchCandidate{result: SearchResult{Snippet: "Avery attended the festival."}}
	matchBonus, _ := searchCandidateAgreement(matching, constraints)
	wrongBonus, _ := searchCandidateAgreement(wrongSubject, constraints)
	entityBonus, _ := searchCandidateAgreement(entityOnly, constraints)
	if matchBonus <= entityBonus || wrongBonus != 0 || matchBonus > maxSearchAgreementBonus {
		t.Fatalf("bonuses = match %f wrong %f entity-only %f", matchBonus, wrongBonus, entityBonus)
	}
}

func TestParseSearchConstraintsBoundsAndSentenceStarts(t *testing.T) {
	got := parseSearchConstraints(`What Avery Blake Casey Devon Emery Finley Gray Harper Indigo alpha beta gamma delta epsilon zeta eta theta iota kappa lambda`)
	if len(got.Entities) != maxSearchEntities || len(got.Phrases) != maxSearchPhrases || !got.Truncated {
		t.Fatalf("constraints = %#v", got)
	}

	for _, question := range []string{
		"What opens the archive?",
		"When opens the archive?",
		"Where opens the archive?",
		"Who opens the archive?",
		"Why opens the archive?",
		"How opens the archive?",
		"Ordinary lowercase sentence starts here.",
	} {
		constraints := parseSearchConstraints(question)
		if len(constraints.Entities) != 0 {
			t.Fatalf("sentence start in %q became an entity: %#v", question, constraints.Entities)
		}
	}
}

func TestParseSearchConstraintsRetainsEscapedQuotedPhrase(t *testing.T) {
	got := parseSearchConstraints("Find \"stale \\\"token\\\" order\" after the review.")
	if len(got.Phrases) == 0 || !slices.Equal(got.Phrases[0].Words, []string{"stale", "token", "order"}) {
		t.Fatalf("quoted phrase was not retained: %#v", got.Phrases)
	}
	if !got.Phrases[0].Explicit {
		t.Fatalf("first phrase is not explicit: %#v", got.Phrases[0])
	}
}

func TestSearchCandidateAgreementRequiresWholeEntityToken(t *testing.T) {
	constraints := parseSearchConstraints(`What did Ann recommend?`)
	candidate := searchCandidate{result: SearchResult{Snippet: "Hannah recommended it."}}
	if bonus, signals := searchCandidateAgreement(candidate, constraints); bonus != 0 || len(signals) != 0 {
		t.Fatalf("substring entity matched as a whole token: %f %v", bonus, signals)
	}
}

func TestExplicitPhraseMatchingRequiresExactNormalizedWords(t *testing.T) {
	phrase := searchPhraseConstraint{Words: []string{"recommend", "book"}, Explicit: true}
	if !orderedSearchPhraseMatch("recommend book", phrase) {
		t.Fatal("exact explicit phrase did not match")
	}
	if orderedSearchPhraseMatch("recommended book", phrase) {
		t.Fatal("explicit phrase accepted a morphological variant")
	}
}

func TestAutomaticPhraseMorphologyIsSymmetric(t *testing.T) {
	forward, forwardSignals := searchCandidateAgreement(
		searchCandidate{result: SearchResult{Snippet: "Avery recommended the book."}},
		parseSearchConstraints(`What book did Avery recommend?`),
	)
	reverse, reverseSignals := searchCandidateAgreement(
		searchCandidate{result: SearchResult{Snippet: "Avery recommend the book."}},
		parseSearchConstraints(`What book did Avery recommended?`),
	)
	if forward == 0 || reverse == 0 || forward != reverse ||
		!containsString(forwardSignals, "constraint:relationship") ||
		!containsString(reverseSignals, "constraint:relationship") {
		t.Fatalf("automatic morphology = forward %f %v, reverse %f %v", forward, forwardSignals, reverse, reverseSignals)
	}
}

func TestExplicitPhraseAgreementScoresExactOrderWithoutEntity(t *testing.T) {
	constraints := parseSearchConstraints(`"stale token order"`)
	exact := searchCandidate{result: SearchResult{Snippet: "the stale token order is rejected"}}
	reversed := searchCandidate{result: SearchResult{Snippet: "the order token stale is rejected"}}
	exactBonus, exactSignals := searchCandidateAgreement(exact, constraints)
	reversedBonus, reversedSignals := searchCandidateAgreement(reversed, constraints)
	if exactBonus <= 0 || exactBonus > 1.5 || !containsString(exactSignals, "constraint:phrase") {
		t.Fatalf("exact phrase bonus = %f %v", exactBonus, exactSignals)
	}
	if reversedBonus != 0 || len(reversedSignals) != 0 {
		t.Fatalf("reversed phrase bonus = %f %v, want zero", reversedBonus, reversedSignals)
	}
}

func TestSearchAgreementLeavesUnconstrainedQueryScoresAlone(t *testing.T) {
	candidate := searchCandidate{result: SearchResult{Snippet: "update market sizes"}}
	constraints := parseSearchConstraints("update market sizes")
	if bonus, signals := searchCandidateAgreement(candidate, constraints); bonus != 0 || len(signals) != 0 {
		t.Fatalf("unconstrained query agreement = %f %v, want zero", bonus, signals)
	}
}

func TestParseSearchConstraintsTreatsShoutedSentenceAsEmphasis(t *testing.T) {
	constraints := parseSearchConstraints("UPDATE MARKET SIZES")
	if len(constraints.Entities) != 0 {
		t.Fatalf("shouted sentence became named entities: %#v", constraints.Entities)
	}
}

func TestSearchRepositoryBoostsRelationshipPhraseOnNamedSubject(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "notes/recommendations.go", `package notes

func First() string { return "Avery recommended the movie Arrival." }
func Second() string { return "Blake recommended the movie Arrival." }
`)

	response, err := SearchRepository(context.Background(), repo, "test", "What movie did Avery recommend?", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("results = %#v", response.Results)
	}
	if response.Results[0].SymbolName != "First" || !containsString(response.Results[0].Signals, "constraint:relationship") {
		t.Fatalf("relationship candidate did not rank first: %#v", response.Results)
	}
	seenSecond := false
	for _, result := range response.Results {
		seenSecond = seenSecond || result.SymbolName == "Second"
	}
	if !seenSecond {
		t.Fatalf("non-agreeing candidate was removed: %#v", response.Results)
	}
}

func TestSearchRepositoryRanksExactQuotedOrderWithoutFilteringReverse(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "notes/ordered.go", `package notes
func Ordered() string { return "stale token order" }
`)
	write(t, repo, "notes/reversed.go", `package notes
func Reversed() string { return "order token stale" }
`)
	response, err := SearchRepository(context.Background(), repo, "test", `"stale token order"`, SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) < 2 || response.Results[0].SymbolName != "Ordered" ||
		!containsString(response.Results[0].Signals, "constraint:phrase") {
		t.Fatalf("exact quoted order did not rank first: %#v", response.Results)
	}
	reversedFound := false
	for _, result := range response.Results {
		reversedFound = reversedFound || result.SymbolName == "Reversed"
	}
	if !reversedFound {
		t.Fatalf("reversed candidate was filtered: %#v", response.Results)
	}
}

func TestSearchRepositoryUnquotedConceptQueryKeepsIndependentRegions(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "encoding/serializer.go", `package encoding

// MarshalCompact emits minified serialization output for network transport.
func MarshalCompact(value any) []byte { return nil }

// MarshalIndented provides pretty printing for human-readable output.
func MarshalIndented(value any) []byte { return nil }
`)
	response, err := SearchRepository(context.Background(), repo, "test", "minified and pretty printing serialization output", SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, MaxRegionsPerFile: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, result := range response.Results {
		seen[result.SymbolName] = true
	}
	if !seen["MarshalCompact"] || !seen["MarshalIndented"] {
		t.Fatalf("unquoted concept query lost an independent region: %#v", response.Results)
	}
}

func TestSearchResponseRetainsExactRawConstrainedQuery(t *testing.T) {
	const query = "  What did Avery call \"stale token order\"?  "
	if got := buildSearchQuery(query).raw; got != query {
		t.Fatalf("raw query = %q, want %q", got, query)
	}
	repo := t.TempDir()
	write(t, repo, "notes/item.go", "package notes\nfunc Item() {}\n")
	response, err := SearchRepository(context.Background(), repo, "test", query, SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Query != query {
		t.Fatalf("response query = %q, want %q", response.Query, query)
	}
}

func TestSearchStatsReportsTruncatedConstraints(t *testing.T) {
	query := `What Avery Blake Casey Devon Emery Finley Gray Harper Indigo alpha beta gamma delta epsilon zeta eta theta iota kappa lambda`
	repo := t.TempDir()
	write(t, repo, "notes/item.go", "package notes\nfunc Item() {}\n")
	response, err := SearchRepository(context.Background(), repo, "test", query, SearchOptions{
		Worktree: true, Profile: ProfileSyntaxOnly, TopK: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Stats.QueryConstraintsTruncated {
		t.Fatalf("truncated constraint flag missing: %#v", response.Stats)
	}
}

func TestSearchTaskOneProductionContainsNoBenchmarkLeakage(t *testing.T) {
	sources := map[string]string{}
	for _, path := range []string{"search.go", "search_constraints.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sources[path] = string(source)
	}
	if leaks := searchTaskOneProductionLeakage(sources); len(leaks) != 0 {
		t.Fatalf("task-one production leakage = %v", leaks)
	}
}

func TestSearchTaskOneProductionLeakageCheckerHasCaseFoldedPositiveControl(t *testing.T) {
	leaks := searchTaskOneProductionLeakage(map[string]string{"synthetic.go": "const tag = \"SeSsIoN_example\""})
	if len(leaks) != 1 || leaks[0] != "synthetic.go:session_" {
		t.Fatalf("positive-control leakage = %v", leaks)
	}
}

func containsSearchConstraintPhrase(phrases []searchPhraseConstraint, want []string) bool {
	for _, phrase := range phrases {
		if slices.Equal(phrase.Words, want) {
			return true
		}
	}
	return false
}

func searchTaskOneProductionLeakage(sources map[string]string) []string {
	markers := []string{
		"graphify", "graphmark", "locomo", "longmemeval", "erpnext",
		"case_", "session_", "qa:",
	}
	var leaks []string
	for path, source := range sources {
		folded := strings.ToLower(source)
		for _, marker := range markers {
			if strings.Contains(folded, marker) {
				leaks = append(leaks, path+":"+marker)
			}
		}
	}
	slices.Sort(leaks)
	return leaks
}
