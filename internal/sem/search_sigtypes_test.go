package sem

import (
	"strings"
	"testing"
)

func TestSearchSignatureTypeNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		signature string
		own       string
		owner     string
		want      []string
	}{
		{
			name:      "rust associated function names its params and Self",
			signature: "pub const fn deletion(start: TextSize, end: TextSize) -> Self",
			own:       "deletion",
			owner:     "Edit",
			want:      []string{"Edit", "TextSize"},
		},
		{
			name:      "go method",
			signature: "func (f *Fix) Apply(edits []Edit, locator Locator) error",
			own:       "Apply",
			owner:     "Fix",
			want:      []string{"Fix", "Edit", "Locator"},
		},
		{
			name:      "modifiers before the name are not types",
			signature: "public static async Task<Edit> BuildAsync(SourceFile file)",
			own:       "BuildAsync",
			owner:     "Builder",
			want:      []string{"Builder", "SourceFile"},
		},
		{
			name:      "lowercase parameter names are never types",
			signature: "def apply(self, edit, locator)",
			own:       "apply",
			owner:     "Fixer",
			want:      []string{"Fixer"},
		},
		{
			name:      "no owner and no types",
			signature: "fn main()",
			own:       "main",
			owner:     "",
			want:      nil,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := searchSignatureTypeNames(testCase.signature, testCase.own, testCase.owner)
			if strings.Join(got, ",") != strings.Join(testCase.want, ",") {
				t.Errorf("searchSignatureTypeNames(%q) = %v, want %v", testCase.signature, got, testCase.want)
			}
		})
	}
}

// sigTypesFixture is the reproduction shape: the located symbol is
// `Edit::range_replacement`, and the type in its own signature — `Edit`, via
// `Self` — carries the constructor the caller actually needs.
func sigTypesFixture() (SymbolRecord, map[string]SymbolRecord, map[string][]SymbolRecord, []RelationRecord) {
	symbol := func(id, kind, name, qualified string, line int, container, signature string) SymbolRecord {
		return SymbolRecord{
			ID: id, Kind: kind, Name: name, QualifiedName: qualified, FilePath: "src/edit.rs",
			StartLine: line, EndLine: line + 2, ContainerID: container, Signature: signature,
			Language: "Rust",
		}
	}
	symbols := []SymbolRecord{
		symbol("edit", "struct", "Edit", "Edit", 12, "", "pub struct Edit"),
		symbol("edit.range", "field", "range", "Edit.range", 14, "edit", "range TextRange"),
		symbol("edit.deletion", "method", "deletion", "Edit.deletion", 22, "edit",
			"pub const fn deletion(start: TextSize, end: TextSize) -> Self"),
		symbol("edit.range_replacement", "method", "range_replacement", "Edit.range_replacement", 41, "edit",
			"pub fn range_replacement(content: String, range: TextRange) -> Self"),
		symbol("fix", "struct", "Fix", "Fix", 80, "", "pub struct Fix"),
		symbol("fix.edits", "field", "edits", "Fix.edits", 82, "fix", "edits Box<[Edit]>"),
	}
	byID := map[string]SymbolRecord{}
	for _, record := range symbols {
		byID[record.ID] = record
	}
	byFile := map[string][]SymbolRecord{"src/edit.rs": symbols}
	relations := []RelationRecord{
		{RecordType: "relation", FromID: "edit", ToID: "edit.range", Type: "CONTAINS", RelationScope: "file"},
		{RecordType: "relation", FromID: "edit", ToID: "edit.deletion", Type: "CONTAINS", RelationScope: "file"},
	}
	return byID["edit.range_replacement"], byID, byFile, relations
}

func TestSearchSignatureTypeSurfaceResolvesTheDeclaringType(t *testing.T) {
	t.Parallel()
	anchor, byID, byFile, relations := sigTypesFixture()
	block := searchSignatureTypeSurface(anchor, byID, byFile, relations, searchSignatureTypeLimit)
	if len(block) == 0 {
		t.Fatal("no signature-type entries")
	}
	if block[0].Name != "Edit" || block[0].FilePath != "src/edit.rs" || block[0].StartLine != 12 {
		t.Fatalf("first entry = %#v, want the declaring type Edit at src/edit.rs:12", block[0])
	}
	if strings.Join(block[0].Fields, ",") != "range: TextRange" {
		t.Errorf("fields = %v", block[0].Fields)
	}
	joined := strings.Join(block[0].Methods, " ")
	if !strings.Contains(joined, "deletion(start: TextSize, end: TextSize)") {
		t.Errorf("the constructor the caller needs is missing: %q", joined)
	}
	// Bodies are never included, and a different type's members never appear.
	for _, entry := range block {
		for _, member := range append(entry.Fields, entry.Methods...) {
			if strings.Contains(member, "{") || strings.Contains(member, "edits") {
				t.Errorf("entry %q leaked a body or another type's member: %q", entry.Name, member)
			}
		}
	}
}

