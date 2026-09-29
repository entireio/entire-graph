package cli

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// DECLARATION LINES IN AGENT BLOCKS (agent format only).
//
// An agent block prints the widest balanced window of a hit's snippet around its focus line, and
// the focus line is the line the query terms hit most densely — usually deep in a body. When the
// snippet holds the whole callable but the budget holds only part of it, the window is centred
// there and the line that NAMES the callable is cut. The reader then holds `1. f.go:120-141
// Store.flush [focus:131]` over twenty body lines with no signature, and opens the file to find
// it. `git grep -p` prints that line for free, which is most of why grep located what this block
// did not (study-5 forensics, diagnostic only: 40 of 121 misses were a ranked, fully-bodied target
// whose declaration fell outside the printed window).
//
// So when the declaration lies inside the snippet but outside the window the budget allows, the
// block prints it first, then an elision line, then the focus window — all inside the block's own
// byte share, so no other block gives up anything. Policy:
//
//   - The declaration is the parser's name line (sem.SearchResult.SymbolNameLine) whenever the
//     index recorded one inside the symbol's span and the snippet, however far below the symbol's
//     first line it sits. Only without one is it the line the text fallback
//     sem.DeclarationLineIndex picks at or after the symbol's start, within its span and at most
//     exactNameAnchorLines down (the exact-name anchor's rule). A symbol with neither is rendered
//     exactly as before.
//   - A multi-line signature's continuation lines (a named line ending in `(` or `,`) and the
//     declarations of the members a merged span absorbed (sem.SearchResult.MergedDeclLines) are
//     optional. A variant adding k of them is taken only when it prints at most k fewer BODY lines
//     than the variant without them, where a body line is a printed source line outside every
//     declaration group: each added declaration displaces at most one body line. The elision line
//     an added declaration brings with it is not a body line and is not counted either way; its
//     bytes are paid from the window. The survivor's own declaration is mandatory.
//   - Never locate less: a block whose ordinary window already shows every declaration is returned
//     byte for byte. Otherwise every declaration the ordinary window showed is mandatory, AND every
//     line it showed of a declaration REGION (agentSearchPlainHeads). With a parser name line the
//     region is exact and unbounded: every line from the symbol's first line (for an absorbed member,
//     its sem.SearchResult.MergedDeclStarts entry) down to the name line, annotations included.
//     Without one it is every line any text finder could have taken for the declaration: a line
//     within agentSearchHeadLines of the symbol's start naming it anywhere; for an absorbed member,
//     every line within agentSearchHeadLines above its recorded line and the annotation run below
//     it — so a wrong pick never makes the new block drop the right line.
//   - When no window fits beside the mandatory lines, they alone are printed rather than a window
//     without them; when not even that fits, the ordinary block (or bare header) is kept.
//   - Header: the minimal rung names the block's first printed line, not its focus line, so every
//     printed line's number is recoverable (agentSearchRenderDecls).
const (
	// agentSearchSignatureLines bounds the continuation lines of a multi-line signature, the same
	// bound the exact-name answer uses.
	agentSearchSignatureLines = exactNameSignatureLines
	// agentSearchHeadLines is how far below a symbol's first line (or an absorbed member's recorded
	// declaration line) a line the ordinary window printed is carried over when a finder could have
	// taken it for the declaration (agentSearchPlainHeads): the declaration search's own bound.
	agentSearchHeadLines = exactNameAnchorLines
)

// agentSearchBlockView is one block's rendering inputs, computed once by agentSearchPrimaryBlock.
type agentSearchBlockView struct {
	result    sem.SearchResult
	lines     []string
	first     int // file line of lines[0]
	focus     int // index of the focus line in lines
	focusLine int // the focus line the header reports
	name, tag string
	scored    string
}

// agentSearchDecl is one group of lines a block must or may show beside its window: the snippet
// index of its first line and how many lines follow it. A declaration group is the line that names
// a symbol plus its signature continuation lines. A head group (head) is a run of lines the ordinary
// window printed within a symbol's first agentSearchHeadLines lines, carried over so the new block
// never shows less of a declaration than the old one did; its lines count as body, not declaration.
type agentSearchDecl struct {
	index, cont int
	head        bool
	// region is the snippet index where an absorbed member's declaration region starts when
	// hasRegion: its declaration line is the parser's name line, so every line from region to index
	// belongs to it. (The block's own region comes from its SymbolStartLine and SymbolNameLine.)
	region    int
	hasRegion bool
}

