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

// Text fallback, wrong pick ABOVE the real declaration: every line the focus window showed within
// the scan bound that mentions the name is kept, or the focus window stays.
func TestTersifyFallbackKeepsShownMentions(t *testing.T) {
	t.Parallel()
	lines := []string{"    val run = Runner()", "", "    fun run() {"}
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf("        step%02d()", i))
	}
	lines = append(lines, "    }")
	first := 30
	base := SearchResult{FilePath: "a/K.kt", StartLine: first, EndLine: first + len(lines) - 1,
		SnippetStartLine: first, SnippetEndLine: first + len(lines) - 1, SymbolStartLine: first, SymbolEndLine: first + len(lines) - 1,
		SymbolName: "run", Snippet: strings.Join(lines, "\n")}
	if decl, ok, parsed := searchResultDeclarationLine(base); !ok || parsed || decl != first {
		t.Fatalf("premise: the text fallback should pick the property line %d, got %d ok=%v parsed=%v", first, decl, ok, parsed)
	}
	checked := 0
	for focus := first; focus <= base.SnippetEndLine; focus++ {
		for maxLines := 1; maxLines <= 4; maxLines++ {
			r := base
			r.FocusLine = focus
			terse := tersifySearchResult(r, maxLines)
			got := tersifySearchResultKeepingDeclaration(r, maxLines)
			for line := terse.SnippetStartLine; line <= terse.SnippetEndLine; line++ {
				if !searchLineMentionsName(lines[line-first], "run") {
					continue
				}
				checked++
				if line < got.SnippetStartLine || line > got.SnippetEndLine {
					t.Fatalf("focus %d max %d: the focus window showed %q; kept [%d-%d]", focus, maxLines, lines[line-first], got.SnippetStartLine, got.SnippetEndLine)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no focus window showed a mention: the sweep tests nothing")
	}
}

// Known outside the snippet: no declaration here, and no text guess. The fallback control shows the
// guess the known name line prevents.
func TestSymbolDeclarationLineKnownOutsideIsNotGuessed(t *testing.T) {
	t.Parallel()
	lines := []string{"decltype(run())", "{"}
	r := SearchResult{SymbolStartLine: 10, SymbolEndLine: 20, SymbolNameLine: 12, SymbolName: "run", Language: "C++"}
	if line, ok, _ := searchSymbolDeclarationLine(lines, 10, r); ok {
		t.Fatalf("known name line outside the snippet was replaced by line %d", line)
	}
	r.SymbolNameLine = 25 // outside the span: invalid
	if line, ok, _ := searchSymbolDeclarationLine(lines, 10, r); ok {
		t.Fatalf("invalid name line fell back to line %d", line)
	}
	r.SymbolNameLine = 0
	if line, ok, parsed := searchSymbolDeclarationLine(lines, 10, r); !ok || parsed || line != 10 {
		t.Fatalf("control: absent name line should use the text fallback (got %d ok=%v parsed=%v)", line, ok, parsed)
	}
}

// Round 2's TestRev2MergedDeclLinesCSharpLazyProperty only logs. Asserted: with no name lines, the
// absorbed C# property whose type equals its name records its declaration line (40), not the lazy
// getter below it; with the parser's name line, that line.
func TestMergedDeclLinesCSharpLazyPropertyAsserted(t *testing.T) {
	t.Parallel()
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
	if !ok || len(span.MergedDeclLines) != 1 || span.MergedDeclLines[0] != 40 {
		t.Fatalf("merged=%v MergedDeclLines=%v; want [40]", ok, span.MergedDeclLines)
	}
	absorbed.SymbolNameLine = 40
	_, span, _ = mergedSearchSpanResult([]SearchResult{survivor, absorbed}, []int{0, 1}, fileLines)
	if len(span.MergedDeclLines) != 1 || span.MergedDeclLines[0] != 40 || len(span.MergedDeclStarts) != 1 || span.MergedDeclStarts[0] != 40 {
		t.Fatalf("with a name line: MergedDeclLines=%v MergedDeclStarts=%v; want [40] [40]", span.MergedDeclLines, span.MergedDeclStarts)
	}
}

// The sem fallback lexes with the file's language (see TestAgentBlockFallbackUsesTheFilesLanguage).
func TestSymbolDeclarationLineFallbackUsesTheFilesLanguage(t *testing.T) {
	t.Parallel()
	lines := []string{`let s = r"\"; fn run() {`, "}"}
	r := SearchResult{SymbolStartLine: 5, SymbolEndLine: 6, SymbolName: "run", Language: "Rust", FilePath: "src/lib.rs"}
	if line, ok, _ := searchSymbolDeclarationLine(lines, 5, r); !ok || line != 5 {
		t.Fatalf("Rust: %d,%v; want 5", line, ok)
	}
	r.Language, r.FilePath = "", ""
	if _, ok, _ := searchSymbolDeclarationLine(lines, 5, r); ok {
		t.Fatal("control: with no language the raw string masks the rest of the line")
	}
}
