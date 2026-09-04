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

// A callable declared inside a method BODY is a function-local binding, not a
// member of the enclosing class.
//
// `function handler(){}` (declaration or named expression) inside
// `Widget.render()` inherited the class scope and was emitted as the method
// `Widget.handler` — a symbol naming a member the class does not have. It then
// shared a base compound-v1 ID with the real `Widget.handler`, so the
// disambiguation branch in entitySymbols fired for BOTH and the real method's
// published ID moved during an ordinary edit to an unrelated method body:
//
//	before: local/r:JavaScript:widget.js:method:Widget.handler
//	after:  local/r:JavaScript:widget.js:method:Widget.handler#sig:4bae429b24ae5cb3
//
// The rule pinned here: in JS/TS a nested callable is qualified by the CALLABLE
// that declares it rather than by the type — `Widget.render.handler`, kind
// "function", Local, contained by `Widget.render`. The type scope is replaced,
// not cleared: clearing it (emitting a bare `handler`) removes the only thing
// telling two nested declarations apart, which is its own instability —
// TestNestedCallableIDSurvivesAnUnrelatedClass pins that half.
//
// The `const handler = (a) => a` spelling is left exactly as it was: a
// variable_declarator never consults the scope, so there was no type
// qualification to replace. The third case pins that this change did not move
// it.
func TestJavaScriptNestedCallableIsNotAClassMember(t *testing.T) {
	const noNested = "class Widget {\n" +
		"  render(a) { return a }\n" +
		"  handler(a) { return a + 1 }\n" +
		"}\n"
	const wantMemberID = "local/r:JavaScript:widget.js:method:Widget.handler"

	symbolsFor := func(t *testing.T, src string) []SymbolRecord {
		t.Helper()
		entities, _, status := TreeSitterParser{}.ParseWithStatus("widget.js", src)
		if status.ParseError {
			t.Fatalf("unexpected parse error: %s", status.Detail)
		}
		return entitySymbols("local/r", "widget.js", "JavaScript", entities)
	}

	// Control: with no nested callable the class member owns the bare ID.
	if got := symbolsFor(t, noNested); len(got) != 3 || got[2].ID != wantMemberID {
		t.Fatalf("baseline symbols = %s, want the member at the bare ID %s", symbolIDs(got), wantMemberID)
	}

	for _, testCase := range []struct {
		name       string
		src        string
		wantNested SymbolRecord
	}{{
		name: "function declaration",
		src: "class Widget {\n" +
			"  render(a) {\n" +
			"    function handler(v) { return v }\n" +
			"    return handler(a)\n" +
			"  }\n" +
			"  handler(a) { return a + 1 }\n" +
			"}\n",
		wantNested: SymbolRecord{Kind: "function", Name: "handler", QualifiedName: "Widget.render.handler"},
	}, {
		name: "named function expression",
		src: "class Widget {\n" +
			"  render(a) {\n" +
			"    return (0, function handler(v) { return v })(a)\n" +
			"  }\n" +
			"  handler(a) { return a + 1 }\n" +
			"}\n",
		wantNested: SymbolRecord{Kind: "function", Name: "handler", QualifiedName: "Widget.render.handler"},
	}, {
		// The spelling this change does NOT touch, pinned so it cannot drift.
		name: "arrow function bound to a const",
		src: "class Widget {\n" +
			"  render(a) {\n" +
			"    const handler = (v) => v\n" +
			"    return handler(a)\n" +
			"  }\n" +
			"  handler(a) { return a + 1 }\n" +
			"}\n",
		wantNested: SymbolRecord{Kind: "function", Name: "handler", QualifiedName: "handler"},
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			symbols := symbolsFor(t, testCase.src)

			// Exactly one symbol may claim to be Widget.handler, and it is the
			// class member: a second one is a symbol naming a member that does
			// not exist, and `def Widget.handler` would then have two answers.
			var members, nested []SymbolRecord
			for _, symbol := range symbols {
				switch symbol.QualifiedName {
				case "Widget.handler":
					members = append(members, symbol)
				case testCase.wantNested.QualifiedName:
					nested = append(nested, symbol)
				}
			}
			if len(members) != 1 {
				t.Fatalf("symbols claiming Widget.handler = %d, want 1: %s", len(members), symbolIDs(symbols))
			}

			// The member keeps the bare compound-v1 ID it had before the nested
			// callable existed: adding one is an ordinary edit to another method.
			if members[0].ID != wantMemberID {
				t.Errorf("class member ID = %s, want the unchanged %s", members[0].ID, wantMemberID)
			}
			if members[0].Local {
				t.Errorf("class member marked function-local: %#v", members[0])
			}

			// The nested callable survives as its own function-local symbol
			// contained by the method it is declared in.
			if len(nested) != 1 {
				t.Fatalf("nested symbols named %q = %d, want 1: %s",
					testCase.wantNested.QualifiedName, len(nested), symbolIDs(symbols))
			}
			got := nested[0]
			if got.Kind != testCase.wantNested.Kind || got.Name != testCase.wantNested.Name ||
				got.QualifiedName != testCase.wantNested.QualifiedName {
				t.Errorf("nested symbol = %s %s/%s, want %s %s/%s", got.Kind, got.Name, got.QualifiedName,
					testCase.wantNested.Kind, testCase.wantNested.Name, testCase.wantNested.QualifiedName)
			}
			if !got.Local {
				t.Errorf("nested callable not marked function-local: %#v", got)
			}
			if want := "local/r:JavaScript:widget.js:method:Widget.render"; got.ContainerID != want {
				t.Errorf("nested container = %q, want the enclosing method %q", got.ContainerID, want)
			}
		})
	}
}

