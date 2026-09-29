package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// EXACT-NAME ANSWERS (agent format only).
//
// A query that is one identifier token spelled exactly like a symbol name is a declaration lookup:
// the caller already knows the name and wants the definition. The ordinary fitter answers it the
// way it answers a paraphrase — it spreads the cap over all ranked rows — and two things go wrong,
// both measured (study 3, fx-git-sync, 40 bare-name queries):
//
//   - bytes: every arm located 40/40 at 4 KiB, but the graph returned a median ~3,300 B against a
//     strong grep's 666 B. The declaration was already on screen; the extra bytes were rows 2..10.
//   - locations: at 2 KiB the graph MISSED 5/40, and in every miss the exact row was present and
//     named but printed as a bare header (`1. path:1516 translator.resolveMessageRef s=104.5
//     [focus:1516]`, no body) because the rank-weighted split handed it ~170 bytes while nine other
//     rows took the rest. The declaration line fell outside the row's share, not outside the cap.
//
// So when the query is an exact identifier and the ranking holds symbols with that exact name, the
// ranking is replaced by those rows alone, each anchored on its declaration line with a bounded body,
// followed by one line counting what was left out. Policy:
//
//   - Gate: the trimmed query is a single ASCII identifier ([A-Za-z_][A-Za-z0-9_]*) and at least one
//     result has SymbolName == query, byte for byte. A partial name or a case mismatch is not an
//     exact lookup and takes the ordinary path unchanged; so does every multi-word query.
//   - Ambiguity: EVERY exact-name result is shown, in ranking order, never a subset. First each gets
//     its declaration plus a bounded body from a rank-weighted share; if that cannot hold every
//     declaration, each gets its declaration line only; if even that does not fit, the ordinary
//     path runs. There is no rung that shows some exact matches and hides others.
//   - Never locate less: the mode is taken only when every exact row's snippet contains its own
//     declaration line and the rendered block shows it. Otherwise the ordinary path runs with the
//     same prefix, so the answer is byte-identical to what it was before this mode existed.
const (
	// exactNameBodyLines bounds the body shown under a declaration: enough for a signature, its
	// guard clauses and the first statements, which is what a name lookup is for. The header's
	// [ends:N] tells the reader where the rest is.
	exactNameBodyLines = 20
	// exactNameRowBytes bounds one row regardless of how roomy the cap is. Without it a 24 KiB
	// default cap would print a 400-line function whole, which is a file read, not a lookup.
	exactNameRowBytes = 1024
)

// agentExactNameQuery reports whether query is one identifier token, returning it trimmed.
func agentExactNameQuery(query string) (string, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", false
	}
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return "", false
		}
	}
	return query, true
}

// agentExactNameDeclLine is the line the declaration starts on.
func agentExactNameDeclLine(result sem.SearchResult) int {
	if result.SymbolStartLine > 0 {
		return result.SymbolStartLine
	}
	return result.StartLine
}

// agentExactNameRows returns the results whose symbol is named exactly query, in ranking order,
// and how many other results the exact answer leaves out. It returns nil when the mode does not
// apply: the query is not an identifier, nothing matches exactly, or some exact row's snippet does
// not hold its own declaration line (a call site that happens to sit in a same-named function, for
// instance), which the ordinary path is left to render as it always has.
func agentExactNameRows(results []sem.SearchResult, query string) ([]sem.SearchResult, int) {
	name, ok := agentExactNameQuery(query)
	if !ok {
		return nil, 0
	}
	var rows []sem.SearchResult
	for _, result := range results {
		if result.SymbolName != name {
			continue
		}
		if _, ok := agentExactNameDeclSnippet(result, 1); !ok {
			return nil, 0
		}
		rows = append(rows, result)
	}
	if len(rows) == 0 {
		return nil, 0
	}
	return rows, len(results) - len(rows)
}

// agentExactNameDeclSnippet re-anchors result on its declaration line and keeps at most `lines`
// lines from there down. ok is false when the snippet does not contain the declaration.
func agentExactNameDeclSnippet(result sem.SearchResult, lines int) (sem.SearchResult, bool) {
	snippet := strings.Split(result.Snippet, "\n")
	if len(snippet) > 1 && snippet[len(snippet)-1] == "" {
		snippet = snippet[:len(snippet)-1]
	}
	start := result.SnippetStartLine
	if start <= 0 {
		start = result.StartLine
	}
	decl := agentExactNameDeclLine(result)
	index := decl - start
	if start <= 0 || decl <= 0 || index < 0 || index >= len(snippet) || strings.TrimSpace(snippet[index]) == "" {
		return result, false
	}
	end := minIntCLI(len(snippet), index+lines)
	result.Snippet = strings.Join(snippet[index:end], "\n")
	result.SnippetStartLine = decl
	result.StartLine = decl
	result.FocusLine = decl
	result.Passages = nil
	return result, true
}

