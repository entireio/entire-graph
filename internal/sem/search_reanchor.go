package sem

import "strings"

// Re-anchoring a hit that landed in a doc comment
// ==============================================
//
// A ranked region can match entirely inside prose. The commonest shape is a name mentioned in a
// doc comment — "@param commitQueue List of components which have callbacks to invoke in
// commitRoot" — which is a real, useful signal about WHICH unit is relevant and a useless place
// to point an agent at. Measured on preactjs__preact-3010: the gold file arrived as
// `src/diff/children.js:19`, a bodyless locator sitting in the JSDoc block of `diffChildren`,
// while the three body slots went to `benches/` and `karma.conf.js`. The payload had located the
// right file and then described it by quoting four lines of `@param` text.
//
// The fix is to move the ANCHOR, not the score: a hit whose focus line is a comment is
// re-anchored to the first code line the comment documents (the declaration below it, or the
// enclosing unit when there is nothing below), before body allocation runs. From there it is an
// ordinary code hit — the enclosure planner can find its callable, it competes for a body slot at
// its code location, and the SAME-CONCEPT LITERAL block mines identifiers out of program text
// instead of out of `@param` prose.
//
// Ranking is deliberately untouched. The comment match is why the hit scored what it scored, and
// re-scoring it here would change which files come back rather than how the ones that came back
// are described. The original line is kept in CommentFocusLine and printed as `focus=19->26`, so
// the evidence stays visible and the move is never silent.

const (
	// searchDocReanchorMaxScan bounds how far below the matched comment line the re-anchor will
	// look for the code that comment documents. A doc block that runs longer than this is prose in
	// its own right (a license header, a design note), and the code after it is not what the
	// comment is about.
	searchDocReanchorMaxScan = 60

	// searchDocReanchorMaxBlankRun is how many consecutive blank lines the scan will cross. One
	// blank line inside a comment block is formatting; two mean the comment and the code below it
	// are separate things.
	searchDocReanchorMaxBlankRun = 1
)

// searchCommentLinePrefixes is the comment vocabulary the re-anchor recognises. It is deliberately
// the same list precedingDocumentationStart and searchConfidenceWrapperReason already use, so
// "this line is prose" means one thing across the payload.
var searchCommentLinePrefixes = []string{"//", "/*", "*/", "*", "#", "--", ";;", "%%", "<!--", `"""`, "'''"}

// searchPreprocessorDirectives are the `#`-led tokens that are PROGRAM TEXT, not prose. Without
// them a hit on `#include <stdio.h>` or `#define MAX 8` would be read as a comment and re-anchored
// away from the very line the query matched — a `#` prefix means "comment" in Python, Ruby and
// shell and "directive" in C, C++ and Objective-C, and only the token after it can tell them apart.
var searchPreprocessorDirectives = map[string]bool{
	"include": true, "include_next": true, "define": true, "undef": true, "if": true,
	"ifdef": true, "ifndef": true, "elif": true, "elifdef": true, "elifndef": true,
	"else": true, "endif": true, "pragma": true, "error": true, "warning": true,
	"line": true, "import": true, "embed": true, "region": true, "endregion": true,
}

// searchLineIsComment reports whether a source line is prose rather than program text. The test is
// lexical and language-agnostic: it is applied to a line the ranker already chose, so its job is to
// recognise the obvious cases, not to parse.
func searchLineIsComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") {
		return !searchHashLineIsCode(trimmed)
	}
	for _, prefix := range searchCommentLinePrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// searchHashLineIsCode reports whether a `#`-led line is program text: a Rust or Swift attribute
// (`#[derive(...)]`, `#!/...` shebangs excluded — a shebang is not code an agent edits), or a C
// preprocessor directive.
func searchHashLineIsCode(trimmed string) bool {
	rest := strings.TrimLeft(strings.TrimPrefix(trimmed, "#"), " \t")
	if strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "![") {
		return true
	}
	token := rest
	if cut := strings.IndexAny(token, " \t(<\"'"); cut >= 0 {
		token = token[:cut]
	}
	return searchPreprocessorDirectives[strings.ToLower(token)]
}

// searchDocReanchorTarget resolves the first CODE line at or below `focus` when `focus` itself is a
// comment line. It returns false when the focus line is already code, when the comment block runs
// past searchDocReanchorMaxScan, or when nothing but prose follows it to the end of the file.
//
// Blank lines are crossed (one at a time) because a doc block separated from its declaration by a
// single blank line is still that declaration's doc block; a longer gap is not.
func searchDocReanchorTarget(lines []string, focus int) (int, bool) {
	if focus < 1 || focus > len(lines) {
		return 0, false
	}
	if !searchLineIsComment(lines[focus-1]) {
		return 0, false
	}
	blanks := 0
	for line := focus + 1; line <= len(lines) && line-focus <= searchDocReanchorMaxScan; line++ {
		text := strings.TrimSpace(lines[line-1])
		switch {
		case text == "":
			blanks++
			if blanks > searchDocReanchorMaxBlankRun {
				return 0, false
			}
		case searchLineIsComment(lines[line-1]):
			blanks = 0
		default:
			return line, true
		}
	}
	return 0, false
}

