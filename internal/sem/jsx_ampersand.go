package sem

import (
	"context"
	"unicode"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"
)

// A bare `&` in JSX text ("Terms & Privacy") or in a JSX attribute string
// (`href="/s?q=1&page=2"`) is valid JSX: TypeScript and Babel keep it as
// literal text. The bundled tree-sitter JavaScript and TSX grammars do not
// accept it. Their JSX text token stops at `&` so that html_character_reference
// can match `&amp;`, and an `&` that starts no character reference matches no
// token at all, so the parser emits an ERROR node and a valid .tsx, .jsx or .js
// file is reported as E_PARSE_ERROR (issue #409). Attribute strings fail the
// same way when the `&` is followed by a letter or `#`. Upstream
// tree-sitter-javascript's JSX text scanner still stops at `&`, so refreshing
// the grammar would not fix it.
//
// reparseJSXBareAmpersands blanks those ampersands to spaces, one byte for one
// byte, and parses again. JSX text runs until the next `<` or `{`, an attribute
// string until its closing quote, and a space is valid anywhere in either, so
// the masked source parses as the same elements with the same byte offsets;
// entities are still sliced from the original content.
//
// Which `&` is in JSX is read from the tree the parse produced, not from a
// lexical scan: telling JSX apart from comparisons, generics and bitwise `&`
// needs a full JavaScript lexer. Only an `&` right after a node that JSX
// children follow counts (jsx_text, an opening tag, or a child element,
// expression or character reference of a jsx_element), or one inside the
// string value of a jsx_attribute, and only on a tree that already has errors,
// so a file that parses cleanly pays nothing and `a & b` in ordinary code is
// never touched.
//
// When the bare `&` derails recovery badly enough that later JSX in the file is
// not recognised as JSX, the next pass finds it; jsxBareAmpersandMaxPasses
// bounds the passes, because each one is a full parse.
const jsxBareAmpersandMaxPasses = 4

// jsxBareAmpersandPollStride is how often the position walk checks the
// context, in visited nodes. It matches the stride of the Rust unwrap walk.
const jsxBareAmpersandPollStride = 512

// reparseJSXBareAmpersands returns the tree to use and the source it was parsed
// from. tree must be the parse of src. When it returns a different tree it has
// closed the one it was given, and the returned source is a masked copy: src
// itself is never written, because callers keep it as the entity source.
//
// ctx bounds the reparses. If one fails or is cancelled, the last complete
// parse is returned with the source it was parsed from. The error is ctx's when
// ctx ended the passes while that parse still has errors, so the caller reports
// a timeout instead of blaming source that a finished reparse might have
// accepted. It is nil otherwise, including when a pass fails for another
// reason: the last complete parse then stands, errors and all.
func reparseJSXBareAmpersands(ctx context.Context, parser *sitter.Parser, tree *sitter.Tree, src []byte) (*sitter.Tree, []byte, error) {
	current, currentSrc := tree, src
	for pass := 0; pass < jsxBareAmpersandMaxPasses; pass++ {
		root := current.RootNode()
		if !validNode(root) || !root.HasError() || ctx.Err() != nil {
			break
		}
		offsets := jsxBareAmpersandOffsets(ctx, root, currentSrc)
		if len(offsets) == 0 {
			break
		}
		masked := blankedCopy(currentSrc, offsets)
		next, err := parser.ParseCtx(ctx, nil, masked)
		if err != nil || next == nil || !validNode(next.RootNode()) {
			if next != nil {
				next.Close()
			}
			break
		}
		current.Close()
		current, currentSrc = next, masked
	}
	if root := current.RootNode(); validNode(root) && !root.HasError() {
		return current, currentSrc, nil
	}
	return current, currentSrc, ctx.Err()
}

// blankedCopy returns a copy of src with a space at each offset. src itself is
// not written.
func blankedCopy(src []byte, offsets []int) []byte {
	masked := append([]byte(nil), src...)
	for _, offset := range offsets {
		masked[offset] = ' '
	}
	return masked
}

