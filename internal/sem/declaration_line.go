package sem

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Declaration lines (text fallback)
// =================================
//
// Several renderers anchor a symbol on "the line that names it". The parser records that line
// (SymbolRecord.NameLine, SearchResult.SymbolNameLine; see declaration_name_line.go) and callers use
// it whenever it is present. This file is the FALLBACK for symbols with no parse node and for
// callers holding only text: it finds the naming line by lexing the lines the way a compiler would
// before looking for the name.
//
// Every earlier version of this finder looked at one line at a time and lost its lexical state at
// each newline, so each review round found another construct that spans lines and turned text into
// "code": a multi-line annotation's arguments (`@Policy(\n  run = true,\n)`), a raw string
// (`src := \`\nfunc run() {}\n\``), a block comment without leading stars, a docstring or heredoc.
// DeclarationCodeLines is one lexer that carries its state across lines and replaces every byte
// that is not code with a space, keeping byte offsets; the name search then runs on code only.
//
// What is not code:
//
//   - comments: whole-line `//`, `#` (other than `#[`, `#![`, `#define`), `--`, `*`-continuation;
//     trailing `//` and a trailing `#` comment (`#` after whitespace, followed by whitespace);
//     `/* ... */` across lines;
//   - literals: "..." and '...' (to the end of the line at most), a Rust lifetime excepted;
//     """...""", '''...''', `...`, Rust r"..."/r#"..."#, and C# @"..." across lines;
//   - annotations, attributes and decorators: `@Name`, `@a.b(...)`, `#[...]`, `#![...]`, and a
//     line-leading `[...]`, with their bracketed arguments across lines.
//
// Among the lines where the name then occurs as a whole identifier (identifier characters are
// Unicode letters and digits, `_` and `$`, so `runé` is not `run`), the first DEFINITION-SHAPED
// line wins, else the first line with an occurrence. When no line has one the finder reports none,
// and callers leave their output as it was.

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

// declarationNotTypeWords are words that, right before an identifier, make it a USE rather than a
// declared name in `Type Name` position: `return x`, `new Foo`, `extends Base`.
var declarationNotTypeWords = map[string]bool{
	"return": true, "new": true, "throw": true, "throws": true, "yield": true, "await": true,
	"else": true, "case": true, "goto": true, "in": true, "of": true, "is": true, "as": true,
	"not": true, "and": true, "or": true, "delete": true, "typeof": true, "sizeof": true,
	"instanceof": true, "echo": true, "print": true, "puts": true, "raise": true, "assert": true,
	"do": true, "then": true, "with": true, "import": true, "from": true, "using": true,
	"package": true, "include": true, "require": true, "extends": true, "implements": true,
	"go": true, "defer": true, "break": true, "continue": true, "when": true, "if": true,
	"while": true, "for": true, "until": true, "unless": true, "select": true, "where": true,
}

// DeclarationLineIndex returns the index in lines[from..to] (inclusive, clamped to lines) of the
// line that declares name: the first definition-shaped line with an occurrence of name in code,
// else the first line with such an occurrence. Lexical state is carried from lines[from] down, so
// a multi-line annotation, comment or literal is never code. ok is false when no line in the range
// has an occurrence.
func DeclarationLineIndex(lines []string, from, to int, name string) (int, bool) {
	if name == "" || from < 0 {
		return 0, false
	}
	if to >= len(lines) {
		to = len(lines) - 1
	}
	code := DeclarationCodeLines(lines, from, to)
	fallback := -1
	for k, line := range code {
		occurrences := declarationIdentifierOccurrences(line, name)
		if len(occurrences) == 0 {
			continue
		}
		for _, at := range occurrences {
			if declarationShaped(line, at, name) {
				return from + k, true
			}
		}
		if fallback < 0 {
			fallback = from + k
		}
	}
	if fallback < 0 {
		return 0, false
	}
	return fallback, true
}

