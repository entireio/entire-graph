package sem

import (
	"strconv"
	"strings"
	"testing"
)

func proseResolutionParent(rank int, path string, line int, snippet string, passages ...SearchPassage) SearchResult {
	return SearchResult{
		Rank: rank, Score: float64(100 - rank), FilePath: path,
		StartLine: line, EndLine: line, FocusLine: line,
		SnippetStartLine: line, SnippetEndLine: line, Snippet: snippet,
		Language: "markdown",
		Signals:  []string{proseParentRetrievalSignal},
		Passages: passages,
	}
}

func proseResolutionPassage(line int, snippet string) SearchPassage {
	return SearchPassage{StartLine: line, EndLine: line, FocusLine: line, Snippet: snippet}
}

// TestExpandProseResolutionPromotesPassagesIntoSpareSlots is the whole point of the pass: the
// finer regions the ranker already computed become results a consumer that reads `results[]`
// can see, instead of staying in a field it never asked about.
func TestExpandProseResolutionPromotesPassagesIntoSpareSlots(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		proseResolutionParent(1, "sessions/one.md", 10, "primary one",
			proseResolutionPassage(30, "one alpha"), proseResolutionPassage(50, "one beta")),
	}

	expanded := expandProseResolution(results, 10, 0)

	if len(expanded) != 3 {
		t.Fatalf("expanded to %d results, want 3", len(expanded))
	}
	if len(expanded[0].Passages) != 0 {
		t.Fatalf("promoted passages still attached to the parent: %#v", expanded[0].Passages)
	}
	for index, want := range []struct {
		line    int
		snippet string
	}{{30, "one alpha"}, {50, "one beta"}} {
		got := expanded[index+1]
		if got.Rank != index+2 {
			t.Fatalf("rank %d at index %d", got.Rank, index+1)
		}
		if got.FilePath != "sessions/one.md" || got.StartLine != want.line || got.EndLine != want.line ||
			got.SnippetStartLine != want.line || got.SnippetEndLine != want.line || got.FocusLine != want.line {
			t.Fatalf("promoted span = %#v, want lines %d", got, want.line)
		}
		if got.Snippet != want.snippet {
			t.Fatalf("promoted snippet = %q, want %q", got.Snippet, want.snippet)
		}
		if got.Score != results[0].Score {
			t.Fatalf("promoted score = %v, want the parent's %v", got.Score, results[0].Score)
		}
		if !hasSearchSignal(got, proseResolutionSignal) {
			t.Fatalf("promoted result is not labelled: %#v", got.Signals)
		}
		if got.SymbolID != "" || got.SymbolName != "" || got.QualifiedName != "" {
			t.Fatalf("promoted result claims the parent's symbol identity: %#v", got)
		}
	}
	if len(results[0].Passages) != 2 {
		t.Fatalf("input was mutated: %#v", results[0].Passages)
	}
}

// TestExpandProseResolutionNeverDisplacesAFile is the property that separates this from --deep:
// the expansion only ever fills slots the distinct-file ranking left empty.
func TestExpandProseResolutionNeverDisplacesAFile(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		proseResolutionParent(1, "sessions/one.md", 10, "primary one", proseResolutionPassage(30, "one alpha")),
		proseResolutionParent(2, "sessions/two.md", 20, "primary two", proseResolutionPassage(40, "two alpha")),
	}

	for _, topK := range []int{0, 1, 2} {
		expanded := expandProseResolution(results, topK, 0)
		if len(expanded) != len(results) {
			t.Fatalf("top-k %d expanded to %d results, want %d", topK, len(expanded), len(results))
		}
		if len(expanded[0].Passages) != 1 {
			t.Fatalf("top-k %d dropped a passage it did not promote", topK)
		}
	}
}

