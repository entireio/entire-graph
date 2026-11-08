package sem

import (
	"strings"
	"testing"
)

// REV2 finder sweep: for each shape, the index DeclarationLineIndex picks vs the real declaration.
func TestRev2FinderLanguages(t *testing.T) {
	cases := []struct {
		name, symbol string
		want         int
		src          string
	}{
		{"go-method-receiver", "Handle", 0, "func (s *Server) Handle(w http.ResponseWriter) {\n\ts.Handle2(w)\n}"},
		{"go-func-literal-var", "handle", 0, "var handle = func(x int) int {\n\treturn x\n}"},
		{"go-grouped-type", "Config", 0, "\tConfig struct {\n\t\tName string\n\t}"},
		{"py-async-def", "fetch", 0, "async def fetch(url):\n    return await get(url)"},
		{"py-nested-def", "inner", 1, "def outer():\n    def inner():\n        pass"},
		{"py-docstring-def-text", "run", 0, "def run(x):\n    \"\"\"Usage:\n    def run(x): ...\n    run(1)\n    \"\"\"\n    return x"},
		{"py-decorator-trailing-comment", "fetch", 2, "@retry  # fetch() may raise\n@lru_cache()\ndef fetch(url):\n    return get(url)"},
		{"py-multiline-decorator-kwarg", "login", 4, "@app.route(\n    \"/login\",\n    login=True,\n)\ndef login():\n    pass"},
		{"js-arrow-const", "handler", 0, "export const handler = async (req) => {\n  return handler2(req)\n}"},
		{"js-class-method", "handle", 0, "  async handle(req) {\n    return 1\n  }"},
		{"js-object-shorthand", "handle", 0, "  handle(req) {\n    return 1\n  },"},
		{"rust-impl-fn", "handle", 0, "    pub fn handle<'a>(&'a self) -> &'a str {\n        self.x\n    }"},
		{"rust-trait-fn", "handle", 0, "    fn handle(&self);"},
		{"rust-raw-string-multiline", "handle", 3, "#[test_case(r#\"\nhandle(x)\n\"#)]\nfn handle() {}"},
		{"java-ctor", "Server", 0, "    public Server(int port) {\n        this.port = port;\n    }"},
		{"java-generic-method", "handle", 0, "    public <T> List<T> handle(Class<T> c) {\n        return null;\n    }"},
		{"java-multiline-annotation", "handle", 4, "    @RequestMapping(\n        value = \"/h\",\n        name = handle(),\n    )\n    public void handle() {"},
		{"cs-property-auto", "Count", 0, "    public int Count { get; set; }"},
		{"cs-expression-bodied", "Count", 0, "    public int Count => _items.Count;"},
		{"cs-property-same-type-lazy", "Options", 0, "    public Options Options\n    {\n        get { return _options ??= new Options(); }\n    }"},
		{"kotlin-fun", "handle", 0, "fun handle(x: Int): Int {\n    return x\n}"},
		{"ruby-def", "handle", 0, "def handle(x)\n  x\nend"},
		{"ruby-comment-then-def", "handle", 1, "# handle(x) does things\ndef handle(x)\nend"},
		{"ruby-self-noparen-hashkey", "config", 0, "def self.config\n  @config ||= Config.load(config: path)\nend"},
		{"shell-fn", "handle", 0, "handle() {\n  echo hi\n}"},
		{"c-fn-like-macro", "HANDLE", 0, "#define HANDLE(x) ((x) + 1)"},
		{"c-object-macro", "HANDLE", 0, "#define HANDLE 1"},
		{"c-block-comment-unstarred", "handle", 3, "/*\n   handle(x) returns x\n*/\nint handle(int x) {\n  return x;\n}"},
		{"c-split-signature", "handle", 1, "static int\nhandle(int x)\n{"},
		{"elixir-doc-heredoc", "parse", 3, "  @doc \"\"\"\n  parse(input) returns a tree\n  \"\"\"\n  def parse(input) do"},
		{"julia-docstring", "parse", 4, "\"\"\"\n    parse(x)\n\nParse x.\n\"\"\"\nfunction parse(x)"},
		{"go-raw-string-multiline", "handle", 3, "var _ = `\nhandle(x)\n`\nfunc handle() {}"},
		{"haskell-sig", "parse", 0, "parse :: String -> Int\nparse s = length s"},
		{"fsharp-module", "Ledger", 0, "module Fixtures.Ledger\n\ntype Ledger = { Total: int }"},
	}
	bad := 0
	for _, c := range cases {
		lines := strings.Split(c.src, "\n")
		got, ok := DeclarationLineIndex(lines, 0, len(lines)-1, c.symbol)
		mark := "ok"
		if !ok || got != c.want {
			mark = "WRONG"
			bad++
		}
		gl := "<none>"
		if ok {
			gl = lines[got]
		}
		t.Logf("%-32s %-5s picked=%d ok=%v want=%d  picked line %q", c.name, mark, got, ok, c.want, gl)
	}
	t.Logf("wrong picks: %d of %d", bad, len(cases))
}

