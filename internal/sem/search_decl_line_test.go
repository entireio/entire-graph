package sem

import (
	"fmt"
	"strings"
	"testing"
)

// declTailResult is one annotated Java method, 3 annotation/decl lines then a 40-line body, with the
// focus 25 lines below the declaration: a row the tail tersify used to cut to two body lines.
func declTailResult(first int) (SearchResult, []string) {
	lines := []string{
		"    @Override",
		`    @SuppressWarnings("unchecked")`,
		"    public void processBatch(List<Item> items) throws IOException {",
	}
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("        step%02d(items);", i))
	}
	lines = append(lines, "    }")
	end := first + len(lines) - 1
	return SearchResult{
		Rank: 8, Score: 20, FilePath: "src/Batch.java",
		StartLine: first, EndLine: end, FocusLine: first + 2 + 25,
		SnippetStartLine: first, SnippetEndLine: end,
		SymbolStartLine: first, SymbolEndLine: end, SymbolName: "processBatch", Kind: "method",
		Signals: []string{}, Snippet: strings.Join(lines, "\n"),
	}, lines
}

func TestTersifyKeepsDeclarationForDemotedTail(t *testing.T) {
	t.Parallel()
	result, lines := declTailResult(40)
	terse := tersifySearchResultKeepingDeclaration(result, searchEnclosureTailSnippetLines)
	if terse.SnippetStartLine != 42 || terse.SnippetEndLine != 43 {
		t.Fatalf("tail kept %d-%d, want 42-43 (the named line, stepping over the annotations)", terse.SnippetStartLine, terse.SnippetEndLine)
	}
	if want := strings.Join(lines[2:4], "\n"); terse.Snippet != want {
		t.Fatalf("tail snippet is not the verbatim lines 42-43:\n%q\nwant\n%q", terse.Snippet, want)
	}
	if terse.FocusLine != result.FocusLine {
		t.Fatalf("focus moved %d -> %d; it reports where the query matched", result.FocusLine, terse.FocusLine)
	}
	// The row still validates: the contract bounds FocusLine by StartLine/EndLine, not the snippet.
	if terse.FocusLine < terse.StartLine || terse.FocusLine > terse.EndLine ||
		terse.SnippetStartLine < terse.StartLine || terse.SnippetEndLine > terse.EndLine {
		t.Fatalf("row violates the response contract: %+v", terse)
	}
	if got, want := len(strings.Split(terse.Snippet, "\n")), searchEnclosureTailSnippetLines; got != want {
		t.Fatalf("tail holds %d lines, want %d", got, want)
	}
}

func TestTersifyKeepingDeclarationUnchangedWhenWindowHoldsIt(t *testing.T) {
	t.Parallel()
	result, _ := declTailResult(40)
	// Focus on the named line (window 41-42) or one below it (window 42-43): the focus window already
	// holds the declaration, so the row is exactly what tersifySearchResult returns.
	for _, focus := range []int{42, 43} {
		result.FocusLine = focus
		if got, want := tersifySearchResultKeepingDeclaration(result, 2), tersifySearchResult(result, 2); got.Snippet != want.Snippet ||
			got.SnippetStartLine != want.SnippetStartLine {
			t.Fatalf("focus %d: changed a tail that already showed its declaration: %d-%d vs %d-%d", focus, got.SnippetStartLine, got.SnippetEndLine, want.SnippetStartLine, want.SnippetEndLine)
		}
	}
	// No symbol start inside the snippet, or no line naming the symbol: unchanged.
	outside, _ := declTailResult(40)
	outside.SymbolStartLine = 10
	if got, want := tersifySearchResultKeepingDeclaration(outside, 2), tersifySearchResult(outside, 2); got.SnippetStartLine != want.SnippetStartLine {
		t.Fatalf("re-anchored on a declaration outside the snippet")
	}
	unnamed, _ := declTailResult(40)
	unnamed.SymbolName = "somethingElse"
	if got, want := tersifySearchResultKeepingDeclaration(unnamed, 2), tersifySearchResult(unnamed, 2); got.SnippetStartLine != want.SnippetStartLine {
		t.Fatalf("re-anchored with no line naming the symbol")
	}
	// A snippet already within maxLines is untouched.
	short, _ := declTailResult(40)
	short.Snippet, short.SnippetEndLine = strings.Join(strings.Split(short.Snippet, "\n")[:2], "\n"), 41
	if got := tersifySearchResultKeepingDeclaration(short, 2); got.Snippet != short.Snippet {
		t.Fatalf("changed a snippet that needed no tersify")
	}
}

