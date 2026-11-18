package sem

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	treesittertsx "github.com/smacker/go-tree-sitter/typescript/tsx"
)

// Issue #409: a bare `&` in JSX text is valid JSX (TypeScript and Babel keep it
// as text), but the bundled grammars reject it, so valid .tsx/.jsx/.js files
// were reported as E_PARSE_ERROR and skipped downstream. Each case failed to
// parse before the reparse; each must now parse cleanly in every JSX-bearing
// grammar route and keep every declaration.
func TestJSXBareAmpersandParses(t *testing.T) {
	t.Parallel()
	// Each entry is what an exported function component named Component returns.
	returns := []string{
		"<p>Terms & Privacy</p>",
		"(\n    <nav>\n      <a href=\"/terms\">Terms</a> & <a href=\"/privacy\">Privacy</a>\n    </nav>\n  )",
		"<span>&</span>",
		"<code>a && b</code>",
		"<p>R&D {format(n)} more</p>",
		"<p>x &amp; y & z &#38; w</p>",
		"(\n    <p>\n      Line one &\n      line two\n    </p>\n  )",
		// These two derail recovery: on the unmasked source the opening tag
		// lands in an ERROR at program level, not under a jsx_element.
		"<p>Tom & Jerry & Co</p>",
		"<p>&nbsp and more</p>",
		// The grammar caps a decimal reference at five digits, so an emoji
		// written as one (&#128276; is a bell) fails like a bare `&`; React's
		// fixtures/flight-ssr-bench components hit this.
		"<span>&#128276;</span>",
		// Attribute strings: a URL query string, several in one value, and an
		// attribute beside JSX text.
		"<link href=\"https://fonts.example/css?family=Inter&display=swap\" rel=\"stylesheet\" />",
		"<a href='/s?q=1&page=2&sort=new'>next</a>",
		"<a title=\"R&D\">Q & A</a>",
	}
	type fixture struct {
		src   string
		names []string
	}
	fixtures := []fixture{
		{src: "export const Faq = () => <>Q & A</>\n", names: []string{"Faq"}},
		// Two derailing elements: the second is only recognised as JSX once
		// the first is masked, so this needs more than one pass.
		{
			src: "export function First() {\n  return <p>Tom & Jerry & Co</p>\n}\n" +
				"export class Second {\n  render() {\n    return <i>Q & A & more</i>\n  }\n}\n" +
				"export function Third() {\n  return <b>&nbsp and</b>\n}\n",
			names: []string{"First", "Second", "Second.render", "Third"},
		},
	}
	for _, jsx := range returns {
		fixtures = append(fixtures, fixture{
			src:   "export function Component() {\n  return " + jsx + "\n}\n",
			names: []string{"Component"},
		})
	}
	// .tsx parses with the TSX grammar plus the TSX mask, .jsx with the TSX
	// grammar, .js with tree-sitter-javascript, and Flow-typed .js with the TSX
	// grammar plus the Flow mask.
	routes := []struct {
		path   string
		prefix string
	}{
		{"Page.tsx", ""},
		{"Page.jsx", ""},
		{"page.js", ""},
		{"flow.js", "// @flow\n"},
	}
	for _, tc := range fixtures {
		for _, route := range routes {
			requireJSXAmpersandRouteParses(t, route.path, route.prefix+tc.src, tc.names)
		}
	}
	if IdentityRevision == "8" {
		t.Fatal("the bare-ampersand correction must invalidate revision-8 cached records")
	}
}

// requireJSXAmpersandRouteParses checks one fixture on one route: the raw parse
// has errors, so the fixture pins the reparse, and the full parse is clean and
// keeps every name in names.
func requireJSXAmpersandRouteParses(t *testing.T, path, src string, names []string) {
	t.Helper()
	if !parseErrorsWithGrammar(t, path, src) {
		t.Fatalf("%s %q: fixture parses without the reparse, so it pins nothing", path, src)
	}
	entities := requireNoTSParseError(t, path, src)
	got := map[string]bool{}
	for _, e := range entities {
		got[e.Name] = true
	}
	for _, want := range names {
		if !got[want] {
			t.Errorf("%s %q: %s missing from %+v", path, src, want, entities)
		}
	}
}

