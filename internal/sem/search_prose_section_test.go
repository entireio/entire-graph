package sem

import (
	"fmt"
	"reflect"
	"testing"
)

// proseSectionCandidates builds `files` documents of `sections` headed sections each. Section i of
// every document carries the same terms, so the only thing separating them is score.
func proseSectionCandidates(files, sections int) []searchCandidate {
	candidates := make([]searchCandidate, 0, files*sections)
	for file := 0; file < files; file++ {
		for section := 0; section < sections; section++ {
			candidates = append(candidates, proseTestCandidate(
				fmt.Sprintf("sessions/session-%02d.md", file),
				1+section*10,
				float64(100-file*sections-section),
				"# Amber lantern",
				map[string]int{"amber": 1, "lantern": 1},
			))
		}
	}
	sortSearchCandidates(candidates)
	return candidates
}

// A prose document is not one indivisible retrievable thing. Before this, the unit was the file, so
// a corpus of N documents could never return more than N regions however large --top-k was: the
// 4x6 corpus below returned 4 results for --top-k 12 and threw the other 20 ranked sections away.
func TestSelectSearchCandidatesRanksProseSectionsAsUnits(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := proseSectionCandidates(4, 6)

	document := selectSearchCandidates(candidates, q, 12, 3, false)
	if len(document) != 4 {
		t.Fatalf("document resolution selected %d units, want one per file (4)", len(document))
	}

	section := selectSearchCandidates(candidates, q, 12, 3, true)
	if len(section) != 12 {
		t.Fatalf("section resolution selected %d units, want top-k 12", len(section))
	}
	seen := map[string]bool{}
	for _, candidate := range section {
		key := candidate.result.SymbolID
		if seen[key] {
			t.Fatalf("section %q returned twice: %#v", key, section)
		}
		seen[key] = true
	}
}

// Every returned region must carry the score its own content earned. Promotion inherited the
// parent's score, so a top-k of 200 over 19 documents came back with 19 distinct scores and a
// consumer that sorts by score saw one document's regions before any of the next document's.
func TestSelectSearchCandidatesScoresProseSectionsIndependently(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := proseSectionCandidates(4, 6)

	section := selectSearchCandidates(candidates, q, 12, 3, true)
	scores := map[float64]bool{}
	for _, candidate := range section {
		scores[candidate.score] = true
	}
	if len(scores) != len(section) {
		t.Fatalf("%d sections carry only %d distinct scores", len(section), len(scores))
	}
}

// Breadth is the property the file-keyed unit bought, and it must survive: a question is often
// answered by the one document whose terms nobody ranked highly. Document 3 holds the six WORST
// scores in the corpus, so a pure score ranking would exclude it entirely.
func TestSelectSearchCandidatesKeepsEveryProseDocumentRepresented(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := proseSectionCandidates(4, 6)

	section := selectSearchCandidates(candidates, q, 6, 3, true)
	files := map[string]bool{}
	for _, candidate := range section {
		files[candidate.result.FilePath] = true
	}
	for file := 0; file < 4; file++ {
		path := fmt.Sprintf("sessions/session-%02d.md", file)
		if !files[path] {
			t.Fatalf("document %s was excluded entirely: %#v", path, files)
		}
	}
}

// A prose format with no heading structure has no section to belong to. Those regions keep the
// file as their unit, which is the previous behaviour exactly.
func TestSelectSearchCandidatesFallsBackToFileWithoutSectionIdentity(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := proseSectionCandidates(4, 6)
	for index := range candidates {
		candidates[index].result.SymbolID = ""
	}
	sortSearchCandidates(candidates)

	section := selectSearchCandidates(candidates, q, 12, 3, true)
	if len(section) != 4 {
		t.Fatalf("selected %d units without section identity, want one per file (4)", len(section))
	}
}

// The prose gate asks what the documents ARE, not how finely they are subdivided, so it must read
// identically under either unit. Three documents is below the four-document floor whether they
// contribute 3 units or 18.
func TestProseParentCorpusCountsDocumentsNotSections(t *testing.T) {
	candidates := proseSectionCandidates(3, 6)
	if proseParentCorpus(proseParents(candidates, false)) {
		t.Fatal("3 documents passed the prose gate at document resolution")
	}
	if proseParentCorpus(proseParents(candidates, true)) {
		t.Fatal("3 documents passed the prose gate at section resolution")
	}
	wide := proseSectionCandidates(4, 6)
	if !proseParentCorpus(proseParents(wide, false)) {
		t.Fatal("4 documents failed the prose gate at document resolution")
	}
	if !proseParentCorpus(proseParents(wide, true)) {
		t.Fatal("4 documents failed the prose gate at section resolution")
	}
}

// Code search must be inert. A source corpus never reaches the prose selector, so section units
// cannot reorder, add or drop a single code result.
func TestSelectSearchCandidatesLeavesCodeSelectionUnchanged(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	candidates := make([]searchCandidate, 0, 24)
	for file := 0; file < 4; file++ {
		for symbol := 0; symbol < 6; symbol++ {
			candidate := proseTestCandidate(
				fmt.Sprintf("internal/pkg/file%02d.go", file), 1+symbol*10,
				float64(100-file*6-symbol), "func amberLantern() {}",
				map[string]int{"amber": 1, "lantern": 1},
			)
			candidate.result.Language = "Go"
			candidate.result.Kind = "function"
			candidates = append(candidates, candidate)
		}
	}
	sortSearchCandidates(candidates)

	document := searchCandidateResults(selectSearchCandidates(candidates, q, 12, 3, false))
	section := searchCandidateResults(selectSearchCandidates(candidates, q, 12, 3, true))
	if len(document) != len(section) {
		t.Fatalf("code selection changed size: %d -> %d", len(document), len(section))
	}
	for index := range document {
		if !reflect.DeepEqual(document[index], section[index]) {
			t.Fatalf("code result %d changed:\n want %#v\n  got %#v", index, document[index], section[index])
		}
	}
}
