package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// TestWriteTextSearchPinsTheDocumentedSectionOrder is the integration's order contract.
//
// Four independently developed features each append a block to the same payload, and each branch
// chose its own position for its own block in isolation — so on merge the order was neither stable
// nor documented (the two reference blocks about hit 1 ended up split apart by the related and docs
// groups). The order below is the one written down in internal/sem/search_blocks.go, and it is a
// READING order: navigate, edit, check the goal, check the contract, check the names, check the
// neighbours. This test is what stops the next feature from quietly inserting itself anywhere.
func TestWriteTextSearchPinsTheDocumentedSectionOrder(t *testing.T) {
	t.Parallel()
	response := contractSearchResponse()
	response.ContainerMap = &sem.SearchContainerMap{
		FilePath: "tsdb/head_append.go", FileLines: 400, Kind: "struct", Name: "memSeries",
		StartLine: 5, EndLine: 380, AnchorRank: 1,
		Members: []sem.SearchContainerMember{
			{Name: "appendPreprocessor", StartLine: 10, EndLine: 20, Params: "()", Hit: true},
			{Name: "cut", StartLine: 40, EndLine: 44, Params: "()"},
		},
	}
	response.SignatureTypes = []sem.SearchSignatureType{{
		Name: "memChunk", FilePath: "tsdb/head.go", StartLine: 2107,
		Fields: []string{"prev *memChunk"}, FieldsTotal: 1,
		Methods: []string{"len() int"}, MethodsTotal: 1,
	}}

	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// The documented order, top to bottom. Every one of these blocks comes from a different
	// branch; the point of the test is that they coexist in exactly this sequence.
	sections := []struct {
		label  string
		marker string
	}{
		{"CONTAINER MAP", "CONTAINER MAP tsdb/head_append.go"},
		{"candidate fix sites", "1. tsdb/head_append.go:"},
		{"COVERING TEST", searchTextTestHeader},
		{"TYPES IN THIS SIGNATURE", searchTextSignatureTypesHeader},
		{"DECLARATIONS", searchTextTypesHeader},
		{"RELATED SITES", searchTextRelatedHeader},
		{"DOCS & FIXTURES", searchTextDocsHeader},
	}
	previous := -1
	for _, section := range sections {
		at := strings.Index(out, section.marker)
		if at < 0 {
			t.Fatalf("section %s is missing from the payload:\n%s", section.label, out)
		}
		if at <= previous {
			t.Fatalf("section %s is out of order (at %d, previous ended %d):\n%s",
				section.label, at, previous, out)
		}
		previous = at
	}

	// The two reference blocks must be ADJACENT: nothing may be inserted between the signature
	// types and the declaration card, because both are about hit 1 and splitting them makes the
	// reader hold hit 1 across an unrelated group.
	between := out[strings.Index(out, searchTextSignatureTypesHeader):strings.Index(out, searchTextTypesHeader)]
	if strings.Contains(between, searchTextRelatedHeader) || strings.Contains(between, searchTextDocsHeader) {
		t.Fatalf("a group was inserted between the two reference blocks:\n%s", between)
	}
}

// TestWriteTextSearchOmitsEverySectionItDoesNotHave pins that a bare payload pays nothing for any of
// the four blocks: no headers, no placeholders. A section that costs bytes when it has no content is
// a section that taxes every query in the repo.
func TestWriteTextSearchOmitsEverySectionItDoesNotHave(t *testing.T) {
	t.Parallel()
	response := contractSearchResponse()
	response.Results = response.Results[:2]
	response.TypeCard = nil
	response.SignatureTypes = nil
	response.ContainerMap = nil

	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, header := range []string{
		"CONTAINER MAP", searchTextTestHeader, searchTextSignatureTypesHeader,
		searchTextTypesHeader, searchTextRelatedHeader, searchTextDocsHeader,
	} {
		if strings.Contains(out, header) {
			t.Fatalf("an absent section still printed %q:\n%s", header, out)
		}
	}
}
