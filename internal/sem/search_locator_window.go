package sem

import "strings"

// THE NAMELESS LOCATOR.
//
// A ranked hit demoted to a locator keeps three things by design — path, line, and symbol name —
// and the name is what makes the line actionable: it is what the `[body: def NAME]` follow-up
// hands to `def`, and it is what tells a reader whether the file is worth opening at all.
//
// A hit whose focus line has no enclosing INDEXED symbol has no name to keep. Import blocks, C/C++
// header prototype lists, CSS rules, `export { ... }` lists, `.d.ts` declarations, doc-comment
// examples and snapshot fixtures all land here. Demoted, such a hit renders as `N. path:line` and
// nothing else: no source, no symbol, and no follow-up verb, because there is no name to give one.
// It is the only line shape in the payload that carries no information beyond coordinates and
// suggests no next action, so the only way to act on it is the file read the payload exists to
// remove.
//
// MEASURED, 30 SWE-bench Multilingual instances at the shipped defaults (--top-k 10,
// --max-snippet-lines 6, --max-context-bytes 16384): 21 of 238 primary ranked lines (8.8%) render
// this way, across 12 of the 30 instances, at ranks 3 through 10. Every one of the 21 ALREADY
// carried source in the ranked result — mean 3.2 lines, 2,245 bytes in total across all 30 payloads
// — which the JSON emits and the text renderer discarded. On scikit-learn-14629 the discarded window
// was `sklearn/multioutput.py:22-26`, whose focus line is `from .model_selection import
// cross_val_predict`: the exact import coupling the issue is about, in the file the fix lands in,
// thrown away to save 273 bytes.
//
// So the fix is not to fetch anything. It is to stop discarding what the ranker already paid for
// when the alternative is a line that says nothing.
//
// searchLocatorWindowLines bounds what such a hit keeps. Six lines is the same ceiling as the
// default --max-snippet-lines, so an ordinary ranked window passes through whole and the cap only
// ever bites on a window some other lever widened (a head window is 60 lines and can reach this
// path through the render diet's body cap). It is a CEILING, never a floor: this function never
// grows a window, so the payload can only ever gain the bytes the ranker had already allocated.
const searchLocatorWindowLines = 6

// SearchLocatorWindow returns the source a demoted, nameless hit should keep and the line range that
// source actually spans, clipped to searchLocatorWindowLines around the hit's focus line.
//
// The returned range describes exactly the lines returned with it — never the ranked region — for
// the same reason searchResultPrintedRange exists in the renderer: a header naming a span the source
// below it does not contain sends the reader to the wrong lines, which is worse than sending them
// nowhere.
//
// It returns an empty string when the result carries no source, which is the caller's signal to fall
// back to the bare locator. Deciding whether a hit is nameless is deliberately NOT done here: the
// display-name rule (qualified name first, then symbol name) belongs to the renderer that owns the
// locator shape.
func SearchLocatorWindow(result SearchResult) (int, int, string) {
	if result.Snippet == "" {
		return 0, 0, ""
	}
	start := result.SnippetStartLine
	if start <= 0 {
		start = result.StartLine
	}
	if start <= 0 {
		start = 1
	}
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) <= searchLocatorWindowLines {
		return start, start + len(lines) - 1, result.Snippet
	}
	// Centre the clip on the matched line when the snippet contains it, so the one line the query
	// actually hit can never be the line the cap removes. Otherwise keep the head of the window,
	// which is where a snippet without a usable focus reads from.
	first := 0
	if last := start + len(lines) - 1; result.FocusLine >= start && result.FocusLine <= last {
		first = result.FocusLine - start - (searchLocatorWindowLines-1)/2
	}
	if limit := len(lines) - searchLocatorWindowLines; first > limit {
		first = limit
	}
	if first < 0 {
		first = 0
	}
	clipped := lines[first : first+searchLocatorWindowLines]
	return start + first, start + first + searchLocatorWindowLines - 1, strings.Join(clipped, "\n")
}
