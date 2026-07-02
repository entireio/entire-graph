package sem

// Groovy is parsed by a dedicated structural scanner rather than tree-sitter:
// the best available tree-sitter-groovy grammar fails on fundamental Groovy
// syntax (quoted method names, Java-style casts, slashy strings, GString
// interpolation shapes), producing 1,400+ parse errors on real repos like
// apache/groovy and leaving the language's completeness "unsafe". Groovy is
// Java-like at declaration level, so a line/brace-structural scan recovers
// types, methods, and fields reliably once string literals and comments are
// masked out.
//
// The pipeline has two passes:
//
//  1. maskGroovyLiteralsAndComments blanks comment and string-literal content
//     (length- and newline-preserving), including triple-quoted strings,
//     GStrings with nested ${...} interpolation, slashy /regex/ strings
//     (opened only in expression position, so division survives), and
//     dollar-slashy $/.../$ strings. Delimiter characters are kept so later
//     passes can still see where literals were.
//  2. groovyScanner walks the masked source tracking brace depth, splits it
//     into statements, and classifies each statement header as a type
//     declaration, method, field, or nothing. Members are extracted only at
//     type-body or top level, so closures and method-body code never emit
//     symbols; their braces are still tracked for container scoping.

import (
	"regexp"
	"sort"
	"strings"
)

func groovyEntities(content string) ([]Entity, ParseStatus) {
	scanner := &groovyScanner{
		src:    content,
		masked: maskGroovyLiteralsAndComments(content),
		lines:  strings.Split(content, "\n"),
	}
	scanner.scan()
	entities := scanner.entities
	for idx := range entities {
		entity := &entities[idx]
		if entity.EndLine < entity.StartLine {
			entity.EndLine = entity.StartLine
		}
		if entity.EndLine > len(scanner.lines) {
			entity.EndLine = len(scanner.lines)
		}
		if entity.BodyHash == "" {
			block := strings.Join(scanner.lines[entity.StartLine-1:entity.EndLine], "\n")
			entity.BodyHash = hash(normalize(block))
			entity.Fingerprint = hash(normalize(entityFingerprintSource(Entity{Name: entity.Name, Signature: entity.Signature}, block)))
		}
	}
	sort.Slice(entities, func(i, j int) bool {
		if entities[i].StartLine == entities[j].StartLine {
			return entities[i].Name < entities[j].Name
		}
		return entities[i].StartLine < entities[j].StartLine
	})
	status := ParseStatus{}
	if scanner.braceErrors > 0 {
		status = ParseStatus{
			ParseError: true,
			Code:       "E_PARSE_ERROR",
			Detail:     "structural Groovy scan found unbalanced braces after literal masking; extraction may be incomplete",
		}
	}
	return entities, status
}

// groovyCommandCallPattern matches a Groovy command expression at statement
// position: a lowercase call name followed by an unparenthesized first
// argument (`visitType p.type`, `print ' '`, `sleep 100`, `run this`). The
// argument must be unambiguously a value — a string/list/number literal,
// this/it, or a dotted expression — because `ident ident` is also the shape of
// a typed variable declaration (`logger log`), which must not register a call.
var groovyCommandCallPattern = regexp.MustCompile(
	`(?m)^[ \t]*([a-z_$][A-Za-z0-9_$]*)[ \t]+(?:[A-Za-z_$][A-Za-z0-9_$]*\??\.[A-Za-z_$(]|['"\[]|[0-9]|this\b|it\b)`)

// groovyCommandCallKeywords are statement/declaration openers that share the
// command-expression shape but are not calls.
var groovyCommandCallKeywords = map[string]bool{
	"assert": true, "boolean": true, "break": true, "byte": true, "case": true,
	"catch": true, "char": true, "continue": true, "def": true, "default": true,
	"do": true, "double": true, "else": true, "final": true, "finally": true,
	"float": true, "for": true, "goto": true, "if": true, "import": true,
	"in": true, "instanceof": true, "int": true, "it": true, "long": true,
	"native": true, "new": true, "package": true, "print": true, "println": true,
	"private": true, "protected": true, "public": true, "return": true,
	"short": true, "static": true, "strictfp": true, "super": true,
	"synchronized": true, "this": true, "throw": true, "throws": true,
	"transient": true, "try": true, "var": true, "void": true, "volatile": true,
	"while": true, "yield": true,
}

