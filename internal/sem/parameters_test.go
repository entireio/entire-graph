package sem

import (
	"context"
	"reflect"
	"strings"
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
			name: "go variadic keeps the trailing spelling the call site writes",
			path: "a.go",
			src:  "package p\nfunc f(prefix string, args ...string) string { return sink(args...) }\n",
			want: []string{"prefix", "args", "args..."},
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

// astSignatureTypeTextsFor mirrors astParameterNamesFor for the type extractor.
func astSignatureTypeTextsFor(t *testing.T, path, src string) (string, string, bool) {
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
	var params, returns string
	var known bool
	var walk func(node *sitter.Node)
	walk = func(node *sitter.Node) {
		if known || !validNode(node) {
			return
		}
		if p, r, ok := astSignatureTypeTexts(node, []byte(src)); ok {
			params, returns, known = p, r, true
			return
		}
		for index := 0; index < int(node.NamedChildCount()); index++ {
			walk(node.NamedChild(index))
		}
	}
	walk(tree.RootNode())
	return params, returns, known
}

// TestASTSignatureTypesAcrossGrammars pins the type extraction the signature
// string could not do. The string split anchors on the first `(` — the receiver
// for a Go method — and, for languages it has no case for, guesses the return
// type as the second-to-last word before that paren, which is the declaration
// KEYWORD. Zig emitted no RETURNS_TYPE edge anywhere as a result.
func TestASTSignatureTypesAcrossGrammars(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		path, src   string
		wantParams  string
		wantReturns string
	}{
		{
			name:        "go method reads past the receiver",
			path:        "a.go",
			src:         "package p\nfunc (c *Client) Do(a Input) Output { return Output{} }\n",
			wantParams:  "Input",
			wantReturns: "Output",
		},
		{
			name:        "go multi-value result",
			path:        "a.go",
			src:         "package p\nfunc Multi(a Input) (Output, error) { return Output{}, nil }\n",
			wantParams:  "Input",
			wantReturns: "(Output, error)",
		},
		{
			name:        "swift return type is not the func keyword",
			path:        "a.swift",
			src:         "func f(a: Int) -> Result {}\n",
			wantParams:  "Int",
			wantReturns: "Result",
		},
		{
			name:        "zig return type is not the fn keyword",
			path:        "a.zig",
			src:         "fn f(a: u8) Result {}\n",
			wantParams:  "u8",
			wantReturns: "Result",
		},
		{
			name:        "scala return type is not the def keyword",
			path:        "a.scala",
			src:         "def f(a: Int): Result = a\n",
			wantParams:  "Int",
			wantReturns: "Result",
		},
		{
			name:        "kotlin return type has no field to read",
			path:        "a.kt",
			src:         "fun f(a: Int): Result {}\n",
			wantParams:  "Int",
			wantReturns: "Result",
		},
		{
			name:        "dart states the return type before the name",
			path:        "a.dart",
			src:         "Result f(int a) => null;\n",
			wantParams:  "int",
			wantReturns: "Result",
		},
		{
			name:        "typescript annotation colon is trimmed",
			path:        "a.ts",
			src:         "function f(a: number): Result {}\n",
			wantParams:  "number",
			wantReturns: "Result",
		},
		{
			name:        "rust arrow return",
			path:        "a.rs",
			src:         "fn f(a: u8) -> Result { a }\n",
			wantParams:  "u8",
			wantReturns: "Result",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			params, returns, known := astSignatureTypeTextsFor(t, testCase.path, testCase.src)
			if !known {
				t.Fatalf("expected the grammar to expose a parameter list")
			}
			if params != testCase.wantParams {
				t.Errorf("parameter types: got %q want %q", params, testCase.wantParams)
			}
			if returns != testCase.wantReturns {
				t.Errorf("return types: got %q want %q", returns, testCase.wantReturns)
			}
		})
	}
}

