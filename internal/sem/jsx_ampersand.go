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
// parse is returned with the source it was parsed from.
func reparseJSXBareAmpersands(ctx context.Context, parser *sitter.Parser, tree *sitter.Tree, src []byte) (*sitter.Tree, []byte) {
	for pass := 0; pass < jsxBareAmpersandMaxPasses; pass++ {
		root := tree.RootNode()
		if !validNode(root) || !root.HasError() || ctx.Err() != nil {
			break
		}
		offsets := jsxBareAmpersandOffsets(ctx, root, src)
		if len(offsets) == 0 {
			break
		}
		masked := append([]byte(nil), src...)
		for _, offset := range offsets {
			masked[offset] = ' '
		}
		next, err := parser.ParseCtx(ctx, nil, masked)
		if err != nil || next == nil || !validNode(next.RootNode()) {
			if next != nil {
				next.Close()
			}
			break
		}
		tree.Close()
		tree, src = next, masked
	}
	return tree, src
}

// jsxBareAmpersandOffsets lists the byte offsets of bare `&` in JSX text that
// follows a JSX node in root, and in JSX attribute strings that contain an
// error. It descends only into subtrees that contain an error: an `&` the
// grammar rejected sits inside or beside an ERROR node, so the node it follows,
// or the string holding it, is a child of a node that has an error.
func jsxBareAmpersandOffsets(ctx context.Context, root *sitter.Node, src []byte) []int {
	var offsets []int
	visited := 0
	stopped := false
	// Depth-bounded like every other walk over a tree that may be malformed;
	// see maxParseWalkDepth. A bare `&` nested deeper than the limit is left in
	// place and the file keeps its E_PARSE_ERROR.
	var walk func(node *sitter.Node, depth int)
	walk = func(node *sitter.Node, depth int) {
		if depth >= maxParseWalkDepth {
			return
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			visited++
			if visited%jsxBareAmpersandPollStride == 0 && ctx.Err() != nil {
				stopped = true
			}
			if stopped {
				return
			}
			child := node.Child(i)
			if !validNode(child) {
				continue
			}
			if jsxChildrenFollow(child, node) {
				offsets = appendJSXTextRunAmpersands(offsets, src, int(child.EndByte()))
			}
			if child.Type() == "string" && node.Type() == "jsx_attribute" && child.HasError() {
				offsets = appendJSXAttributeStringAmpersands(offsets, src, int(child.StartByte()), int(child.EndByte()))
			}
			if child.HasError() {
				walk(child, depth+1)
			}
		}
	}
	walk(root, 0)
	if stopped {
		return nil
	}
	return offsets
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

// appendJSXTextRunAmpersands appends the offsets of the bare `&` in the JSX
// text run that starts at the first non-space byte at or after start, provided
// that byte is itself a bare `&`. The run ends at the first `<` or `{`, where a
// child element or expression starts, or at a `>` or `}`, which JSX text may
// not contain. Character references such as `&amp;` in the run are kept.
func appendJSXTextRunAmpersands(offsets []int, src []byte, start int) []int {
	pos := start
	for pos < len(src) {
		r, size := utf8.DecodeRune(src[pos:])
		if !unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	if pos >= len(src) || src[pos] != '&' || jsxCharacterReferenceAt(src, pos) {
		return offsets
	}
	for ; pos < len(src); pos++ {
		switch src[pos] {
		case '<', '{', '>', '}':
			return offsets
		case '&':
			if !jsxCharacterReferenceAt(src, pos) {
				offsets = append(offsets, pos)
			}
		}
	}
	return offsets
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
	if start >= end || end > len(src) || (src[start] != '"' && src[start] != '\'') {
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
	count := func(limit int, accept func(byte) bool) int {
		n := 0
		for n < limit && i < len(src) && accept(src[i]) {
			i++
			n++
		}
		return n
	}
	switch {
	case i < len(src) && src[i] == '#':
		i++
		if i < len(src) && (src[i] == 'x' || src[i] == 'X') {
			i++
			if count(6, isHexDigitByte) == 0 {
				return false
			}
		} else if count(5, isDecimalDigitByte) == 0 {
			return false
		}
	default:
		if count(30, isASCIILetterByte) == 0 {
			return false
		}
	}
	return i < len(src) && src[i] == ';'
}

func isHexDigitByte(b byte) bool {
	return isDecimalDigitByte(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isDecimalDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func isASCIILetterByte(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