// parseErrorsWithGrammar parses src exactly as ParseWithStatusCtx would before
// the reparse (same grammar, same mask) and reports whether the tree has
// errors.
func parseErrorsWithGrammar(t *testing.T, path, src string) bool {
	t.Helper()
	grammar := treesittertsx.GetLanguage()
	parseSrc := src
	switch {
	case strings.HasSuffix(path, ".tsx"):
		parseSrc = maskTSXUnsupportedSyntax(src)
	case looksLikeFlowJavaScript(src):
		parseSrc = maskFlowJavaScriptUnsupportedSyntax(src)
	case strings.HasSuffix(path, ".js"):
		grammar = javascript.GetLanguage()
	}
	return parseTree(t, grammar, []byte(parseSrc)).RootNode().HasError()
}

func parseTree(t *testing.T, grammar *sitter.Language, src []byte) *sitter.Tree {
	t.Helper()
	_, tree := newParse(t, grammar, src)
	return tree
}

// newParse returns a parser for grammar and its parse of src. Both are closed at
// cleanup; closing a tree twice is safe, so a test may also close the tree, or
// hand it to code that does.
func newParse(t *testing.T, grammar *sitter.Language, src []byte) (*sitter.Parser, *sitter.Tree) {
	t.Helper()
	parser := sitter.NewParser()
	t.Cleanup(parser.Close)
	parser.SetLanguage(grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		t.Fatalf("parse failed: %v", err)
	}
	t.Cleanup(tree.Close)
	return parser, tree
}

// Only `&` in JSX text and in an attribute string the grammar rejected is
// masked. Bitwise `&`, `&&`, `&=`, an intersection type, an `&` inside a JSX
// expression or an attribute string that parsed, and an `&` after a closing tag
// or a self-closing element used as an expression must keep their bytes, even
// in a file whose tree has errors.
func TestJSXBareAmpersandOffsetsSkipCode(t *testing.T) {
	t.Parallel()
	src := "type Both = A & B\n" +
		"let m = flags & 4\n" +
		"m &= 1\n" +
		"const ok = a && b\n" +
		"const x = <p title=\"a & b\" data-q=\"x&y\">{a & b} Terms & Privacy</p>\n" +
		"const y = <br /> & z\n" +
		"const w = <p>done</p> & q\n" +
		"function broken( {\n"
	tree := parseTree(t, treesittertsx.GetLanguage(), []byte(src))
	if !tree.RootNode().HasError() {
		t.Fatal("fixture must have errors, or the walk is never reached")
	}
	offsets := jsxBareAmpersandOffsets(context.Background(), tree.RootNode(), []byte(src))
	sort.Ints(offsets)
	want := []int{strings.Index(src, "x&y") + 1, strings.Index(src, "Terms &") + len("Terms ")}
	if !slices.Equal(offsets, want) {
		t.Fatalf("offsets = %v, want only the attribute and JSX text ampersands at %v", offsets, want)
	}
}

// Error recovery can leave several anchors in one text run, each ending right
// before a bare `&`. A scan from each must not cover the rest of the run again:
// 4,000 anchors in a 12 KB run used to collect 8,002,000 offsets. Every bare
// `&` must be collected exactly once, whichever order the anchors come in, and
// a character reference in the run must still be kept.
func TestJSXTextRunScansEachByteOnce(t *testing.T) {
	t.Parallel()
	src := []byte(strings.Repeat("&; ", 4000) + "&amp; <")
	var anchors, want []int
	for pos := range src {
		if src[pos] == '&' && !jsxCharacterReferenceAt(src, pos) {
			anchors = append(anchors, pos)
			want = append(want, pos)
		}
	}
	// A whitespace anchor too: its scan skips to the next `&`.
	anchors = append(anchors, 2)
	forward := slices.Clone(anchors)
	reverse := slices.Clone(anchors)
	slices.Reverse(reverse)
	for name, order := range map[string][]int{"forward": forward, "reverse": reverse} {
		scan := jsxAmpersandScan{src: src}
		for _, start := range order {
			scan.appendTextRun(start)
		}
		got := slices.Clone(scan.offsets)
		sort.Ints(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %d offsets, want each of the %d bare ampersands once", name, len(got), len(want))
		}
	}
}

