package sem

import (
	"fmt"
	"testing"
)

// TestFunctionValueBindingIsOneSymbol covers the `export const f = <function value>` family
// across every language whose extractor can emit a binding record: the binding and the
// function expression that initialises it are ONE entity, so exactly one record must be
// emitted, on the declaration's own line, carrying the function's span and signature.
func TestFunctionValueBindingIsOneSymbol(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		path          string
		source        string
		symbol        string
		wantKind      string
		wantStartLine int
		wantEndLine   int
	}{
		{
			name: "typescript arrow after blank line",
			path: "dash.ts",
			source: "export interface Parameter {\n" +
				"  name: string;\n" +
				"}\n" +
				"\n" +
				"export const markDashboardsAsStale = (\n" +
				"  pendingParameters: Parameter[] = [],\n" +
				") => {\n" +
				"  return pendingParameters;\n" +
				"};\n",
			symbol:        "markDashboardsAsStale",
			wantKind:      "function",
			wantStartLine: 5,
			wantEndLine:   9,
		},
		{
			name:          "typescript single line arrow after blank line",
			path:          "one.ts",
			source:        "const other = 1;\n\nexport const helperOne = (x: number): number => x + 1;\n",
			symbol:        "helperOne",
			wantKind:      "function",
			wantStartLine: 3,
			wantEndLine:   3,
		},
		{
			name:          "typescript arrow on first line",
			path:          "first.ts",
			source:        "export const first = (x: number) => x;\n",
			symbol:        "first",
			wantKind:      "function",
			wantStartLine: 1,
			wantEndLine:   1,
		},
		{
			name:          "javascript function expression",
			path:          "expr.js",
			source:        "\n\nexport const funcExpr = function (a) {\n  return a * 2;\n};\n",
			symbol:        "funcExpr",
			wantKind:      "function",
			wantStartLine: 3,
			wantEndLine:   5,
		},
		{
			name:          "javascript exported let async arrow",
			path:          "async.js",
			source:        "export const before = 1;\n\nexport let jsLet = async (c) => {\n  return c;\n};\n",
			symbol:        "jsLet",
			wantKind:      "function",
			wantStartLine: 3,
			wantEndLine:   5,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			entities, _ := TreeSitterParser{}.Parse(testCase.path, testCase.source)
			matches := make([]Entity, 0, 2)
			for _, entity := range entities {
				if entity.Name == testCase.symbol {
					matches = append(matches, entity)
				}
			}
			if len(matches) != 1 {
				t.Fatalf("%s: want exactly 1 record for %q, got %d: %s",
					testCase.path, testCase.symbol, len(matches), describeEntities(matches))
			}
			got := matches[0]
			if got.Kind != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, testCase.wantKind)
			}
			if got.StartLine != testCase.wantStartLine || got.EndLine != testCase.wantEndLine {
				t.Fatalf("span = %d-%d, want %d-%d",
					got.StartLine, got.EndLine, testCase.wantStartLine, testCase.wantEndLine)
			}
			if got.Signature == "" {
				t.Fatalf("signature is empty; the function record's signature must survive")
			}
		})
	}
}

// TestExportedNonFunctionVariableSurvives guards the other side of the collapse: an exported
// binding whose value is NOT a function is a real symbol of its own and must be kept, on its
// own line.
func TestExportedNonFunctionVariableSurvives(t *testing.T) {
	t.Parallel()
	source := "export const fn = () => 1;\n\nexport const LIMIT = 42;\n"
	entities, _ := TreeSitterParser{}.Parse("consts.ts", source)
	var limit *Entity
	for index := range entities {
		if entities[index].Name == "LIMIT" {
			limit = &entities[index]
		}
	}
	if limit == nil {
		t.Fatalf("LIMIT was dropped: %s", describeEntities(entities))
	}
	if limit.Kind != "variable" {
		t.Fatalf("LIMIT kind = %q, want variable", limit.Kind)
	}
	if limit.StartLine != 3 {
		t.Fatalf("LIMIT start line = %d, want 3", limit.StartLine)
	}
}

// TestJavascriptExportedVariableEntityLines pins the regex recovery pass directly: its
// leading indentation match must not be allowed to consume the newline of a preceding blank
// line, which is what made every reported line number one too high.
func TestJavascriptExportedVariableEntityLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source string
		want   map[string]int
	}{
		{
			name:   "blank line before declaration",
			source: "const a = 1;\n\nexport const target = 5;\n",
			want:   map[string]int{"target": 3},
		},
		{
			name:   "several blank lines before declaration",
			source: "const a = 1;\n\n\n\nexport const target = 5;\n",
			want:   map[string]int{"target": 5},
		},
		{
			name:   "indented declaration keeps its own line",
			source: "const a = 1;\n\n  export const target = 5;\n",
			want:   map[string]int{"target": 3},
		},
		{
			name:   "trailing whitespace line before declaration",
			source: "const a = 1;\n   \nexport const target = 5;\n",
			want:   map[string]int{"target": 3},
		},
		{
			name:   "consecutive declarations",
			source: "export const one = 1;\n\nexport const two = 2;\n\nexport const three = 3;\n",
			want:   map[string]int{"one": 1, "two": 3, "three": 5},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			entities := javascriptExportedVariableEntities(testCase.source)
			got := make(map[string]int, len(entities))
			for _, entity := range entities {
				if entity.StartLine != entity.EndLine {
					t.Fatalf("%q span = %d-%d, want a single line",
						entity.Name, entity.StartLine, entity.EndLine)
				}
				got[entity.Name] = entity.StartLine
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("names = %v, want %v", got, testCase.want)
			}
			for name, line := range testCase.want {
				if got[name] != line {
					t.Fatalf("%q start line = %d, want %d", name, got[name], line)
				}
			}
		})
	}
}