// The OTHER half of the same identity rule: a nested callable's compound-v1 ID
// must not move when an unrelated class gains a same-named nested callable.
//
// Qualifying the nested `handler` under its enclosing class alone was the
// original defect (it names a member the class does not have). Dropping the
// qualification entirely is a second defect wearing the first one's clothes:
// `helper` in `A.m` and `helper` in `B.m` then share the base ID
// local/r:JavaScript:w.js:function:helper, so entitySymbols' disambiguation
// fires for BOTH and the one that existed first moves
//
//	before: local/r:JavaScript:w.js:function:helper
//	after:  local/r:JavaScript:w.js:function:helper#sig:80cfac553042146c
//
// on an edit to a class it has nothing to do with — and, because both share a
// signature, the two are then told apart only by an `#2` ordinal that source
// ORDER decides, so moving class B above class A swaps their IDs too.
//
// Qualifying under the enclosing CALLABLE keeps both properties at once:
// `A.m.helper` is not a member of `A`, and it is not `B.m.helper`.
func TestNestedCallableIDSurvivesAnUnrelatedClass(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		path       string
		language   string
		classA     string
		classB     string
		classBWith string
		wantID     string
	}{{
		name:     "JavaScript",
		path:     "w.js",
		language: "JavaScript",
		classA: "class A {\n" +
			"  m(a) {\n" +
			"    function helper(v) { return v }\n" +
			"    return helper(a)\n" +
			"  }\n" +
			"}\n",
		classB: "class B {\n  m(a) { return a }\n}\n",
		classBWith: "class B {\n" +
			"  m(a) {\n" +
			"    function helper(v) { return v }\n" +
			"    return helper(a)\n" +
			"  }\n" +
			"}\n",
		wantID: "local/r:JavaScript:w.js:function:A.m.helper",
	}, {
		name:     "TypeScript",
		path:     "w.ts",
		language: "TypeScript",
		classA: "class A {\n" +
			"  m(a: number): number {\n" +
			"    function helper(v: number): number { return v }\n" +
			"    return helper(a)\n" +
			"  }\n" +
			"}\n",
		classB: "class B {\n  m(a: number): number { return a }\n}\n",
		classBWith: "class B {\n" +
			"  m(a: number): number {\n" +
			"    function helper(v: number): number { return v }\n" +
			"    return helper(a)\n" +
			"  }\n" +
			"}\n",
		wantID: "local/r:TypeScript:w.ts:function:A.m.helper",
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			idOfAHelper := func(t *testing.T, src string) string {
				t.Helper()
				entities, _, status := TreeSitterParser{}.ParseWithStatus(testCase.path, src)
				if status.ParseError {
					t.Fatalf("unexpected parse error: %s", status.Detail)
				}
				symbols := entitySymbols("local/r", testCase.path, testCase.language, entities)
				var found []SymbolRecord
				for _, symbol := range symbols {
					if symbol.Name == "helper" && symbol.StartLine <= 4 {
						found = append(found, symbol)
					}
				}
				if len(found) != 1 {
					t.Fatalf("helpers declared in A.m = %d, want 1: %s", len(found), symbolIDs(symbols))
				}
				if !found[0].Local {
					t.Errorf("nested helper not marked function-local: %#v", found[0])
				}
				return found[0].ID
			}

			before := idOfAHelper(t, testCase.classA+testCase.classB)
			// The edit: an unrelated class gains its own nested `helper`.
			after := idOfAHelper(t, testCase.classA+testCase.classBWith)
			if after != before {
				t.Errorf("adding a nested helper to B.m moved A.m's helper: %s -> %s", before, after)
			}
			if before != testCase.wantID {
				t.Errorf("helper ID = %s, want the callable-qualified %s", before, testCase.wantID)
			}
		})
	}
}