// agentExactNameOmittedLine is the one line that says the ranking was cut on purpose. It must not
// read as a locator (agentSearchLineIsLocator) and it names the way back to the full ranking.
func agentExactNameOmittedLine(omitted int) []byte {
	if omitted <= 0 {
		return nil
	}
	return []byte(fmt.Sprintf("exact name: %d other result%s omitted; search a phrase to see them\n",
		omitted, pluralSuffix(omitted)))
}

// fitAgentExactNameResults renders every exact row with its declaration shown, in at most budget
// bytes after escaping, or returns nil when it cannot (the caller then takes the ordinary path).
//
// Bytes are allocated in two phases so that no row can be starved by another's share. First every
// row is costed at its declaration line alone under the fullest header rung all of them can afford
// together; only if that fits does anything get a body, and the bytes left over are then split by
// rank (the same weights as rankedAgentSearchBudgets) and capped per row at exactNameRowBytes. A
// fixed rank-weighted split, tried first, starved the last of three matches of the ~160 bytes its
// header and declaration need while the first held a kilobyte, and handed the whole answer back to
// the ordinary path.
func fitAgentExactNameResults(rows []sem.SearchResult, omitted, budget int) []byte {
	if len(rows) == 0 {
		return nil
	}
	note := agentExactNameOmittedLine(omitted)
	separators := len(rows) - 1
	available := budget - separators - len(note)
	if available <= 0 {
		return nil
	}
	anchored := make([]sem.SearchResult, len(rows))
	for index, row := range rows {
		row, ok := agentExactNameDeclSnippet(row, 1+exactNameBodyLines)
		if !ok {
			return nil
		}
		anchored[index] = searchResultOnOneLine(row)
	}
	for floor := 0; floor < 3; floor++ {
		costs := make([]int, len(anchored))
		total := 0
		for index, row := range anchored {
			costs[index] = len(agentExactNameBlock(row, floor, 1, 0))
			total += costs[index]
		}
		if total > available {
			continue
		}
		extra := available - total
		weightTotal := len(anchored) * (len(anchored) + 1) / 2
		var output bytes.Buffer
		for index, row := range anchored {
			share := costs[index] + extra*(len(anchored)-index)/weightTotal
			if limit := max(costs[index], exactNameRowBytes); share > limit {
				share = limit
			}
			if index > 0 {
				output.WriteByte('\n')
			}
			output.Write(agentExactNameBlock(row, floor, len(strings.Split(row.Snippet, "\n")), share))
		}
		output.Write(note)
		// Belt and braces: every share is at least the block it was costed at and the shares sum to
		// no more than available, so this holds by construction.
		if output.Len() <= budget {
			return output.Bytes()
		}
	}
	return nil
}

// agentExactNameBlock renders one anchored row, escaped, under header rung `floor` (0 rich, 1
// compact, 2 bare locator) with as many of its first maxLines lines as fit in budget. budget 0
// means the declaration line alone, which is how a row is costed. It returns nil rather than a
// header without the declaration line under it: a bare header is the failure this mode removes.
//
// The header rung is fixed BEFORE the body is sized, unlike the ordinary block, which prefers more
// lines under a bare `path:line *` locator. For a name lookup the rank, name and [ends:N] are worth
// more than one more body line, so every row keeps the richest rung the whole answer can afford.
func agentExactNameBlock(row sem.SearchResult, floor, maxLines, budget int) []byte {
	lines := strings.Split(row.Snippet, "\n")
	maxLines = min(maxLines, len(lines))
	tag := agentSearchSectionTag(row)
	if row.SymbolEndLine > row.SnippetStartLine {
		// Where the declaration ends, so the reader can fetch the rest of a bounded body in one read.
		ends := fmt.Sprintf("[ends:%d]", row.SymbolEndLine)
		if tag == "" {
			tag = ends
		} else {
			tag += " " + ends
		}
	}
	name, scored, decl := searchResultDisplayName(row), agentSearchScoreTag(row), row.SnippetStartLine
	render := func(variant, count int) []byte {
		header := agentSearchLocationHeaders(row.Rank, row.FilePath, decl, decl+count-1, decl, name, tag, scored)[variant]
		return termsafe.Bytes([]byte(header + strings.Join(lines[:count], "\n") + "\n"))
	}
	if budget <= 0 {
		return render(floor, 1)
	}
	var best []byte
	for count := 1; count <= maxLines; count++ {
		block := render(floor, count)
		if len(block) > budget {
			break
		}
		best = block
	}
	return best
}