// DeclarationNameOccurrences returns the byte offsets at which name occurs in line as a whole
// identifier in code, lexing the line on its own (no state from lines above).
func DeclarationNameOccurrences(line, name string) []int {
	if name == "" {
		return nil
	}
	var lexer declarationLexer
	return declarationIdentifierOccurrences(lexer.code(line), name)
}

// DeclarationCodeLines returns lines[from..to] (clamped) with every byte that is not code — comments,
// literals, annotations and their arguments — replaced by a space, so byte offsets are unchanged.
// State is carried from one line to the next; the scan assumes lines[from] starts in code.
func DeclarationCodeLines(lines []string, from, to int) []string {
	if from < 0 {
		from = 0
	}
	if to >= len(lines) {
		to = len(lines) - 1
	}
	if from > to {
		return nil
	}
	var lexer declarationLexer
	out := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, lexer.code(lines[i]))
	}
	return out
}

// declarationIdentifierOccurrences returns the offsets of name as a whole identifier in an
// already-masked line.
func declarationIdentifierOccurrences(line, name string) []int {
	if name == "" {
		return nil
	}
	var found []int
	for i := 0; i < len(line); {
		if strings.HasPrefix(line[i:], name) && !declarationIdentifierBefore(line, i) &&
			!declarationIdentifierAfter(line, i+len(name)) {
			found = append(found, i)
			i += len(name)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if declarationIdentifierRune(r) {
			// Skip the rest of an identifier so a name is never matched inside a longer one.
			for i < len(line) {
				r, size = utf8.DecodeRuneInString(line[i:])
				if !declarationIdentifierRune(r) {
					break
				}
				i += size
			}
			continue
		}
		i += size
	}
	return found
}

// declarationIdentifierRune reports whether r continues an identifier: a Unicode letter or digit,
// `_` or `$`. Every language this serves allows at least ASCII letters, digits and `_`; counting
// every Unicode letter keeps `runé` from containing an occurrence of `run`.
func declarationIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) ||
		unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
}

func declarationIdentifierBefore(line string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(line[:i])
	return declarationIdentifierRune(r)
}