// groovyCommandCallIdentifiers finds paren-less Groovy command-expression call
// names in an already string-masked block; the generic `name(` scanner cannot
// see them. Precision beats recall here: a bare-identifier argument is skipped
// because it is indistinguishable from a variable declaration.
func groovyCommandCallIdentifiers(maskedBlock string) map[string]struct{} {
	identifiers := map[string]struct{}{}
	for _, match := range groovyCommandCallPattern.FindAllStringSubmatch(maskedBlock, -1) {
		name := match[1]
		if groovyCommandCallKeywords[name] {
			continue
		}
		identifiers[name] = struct{}{}
	}
	return identifiers
}

// ---------------------------------------------------------------------------
// Pass 1: literal/comment masking
// ---------------------------------------------------------------------------

func maskGroovyLiteralsAndComments(content string) string {
	b := []byte(content)
	out := make([]byte, len(b))
	copy(out, b)
	for i := 0; i < len(b); {
		i = groovyMaskToken(b, out, i)
	}
	return string(out)
}

// groovyMaskToken masks the comment or string literal starting at i and
// returns the index just past it; when i starts no literal it returns i+1.
// Masking blanks literal content but keeps delimiter characters and newlines,
// so offsets and line numbers in the masked text match the original.
func groovyMaskToken(b, out []byte, i int) int {
	switch b[i] {
	case '/':
		if i+1 < len(b) && b[i+1] == '/' {
			j := i + 2
			for j < len(b) && b[j] != '\n' {
				j++
			}
			groovyBlank(out, i, j)
			return j
		}
		if i+1 < len(b) && b[i+1] == '*' {
			j := i + 2
			for j+1 < len(b) && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			if j+1 < len(b) {
				j += 2
			} else {
				j = len(b)
			}
			groovyBlank(out, i, j)
			return j
		}
		if groovyExpressionPosition(out, i) {
			if end, ok := groovySlashyEnd(b, i); ok {
				groovyBlank(out, i+1, end-1)
				return end
			}
		}
		return i + 1
	case '\'':
		if groovyHasPrefix(b, i, "'''") {
			j := i + 3
			for j < len(b) {
				if b[j] == '\\' {
					j += 2
					continue
				}
				if groovyHasPrefix(b, j, "'''") {
					groovyBlank(out, i+3, j)
					return j + 3
				}
				j++
			}
			groovyBlank(out, i+3, len(b))
			return len(b)
		}
		j := i + 1
		for j < len(b) && b[j] != '\n' {
			if b[j] == '\\' {
				j += 2
				continue
			}
			if b[j] == '\'' {
				groovyBlank(out, i+1, j)
				return j + 1
			}
			j++
		}
		groovyBlank(out, i+1, j) // unterminated: blank to end of line
		return j
	case '"':
		if groovyHasPrefix(b, i, `"""`) {
			return groovyMaskGString(b, out, i+3, true)
		}
		return groovyMaskGString(b, out, i+1, false)
	case '$':
		if groovyHasPrefix(b, i, "$/") && groovyExpressionPosition(out, i) {
			j := i + 2
			for j < len(b) {
				if b[j] == '$' && j+1 < len(b) && (b[j+1] == '$' || b[j+1] == '/') {
					j += 2
					continue
				}
				if b[j] == '/' && j+1 < len(b) && b[j+1] == '$' {
					groovyBlank(out, i+2, j)
					return j + 2
				}
				j++
			}
			groovyBlank(out, i+2, len(b))
			return len(b)
		}
	}
	return i + 1
}

// groovyMaskGString masks a double-quoted GString body starting just past the
// opening delimiter, handling ${...} interpolation (whose expression may nest
// strings and braces) and returning the index just past the closing delimiter.
func groovyMaskGString(b, out []byte, start int, multiline bool) int {
	j := start
	for j < len(b) {
		c := b[j]
		if c == '\\' {
			groovyBlank(out, j, minInt(j+2, len(b)))
			j += 2
			continue
		}
		if !multiline && c == '\n' {
			return j // unterminated single-line GString
		}
		if c == '"' {
			if !multiline {
				return j + 1
			}
			if groovyHasPrefix(b, j, `"""`) {
				return j + 3
			}
			out[j] = ' ' // lone quote inside a triple-quoted string
			j++
			continue
		}
		if c == '$' && j+1 < len(b) && b[j+1] == '{' {
			j = groovyMaskInterpolation(b, out, j)
			continue
		}
		if c != '\n' && c != '\r' {
			out[j] = ' '
		}
		j++
	}
	return len(b)
}

