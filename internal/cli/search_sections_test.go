package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func sectionedSearchResponse() sem.SearchResponse {
	return sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "src/routing/mod.rs", StartLine: 10, EndLine: 14, FocusLine: 10, Score: 21.0,
			QualifiedName: "Router.merge", Signals: []string{"body"}, Snippet: "fn merge() {\n\tmerge_inner()\n}"},
		{Rank: 2, FilePath: "CHANGELOG.md", StartLine: 99, EndLine: 99, FocusLine: 99, Score: 19.5,
			SymbolName: "0.6.13", Signals: []string{"body", "doc-prior"}, Section: sem.SearchSectionDocs,
			Snippet: "## 0.6.13"},
		{Rank: 3, FilePath: "src/routing/path.rs", StartLine: 20, EndLine: 30, FocusLine: 22, Score: 18.0,
			QualifiedName: "PathRouter.merge", Signals: []string{"body"}, Snippet: "fn merge() {\n\t// window\n}"},
		{Rank: 4, FilePath: "Cargo.toml", StartLine: 38, EndLine: 38, FocusLine: 38, Score: 12.0,
			SymbolName: "axum", Signals: []string{"body", "data-prior"}, Section: sem.SearchSectionDocs,
			Snippet: "axum = \"0.6\""},
		{Rank: 5, FilePath: "src/routing/path.rs", StartLine: 40, EndLine: 44, FocusLine: 41, Score: 9.0,
			QualifiedName: "PathRouter.set_fallback", Signals: []string{"related:sibling"},
			Section: sem.SearchSectionRelated, Snippet: "    fn set_fallback(&mut self) {"},
		{Rank: 6, FilePath: "src/routing/mod.rs", StartLine: 80, EndLine: 92, FocusLine: 84, Score: 0,
			QualifiedName: "Router.fallback", Signals: []string{"related:caller"},
			Section: sem.SearchSectionRelated, Snippet: "    fn fallback(self) -> Self {"},
	}}
}

// TestWriteTextSearchGroupsSections pins the whole point of Change 2 for the default renderer: the
// list an agent reads as "candidate fix sites" contains only entries that could be one, the other
// groups are labelled, and nothing is dropped.
func TestWriteTextSearchGroupsSections(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, sectionedSearchResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	relatedAt := strings.Index(out, searchTextRelatedHeader)
	docsAt := strings.Index(out, searchTextDocsHeader)
	if relatedAt < 0 || docsAt < 0 {
		t.Fatalf("section headers missing:\n%s", out)
	}
	if docsAt < relatedAt {
		t.Fatalf("docs section must come after the related-site block:\n%s", out)
	}
	primary := out[:relatedAt]
	for _, path := range []string{"CHANGELOG.md", "Cargo.toml"} {
		if strings.Contains(primary, path) {
			t.Fatalf("%s is presented as a candidate fix site:\n%s", path, out)
		}
	}
	// Nothing is suppressed: both non-code hits are still there, under their own label, with the
	// ranks the payload gave them, so a caller can still reach them.
	docs := out[docsAt:]
	for _, want := range []string{"2. CHANGELOG.md:99", "4. Cargo.toml:38"} {
		if !strings.Contains(docs, want) {
			t.Fatalf("docs section lost %q:\n%s", want, out)
		}
	}
	// A related site is one line: rank, file:line, symbol, and WHY it is related.
	related := out[relatedAt:docsAt]
	for _, want := range []string{
		"5. src/routing/path.rs:41 symbol=PathRouter.set_fallback (sibling)\n",
		"6. src/routing/mod.rs:84 symbol=Router.fallback (caller)\n",
	} {
		if !strings.Contains(related, want) {
			t.Fatalf("related block missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(related, "fn set_fallback(&mut self) {\n\n") {
		t.Fatalf("related block printed a full snippet block:\n%s", out)
	}
}

// TestWriteTextSearchTiersByPositionNotAbsoluteRank pins the interaction between the two changes:
// a non-code hit that outranks the code is sectioned away, and charging its rank against the
// full-snippet tier would leave the agent one full window short for no reason.
func TestWriteTextSearchTiersByPositionNotAbsoluteRank(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, sectionedSearchResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "merge_inner()") {
		t.Fatalf("first fix-site candidate lost its snippet:\n%s", out)
	}
	if !strings.Contains(out, "// window") {
		t.Fatalf("second fix-site candidate lost its snippet because a docs hit consumed rank 2:\n%s", out)
	}
}

// TestWriteTextSearchKeepsNonCodeOnlyPayloadUnsectioned pins the guard at the renderer too: when
// the sem layer reports no primary hits there is no fix-site list to protect, and a page with an
// empty primary section plus a "not fix sites" footer would be strictly worse than the ranking.
func TestWriteTextSearchKeepsNonCodeOnlyPayloadUnsectioned(t *testing.T) {
	t.Parallel()
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{Rank: 1, FilePath: "caddytest/integration/adapt/global_options_default_bind.txt", StartLine: 1, EndLine: 3,
			FocusLine: 1, Score: 39.5, Signals: []string{"body"}, Snippet: "bind 127.0.0.1"},
		{Rank: 2, FilePath: "doc/rules/casing/constant_case.rst", StartLine: 5, EndLine: 7, FocusLine: 5,
			Score: 53.0, Signals: []string{"body"}, Snippet: "constant_case"},
	}}
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, searchTextDocsHeader) {
		t.Fatalf("non-code-only payload was sectioned away:\n%s", out)
	}
	if !strings.Contains(out, "bind 127.0.0.1") {
		t.Fatalf("non-code rank 1 lost its snippet:\n%s", out)
	}
}

