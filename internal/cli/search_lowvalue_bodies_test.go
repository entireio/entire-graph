package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestSearchLowValueBodyPath(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		path string
		want bool
	}{
		{path: "benches/src/keyed-children/index.js", want: true},
		{path: "bench/run.js", want: true},
		{path: "benchmarks/parse_test.go", want: true},
		{path: "packages/demo/app.tsx", want: true},
		{path: "examples/basic/main.py", want: true},
		{path: "karma.conf.js", want: true},
		{path: "config/webpack.config.js", want: true},
		{path: "tools/ci.karma.js", want: true},
		{path: "src/diff/children.js", want: false},
		{path: "src/component.js", want: false},
		// Segment matching, not substring: a source file whose NAME mentions benchmarking is
		// still source.
		{path: "src/benchmarks_helper.js", want: false},
		{path: "src/exampleRegistry.ts", want: false},
		// The directory rule reads directories only; a file literally called `demo.js` in the
		// library is library code.
		{path: "src/demo.js", want: false},
		{path: "", want: false},
	} {
		if got := searchLowValueBodyPath(testCase.path); got != testCase.want {
			t.Errorf("searchLowValueBodyPath(%q) = %v, want %v", testCase.path, got, testCase.want)
		}
	}
}

// The preactjs__preact-3010 payload shape: a benchmark and the karma config outranked the library,
// took two of the three body slots, and left the gold file as a bodyless locator.
func lowValueBodyResponse() sem.SearchResponse {
	return sem.SearchResponse{Results: []sem.SearchResult{
		{
			Rank: 1, FilePath: "benches/src/keyed-children/index.js", StartLine: 12, EndLine: 29,
			FocusLine: 12, SnippetStartLine: 12, SnippetEndLine: 29, Score: 26.8,
			SymbolName: "render", Kind: "function", Signals: []string{"path", "body", "complete-symbol"},
			Snippet: "export function render(framework, rootDom) {\n\tconst { Main } = getComponents(framework);\n}",
		},
		{
			Rank: 2, FilePath: "karma.conf.js", StartLine: 93, EndLine: 104,
			FocusLine: 95, SnippetStartLine: 93, SnippetEndLine: 104, Score: 25.7,
			SymbolName: "subPkgPath", Kind: "function", Signals: []string{"body", "complete-symbol"},
			Snippet: "const subPkgPath = pkgName => {\n\treturn path.join(__dirname, pkgName);\n};",
		},
		{
			Rank: 3, FilePath: "src/component.js", StartLine: 120, EndLine: 124,
			FocusLine: 120, SnippetStartLine: 120, SnippetEndLine: 124, Score: 24.4,
			SymbolName: "renderComponent", Kind: "function", Signals: []string{"complete-symbol"},
			Snippet: "function renderComponent(component) {\n\tlet vnode = component._vnode;\n}",
		},
		{
			Rank: 4, FilePath: "src/diff/children.js", StartLine: 26, EndLine: 30,
			FocusLine: 26, CommentFocusLine: 19, SnippetStartLine: 26, SnippetEndLine: 30, Score: 22.5,
			Signals: []string{"body", "symbol-usage"},
			Snippet: "export function diffChildren(\n\tparentDom,\n\trenderResult,\n\tnewParentVNode,\n\toldParentVNode,",
		},
		{
			Rank: 5, FilePath: "src/diff/index.js", StartLine: 264, EndLine: 268,
			FocusLine: 264, SnippetStartLine: 264, SnippetEndLine: 268, Score: 24.4,
			SymbolName: "commitRoot", Kind: "function", Signals: []string{"complete-symbol"},
			Snippet: "export function commitRoot(commitQueue, root) {\n\tif (options._commit) options._commit(root);\n}",
		},
	}}
}