// groovyMaskInterpolation masks a ${...} interpolation (including its braces,
// so string-internal braces never affect structural depth), skipping nested
// string literals via groovyMaskToken to find the matching close brace.
func groovyMaskInterpolation(b, out []byte, i int) int {
	out[i], out[i+1] = ' ', ' '
	depth := 1
	j := i + 2
	for j < len(b) && depth > 0 {
		switch b[j] {
		case '{':
			out[j] = ' '
			depth++
			j++
		case '}':
			depth--
			out[j] = ' '
			j++
		case '\'', '"', '/', '$':
			next := groovyMaskToken(b, out, j)
			groovyBlank(out, j, next)
			j = next
		default:
			if b[j] != '\n' && b[j] != '\r' {
				out[j] = ' '
			}
			j++
		}
	}
	return j
}

// groovyExpressionPosition reports whether a '/' (or '$/') at i can start a
// slashy string: only when the previous significant character on the same line
// is one that ends no value expression (an operator, opening bracket, comma,
// etc.) or the previous word is a keyword like return. Anything else is
// treated as division. This is deliberately conservative: misreading division
// as a string start would swallow real code, while missing a rare slashy
// string only leaves regex text unmasked.
func groovyExpressionPosition(out []byte, i int) bool {
	for j := i - 1; j >= 0; j-- {
		c := out[j]
		if c == ' ' || c == '\t' || c == '\r' {
			continue
		}
		if c == '\n' {
			return false
		}
		switch c {
		case '=', '(', '[', '{', ',', ':', ';', '!', '?', '&', '|', '^', '<', '>', '+', '-', '*', '%', '~':
			return true
		}
		if groovyWordChar(c) {
			k := j
			for k >= 0 && groovyWordChar(out[k]) {
				k--
			}
			switch string(out[k+1 : j+1]) {
			case "return", "in", "case", "assert", "matches":
				return true
			}
			return false
		}
		return false
	}
	return false
}

// groovySlashyEnd finds the end of a slashy string opened at i, honoring
// backslash escapes. The search is capped so a misdetected opener cannot
// swallow the rest of a large file.
func groovySlashyEnd(b []byte, i int) (int, bool) {
	limit := minInt(len(b), i+4096)
	for j := i + 1; j < limit; j++ {
		if b[j] == '\\' {
			j++
			continue
		}
		if b[j] == '/' {
			return j + 1, true
		}
	}
	return 0, false
}

func groovyBlank(out []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(out) {
		end = len(out)
	}
	for i := start; i < end; i++ {
		if out[i] != '\n' && out[i] != '\r' {
			out[i] = ' '
		}
	}
}

func groovyHasPrefix(b []byte, i int, prefix string) bool {
	if i+len(prefix) > len(b) {
		return false
	}
	return string(b[i:i+len(prefix)]) == prefix
}

