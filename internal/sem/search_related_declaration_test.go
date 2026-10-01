package sem

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func relatedDeclarationFixture() relatedFixture {
	anchor := relatedMethod("alpha", "alphaHandler", "Handler.alphaHandler", "src/Handler.java", "Handler", 3, 8)
	anchor.nameLine = 4
	twin := relatedMethod("beta", "betaHandler", "Handler.betaHandler", "src/Handler.java", "Handler", 10, 15)
	twin.nameLine = 11
	return relatedFixture{
		files: []relatedFile{{path: "src/Handler.java", lines: []string{
			"class Handler {",
			"",
			"  @Test",
			"  public void alphaHandler() {",
			"    String message = \"alphaHandler() appears in a body string\";",
			"    validate(message);",
			"    assertEquals(\"alphaHandler\", message);",
			"  }",
			"",
			"  @Test",
			"  public void betaHandler() {",
			"    String message = \"betaHandler() appears in a body string\";",
			"    alphaHandler();",
			"    assertEquals(\"betaHandler\", message);",
			"  }",
			"}",
		}}},
		symbols:   []SymbolRecord{anchor, twin},
		relations: []RelationRecord{relatedEdge("SIMILAR_TO", "alpha", "beta")},
	}
}

// Both symbol-oriented producers must point at the declaration's name, not the annotation.
// Missing or out-of-span metadata retains the old start-line fallback without searching the body.
func TestSearchRelatedDeclarationLocators(t *testing.T) {
	for _, kind := range []searchRelatedKind{searchRelatedNearDupe, searchRelatedSibling} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				nameLine int
				endLine  int
				wantLine int
				wantText string
			}{
				{"annotated declaration", 11, 15, 11, "  public void betaHandler() {"},
				{"name on final line", 11, 11, 11, "  public void betaHandler() {}"},
				{"missing metadata", 0, 15, 10, "  @Test"},
				{"negative metadata", -1, 15, 10, "  @Test"},
				{"metadata before symbol", 9, 15, 10, "  @Test"},
				{"metadata after symbol", 16, 15, 10, "  @Test"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					fixture := relatedDeclarationFixture()
					fixture.symbols[1].nameLine = tc.nameLine
					fixture.symbols[1].EndLine = tc.endLine
					if tc.endLine == 11 {
						fixture.files[0].lines[10] = "  public void betaHandler() {}"
						fixture.files[0].lines = append(fixture.files[0].lines[:11], "}")
					}
					byID, byFile := fixture.indexes()
					anchor := fixture.symbol(t, "alpha")
					query := buildSearchQuery("test handler")
					var sites []searchRelatedSite
					if kind == searchRelatedNearDupe {
						sites = searchRelatedNearDuplicateSites(anchor, query, fixture.relations, byID, byFile, nil, nil)
					} else {
						sites = searchRelatedSiblingSites(anchor, query, fixture.relations, byID, byFile, nil, nil)
					}
					if len(sites) != 1 || sites[0].symbol.ID != "beta" || sites[0].kind != kind {
						t.Fatalf("related sites = %+v, want one %s beta site", sites, kind)
					}
					if sites[0].line != tc.wantLine {
						t.Errorf("site line = %d, want %d", sites[0].line, tc.wantLine)
					}
					result, ok := searchRelatedResult(sites[0], fixture.files[0].lines)
					if !ok {
						t.Fatal("related declaration was not rendered")
					}
					if result.StartLine != 10 || result.EndLine != tc.endLine ||
						result.FocusLine != tc.wantLine || result.SnippetStartLine != tc.wantLine ||
						result.SnippetEndLine != tc.wantLine || result.Snippet != tc.wantText {
						t.Fatalf("related declaration = %+v, want span 10-%d and line %d %q", result, tc.endLine, tc.wantLine, tc.wantText)
					}
					if result.Section != searchSectionRelated || len(result.Signals) != 1 ||
						result.Signals[0] != "related:"+string(kind) || result.SymbolID != "" ||
						result.Language != "" || result.SymbolNameLine != 0 || len(result.Passages) != 0 {
						t.Fatalf("related declaration expanded beyond a one-line locator: %+v", result)
					}
				})
			}
		})
	}
}