// reanchorSearchDocComments rewrites every ranked result whose focus line sits in a comment so the
// focus, the reported region and the snippet all describe the code that comment documents.
//
// It runs on the SEATED ranking, before enclosure planning, and it never reorders or drops
// anything: the only fields it touches are the ones that say WHERE to look. Files are read through
// the shared content cache, so a result whose file was already hydrated for ranking costs no
// extra IO.
func reanchorSearchDocComments(
	results []SearchResult,
	symbolsByFile map[string][]SymbolRecord,
	read contentReader,
	maxSnippetLines int,
) ([]SearchResult, int) {
	if len(results) == 0 || read == nil {
		return results, 0
	}
	fileLines := map[string][]string{}
	unreadable := map[string]bool{}
	moved := 0
	for index := range results {
		result := results[index]
		if result.FocusLine < 1 || result.CommentFocusLine > 0 || result.FilePath == "" {
			continue
		}
		// Prose files are exempt. In a markdown or reStructuredText hit the "comment" prefixes are
		// the CONTENT — `#` is a heading, not a discarded line — so there is no code below to move
		// to and the move would only mis-describe the hit.
		if NonProgramTextPath(result.FilePath) {
			continue
		}
		lines, cached := fileLines[result.FilePath]
		if !cached {
			if unreadable[result.FilePath] {
				continue
			}
			content, readable := read(result.FilePath)
			if !readable || strings.IndexByte(content, 0) >= 0 {
				unreadable[result.FilePath] = true
				continue
			}
			lines = strings.Split(content, "\n")
			fileLines[result.FilePath] = lines
		}
		// THE GATE, and it is the whole scope of this pass: only a hit ANCHORED IN PROSE is moved.
		// Everything below runs on the assumption that FocusLine is a comment line, so testing it
		// here rather than inside the scan is what keeps a hit on ordinary code untouched — an
		// earlier revision folded this test into searchDocReanchorTarget's "no code found" return and
		// could not tell the two apart, which sent every code hit in the payload to its enclosing
		// declaration (`focus=140→110` on a Go table-driven test, `focus=2787→24` onto a 2,700-line
		// PHP class header).
		if result.FocusLine > len(lines) || !searchLineIsComment(lines[result.FocusLine-1]) {
			continue
		}
		target, ok := searchDocReanchorTarget(lines, result.FocusLine)
		if !ok {
			// Nothing but prose below the comment. The enclosing unit is the last honest anchor:
			// a docstring at the END of a body still documents that body, and its declaration is
			// code the reader can act on. It is accepted only while it stays NEAR the comment, by
			// the same bound as the downward scan: a declaration hundreds of lines above is the
			// file's outer container, and "the class this prose lives somewhere inside" is not a
			// location.
			enclosing, found := smallestSearchSymbolContainingLineWhere(symbolsByFile[result.FilePath], result.FocusLine, nil)
			if !found || enclosing.StartLine < 1 || enclosing.StartLine == result.FocusLine ||
				result.FocusLine-enclosing.StartLine > searchDocReanchorMaxScan ||
				searchLineIsComment(lines[minInt(enclosing.StartLine, len(lines))-1]) {
				continue
			}
			target = enclosing.StartLine
		}
		if target == result.FocusLine {
			continue
		}
		results[index] = reanchorSearchResultTo(result, lines, target, maxSnippetLines)
		moved++
	}
	return results, moved
}

// reanchorSearchResultTo moves one result's anchor to `target` and re-cuts what it reports around
// it.
//
// The region is TRANSLATED rather than widened when the target sits outside it: a hit whose whole
// region is comment text has nothing in that region worth keeping, and widening would spend bytes
// re-printing the prose the move exists to get away from. When the target already falls inside the
// region — a comment inside a body the payload is showing — the region stands and only the focus
// and the snippet window move, so a body already paid for is never given up.
func reanchorSearchResultTo(result SearchResult, lines []string, target, maxSnippetLines int) SearchResult {
	origin := result.FocusLine
	start, end := result.StartLine, result.EndLine
	if target < start || target > end {
		width := end - start
		start, end = clampRegion(target, target+width, len(lines))
		if start == 0 {
			return result
		}
	}
	snippetStart, snippetEnd := focusedSnippetRegion(start, end, target, maxSnippetLines)
	if snippetStart < 1 || snippetEnd > len(lines) || snippetEnd < snippetStart {
		return result
	}
	result.StartLine, result.EndLine = start, end
	result.FocusLine = target
	result.CommentFocusLine = origin
	result.SnippetStartLine, result.SnippetEndLine = snippetStart, snippetEnd
	result.Snippet = strings.Join(lines[snippetStart-1:snippetEnd], "\n")
	return result
}