func groovyWordChar(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---------------------------------------------------------------------------
// Pass 2: structural scan
// ---------------------------------------------------------------------------

// groovyFrame is one open brace on the scan stack. Type-declaration bodies
// carry their name so members inside qualify under the container; every other
// brace (method body, closure, control block, anonymous class, DSL block) is
// an opaque frame whose contents emit no symbols.
type groovyFrame struct {
	isType   bool
	typeName string
	typeKind string // class | interface | trait | enum | record
	entity   int    // entity index whose EndLine closes with this frame, or -1
}

type groovyScanner struct {
	src         string
	masked      string
	lines       []string
	entities    []Entity
	stack       []groovyFrame
	braceErrors int
	i           int
	line        int
}

func (s *groovyScanner) scan() {
	s.i = 0
	s.line = 1
	n := len(s.masked)
	for s.i < n {
		switch s.masked[s.i] {
		case '\n':
			s.line++
			s.i++
		case ' ', '\t', '\r', ';':
			s.i++
		case '}':
			s.closeFrame()
			s.i++
		case '{':
			s.stack = append(s.stack, groovyFrame{entity: -1})
			s.i++
		case '(', ')', '[', ']':
			s.i++ // stray bracket at statement level; skip
		default:
			s.statement()
		}
	}
	for len(s.stack) > 0 {
		s.closeFrame()
		s.braceErrors++
	}
}

func (s *groovyScanner) closeFrame() {
	if len(s.stack) == 0 {
		s.braceErrors++
		return
	}
	frame := s.stack[len(s.stack)-1]
	s.stack = s.stack[:len(s.stack)-1]
	if frame.entity >= 0 {
		s.entities[frame.entity].EndLine = s.line
	}
}

// statement consumes one statement starting at s.i: it collects the header up
// to the terminator ('{' body open, ';', '}', or a non-continuation newline),
// classifies it, emits entities, and pushes the right frame for '{'.
func (s *groovyScanner) statement() {
	n := len(s.masked)
	start := s.i
	startLine := s.line
	line := s.line
	parens, brackets := 0, 0
	var term byte
	j := s.i
scan:
	for j < n {
		c := s.masked[j]
		switch c {
		case '\n':
			if parens == 0 && brackets == 0 &&
				!groovyHeaderContinues(s.masked[start:j]) &&
				!s.nextLineContinuesHeader(j) {
				term = 0
				break scan
			}
			line++
		case '(':
			parens++
		case ')':
			if parens > 0 {
				parens--
			}
		case '[':
			brackets++
		case ']':
			if brackets > 0 {
				brackets--
			}
		case '{', '}', ';':
			if parens == 0 && brackets == 0 {
				term = c
				break scan
			}
		}
		j++
	}
	header := s.masked[start:j]
	orig := s.src[start:j]
	decl := s.classify(header, orig, term)
	s.line = line
	switch term {
	case '{':
		s.i = j + 1
		frame := groovyFrame{entity: -1}
		switch decl.kind {
		case "type":
			idx := s.emitType(decl, startLine, orig)
			frame = groovyFrame{isType: true, typeName: decl.name, typeKind: decl.typeKind, entity: idx}
		case "method":
			frame = groovyFrame{entity: s.emitMethod(decl, startLine, orig)}
		case "field":
			// Field whose initializer opens a brace (closure or map literal):
			// emit the field spanning the initializer; the closure itself is
			// not a symbol.
			frame = groovyFrame{entity: s.emitFields(decl, startLine, line, orig)}
		}
		s.stack = append(s.stack, frame)
	case ';':
		s.i = j + 1
		s.emitHeaderOnly(decl, startLine, line, orig)
	case '}':
		s.i = j // main loop closes the frame
		s.emitHeaderOnly(decl, startLine, line, orig)
	default: // newline or EOF
		s.i = j
		s.emitHeaderOnly(decl, startLine, line, orig)
	}
}

// nextLineContinuesHeader looks past the newline at j for a body brace on its
// own line (K&R style `void foo()\n{`) or a leading `throws` clause, both of
// which keep the current statement open.
func (s *groovyScanner) nextLineContinuesHeader(j int) bool {
	n := len(s.masked)
	k := j
	for k < n {
		switch s.masked[k] {
		case ' ', '\t', '\r', '\n':
			k++
			continue
		}
		break
	}
	if k >= n {
		return false
	}
	if s.masked[k] == '{' {
		return true
	}
	end := k
	for end < n && groovyWordChar(s.masked[end]) {
		end++
	}
	switch s.masked[k:end] {
	case "throws", "extends", "implements":
		return true
	}
	return false
}

// groovyHeaderContinues reports whether a statement header that reached a
// newline is syntactically unfinished: it ends with an operator/comma/dot, an
// infix keyword, or is a type declaration still awaiting its body brace.
func groovyHeaderContinues(header string) bool {
	trimmed := strings.TrimRight(header, " \t\r\n")
	if trimmed == "" {
		return true
	}
	switch trimmed[len(trimmed)-1] {
	case ',', '=', '.', '+', '-', '*', '&', '|', '?', ':', '<', '>', '~', '^', '@':
		return true
	}
	for _, word := range [...]string{"extends", "implements", "throws", "new", "instanceof", "in", "else"} {
		if strings.HasSuffix(trimmed, word) &&
			(len(trimmed) == len(word) || !groovyWordChar(trimmed[len(trimmed)-len(word)-1])) {
			return true
		}
	}
	return groovyTypeDeclKeyword(trimmed) != ""
}

// groovyTypeDeclKeyword returns the type-declaration keyword that starts the
// header (after annotations and modifiers), or "".
func groovyTypeDeclKeyword(header string) string {
	r := &groovyCursor{s: header}
	if !r.skipAnnotationsAndModifiers() {
		return ""
	}
	if r.peek() == '@' {
		save := r.i
		r.i++
		if r.word() == "interface" {
			return "@interface"
		}
		r.i = save
		return ""
	}
	save := r.i
	switch w := r.word(); w {
	case "class", "interface", "trait", "enum":
		return w
	case "record":
		// `record` is not reserved; only `record Name(...)` is a declaration.
		r.ws()
		if r.word() != "" {
			r.ws()
			if r.peek() == '(' || r.peek() == '<' {
				return w
			}
		}
	}
	r.i = save
	return ""
}

// ---------------------------------------------------------------------------
// Statement classification
// ---------------------------------------------------------------------------

type groovyDecl struct {
	kind       string // "" | "type" | "method" | "field"
	typeKind   string // entity kind for type declarations
	name       string
	fieldNames []string
	fieldType  string
	topLevel   bool
	container  string
}

// groovyStatementKeywords are words that can never start a declaration.
var groovyStatementKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "switch": true,
	"do": true, "try": true, "catch": true, "finally": true, "return": true,
	"throw": true, "new": true, "assert": true, "break": true, "continue": true,
	"case": true, "import": true, "package": true, "this": true, "super": true,
	"instanceof": true, "goto": true, "yield": true, "print": true, "println": true,
}

