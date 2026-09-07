package sem

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const yamlMultilineQuotedScalarFixture = `apiVersion: example.test/v1
kind: Example
metadata:
  name: sample
spec:
  rule: 'enabled(self.mode) ? self.mode ==
    ''Active'' : false'
`

// Independently authored reduction of partial-failure index 83. The original
// source is Kubernetes standard-install.yaml at revision
// b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe, SHA-256
// 558218a3e7f5a6c9f0d1d8c26e3519c5fdc3955e4a398b2c61cc2066a2034e78.
// Its four occurrences use the same YAML multiline-single-quoted-scalar shape;
// this fixture is newly written and contains none of the original field text.
func TestYAMLMultilineSingleQuotedScalarColonIsNotMappingKey(t *testing.T) {
	source := yamlMultilineQuotedScalarFixture

	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(treeSitterLanguages[".yaml"].grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if root := tree.RootNode(); root == nil || root.HasError() {
		t.Fatalf("independently authored YAML is not a clean grammar fixture: %s", parseErrorDetail(root, []byte(source)))
	}
	tree.Close()

	masked := maskYAMLUnsupportedSyntax(source)
	if masked != source {
		t.Fatalf("multiline scalar continuation was mistaken for a mapping key:\nsource=%q\nmasked=%q", source, masked)
	}
	_, language, status := TreeSitterParser{}.ParseWithStatus("example.yaml", source)
	if language != "YAML" || status.ParseError {
		t.Fatalf("public parse = language %q status %+v", language, status)
	}
}

func TestYAMLQuotedMappingKeyMaskPreservesCoordinates(t *testing.T) {
	source := "navigation:\n  - 'A very long, grammar-sensitive quoted key': target.md\n  - \"An escaped \\\"quoted\\\" key\": other.md\n"
	masked := maskYAMLUnsupportedSyntax(source)
	if len(masked) != len(source) || strings.Count(masked, "\n") != strings.Count(source, "\n") {
		t.Fatalf("quoted-key parse view changed source coordinates:\nsource=%q\nmasked=%q", source, masked)
	}
	if masked == source {
		t.Fatal("fixture did not exercise quoted mapping-key compatibility")
	}
	_, _, status := TreeSitterParser{}.ParseWithStatus("navigation.yaml", source)
	if status.ParseError {
		t.Fatalf("quoted mapping key remains unparseable: %+v", status)
	}
}

func TestYAMLQuotedMappingKeyMaskPreservesLongSequenceEntries(t *testing.T) {
	source := "navigation:\n" +
		"  - '" + strings.Repeat("SINGLE_KEY_SEGMENT, ", 18) + "tail': single.md\n" +
		"  - \"" + strings.Repeat("DOUBLE_KEY_SEGMENT, ", 18) + "tail\": double.md\n"
	masked := maskYAMLUnsupportedSyntax(source)
	if len(masked) != len(source) || strings.Count(masked, "\n") != strings.Count(source, "\n") {
		t.Fatalf("long sequence mapping keys changed coordinates:\nsource=%q\nmasked=%q", source, masked)
	}
	if strings.Contains(masked, "SINGLE_KEY_SEGMENT") || strings.Contains(masked, "DOUBLE_KEY_SEGMENT") {
		t.Fatalf("long quoted key contents survived parse-view mask: %q", masked)
	}
	_, language, status := TreeSitterParser{}.ParseWithStatus("navigation.yaml", source)
	if language != "YAML" || status.ParseError {
		t.Fatalf("long quoted sequence keys = language %q status %+v", language, status)
	}
}

func TestYAMLQuotedMappingKeyExtractionUsesAuthoredRangesAndCacheParity(t *testing.T) {
	longSingleKey := strings.Repeat("SINGLE_KEY_SEGMENT, ", 18) + "tail"
	longDoubleKey := strings.Repeat("DOUBLE_KEY_SEGMENT, ", 18) + "tail"
	source := "navigation:\n" +
		"  - '" + longSingleKey + "': target.md\n" +
		"  - \"" + longDoubleKey + "\": other.md\n" +
		"after:\n" +
		"  child: value"

	language, ok := languageForPath("navigation.yaml")
	if !ok {
		t.Fatal("YAML language unavailable")
	}
	spec := resolveProfile(ProfileFull)
	cache := &extractionCache{
		ctx:         context.Background(),
		directory:   t.TempDir(),
		repository:  "fixture/yaml",
		build:       "fixture-build",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
	sourceFile := captureSource("navigation.yaml", source)
	cold, hit := cache.extract(spec, language, sourceFile, defaultMaxParseBytes)
	if hit || cold.status.ParseError {
		t.Fatalf("cold extraction = hit %v status %+v", hit, cold.status)
	}
	if len(cold.entities) != 2 {
		t.Fatalf("cold entities = %#v, want navigation and following top-level entity", cold.entities)
	}
	wantNavigation := "navigation:\n" +
		"  - '" + longSingleKey + "': target.md\n" +
		"  - \"" + longDoubleKey + "\": other.md"
	want := map[string]struct {
		startLine int
		endLine   int
		signature string
		bodyHash  string
	}{
		"navigation": {1, 3, "section navigation", hash(normalize(wantNavigation))},
		"after":      {4, 5, "section after", hash(normalize("after:\n  child: value"))},
	}
	for _, entity := range cold.entities {
		expected, ok := want[entity.Name]
		if !ok {
			t.Fatalf("unexpected authored YAML entity: %#v", entity)
		}
		if entity.StartLine != expected.startLine || entity.EndLine != expected.endLine ||
			entity.Signature != expected.signature || entity.BodyHash != expected.bodyHash {
			t.Fatalf("authored entity %q = %#v, want lines %d-%d signature %q body %q", entity.Name, entity,
				expected.startLine, expected.endLine, expected.signature, expected.bodyHash)
		}
		if entity.sourceStartByte != 0 || entity.sourceEndByte != 0 {
			t.Fatalf("YAML entity %q unexpectedly acquired parse-view byte positions: %#v", entity.Name, entity)
		}
	}
	cache.flush()
	warm, hit := cache.extract(spec, language, sourceFile, defaultMaxParseBytes)
	if !hit || !reflect.DeepEqual(cold, warm) {
		t.Fatalf("warm extraction = hit %v value %#v, want cold %#v", hit, warm, cold)
	}
	cache.flush()
}

func TestYAMLQuotedMappingKeyMaskLeavesMalformedInputVisible(t *testing.T) {
	source := "navigation:\n  - \"unterminated quoted key: target.md\n"
	if masked := maskYAMLUnsupportedSyntax(source); masked != source {
		t.Fatalf("malformed quoted input was rewritten: %q", masked)
	}
	_, _, status := TreeSitterParser{}.ParseWithStatus("broken.yaml", source)
	if !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("malformed quoted input status = %+v, want E_PARSE_ERROR", status)
	}
}

func TestYAMLMultilineSingleQuotedScalarExtractionCacheParity(t *testing.T) {
	cache := &extractionCache{
		ctx:         context.Background(),
		directory:   t.TempDir(),
		repository:  "fixture/yaml",
		build:       "fixture-build",
		maxBytes:    extractionDiskLimit,
		maxEntries:  extractionEntryLimit,
		limitsReady: true,
	}
	language, ok := languageForPath("example.yaml")
	if !ok {
		t.Fatal("YAML language unavailable")
	}
	source := captureSource("example.yaml", yamlMultilineQuotedScalarFixture)
	spec := resolveProfile(ProfileFull)
	cold, hit := cache.extract(spec, language, source, defaultMaxParseBytes)
	if hit || cold.status.ParseError {
		t.Fatalf("cold extraction = hit %v status %+v", hit, cold.status)
	}
	cache.flush()
	warm, hit := cache.extract(spec, language, source, defaultMaxParseBytes)
	if !hit || !reflect.DeepEqual(cold, warm) {
		t.Fatalf("warm extraction = hit %v value %#v, want cold %#v", hit, warm, cold)
	}
	cache.flush()
	for _, entity := range warm.entities {
		if strings.Contains(entity.Name, "key") || strings.Contains(entity.Signature, "key") {
			t.Fatalf("private quoted-key replacement leaked into cached entity: %#v", entity)
		}
	}
}
