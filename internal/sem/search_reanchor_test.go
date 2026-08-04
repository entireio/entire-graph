package sem

import (
	"strings"
	"testing"
)

func TestSearchLineIsComment(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		line string
		want bool
	}{
		{line: "// a line comment", want: true},
		{line: "\t * @param {Array} commitQueue List of components", want: true},
		{line: "/** doc block opener", want: true},
		{line: "     */", want: true},
		{line: "# a python comment", want: true},
		{line: "#no space is still a comment", want: true},
		{line: "-- a sql comment", want: true},
		{line: `    """A docstring.`, want: true},
		{line: "<!-- an html comment", want: true},
		{line: ";; an elisp comment", want: true},
		{line: "", want: false},
		{line: "   ", want: false},
		{line: "export function diffChildren(", want: false},
		{line: "\t\t\tmodel.LabelName(\"__meta\"): model.LabelValue(\"true\"),", want: false},
		// `#` is prose in Python and program text in C. Getting this wrong re-anchors a hit
		// away from the very directive the query matched.
		{line: "#include <stdio.h>", want: false},
		{line: "#  define MAX 8", want: false},
		{line: "#ifndef GUARD_H", want: false},
		{line: "#endif", want: false},
		{line: "#pragma once", want: false},
		{line: "#[derive(Debug)]", want: false},
		{line: "#![allow(dead_code)]", want: false},
	} {
		if got := searchLineIsComment(testCase.line); got != testCase.want {
			t.Errorf("searchLineIsComment(%q) = %v, want %v", testCase.line, got, testCase.want)
		}
	}
}

// The JSDoc shape from preactjs__preact-3010: the query matched an `@param` line 7 lines above the
// declaration that comment documents.
const reanchorJSDocFile = `import { diff } from './index';

/**
 * Diff the children of a virtual node
 * @param {Array<Component>} commitQueue List of components
 * which have callbacks to invoke in commitRoot
 */
export function diffChildren(parentDom, commitQueue) {
	let i;
	// the loop below is where the fix lands
	for (i = 0; i < 10; i++) {
		commitQueue.push(i);
	}
}
`