// Name metadata belongs to the declaration; it must not replace explicit call-site evidence.
func TestSearchRelatedDeclarationKeepsCallerEvidenceLine(t *testing.T) {
	fixture := relatedDeclarationFixture()
	byID, _ := fixture.indexes()
	relation := relatedEdge("CALLS", "beta", "alpha")
	relation.Evidence = []Evidence{{Kind: "call", FilePath: "src/Handler.java", StartLine: 13}}
	sites := searchRelatedCallerSites(fixture.symbol(t, "alpha"), buildSearchQuery("test handler"), []RelationRecord{relation}, byID)
	if len(sites) != 1 || sites[0].line != 13 {
		t.Fatalf("caller sites = %+v, want evidence line 13", sites)
	}
	result, ok := searchRelatedResult(sites[0], fixture.files[0].lines)
	if !ok || result.StartLine != 10 || result.EndLine != 15 || result.FocusLine != 13 ||
		result.SnippetStartLine != 13 || result.SnippetEndLine != 13 || result.Snippet != "    alphaHandler();" {
		t.Fatalf("caller result = %+v (ok=%v), want call at line 13", result, ok)
	}
	relation.Evidence = nil
	sites = searchRelatedCallerSites(fixture.symbol(t, "alpha"), buildSearchQuery("test handler"), []RelationRecord{relation}, byID)
	if len(sites) != 1 || sites[0].line != 10 {
		t.Fatalf("caller without evidence = %+v, want unchanged start-line fallback 10", sites)
	}
}

// The pre-render filter must compare the declaration locator, not the annotation above it.
func TestSearchRelatedDeclarationAlreadySurfaced(t *testing.T) {
	for _, tc := range []struct {
		name        string
		visibleLine int
		wantSites   int
	}{
		{"annotation alone does not hide declaration", 10, 1},
		{"visible declaration suppresses duplicate", 11, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := relatedDeclarationFixture()
			results := []SearchResult{{FilePath: "src/Handler.java", SnippetStartLine: tc.visibleLine, SnippetEndLine: tc.visibleLine}}
			sites := selectFixtureSites(t, fixture, "alpha", "test handler", results, 1)
			if len(sites) != tc.wantSites {
				t.Fatalf("sites = %+v, want %d with visible line %d", sites, tc.wantSites, tc.visibleLine)
			}
			if len(sites) == 1 && (sites[0].kind != searchRelatedNearDupe || sites[0].line != 11) {
				t.Fatalf("site = %+v, want near-dupe declaration at line 11", sites[0])
			}
		})
	}
}

func TestSearchRelatedDeclarationOutsideSourceFallsBack(t *testing.T) {
	fixture := relatedDeclarationFixture()
	fixture.symbols[1].EndLine = 30
	fixture.symbols[1].nameLine = 20
	byID, byFile := fixture.indexes()
	sites := searchRelatedNearDuplicateSites(fixture.symbol(t, "alpha"), buildSearchQuery("test handler"), fixture.relations, byID, byFile, nil, nil)
	if len(sites) != 1 {
		t.Fatalf("sites = %+v, want one near-duplicate", sites)
	}
	result, ok := searchRelatedResult(sites[0], fixture.files[0].lines)
	if !ok || result.StartLine != 10 || result.EndLine != 16 || result.FocusLine != 10 ||
		result.SnippetStartLine != 10 || result.SnippetEndLine != 10 || result.Snippet != "  @Test" {
		t.Fatalf("result = %+v (ok=%v), want clamped span 10-16 and fallback line 10", result, ok)
	}
}

// A declaration can be much longer than its annotation; charge its actual serialized bytes.
func TestSearchRelatedDeclarationMergeBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tailBytes int
		wantSites int
	}{
		{"funded declaration", 4000, 1},
		{"annotation fits but declaration does not", 400, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := relatedDeclarationFixture()
			declaration := "  public void betaHandler(String " + strings.Repeat("界", 400) + ") {"
			fixture.files[0].lines[10] = declaration
			byID, byFile := fixture.indexes()
			sites := searchRelatedNearDuplicateSites(fixture.symbol(t, "alpha"), buildSearchQuery("test handler"), fixture.relations, byID, byFile, nil, nil)
			results := mergeTestResults(6, "head source")
			results[5].FilePath = results[0].FilePath
			results[5].Snippet = strings.Repeat("x", tc.tailBytes)
			before, err := json.Marshal(results)
			if err != nil {
				t.Fatal(err)
			}
			budget := len(before)
			merged, count := mergeSearchRelatedSites(results, sites, fixture.reader(), budget, len(results))
			if count != tc.wantSites {
				t.Fatalf("merged %d related sites, want %d", count, tc.wantSites)
			}
			after, err := json.Marshal(merged)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) > budget || len(merged) > len(results) {
				t.Fatalf("merge exceeded baseline: bytes %d > %d or results %d > %d", len(after), budget, len(merged), len(results))
			}
			if count == 0 && !reflect.DeepEqual(merged, results) {
				t.Fatal("unfunded declaration changed the ranking")
			}
			for _, result := range merged {
				if result.Section == searchSectionRelated && (result.FocusLine != 11 || result.Snippet != declaration) {
					t.Fatalf("funded locator does not render the declaration: %+v", result)
				}
			}
		})
	}
}
