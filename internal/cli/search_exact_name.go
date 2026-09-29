package cli

import (
	"bytes"
	"fmt"
	"path"
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
// ranking is replaced by those rows alone, each anchored on the line that NAMES the symbol with a
// bounded body, followed by a line saying what was left out. Policy:
//
//   - Gate: the trimmed query is a single ASCII identifier ([A-Za-z_][A-Za-z0-9_]*) and at least one
//     result has SymbolName == query, byte for byte. A partial name or a case mismatch is not an
//     exact lookup and takes the ordinary path unchanged; so does every multi-word query.
//   - Anchor: the line the index says a symbol starts on is not always the line that names it. For
//     Java and C# it is the first annotation (`@Override`, `[Obsolete]`), so an answer anchored there
//     printed an annotation and lost the signature the ordinary answer had shown. A row is anchored
//     on the line sem.DeclarationLineIndex picks between the symbol's start and at most
//     exactNameAnchorLines down, within its span: the name as a whole identifier OUTSIDE literals and
//     leading annotations (an annotation argument `@Named("fooBar")` or a decorator
//     `@app.route("/login")` names the symbol but does not declare it), definition-shaped lines
//     first. A row with no such line disqualifies the whole mode (see Ambiguity).
//   - Definitions the index did not surface: a row for ANOTHER symbol whose own span defines the name
//     in that file's own language (`name := func…` in Go, `const name =` in TS, a nested `def name`
//     in Python) is a definition too, and the ordinary answer showed it. It is kept as an exact row,
//     anchored on that line. Another language's keyword (`class Animal {` inside a Go test's string
//     literal) is text, not a definition.
//   - Ambiguity: EVERY exact row is shown, in ranking order, never a subset. First each gets its
//     named line plus a bounded body from a rank-weighted share; if that cannot hold every row, each
//     gets its named line only; if even that does not fit, the ordinary path runs. There is no rung
//     that shows some exact matches and hides others.
//   - Growth order inside a row: the named line (always), then the annotations between the symbol's
//     start and it, then the first lines of a multi-line signature, then the leading doc comment
//     (whole or not at all), then the rest of the bounded body. A doc comment or an annotation is
//     never bought at the cost of the signature.
//   - Never locate less (for exact rows): the mode is taken only when every exact row's named line is
//     shown. Otherwise the ordinary path runs with the same prefix, so the answer is byte-identical to
//     what it was before this mode existed.
const (
	// exactNameBodyLines bounds the body shown under a declaration: enough for a signature, its
	// guard clauses and the first statements, which is what a name lookup is for. The header's
	// [ends:N] tells the reader where the rest is.
	exactNameBodyLines = 20
	// exactNameRowBytes bounds one row regardless of how roomy the cap is. Without it a 24 KiB
	// default cap would print a 400-line function whole, which is a file read, not a lookup.
	exactNameRowBytes = 1024
	// exactNameAnchorLines bounds the search for the named line below the symbol's first line: a
	// stack of annotations or decorators is a few lines; a name found deeper is a recursive call.
	exactNameAnchorLines = 16
	// exactNameSignatureLines is how many lines under the named line are shown before the doc
	// comment competes for bytes: the rest of a multi-line signature.
	exactNameSignatureLines = 3
	// exactNameDocLines bounds the leading doc comment. A longer one is left out whole rather than
	// cut to its last lines, which would drop its summary sentence and keep its fine print.
	exactNameDocLines = 16
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

// agentExactNameDeclLine is the line the index says the symbol starts on.
func agentExactNameDeclLine(result sem.SearchResult) int {
	if result.SymbolStartLine > 0 {
		return result.SymbolStartLine
	}
	return result.StartLine
}

func exactNameIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// exactNameDefinitionKeywords are, per file extension, the keywords that introduce a definition of
// the identifier after them. They are per language because a keyword of one language inside another
// language's file is almost always text: `class Animal {` in a Go test's string literal is fixture
// source, not a definition of Animal, and treating it as one printed test bodies as "definitions".
// A file whose language is not listed gets none.
var exactNameDefinitionKeywords = map[string][]string{
	".go":    {"func", "var", "const", "type"},
	".py":    {"def", "class"},
	".ts":    {"function", "class", "const", "let", "var", "interface", "type", "enum"},
	".tsx":   {"function", "class", "const", "let", "var", "interface", "type", "enum"},
	".js":    {"function", "class", "const", "let", "var"},
	".jsx":   {"function", "class", "const", "let", "var"},
	".mjs":   {"function", "class", "const", "let", "var"},
	".java":  {"class", "interface", "enum", "record"},
	".cs":    {"class", "interface", "enum", "record", "struct"},
	".rs":    {"fn", "struct", "enum", "trait", "type", "mod", "const", "static", "let"},
	".kt":    {"fun", "class", "interface", "object", "val", "var"},
	".rb":    {"def", "class", "module"},
	".swift": {"func", "class", "struct", "enum", "protocol", "let", "var"},
	".scala": {"def", "class", "object", "trait", "val", "var"},
}

// exactNameLineDefines reports whether line, in a file with extension ext, DEFINES name — `name :=`
// in Go, or one of the language's definition keywords right before it (`const name =`, `def name(`,
// `func (r *T) name(`) — as opposed to using it. It is deliberately narrow: an assignment
// `name = …` is a use as often as a definition.
func exactNameLineDefines(line, name, ext string) bool {
	keywords := exactNameDefinitionKeywords[ext]
	if len(keywords) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(line)
	for _, comment := range []string{"//", "/*", "*", "--", "# "} {
		if strings.HasPrefix(trimmed, comment) {
			return false
		}
	}
	for _, at := range sem.DeclarationNameOccurrences(line, name) {
		if ext == ".go" && strings.HasPrefix(strings.TrimLeft(line[at+len(name):], " \t"), ":=") {
			return true
		}
		before := strings.TrimRight(line[:at], " \t")
		if len(before) == len(line[:at]) {
			continue // the keyword must be separated from the name
		}
		if ext == ".go" && strings.HasSuffix(before, ")") && strings.HasPrefix(strings.TrimSpace(before), "func") {
			return true // a Go method: func (r *T) name
		}
		word := len(before)
		for word > 0 && exactNameIdentByte(before[word-1]) {
			word--
		}
		for _, keyword := range keywords {
			if before[word:] == keyword {
				return true
			}
		}
	}
	return false
}

// exactNameLeadLine reports whether a line directly above a declaration belongs to it: a comment,
// an annotation, a decorator or an attribute.
func exactNameLeadLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, lead := range []string{"//", "/*", "*", "#", "@", "[", "--", "'''", `"""`} {
		if strings.HasPrefix(trimmed, lead) {
			return true
		}
	}
	return false
}

// exactNameAnchor is one exact row, re-anchored on the line that names it, with the order in which
// its block grows.
type exactNameAnchor struct {
	raw    sem.SearchResult // as ranked, for callers that compare with the response
	result sem.SearchResult // one-lined and quarantined, for rendering
	lines  []string
	first  int // line number of lines[0]
	named  int // index of the line that names (or defines) the symbol
	// steps[k] is the [top, bottom] line index range shown at growth step k; steps[0] is the named
	// line alone. Every step contains every earlier one, so more bytes never show fewer lines.
	steps [][2]int
}

// agentExactNameAnchor re-anchors result on the line that names `name`. ok is false when the
// snippet does not hold such a line within the bounded search.
func agentExactNameAnchor(result sem.SearchResult, name string) (exactNameAnchor, bool) {
	shown := searchResultOnOneLine(result)
	lines := strings.Split(shown.Snippet, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := shown.SnippetStartLine
	if first <= 0 {
		first = shown.StartLine
	}
	decl := agentExactNameDeclLine(shown)
	start := decl - first
	if first <= 0 || decl <= 0 || start < 0 || start >= len(lines) {
		return exactNameAnchor{}, false
	}
	end := len(lines) - 1
	if shown.SymbolEndLine >= decl {
		end = min(end, shown.SymbolEndLine-first)
	}
	own := result.SymbolName == name
	named, spanTop := -1, start
	if own {
		// The shared finder: an annotation, decorator or literal that merely mentions the name
		// (`@Named("fooBar")`, `@app.route("/login")`) is not the line that declares it.
		if index, ok := sem.DeclarationLineIndex(lines, start, min(end, start+exactNameAnchorLines-1), name); ok {
			named = index
		}
	} else {
		for i := start; i <= end; i++ {
			if exactNameLineDefines(lines[i], name, strings.ToLower(path.Ext(result.FilePath))) {
				named, spanTop = i, i // the lines above it are its enclosing symbol's, not its own
				break
			}
		}
	}
	if named < 0 {
		return exactNameAnchor{}, false
	}
	top, bottom := named, named
	steps := [][2]int{{top, bottom}}
	grow := func() { steps = append(steps, [2]int{top, bottom}) }
	// Annotations, attributes and decorators the index counts as part of the symbol.
	for top > spanTop {
		top--
		grow()
	}
	bodyEnd := min(end, named+exactNameBodyLines)
	for bottom < min(bodyEnd, named+exactNameSignatureLines) {
		bottom++
		grow()
	}
	// The leading doc comment (and any decorators above the symbol's first line), whole or not at all.
	if own {
		docTop := top
		for docTop > 0 && strings.TrimSpace(lines[docTop-1]) != "" && exactNameLeadLine(lines[docTop-1]) {
			docTop--
		}
		if docTop < top && top-docTop <= exactNameDocLines {
			top = docTop
			grow()
		}
	}
	for bottom < bodyEnd {
		bottom++
		grow()
	}
	return exactNameAnchor{raw: result, result: shown, lines: lines, first: first, named: named, steps: steps}, true
}

// agentExactNameAnchors returns the exact rows in ranking order, anchored, and how many other
// results the exact answer leaves out. It returns nil when the mode does not apply: the query is
// not an identifier, no symbol is named exactly query, or some symbol that is cannot be anchored on
// a line naming it, which the ordinary path is left to render as it always has.
func agentExactNameAnchors(results []sem.SearchResult, query string) ([]exactNameAnchor, int) {
	name, ok := agentExactNameQuery(query)
	if !ok {
		return nil, 0
	}
	var anchors []exactNameAnchor
	named := false
	for _, result := range results {
		anchor, ok := agentExactNameAnchor(result, name)
		if result.SymbolName == name {
			if !ok {
				return nil, 0
			}
			named = true
		} else if !ok {
			continue
		}
		anchors = append(anchors, anchor)
	}
	if !named {
		return nil, 0
	}
	return anchors, len(results) - len(anchors)
}

// agentExactNameRows is agentExactNameAnchors reporting the ranked results themselves.
func agentExactNameRows(results []sem.SearchResult, query string) ([]sem.SearchResult, int) {
	anchors, omitted := agentExactNameAnchors(results, query)
	if anchors == nil {
		return nil, 0
	}
	rows := make([]sem.SearchResult, len(anchors))
	for i, anchor := range anchors {
		rows[i] = anchor.raw
	}
	return rows, omitted
}

// agentExactNameOmittedLine is the one line that says the ranking was cut on purpose, or that it
// may have been cut short of more exact definitions. It must not read as a locator
// (agentSearchLineIsLocator).
//
//   - omitted > 0: the other rows are counted. Outside a search session it names the way back to
//     them. Under a session cap (EG_SEARCH_SESSION) it names nothing: the cap exists to stop a
//     task fanning out into more searches, and a later search may replay an earlier answer rather
//     than run, so an invitation to search again is not one this payload can honour.
//   - omitted == 0 and the ranking filled --top-k: every returned row is an exact definition, so
//     more may exist past the cut. Said, with the knob, except under a session cap.
//   - otherwise nothing was left out and the line is absent.
func agentExactNameOmittedLine(omitted, shown, topK int, capped bool) []byte {
	switch {
	case omitted > 0 && capped:
		return []byte(fmt.Sprintf("exact name: %d other result%s omitted\n", omitted, pluralSuffix(omitted)))
	case omitted > 0:
		return []byte(fmt.Sprintf("exact name: %d other result%s omitted; search a phrase to see them\n",
			omitted, pluralSuffix(omitted)))
	case topK > 0 && shown >= topK && capped:
		return []byte(fmt.Sprintf("exact name: showing %d exact definition%s from the top %d; more may exist\n", shown, pluralSuffix(shown), topK))
	case topK > 0 && shown >= topK:
		return []byte(fmt.Sprintf("exact name: showing %d exact definition%s from the top %d; more may exist; raise --top-k\n", shown, pluralSuffix(shown), topK))
	}
	return nil
}

// fitAgentExactNameResults renders every exact row with its named line shown, in at most budget
// bytes after escaping, or returns nil when it cannot (the caller then takes the ordinary path).
//
// Bytes are allocated in two phases so that no row can be starved by another's share. First every
// row is costed at its named line alone under the fullest header rung all of them can afford
// together; only if that fits does anything grow, and the bytes left over are then split by rank
// (the same weights as rankedAgentSearchBudgets) and capped per row at exactNameRowBytes. A fixed
// rank-weighted split, tried first, starved the last of three matches of the ~160 bytes its header
// and declaration need while the first held a kilobyte, and handed the whole answer back to the
// ordinary path.
func fitAgentExactNameResults(anchors []exactNameAnchor, note []byte, budget int) []byte {
	if len(anchors) == 0 {
		return nil
	}
	separators := len(anchors) - 1
	available := budget - separators - len(note)
	if available <= 0 {
		return nil
	}
	for floor := 0; floor < 3; floor++ {
		costs := make([]int, len(anchors))
		total := 0
		for index, anchor := range anchors {
			costs[index] = len(agentExactNameBlock(anchor, floor, 0))
			total += costs[index]
		}
		if total > available {
			continue
		}
		extra := available - total
		weightTotal := len(anchors) * (len(anchors) + 1) / 2
		var output bytes.Buffer
		for index, anchor := range anchors {
			share := costs[index] + extra*(len(anchors)-index)/weightTotal
			if limit := max(costs[index], exactNameRowBytes); share > limit {
				share = limit
			}
			if index > 0 {
				output.WriteByte('\n')
			}
			output.Write(agentExactNameBlock(anchor, floor, share))
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
// compact, 2 bare locator) at the furthest growth step that fits in budget. budget 0 means the named
// line alone, which is how a row is costed. It returns nil rather than a header without the named
// line under it: a bare header is the failure this mode removes.
//
// The header rung is fixed BEFORE the body is sized, unlike the ordinary block, which prefers more
// lines under a bare `path:line *` locator. For a name lookup the rank, name and [ends:N] are worth
// more than one more body line, so every row keeps the richest rung the whole answer can afford.
func agentExactNameBlock(anchor exactNameAnchor, floor, budget int) []byte {
	row := anchor.result
	tag := agentSearchSectionTag(row)
	named := anchor.first + anchor.named
	if row.SymbolEndLine > named {
		// Where the declaration ends, so the reader can fetch the rest of a bounded body in one read.
		ends := fmt.Sprintf("[ends:%d]", row.SymbolEndLine)
		if tag == "" {
			tag = ends
		} else {
			tag += " " + ends
		}
	}
	name, scored := searchResultDisplayName(row), agentSearchScoreTag(row)
	render := func(step int) []byte {
		top, bottom := anchor.steps[step][0], anchor.steps[step][1]
		header := agentSearchLocationHeaders(row.Rank, row.FilePath, anchor.first+top, anchor.first+bottom, named, name, tag, scored)[floor]
		return termsafe.Bytes([]byte(header + strings.Join(anchor.lines[top:bottom+1], "\n") + "\n"))
	}
	if budget <= 0 {
		return render(0)
	}
	// The furthest step that fits, not the first that does not: a line added above can shorten the
	// header's range, so cost is not strictly monotone in the step, and this keeps the chosen step
	// monotone in budget.
	var best []byte
	for step := range anchor.steps {
		if block := render(step); len(block) <= budget {
			best = block
		}
	}
	return best
}