func TestWriteTextSearchDeniesBodySlotsToLowValuePaths(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, lowValueBodyResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// The benchmark and the config script keep their rank and their locator, and lose their body.
	// The locator also names the verb that would fetch the body it was denied — a demoted hit is still
	// reachable, and the point of the demotion is the byte cost of the body, not hiding the symbol.
	if !strings.Contains(out, "1. benches/src/keyed-children/index.js:12 render  [body: def render]\n") {
		t.Fatalf("rank 1 should be a locator:\n%s", out)
	}
	if !strings.Contains(out, "2. karma.conf.js:95 subPkgPath  [body: def subPkgPath]\n") {
		t.Fatalf("rank 2 should be a locator:\n%s", out)
	}
	if strings.Contains(out, "getComponents(framework)") || strings.Contains(out, "subPkgPath = pkgName") {
		t.Fatalf("a low-value path still carries a body:\n%s", out)
	}
	// The slots they freed go to real source — including the rank that used to be past the
	// full-snippet tier, which is the point: a demoted hit charges neither a body slot nor a tier
	// position.
	if !strings.Contains(out, "function renderComponent(component)") {
		t.Fatalf("rank 3 lost its body:\n%s", out)
	}
	if !strings.Contains(out, "export function diffChildren(") {
		t.Fatalf("rank 4 did not receive a freed body slot:\n%s", out)
	}
	if !strings.Contains(out, "export function commitRoot(commitQueue, root)") {
		t.Fatalf("rank 5 lost its body:\n%s", out)
	}
	// The re-anchored hit reports BOTH lines, so the comment that put it in the payload is still
	// visible.
	if !strings.Contains(out, "focus=19→26") {
		t.Fatalf("re-anchored hit did not report its origin line:\n%s", out)
	}
}

func TestWriteTextSearchKeepsALowValueBodyWhenItIsAllThereIs(t *testing.T) {
	t.Parallel()
	// One real source hit is not enough to fund the demotion: the rule is "never outrank real
	// source for a body slot", not "never show a body".
	response := sem.SearchResponse{Results: []sem.SearchResult{
		{
			Rank: 1, FilePath: "benches/run.js", StartLine: 1, EndLine: 3, FocusLine: 1,
			SnippetStartLine: 1, SnippetEndLine: 3, Score: 20, SymbolName: "run",
			Signals: []string{"body"}, Snippet: "function run() {\n\tmeasure();\n}",
		},
		{
			Rank: 2, FilePath: "src/only.js", StartLine: 5, EndLine: 6, FocusLine: 5,
			SnippetStartLine: 5, SnippetEndLine: 6, Score: 18, SymbolName: "only",
			Signals: []string{"body"}, Snippet: "function only() {}",
		},
	}}
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "function run() {") {
		t.Fatalf("the only-benchmark payload lost its source:\n%s", out)
	}
}

// The vuejs__core-11870 shape: three ordinary hits already claim the diet's three body slots, and the
// re-anchored hit at rank 3 also carries a complete body. It must NOT take rank 5's slot.
func reanchorFundingResponse() sem.SearchResponse {
	return sem.SearchResponse{Results: []sem.SearchResult{
		{
			Rank: 1, FilePath: "packages/reactivity/src/reactive.ts", StartLine: 140, EndLine: 150,
			FocusLine: 140, SnippetStartLine: 140, SnippetEndLine: 150, Score: 78,
			SymbolName: "shallowReactive", Kind: "function", Signals: []string{"body", "complete-symbol"},
			Snippet: "export function shallowReactive(target) {\n\treturn createReactiveObject(target)\n}",
		},
		{
			Rank: 2, FilePath: "packages/reactivity/src/reactive.ts", StartLine: 257, EndLine: 298,
			FocusLine: 257, SnippetStartLine: 257, SnippetEndLine: 298, Score: 74,
			SymbolName: "createReactiveObject", Kind: "function", Signals: []string{"complete-symbol"},
			Snippet: "function createReactiveObject(target) {\n\treturn new Proxy(target, handlers)\n}",
		},
		{
			Rank: 3, FilePath: "packages/reactivity/src/arrayInstrumentations.ts", StartLine: 12, EndLine: 17,
			FocusLine: 12, CommentFocusLine: 10, BodyFromReanchor: true,
			SnippetStartLine: 12, SnippetEndLine: 17, Score: 70,
			SymbolName: "reactiveReadArray", Kind: "function", Signals: []string{"body", "symbol-usage", "complete-symbol"},
			Snippet: "export function reactiveReadArray(array) {\n\tconst raw = toRaw(array)\n}",
		},
		{
			Rank: 4, FilePath: "packages/reactivity/src/index.ts", StartLine: 28, EndLine: 32,
			FocusLine: 30, SnippetStartLine: 28, SnippetEndLine: 32, Score: 69,
			Signals: []string{"body", "symbol-usage"}, Snippet: "export { shallowReactive }",
		},
		{
			Rank: 5, FilePath: "packages/runtime-core/src/helpers/renderList.ts", StartLine: 54, EndLine: 107,
			FocusLine: 54, SnippetStartLine: 54, SnippetEndLine: 107, Score: 69,
			SymbolName: "renderList", Kind: "function", Signals: []string{"path", "body", "complete-symbol"},
			Snippet: "export function renderList(source, renderItem) {\n\tlet ret\n\treturn ret\n}",
		},
	}}
}