// TestOrderAgentSearchResultsGroupsAndTags pins the agent format's version of sectioning: the byte
// cap leaves no room for group headers, so the label rides on each block and grouping is order.
func TestOrderAgentSearchResultsGroupsAndTags(t *testing.T) {
	t.Parallel()
	ordered := orderAgentSearchResults(sectionedSearchResponse().Results)
	var sections []string
	for _, result := range ordered {
		switch result.Section {
		case sem.SearchSectionRelated:
			sections = append(sections, "related")
		case sem.SearchSectionDocs:
			sections = append(sections, "docs")
		default:
			sections = append(sections, "primary")
		}
	}
	want := "primary,primary,related,related,docs,docs"
	if strings.Join(sections, ",") != want {
		t.Fatalf("agent order = %s, want %s", strings.Join(sections, ","), want)
	}
	cases := map[string]string{"related": "(sibling)", "docs": "(docs)", "primary": ""}
	for _, result := range ordered {
		var key string
		switch result.Section {
		case sem.SearchSectionRelated:
			key = "related"
		case sem.SearchSectionDocs:
			key = "docs"
		default:
			key = "primary"
		}
		tag := agentSearchSectionTag(result)
		if key == "primary" && tag != cases[key] {
			t.Fatalf("primary block carries tag %q: the common case must cost zero bytes", tag)
		}
		if key == "docs" && tag != "(docs)" {
			t.Fatalf("docs block tag = %q, want (docs)", tag)
		}
		if key == "related" && !strings.HasPrefix(tag, "(") {
			t.Fatalf("related block tag = %q, want a reason in parentheses", tag)
		}
	}
}

func TestAgentSearchBlockKeepsSectionTagWithinCap(t *testing.T) {
	t.Parallel()
	result := sem.SearchResult{
		Rank: 5, FilePath: "src/routing/path.rs", StartLine: 40, EndLine: 44, FocusLine: 41,
		QualifiedName: "PathRouter.set_fallback", Signals: []string{"related:sibling"},
		Section: sem.SearchSectionRelated, Snippet: "    fn set_fallback(&mut self) {",
	}
	block := agentSearchBlock(result, 0)
	if !strings.Contains(string(block), "(sibling)") {
		t.Fatalf("agent block lost its section tag: %q", block)
	}
	for _, cap := range []int{16, 24, 40, 64} {
		tight := agentSearchBlock(result, cap)
		if len(tight) > cap {
			t.Fatalf("agent block used %d bytes, cap %d: %q", len(tight), cap, tight)
		}
	}
}

// TestOrderAgentSearchResultsIsIdentityWithoutSections keeps the ordinary path allocation-free and
// byte-identical to before, so the agent format only changes when a payload actually has sections.
func TestOrderAgentSearchResultsIsIdentityWithoutSections(t *testing.T) {
	t.Parallel()
	results := []sem.SearchResult{
		{Rank: 1, FilePath: "a.go"}, {Rank: 2, FilePath: "b.go"},
	}
	ordered := orderAgentSearchResults(results)
	if len(ordered) != len(results) || ordered[0].FilePath != "a.go" || ordered[1].FilePath != "b.go" {
		t.Fatalf("ordering changed a payload with no sections: %+v", ordered)
	}
}
