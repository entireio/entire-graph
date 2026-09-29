package sem

import "strings"

// Declaration lines
// =================
//
// Several renderers anchor a symbol on "the line that names it": the index says where a symbol
// STARTS, and for Java, C#, Rust and Python that is often an annotation, attribute or decorator,
// so the naming line is found by scanning down from the start for the name as a whole identifier.
//
// A plain whole-identifier scan takes the first line that CONTAINS the name, and an annotation or
// decorator argument often does: `@Named("fooBar")` above `public void fooBar(`, `@app.route("/login")`
// above `def login():`, `#[route("x")]` above `fn x()`, `[Route("x")]` above `public void x()`, and a
// Go struct tag `json:"x"`. The first line then "is" the declaration and the real signature is
// treated as body — printed below an annotation as if it were the declaration, or elided entirely.
//
// DeclarationLineIndex is the one finder every such renderer uses. It is language-agnostic and
// conservative:
//
//   - an occurrence counts only OUTSIDE string, char and raw-string literals, outside a trailing
//     `//` comment, and not inside the leading annotations/attributes/decorators of the line
//     (`@Name(...)`, `#[...]`, `#![...]`, `[...]`), which are stripped first; a whole-line comment
//     (`//`, `/*`, `* `, `#` other than `#define`/`#[`, `--`) has none;
//   - among the lines that have one, a DEFINITION-SHAPED line is preferred: the name followed by
//     `(`, `<`, `[`, `:`, `=` (not `::`, `==`, `=>`), or preceded by a definition keyword (func, def,
//     fn, class, type, struct, interface, ...). Failing that, the first line with an occurrence;
//   - when no line has an occurrence the finder reports none, and callers leave their output as it
//     was rather than anchoring on a line that only mentions the name in a literal or annotation.

// declarationKeywords introduce a definition of the identifier that follows them, in some language.
// A keyword of one language is harmless in another: it only raises a line that already names the
// symbol outside every literal above one that names it with no definition shape at all.
var declarationKeywords = map[string]bool{
	"func": true, "function": true, "def": true, "fn": true, "fun": true, "sub": true, "proc": true,
	"class": true, "struct": true, "interface": true, "type": true, "enum": true, "trait": true,
	"impl": true, "union": true, "typedef": true, "record": true, "object": true, "protocol": true,
	"extension": true, "module": true, "mod": true, "namespace": true, "macro": true,
	"defmodule": true, "defp": true, "defmacro": true, "defn": true, "defun": true,
	"var": true, "let": true, "val": true, "const": true, "static": true, "define": true,
}

// DeclarationLineIndex returns the index in lines[from..to] (inclusive, clamped to lines) of the
// line that declares name: the first definition-shaped line with an occurrence of name outside
// literals and annotations, else the first line with such an occurrence. ok is false when no line
// in the range has one.
func DeclarationLineIndex(lines []string, from, to int, name string) (int, bool) {
	if name == "" || from < 0 {
		return 0, false
	}
	if to >= len(lines) {
		to = len(lines) - 1
	}
	fallback := -1
	for i := from; i <= to; i++ {
		occurrences := DeclarationNameOccurrences(lines[i], name)
		if len(occurrences) == 0 {
			continue
		}
		for _, at := range occurrences {
			if declarationShaped(lines[i], at, name) {
				return i, true
			}
		}
		if fallback < 0 {
			fallback = i
		}
	}
	if fallback < 0 {
		return 0, false
	}
	return fallback, true
}

// DeclarationNameOccurrences returns the byte offsets at which name occurs in line as a whole
// identifier outside string/char literals, trailing `//` comments and the line's leading
// annotations, attributes or decorators. A whole-line comment has none.
func DeclarationNameOccurrences(line, name string) []int {
	if name == "" {
		return nil
	}
	start := len(line) - len(strings.TrimLeft(line, " \t"))
	rest := line[start:]
	if declarationCommentLine(rest) {
		return nil
	}
	start = declarationSkipLeadingAnnotations(line, start)
	var found []int
	for i := start; i < len(line); {
		c := line[i]
		switch {
		case c == '"' || c == '`':
			i = declarationSkipLiteral(line, i, c)
			continue
		case c == '\'':
			if lifetime := declarationLifetimeEnd(line, i); lifetime > 0 {
				i = lifetime
				continue
			}
			i = declarationSkipLiteral(line, i, c)
			continue
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return found
		}
		if strings.HasPrefix(line[i:], name) {
			end := i + len(name)
			if (i == 0 || !searchIdentifierByte(line[i-1])) && (end == len(line) || !searchIdentifierByte(line[end])) {
				found = append(found, i)
				i = end
				continue
			}
		}
		if searchIdentifierByte(c) {
			// Skip the rest of an identifier so a name is never matched inside a longer one.
			for i < len(line) && searchIdentifierByte(line[i]) {
				i++
			}
			continue
		}
		i++
	}
	return found
}