// REV2: the tail tersify replaces a focus window that SHOWED the real declaration with one at a
// wrongly picked line, so the demoted row no longer prints its declaration (no heads guard here).
func TestRev2TersifyDropsRealDeclOnWrongPick(t *testing.T) {
	cases := []struct {
		name, symbol string
		src          string
		real, focus  int // 0-based
	}{
		{"py-decorator-trailing-comment", "fetch", "@retry  # fetch() may raise\n@lru_cache()\ndef fetch(url, timeout):\n    r = get(url, timeout)\n    return r.json()\n    # end", 2, 2},
		{"cs-lazy-property", "Options", "    public Options Options\n    {\n        get { return _options ??= new Options(); }\n    }\n", 0, 0},
		{"ruby-self-hashkey", "config", "def self.config\n  @config ||= Config.load(config: path)\n  @config.freeze\nend", 0, 0},
	}
	for _, c := range cases {
		lines := strings.Split(strings.TrimSuffix(c.src, "\n"), "\n")
		first := 100
		r := SearchResult{FilePath: "x", StartLine: first, EndLine: first + len(lines) - 1, FocusLine: first + c.focus,
			SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1, SymbolStartLine: first,
			SymbolEndLine: first + len(lines) - 1, SymbolName: c.symbol, Snippet: strings.Join(lines, "\n")}
		old := tersifySearchResult(r, 2)
		neu := tersifySearchResultKeepingDeclaration(r, 2)
		oldHas := strings.Contains(old.Snippet, lines[c.real])
		newHas := strings.Contains(neu.Snippet, lines[c.real])
		t.Logf("%s: old [%d-%d] shows real=%v; new [%d-%d] shows real=%v\n  old: %q\n  new: %q", c.name,
			old.SnippetStartLine, old.SnippetEndLine, oldHas, neu.SnippetStartLine, neu.SnippetEndLine, newHas, old.Snippet, neu.Snippet)
		if oldHas && !newHas {
			t.Errorf("DEFECT %s: demoted row showed its declaration before and drops it now", c.name)
		}
	}
}

// REV2-F premise: sem records the absorbed C# property's declaration at the getter, not the property line.
func TestRev2MergedDeclLinesCSharpLazyProperty(t *testing.T) {
	fileLines := make([]string, 80)
	for i := range fileLines {
		fileLines[i] = "        x();"
	}
	fileLines[9] = "    public void Load(string path)"
	fileLines[39] = "    public Options Options"
	fileLines[40] = "    {"
	fileLines[41] = "        get { return _options ??= new Options(); }"
	fileLines[42] = "    }"
	survivor := spanMergeBody(1, "src/Cfg.cs", 10, 30)
	survivor.SymbolStartLine, survivor.SymbolEndLine, survivor.SymbolName = 10, 30, "Load"
	absorbed := spanMergeBody(2, "src/Cfg.cs", 40, 43)
	absorbed.SymbolStartLine, absorbed.SymbolEndLine, absorbed.SymbolName = 40, 43, "Options"
	_, span, ok := mergedSearchSpanResult([]SearchResult{survivor, absorbed}, []int{0, 1}, fileLines)
	t.Logf("merged=%v MergedDeclLines=%v (real declaration is line 40)", ok, span.MergedDeclLines)
}
