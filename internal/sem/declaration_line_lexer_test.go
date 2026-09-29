package sem

import (
	"strings"
	"testing"
)

// The text fallback carries lexical state across lines: a construct that opens on one line and
// closes on a later one is not code on any line in between. Each case holds a line that LOOKS like
// the declaration inside such a construct, above the real one.
func TestDeclarationLineIndexCarriesLexicalStateAcrossLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{"block-comment-unstarred", []string{"/*", "   run(x) returns x", "*/", "int run(int x) {"}, 3},
		{"python-docstring-above", []string{`x = """`, "def run():", `"""`, "def run():"}, 3},
		{"python-single-quote-triple", []string{"x = '''", "def run():", "'''", "def run():"}, 3},
		{"go-raw-string", []string{"src := `", "func run() {}", "`", "func run() {}"}, 3},
		{"rust-raw-hashes", []string{`let s = r#"`, `fn run() "quoted" {}`, `"#;`, "fn run() {}"}, 3},
		{"csharp-verbatim", []string{`var s = @"`, `void run() {}`, `""still text""`, `";`, "void run() {}"}, 4},
		{"java-annotation-args", []string{"@Policy(", "    run = true,", ")", "public void run() {}"}, 3},
		{"rust-attribute-args", []string{"#[cfg(", "    run(x)", ")]", "fn run() {}"}, 3},
		{"annotation-bracket-in-string", []string{`@Doc(")", `, "    run = 1,", ")", "void run() {}"}, 3},
		{"python-trailing-hash-comment", []string{"@retry  # run() may raise", "def run():"}, 1},
		{"python-code-then-trailing-hash", []string{"x = compute()  # run(x) later", "def run(x):"}, 1},
		{"extends-is-a-use", []string{"class Child extends Options {", "}", "class Options {"}, 2},
		{"return-is-a-use", []string{"    return Options", "}", "type Options struct {"}, 2},
		{"csharp-type-equals-name", []string{"    public Options Options", "    {", "        get { return _o ??= new Options(); }"}, 0},
		{"ruby-receiver", []string{"def self.config", "  @config ||= load(config: path)"}, 0},
		{"fsharp-module-path", []string{"module Fixtures.Ledger", "", "type Ledger = { Total: int }"}, 0},
	}
	for _, c := range cases {
		got, ok := DeclarationLineIndex(c.lines, 0, len(c.lines)-1, lineName(c.lines[c.want]))
		if !ok || got != c.want {
			t.Errorf("%s: DeclarationLineIndex = %d,%v; want %d (%q)", c.name, got, ok, c.want, c.lines[c.want])
		}
	}
}

// lineName picks the symbol name a case is about from its declaring line.
func lineName(line string) string {
	for _, name := range []string{"Options", "config", "Ledger", "run"} {
		if strings.Contains(line, name) {
			return name
		}
	}
	return ""
}

// Identifier boundaries are Unicode-aware: a longer identifier in another script does not contain
// an occurrence of an ASCII name, and a Unicode name matches as a whole.
func TestDeclarationNameOccurrencesUnicodeBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line, name string
		want       int
	}{
		{"func runé() {}", "run", 0},
		{"func érun() {}", "run", 0},
		{"func run_2() {}", "run", 0},
		{"func run() {}", "run", 1},
		{"func runé() {}", "runé", 1},
		{"def naïve(x): naïve2(x)", "naïve", 1},
		{"x := 数据run()", "run", 0},
	}
	for _, c := range cases {
		if got := len(DeclarationNameOccurrences(c.line, c.name)); got != c.want {
			t.Errorf("%q in %q: %d occurrences, want %d", c.name, c.line, got, c.want)
		}
	}
}

// Masking preserves byte offsets, so an occurrence's offset is its offset in the source line.
func TestDeclarationCodeLinesKeepOffsets(t *testing.T) {
	t.Parallel()
	lines := []string{`s := "é" + run() /* é`, `é */ run()`}
	code := DeclarationCodeLines(lines, 0, 1)
	for i := range lines {
		if len(code[i]) != len(lines[i]) {
			t.Fatalf("line %d: masked %d bytes from %d", i, len(code[i]), len(lines[i]))
		}
	}
	if got := DeclarationNameOccurrences(lines[0], "run"); len(got) != 1 || lines[0][got[0]:got[0]+3] != "run" {
		t.Fatalf("offsets %v do not point at run in %q", got, lines[0])
	}
	if strings.Contains(code[1][:4], "é") || !strings.Contains(code[1], "run()") {
		t.Fatalf("second line masked as %q", code[1])
	}
}