// A real syntax error elsewhere in the file still fails the parse, and its
// detail names that error rather than the masked ampersand.
func TestJSXBareAmpersandKeepsRealParseErrors(t *testing.T) {
	t.Parallel()
	src := "export function Footer() {\n  return <p>Terms & Privacy</p>\n}\nfunction broken( {\n"
	_, _, status := TreeSitterParser{}.ParseWithStatus("Footer.tsx", src)
	if !status.ParseError || status.Code != "E_PARSE_ERROR" {
		t.Fatalf("status = %+v, want E_PARSE_ERROR", status)
	}
	if strings.Contains(status.Detail, "& Privacy") {
		t.Fatalf("detail still blames the JSX text ampersand: %s", status.Detail)
	}
}

// The reparse must never write into the source it is handed: ParseWithStatusCtx
// keeps that slice as the entity source, so a masked byte there would leak into
// signatures and bodies. The masked copy must keep every byte offset.
func TestReparseJSXBareAmpersandsCopiesSource(t *testing.T) {
	t.Parallel()
	src := []byte("const a = <p>Tom & Jerry & Co</p>\n")
	original := append([]byte(nil), src...)
	parser, tree := newParse(t, treesittertsx.GetLanguage(), src)
	tree, masked, err := reparseJSXBareAmpersands(context.Background(), parser, tree, src)
	defer tree.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, original) {
		t.Fatalf("source was written: %q", src)
	}
	if tree.RootNode().HasError() {
		t.Fatalf("reparse left errors: %s", tree.RootNode().String())
	}
	if want := "const a = <p>Tom   Jerry   Co</p>\n"; string(masked) != want {
		t.Fatalf("masked = %q, want %q", masked, want)
	}
}

// A cancelled context stops the reparse, hands back the parse it was given, and
// says it was cancelled, so ParseWithStatusCtx reports E_PARSE_TIMEOUT rather
// than an E_PARSE_ERROR that blames the `&`.
func TestReparseJSXBareAmpersandsStopsOnCancel(t *testing.T) {
	t.Parallel()
	src := []byte("const a = <p>Terms & Privacy</p>\n")
	parser, tree := newParse(t, treesittertsx.GetLanguage(), src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, gotSrc, err := reparseJSXBareAmpersands(ctx, parser, tree, src)
	if got != tree || !bytes.Equal(gotSrc, src) {
		t.Fatal("cancelled reparse replaced the tree")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A file whose only errors are a real syntax error has nothing to mask, so the
// reparse returns the parse unchanged and no error: the caller then reports
// E_PARSE_ERROR from it, as before.
func TestReparseJSXBareAmpersandsLeavesRealErrors(t *testing.T) {
	t.Parallel()
	src := []byte("function broken( {\n")
	parser, tree := newParse(t, treesittertsx.GetLanguage(), src)
	got, gotSrc, err := reparseJSXBareAmpersands(context.Background(), parser, tree, src)
	if got != tree || !bytes.Equal(gotSrc, src) || err != nil {
		t.Fatalf("reparse changed a parse it had nothing to mask in: err = %v", err)
	}
}

// jsxCharacterReferenceAt must accept exactly what the grammar's
// html_character_reference accepts: a reference it wrongly accepted would be
// left in place and keep the error, one it wrongly rejected would be blanked.
// Each case is checked against the bundled grammars themselves.
func TestJSXCharacterReferenceMatchesGrammar(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"&amp;":                             true,
		"&AMP;":                             true,
		"&#38;":                             true,
		"&#12345;":                          true,
		"&#123456;":                         false,
		"&#x26;":                            true,
		"&#X2a;":                            true,
		"&#x123456;":                        true,
		"&#x1234567;":                       false,
		"&#x;":                              false,
		"&#;":                               false,
		"&;":                                false,
		"&amp":                              false,
		"&a1;":                              false,
		"& amp;":                            false,
		"&" + strings.Repeat("a", 30) + ";": true,
		"&" + strings.Repeat("a", 31) + ";": false,
	}
	for ref, want := range cases {
		if got := jsxCharacterReferenceAt([]byte(ref), 0); got != want {
			t.Errorf("jsxCharacterReferenceAt(%q) = %v, want %v", ref, got, want)
		}
		for _, grammar := range []*sitter.Language{treesittertsx.GetLanguage(), javascript.GetLanguage()} {
			src := []byte("const a = <p>" + ref + "</p>\n")
			if parsed := !parseTree(t, grammar, src).RootNode().HasError(); parsed != want {
				t.Errorf("grammar parses %q cleanly = %v, matcher says %v", ref, parsed, want)
			}
		}
	}
}
