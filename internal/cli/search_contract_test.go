package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// contractSearchResponse is a payload carrying both contract blocks: a covering-test entry in the
// results and a declaration card beside them.
func contractSearchResponse() sem.SearchResponse {
	return sem.SearchResponse{
		Results: []sem.SearchResult{
			{Rank: 1, FilePath: "tsdb/head_append.go", StartLine: 10, EndLine: 20, FocusLine: 10, Score: 88.5,
				QualifiedName: "memSeries.appendPreprocessor", Signals: []string{"body", "complete-symbol"},
				SnippetStartLine: 10, SnippetEndLine: 12, Snippet: "func (s *memSeries) appendPreprocessor() {\n\tc = s.headChunks\n}"},
			{Rank: 2, FilePath: "tsdb/head.go", StartLine: 40, EndLine: 44, FocusLine: 41, Score: 30.0,
				QualifiedName: "memSeries.cut", Signals: []string{"body"},
				SnippetStartLine: 40, SnippetEndLine: 41, Snippet: "func (s *memSeries) cut() {\n\t// window"},
			{Rank: 3, FilePath: "docs/tsdb.md", StartLine: 5, EndLine: 5, FocusLine: 5, Score: 12.0,
				SymbolName: "Chunks", Signals: []string{"body", "doc-prior"}, Section: sem.SearchSectionDocs,
				SnippetStartLine: 5, SnippetEndLine: 5, Snippet: "## Chunks"},
			{Rank: 4, FilePath: "tsdb/head.go", StartLine: 60, EndLine: 70, FocusLine: 61, Score: 0,
				QualifiedName: "memSeries.append", Signals: []string{"related:caller"},
				Section: sem.SearchSectionRelated, SnippetStartLine: 61, SnippetEndLine: 61,
				Snippet: "\tc, ok := s.appendPreprocessor()"},
			{Rank: 5, FilePath: "tsdb/head_test.go", StartLine: 100, EndLine: 110, FocusLine: 104, Score: 0,
				QualifiedName: "TestAppendPreprocessor", Signals: []string{"related:covering-test"},
				Section: sem.SearchSectionCoveringTest, SnippetStartLine: 103, SnippetEndLine: 104,
				Snippet: "\tc, _, created := s.appendPreprocessor()\n\trequire.True(t, created)"},
		},
		TypeCard: []sem.TypeCardEntry{
			{Name: "memSeries.headChunks", FilePath: "tsdb/head.go", Line: 1971, Decl: "headChunks *memChunk"},
			{Name: "memChunk", FilePath: "tsdb/head.go", Line: 2107, Decl: "type memChunk struct { prev *memChunk }"},
			{Name: "cutoff", FilePath: "tsdb/head_append.go", Line: 12, Decl: "cutoff := rangeFor(t)", UseLines: []int{15}},
		},
	}
}

// TestWriteTextSearchRendersContractBlocks pins that both blocks are LABELLED and that the
// covering test never appears in the list an agent reads as candidate fix sites.
func TestWriteTextSearchRendersContractBlocks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, contractSearchResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	testAt := strings.Index(out, searchTextTestHeader)
	typesAt := strings.Index(out, searchTextTypesHeader)
	relatedAt := strings.Index(out, searchTextRelatedHeader)
	if testAt < 0 || typesAt < 0 || relatedAt < 0 {
		t.Fatalf("a section header is missing:\n%s", out)
	}
	if !(testAt < typesAt && typesAt < relatedAt) {
		t.Fatalf("contract blocks must come after the fix sites and before the related block:\n%s", out)
	}
	if primary := out[:testAt]; strings.Contains(primary, "head_test.go") {
		t.Fatalf("the covering test is presented as a candidate fix site:\n%s", out)
	}
	// The test entry carries its source, so the contract can be read without a second call.
	if !strings.Contains(out, "require.True(t, created)") {
		t.Fatalf("the covering test's assertion is missing:\n%s", out)
	}
	// ... and no relevance score, which it does not have: `score=0.0000` reads as "worthless".
	if strings.Contains(out[testAt:typesAt], "score=") {
		t.Fatalf("the covering test printed a score it does not have:\n%s", out[testAt:typesAt])
	}
	card := out[typesAt:relatedAt]
	for _, want := range []string{
		"memSeries.headChunks tsdb/head.go:1971", "prev *memChunk", "cutoff", "(used 15)",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("declaration card is missing %q:\n%s", want, card)
		}
	}
}

// TestWriteTextSearchOmitsAbsentContractBlocks pins that a payload with neither block pays nothing
// for them: no headers, no blank lines, no placeholder.
func TestWriteTextSearchOmitsAbsentContractBlocks(t *testing.T) {
	t.Parallel()
	response := contractSearchResponse()
	response.TypeCard = nil
	response.Results = response.Results[:4]
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, searchTextTestHeader) || strings.Contains(out, searchTextTypesHeader) {
		t.Fatalf("absent blocks still printed a header:\n%s", out)
	}
}

// TestAgentSearchOrdersCoveringTestAfterFixSites pins the agent format's degradation order: under a
// tight cap the entries that were never fix sites are the ones that go, and the covering test is
// tagged so it can never be read as one.
func TestAgentSearchOrdersCoveringTestAfterFixSites(t *testing.T) {
	t.Parallel()
	ordered := orderAgentSearchResults(contractSearchResponse().Results)
	var sections []string
	for _, result := range ordered {
		sections = append(sections, result.Section)
	}
	want := []string{"", "", sem.SearchSectionCoveringTest, sem.SearchSectionRelated, sem.SearchSectionDocs}
	if len(sections) != len(want) {
		t.Fatalf("sections = %v, want %v", sections, want)
	}
	for index := range want {
		if sections[index] != want[index] {
			t.Fatalf("sections = %v, want %v", sections, want)
		}
	}
	for _, result := range ordered {
		if result.Section != sem.SearchSectionCoveringTest {
			continue
		}
		if tag := agentSearchSectionTag(result); tag != "(covers)" {
			t.Fatalf("covering-test tag = %q, want (covers)", tag)
		}
		// A block with no relevance score must not print one: `s=0.0` reads as "worthless".
		if tag := agentSearchScoreTag(result); tag != "" {
			t.Fatalf("covering-test score tag = %q, want none", tag)
		}
	}
}

// TestWriteAgentSearchDropsTypeCardBeforeResults pins that the card is the first thing the agent
// format gives up: a location an agent cannot otherwise reach outranks a declaration it can.
func TestWriteAgentSearchDropsTypeCardBeforeResults(t *testing.T) {
	t.Parallel()
	response := contractSearchResponse()

	var roomy bytes.Buffer
	if err := writeAgentSearch(&roomy, response, 4096); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(roomy.String(), "D: memChunk tsdb/head.go:2107") {
		t.Fatalf("the card is missing at a budget that can hold it:\n%s", roomy.String())
	}
	if !strings.Contains(roomy.String(), "D: cutoff tsdb/head_append.go:12 used=15") {
		t.Fatalf("a binding entry lost its use lines:\n%s", roomy.String())
	}

	var tight bytes.Buffer
	if err := writeAgentSearch(&tight, response, 420); err != nil {
		t.Fatal(err)
	}
	out := tight.String()
	if len(out) > 420 {
		t.Fatalf("agent payload is %d bytes, over the 420 cap", len(out))
	}
	if strings.Contains(out, "D: ") {
		t.Fatalf("the card survived a cap too tight for the ranking:\n%s", out)
	}
	if !strings.Contains(out, "tsdb/head_append.go") {
		t.Fatalf("the top location was dropped before the card:\n%s", out)
	}
}