func TestSearchSignatureTypeSurfaceSkipsAmbiguousNames(t *testing.T) {
	t.Parallel()
	anchor, byID, byFile, relations := sigTypesFixture()
	// A second, unrelated declaration of the same name in another file makes the
	// short name ambiguous. Pointing at the wrong declaration is worse than
	// pointing at none, so the entry must be dropped.
	twin := byID["edit"]
	twin.ID = "edit-twin"
	twin.FilePath = "vendor/edit.rs"
	byID[twin.ID] = twin
	byFile["vendor/edit.rs"] = []SymbolRecord{twin}
	// The anchor lives in a third file and names `Edit` explicitly, so `Edit` IS
	// a candidate — it must be dropped for being ambiguous, not for being absent
	// from the signature.
	anchorElsewhere := anchor
	anchorElsewhere.FilePath = "src/apply.rs"
	anchorElsewhere.ContainerID = ""
	anchorElsewhere.Signature = "pub fn range_replacement(content: String, range: TextRange) -> Edit"
	byFile["src/apply.rs"] = []SymbolRecord{anchorElsewhere}
	if names := searchSignatureTypeNames(anchorElsewhere.Signature, anchorElsewhere.Name, ""); !containsName(names, "Edit") {
		t.Fatalf("test setup: %v does not offer Edit as a candidate", names)
	}
	block := searchSignatureTypeSurface(anchorElsewhere, byID, byFile, relations, searchSignatureTypeLimit)
	for _, entry := range block {
		if entry.Name == "Edit" {
			t.Errorf("ambiguous name resolved anyway: %#v", entry)
		}
	}
}

func TestFundSearchSignatureTypesNeverGrowsThePayload(t *testing.T) {
	t.Parallel()
	block := []SearchSignatureType{{
		Name: "Edit", Kind: "struct", FilePath: "src/edit.rs", StartLine: 12,
		Fields:  []string{"range: TextRange", "content: Option<Box<str>>"},
		Methods: []string{"deletion(start: TextSize, end: TextSize) -> Self", "insertion(content: String, at: TextSize) -> Self"},
	}}
	cases := []struct {
		name      string
		results   []SearchResult
		wantBlock bool
	}{
		{
			name: "redundant tail locator funds the block",
			results: []SearchResult{
				{Rank: 1, FilePath: "src/edit.rs", Snippet: strings.Repeat("x\n", 40)},
				{Rank: 2, FilePath: "src/fix.rs", Snippet: strings.Repeat("y\n", 40)},
				{Rank: 3, FilePath: "src/apply.rs", Snippet: strings.Repeat("z\n", 40)},
				{Rank: 4, FilePath: "src/lib.rs", Snippet: strings.Repeat("w\n", 40)},
				{Rank: 5, FilePath: "src/noqa.rs", Snippet: strings.Repeat("v\n", 40)},
				{Rank: 6, FilePath: "src/edit.rs", Snippet: strings.Repeat("u\n", 60)},
				{Rank: 7, FilePath: "src/fix.rs", Snippet: strings.Repeat("t\n", 60)},
			},
			wantBlock: true,
		},
		{
			name: "nothing displaceable means no block",
			results: []SearchResult{
				{Rank: 1, FilePath: "src/edit.rs", Snippet: "x"},
				{Rank: 2, FilePath: "src/fix.rs", Snippet: "y"},
			},
			wantBlock: false,
		},
		{
			name:      "no results means no block",
			results:   nil,
			wantBlock: false,
		},
		{
			// The displaceable tail here is TINY: each locator pays for only a
			// fraction of the block. A funder that seated more than it displaced
			// would grow the payload, which the byte assertion below catches.
			name: "a tiny displaceable tail pays for the block a locator at a time",
			results: []SearchResult{
				{Rank: 1, FilePath: "a.rs", Snippet: "1"},
				{Rank: 2, FilePath: "b.rs", Snippet: "2"},
				{Rank: 3, FilePath: "c.rs", Snippet: "3"},
				{Rank: 4, FilePath: "d.rs", Snippet: "4"},
				{Rank: 5, FilePath: "e.rs", Snippet: "5"},
				{Rank: 6, FilePath: "a.rs", Snippet: "6"},
				{Rank: 7, FilePath: "b.rs", Snippet: "7"},
			},
			wantBlock: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			baseline := serializedSearchResultBytes(testCase.results)
			kept, seated := fundSearchSignatureTypes(testCase.results, block, searchEnclosureHeadRanks)
			if testCase.wantBlock != (len(seated) > 0) {
				t.Fatalf("seated block = %v, want %v", len(seated) > 0, testCase.wantBlock)
			}
			total := serializedSearchResultBytes(kept) + serializedSearchResultBytes(seated)
			if len(seated) > 0 && total > baseline {
				t.Errorf("payload grew: %d bytes with the block vs %d without", total, baseline)
			}
			if len(seated) == 0 && serializedSearchResultBytes(kept) != baseline {
				t.Errorf("results were displaced without seating a block")
			}
			// Every file the baseline mentioned must still be mentioned: the block
			// is funded from redundancy, never from recall.
			files := map[string]bool{}
			for _, result := range kept {
				files[result.FilePath] = true
			}
			for _, result := range testCase.results {
				if !files[result.FilePath] {
					t.Errorf("funding dropped the last mention of %s", result.FilePath)
				}
			}
		})
	}
}

func TestShrinkSearchSignatureTypesKeepsTotals(t *testing.T) {
	t.Parallel()
	block := []SearchSignatureType{{
		Name:         "Edit",
		Fields:       []string{"a", "b", "c"},
		Methods:      []string{"m1()", "m2()", "m3()", "m4()"},
		FieldsTotal:  9,
		MethodsTotal: 17,
	}}
	shrunk := shrinkSearchSignatureTypes(block, 2)
	if len(shrunk[0].Methods) != 2 || len(shrunk[0].Fields) != 2 {
		t.Fatalf("shrunk entry = %#v", shrunk[0])
	}
	if shrunk[0].MethodsTotal != 17 || shrunk[0].FieldsTotal != 9 {
		t.Errorf("totals must survive shrinking so the omitted count stays honest: %#v", shrunk[0])
	}
	if len(block[0].Methods) != 4 {
		t.Errorf("shrinking mutated the caller's block")
	}
}
