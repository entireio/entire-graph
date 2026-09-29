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
//     MergedDeclLines (and, for parser name lines, their region starts in MergedDeclStarts) so a
//     renderer that windows the span can still show them.
//
// The declaration line is the parser's name line (SearchResult.SymbolNameLine) whenever the index
// recorded one inside the symbol's span; the text finder below is the fallback for symbols without.
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

// searchSymbolDeclarationLine is the declaration line of a symbol whose snippet lines start at file
// line first: the parser's name line when it lies in the symbol's span and the snippet (parsed is
// then true, and there is no scan bound: it is where the name IS, however many annotation lines
// precede it), else searchDeclarationLine's text fallback.
func searchSymbolDeclarationLine(lines []string, first, symbolStart, symbolEnd, nameLine int, name string) (line int, ok, parsed bool) {
	if nameLine > 0 && nameLine >= symbolStart && (symbolEnd < symbolStart || nameLine <= symbolEnd) &&
		nameLine >= first && nameLine < first+len(lines) {
		return nameLine, true, true
	}
	line, ok = searchDeclarationLine(lines, first, symbolStart, symbolEnd, name)
	return line, ok, false
}

// searchResultDeclarationLine is searchSymbolDeclarationLine for a result's own snippet and symbol.
func searchResultDeclarationLine(result SearchResult) (int, bool, bool) {
	if result.Snippet == "" || result.SymbolStartLine <= 0 {
		return 0, false, false
	}
	lines := strings.Split(result.Snippet, "\n")
	if len(lines) != result.SnippetEndLine-result.SnippetStartLine+1 {
		return 0, false, false
	}
	return searchSymbolDeclarationLine(lines, result.SnippetStartLine, result.SymbolStartLine, result.SymbolEndLine,
		result.SymbolNameLine, result.SymbolName)
}

// searchLineMentionsName reports whether line contains name as a whole identifier anywhere,
// literals, annotations and comments included (Unicode identifier boundaries).
func searchLineMentionsName(line, name string) bool {
	if name == "" {
		return false
	}
	for from := 0; from < len(line); {
		at := strings.Index(line[from:], name)
		if at < 0 {
			return false
		}
		at += from
		if !declarationIdentifierBefore(line, at) && !declarationIdentifierAfter(line, at+len(name)) {
			return true
		}
		from = at + 1
	}
	return false
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
//
// Never less: the replacement must also keep every line of the declaration REGION the focus window
// showed. The region is the symbol's first line down to its declaration line — its annotations,
// attributes and decorators — when the declaration is the parser's name line. When it is the text
// fallback's pick, which can be wrong, the region also takes every line within
// searchDeclarationLineMaxScan of the symbol's start that mentions the name anywhere (the superset
// any finder chooses from), so a wrong pick never trades the right line away. When the maxLines
// cannot hold the declaration and every such line together, the focus window is kept.
func tersifySearchResultKeepingDeclaration(result SearchResult, maxLines int) SearchResult {
	terse := tersifySearchResult(result, maxLines)
	if maxLines <= 0 || terse.SnippetStartLine == result.SnippetStartLine && terse.SnippetEndLine == result.SnippetEndLine {
		return terse
	}
	decl, ok, parsed := searchResultDeclarationLine(result)
	if !ok || decl >= terse.SnippetStartLine && decl <= terse.SnippetEndLine {
		return terse
	}
	lines := strings.Split(result.Snippet, "\n")
	start, last := decl, decl
	for line := maxInt(terse.SnippetStartLine, result.SymbolStartLine); line <= terse.SnippetEndLine; line++ {
		inRegion := line <= decl
		if !parsed && !inRegion && line < result.SymbolStartLine+searchDeclarationLineMaxScan {
			inRegion = searchLineMentionsName(lines[line-result.SnippetStartLine], result.SymbolName)
		}
		if inRegion {
			start, last = minInt(start, line), maxInt(last, line)
		}
	}
	if last-start+1 > maxLines {
		return terse
	}
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
	decls, _ := searchMergedDeclarations(results, run, survivor, lines, start, end)
	return decls
}

// searchMergedDeclarations is searchMergedDeclarationLines together with, for each listed line, the
// first line of that member's declaration region inside the span (its SymbolStartLine, clamped to
// the span) when the line is the parser's name line, or 0 when the text fallback found it. starts is
// nil when no listed line came from the parser.
func searchMergedDeclarations(results []SearchResult, run []int, survivor int, lines []string, start, end int) (decls, starts []int) {
	if start < 1 || end > len(lines) || end < start {
		return nil, nil
	}
	span := lines[start-1 : end]
	lead := results[survivor]
	own, hasOwn, _ := searchSymbolDeclarationLine(span, start, lead.SymbolStartLine, lead.SymbolEndLine, lead.SymbolNameLine, lead.SymbolName)
	type found struct{ decl, start int }
	var list []found
	seen := map[int]bool{}
	anyParsed := false
	for _, index := range run {
		member := results[index] // the survivor's own declaration is excluded by `own` below
		decl, ok, parsed := searchSymbolDeclarationLine(span, start, member.SymbolStartLine, member.SymbolEndLine, member.SymbolNameLine, member.SymbolName)
		if !ok || seen[decl] || hasOwn && decl == own {
			continue
		}
		seen[decl] = true
		regionStart := 0
		if parsed {
			regionStart, anyParsed = maxInt(member.SymbolStartLine, start), true
		}
		list = append(list, found{decl, regionStart})
	}
	if len(list) == 0 {
		return nil, nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].decl < list[j].decl })
	for _, entry := range list {
		decls = append(decls, entry.decl)
		starts = append(starts, entry.start)
	}
	if !anyParsed {
		starts = nil
	}
	return decls, starts
}
