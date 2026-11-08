package sem

import (
	"reflect"
	"strings"
	"testing"
)

func alreadyCompleteTestSource() ([]string, SymbolRecord, SearchResult) {
	lines := []string{"package small", "", "// Exhausted reports whether the budget is spent.", "func Exhausted(attempt, budget int) bool {", "\treturn attempt >= budget", "}", "", "// trailing context"}
	symbol := SymbolRecord{ID: "whole", FilePath: "pkg/file.go", Kind: "function", Name: "Exhausted", StartLine: 4, EndLine: 6}
	result := SearchResult{Rank: 1, FilePath: symbol.FilePath, SymbolID: symbol.ID, SymbolName: symbol.Name, Kind: symbol.Kind,
		StartLine: 2, EndLine: 7, FocusLine: 4, SnippetStartLine: 2, SnippetEndLine: 7,
		SymbolStartLine: 4, SymbolEndLine: 6, Snippet: strings.Join(lines[1:7], "\n"), Signals: []string{"body"}}
	return lines, symbol, result
}

func TestAlreadyCompleteSnippetGainsSignalWithoutChangingSource(t *testing.T) {
	for _, bounds := range [][2]int{{4, 6}, {2, 6}, {2, 7}} {
		lines, symbol, result := alreadyCompleteTestSource()
		result.StartLine, result.SnippetStartLine = bounds[0], bounds[0]
		result.EndLine, result.SnippetEndLine = bounds[1], bounds[1]
		result.Snippet = strings.Join(lines[bounds[0]-1:bounds[1]], "\n")
		results := []SearchResult{result}
		got, bodies := certifyAlreadyCompleteSearchResults(results, results, map[string]SymbolRecord{symbol.ID: symbol},
			enclosureTestReader(lines), serializedSearchResultBytes(results)+128, 0)
		if bodies != 1 || !hasSearchSignal(got[0], searchCompleteSymbolSignal) {
			t.Fatalf("bounds=%v: bodies=%d signals=%v", bounds, bodies, got[0].Signals)
		}
		want := result
		want.Signals = append(append([]string(nil), result.Signals...), searchCompleteSymbolSignal)
		if !reflect.DeepEqual(got[0], want) {
			t.Fatalf("bounds=%v: certification changed result:\ngot  %+v\nwant %+v", bounds, got[0], want)
		}
	}
}

func TestAlreadyCompleteCertificationDoesNotBuyMetadataWithSiblingSource(t *testing.T) {
	lines, symbol, first := alreadyCompleteTestSource()
	results := []SearchResult{first}
	for rank := 2; rank <= 6; rank++ {
		results = append(results, SearchResult{Rank: rank, FilePath: "other.go", StartLine: 1, EndLine: 6, FocusLine: 3,
			SnippetStartLine: 1, SnippetEndLine: 6, Snippet: "one\ntwo\nthree\nfour\nfive\nsix", Signals: []string{"body"}})
	}
	wantMarked := append([]SearchResult(nil), results...)
	wantMarked[0].Signals = append(append([]string(nil), first.Signals...), searchCompleteSymbolSignal)
	for _, tc := range []struct {
		name     string
		budget   int
		reserved int
		marked   bool
	}{
		{"no spare bytes", serializedSearchResultBytes(results), 0, false},
		{"one byte short of metadata", serializedSearchResultBytes(wantMarked) - 1, 0, false},
		{"exact metadata fit", serializedSearchResultBytes(wantMarked), 0, true},
		{"reserved blocks leave no metadata room", serializedSearchResultBytes(wantMarked), 1, false},
		{"metadata and reserved blocks fit exactly", serializedSearchResultBytes(wantMarked) + 100, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, bodies := certifyAlreadyCompleteSearchResults(results, results[:1], map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), tc.budget, tc.reserved)
			want := results
			if tc.marked {
				want = wantMarked
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("metadata changed source allocation: got=%+v want=%+v", got, want)
			}
			if (bodies == 1) != tc.marked {
				t.Fatalf("bodies=%d marked=%v", bodies, tc.marked)
			}
			if size := serializedSearchResultBytes(got) + tc.reserved; size > tc.budget {
				t.Fatalf("serialized size %d exceeds %d", size, tc.budget)
			}
		})
	}
}

func TestAlreadyCompleteCertificationRequiresVerifiedSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*SymbolRecord, *SearchResult, *[]string)
	}{
		{"mismatched bytes", func(_ *SymbolRecord, r *SearchResult, _ *[]string) {
			r.Snippet = strings.Replace(r.Snippet, ">=", "<=", 1)
		}},
		{"missing closing line", func(_ *SymbolRecord, r *SearchResult, _ *[]string) {
			r.Snippet = strings.Join(strings.Split(r.Snippet, "\n")[:4], "\n")
		}},
		{"invalid symbol start", func(s *SymbolRecord, _ *SearchResult, _ *[]string) { s.StartLine = 0 }},
		{"invalid result start", func(_ *SymbolRecord, r *SearchResult, _ *[]string) { r.StartLine = 0 }},
		{"negative reported symbol bound", func(_ *SymbolRecord, r *SearchResult, _ *[]string) { r.SymbolStartLine = -1 }},
		{"symbol past eof", func(s *SymbolRecord, r *SearchResult, lines *[]string) {
			s.EndLine = len(*lines) + 2
			r.SnippetEndLine = len(*lines)
			r.EndLine = len(*lines)
			r.Snippet = strings.Join((*lines)[r.SnippetStartLine-1:], "\n")
		}},
		{"wrong file", func(s *SymbolRecord, _ *SearchResult, _ *[]string) { s.FilePath = "other.go" }},
		{"missing identity", func(s *SymbolRecord, _ *SearchResult, _ *[]string) { s.ID = "different" }},
		{"container", func(s *SymbolRecord, _ *SearchResult, _ *[]string) { s.Kind = "class" }},
		{"focus outside source", func(_ *SymbolRecord, r *SearchResult, _ *[]string) { r.FocusLine = 8 }},
		{"elided signal", func(_ *SymbolRecord, r *SearchResult, _ *[]string) {
			r.Signals = append(r.Signals, searchFullUnitElidedSignal)
		}},
		{"larger unit bounds", func(_ *SymbolRecord, r *SearchResult, _ *[]string) { r.UnitStartLine, r.UnitEndLine = 1, 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, symbol, result := alreadyCompleteTestSource()
			tc.mutate(&symbol, &result, &lines)
			results := []SearchResult{result}
			got, _ := certifyAlreadyCompleteSearchResults(results, results, map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), 10000, 0)
			if hasSearchSignal(got[0], searchCompleteSymbolSignal) {
				t.Fatalf("unverified source certified: %+v", got[0])
			}
		})
	}
}

