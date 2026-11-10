package sem

import (
	"fmt"
	"testing"
)

// cloneProseCandidates deep-copies the passage plans as well as the candidates. dropSelectedProsePassages
// filters a plan in place (`kept := plan[:0]`), so two runs over slices that share a backing array
// would corrupt each other — the test needs independent inputs, not the production path's.
func cloneProseCandidates(candidates []searchCandidate) []searchCandidate {
	cloned := append([]searchCandidate(nil), candidates...)
	for index := range cloned {
		cloned[index].prosePassagePlan = append([]SearchPassage(nil), cloned[index].prosePassagePlan...)
	}
	return cloned
}

// TestProsePassageDeduplicationRunsOnTheFinalSelection pins WHERE passages are deduplicated.
//
// Passages are planned per selected unit over the whole document, so the same region is planned by
// every selected section of that document and has to be given to exactly one of them. Doing that
// against the SEMANTIC selection is wrong under --deep, because hybrid fusion then replaces rows:
// a region claimed by a semantic row fusion drops is deleted from the plan of the row that would
// still have printed it, and disappears from the payload entirely. Deduplication therefore has to
// see the selection the caller will actually be shown.
func TestProsePassageDeduplicationRunsOnTheFinalSelection(t *testing.T) {
	q := buildSearchQuery("amber lantern")
	q.matchableWords = []string{"amber", "lantern"}
	// Four documents is the corpus gate; eight slots over six sections each puts more than one
	// section of the same document in the selection, which is what makes a passage contended.
	selected := selectSearchCandidates(proseSectionCandidates(4, 6), q, 8, 3, true)
	if len(selected) < 2 {
		t.Fatalf("selected %d units, need at least 2 to drop one", len(selected))
	}

	// What the top row claims when the whole semantic selection is deduplicated together.
	full := dropSelectedProsePassages(cloneProseCandidates(selected))
	claimed := full[0].prosePassagePlan
	if len(claimed) == 0 {
		t.Fatal("the top row claimed no passages, so this test cannot observe them being lost")
	}

	// Fusion drops that row. Everything it claimed must still be reachable from the rows that
	// survived — they all planned those regions too.
	survivors := dropSelectedProsePassages(cloneProseCandidates(selected[1:]))
	printed := map[string]bool{}
	key := func(path string, line int) string { return path + ":" + itoaSearchLine(line) }
	for _, candidate := range survivors {
		path := candidate.result.FilePath
		printed[key(path, candidate.result.SnippetStartLine)] = true
		for _, passage := range candidate.prosePassagePlan {
			printed[key(path, passage.StartLine)] = true
		}
	}
	for _, passage := range claimed {
		if !printed[key(full[0].result.FilePath, passage.StartLine)] {
			t.Fatalf("passage at line %d was claimed by a row fusion dropped and is now printed "+
				"nowhere: deduplication ran against a selection the caller never sees", passage.StartLine)
		}
	}

	// And the property deduplication exists for still holds on that final selection: no region is
	// printed twice.
	seen := map[string]bool{}
	for _, candidate := range survivors {
		path := candidate.result.FilePath
		if seen[key(path, candidate.result.SnippetStartLine)] {
			t.Fatalf("region %s:%d printed twice", path, candidate.result.SnippetStartLine)
		}
		seen[key(path, candidate.result.SnippetStartLine)] = true
		for _, passage := range candidate.prosePassagePlan {
			if seen[key(path, passage.StartLine)] {
				t.Fatalf("passage %s:%d duplicates a region already printed", path, passage.StartLine)
			}
			seen[key(path, passage.StartLine)] = true
		}
	}
}

func itoaSearchLine(line int) string {
	return fmt.Sprintf("%d", line)
}