// jsxBareAmpersandOffsets lists the byte offsets of bare `&` in JSX text that
// follows a JSX node in root, and in JSX attribute strings that contain an
// error. It descends only into subtrees that contain an error: an `&` the
// grammar rejected sits inside or beside an ERROR node, so the node it follows,
// or the string holding it, is a child of a node that has an error.
func jsxBareAmpersandOffsets(ctx context.Context, root *sitter.Node, src []byte) []int {
	scan := jsxAmpersandScan{src: src}
	scan.walk(ctx, root, 0)
	if scan.stopped {
		return nil
	}
	return scan.offsets
}

// jsxAmpersandScan is the state of one jsxBareAmpersandOffsets walk.
type jsxAmpersandScan struct {
	src     []byte
	offsets []int
	visited int
	stopped bool
	// scanned marks the bytes a text-run scan has already examined, so each
	// is examined once per walk; see appendTextRun. Allocated on first use.
	scanned []bool
}

// walk visits node's children and descends into those that contain an error.
// It is depth-bounded like every other walk over a tree that may be malformed;
// see maxParseWalkDepth. A bare `&` nested deeper than the limit is left in
// place and the file keeps its E_PARSE_ERROR.
func (s *jsxAmpersandScan) walk(ctx context.Context, node *sitter.Node, depth int) {
	if depth >= maxParseWalkDepth {
		return
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		if s.cancelled(ctx) {
			return
		}
		child := node.Child(i)
		if !validNode(child) {
			continue
		}
		s.visit(child, node)
		if child.HasError() {
			s.walk(ctx, child, depth+1)
		}
	}
}

// cancelled counts one visited node and reports whether the walk must stop,
// polling the context every jsxBareAmpersandPollStride nodes.
func (s *jsxAmpersandScan) cancelled(ctx context.Context) bool {
	s.visited++
	if s.visited%jsxBareAmpersandPollStride == 0 && ctx.Err() != nil {
		s.stopped = true
	}
	return s.stopped
}

// visit records the bare `&` that child, a child of parent, anchors: the JSX
// text run after it, or the bytes of the attribute string it is.
func (s *jsxAmpersandScan) visit(child, parent *sitter.Node) {
	if jsxChildrenFollow(child, parent) {
		s.appendTextRun(int(child.EndByte()))
	}
	if child.Type() == "string" && parent.Type() == "jsx_attribute" && child.HasError() {
		s.offsets = appendJSXAttributeStringAmpersands(s.offsets, s.src, int(child.StartByte()), int(child.EndByte()))
	}
}

// jsxChildrenFollow reports whether the source right after node is in JSX
// children position: after JSX text, after an opening tag (`<p>` or `<>`), or
// after a child of a jsx_element. A closing tag, a self-closing element used as
// an expression, and an attribute's `{...}` value are not: what follows them is
// ordinary code or the rest of a tag.
func jsxChildrenFollow(node, parent *sitter.Node) bool {
	switch node.Type() {
	case "jsx_text", "jsx_opening_element":
		return true
	case "jsx_element", "jsx_self_closing_element", "jsx_expression", "html_character_reference":
		return parent.Type() == "jsx_element"
	}
	return false
}

