package sem

import (
	"sort"
	"strings"
)

// Declaration lines in bounded snippets
// =====================================
//
// A ranked hit's snippet is often a window of a longer callable, centred on the line the query
// matched most densely (searchFocusLine). That line is evidence for WHY the hit is here, but the
// line an agent navigates by is the one that NAMES the callable — the line `git grep -p` prints as
// the enclosing definition. When the window is cut below the declaration, the reader holds a named
// header and twenty lines of body with no signature, and opens the file to find it.
//
// Two engine-side places decide which lines a bounded hit keeps and can lose the declaration:
//
//   - the tail tersify (planWithDemotionFrom): a demoted row keeps searchEnclosureTailSnippetLines
//     lines around its focus. It now keeps them starting at the declaration instead, when the
//     declaration was in the row's snippet and the focus window would have dropped it.
//   - the same-file span merge (search_span_merge.go): a merged survivor keeps its own symbol
//     identity, so the members it absorbed lose theirs. Their declaration lines are recorded in
//     MergedDeclLines so a renderer that windows the span can still show them.
//
// searchDeclarationLineMaxScan bounds how far below the symbol's first line the naming line may
// sit: annotations, attributes and decorators are a few lines; a name found deeper is a use (a
// recursive call), not the declaration. It matches the CLI's exact-name anchor bound.
const searchDeclarationLineMaxScan = 16

// searchDeclarationLine returns the file line that declares the symbol a result belongs to: the
// line DeclarationLineIndex picks at or after symbolStart, within the symbol's span, at most
// searchDeclarationLineMaxScan lines down, and inside the snippet [first, first+len(lines)-1] (the
// name outside literals and leading annotations, definition-shaped lines first). ok is false when the symbol does not start inside the snippet or no such line exists.
func searchDeclarationLine(lines []string, first, symbolStart, symbolEnd int, name string) (int, bool) {
	if name == "" || first <= 0 || symbolStart < first || symbolStart >= first+len(lines) {
		return 0, false
	}
	last := first + len(lines) - 1
	if symbolEnd >= symbolStart && symbolEnd < last {
		last = symbolEnd
	}
	if limit := symbolStart + searchDeclarationLineMaxScan - 1; limit < last {
		last = limit
	}
	index, ok := DeclarationLineIndex(lines, symbolStart-first, last-first, name)
	if !ok {
		return 0, false
	}
	return first + index, true
}

// searchResultDeclarationLine is searchDeclarationLine for a result's own snippet and symbol.
func searchResultDeclarationLine(result SearchResult) (int, bool) {
	if result.Snippet == "" || result.SymbolStartLine <= 0 {
		return 0, false
	}
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) != result.SnippetEndLine-result.SnippetStartLine+1 {
		return 0, false
	}
	return searchDeclarationLine(lines, result.SnippetStartLine, result.SymbolStartLine, result.SymbolEndLine, result.SymbolName)
}

// tersifySearchResultKeepingDeclaration is tersifySearchResult for a demoted tail row, except that
// when the focus window would drop the line that declares the row's symbol and that line was in the
// row's snippet, the maxLines kept start AT the declaration instead.
//
// The row stays a verbatim, contiguous slice of maxLines (or fewer, at the snippet's end) lines, so
// it costs what the focus window cost to within the lengths of the lines swapped, and every
// consumer that maps snippet line i to SnippetStartLine+i stays correct. FocusLine is left where the
// query matched: it may now lie outside the snippet, which the response contract allows (it bounds
// FocusLine by StartLine/EndLine, not by the snippet), and it is what the header's [focus:N] reports.
//
// Why the declaration rather than the matched line: a demoted row is a locator — "is this worth
// opening" — and the line an agent opens a callable by is the one that names it. Measured before
// this existed, a 2-line tail row printed `7. indexer.go:197-198 Indexer.resolveNamespace [focus:198]`
// over two lines of its body while its signature sat four lines up, outside the window.
//
// A row whose focus window already holds the declaration is returned exactly as tersifySearchResult
// returns it, so no row that showed its declaration before shows anything else now.
func tersifySearchResultKeepingDeclaration(result SearchResult, maxLines int) SearchResult {
	terse := tersifySearchResult(result, maxLines)
	if maxLines <= 0 || terse.SnippetStartLine == result.SnippetStartLine && terse.SnippetEndLine == result.SnippetEndLine {
		return terse
	}
	decl, ok := searchResultDeclarationLine(result)
	if !ok || decl >= terse.SnippetStartLine && decl <= terse.SnippetEndLine {
		return terse
	}
	lines := strings.Split(result.Snippet, "\n")
	start := decl
	end := minInt(result.SnippetEndLine, start+maxLines-1)
	offset := start - result.SnippetStartLine
	result.Snippet = strings.Join(lines[offset:offset+(end-start+1)], "\n")
	result.SnippetStartLine, result.SnippetEndLine = start, end
	return result
}

// searchMergedDeclarationLines returns, in ascending order, the declaration line of every run
// member other than the survivor whose declaration lies inside the merged span [start, end] of
// lines (the whole file, 1-based). The survivor's own declaration is not listed: its symbol fields
// already carry it.
func searchMergedDeclarationLines(results []SearchResult, run []int, survivor int, lines []string, start, end int) []int {
	if start < 1 || end > len(lines) || end < start {
		return nil
	}
	span := lines[start-1 : end]
	own, hasOwn := searchDeclarationLine(span, start, results[survivor].SymbolStartLine, results[survivor].SymbolEndLine, results[survivor].SymbolName)
	var decls []int
	seen := map[int]bool{}
	for _, index := range run {
		member := results[index] // the survivor's own declaration is excluded by `own` below
		decl, ok := searchDeclarationLine(span, start, member.SymbolStartLine, member.SymbolEndLine, member.SymbolName)
		if !ok || seen[decl] || hasOwn && decl == own {
			continue
		}
		seen[decl] = true
		decls = append(decls, decl)
	}
	if len(decls) == 0 {
		return nil
	}
	sort.Ints(decls)
	return decls
}