func TestSearchDocReanchorTarget(t *testing.T) {
	t.Parallel()
	lines := strings.Split(reanchorJSDocFile, "\n")
	for _, testCase := range []struct {
		name  string
		focus int
		want  int
		ok    bool
	}{
		{name: "an @param line moves to the declaration below the block", focus: 6, want: 8, ok: true},
		{name: "the block opener moves to the same declaration", focus: 3, want: 8, ok: true},
		{name: "a comment inside a body moves to the next statement", focus: 10, want: 11, ok: true},
		{name: "a code line is not re-anchored at all", focus: 8, ok: false},
		{name: "a line past the end is refused", focus: len(lines) + 5, ok: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, ok := searchDocReanchorTarget(lines, testCase.focus)
			if ok != testCase.ok || (ok && got != testCase.want) {
				t.Fatalf("target(%d) = %d/%v, want %d/%v", testCase.focus, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestSearchDocReanchorTargetStopsAtADetachedComment(t *testing.T) {
	t.Parallel()
	// Two blank lines mean the comment and the code below it are separate things, so there is
	// nothing the comment documents and the hit is left where it matched.
	lines := strings.Split("// a note about nothing in particular\n\n\ncode();\n", "\n")
	if got, ok := searchDocReanchorTarget(lines, 1); ok {
		t.Fatalf("crossed a two-line gap to %d", got)
	}
	// One blank line is formatting inside a doc block, and is crossed.
	lines = strings.Split("// documents the code below\n\ncode();\n", "\n")
	if got, ok := searchDocReanchorTarget(lines, 1); !ok || got != 3 {
		t.Fatalf("target = %d/%v, want 3/true", got, ok)
	}
}

func TestSearchDocReanchorTargetRefusesAnOverlongBlock(t *testing.T) {
	t.Parallel()
	var builder strings.Builder
	for i := 0; i < searchDocReanchorMaxScan+2; i++ {
		builder.WriteString("// license header line\n")
	}
	builder.WriteString("code();\n")
	if got, ok := searchDocReanchorTarget(strings.Split(builder.String(), "\n"), 1); ok {
		t.Fatalf("scanned past the cap to %d", got)
	}
}

// reanchorTestReader serves one in-memory file to the pass under test.
func reanchorTestReader(path, content string) contentReader {
	return func(request string) (string, bool) {
		if request != path {
			return "", false
		}
		return content, true
	}
}

func TestReanchorSearchDocCommentsMovesACommentHitOntoCode(t *testing.T) {
	t.Parallel()
	read := reanchorTestReader("src/diff/children.js", reanchorJSDocFile)
	results := []SearchResult{{
		Rank: 1, FilePath: "src/diff/children.js",
		StartLine: 4, EndLine: 7, FocusLine: 6,
		SnippetStartLine: 4, SnippetEndLine: 7,
		Snippet: strings.Join(strings.Split(reanchorJSDocFile, "\n")[3:7], "\n"),
		Signals: []string{"body"},
	}}
	results, moved := reanchorSearchDocComments(results, nil, read, 6)
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}
	got := results[0]
	if got.FocusLine != 8 {
		t.Fatalf("focus = %d, want 8 (the declaration the comment documents)", got.FocusLine)
	}
	if got.CommentFocusLine != 6 {
		t.Fatalf("comment focus = %d, want 6 - the matched line has to stay visible", got.CommentFocusLine)
	}
	// The region is translated onto the code, keeping the width the ranker allotted, and the
	// invariants SearchResponse.Validate enforces must still hold.
	if got.StartLine != 8 || got.EndLine != 11 {
		t.Fatalf("region = %d-%d, want 8-11", got.StartLine, got.EndLine)
	}
	if got.SnippetStartLine < got.StartLine || got.SnippetEndLine > got.EndLine ||
		got.FocusLine < got.StartLine || got.FocusLine > got.EndLine {
		t.Fatalf("invariant broken: %+v", got)
	}
	if !strings.HasPrefix(got.Snippet, "export function diffChildren(") {
		t.Fatalf("snippet still starts in the comment: %q", got.Snippet)
	}
	if strings.Contains(got.Snippet, "@param") {
		t.Fatalf("snippet still carries the doc block: %q", got.Snippet)
	}
}

func TestReanchorSearchDocCommentsLeavesCodeAndProseAlone(t *testing.T) {
	t.Parallel()
	// A hit already anchored on code is untouched — this is the whole scope of the pass, and the
	// regression it guards is a version that sent every code hit to its enclosing declaration.
	read := reanchorTestReader("src/diff/children.js", reanchorJSDocFile)
	code := []SearchResult{{
		Rank: 1, FilePath: "src/diff/children.js",
		StartLine: 8, EndLine: 14, FocusLine: 8,
		SnippetStartLine: 8, SnippetEndLine: 14, Snippet: "export function diffChildren(parentDom, commitQueue) {",
	}}
	before := code[0]
	if got, moved := reanchorSearchDocComments(code, nil, read, 6); moved != 0 || !sameSearchResultLocation(got[0], before) {
		t.Fatalf("a code hit was moved: %d, %+v", moved, got[0])
	}
	// A markdown hit is untouched even though `#` heads its lines: in prose the comment prefixes
	// ARE the content.
	prose := []SearchResult{{
		Rank: 1, FilePath: "docs/guide.md",
		StartLine: 1, EndLine: 2, FocusLine: 1,
		SnippetStartLine: 1, SnippetEndLine: 2, Snippet: "# Heading\ntext",
	}}
	proseBefore := prose[0]
	proseRead := reanchorTestReader("docs/guide.md", "# Heading\ntext\n")
	if got, moved := reanchorSearchDocComments(prose, nil, proseRead, 6); moved != 0 || !sameSearchResultLocation(got[0], proseBefore) {
		t.Fatalf("a prose hit was moved: %d, %+v", moved, got[0])
	}
}

// sameSearchResultLocation compares only the fields the re-anchor pass is allowed to touch.
func sameSearchResultLocation(left, right SearchResult) bool {
	return left.StartLine == right.StartLine && left.EndLine == right.EndLine &&
		left.FocusLine == right.FocusLine && left.CommentFocusLine == right.CommentFocusLine &&
		left.SnippetStartLine == right.SnippetStartLine && left.SnippetEndLine == right.SnippetEndLine &&
		left.Snippet == right.Snippet
}

func TestReanchorSearchDocCommentsFallsBackToANearbyEnclosingUnit(t *testing.T) {
	t.Parallel()
	// A trailing docstring documents the unit it sits in, and there is no code below it to move
	// to. The unit's own declaration is the last honest anchor.
	content := "def f(x):\n    return x\n    # a trailing note\n"
	read := reanchorTestReader("m.py", content)
	symbols := map[string][]SymbolRecord{"m.py": {{
		ID: "s", FilePath: "m.py", Kind: "function", Name: "f", StartLine: 1, EndLine: 3,
	}}}
	results := []SearchResult{{
		Rank: 1, FilePath: "m.py", StartLine: 3, EndLine: 3, FocusLine: 3,
		SnippetStartLine: 3, SnippetEndLine: 3, Snippet: "    # a trailing note",
	}}
	results, moved := reanchorSearchDocComments(results, symbols, read, 6)
	if moved != 1 || results[0].FocusLine != 1 || results[0].CommentFocusLine != 3 {
		t.Fatalf("fallback = %d, %+v", moved, results[0])
	}
	// A container hundreds of lines above is NOT a location: the fallback is bounded by the same
	// scan distance as the downward walk.
	far := map[string][]SymbolRecord{"m.py": {{
		ID: "s", FilePath: "m.py", Kind: "class", Name: "C",
		StartLine: 1, EndLine: 3 + searchDocReanchorMaxScan,
	}}}
	distant := []SearchResult{{
		Rank: 1, FilePath: "m.py", StartLine: 3, EndLine: 3, FocusLine: 3,
		SnippetStartLine: 3, SnippetEndLine: 3, Snippet: "    # a trailing note",
	}}
	farRead := reanchorTestReader("m.py", strings.Repeat("x\n", 2)+"    # a trailing note\n")
	distant[0].FocusLine = 3
	if _, moved := reanchorSearchDocComments(distant, far, farRead, 6); moved != 1 {
		t.Fatalf("a nearby container should still anchor: moved = %d", moved)
	}
	distant = []SearchResult{{
		Rank: 1, FilePath: "m.py",
		StartLine: searchDocReanchorMaxScan + 5, EndLine: searchDocReanchorMaxScan + 5,
		FocusLine:        searchDocReanchorMaxScan + 5,
		SnippetStartLine: searchDocReanchorMaxScan + 5, SnippetEndLine: searchDocReanchorMaxScan + 5,
		Snippet: "# a trailing note",
	}}
	farRead = reanchorTestReader("m.py", strings.Repeat("x\n", searchDocReanchorMaxScan+4)+"# a trailing note\n")
	far["m.py"][0].EndLine = searchDocReanchorMaxScan + 5
	if _, moved := reanchorSearchDocComments(distant, far, farRead, 6); moved != 0 {
		t.Fatalf("a distant container was accepted as a location: moved = %d", moved)
	}
}

// SAME-CONCEPT LITERAL mines the anchor's own reported region (searchLiteralAnchorLines), so
// re-anchoring before the block is built is what stops the literal being lifted out of `@param`
// prose. This pins that wiring: the same anchor, before and after the pass.
func TestSearchLiteralAnchorLinesFollowTheReanchoredRegion(t *testing.T) {
	t.Parallel()
	fileLines := strings.Split(reanchorJSDocFile, "\n")
	anchor := SearchResult{
		Rank: 1, FilePath: "src/diff/children.js",
		StartLine: 4, EndLine: 7, FocusLine: 6,
		SnippetStartLine: 4, SnippetEndLine: 7,
		Snippet: strings.Join(fileLines[3:7], "\n"),
	}
	before := strings.Join(searchLiteralAnchorLines(fileLines, anchor), "\n")
	if !strings.Contains(before, "@param") {
		t.Fatalf("fixture is wrong: the un-moved anchor should mine the doc block, got %q", before)
	}
	results, moved := reanchorSearchDocComments(
		[]SearchResult{anchor}, nil, reanchorTestReader("src/diff/children.js", reanchorJSDocFile), 6)
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}
	after := strings.Join(searchLiteralAnchorLines(fileLines, results[0]), "\n")
	if strings.Contains(after, "@param") {
		t.Fatalf("literals are still mined from the doc block: %q", after)
	}
	if !strings.Contains(after, "diffChildren") {
		t.Fatalf("literals are not mined from the code the comment documents: %q", after)
	}
}