// TestExpandProseResolutionRoundRobinsAcrossParents stops one long document spending every spare
// slot before a second document has contributed anything.
func TestExpandProseResolutionRoundRobinsAcrossParents(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		proseResolutionParent(1, "sessions/one.md", 10, "primary one",
			proseResolutionPassage(30, "one alpha"), proseResolutionPassage(50, "one beta")),
		proseResolutionParent(2, "sessions/two.md", 20, "primary two",
			proseResolutionPassage(40, "two alpha")),
	}

	expanded := expandProseResolution(results, 4, 0)

	if len(expanded) != 4 {
		t.Fatalf("expanded to %d results, want 4", len(expanded))
	}
	// Round robin is a property of WHICH passages are promoted, not of where they are printed:
	// the second document contributes before the first document contributes twice. `one beta` is
	// the first document's depth-2 passage, so its absence is the property under test. (Position
	// is no longer meaningful here — results[] is re-sorted by score, so a promotion is reported
	// beside its parent rather than at the end.)
	promoted := map[string]bool{}
	bySnippet := map[string]SearchResult{}
	for _, result := range expanded {
		bySnippet[result.Snippet] = result
		if hasSearchSignal(result, proseResolutionSignal) {
			promoted[result.Snippet] = true
		}
	}
	if !promoted["one alpha"] || !promoted["two alpha"] || promoted["one beta"] {
		t.Fatalf("promoted set = %v, want one alpha and two alpha but not one beta", promoted)
	}
	if parent := bySnippet["primary one"]; len(parent.Passages) != 1 || parent.Passages[0].Snippet != "one beta" {
		t.Fatalf("unpromoted passage lost from its parent: %#v", parent.Passages)
	}
	if parent := bySnippet["primary two"]; len(parent.Passages) != 0 {
		t.Fatalf("promoted passage still attached to its parent: %#v", parent.Passages)
	}
}

// TestExpandProseResolutionKeepsResultsScoreOrdered pins the invariant results[] advertises.
// Promotions inherit their parent's score, so appending them left a high-scoring passage sitting
// behind every low-scoring parent: a consumer taking "top 20 by score" got a different set than
// the first 20 entries, which is precisely the consumer this pass exists to serve.
func TestExpandProseResolutionKeepsResultsScoreOrdered(t *testing.T) {
	t.Parallel()
	results := make([]SearchResult, 0, 6)
	for index := 1; index <= 6; index++ {
		results = append(results, proseResolutionParent(
			index, "sessions/doc-"+strconv.Itoa(index)+".md", index*10,
			"primary "+strconv.Itoa(index),
			proseResolutionPassage(index*10+5, "passage "+strconv.Itoa(index)),
		))
	}

	expanded := expandProseResolution(results, 12, 0)

	if len(expanded) != 12 {
		t.Fatalf("expanded to %d results, want 12", len(expanded))
	}
	for index := 1; index < len(expanded); index++ {
		if expanded[index].Score > expanded[index-1].Score {
			t.Fatalf("results not descending by score: position %d scores %v, position %d scores %v",
				index-1, expanded[index-1].Score, index, expanded[index].Score)
		}
	}
	for index := range expanded {
		if expanded[index].Rank != index+1 {
			t.Fatalf("rank %d reported at position %d", expanded[index].Rank, index)
		}
	}
}

// TestExpandProseResolutionRespectsByteBudget: the expansion is additive, so it is the one pass
// that could push a payload past the ceiling the fitter already satisfied.
func TestExpandProseResolutionRespectsByteBudget(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		proseResolutionParent(1, "sessions/one.md", 10, "primary one",
			proseResolutionPassage(30, strings.Repeat("a", 400)),
			proseResolutionPassage(50, strings.Repeat("b", 400))),
	}
	budget := serializedSearchResultBytes(results)

	expanded := expandProseResolution(results, 10, budget)

	if serializedSearchResultBytes(expanded) > budget {
		t.Fatalf("expansion breached the budget: %d > %d", serializedSearchResultBytes(expanded), budget)
	}
	if len(expanded) != 1 {
		t.Fatalf("expanded to %d results under a tight budget, want 1", len(expanded))
	}
	if len(expanded[0].Passages) != 2 {
		t.Fatalf("passages lost when promotion was unaffordable: %#v", expanded[0].Passages)
	}
	if unbounded := expandProseResolution(results, 10, 0); len(unbounded) != 3 {
		t.Fatalf("unbounded expansion returned %d results, want 3", len(unbounded))
	}
}

