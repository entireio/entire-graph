package sem

import (
	"context"
	"reflect"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// astParameterNamesFor parses src and returns the parameter names of the first
// callable the walk reaches, mirroring how the parser reaches an entity node.
func astParameterNamesFor(t *testing.T, path, src string) ([]string, bool) {
	t.Helper()
	spec, ok := languageForPath(path)
	if !ok {
		t.Fatalf("no language spec for %s", path)
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(spec.grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	defer tree.Close()

	var names []string
	var known bool
	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		if known || !validNode(node) {
			return
		}
		if got, ok := astParameterNames(node, []byte(src)); ok && len(got) > 0 {
			names, known = got, true
			return
		}
		for index := 0; index < int(node.NamedChildCount()); index++ {
			walk(node.NamedChild(index))
		}
	}
	walk(tree.RootNode())
	return names, known
}

// TestASTParameterNamesAcrossGrammars pins the parameter extraction for one
// callable per grammar family. The signature-regex fallback it replaces splits
// on every comma and takes the first whitespace-delimited token, so it reported
// a type as the parameter name in every type-first language (Java's
// `check(String token)` yielded `String`) and leaked nested punctuation into the
// list. Each case below is one of the shapes that got wrong.
func TestASTParameterNamesAcrossGrammars(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
		src  string
		want []string
	}{
		{
			name: "go receiver excluded from the list but kept as an input",
			path: "a.go",
			src:  "package p\nfunc (c *Client) Do(ctx Context, name, urlStr string, opts map[string]int) error { return nil }\n",
			want: []string{"c", "ctx", "name", "urlStr", "opts"},
		},
		{
			name: "python keeps both spellings of a splat",
			path: "a.py",
			src:  "def f(a, b=1, c: int = 2, *args, **kw):\n    pass\n",
			want: []string{"a", "b", "c", "args", "*args", "kw", "**kw"},
		},
		{
			name: "rust skips the self parameter",
			path: "a.rs",
			src:  "fn f(&self, a: u8, b: Vec<(u8, u8)>) -> u8 { a }\n",
			want: []string{"a", "b"},
		},
		{
			name: "java type-first parameters name the binding, not the type",
			path: "a.java",
			src:  "class C { void m(int a, Map<String, Integer> b, String... c) {} }\n",
			want: []string{"a", "b", "c"},
		},
		{
			name: "zig anonymous struct parameter contributes no field names",
			path: "a.zig",
			src:  "fn caller(b: *Build, options: struct { scripts: u8, git_commit: u8 }) void {}\n",
			want: []string{"b", "options"},
		},
		{
			name: "c declarator unwraps pointer and function layers",
			path: "a.c",
			src:  "int f(int a, char *b, void (*cb)(int)) { return a; }\n",
			want: []string{"a", "b", "cb"},
		},
		{
			name: "csharp params array is not mistaken for a parameter",
			path: "a.cs",
			src:  "class C { void M(int a, Dictionary<string, int> b, params int[] c) {} }\n",
			want: []string{"a", "b", "c"},
		},
		{
			name: "php strips the sigil and keeps the variadic spelling",
			path: "a.php",
			src:  "<?php function f($a, int $b = 1, ...$c) {} ?>\n",
			want: []string{"a", "b", "c", "...c"},
		},
		{
			name: "kotlin default value expression is not a parameter",
			path: "a.kt",
			src:  "fun f(a: Int, b: Map<String, Int> = mapOf()) {}\n",
			want: []string{"a", "b"},
		},
		{
			name: "swift external label yields to the internal name",
			path: "a.swift",
			src:  "func f(_ a: Int, label b: [String: Int]) {}\n",
			want: []string{"a", "b"},
		},
		{
			name: "ruby splat and block parameters",
			path: "a.rb",
			src:  "def f(a, b = 1, *c, **d)\nend\n",
			want: []string{"a", "b", "c", "*c", "d", "**d"},
		},
		{
			name: "dart type-first parameters",
			path: "a.dart",
			src:  "int f(int a, int b) => a;\n",
			want: []string{"a", "b"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, known := astParameterNamesFor(t, testCase.path, testCase.src)
			if !known {
				t.Fatalf("expected the grammar to expose a parameter list")
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("parameter names: got %v want %v", got, testCase.want)
			}
		})
	}
}

// TestASTParameterNamesDeclinesUnknownShapes keeps the fallback honest: Elixir
// models `def f(a, b)` as a macro call with no parameter list, and reporting an
// empty list as authoritative there would suppress the signature regex that
// still finds names.
func TestASTParameterNamesDeclinesUnknownShapes(t *testing.T) {
	if _, known := astParameterNamesFor(t, "a.ex", "def f(a, b) do\nend\n"); known {
		t.Fatalf("expected Elixir to fall through to the signature regex")
	}
}

// TestASTParameterNamesRejectsSignatureRegexArtifacts is the regression this
// work exists for: the regex invented parameters out of struct fields and a
// stray closing brace, and those names then fabricated DATA_FLOWS edges
// asserting a caller parameter was forwarded when the identifier was a
// module-level constant.
func TestASTParameterNamesRejectsSignatureRegexArtifacts(t *testing.T) {
	const zig = "fn caller(b: *Build, options: struct { scripts: u8, git_commit: u8, }) void {}\n"
	regexNames := parameterNames("fn caller(b: *Build, options: struct { scripts: u8, git_commit: u8, }) void")
	for _, artifact := range []string{"}", "git_commit"} {
		if !regexNames[artifact] {
			t.Fatalf("expected the signature regex to invent %q (the bug under test)", artifact)
		}
	}
	got, known := astParameterNamesFor(t, "a.zig", zig)
	if !known {
		t.Fatalf("expected Zig parameters to come from the parse tree")
	}
	for _, artifact := range []string{"}", "git_commit"} {
		for _, name := range got {
			if name == artifact {
				t.Fatalf("parse tree extraction leaked the regex artifact %q: %v", artifact, got)
			}
		}
	}
}

// TestCleanParameterNameKeepsUnicodeIdentifiers guards a rule that was too
// strict: rejecting every non-ASCII rune also rejected legitimate identifiers
// in the many supported languages that allow them. `func f(café string, naïve
// int)` lost BOTH parameters, and because the list was still reported as
// AST-confirmed the signature fallback never ran, so the function contributed
// no data flows at all. Punctuation must still be rejected — that is what keeps
// the old regex artifacts out.
func TestCleanParameterNameKeepsUnicodeIdentifiers(t *testing.T) {
	for _, name := range []string{"café", "naïve", "数値", "ünter", "_private", "a1"} {
		if got := cleanParameterName(name); got != name {
			t.Errorf("identifier %q was rejected (got %q)", name, got)
		}
	}
	for _, artifact := range []string{"}", "]", ")", "{", "a-b", "a.b", "a b", "", "self", "this", "_"} {
		if got := cleanParameterName(artifact); got != "" {
			t.Errorf("non-identifier %q was accepted as %q", artifact, got)
		}
	}
	// Sigils are still stripped rather than rejected.
	if got := cleanParameterName("$php"); got != "php" {
		t.Errorf("sigil handling changed: got %q", got)
	}
}