var groovyModifierWords = map[string]bool{
	"public": true, "protected": true, "private": true, "static": true,
	"final": true, "abstract": true, "strictfp": true, "native": true,
	"transient": true, "volatile": true, "default": true, "sealed": true,
}

func (s *groovyScanner) classify(header, orig string, term byte) groovyDecl {
	inType, container, containerKind := false, "", ""
	topLevel := len(s.stack) == 0
	if !topLevel {
		top := s.stack[len(s.stack)-1]
		if !top.isType {
			return groovyDecl{} // method body / closure / opaque block
		}
		inType, container, containerKind = true, top.typeName, top.typeKind
	}

	r := &groovyCursor{s: header, orig: orig}
	sawDef, sawMods := false, false
	for {
		r.ws()
		if r.eof() {
			return groovyDecl{}
		}
		if r.peek() == '@' {
			save := r.i
			r.i++
			w := r.word()
			if w == "" || w == "interface" {
				r.i = save
				break
			}
			for r.peek() == '.' {
				r.i++
				if r.word() == "" {
					return groovyDecl{}
				}
			}
			r.ws()
			if r.peek() == '(' && !r.skipBalanced('(', ')') {
				return groovyDecl{}
			}
			continue
		}
		save := r.i
		w := r.word()
		if w == "" {
			break
		}
		if w == "def" || w == "var" {
			sawDef = true
			continue
		}
		if w == "synchronized" {
			r.ws()
			if r.peek() == '(' {
				return groovyDecl{} // synchronized (lock) { ... } block
			}
			sawMods = true
			continue
		}
		if groovyModifierWords[w] {
			sawMods = true
			continue
		}
		r.i = save
		break
	}
	r.ws()

	// Type declarations (including annotation types).
	if kw := groovyTypeKeywordAt(r); kw != "" {
		r.ws()
		nameStart := r.i
		name := r.word()
		if name != "" && !groovyStatementKeywords[name] {
			if kw != "record" || func() bool { r.ws(); return r.peek() == '(' || r.peek() == '<' }() {
				return groovyDecl{
					kind:      "type",
					typeKind:  groovyTypeKindFor(kw),
					name:      orig[nameStart : nameStart+len(name)],
					topLevel:  topLevel,
					container: container,
				}
			}
		}
		return groovyDecl{}
	}

	// Quoted method names: def "spec feature name"() { ... }
	if (sawDef || sawMods) && (r.peek() == '"' || r.peek() == '\'') {
		if name, ok := r.quotedName(); ok {
			r.ws()
			if r.peek() == '(' {
				return groovyDecl{kind: "method", name: name, topLevel: topLevel, container: container}
			}
		}
		return groovyDecl{}
	}

	// Generic method type parameters: static <T> T tryAll(...)
	if r.peek() == '<' {
		if !r.skipBalanced('<', '>') {
			return groovyDecl{}
		}
		r.ws()
	}

	first := r.word()
	if first == "" || groovyStatementKeywords[first] {
		return groovyDecl{}
	}
	// Extend `first` into a full type token: dotted path, generics, arrays.
	typeTok := first
	for r.peek() == '.' {
		r.i++
		part := r.word()
		if part == "" {
			return groovyDecl{}
		}
		typeTok += "." + part
	}
	if r.peek() == '<' {
		gs := r.i
		if !r.skipBalanced('<', '>') {
			return groovyDecl{}
		}
		typeTok += header[gs:r.i]
	}
	for {
		save := r.i
		r.ws()
		if r.peek() == '[' {
			r.i++
			r.ws()
			if r.peek() == ']' {
				r.i++
				typeTok += "[]"
				continue
			}
		}
		r.i = save
		break
	}
	r.ws()

	switch {
	case r.peek() == '(':
		// `name(...)`: a constructor, a modifier-only method (`private foo()`),
		// a def method (`def foo()`), or — anywhere else — just a call.
		if inType && typeTok == container && !sawDef {
			// Constructors are not emitted as symbols (matching the Java
			// path); the body brace still becomes an opaque frame.
			return groovyDecl{}
		}
		if sawDef || sawMods {
			return groovyDecl{kind: "method", name: typeTok, topLevel: topLevel, container: container}
		}
		return s.enumConstants(header, orig, containerKind, container)
	case r.peek() == '"' || r.peek() == '\'':
		// Typed quoted method name: void "feature name"() { ... }
		if name, ok := r.quotedName(); ok {
			r.ws()
			if r.peek() == '(' {
				return groovyDecl{kind: "method", name: name, topLevel: topLevel, container: container}
			}
		}
		return groovyDecl{}
	case r.peek() == '=' && !r.at("=="):
		// `def x = ...` — typeTok is the variable name.
		if sawDef {
			return groovyDecl{kind: "field", fieldNames: []string{typeTok}, topLevel: topLevel, container: container}
		}
		return groovyDecl{}
	case r.peek() == ',':
		if sawDef {
			names := r.declaratorNames(typeTok)
			return groovyDecl{kind: "field", fieldNames: names, topLevel: topLevel, container: container}
		}
		return s.enumConstants(header, orig, containerKind, container)
	case r.eof() || r.peek() == ';':
		if sawDef {
			return groovyDecl{kind: "field", fieldNames: []string{typeTok}, topLevel: topLevel, container: container}
		}
		return s.enumConstants(header, orig, containerKind, container)
	}

	name := r.word()
	if name == "" || groovyStatementKeywords[name] {
		return groovyDecl{}
	}
	r.ws()
	switch {
	case r.peek() == '(':
		// `Type name(...)`: a method. At top level (scripts) require a body
		// and a plausible return type so command expressions like
		// `println foo(1)` or Gradle's `task foo(...)` do not match.
		if inType || sawDef || sawMods ||
			(topLevel && term == '{' && groovyLikelyType(typeTok)) {
			return groovyDecl{kind: "method", name: name, topLevel: topLevel, container: container}
		}
		return groovyDecl{}
	case r.peek() == '=' && !r.at("=="), r.peek() == ',', r.eof(), r.peek() == ';':
		// `Type name [= init][, more]`: a field at member level; at top level
		// only with an initializer and a plausible type (scripts are full of
		// command expressions shaped like `word word`).
		if !inType && !(topLevel && (sawDef || sawMods || (groovyLikelyType(typeTok) && r.peek() == '='))) {
			return groovyDecl{}
		}
		names := r.declaratorNames(name)
		return groovyDecl{kind: "field", fieldNames: names, fieldType: typeTok, topLevel: topLevel, container: container}
	}
	return groovyDecl{}
}

