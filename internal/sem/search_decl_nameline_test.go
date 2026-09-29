package sem

import (
	"fmt"
	"strings"
	"testing"
)

func declNameLineRows(annos, body int) []string {
	var lines []string
	for i := 0; i < annos; i++ {
		lines = append(lines, fmt.Sprintf("@Rule%02d(run = true)", i))
	}
	lines = append(lines, "public void run() {")
	for i := 0; i < body; i++ {
		lines = append(lines, fmt.Sprintf("    step%02d();", i))
	}
	return append(lines, "}")
}

// A demoted row keeps its declaration by the parser's name line with no scan bound: 20 annotation
// lines above the name, focus deep in the body, two lines kept — they start at the name line. The
// text fallback (no name line) cannot see past its bound and keeps the focus window.
func TestTersifyKeepsParserNameLineBeyondScanBound(t *testing.T) {
	t.Parallel()
	lines := declNameLineRows(20, 30)
	first := 100
	result := SearchResult{FilePath: "a/Svc.java", StartLine: first, EndLine: first + len(lines) - 1, FocusLine: first + 45,
		SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1, SymbolStartLine: first, SymbolEndLine: first + len(lines) - 1,
		SymbolNameLine: first + 20, SymbolName: "run", Snippet: strings.Join(lines, "\n")}
	got := tersifySearchResultKeepingDeclaration(result, 2)
	if got.SnippetStartLine != first+20 || !strings.HasPrefix(got.Snippet, "public void run() {") {
		t.Fatalf("kept [%d-%d] %q; want the window at the name line %d", got.SnippetStartLine, got.SnippetEndLine, got.Snippet, first+20)
	}
	result.SymbolNameLine = 0
	if got := tersifySearchResultKeepingDeclaration(result, 2); got.Snippet != tersifySearchResult(result, 2).Snippet {
		t.Fatalf("text fallback moved the window past its bound: %q", got.Snippet)
	}
}

// Never less: a focus window that showed part of the declaration region (annotations above the
// name) is replaced only by one that still shows those lines and the name; when both cannot fit,
// the focus window stays.
func TestTersifyNeverDropsShownDeclarationRegion(t *testing.T) {
	t.Parallel()
	pre := []string{"}", ""}
	lines := append(append([]string(nil), pre...), declNameLineRows(3, 12)...)
	first := 10
	base := SearchResult{FilePath: "a/Svc.java", StartLine: first, EndLine: first + len(lines) - 1,
		SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1, SymbolStartLine: first + 2, SymbolEndLine: first + len(lines) - 1,
		SymbolNameLine: first + 5, SymbolName: "run", Snippet: strings.Join(lines, "\n")}
	checked := 0
	for focus := first; focus <= base.SnippetEndLine; focus++ {
		for maxLines := 1; maxLines <= 6; maxLines++ {
			r := base
			r.FocusLine = focus
			terse := tersifySearchResult(r, maxLines)
			got := tersifySearchResultKeepingDeclaration(r, maxLines)
			for line := maxInt(terse.SnippetStartLine, base.SymbolStartLine); line <= minInt(terse.SnippetEndLine, base.SymbolNameLine); line++ {
				checked++
				if line < got.SnippetStartLine || line > got.SnippetEndLine {
					t.Fatalf("focus %d max %d: focus window showed region line %d; kept [%d-%d]", focus, maxLines, line, got.SnippetStartLine, got.SnippetEndLine)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no focus window showed the region: the sweep tests nothing")
	}
}

// A merged span records each absorbed member's declaration by its parser name line, and the start
// of that member's region, so a renderer can keep the whole region.
func TestMergedDeclarationsUseParserNameLines(t *testing.T) {
	t.Parallel()
	fileLines := make([]string, 80)
	for i := range fileLines {
		fileLines[i] = "        x();"
	}
	fileLines[9] = "    public void Load(string path)"
	for i := 39; i < 58; i++ {
		fileLines[i] = fmt.Sprintf("    [Rule%02d(Options = 1)]", i)
	}
	fileLines[58] = "    public Options Options"
	survivor := spanMergeBody(1, "src/Cfg.cs", 10, 30)
	survivor.SymbolStartLine, survivor.SymbolEndLine, survivor.SymbolName, survivor.SymbolNameLine = 10, 30, "Load", 10
	absorbed := spanMergeBody(2, "src/Cfg.cs", 40, 62)
	absorbed.SymbolStartLine, absorbed.SymbolEndLine, absorbed.SymbolName, absorbed.SymbolNameLine = 40, 62, "Options", 59
	_, span, ok := mergedSearchSpanResult([]SearchResult{survivor, absorbed}, []int{0, 1}, fileLines)
	if !ok {
		t.Fatal("not merged")
	}
	if len(span.MergedDeclLines) != 1 || span.MergedDeclLines[0] != 59 || len(span.MergedDeclStarts) != 1 || span.MergedDeclStarts[0] != 40 {
		t.Fatalf("MergedDeclLines=%v MergedDeclStarts=%v; want [59] [40]", span.MergedDeclLines, span.MergedDeclStarts)
	}
	absorbed.SymbolNameLine = 0
	_, span, _ = mergedSearchSpanResult([]SearchResult{survivor, absorbed}, []int{0, 1}, fileLines)
	if span.MergedDeclStarts != nil {
		t.Fatalf("text-fallback declarations carry region starts %v", span.MergedDeclStarts)
	}
}