// agentSearchDeclIndex returns the snippet index of the line declaring `name` (sem.DeclarationLineIndex:
// outside literals and leading annotations, definition-shaped lines first), searching from the
// symbol's first line (file line start) through its end, at most exactNameAnchorLines lines down.
func agentSearchDeclIndex(lines []string, first, start, end int, name string) (int, bool) {
	if name == "" || start < first || start >= first+len(lines) {
		return 0, false
	}
	last := first + len(lines) - 1
	if end >= start && end < last {
		last = end
	}
	if limit := start + exactNameAnchorLines - 1; limit < last {
		last = limit
	}
	return sem.DeclarationLineIndex(lines, start-first, last-first, name)
}

// agentSearchSignatureContinuation counts the lines after lines[index] that continue an unfinished
// signature: while the line so far ends in `(` or `,`, the next line belongs to it.
func agentSearchSignatureContinuation(lines []string, index, last int) int {
	cont := 0
	for at := index; cont < agentSearchSignatureLines && at < last; at++ {
		trimmed := strings.TrimRight(lines[at], " \t\r")
		if !strings.HasSuffix(trimmed, "(") && !strings.HasSuffix(trimmed, ",") {
			break
		}
		cont++
	}
	return cont
}

// agentSearchDecls returns the block's own declaration (ok reports whether there is one) and the
// declarations of the members a merged span absorbed, in file order.
func agentSearchDecls(view agentSearchBlockView) (agentSearchDecl, bool, []agentSearchDecl) {
	result, lines, first := view.result, view.lines, view.first
	last := len(lines) - 1
	if result.SymbolEndLine >= first && result.SymbolEndLine-first < last {
		last = result.SymbolEndLine - first
	}
	var own agentSearchDecl
	index, hasOwn, parsed := agentSearchNameLineIndex(result, first, len(lines))
	if !parsed {
		index, hasOwn = agentSearchDeclIndex(lines, first, result.SymbolStartLine, result.SymbolEndLine, result.SymbolName)
	}
	if hasOwn {
		own = agentSearchDecl{index: index, cont: agentSearchSignatureContinuation(lines, index, last)}
	}
	var absorbed []agentSearchDecl
	starts := result.MergedDeclStarts
	if len(starts) != len(result.MergedDeclLines) {
		starts = nil
	}
	for k, line := range result.MergedDeclLines {
		index := line - first
		if index < 0 || index >= len(lines) || hasOwn && index == own.index {
			continue
		}
		decl := agentSearchDecl{index: index}
		if starts != nil && starts[k] > 0 && starts[k] <= line {
			decl.region, decl.hasRegion = max(starts[k]-first, 0), true
		}
		absorbed = append(absorbed, decl)
	}
	return own, hasOwn, absorbed
}

// agentSearchNameLineIndex returns the snippet index of the parser's name line for the result's own
// symbol when it lies in the symbol's span and in the snippet of n lines starting at file line first.
func agentSearchNameLineIndex(result sem.SearchResult, first, n int) (index int, ok, parsed bool) {
	line := result.SymbolNameLine
	if line <= 0 || line < result.SymbolStartLine || result.SymbolEndLine >= result.SymbolStartLine && line > result.SymbolEndLine ||
		line < first || line >= first+n {
		return 0, false, false
	}
	return line - first, true, true
}