// groovyTypeKeywordAt consumes and returns a type-declaration keyword at the
// cursor, or returns "" leaving the cursor unchanged.
func groovyTypeKeywordAt(r *groovyCursor) string {
	if r.peek() == '@' {
		save := r.i
		r.i++
		if r.word() == "interface" {
			return "@interface"
		}
		r.i = save
		return ""
	}
	save := r.i
	switch w := r.word(); w {
	case "class", "interface", "trait", "enum", "record":
		return w
	default:
		_ = w
	}
	r.i = save
	return ""
}

func groovyTypeKindFor(keyword string) string {
	switch keyword {
	case "class":
		return "class"
	case "interface", "@interface":
		return "interface"
	case "trait":
		return "trait"
	case "enum":
		return "enum"
	case "record":
		return "record"
	}
	return "class"
}

// groovyLikelyType reports whether a token plausibly names a return/field type
// in top-level script position: void/def, a capitalized class name, a generic,
// or an array. Lowercase bare words (`println`, `task`) are commands.
func groovyLikelyType(tok string) bool {
	switch tok {
	case "void", "def", "boolean", "byte", "char", "short", "int", "long", "float", "double":
		return true
	}
	if strings.ContainsAny(tok, "<[") {
		return true
	}
	last := tok
	if idx := strings.LastIndex(tok, "."); idx >= 0 {
		last = tok[idx+1:]
	}
	return last != "" && last[0] >= 'A' && last[0] <= 'Z'
}

