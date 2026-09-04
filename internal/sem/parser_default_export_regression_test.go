package sem

import (
	"regexp"
	"strings"
	"testing"
)

// An anonymous default export is emitted by a raw-source regexp, because
// tree-sitter needs a name node and these values have none. These tests pin the
// three ways that regexp used to describe a module wrongly: a callable reported
// as a container, a source range covering code the symbol does not contain, and
// a symbol for an export that does not exist at all.

// The kind must come from the alternative that matched, not from a containment
// test over the whole matched prefix: an arrow parameter that merely BEGINS with
// "class" is still a function.
func TestJavaScriptDefaultExportKindComesFromMatchedAlternative(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
		want    string
	}{
		{"arrow parameter starting with class", "export default classifier => classifier()\n", "function"},
		{"arrow parameter containing class", "export default (classMap) => classMap.size\n", "function"},
		{"anonymous class", "export default class {\n  run() {}\n}\n", "class"},
		{"anonymous class, no space", "export default class{\n  run() {}\n}\n", "class"},
		{"anonymous function", "export default function (a) {\n  return a\n}\n", "function"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			entities := javascriptDefaultExportEntities("helper.js", testCase.content)
			if len(entities) != 1 {
				t.Fatalf("entities = %d, want 1", len(entities))
			}
			if entities[0].Kind != testCase.want {
				t.Fatalf("kind = %q, want %q", entities[0].Kind, testCase.want)
			}
		})
	}
}

// An expression-bodied arrow has no braced body. Searching the rest of the file
// for a `{` handed the symbol the end line and body hash of whatever came next.
func TestJavaScriptDefaultExportExpressionArrowKeepsItsOwnRange(t *testing.T) {
	content := "export default value => value + 1\n" +
		"\n" +
		"const later = {\n" +
		"  a: 1,\n" +
		"  b: 2,\n" +
		"}\n"
	entities := javascriptDefaultExportEntities("helper.js", content)
	if len(entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(entities))
	}
	if entities[0].StartLine != 1 || entities[0].EndLine != 1 {
		t.Fatalf("range = %d-%d, want 1-1", entities[0].StartLine, entities[0].EndLine)
	}

	// The braced forms must still reach their real closing brace, so the fix is
	// not simply "never look for a body".
	braced := "export default (a) => {\n  return a\n}\n"
	entities = javascriptDefaultExportEntities("helper.js", braced)
	if len(entities) != 1 {
		t.Fatalf("braced entities = %d, want 1", len(entities))
	}
	if entities[0].StartLine != 1 || entities[0].EndLine != 3 {
		t.Fatalf("braced range = %d-%d, want 1-3", entities[0].StartLine, entities[0].EndLine)
	}

	fn := "export default function (a) {\n  return a\n}\n"
	entities = javascriptDefaultExportEntities("helper.js", fn)
	if len(entities) != 1 {
		t.Fatalf("function entities = %d, want 1", len(entities))
	}
	if entities[0].EndLine != 3 {
		t.Fatalf("function end line = %d, want 3", entities[0].EndLine)
	}

	// TypeScript puts a return annotation between the parameter list and the
	// body, so the body is not simply the next non-space byte.
	for _, annotated := range []string{
		"export default function (): Result {\n  return build()\n}\n",
		"export default function (a: string): Promise<Result> {\n  return build(a)\n}\n",
		"export default async function (): Promise<void> {\n  await run()\n}\n",
	} {
		entities = javascriptDefaultExportEntities("helper.ts", annotated)
		if len(entities) != 1 {
			t.Fatalf("annotated entities = %d, want 1 for %q", len(entities), annotated)
		}
		if entities[0].EndLine != 3 {
			t.Fatalf("annotated end line = %d, want 3 for %q", entities[0].EndLine, annotated)
		}
	}
}

// A module that exports nothing must produce no export symbol, however much
// export-shaped text its comments and template literals contain.
func TestJavaScriptDefaultExportIgnoresCommentsAndLiterals(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
	}{
		{"block comment", "/*\nexport default () => {}\n*/\nexport const real = 1\n"},
		{"line comment", "// export default () => {}\nexport const real = 1\n"},
		{"template literal", "const snippet = `\nexport default () => {}\n`\nexport const real = 1\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if entities := javascriptDefaultExportEntities("helper.js", testCase.content); len(entities) != 0 {
				t.Fatalf("entities = %d (%+v), want 0", len(entities), entities)
			}
		})
	}

	// The same file with a real export must still yield exactly one symbol, so
	// the stripping cannot pass by suppressing everything.
	real := "/*\nexport default () => {}\n*/\nexport default function (a) {\n  return a\n}\n"
	entities := javascriptDefaultExportEntities("helper.js", real)
	if len(entities) != 1 {
		t.Fatalf("real entities = %d, want 1", len(entities))
	}
	if entities[0].StartLine != 4 {
		t.Fatalf("real start line = %d, want 4", entities[0].StartLine)
	}
}

var graphqlDocumentNameRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// Every GraphQL name a service boundary reports must appear VERBATIM in the
// document it was read from. A truncated name — `...ViewerFields` read as
// `iewerFields` — names nothing that exists, which is worse than reporting
// nothing at all, and only an exact round-trip can rule it out.
func TestGraphQLBoundaryNamesRoundTripAgainstTheDocument(t *testing.T) {
	for _, document := range []string{
		"query GetFragmentViewer { ...ViewerFields } fragment ViewerFields on Query { viewer { id } }",
		"query GetViewer { ...AFields ...BFields } fragment AFields on Query { viewer { id } } fragment BFields on Query { account { id } }",
		"mutation SaveUser { ...SaveFields } fragment SaveFields on Mutation { updateUser(id: $id) { ok } }",
	} {
		block := "const document = gql`" + document + "`"
		boundaries := serviceBoundaries(SymbolRecord{Language: "TypeScript", Kind: "variable", Name: "document"}, block)
		if len(boundaries) == 0 {
			t.Fatalf("no boundaries for %q", document)
		}
		present := map[string]bool{}
		for _, name := range graphqlDocumentNameRe.FindAllString(document, -1) {
			present[name] = true
		}
		for _, boundary := range boundaries {
			for _, word := range strings.Fields(boundary.Name) {
				if !present[word] {
					t.Fatalf("boundary %q names %q, which does not appear in %q", boundary.Name, word, document)
				}
			}
		}
	}
}

// The spread itself is resolved by graphqlRootFragmentSpreads, so the field
// scanner must skip it whole — and must not skip anything else with it.
func TestGraphQLRootSelectionFieldsSkipsWholeFragmentSpread(t *testing.T) {
	for _, testCase := range []struct {
		body string
		want string
	}{
		{" ...ViewerFields ", ""},
		{" ...A ...B ", ""},
		{" ...ViewerFields name ", "name"},
		{" viewer { id } ...A other ", "other,viewer"},
		{" updateUser(id: $id) { ok } ", "updateUser"},
		// The skip must leave the loop's own increment landing ON the byte after
		// the name. Overshooting it swallows a `#`, and the comment's first word
		// is then read as a field.
		{" ...ViewerFields#note\nname ", "name"},
		{" viewer { id } ...A#c\nother ", "other,viewer"},
	} {
		got := strings.Join(graphqlRootSelectionFields(testCase.body), ",")
		if got != testCase.want {
			t.Fatalf("graphqlRootSelectionFields(%q) = %q, want %q", testCase.body, got, testCase.want)
		}
	}
}