func TestWriteTextSearchNeverFundsAReanchoredBodyByEvictingOne(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, reanchorFundingResponse()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// The body that already existed survives, whole.
	if !strings.Contains(out, "5. packages/runtime-core/src/helpers/renderList.ts:54-107 ") ||
		!strings.Contains(out, "export function renderList(source, renderItem)") {
		t.Fatalf("the re-anchored hit evicted an existing body:\n%s", out)
	}
	// The re-anchored hit yields the slot and keeps the anchor move, which is the part that costs
	// nothing: its locator points at the code line, not at the comment.
	if strings.Contains(out, "export function reactiveReadArray(array)") {
		t.Fatalf("the re-anchored hit took a slot it could not fund:\n%s", out)
	}
	if !strings.Contains(out, "3. packages/reactivity/src/arrayInstrumentations.ts:12 reactiveReadArray  [body: def reactiveReadArray]\n") {
		t.Fatalf("the re-anchored locator lost its code anchor:\n%s", out)
	}
}

func TestWriteTextSearchGivesAReanchoredHitAFreeSlot(t *testing.T) {
	t.Parallel()
	// Same shape with rank 5's body removed: one slot is now genuinely free, so the re-anchored hit
	// takes it. The rule is "never evict", not "never fund".
	response := reanchorFundingResponse()
	response.Results[4].Signals = []string{"path", "body"}
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "export function reactiveReadArray(array)") {
		t.Fatalf("a free slot was not given to the re-anchored hit:\n%s", out)
	}
	if !strings.Contains(out, "focus=10→12") {
		t.Fatalf("the re-anchored body lost its origin line:\n%s", out)
	}
}

func TestWriteTextSearchIsUnchangedWithoutLowValuePaths(t *testing.T) {
	t.Parallel()
	// The regression guard for the tier change: with no low-value hit in the group, the body diet
	// must count exactly what it counted before.
	response := lowValueBodyResponse()
	response.Results[0].FilePath = "src/keyed-children.js"
	response.Results[1].FilePath = "src/pkg-path.js"
	var buf bytes.Buffer
	if err := writeTextSearch(&buf, response); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"getComponents(framework)", "subPkgPath = pkgName", "function renderComponent(component)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in the ordinary payload:\n%s", want, out)
		}
	}
	// Three bodies is the cap, so rank 4 stays a locator and rank 5 keeps the body the allocator
	// bought for it (complete-symbol is exempt from the rank tier, not from the cap).
	if strings.Contains(out, "export function diffChildren(") {
		t.Fatalf("the body cap was breached:\n%s", out)
	}
	if !strings.Contains(out, "4. src/diff/children.js:26\n") {
		t.Fatalf("rank 4 should be a plain locator:\n%s", out)
	}
}