// enumConstants matches an enum-body constant list (`FOO, BAR` /
// `FOO(1), BAR(2)` / `FOO { ... }`) and emits each constant as a field.
func (s *groovyScanner) enumConstants(header, orig, containerKind, container string) groovyDecl {
	if containerKind != "enum" {
		return groovyDecl{}
	}
	r := &groovyCursor{s: header, orig: orig}
	var names []string
	for {
		r.ws()
		name := r.word()
		if name == "" || groovyStatementKeywords[name] {
			return groovyDecl{}
		}
		names = append(names, name)
		r.ws()
		if r.peek() == '(' && !r.skipBalanced('(', ')') {
			return groovyDecl{}
		}
		r.ws()
		switch {
		case r.peek() == ',':
			r.i++
			r.ws()
			if r.eof() {
				// trailing comma before a line break handled by continuation
				return groovyDecl{kind: "field", fieldNames: names, container: container}
			}
		case r.eof() || r.peek() == ';':
			return groovyDecl{kind: "field", fieldNames: names, container: container}
		default:
			return groovyDecl{}
		}
	}
}

// ---------------------------------------------------------------------------
// Entity emission
// ---------------------------------------------------------------------------

func (s *groovyScanner) emitType(decl groovyDecl, startLine int, orig string) int {
	s.entities = append(s.entities, Entity{
		Kind:      decl.typeKind,
		Name:      decl.name,
		Signature: normalize(orig),
		StartLine: startLine,
		EndLine:   startLine,
	})
	return len(s.entities) - 1
}

func (s *groovyScanner) emitMethod(decl groovyDecl, startLine int, orig string) int {
	kind := "function"
	name := decl.name
	if decl.container != "" {
		kind = "method"
		name = qualify(decl.container, name)
	}
	s.entities = append(s.entities, Entity{
		Kind:      kind,
		Name:      name,
		Signature: normalize(orig),
		StartLine: startLine,
		EndLine:   startLine,
	})
	return len(s.entities) - 1
}

// emitFields emits one field entity per declared name, mirroring the
// tree-sitter fieldEntities conventions (kind, signature, hashes). It returns
// the index of the last emitted entity so a brace-opening initializer can
// extend its EndLine, or -1.
func (s *groovyScanner) emitFields(decl groovyDecl, startLine, endLine int, orig string) int {
	kind := "field"
	if decl.container == "" {
		kind = "variable"
	}
	last := -1
	for _, name := range decl.fieldNames {
		signature := name
		if decl.fieldType != "" {
			signature = name + " " + decl.fieldType
		}
		s.entities = append(s.entities, Entity{
			Kind:        kind,
			Name:        qualify(decl.container, name),
			Signature:   signature,
			StartLine:   startLine,
			EndLine:     endLine,
			BodyHash:    hash(decl.fieldType),
			Fingerprint: hash(normalize(signature)),
		})
		last = len(s.entities) - 1
	}
	return last
}

// emitHeaderOnly emits declarations that carry no body brace: abstract and
// interface methods, and fields whose statement ended at a newline/semicolon.
func (s *groovyScanner) emitHeaderOnly(decl groovyDecl, startLine, endLine int, orig string) {
	switch decl.kind {
	case "method":
		idx := s.emitMethod(decl, startLine, orig)
		s.entities[idx].EndLine = endLine
	case "field":
		s.emitFields(decl, startLine, endLine, orig)
	}
}