// appendTextRun appends the offsets of the bare `&` in the JSX text run that
// starts at the first non-space byte at or after start, provided that byte is
// itself a bare `&`. The run ends at the first `<` or `{`, where a child
// element or expression starts, or at a `>` or `}`, which JSX text may not
// contain. Character references such as `&amp;` in the run are kept.
//
// One run can hold several anchors, because error recovery may split it into
// fragments that each end before a bare `&`. Scanning the rest of the run from
// each of them would collect the same offsets again and again, quadratic in the
// run's length. So each byte is examined once per walk: a scan stops at the
// first byte an earlier scan examined. That loses nothing. An earlier scan that
// reached a byte went on to the end of its run, and a scan that only skipped
// whitespace stopped at the same first non-space byte this one would reach.
func (s *jsxAmpersandScan) appendTextRun(start int) {
	src := s.src
	if s.scanned == nil {
		s.scanned = make([]bool, len(src))
	}
	pos := s.skipRunSpace(start)
	if pos >= len(src) || s.scanned[pos] || src[pos] != '&' || jsxCharacterReferenceAt(src, pos) {
		return
	}
	for ; pos < len(src) && !s.scanned[pos]; pos++ {
		s.scanned[pos] = true
		switch src[pos] {
		case '<', '{', '>', '}':
			return
		case '&':
			if !jsxCharacterReferenceAt(src, pos) {
				s.offsets = append(s.offsets, pos)
			}
		}
	}
}

// skipRunSpace returns the offset of the first non-space byte at or after
// start, marking the whitespace it passes as examined. It stops early at a
// byte already examined, and the caller then stops too.
func (s *jsxAmpersandScan) skipRunSpace(start int) int {
	pos := start
	for pos < len(s.src) && !s.scanned[pos] {
		r, size := utf8.DecodeRune(s.src[pos:])
		if !unicode.IsSpace(r) {
			break
		}
		for end := pos + size; pos < end; pos++ {
			s.scanned[pos] = true
		}
	}
	return pos
}

// appendJSXAttributeStringAmpersands appends the offsets of the bare `&` in the
// JSX attribute string src[start:end] (`href="/s?q=1&page=2"`). The grammar's
// attribute string fragment rejects an `&` followed by a letter or `#` that
// starts no character reference, which is how a query string in a URL fails the
// parse. A JSX attribute string has no escapes, so it runs from its opening
// quote to the next same quote; the scan also stops at end, the string node's
// end, so an unterminated string is never scanned past what the parser
// assigned to it.
func appendJSXAttributeStringAmpersands(offsets []int, src []byte, start, end int) []int {
	if start >= end || end > len(src) {
		return offsets
	}
	if src[start] != '"' && src[start] != '\'' {
		return offsets
	}
	quote := src[start]
	for pos := start + 1; pos < end && src[pos] != quote; pos++ {
		if src[pos] == '&' && !jsxCharacterReferenceAt(src, pos) {
			offsets = append(offsets, pos)
		}
	}
	return offsets
}

// jsxCharacterReferenceAt reports whether src[pos] starts a token the grammar's
// html_character_reference accepts:
// /&(#([xX][0-9a-fA-F]{1,6}|[0-9]{1,5})|[A-Za-z]{1,30});/.
func jsxCharacterReferenceAt(src []byte, pos int) bool {
	i := pos + 1
	var end int
	if i < len(src) && src[i] == '#' {
		end = jsxNumericReferenceEnd(src, i+1)
	} else {
		end = byteRunEnd(src, i, 30, isASCIILetterByte)
	}
	return end >= 0 && end < len(src) && src[end] == ';'
}

// jsxNumericReferenceEnd returns the offset just past the digits of a numeric
// character reference whose digits start at i, right after `&#`: up to six hex
// digits after an `x` or `X`, otherwise up to five decimal digits. It returns
// -1 when there are no digits.
func jsxNumericReferenceEnd(src []byte, i int) int {
	if i < len(src) && (src[i] == 'x' || src[i] == 'X') {
		return byteRunEnd(src, i+1, 6, isHexDigitByte)
	}
	return byteRunEnd(src, i, 5, isDecimalDigitByte)
}

// byteRunEnd returns the offset just past the run of at most limit bytes from
// start that accept matches, or -1 when the run is empty.
func byteRunEnd(src []byte, start, limit int, accept func(byte) bool) int {
	end := start
	for end < len(src) && end-start < limit && accept(src[end]) {
		end++
	}
	if end == start {
		return -1
	}
	return end
}

func isHexDigitByte(b byte) bool {
	return isDecimalDigitByte(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isDecimalDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func isASCIILetterByte(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