// agentSearchDeclarationBlock returns the block with its declaration(s) shown, or nil when the
// ordinary block (plain, covering snippet indexes left..right; nil when nothing fitted) should be
// used unchanged.
func agentSearchDeclarationBlock(view agentSearchBlockView, plain []byte, left, right, budget int) []byte {
	own, hasOwn, absorbed := agentSearchDecls(view)
	shownByPlain := func(decl agentSearchDecl) bool {
		return plain != nil && decl.index >= left && decl.index <= right
	}
	// The mandatory set: the block's own declaration, every declaration the ordinary window already
	// shows, and (defence in depth) every line the ordinary window showed near the symbol's start
	// that names it, or that follows an absorbed member's annotation (agentSearchPlainHeads). The
	// last is what keeps "never locate less" true when a finder takes the wrong line for a
	// declaration: any line the old block printed that could have been the declaration is printed. The optional set, in
	// priority order: the own signature's continuation, then the absorbed declarations the ordinary
	// window does not show.
	var base, optional []agentSearchDecl
	missing := false
	if hasOwn {
		base = append(base, agentSearchDecl{index: own.index})
		missing = missing || !shownByPlain(own)
	}
	for _, decl := range absorbed {
		if shownByPlain(decl) {
			base = append(base, decl)
		} else {
			optional = append(optional, decl)
			missing = true
		}
	}
	if !missing {
		return nil
	}
	base = append(base, agentSearchPlainHeads(view, absorbed, plain, left, right)...)
	if hasOwn && own.cont > 0 {
		optional = append([]agentSearchDecl{own}, optional...)
	}

	baseBlock, baseBody := plain, agentSearchBodyLines(len(view.lines), base, left, right)
	if plain == nil {
		baseBody = 0
	}
	if hasOwn && !shownByPlain(own) {
		baseBlock, baseBody = agentSearchWidestWithDecls(view, base, budget)
	}
	if baseBlock == nil {
		// No window fits beside the mandatory lines. Print them alone when they fit: the line that
		// names the callable locates it; a window of body under a header does not.
		if len(base) == 0 {
			return nil
		}
		return agentSearchDeclsOnly(view, base, budget)
	}
	// Richer variants, richest first. A variant adding k optional groups is taken only when it
	// still prints at least baseBody-k BODY lines (source lines outside every declaration group;
	// elision lines are not body), so each added declaration displaces at most one body line.
	for count := len(optional); count > 0; count-- {
		tier := append(append([]agentSearchDecl(nil), base...), optional[:count]...)
		block, body := agentSearchWidestWithDecls(view, tier, budget)
		if block != nil && body >= baseBody-count {
			return block
		}
	}
	if hasOwn && !shownByPlain(own) {
		return baseBlock
	}
	return nil
}

// agentSearchPlainHeads returns, as head groups, the lines the ordinary window (left..right; none
// when plain is nil) printed that any finder could have taken for a declaration:
//
//   - within agentSearchHeadLines lines of the symbol's first line, every line that contains the
//     symbol's name as a whole identifier ANYWHERE — inside an annotation, a literal or a comment
//     too. That is the superset of every candidate a declaration finder chooses from, so whichever
//     of them the finder picked, and whether or not it picked the right one, a line the old block
//     showed that names the symbol near its start is still shown;
//   - for an absorbed member, whose name the renderer does not have, the recorded declaration line
//     and, when that line is an annotation, decorator, attribute or comment, the lines after it up
//     to and including the first line that is not (the declaration an annotation line stands above).
//
// Only these lines, not every line the old window showed near a symbol's start: forcing arbitrary
// body lines printed them as lone islands between elision lines and bought nothing a reader locates by.
func agentSearchPlainHeads(view agentSearchBlockView, absorbed []agentSearchDecl, plain []byte, left, right int) []agentSearchDecl {
	if plain == nil || left < 0 {
		return nil
	}
	in := make([]bool, len(view.lines))
	result := view.result
	if index, _, parsed := agentSearchNameLineIndex(result, view.first, len(view.lines)); parsed {
		// Exact: the whole header region the parser names, annotations included, with no bound.
		for i := max(result.SymbolStartLine-view.first, left, 0); i <= min(index, right); i++ {
			in[i] = true
		}
	} else if start := result.SymbolStartLine - view.first; result.SymbolStartLine > 0 && result.SymbolName != "" && start < len(view.lines) {
		for i := max(start, left); i <= min(start+agentSearchHeadLines-1, right); i++ {
			if agentSearchLineMentions(view.lines[i], result.SymbolName) {
				in[i] = true
			}
		}
	}
	for _, decl := range absorbed {
		if decl.hasRegion {
			for i := max(decl.region, left); i <= min(decl.index, right); i++ {
				in[i] = true
			}
			continue
		}
		// Text fallback: the renderer has no name for an absorbed member, so every line within
		// agentSearchHeadLines above its recorded line is a candidate for the real declaration,
		// and so is the annotation run below it.
		for i := max(decl.index-agentSearchHeadLines+1, left, 0); i <= min(decl.index, right); i++ {
			in[i] = true
		}
		for i := decl.index; i < min(decl.index+agentSearchHeadLines, len(view.lines)); i++ {
			if i >= left && i <= right {
				in[i] = true
			}
			if !exactNameLeadLine(view.lines[i]) {
				break
			}
		}
	}
	var heads []agentSearchDecl
	for i := 0; i < len(in); i++ {
		if !in[i] {
			continue
		}
		j := i
		for j+1 < len(in) && in[j+1] {
			j++
		}
		heads = append(heads, agentSearchDecl{index: i, cont: j - i, head: true})
		i = j
	}
	return heads
}