// ---------------------------------------------------------------------------
// Header cursor
// ---------------------------------------------------------------------------

// groovyCursor is a byte cursor over one masked statement header. orig is the
// unmasked text at the same offsets, used to recover quoted method names whose
// content the mask pass blanked.
type groovyCursor struct {
	s    string
	orig string
	i    int
}

func (r *groovyCursor) eof() bool { return r.i >= len(r.s) }

func (r *groovyCursor) peek() byte {
	if r.eof() {
		return 0
	}
	return r.s[r.i]
}

func (r *groovyCursor) at(prefix string) bool {
	return r.i+len(prefix) <= len(r.s) && r.s[r.i:r.i+len(prefix)] == prefix
}

func (r *groovyCursor) ws() {
	for !r.eof() {
		switch r.s[r.i] {
		case ' ', '\t', '\r', '\n':
			r.i++
		default:
			return
		}
	}
}

// word consumes and returns an identifier-shaped token, or "" (identifiers
// cannot start with a digit).
func (r *groovyCursor) word() string {
	if r.eof() {
		return ""
	}
	c := r.s[r.i]
	if !groovyWordChar(c) || (c >= '0' && c <= '9') {
		return ""
	}
	start := r.i
	for !r.eof() && groovyWordChar(r.s[r.i]) {
		r.i++
	}
	return r.s[start:r.i]
}

// skipBalanced consumes a balanced open...close region starting at the cursor.
func (r *groovyCursor) skipBalanced(open, close byte) bool {
	if r.peek() != open {
		return false
	}
	depth := 0
	for !r.eof() {
		switch r.s[r.i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				r.i++
				return true
			}
		}
		r.i++
	}
	return false
}

// skipAnnotationsAndModifiers advances past leading annotations (with optional
// argument lists) and modifier keywords, including def/var. It returns false
// when the header is malformed (e.g. an unbalanced annotation argument list).
func (r *groovyCursor) skipAnnotationsAndModifiers() bool {
	for {
		r.ws()
		if r.eof() {
			return true
		}
		if r.peek() == '@' {
			save := r.i
			r.i++
			w := r.word()
			if w == "" || w == "interface" {
				r.i = save
				return true
			}
			for r.peek() == '.' {
				r.i++
				if r.word() == "" {
					return false
				}
			}
			r.ws()
			if r.peek() == '(' && !r.skipBalanced('(', ')') {
				return false
			}
			continue
		}
		save := r.i
		w := r.word()
		if w == "def" || w == "var" || w == "synchronized" || groovyModifierWords[w] {
			continue
		}
		r.i = save
		return true
	}
}

// quotedName consumes a quoted method name, returning its unmasked text.
func (r *groovyCursor) quotedName() (string, bool) {
	quote := r.peek()
	if quote != '"' && quote != '\'' {
		return "", false
	}
	for j := r.i + 1; j < len(r.s); j++ {
		if r.s[j] == quote {
			name := strings.TrimSpace(r.orig[r.i+1 : j])
			r.i = j + 1
			if name == "" {
				return "", false
			}
			return name, true
		}
		if r.s[j] == '\n' {
			break
		}
	}
	return "", false
}

// declaratorNames parses a comma-separated declarator list starting with an
// already-consumed first name, skipping `= initializer` values at bracket
// depth zero. A token that does not look like a declarator ends the list.
func (r *groovyCursor) declaratorNames(first string) []string {
	names := []string{first}
	for {
		r.ws()
		switch {
		case r.eof(), r.peek() == ';':
			return names
		case r.peek() == '=' && !r.at("=="):
			r.i++
			depth := 0
			for !r.eof() {
				c := r.s[r.i]
				if c == '(' || c == '[' || c == '{' {
					depth++
				} else if c == ')' || c == ']' || c == '}' {
					depth--
				} else if c == ',' && depth <= 0 {
					break // next iteration handles the comma
				}
				r.i++
			}
		case r.peek() == ',':
			r.i++
			r.ws()
			next := r.word()
			if next == "" || groovyStatementKeywords[next] {
				return names
			}
			save := r.i
			r.ws()
			if !r.eof() && r.peek() != '=' && r.peek() != ',' && r.peek() != ';' {
				r.i = save
				return names
			}
			r.i = save
			names = append(names, next)
		default:
			return names
		}
	}
}
