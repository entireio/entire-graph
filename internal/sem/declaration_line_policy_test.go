package sem

import (
	"strings"
	"testing"
)

// The round-2 finder sweep (TestRev2FinderLanguages) only logs, so it cannot go red. This is the
// same table as an assertion, with two corrections and two stated policies:
//
//   - julia-docstring: the declaration is `function parse(x)`, index 5; the sweep's 4 is the
//     closing `"""`.
//   - haskell-sig (policy: SIGNATURE FIRST): `parse :: String -> Int` names parse before its
//     binding `parse s = ...`. The text fallback takes the first line naming the symbol when no
//     line is definition-shaped, so the type signature is the declaration line.
//   - fsharp-module (policy: MODULE FIRST): `module Fixtures.Ledger` and `type Ledger = ...` both
//     name Ledger; the text fallback cannot tell which symbol it is asked about and takes the
//     first definition-shaped line, the module. With a parser name line the symbol's own token is
//     used instead (TestParserNameLineIsTheNameToken covers both the module and the type).
func TestFinderLanguagesAsserted(t *testing.T) {
	t.Parallel()
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
		{"julia-docstring", "parse", 5, "\"\"\"\n    parse(x)\n\nParse x.\n\"\"\"\nfunction parse(x)"},
		{"go-raw-string-multiline", "handle", 3, "var _ = `\nhandle(x)\n`\nfunc handle() {}"},
		{"haskell-sig", "parse", 0, "parse :: String -> Int\nparse s = length s"},
		{"fsharp-module", "Ledger", 0, "module Fixtures.Ledger\n\ntype Ledger = { Total: int }"},
	}
	for _, c := range cases {
		lines := strings.Split(c.src, "\n")
		got, ok := DeclarationLineIndex(lines, 0, len(lines)-1, c.symbol)
		if !ok || got != c.want {
			picked := "<none>"
			if ok {
				picked = lines[got]
			}
			t.Errorf("%s: picked %d (%q) ok=%v; want %d (%q)", c.name, got, picked, ok, c.want, lines[c.want])
		}
	}
}