// The allocator's tail demotion is where the tail rows are cut; it must keep the declaration.
func TestPlanWithDemotionKeepsTailDeclaration(t *testing.T) {
	t.Parallel()
	result, lines := declTailResult(40)
	result.Rank = 1
	plan, _, _, _ := planWithDemotionFrom([]SearchResult{result}, make([]searchEnclosure, 1), 1<<20, 0, 0, 2, 0)
	if !strings.HasPrefix(plan[0].Snippet, lines[2]) {
		t.Fatalf("demoted tail lost its declaration:\n%s", plan[0].Snippet)
	}
}

func TestMergedSpanRecordsAbsorbedDeclarations(t *testing.T) {
	t.Parallel()
	const path = "pkg/store.go"
	fileLines := make([]string, 120)
	for i := range fileLines {
		fileLines[i] = fmt.Sprintf("\tbody %d", i+1)
	}
	fileLines[9] = "func first() {"         // line 10
	fileLines[39] = "// @annotation-ish"    // line 40: the absorbed member's first line
	fileLines[40] = "func second() error {" // line 41: its named line
	survivor := spanMergeBody(1, path, 10, 30)
	survivor.SymbolStartLine, survivor.SymbolEndLine, survivor.SymbolName = 10, 30, "first"
	absorbed := spanMergeBody(2, path, 40, 60)
	absorbed.SymbolStartLine, absorbed.SymbolEndLine, absorbed.SymbolName = 40, 60, "second"
	unnamed := spanMergeBody(3, path, 62, 70)
	unnamed.SymbolStartLine, unnamed.SymbolEndLine, unnamed.SymbolName = 62, 70, "nowhere"
	results := []SearchResult{survivor, absorbed, unnamed}
	_, span, ok := mergedSearchSpanResult(results, []int{0, 1, 2}, fileLines)
	if !ok {
		t.Fatal("not merged")
	}
	if fmt.Sprint(span.MergedDeclLines) != "[41]" {
		t.Fatalf("MergedDeclLines = %v, want [41] (the absorbed member's named line; not the survivor's; none for a member with no named line)", span.MergedDeclLines)
	}
	// A plain ranking carries none.
	if merged, _, _ := mergeSameFileSearchSpans([]SearchResult{survivor}, spanMergeFile(path, 120), 0); merged[0].MergedDeclLines != nil {
		t.Fatal("an unmerged result carries MergedDeclLines")
	}
}

// A decorator or annotation ARGUMENT that spells an absorbed member's name is not its declaration:
// the recorded line is the def/signature below it (DeclarationLineIndex), not `@app.route("/logout")`.
func TestMergedSpanRecordsDeclarationNotAnnotationArgument(t *testing.T) {
	t.Parallel()
	const path = "app/views.py"
	fileLines := make([]string, 120)
	for i := range fileLines {
		fileLines[i] = fmt.Sprintf("    step_%d()", i+1)
	}
	fileLines[9] = `@app.route("/login")`   // line 10: the survivor's first line
	fileLines[10] = "def login():"          // line 11
	fileLines[39] = `@app.route("/logout")` // line 40: the absorbed member's first line
	fileLines[40] = "def logout():"         // line 41: its declaration
	survivor := spanMergeBody(1, path, 10, 30)
	survivor.SymbolStartLine, survivor.SymbolEndLine, survivor.SymbolName = 10, 30, "login"
	absorbed := spanMergeBody(2, path, 40, 60)
	absorbed.SymbolStartLine, absorbed.SymbolEndLine, absorbed.SymbolName = 40, 60, "logout"
	_, span, ok := mergedSearchSpanResult([]SearchResult{survivor, absorbed}, []int{0, 1}, fileLines)
	if !ok {
		t.Fatal("not merged")
	}
	if fmt.Sprint(span.MergedDeclLines) != "[41]" {
		t.Fatalf("MergedDeclLines = %v, want [41] (def logout, not the decorator naming it)", span.MergedDeclLines)
	}
}