// TestASTSignatureTypesRejectKeywordReturns is the regression proper: the
// signature-string path reports a language keyword as the return type for every
// language it has no explicit case for.
func TestASTSignatureTypesRejectKeywordReturns(t *testing.T) {
	for _, testCase := range []struct{ language, path, src, keyword string }{
		{"Swift", "a.swift", "func f(a: Int) -> Result {}\n", "func"},
		{"Zig", "a.zig", "fn f(a: u8) Result {}\n", "fn"},
		{"Scala", "a.scala", "def f(a: Int): Result = a\n", "def"},
	} {
		t.Run(testCase.language, func(t *testing.T) {
			stale := signatureTypeReferences(testCase.language, strings.TrimSuffix(strings.SplitN(testCase.src, "{", 2)[0], " "))
			if !slicesContain(stale["RETURNS_TYPE"], testCase.keyword) {
				t.Fatalf("expected the signature split to report %q as a return type (the bug under test), got %v",
					testCase.keyword, stale["RETURNS_TYPE"])
			}
			_, returns, known := astSignatureTypeTextsFor(t, testCase.path, testCase.src)
			if !known {
				t.Fatalf("expected parse-tree extraction to apply")
			}
			if returns == testCase.keyword {
				t.Fatalf("parse-tree extraction still reports the %q keyword as a return type", testCase.keyword)
			}
		})
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestJavaScriptReexportCandidateGatesNonJSPaths pins the language gate on the
// re-export resolution. It used to run for every resolved import in every
// language — opening the target file and scanning it for `export … from`
// syntax, recursing up to eight levels — which on a Rust repository was 32% of
// a fast-profile snapshot spent looking for JavaScript in `use` targets.
func TestJavaScriptReexportCandidateGatesNonJSPaths(t *testing.T) {
	for _, path := range []string{
		"src/index.ts", "src/index.tsx", "lib/mod.js", "lib/mod.jsx",
		"lib/mod.mjs", "lib/mod.cjs", "types/api.d.ts",
	} {
		if !javaScriptReexportCandidate(path) {
			t.Errorf("%s should be scanned for re-exports", path)
		}
	}
	for _, path := range []string{
		"src/main.rs", "src/main.go", "src/main.zig", "src/main.py",
		"src/Main.java", "src/main.c", "Cargo.toml", "README.md",
	} {
		if javaScriptReexportCandidate(path) {
			t.Errorf("%s cannot carry JS re-export syntax and must not be opened for it", path)
		}
	}
}

// TestGraphQLBodyScanHonoursCapabilityTable pins the gate to one source of
// truth. The body patterns match ordinary prose and code — `query the columns`
// in a Python docstring, `subscription cancel() {` in Java — so running them
// everywhere emitted 1,245 edges in cli-cli and 1,057 in spring-framework that
// `capabilities --json` said those languages could not produce. The gate reads
// the capability table rather than repeating the language list, so the two
// cannot drift apart again.
func TestGraphQLBodyScanHonoursCapabilityTable(t *testing.T) {
	for _, language := range []string{"TypeScript", "JavaScript", "Python", "GraphQL"} {
		if !serviceBoundaryScanLanguage(language) {
			t.Errorf("%s declares HANDLES_GRAPHQL and must be scanned", language)
		}
	}
	for _, language := range []string{"Go", "Java", "Ruby", "Rust", "Zig", "C#"} {
		if serviceBoundaryScanLanguage(language) {
			t.Errorf("%s does not declare HANDLES_GRAPHQL and must not be scanned", language)
		}
		if languageSupportsRelation(language, "HANDLES_GRAPHQL") {
			t.Errorf("capability table unexpectedly declares HANDLES_GRAPHQL for %s", language)
		}
	}
	// The gate and the table are the same question, so they cannot disagree.
	for _, language := range []string{"TypeScript", "Go", "Python", "Java", "Zig"} {
		if serviceBoundaryScanLanguage(language) != languageSupportsRelation(language, "HANDLES_GRAPHQL") {
			t.Errorf("gate and capability table disagree for %s", language)
		}
	}
}

// TestGraphQLBodyScanSkipsProseInUndeclaredLanguages is the end-to-end form:
// Go source whose comments read like GraphQL must produce no boundary.
func TestGraphQLBodyScanSkipsProseInUndeclaredLanguages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "store.go", `package store

// Load runs a query string against the index and will mutation-test the
// result. Callers subscription cancel semantics are documented above.
func Load(name string) string {
	return name
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range snapshot.Relations {
		if relation.Type == "HANDLES_GRAPHQL" {
			t.Fatalf("Go prose produced a GraphQL boundary: %+v", relation)
		}
	}
}

// TestGraphQLOperationsRequireLiteralContext pins the precision fix. The
// operation patterns used to run over whole symbol bodies, so they matched
// three kinds of non-GraphQL text: prose ("query the columns" in a docstring),
// comments, and — for the selection-set pattern, which looks for
// `<keyword> <name>? (args)? {` — an ordinary method declared as `query() {}`.
// Confining them to string and template literals removes the code and comment
// cases; requiring a selection set inside the literal removes the prose.
func TestGraphQLOperationsRequireLiteralContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "client.ts", `export function loadViewer(id: string): unknown {
  return gql`+"`"+`query GetViewer($id: ID!) { viewer { id } }`+"`"+`;
}

export function plainString(): unknown {
  return request("mutation UpdateUser($id: ID!) { updateUser(id: $id) { id } }");
}

export class Repo {
  query() {
    return 1;
  }
}
`)
	writeFile(t, repo, "client.py", `def fetch(client):
    """Run a query against the API and query the cache for results."""
    return client.execute("""
        query ListUsers { users { id } }
    """)
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, relation := range snapshot.Relations {
		if relation.Type != "HANDLES_GRAPHQL" {
			continue
		}
		from := relation.FromID[strings.LastIndex(relation.FromID, ":")+1:]
		to := relation.ToID
		found[from+" => "+to[strings.Index(to, "graphql:")+len("graphql:"):]] = true
	}
	// A tagged template, a plain string argument and a Python triple-quoted
	// literal are all real embeddings and must be found.
	for _, want := range []string{
		"loadViewer => query GetViewer",
		"loadViewer => query viewer",
		"plainString => mutation UpdateUser",
		"fetch => query ListUsers",
		"fetch => query users",
	} {
		if !found[want] {
			t.Errorf("missing real GraphQL boundary %q; got %v", want, sortedKeysOf(found))
		}
	}
	// Prose in the same docstring, and a method that merely looks like an
	// operation, must produce nothing.
	for key := range found {
		for _, forbidden := range []string{"query the", "query against", "query return", "query cache"} {
			if strings.Contains(key, forbidden) {
				t.Errorf("non-GraphQL text produced a boundary: %q", key)
			}
		}
	}
}

// TestHostLanguageLiteralsSpansQuotingStyles covers the literal scanner the
// precision fix depends on.
func TestHostLanguageLiteralsSpansQuotingStyles(t *testing.T) {
	literals := hostLanguageLiterals("a = `tpl\nspans lines`; b = \"double\"; c = 'single'; d = \"esc\\\"aped\";")
	for _, want := range []string{"tpl\nspans lines", "double", "single"} {
		if !slicesContain(literals, want) {
			t.Errorf("missing literal %q in %v", want, literals)
		}
	}
	// A quoted literal does not run past its line, so an unterminated quote
	// cannot swallow the rest of a body.
	if got := hostLanguageLiterals("x = \"unterminated\nquery Nope { a }"); slicesContain(got, "unterminated\nquery Nope { a }") {
		t.Errorf("an unterminated quote swallowed following lines: %v", got)
	}
	// Python triple quotes are one literal, newlines included.
	if got := hostLanguageLiterals(`x = """query Doc { a }"""`); !slicesContain(got, "query Doc { a }") {
		t.Errorf("triple-quoted literal not captured: %v", got)
	}
}
