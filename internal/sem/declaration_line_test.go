package sem

import (
	"strings"
	"testing"
)

// An annotation, attribute, decorator, string or tag that spells the name is not where the symbol is
// declared. Each case is a symbol's first lines; want is the index of the declaring line.
func TestDeclarationLineIndexSkipsAnnotationArguments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, symbol string
		lines        []string
		want         int
	}{
		{"java-named", "fooBar", []string{`    @Named("fooBar")`, `    @Inject`, `    public void fooBar(List<Item> items) {`}, 2},
		{"java-annotation-same-line", "fooBar", []string{`    @Named("fooBar") public void fooBar() {`}, 0},
		{"java-annotation-type", "Marker", []string{`@interface Marker {`}, 0},
		{"flask-route", "login", []string{`@app.route("/login", methods=["GET"])`, `def login():`}, 1},
		{"python-decorator-arg", "index", []string{`@register(index)`, `def index(request):`}, 1},
		{"rust-route", "x", []string{`#[route("x")]`, `fn x(req: Request) {`}, 1},
		{"rust-inner-attr", "x", []string{`#![doc = "x"]`, `pub fn x() {}`}, 1},
		{"rust-lifetime", "Foo", []string{`impl<'a> Foo<'a> {`}, 0},
		{"rust-lifetimes", "Foo", []string{`impl<'a, 'b> Foo<'a, 'b> {`}, 0},
		{"rust-ref-lifetime", "parse", []string{`pub fn parse(s: &'a str) -> Foo<'a> {`}, 0},
		{"python-word-string", "run", []string{`    doc = 'call run() now'`, `def run():`}, 1},
		{"csharp-route", "Index", []string{`    [Route("Index")]`, `    [HttpGet]`, `    public IActionResult Index(int page)`}, 2},
		{"csharp-attr-same-line", "Index", []string{`    [HttpGet] public IActionResult Index()`}, 0},
		{"go-struct-tag", "retry", []string{"\tRetry int `json:\"retry\"`", "}", "", "func retry(n int) error {"}, 3},
		{"char-literal", "a", []string{`x := 'a'`, `func a() {`}, 1},
		{"trailing-comment", "load", []string{`init() // calls load`, `func load() {`}, 1},
		{"comment-line", "load", []string{`// load reads it`, `# load`, ` * load`, `func load() {`}, 3},
		{"c-define", "MAX", []string{`#define MAX(a, b) ((a) > (b) ? (a) : (b))`}, 0},
		{"prefer-definition-shape", "run", []string{`    run`, `func run() {`}, 1},
		{"fallback-first-mention", "Config", []string{`public Config`, `    extends Base {`}, 0},
		{"recursive-call-after-decl", "walk", []string{`func walk(n *Node) {`, `	walk(n.Left)`}, 0},
		{"keyword-shape", "Foo", []string{`Foo.bar()`, `class Foo`}, 1},
		{"scope-is-not-shape", "Foo", []string{`Foo::bar();`, `struct Foo`}, 1},
	}
	for _, c := range cases {
		got, ok := DeclarationLineIndex(c.lines, 0, len(c.lines)-1, c.symbol)
		if !ok || got != c.want {
			t.Errorf("%s: DeclarationLineIndex = %d,%v; want %d (%q)", c.name, got, ok, c.want, c.lines[c.want])
		}
	}
}

// A symbol whose name appears only inside literals and annotations has no declaring line: callers
// leave their output as it was instead of anchoring on a mention.
func TestDeclarationLineIndexNoneWhenOnlyMentioned(t *testing.T) {
	t.Parallel()
	for _, lines := range [][]string{
		{`@Named("fooBar")`, `@Inject`},
		{`log.Print("fooBar failed")`},
		{"\tFoo int `json:\"fooBar\"`"},
		{`// fooBar does things`},
		{`[Display("fooBar")]`},
		// Unquoted annotation arguments, even definition-shaped ones, are still annotations.
		{`@Named(fooBar)`},
		{`@Bean(fooBar())`},
		{`#[route(fooBar())]`},
		{`[Display(fooBar(1))]`},
		// Comment lines, even ones that look like a call.
		{`// fooBar(x) does things`},
		{`# fooBar(x) does things`},
		{` * fooBar(x) does things`},
	} {
		if got, ok := DeclarationLineIndex(lines, 0, len(lines)-1, "fooBar"); ok {
			t.Errorf("%q: found a declaration at %d", strings.Join(lines, " | "), got)
		}
	}
	if _, ok := DeclarationLineIndex([]string{"func fooBar() {"}, 0, 5, "fooBar"); !ok {
		t.Errorf("a range past the end is clamped, not refused")
	}
}

// Whole identifiers only, outside literals; a name inside a longer identifier is not an occurrence.
func TestDeclarationNameOccurrences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line, name string
		want       []int
	}{
		{`func foo() { fooBar(); foo() }`, "foo", []int{5, 23}},
		{`s := "foo" + foo`, "foo", []int{13}},
		{"s := `foo` + foo", "foo", []int{13}},
		{`s := "a\"foo" + foo`, "foo", []int{16}},
		{`@Named("foo") void foo() {`, "foo", []int{19}},
		{`$foo = foo`, "foo", []int{7}},
		{`x := foo // foo`, "foo", []int{5}},
	}
	for _, c := range cases {
		got := DeclarationNameOccurrences(c.line, c.name)
		if len(got) != len(c.want) {
			t.Errorf("%q: occurrences %v, want %v", c.line, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: occurrences %v, want %v", c.line, got, c.want)
				break
			}
		}
	}
}