// The qualification REPLACES a type scope; it does not invent one, and it does
// not survive the next type. Three shapes pin that boundary, and each is the
// only thing standing between the walk and an ID migration far wider than the
// defect being fixed.
func TestNestedCallableQualificationOnlyReplacesATypeScope(t *testing.T) {
	symbolsFor := func(t *testing.T, src string) []SymbolRecord {
		t.Helper()
		entities, _, status := TreeSitterParser{}.ParseWithStatus("w.js", src)
		if status.ParseError {
			t.Fatalf("unexpected parse error: %s", status.Detail)
		}
		return entitySymbols("local/r", "w.js", "JavaScript", entities)
	}

	// A callable nested in a TOP-LEVEL function never carried a type scope, so
	// there is nothing to replace and its published ID is what it always was.
	// Qualifying it too would move every nested helper in every JS file that has
	// no class in it — a far larger migration than this fix, and not this fix's
	// to make.
	const topLevel = "function outer(a) {\n" +
		"  function inner(v) { return v }\n" +
		"  return inner(a)\n" +
		"}\n"
	var innerSymbols []SymbolRecord
	for _, symbol := range symbolsFor(t, topLevel) {
		if symbol.Name == "inner" {
			innerSymbols = append(innerSymbols, symbol)
		}
	}
	if len(innerSymbols) != 1 {
		t.Fatalf("symbols named inner = %d, want 1", len(innerSymbols))
	}
	if want := "local/r:JavaScript:w.js:function:inner"; innerSymbols[0].ID != want {
		t.Errorf("callable nested in a top-level function = %s, want the unqualified %s",
			innerSymbols[0].ID, want)
	}

	// An object literal's method IS declared with member syntax, so it keeps
	// kind "method" — only the `function name(){}` forms are lexical bindings
	// that lexicalCallableForm demotes back from entityFromNode's promotion.
	const objectLiteral = "class A {\n" +
		"  m() {\n" +
		"    const o = { go() { return 1 } }\n" +
		"    return o\n" +
		"  }\n" +
		"}\n"
	var goSymbols []SymbolRecord
	for _, symbol := range symbolsFor(t, objectLiteral) {
		if symbol.Name == "go" {
			goSymbols = append(goSymbols, symbol)
		}
	}
	if len(goSymbols) != 1 {
		t.Fatalf("symbols named go = %d, want 1", len(goSymbols))
	}
	if want := "local/r:JavaScript:w.js:method:A.m.go"; goSymbols[0].ID != want {
		t.Errorf("object literal method = %s, want %s", goSymbols[0].ID, want)
	}

	// A TYPE declared in a callable body scopes its own members again, so the
	// re-anchoring must not leak past it: `function f(){}` in that class's
	// static block is a member of the class, not a lexical binding of whatever
	// method the class happens to sit in. Without the reset it is emitted as
	// `function C.f` instead of `method C.f`.
	const nestedClass = "class A {\n" +
		"  m() {\n" +
		"    class C {\n" +
		"      static { function f() { return 1 } }\n" +
		"    }\n" +
		"    return C\n" +
		"  }\n" +
		"}\n"
	var fSymbols []SymbolRecord
	for _, symbol := range symbolsFor(t, nestedClass) {
		if symbol.Name == "f" {
			fSymbols = append(fSymbols, symbol)
		}
	}
	if len(fSymbols) != 1 {
		t.Fatalf("symbols named f = %d, want 1", len(fSymbols))
	}
	if want := "local/r:JavaScript:w.js:method:C.f"; fSymbols[0].ID != want {
		t.Errorf("member of a class declared in a method body = %s, want %s", fSymbols[0].ID, want)
	}
}

// The scope reset is JS/TS-only, and that gate is load-bearing: it bounds the
// blast radius of the fix above to two languages.
//
// Python's nested `def` reaches the same phantom-member shape (`C.helper` for a
// helper defined inside `C.m`), and Java walks anonymous-class members with the
// enclosing type scope on purpose (see initializerTypeBodies). Neither is
// touched here — widening the gate would move every nested-callable compound-v1
// ID in those languages, which is a separate decision with its own migration.
// What this pins is only that the JS/TS fix did not silently make it for them.
func TestNestedCallableScopeResetIsJavaScriptOnly(t *testing.T) {
	for _, testCase := range []struct {
		language string
		want     bool
	}{
		{"JavaScript", true},
		{"TypeScript", true},
		{"Python", false},
		{"Java", false},
		{"Ruby", false},
		{"Go", false},
	} {
		if got := functionLocalScopeResets(testCase.language); got != testCase.want {
			t.Errorf("functionLocalScopeResets(%q) = %v, want %v", testCase.language, got, testCase.want)
		}
	}

	// Behavioural half: a Python helper defined inside a method keeps the
	// qualified name it has today, so no Python symbol ID moved.
	const src = "class C:\n" +
		"    def m(self):\n" +
		"        def helper(v):\n" +
		"            return v\n" +
		"        return helper(1)\n"
	entities, _, status := TreeSitterParser{}.ParseWithStatus("c.py", src)
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	found := false
	for _, entity := range entities {
		if entity.Name == "C.helper" && entity.Local {
			found = true
		}
	}
	if !found {
		t.Errorf("Python nested def lost its class qualification; entities = %s", describeEntities(entities))
	}
}

func symbolIDs(symbols []SymbolRecord) string {
	out := ""
	for _, symbol := range symbols {
		out += fmt.Sprintf("{%s %s local=%v}", symbol.Kind, symbol.ID, symbol.Local)
	}
	return out
}
