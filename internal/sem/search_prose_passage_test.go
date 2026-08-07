package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlanProseParentPassagesPrefersMarginalCoverageAndRejectsOverlap(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("amber lantern")
	primary := proseTestCandidate(
		"sessions/focus.md", 50, 20, "Amber archive", map[string]int{"amber": 1},
	)
	overlap := primary
	overlap.score = 30
	overlap.result.Snippet = "Amber duplicate"
	lantern := proseTestCandidate(
		"sessions/focus.md", 10, 4, "Lantern detail", map[string]int{"lantern": 1},
	)
	amber := proseTestCandidate(
		"sessions/focus.md", 90, 12, "Amber history", map[string]int{"amber": 1},
	)
	parent := &proseParent{
		path:       primary.result.FilePath,
		best:       &primary,
		candidates: []*searchCandidate{&overlap, &lantern, &amber},
	}

	passages := planProseParentPassages(parent, primary, q, 3)
	if len(passages) != 2 {
		t.Fatalf("planned %d passages, want 2: %#v", len(passages), passages)
	}
	if passages[0].StartLine != 10 || passages[1].StartLine != 90 {
		t.Fatalf("passage lines = %d, %d; want marginal query coverage then distant fallback",
			passages[0].StartLine, passages[1].StartLine)
	}
	if passages[0].Snippet != "Lantern detail" || passages[1].Snippet != "Amber history" {
		t.Fatalf("unexpected passage text: %#v", passages)
	}
}

func TestAllocateProseParentPassagesRoundRobinsAcrossSessions(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{
			Rank: 1, FilePath: "sessions/one.md", StartLine: 10, EndLine: 10, FocusLine: 10,
			SnippetStartLine: 10, SnippetEndLine: 10, Snippet: "primary one",
			Signals: []string{proseParentRetrievalSignal},
		},
		{
			Rank: 2, FilePath: "sessions/two.md", StartLine: 20, EndLine: 20, FocusLine: 20,
			SnippetStartLine: 20, SnippetEndLine: 20, Snippet: "primary two",
			Signals: []string{proseParentRetrievalSignal},
		},
	}
	passage := func(line int, label string) SearchPassage {
		return SearchPassage{StartLine: line, EndLine: line, FocusLine: line, Snippet: strings.Repeat(label, 20)}
	}
	plans := map[int][]SearchPassage{
		1: {passage(100, "alpha "), passage(110, "beta ")},
		2: {passage(200, "gamma "), passage(210, "delta ")},
	}
	want := append([]SearchResult(nil), results...)
	want[0].Passages = []SearchPassage{plans[1][0]}
	want[1].Passages = []SearchPassage{plans[2][0]}
	hardBudget := serializedSearchResultBytes(want)

	allocated, count, _ := allocateProseParentPassages(results, plans, hardBudget)
	if count != 2 || len(allocated[0].Passages) != 1 || len(allocated[1].Passages) != 1 {
		t.Fatalf("round-robin allocation = count %d, passages %d/%d; want one per session",
			count, len(allocated[0].Passages), len(allocated[1].Passages))
	}
	if size := serializedSearchResultBytes(allocated); size > hardBudget {
		t.Fatalf("passage payload %d exceeds hard budget %d", size, hardBudget)
	}
}

func TestSearchResponseRejectsOverlappingProsePassages(t *testing.T) {
	t.Parallel()
	result := SearchResult{
		Rank: 1, FilePath: "sessions/focus.md", StartLine: 10, EndLine: 20, FocusLine: 15,
		SnippetStartLine: 10, SnippetEndLine: 20, Snippet: "primary",
		Passages: []SearchPassage{{StartLine: 18, EndLine: 22, FocusLine: 20, Snippet: "overlap"}},
	}
	response := SearchResponse{
		Results: []SearchResult{result},
		Stats:   SearchStats{ResultBytes: serializedSearchResultBytes([]SearchResult{result})},
	}
	if err := response.Validate(); err == nil || !strings.Contains(err.Error(), "passage") {
		t.Fatalf("Validate error = %v, want overlapping passage rejection", err)
	}
}

func TestSearchRepositoryReturnsDistantPassageFromMarkdownParent(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "sessions/focus.md", `# Amber travel archive

The amber trip began in Lisbon.

`+strings.Repeat("Neutral travel note.\n", 100)+`
The lantern purchase cost exactly 185 dollars.
`)
	for index := 1; index < 5; index++ {
		write(t, repo, fmt.Sprintf("sessions/peer-%d.md", index), fmt.Sprintf(`
# Amber lantern archive %d

This unrelated note records marker %d.
`, index, index))
	}

	response, err := SearchRepository(
		t.Context(), repo, "test", "amber lantern purchase cost", SearchOptions{
			Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, MaxContextBytes: 128_000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath != "sessions/focus.md" {
			continue
		}
		if strings.Contains(result.Snippet, "185 dollars") {
			return
		}
		for _, passage := range result.Passages {
			if strings.Contains(passage.Snippet, "185 dollars") {
				return
			}
		}
		t.Fatalf("focus parent omitted distant answer-bearing passage: %#v", result)
	}
	t.Fatalf("focus parent missing from results: %#v", response.Results)
}
