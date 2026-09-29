package cli

import (
	"fmt"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
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
//   - The declaration is the line sem.DeclarationLineIndex picks at or after the symbol's start,
//     within its span and at most exactNameAnchorLines down: the name as a whole identifier outside
//     literals and leading annotations, definition-shaped lines first (the exact-name anchor's rule,
//     so `@Named("fooBar")` or `@app.route("/login")` is stepped over rather than printed in the
//     signature's place). A symbol with no such line is rendered exactly as before.
//   - A multi-line signature's continuation lines (a named line ending in `(` or `,`) ride with the
//     declaration only when the block then prints at least as many lines in total, and so do the
//     declarations of the members a merged span absorbed (sem.SearchResult.MergedDeclLines): each
//     may replace a body line, never more than one. The survivor's own declaration is mandatory.
//   - Never locate less: a block whose ordinary window already shows every declaration is returned
//     byte for byte, and no variant is taken that drops a declaration the ordinary window showed.
//   - When no window fits beside the declaration, the declaration alone is printed rather than a
//     window without it; when not even that fits, the ordinary block (or bare header) is kept.
const (
	// agentSearchSignatureLines bounds the continuation lines of a multi-line signature, the same
	// bound the exact-name answer uses.
	agentSearchSignatureLines = exactNameSignatureLines
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

// agentSearchDecl is one declaration a block may show: the snippet index of the line that names
// the symbol and how many signature continuation lines follow it.
type agentSearchDecl struct {
	index, cont int
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
	index, hasOwn := agentSearchDeclIndex(lines, first, result.SymbolStartLine, result.SymbolEndLine, result.SymbolName)
	if hasOwn {
		own = agentSearchDecl{index: index, cont: agentSearchSignatureContinuation(lines, index, last)}
	}
	var absorbed []agentSearchDecl
	for _, line := range result.MergedDeclLines {
		index := line - first
		if index < 0 || index >= len(lines) || hasOwn && index == own.index {
			continue
		}
		absorbed = append(absorbed, agentSearchDecl{index: index})
	}
	return own, hasOwn, absorbed
}

// agentSearchDeclarationBlock returns the block with its declaration(s) shown, or nil when the
// ordinary block (plain, covering snippet indexes left..right; nil when nothing fitted) should be
// used unchanged.
func agentSearchDeclarationBlock(view agentSearchBlockView, plain []byte, left, right, budget int) []byte {
	own, hasOwn, absorbed := agentSearchDecls(view)
	shownByPlain := func(decl agentSearchDecl) bool {
		return plain != nil && decl.index >= left && decl.index <= right
	}
	// The mandatory set: the block's own declaration and every declaration the ordinary window
	// already shows. The optional set, in priority order: the own signature's continuation, then the
	// absorbed declarations the ordinary window does not show.
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
	if hasOwn && own.cont > 0 {
		optional = append([]agentSearchDecl{own}, optional...)
	}

	baseBlock, basePrinted := plain, right-left+1
	if plain == nil {
		basePrinted = 0
	}
	if hasOwn && !shownByPlain(own) {
		baseBlock, basePrinted = agentSearchWidestWithDecls(view, base, budget)
	}
	if baseBlock == nil {
		// No window fits beside the mandatory declarations. Print them alone when they fit: the
		// line that names the callable locates it; a window of body under a header does not.
		if len(base) == 0 {
			return nil
		}
		return agentSearchDeclsOnly(view, base, budget)
	}
	// Richer variants, richest first. One is taken only when the block still prints at least as
	// many lines as the base variant, so every added declaration line displaces at most one body line.
	for count := len(optional); count > 0; count-- {
		tier := append(append([]agentSearchDecl(nil), base...), optional[:count]...)
		block, printed := agentSearchWidestWithDecls(view, tier, budget)
		if block != nil && printed >= basePrinted {
			return block
		}
	}
	if hasOwn && !shownByPlain(own) {
		return baseBlock
	}
	return nil
}

// agentSearchWidestWithDecls is the ordinary widest-balanced-window search with the given
// declarations always shown. It returns the block and how many lines it prints under its header
// (source lines plus elision lines), or nil when no window fits.
func agentSearchWidestWithDecls(view agentSearchBlockView, decls []agentSearchDecl, budget int) ([]byte, int) {
	lines, focus := view.lines, view.focus
	for span := len(lines); span > 0; span-- {
		leftMin := max(focus-span+1, 0)
		leftMax := min(focus, len(lines)-span)
		bestBalance := len(lines) + 1
		var best []byte
		bestPrinted := 0
		for left := leftMin; left <= leftMax; left++ {
			right := left + span - 1
			block, printed := agentSearchRenderDecls(view, decls, left, right, budget)
			if block == nil {
				continue
			}
			balance := focus - left - (right - focus)
			if balance < 0 {
				balance = -balance
			}
			if best == nil || balance < bestBalance {
				best, bestBalance, bestPrinted = block, balance, printed
			}
		}
		if best != nil {
			return best, bestPrinted
		}
	}
	return nil, 0
}

// agentSearchDeclsOnly prints the declarations with no focus window.
func agentSearchDeclsOnly(view agentSearchBlockView, decls []agentSearchDecl, budget int) []byte {
	block, _ := agentSearchRenderDecls(view, decls, -1, -1, budget)
	return block
}

// agentSearchRenderDecls renders the snippet lines left..right (none when left < 0) together with
// each declaration's lines, in file order, an elision line standing for every run of lines left out
// between two printed ones. It returns nil when no header rung fits the budget.
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
	printed := 0
	for i := top; i <= bottom; {
		if shown[i] {
			text.WriteString(lines[i])
			text.WriteByte('\n')
			printed++
			i++
			continue
		}
		gap := i
		for gap <= bottom && !shown[gap] {
			gap++
		}
		text.WriteString(agentSearchElisionLine(gap - i))
		printed++
		i = gap
	}
	result := view.result
	for _, header := range agentSearchLocationHeaders(result.Rank, result.FilePath, view.first+top, view.first+bottom,
		view.focusLine, view.name, view.tag, view.scored, "") {
		if candidate := header + text.String(); budget <= 0 || len(candidate) <= budget {
			return []byte(candidate), printed
		}
	}
	return nil, 0
}

// agentSearchElisionLine stands for n source lines a block leaves out between two it prints. It is
// not shaped like a locator (agentSearchLineIsLocator): its first token carries no `:<digits>`.
func agentSearchElisionLine(n int) string {
	return fmt.Sprintf("... %d line%s elided\n", n, pluralSuffix(n))
}