// agentSearchLineMentions reports whether line contains name as a whole identifier anywhere,
// literals, annotations and comments included.
func agentSearchLineMentions(line, name string) bool {
	for from := 0; from < len(line); {
		at := strings.Index(line[from:], name)
		if at < 0 {
			return false
		}
		at += from
		end := at + len(name)
		if !agentSearchIdentifierRuneBefore(line, at) && !agentSearchIdentifierRuneAt(line, end) {
			return true
		}
		from = at + 1
	}
	return false
}

// agentSearchBodyLines counts the BODY lines a candidate prints: the source lines of the window
// left..right (none when left < 0) and of every group, minus those inside a declaration group (a
// group that is not a head). Elision lines are not counted.
func agentSearchBodyLines(n int, decls []agentSearchDecl, left, right int) int {
	shown := make([]bool, n)
	declared := make([]bool, n)
	if left >= 0 {
		for i := left; i <= right && i < n; i++ {
			shown[i] = true
		}
	}
	for _, decl := range decls {
		for i := decl.index; i <= decl.index+decl.cont && i < n; i++ {
			shown[i] = true
			if !decl.head {
				declared[i] = true
			}
		}
	}
	body := 0
	for i := range shown {
		if shown[i] && !declared[i] {
			body++
		}
	}
	return body
}

// agentSearchWidestWithDecls is the ordinary widest-balanced-window search with the given
// groups always shown. It returns the block and how many BODY lines it prints (agentSearchBodyLines),
// or nil when no window fits.
//
// It chooses exactly what the ordinary search would among windows that fit — the widest span, then
// the best balance about the focus, then the leftmost — but it prices a candidate from prefix sums
// and a lower bound on its header before rendering anything, and tries a span's windows best balance
// first so the first that fits is the answer. Rendering every candidate the way the ordinary search
// does cost ~3x its time on a 1,000-line snippet; this is linear in the windows it rejects.
func agentSearchWidestWithDecls(view agentSearchBlockView, decls []agentSearchDecl, budget int) ([]byte, int) {
	lines, focus := view.lines, view.focus
	prefix := make([]int, len(lines)+1)
	for i, line := range lines {
		prefix[i+1] = prefix[i] + len(line) + 1
	}
	groups := make([][2]int, 0, len(decls)+1)
	for _, decl := range decls {
		groups = append(groups, [2]int{decl.index, min(decl.index+decl.cont, len(lines)-1)})
	}
	// A lower bound on every header rung: each holds the path, a colon, at least one digit, and at
	// least two closing bytes (`*\n` or `]\n`). It is only a bound, never an estimate, so pricing a
	// candidate against it can reject only what no rung could fit.
	floor := len(view.result.FilePath) + 4
	lefts := make([]int, 0, len(lines))
	for span := len(lines); span > 0; span-- {
		leftMin := max(focus-span+1, 0)
		leftMax := min(focus, len(lines)-span)
		lefts = lefts[:0]
		for left := leftMin; left <= leftMax; left++ {
			lefts = append(lefts, left)
		}
		balance := func(left int) int {
			value := focus - left - (left + span - 1 - focus)
			if value < 0 {
				return -value
			}
			return value
		}
		sort.SliceStable(lefts, func(i, j int) bool { return balance(lefts[i]) < balance(lefts[j]) })
		for _, left := range lefts {
			right := left + span - 1
			if budget > 0 && floor+agentSearchDeclBodyBytes(prefix, groups, left, right) > budget {
				continue
			}
			if block, body := agentSearchRenderDecls(view, decls, left, right, budget); block != nil {
				return block, body
			}
		}
	}
	return nil, 0
}

// agentSearchDeclBodyBytes is the byte cost of the lines a candidate prints under its header: the
// window left..right and every declaration group, merged, with one elision line per gap.
func agentSearchDeclBodyBytes(prefix []int, groups [][2]int, left, right int) int {
	spans := make([][2]int, 0, len(groups)+1)
	spans = append(spans, [2]int{left, right})
	spans = append(spans, groups...)
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	total := 0
	current := spans[0]
	for _, next := range spans[1:] {
		if next[0] <= current[1]+1 {
			current[1] = max(current[1], next[1])
			continue
		}
		total += prefix[current[1]+1] - prefix[current[0]] + len(agentSearchElisionLine(next[0]-current[1]-1))
		current = next
	}
	return total + prefix[current[1]+1] - prefix[current[0]]
}