func TestAlreadyCompleteCertificationPreservesIdentityEligibilityAfterRenumbering(t *testing.T) {
	lines, symbol, result := alreadyCompleteTestSource()
	unknown := SearchResult{Rank: 1, FilePath: "unknown.go", StartLine: 1, EndLine: 1, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 1, Snippet: "unknown"}
	// The eligible original head moved to rank two; the new rank one must not
	// inherit its eligibility merely because it now occupies that array index.
	results := []SearchResult{unknown, result}
	results[1].Rank = 2
	got, count := certifyAlreadyCompleteSearchResults(results, []SearchResult{result}, map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), 10000, 0)
	if count != 1 || !reflect.DeepEqual(got[0], unknown) || !hasSearchSignal(got[1], searchCompleteSymbolSignal) {
		t.Fatalf("eligibility moved by index: %+v", got)
	}
	got, count = certifyAlreadyCompleteSearchResults(results, []SearchResult{unknown}, map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), 10000, 0)
	if count != 0 || !reflect.DeepEqual(got, results) {
		t.Fatalf("an ineligible identity was certified: %+v", got)
	}
}

func TestAlreadyCompleteCertificationDoesNotPromoteLocatorTails(t *testing.T) {
	lines, symbol, result := alreadyCompleteTestSource()
	results := make([]SearchResult, searchRenderedSnippetHeadRanks+1)
	for index := range results {
		results[index] = SearchResult{Rank: index + 1, FilePath: "unknown.go", StartLine: 1, EndLine: 1, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 1, Snippet: "unknown"}
	}
	result.Rank = len(results)
	results[len(results)-1] = result
	got, count := certifyAlreadyCompleteSearchResults(results, []SearchResult{result}, map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), 10000, 0)
	if count != 0 || !reflect.DeepEqual(got, results) {
		t.Fatalf("locator tail gained a body: %+v", got)
	}
}

func TestAlreadyCompleteSingleLineRequiresAllSourceBytes(t *testing.T) {
	const source = "func Answer() int { return 42 }"
	symbol := SymbolRecord{ID: "one", FilePath: "one.go", Kind: "function", Name: "Answer", StartLine: 1, EndLine: 1}
	for _, snippet := range []string{source, "func Answer() int"} {
		result := SearchResult{Rank: 1, FilePath: symbol.FilePath, SymbolID: symbol.ID, SymbolName: symbol.Name, Kind: symbol.Kind, StartLine: 1, EndLine: 1, FocusLine: 1, SnippetStartLine: 1, SnippetEndLine: 1, Snippet: snippet}
		got, count := certifyAlreadyCompleteSearchResults([]SearchResult{result}, []SearchResult{result}, map[string]SymbolRecord{symbol.ID: symbol}, func(string) (string, bool) { return source, true }, 10000, 0)
		if (count == 1) != (snippet == source) || hasSearchSignal(got[0], searchCompleteSymbolSignal) != (snippet == source) {
			t.Fatalf("snippet=%q signals=%v count=%d", snippet, got[0].Signals, count)
		}
	}
}

func TestAlreadyCompleteCertificationRejectsMismatchedRecordIdentity(t *testing.T) {
	lines, symbol, result := alreadyCompleteTestSource()
	symbol.ID = "other"
	got, count := certifyAlreadyCompleteSearchResults([]SearchResult{result}, []SearchResult{result}, map[string]SymbolRecord{result.SymbolID: symbol}, enclosureTestReader(lines), 10000, 0)
	if count != 0 || !reflect.DeepEqual(got, []SearchResult{result}) {
		t.Fatalf("mismatched record identity certified: %+v", got)
	}
}

func TestAlreadyCompleteCertificationPreservesEmptyResults(t *testing.T) {
	results := []SearchResult{}
	got, count := certifyAlreadyCompleteSearchResults(results, nil, nil, nil, 1000, 0)
	if count != 0 || !reflect.DeepEqual(got, results) {
		t.Fatalf("empty array changed: got=%#v count=%d", got, count)
	}
}

func TestAlreadyCompleteCertificationChecksTheFinalClippedSpan(t *testing.T) {
	lines, symbol, original := alreadyCompleteTestSource()
	clipped := tersifySearchResult(original, 1)
	got, count := certifyAlreadyCompleteSearchResults([]SearchResult{clipped}, []SearchResult{original}, map[string]SymbolRecord{symbol.ID: symbol}, enclosureTestReader(lines), 10000, 0)
	if count != 0 || !reflect.DeepEqual(got, []SearchResult{clipped}) {
		t.Fatalf("old eligibility certified a clipped final body: %+v", got)
	}
}