func TestCollapseFunctionValueBindings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input []Entity
		want  []string
	}{
		{
			name: "binding one line above its function collapses",
			input: []Entity{
				{Kind: "variable", Name: "f", StartLine: 4, EndLine: 4},
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 9, Signature: "f = () =>"},
			},
			want: []string{"function:f:5-9"},
		},
		{
			name: "binding on the function's own line collapses",
			input: []Entity{
				{Kind: "variable", Name: "f", StartLine: 5, EndLine: 5},
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 5, Signature: "f = () =>"},
			},
			want: []string{"function:f:5-5"},
		},
		{
			name: "binding inside the function body collapses",
			input: []Entity{
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 9, Signature: "f = () =>"},
				{Kind: "constant", Name: "f", StartLine: 7, EndLine: 7},
			},
			want: []string{"function:f:5-9"},
		},
		{
			name: "unrelated same-name variable elsewhere survives",
			input: []Entity{
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 9, Signature: "f = () =>"},
				{Kind: "variable", Name: "f", StartLine: 40, EndLine: 40},
			},
			want: []string{"function:f:5-9", "variable:f:40-40"},
		},
		{
			name: "different names never collapse",
			input: []Entity{
				{Kind: "variable", Name: "g", StartLine: 4, EndLine: 4},
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 9},
			},
			want: []string{"variable:g:4-4", "function:f:5-9"},
		},
		{
			name: "qualified names keep scopes apart",
			input: []Entity{
				{Kind: "variable", Name: "Outer.f", StartLine: 4, EndLine: 4},
				{Kind: "function", Name: "f", StartLine: 5, EndLine: 9},
			},
			want: []string{"variable:Outer.f:4-4", "function:f:5-9"},
		},
		{
			name: "class value binding is not collapsed",
			input: []Entity{
				{Kind: "variable", Name: "S", StartLine: 5, EndLine: 5},
				{Kind: "class", Name: "S", StartLine: 5, EndLine: 9},
			},
			want: []string{"variable:S:5-5", "class:S:5-9"},
		},
		{
			name: "field with a method of the same name is untouched",
			input: []Entity{
				{Kind: "field", Name: "A.run", StartLine: 5, EndLine: 5},
				{Kind: "method", Name: "A.run", StartLine: 5, EndLine: 9},
			},
			want: []string{"field:A.run:5-5", "method:A.run:5-9"},
		},
		{
			name:  "no bindings is a no-op",
			input: []Entity{{Kind: "function", Name: "f", StartLine: 1, EndLine: 2}},
			want:  []string{"function:f:1-2"},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := collapseFunctionValueBindings(append([]Entity(nil), testCase.input...))
			keys := make([]string, 0, len(got))
			for _, entity := range got {
				keys = append(keys, fmt.Sprintf("%s:%s:%d-%d",
					entity.Kind, entity.Name, entity.StartLine, entity.EndLine))
			}
			if len(keys) != len(testCase.want) {
				t.Fatalf("entities = %v, want %v", keys, testCase.want)
			}
			for index := range keys {
				if keys[index] != testCase.want[index] {
					t.Fatalf("entities = %v, want %v", keys, testCase.want)
				}
			}
		})
	}
}

// TestFunctionValueBindingLanguageSurvey documents, per language, how many records the
// binding idiom produces. Every language must produce AT MOST one record for the bound name:
// the defect this guards is a second, phantom record sharing the name and qualified name,
// which makes the symbol unaddressable by impact/neighbors.
func TestFunctionValueBindingLanguageSurvey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path   string
		source string
		symbol string
	}{
		{"a.ts", "const x = 0;\n\nexport const bound = (a: number) => a;\n", "bound"},
		{"a.js", "const x = 0;\n\nexport const bound = function (a) { return a; };\n", "bound"},
		{"a.py", "import os\n\nbound = lambda x: x * 2\n", "bound"},
		{"a.php", "<?php\n\n$bound = function ($n) { return $n; };\n", "bound"},
		{"a.rb", "\nbound = lambda { |x| x * x }\n", "bound"},
		{"a.rs", "\npub const BOUND: fn(i32) -> i32 = |x| x + 1;\n", "BOUND"},
		{"a.go", "package main\n\nvar Bound = func(a int) int { return a }\n", "Bound"},
		{"a.groovy", "\ndef bound = { x -> x * 2 }\n", "bound"},
		{"a.lua", "\nlocal bound = function(x) return x end\n", "bound"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.path, func(t *testing.T) {
			t.Parallel()
			entities, _ := TreeSitterParser{}.Parse(testCase.path, testCase.source)
			count := 0
			for _, entity := range entities {
				if entity.Name == testCase.symbol {
					count++
				}
			}
			if count > 1 {
				t.Fatalf("%s: %q produced %d records, want at most 1: %s",
					testCase.path, testCase.symbol, count, describeEntities(entities))
			}
		})
	}
}

func describeEntities(entities []Entity) string {
	out := ""
	for _, entity := range entities {
		out += fmt.Sprintf("{%s %s %d-%d %q} ",
			entity.Kind, entity.Name, entity.StartLine, entity.EndLine, entity.Signature)
	}
	return out
}