// agentSearchDeclsOnly prints the declarations with no focus window.
func agentSearchDeclsOnly(view agentSearchBlockView, decls []agentSearchDecl, budget int) []byte {
	block, _ := agentSearchRenderDecls(view, decls, -1, -1, budget)
	return block
}

// agentSearchRenderDecls renders the snippet lines left..right (none when left < 0) together with
// each group's lines, in file order, an elision line standing for every run of lines left out
// between two printed ones. It returns the block and the BODY lines it prints
// (agentSearchBodyLines), or nil when no header rung fits the budget.
//
// Every printed line's file line number is recoverable from the block alone: the rich and compact
// rungs name the range first-last, and the minimal rung names the FIRST printed line (`path:N *`),
// not the focus line the ordinary block's minimal rung names. Counting down from it, a source line
// adds one and an elision line adds its count. An ordinary block's window is contiguous around its
// focus; a declaration block's first line is the declaration, often many lines above the focus, and
// `path:FOCUS *` over it read as "this declaration is at FOCUS". The locator keeps the shape every
// other minimal rung has (`<path>:<digits> *`), so agentSearchLineIsLocator and the record grammar
// the forgery quarantine matches are unchanged.
func agentSearchRenderDecls(view agentSearchBlockView, decls []agentSearchDecl, left, right, budget int) ([]byte, int) {
	lines := view.lines
	shown := make([]bool, len(lines))
	if left >= 0 {
		for i := left; i <= right; i++ {
			shown[i] = true
		}
	}
	for _, decl := range decls {
		for i := decl.index; i <= decl.index+decl.cont && i < len(lines); i++ {
			shown[i] = true
		}
	}
	top, bottom := -1, -1
	for i, on := range shown {
		if on {
			if top < 0 {
				top = i
			}
			bottom = i
		}
	}
	if top < 0 {
		return nil, 0
	}
	var text strings.Builder
	for i := top; i <= bottom; {
		if shown[i] {
			text.WriteString(lines[i])
			text.WriteByte('\n')
			i++
			continue
		}
		gap := i
		for gap <= bottom && !shown[gap] {
			gap++
		}
		text.WriteString(agentSearchElisionLine(gap - i))
		i = gap
	}
	result := view.result
	headers := agentSearchLocationHeaders(result.Rank, result.FilePath, view.first+top, view.first+bottom,
		view.focusLine, view.name, view.tag, view.scored, "")
	headers[len(headers)-1] = fmt.Sprintf("%s:%d *\n", result.FilePath, view.first+top)
	for _, header := range headers {
		// Measured ESCAPED, the form it is printed in. A declaration line the ordinary window did
		// not print can carry control bytes that escape to several bytes each; measured raw, it
		// overran its share after escaping, and the final fitter then shrank every row's share to
		// compensate, costing another row a declaration it had shown (the raw pricing bound in
		// agentSearchWidestWithDecls stays a valid lower bound: escaping never shrinks a line).
		if candidate := header + text.String(); budget <= 0 || len(termsafe.Bytes([]byte(candidate))) <= budget {
			return []byte(candidate), agentSearchBodyLines(len(lines), decls, left, right)
		}
	}
	return nil, 0
}

// agentSearchElisionLine stands for n source lines a block leaves out between two it prints. It is
// not shaped like a locator (agentSearchLineIsLocator): its first token carries no `:<digits>`. A
// source line with this exact spelling is quarantined (indented) by the forgery quarantine, so in a
// payload an unindented line of this shape is always the renderer's gap record.
func agentSearchElisionLine(n int) string {
	return fmt.Sprintf("... %d line%s elided\n", n, pluralSuffix(n))
}

// agentSearchLineIsElision reports whether line (without its newline) has the gap record's shape:
// `... <digits> line elided` or `... <digits> lines elided`, allowing trailing whitespace.
func agentSearchLineIsElision(line string) bool {
	rest, ok := strings.CutPrefix(line, "... ")
	if !ok {
		return false
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return false
	}
	rest = strings.TrimRight(rest[digits:], " \t\r")
	return rest == " line elided" || rest == " lines elided"
}

// agentSearchIdentifierRune reports whether r continues an identifier: a Unicode letter or digit,
// `_` or `$` (sem's declaration lexer uses the same rule), so `naïve2` does not mention `naïve`.
func agentSearchIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) ||
		unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
}

func agentSearchIdentifierRuneBefore(line string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(line[:i])
	return agentSearchIdentifierRune(r)
}

func agentSearchIdentifierRuneAt(line string, i int) bool {
	if i >= len(line) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(line[i:])
	return agentSearchIdentifierRune(r)
}