// TestExpandProseResolutionIgnoresResultsWithoutPassages is the code-search case: nothing to
// promote, so nothing changes.
func TestExpandProseResolutionIgnoresResultsWithoutPassages(t *testing.T) {
	t.Parallel()
	results := []SearchResult{
		{Rank: 1, FilePath: "internal/sem/search.go", StartLine: 1, EndLine: 2, FocusLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 2, Snippet: "func Search() {}"},
	}

	expanded := expandProseResolution(results, 10, 0)

	if len(expanded) != 1 || expanded[0].Snippet != results[0].Snippet {
		t.Fatalf("code result changed: %#v", expanded)
	}
}

// TestSearchRepositoryReturnsMultiResolutionProseResults exercises the wiring end to end, and its
// --single-resolution counterpart proves the switch restores the previous shape.
func TestSearchRepositoryReturnsMultiResolutionProseResults(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 6; index++ {
		var body strings.Builder
		body.WriteString("# Session\n\n")
		for turn := 0; turn < 12; turn++ {
			body.WriteString("## note: amber lantern orchard ledger braided vessel ridge marker\n\n")
		}
		write(t, repo, "sessions/session-"+string(rune('a'+index))+".md", body.String())
	}

	// The switches are separate levers over the same corpus: --document-resolution makes the
	// DOCUMENT the ranked unit again (one result per file), and --single-resolution stops the
	// spare slots being spent on finer regions of whatever unit was ranked.
	search := func(single, document bool) SearchResponse {
		response, err := SearchRepository(
			t.Context(), repo, "test", "amber lantern orchard ledger", SearchOptions{
				Worktree:           true,
				Profile:            ProfileSyntaxOnly,
				TopK:               40,
				MaxIndexedFiles:    32,
				MaxContextBytes:    400000,
				SingleResolution:   single,
				DocumentResolution: document,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("invalid response (single=%v document=%v): %v", single, document, err)
		}
		return response
	}

	multi, single := search(false, true), search(true, true)
	files := map[string]bool{}
	for _, result := range single.Results {
		files[result.FilePath] = true
	}
	if len(single.Results) != len(files) {
		t.Fatalf("single-resolution returned %d results over %d files", len(single.Results), len(files))
	}
	// Section resolution is the default, and it must reach more of the corpus than either switch:
	// twelve headed turns per document are twelve ranked units, not one.
	sections := search(false, false)
	if len(sections.Results) <= len(single.Results) {
		t.Fatalf("section resolution returned %d results, document resolution returned %d; want more",
			len(sections.Results), len(single.Results))
	}
	type span struct {
		path       string
		start, end int
	}
	sectionSpans := map[span]bool{}
	for _, result := range sections.Results {
		sectionSpans[span{result.FilePath, result.StartLine, result.EndLine}] = true
	}
	if len(sectionSpans) != len(sections.Results) {
		t.Fatalf("section resolution returned %d results over %d distinct spans",
			len(sections.Results), len(sectionSpans))
	}
	if sections.Stats.ResultBytes > sections.Stats.ContextBudgetBytes {
		t.Fatalf("section resolution breached the budget: %d > %d",
			sections.Stats.ResultBytes, sections.Stats.ContextBudgetBytes)
	}
	if len(multi.Results) <= len(single.Results) {
		t.Fatalf("multi-resolution returned %d results, single returned %d; want more",
			len(multi.Results), len(single.Results))
	}
	multiFiles := map[string]bool{}
	for _, result := range multi.Results {
		multiFiles[result.FilePath] = true
	}
	for path := range files {
		if !multiFiles[path] {
			t.Fatalf("multi-resolution dropped file %s", path)
		}
	}
	if multi.Stats.ResultBytes > multi.Stats.ContextBudgetBytes {
		t.Fatalf("multi-resolution breached the budget: %d > %d",
			multi.Stats.ResultBytes, multi.Stats.ContextBudgetBytes)
	}
}