// declarationCommentLine reports whether a line (leading whitespace removed) is a comment line.
func declarationCommentLine(trimmed string) bool {
	switch {
	case strings.HasPrefix(trimmed, "//"), strings.HasPrefix(trimmed, "/*"), strings.HasPrefix(trimmed, "--"):
		return true
	case trimmed == "*", strings.HasPrefix(trimmed, "* "), strings.HasPrefix(trimmed, "*/"), strings.HasPrefix(trimmed, "*\t"):
		return true
	case strings.HasPrefix(trimmed, "#"):
		if strings.HasPrefix(trimmed, "#[") || strings.HasPrefix(trimmed, "#![") {
			return false
		}
		directive := strings.TrimLeft(trimmed[1:], " \t")
		return !strings.HasPrefix(directive, "define")
	}
	return false
}

// declarationSkipLeadingAnnotations returns the offset in line after the annotations, attributes
// and decorators that open it at offset i: `@Name`, `@a.b.c(...)`, `#[...]`, `#![...]`, `[...]`.
// `@interface` is Java's annotation-type keyword, not an annotation, and is left in place.
func declarationSkipLeadingAnnotations(line string, i int) int {
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			return i
		}
		switch {
		case line[i] == '@':
			j := i + 1
			for j < len(line) && (searchIdentifierByte(line[j]) || line[j] == '.') {
				j++
			}
			if j == i+1 || line[i+1:j] == "interface" {
				return i
			}
			k := j
			for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
				k++
			}
			if k < len(line) && line[k] == '(' {
				j = declarationSkipBalanced(line, k, '(', ')')
			}
			i = j
		case strings.HasPrefix(line[i:], "#["):
			i = declarationSkipBalanced(line, i+1, '[', ']')
		case strings.HasPrefix(line[i:], "#!["):
			i = declarationSkipBalanced(line, i+2, '[', ']')
		case line[i] == '[':
			i = declarationSkipBalanced(line, i, '[', ']')
		default:
			return i
		}
	}
}

// declarationSkipBalanced returns the offset after the bracket that closes line[i] (== open),
// skipping literals; len(line) when it is not closed on this line.
func declarationSkipBalanced(line string, i int, open, close byte) int {
	depth := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == '"' || c == '`':
			i = declarationSkipLiteral(line, i, c)
			continue
		case c == '\'':
			if lifetime := declarationLifetimeEnd(line, i); lifetime > 0 {
				i = lifetime
				continue
			}
			i = declarationSkipLiteral(line, i, c)
			continue
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(line)
}

// declarationSkipLiteral returns the offset after the literal opened by quote at line[i]: the next
// unescaped quote (backslash escapes do not apply to a backtick raw string), or len(line).
func declarationSkipLiteral(line string, i int, quote byte) int {
	for j := i + 1; j < len(line); j++ {
		if line[j] == '\\' && quote != '`' {
			j++
			continue
		}
		if line[j] == quote {
			return j + 1
		}
	}
	return len(line)
}

// declarationLifetimeEnd recognises a single quote that does not open a literal: a Rust lifetime
// (`<'a>`, `&'a str`, `<'a, 'b>`, `T: 'static +`). It returns the offset after the lifetime's name,
// or 0 when line[i] opens a char or string literal. A quote counts as a lifetime only in a lifetime's
// position — right after `<` or `&`, or after `,` `:` `+` with `>` `,` `+` next — because a Python or
// SQL string that starts with a word (`'def f(): pass'`) is otherwise indistinguishable from one.
func declarationLifetimeEnd(line string, i int) int {
	j := i + 1
	for j < len(line) && searchIdentifierByte(line[j]) {
		j++
	}
	if j == i+1 || j < len(line) && line[j] == '\'' {
		return 0
	}
	prev := strings.TrimRight(line[:i], " \t")
	if prev == "" {
		return 0
	}
	switch prev[len(prev)-1] {
	case '<', '&':
		return j
	case ',', ':', '+':
		if j < len(line) && strings.IndexByte(">,+", line[j]) >= 0 {
			return j
		}
	}
	return 0
}

// declarationShaped reports whether the occurrence of name at line[at] is shaped like a definition:
// followed by `(`, `<`, `[`, `:` or `=` (not `::`, `==` or `=>`), or preceded by a definition keyword.
func declarationShaped(line string, at int, name string) bool {
	after := strings.TrimLeft(line[at+len(name):], " \t")
	if after != "" {
		switch after[0] {
		case '(', '<', '[':
			return true
		case ':':
			if !strings.HasPrefix(after, "::") {
				return true
			}
		case '=':
			if !strings.HasPrefix(after, "==") && !strings.HasPrefix(after, "=>") {
				return true
			}
		}
	}
	before := strings.TrimRight(line[:at], " \t*&")
	word := len(before)
	for word > 0 && searchIdentifierByte(before[word-1]) {
		word--
	}
	return word < len(before) && declarationKeywords[before[word:]]
}
