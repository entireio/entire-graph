package sem

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every shape the text finder misread in review is one the parse tree names exactly. Each case is a
// real parse of real source; want is the 1-based line of the declaration's name token. The
// shapes that fooled text scanning sit ABOVE the name: an annotation argument spelled like the
// name, a raw string or block comment holding `name(`, a property type spelled like the name.
func TestParserNameLineIsTheNameToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, path, src, symbol, kind string
		want                          int
	}{
		{"java-multiline-annotation-argument", "a/H.java", "class H {\n  @Policy(\n      run = true,\n      audit = false\n  )\n  public void run() {}\n}\n", "run", "method", 6},
		{"csharp-property-type-equals-name", "a/C.cs", "class C {\n    public Options Options\n    {\n        get { return _options ??= new Options(); }\n    }\n}\n", "Options", "", 2},
		{"csharp-property-type-on-line-above", "a/E.cs", "class E {\n    public Options\n        Options { get; }\n}\n", "Options", "", 3},
		{"java-annotated-field-unquoted-argument", "a/F.java", "class F {\n  @Column(\n      name = retries\n  )\n  private int retries;\n}\n", "retries", "", 5},
		{"csharp-attributes", "a/D.cs", "class D {\n    [Route(\"Index\")]\n    [HttpGet]\n    public IActionResult Index(int page) { return null; }\n}\n", "Index", "method", 4},
		{"ruby-self-method-no-parens", "a/x.rb", "class A\n  def self.config\n    @config ||= Config.load(config: path)\n  end\nend\n", "config", "method", 2},
		{"python-decorator-trailing-comment", "a/x.py", "@retry  # fetch() may raise\n@lru_cache()\ndef fetch(url):\n    return get(url)\n", "fetch", "function", 3},
		{"python-multiline-decorator-kwarg", "a/y.py", "@app.route(\n    \"/login\",\n    login=True,\n)\ndef login():\n    pass\n", "login", "function", 5},
		{"rust-raw-string-attribute", "a/x.rs", "#[test_case(r#\"\nhandle(x)\n\"#)]\nfn handle() {}\n", "handle", "function", 4},
		{"go-raw-string-fixture", "a/x.go", "package a\n\nvar _ = `\nfunc handle() {}\n`\n\nfunc handle() {}\n", "handle", "function", 7},
		{"fsharp-qualified-module", "a/L.fs", "module Fixtures.Ledger\n\ntype Ledger = { Total: int }\n", "Ledger", "module", 1},
		{"kotlin-multiline-annotation", "a/x.kt", "class K {\n  @Deprecated(\n    \"run\"\n  )\n  fun run(x: Int): Int = x\n}\n", "run", "method", 5},
		{"typescript-decorator", "a/x.ts", "class S {\n  @Get(\n    'run'\n  )\n  run(): void {}\n}\n", "run", "method", 5},
		{"c-unstarred-block-comment", "a/x.c", "/*\n   handle(x) returns x\n*/\nint handle(int x) {\n  return x;\n}\n", "handle", "function", 4},
		{"cpp-split-signature", "a/x.cpp", "namespace n {\n[[nodiscard]]\nint\nhandle(int x) { return x; }\n}\n", "handle", "function", 4},
		{"elixir-doc-heredoc", "a/x.ex", "defmodule M do\n  @doc \"\"\"\n  parse(input) returns a tree\n  \"\"\"\n  def parse(input) do\n    input\n  end\nend\n", "parse", "method", 5},
		{"php-multiline-attribute", "a/x.php", "<?php\nclass S {\n  #[Route(\n    'run'\n  )]\n  public function run() {}\n}\n", "run", "method", 6},
		{"swift-attribute", "a/x.swift", "class S {\n  @objc\n  func run() {}\n}\n", "run", "method", 3},
		{"scala-annotation", "a/x.scala", "object O {\n  @deprecated(\"run\")\n  def run(): Unit = ()\n}\n", "run", "method", 3},
		{"unicode-identifier", "a/u.go", "package a\n\nfunc runé() {}\n", "runé", "function", 3},
		{"protobuf-synthetic-offset", "a/x.proto", "service S {\n  rpc Run(Req) returns (Res);\n}\n", "Run", "rpc", 2},
	}
	for _, c := range cases {
		entities, _ := TreeSitterParser{}.Parse(c.path, c.src)
		var got []string
		matched := false
		for _, entity := range entities {
			if shortEntityName(entity.Name) != c.symbol || c.kind != "" && entity.Kind != c.kind {
				continue
			}
			matched = true
			if line := entityNameLineWithin(entity); line != c.want {
				t.Errorf("%s: %s %s name line = %d (start %d); want %d", c.name, entity.Kind, entity.Name, line, entity.StartLine, c.want)
			}
		}
		if !matched {
			for _, entity := range entities {
				got = append(got, entity.Kind+" "+entity.Name)
			}
			t.Errorf("%s: no %q entity among %v", c.name, c.symbol, got)
		}
	}
}

// A name line outside the symbol's own span is parse-metadata disagreement, not a name line.
func TestEntityNameLineWithinSpan(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		entity Entity
		want   int
	}{
		{Entity{StartLine: 3, EndLine: 9, nameLine: 5}, 5},
		{Entity{StartLine: 3, EndLine: 9, nameLine: 3}, 3},
		{Entity{StartLine: 3, EndLine: 9, nameLine: 9}, 9},
		{Entity{StartLine: 3, EndLine: 9, nameLine: 2}, 0},
		{Entity{StartLine: 3, EndLine: 9, nameLine: 10}, 0},
		{Entity{StartLine: 3, EndLine: 9}, 0},
	} {
		if got := entityNameLineWithin(c.entity); got != c.want {
			t.Errorf("%+v: %d, want %d", c.entity, got, c.want)
		}
	}
}