func declarationIdentifierAfter(line string, i int) bool {
	if i >= len(line) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(line[i:])
	return declarationIdentifierRune(r)
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

type declarationMode int

const (
	declarationInCode declarationMode = iota
	declarationInBlockComment
	declarationInTriple   // """...""" or '''...''' (quote holds the quote byte)
	declarationInBacktick // `...`
	declarationInRaw      // Rust r"..." / r#"..."# (hashes holds the # count)
	declarationInVerbatim // C# @"..." ("" escapes a quote)
)

// declarationLexer masks non-code bytes line by line, carrying multi-line constructs across lines.
type declarationLexer struct {
	mode   declarationMode
	quote  byte
	hashes int
	// annotation is the bracket depth of an open annotation, attribute or decorator argument list;
	// while it is positive every byte is masked, and literals inside it are still lexed so a
	// bracket inside a string does not close it.
	annotation int
	// annotationLead records that the open annotation began the line, so the code after it is
	// still at the line's lead (another annotation or a comment may follow).
	annotationLead bool
}

// code returns line with every non-code byte replaced by a space.
func (lexer *declarationLexer) code(line string) string {
	out := []byte(line)
	blank := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			out[k] = ' '
		}
	}
	lead := true // only whitespace and annotations so far on this line
	for i := 0; i < len(line); {
		switch lexer.mode {
		case declarationInBlockComment:
			end := strings.Index(line[i:], "*/")
			if end < 0 {
				blank(i, len(line))
				return string(out)
			}
			blank(i, i+end+2)
			i += end + 2
			lexer.mode = declarationInCode
			continue
		case declarationInTriple:
			closing := strings.Repeat(string(lexer.quote), 3)
			end := strings.Index(line[i:], closing)
			if end < 0 {
				blank(i, len(line))
				return string(out)
			}
			blank(i, i+end+3)
			i += end + 3
			lexer.mode = declarationInCode
			continue
		case declarationInBacktick:
			end := strings.IndexByte(line[i:], '`')
			if end < 0 {
				blank(i, len(line))
				return string(out)
			}
			blank(i, i+end+1)
			i += end + 1
			lexer.mode = declarationInCode
			continue
		case declarationInRaw:
			closing := `"` + strings.Repeat("#", lexer.hashes)
			end := strings.Index(line[i:], closing)
			if end < 0 {
				blank(i, len(line))
				return string(out)
			}
			blank(i, i+end+len(closing))
			i += end + len(closing)
			lexer.mode = declarationInCode
			continue
		case declarationInVerbatim:
			j := i
			for j < len(line) {
				if line[j] == '"' {
					if j+1 < len(line) && line[j+1] == '"' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(line) {
				blank(i, len(line))
				return string(out)
			}
			blank(i, j+1)
			i = j + 1
			lexer.mode = declarationInCode
			continue
		}
		c := line[i]
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		// Comments and literals are lexed the same way inside and outside an annotation.
		if strings.HasPrefix(line[i:], "/*") {
			blank(i, i+2)
			i += 2
			lexer.mode = declarationInBlockComment
			continue
		}
		if strings.HasPrefix(line[i:], "//") {
			blank(i, len(line))
			return string(out)
		}
		if next, ok := lexer.literal(line, i); ok {
			blank(i, next)
			i = next
			continue
		}
		if lexer.annotation > 0 {
			switch c {
			case '(', '[', '{':
				lexer.annotation++
			case ')', ']', '}':
				lexer.annotation--
				if lexer.annotation == 0 {
					lead = lexer.annotationLead
				}
			}
			blank(i, i+1)
			i++
			continue
		}
		if lead && declarationCommentLine(line[i:]) {
			blank(i, len(line))
			return string(out)
		}
		if c == '#' && i > 0 && (line[i-1] == ' ' || line[i-1] == '\t') &&
			(i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t') {
			// A trailing `# comment` (Python, Ruby, shell, YAML, ...).
			blank(i, len(line))
			return string(out)
		}
		if end, open, ok := declarationAnnotationStart(line, i, lead); ok {
			blank(i, end)
			i = end
			if open {
				lexer.annotation, lexer.annotationLead = 1, lead
			}
			continue
		}
		lead = false
		i++
	}
	return string(out)
}

// literal recognises a literal opening at line[i]. It returns the offset after the part of the
// literal on this line and true; a literal that continues past the line sets the lexer's mode.
func (lexer *declarationLexer) literal(line string, i int) (int, bool) {
	c := line[i]
	switch {
	case c == '"' || c == '\'':
		if strings.HasPrefix(line[i:], strings.Repeat(string(c), 3)) {
			if end := strings.Index(line[i+3:], strings.Repeat(string(c), 3)); end >= 0 {
				return i + 3 + end + 3, true
			}
			lexer.mode, lexer.quote = declarationInTriple, c
			return len(line), true
		}
		if c == '"' && i > 0 && line[i-1] == '@' {
			lexer.mode = declarationInVerbatim
			return i + 1, true
		}
		if c == '\'' {
			if lifetime := declarationLifetimeEnd(line, i); lifetime > 0 {
				return 0, false
			}
		}
		return declarationSkipLiteral(line, i, c), true
	case c == '`':
		if end := strings.IndexByte(line[i+1:], '`'); end >= 0 {
			return i + 1 + end + 1, true
		}
		lexer.mode = declarationInBacktick
		return len(line), true
	case c == 'r' || c == 'b':
		// Rust raw strings: r"..", r#".."#, br"..", br#".."#; Python r".." closes the same way.
		j := i
		if c == 'b' {
			j++
			if j >= len(line) || line[j] != 'r' {
				return 0, false
			}
		}
		if i > 0 && declarationIdentifierBefore(line, i) {
			return 0, false
		}
		j++
		hashes := 0
		for j < len(line) && line[j] == '#' {
			hashes++
			j++
		}
		if j >= len(line) || line[j] != '"' {
			return 0, false
		}
		if hashes == 0 && strings.HasPrefix(line[j:], `"""`) {
			return 0, false // r"""...""": the triple-quote case handles it from the quote
		}
		closing := `"` + strings.Repeat("#", hashes)
		if end := strings.Index(line[j+1:], closing); end >= 0 {
			return j + 1 + end + len(closing), true
		}
		lexer.mode, lexer.hashes = declarationInRaw, hashes
		return len(line), true
	}
	return 0, false
}

// declarationAnnotationStart recognises an annotation, attribute or decorator at line[i]: `@Name`,
// `@a.b.c` (optionally followed by an argument list), `#[`, `#![`, or — at the start of a line —
// `[`. It returns the offset after its name (after the opening bracket when it has arguments),
// whether an argument list was opened, and ok. `@interface` is Java's annotation-type keyword, and
// `@"` opens a C# verbatim string; neither is an annotation.
func declarationAnnotationStart(line string, i int, lead bool) (end int, open, ok bool) {
	switch {
	case line[i] == '@':
		if i > 0 && !lead {
			prev := line[i-1]
			if prev != ' ' && prev != '\t' && prev != '(' && prev != ',' {
				return 0, false, false
			}
		}
		j := i + 1
		for j < len(line) {
			r, size := utf8.DecodeRuneInString(line[j:])
			if !declarationIdentifierRune(r) && r != '.' {
				break
			}
			j += size
		}
		if j == i+1 || line[i+1:j] == "interface" {
			return 0, false, false
		}
		k := j
		for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
			k++
		}
		if k < len(line) && line[k] == '(' {
			return k + 1, true, true
		}
		return j, false, true
	case strings.HasPrefix(line[i:], "#["):
		return i + 2, true, true
	case strings.HasPrefix(line[i:], "#!["):
		return i + 3, true, true
	case lead && line[i] == '[':
		return i + 1, true, true
	}
	return 0, false, false
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

// declarationShaped reports whether the occurrence of name at line[at] (a masked line) is shaped
// like a definition:
//
//   - followed by `(`, `<`, `[`, `:` or `=` (not `::`, `==` or `=>`);
//   - preceded by a definition keyword, possibly through a receiver or module path
//     (`def self.config`, `module Fixtures.Ledger`);
//   - in `Type Name` position: preceded by a type word (not `return`, `new`, ...) or by `>`, `]`,
//     `?`, `*`, `&`, and followed by nothing, `{`, `;`, `,`, `)`, `=>` — a C#, Java or C property,
//     field or parameter (`public Options Options`, `public int Count => ...`).
func declarationShaped(line string, at int, name string) bool {
	after := strings.TrimLeft(line[at+len(name):], " \t\r")
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
	before := strings.TrimRight(line[:at], " \t")
	// A receiver or module path: `self.`, `Fixtures.`, `Outer::`.
	path := before
	for {
		trimmed := strings.TrimSuffix(path, ".")
		if trimmed == path {
			trimmed = strings.TrimSuffix(path, "::")
		}
		if trimmed == path {
			break
		}
		word := declarationTrailingWord(trimmed)
		if word == "" {
			break
		}
		path = strings.TrimRight(trimmed[:len(trimmed)-len(word)], " \t")
	}
	if word := declarationTrailingWord(strings.TrimRight(path, " \t*&")); word != "" && declarationKeywords[word] {
		return true
	}
	if path != before {
		return false // a qualified use (`x.name`) is not `Type Name`
	}
	if after == "" || after[0] == '{' || after[0] == ';' || after[0] == ',' || after[0] == ')' || strings.HasPrefix(after, "=>") {
		if before == "" {
			return false
		}
		switch before[len(before)-1] {
		case '>', ']', '?', '*', '&':
			return true
		}
		word := declarationTrailingWord(before)
		return word != "" && !declarationNotTypeWords[word]
	}
	return false
}

// declarationTrailingWord returns the identifier that ends s, or "".
func declarationTrailingWord(s string) string {
	end := len(s)
	start := end
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:start])
		if !declarationIdentifierRune(r) {
			break
		}
		start -= size
	}
	return s[start:end]
}