// The preindex cache keeps the name line: a warm search anchors where a cold one does.
func TestSearchSnapshotCachePreservesNameLine(t *testing.T) {
	t.Parallel()
	snapshot := ProviderSnapshot{Symbols: []SymbolRecord{{ID: "named", nameLine: 42}, {ID: "unnamed"}}}
	cache := newCachedSearchSnapshot("test-version", "commit", "tree", ProviderSnapshotOptions{Profile: ProfileFull}, snapshot)
	entry := testCacheEntry(t)
	if err := writeSearchSnapshot(entry, cache); err != nil {
		t.Fatal(err)
	}
	restored, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot.Symbols[0].NameLine(); got != 42 {
		t.Fatalf("restored name line = %d, want 42", got)
	}
	if got := restored.Snapshot.Symbols[1].NameLine(); got != 0 {
		t.Fatalf("a symbol without a name line restored %d", got)
	}
	if !strings.HasPrefix(searchSnapshotCacheVersion, "search-snapshot-v17-") {
		t.Fatalf("cache version %q: entries written before name lines must be retired", searchSnapshotCacheVersion)
	}
}

// Search carries the parser's name line to the result and to JSON, where an annotation sits
// between the symbol's first line and its name.
func TestSearchResultCarriesSymbolNameLine(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "src/Handler.java", `package demo;

public class Handler {
  @Policy(
      dispatchRequest = true,
      audit = false
  )
  public void dispatchRequest(String input) {
    System.out.println(input);
  }
}
`)
	response, err := SearchRepository(t.Context(), repo, "test-version", "dispatchRequest",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.SymbolName != "dispatchRequest" {
			continue
		}
		if result.SymbolStartLine != 4 || result.SymbolNameLine != 8 {
			t.Fatalf("symbol lines start=%d name=%d; want start 4, name 8", result.SymbolStartLine, result.SymbolNameLine)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"symbol_name_line":8`) {
			t.Fatalf("JSON lacks symbol_name_line: %s", encoded)
		}
		return
	}
	t.Fatalf("no dispatchRequest result: %+v", response.Results)
}

// Absent stays absent: omitempty keeps results without a parse-tree name byte-identical on the wire.
func TestSymbolNameLineOmittedWhenUnknown(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(SearchResult{SymbolStartLine: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "symbol_name_line") {
		t.Fatalf("unknown name line serialized: %s", encoded)
	}
}

// Every place a result takes its symbol identity copies the name line from the same symbol, so no
// path through search hands a renderer a start line without the matching name line.
func TestSearchResultSymbolSitesCarryNameLine(t *testing.T) {
	t.Parallel()
	lines := []string{"@A", "@B", "func run() {", "  x()", "}"}
	symbol := SymbolRecord{ID: "run", Name: "run", QualifiedName: "run", FilePath: "a.go", StartLine: 1, EndLine: 5, nameLine: 3}

	sparse := []searchCandidate{{result: SearchResult{FilePath: "a.go", FocusLine: 4}}}
	attachSparseCandidateSymbols(sparse, map[string][]SymbolRecord{"a.go": {symbol}})
	if got := sparse[0].result.SymbolNameLine; got != 3 {
		t.Errorf("sparse candidate name line = %d, want 3", got)
	}

	if candidate, ok := makeSearchCandidate(buildSearchQuery("run"), "a.go", "Go", lines, 1, 5, symbol, 40); !ok {
		t.Errorf("makeSearchCandidate declined")
	} else if got := candidate.result.SymbolNameLine; got != 3 {
		t.Errorf("symbol candidate name line = %d, want 3", got)
	}

	target := symbol
	target.ID, target.FilePath = "target", "target.go"
	out := expandGraphCandidates(
		[]searchCandidate{{result: SearchResult{SymbolID: "seed", FilePath: "seed.go"}, score: 5}}, searchQuery{},
		[]RelationRecord{{FromID: "seed", ToID: "target", Type: "CALLS", Confidence: 1.0}},
		map[string]SymbolRecord{"seed": {ID: "seed", Name: "Seed", FilePath: "seed.go", StartLine: 1, EndLine: 3}, "target": target},
		nil, func(path string) (string, bool) { return strings.Join(lines, "\n"), path == "target.go" }, nil,
		SearchOptions{MaxRegionLines: 40, MaxSnippetLines: 40})
	found := false
	for _, candidate := range out {
		if candidate.result.SymbolID == "target" {
			found = true
			if got := candidate.result.SymbolNameLine; got != 3 {
				t.Errorf("graph neighbor name line = %d, want 3", got)
			}
		}
	}
	if !found {
		t.Errorf("graph neighbor not expanded: %+v", out)
	}

	widened := widenSearchResultToEnclosure(SearchResult{FilePath: "a.go", StartLine: 3, EndLine: 4, FocusLine: 4,
		SnippetStartLine: 3, SnippetEndLine: 4, Signals: []string{}}, searchEnclosure{start: 1, end: 5, lines: lines, symbol: symbol})
	if got := widened.SymbolNameLine; got != 3 {
		t.Errorf("enclosure name line = %d, want 3", got)
	}
}
