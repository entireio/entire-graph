package sem

// F# juxtaposition call extraction. F# applies a function by writing its
// arguments beside it — `add ledger amount`, with no dot and no parentheses —
// and that is the language's ordinary call syntax. None of the existing
// scanners see it: fsharpDottedCallRe and fsharpDottedApplyRe both need a `.`,
// and the generic scanner needs `name(`. So a module of functions calling each
// other produced no CALLS edge at all, while `capabilities --json` advertises
// CALLS for F#.
//
// Reading juxtaposition is harder than reading a pipe, because the pipe makes
// the applied position unambiguous and whitespace does not: in `List.map helper
// add` every one of the three names is written the same way and only one is
// applied. The approach here is the one OCaml already uses in this package
// (ocamlBareCallSites), which is the closest relative F# has — same ML binding
// forms, same application-by-juxtaposition. Four things together make it
// precise enough:
//
//   - only names that are KNOWN CALLABLES in the file are considered, so an
//     unrecognized name is never guessed into a call;
//   - a binding head (between `let`/`and`/`fun`/`member`/... and the `=` or
//     `->` that starts the body) applies nothing — every name in it is a binder
//     or a parameter;
//   - a name the block binds for itself — a parameter, or a local value
//     binding — is that local binding, not the file-level function it shadows;
//   - the name must stand in head position AND be followed by an argument, or
//     else be applied through a pipe operator, which is what separates
//     `add x 1` from `List.map helper add` and from a bare value reference.
//
// Head position and "followed by an argument" are both judged under F#'s
// offside rule rather than by skipping whitespace: a token on a later line
// belongs to this expression only when it is indented past the line the name
// sits on. Skipping newlines outright fused adjacent expressions, so
// `let shown = double` followed by `add shown 1` read `double` as applying
// `add` and read `add` as `double`'s trailing argument — one invented edge and
// one lost one.

import (
	"regexp"
	"strings"
)

var (
	fsharpIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_']*`)
	// `open Ledger`, `open type System.Math`, `open Paket.UpdateProcess`.
	fsharpOpenRe = regexp.MustCompile(`(?m)^\s*open\s+(?:type\s+)?([A-Za-z_][A-Za-z0-9_'.]*)`)
)

// fsharpKeyword reports F# reserved words. They are language syntax, never
// call targets, and several of them also open a binding head.
func fsharpKeyword(word string) bool {
	switch word {
	case "abstract", "and", "as", "assert", "base", "begin", "class", "default",
		"delegate", "do", "done", "downcast", "downto", "elif", "else", "end",
		"exception", "extern", "false", "finally", "fixed", "for", "fun",
		"function", "global", "if", "in", "inherit", "inline", "interface",
		"internal", "lazy", "let", "match", "member", "module", "mutable",
		"namespace", "new", "not", "null", "of", "open", "or", "override",
		"private", "public", "rec", "return", "select", "static", "struct",
		"then", "to", "true", "try", "type", "upcast", "use", "val", "void",
		"when", "while", "with", "yield":
		return true
	}
	return false
}

// fsharpBindingHeadKeyword reports the keywords that open a binding head, where
// every following name up to the `=` or `->` is a binder or a parameter rather
// than something being applied.
func fsharpBindingHeadKeyword(word string) bool {
	switch word {
	case "let", "and", "fun", "function", "member", "val", "use", "override",
		"abstract", "new", "extern", "inherit":
		return true
	}
	return false
}

func isFSharpIdentByte(b byte) bool {
	return b == '_' || b == '\'' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// fsharpLineStart returns the index of the first byte of the line containing
// pos.
func fsharpLineStart(s string, pos int) int {
	start := pos
	for start > 0 && s[start-1] != '\n' {
		start--
	}
	return start
}

// fsharpColumn returns the 0-based column of pos on its own line.
func fsharpColumn(s string, pos int) int {
	return pos - fsharpLineStart(s, pos)
}

// fsharpLineIndent returns the column of the first non-blank byte on the line
// containing pos, which is the offside column that line's expression is
// measured from.
func fsharpLineIndent(s string, pos int) int {
	start := fsharpLineStart(s, pos)
	indent := 0
	for start+indent < len(s) && (s[start+indent] == ' ' || s[start+indent] == '\t') {
		indent++
	}
	return indent
}

func fsharpIsSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// fsharpArgumentFollows reports whether what comes after a name can start an
// argument: an identifier that is not a keyword, a literal, or an opening
// bracket. `f <| x` also applies f, with the argument behind the operator. A
// following `=`, `)`, `,`, `->`, keyword or other operator means the name was a
// definition, a record field, or a plain value reference, not an application.
func fsharpArgumentFollows(s string, end int) bool {
	i := end
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i < len(s) && (s[i] == '\n' || s[i] == '\r') {
		// Offside rule: a token on a later line continues this application only
		// when it is indented past the line the name sits on. At the same or a
		// shallower column it opens a separate expression, and the name it
		// follows applied nothing.
		indent := fsharpLineIndent(s, end)
		for i < len(s) && fsharpIsSpace(s[i]) {
			i++
		}
		if i >= len(s) || fsharpColumn(s, i) <= indent {
			return false
		}
	}
	if i >= len(s) {
		return false
	}
	if i+1 < len(s) && s[i] == '<' && s[i+1] == '|' {
		return true
	}
	switch c := s[i]; {
	case c == '(' || c == '[' || c == '{' || c == '"':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return !fsharpKeyword(fsharpIdentRe.FindString(s[i:]))
	}
	return false
}

// fsharpHeadPosition reports whether the name starting at `start` can be the
// head of an application, judged by the token before it. In `List.map helper
// add` the middle name is followed by another name and would look applied; what
// gives it away is what precedes it — an identifier, a literal or a closing
// bracket means the name is itself a trailing argument. Operators, opening
// brackets, separators, keywords and start-of-block all leave it in head
// position.
func fsharpHeadPosition(s string, start int) bool {
	i := start - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t') {
		i--
	}
	if i >= 0 && (s[i] == '\n' || s[i] == '\r') {
		// The mirror of the rule in fsharpArgumentFollows. This name continues
		// the previous line's expression only when it is indented past that
		// line; at the same or a shallower column it starts a new expression
		// and so stands in head position whatever that line ended with.
		column := fsharpColumn(s, start)
		for i >= 0 && fsharpIsSpace(s[i]) {
			i--
		}
		if i < 0 || column <= fsharpLineIndent(s, i) {
			return true
		}
	}
	if i < 0 {
		return true
	}
	switch c := s[i]; {
	case c == ')' || c == ']' || c == '}':
		return false // trailing argument after a bracketed argument
	case c == ':':
		return false // a type annotation, not an application
	case c >= '0' && c <= '9':
		return false // trailing argument after a numeric literal
	case isFSharpIdentByte(c):
		word := i
		for word > 0 && isFSharpIdentByte(s[word-1]) {
			word--
		}
		return fsharpKeyword(s[word : i+1])
	}
	return true
}

// fsharpPipelineApplied reports whether the name starting at `start` is applied
// through a forward pipe (`x |> fn`, `(a, b) ||> fn`), where the function
// follows the operator and so has no argument to its right. The backward pipe
// is the mirror image — in `fn <| x` the function is on the LEFT — so a
// preceding `<|` marks an argument and is deliberately not accepted here.
func fsharpPipelineApplied(s string, start int) bool {
	i := start - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
	}
	if i < 1 || s[i] != '>' {
		return false
	}
	return s[i-1] == '|'
}

// fsharpDotFollows reports whether a `.` comes next, which marks the name as a
// qualifier rather than the name being bound — the `this` of `member this.Add`.
func fsharpDotFollows(s string, end int) bool {
	i := end
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i < len(s) && s[i] == '.'
}

// fsharpLocallyBoundNames returns the names a block binds for itself: every
// parameter of every binding head in it, and the name of every value binding
// (`let shown = ...`). Inside the block such a name denotes the local binding,
// so applying it is not a call to the file-level function it shadows —
// `let apply add ledger = add ledger 1` applies its own parameter, and
// resolving that to a module-level `add` would be an invented edge.
//
// The name of a binding that takes arguments (`let add x y = ...`,
// `let run () = ...`) is deliberately left out: it names the function being
// defined, and shadowing it would drop every recursive call. A name is only
// treated as a value binding when nothing at all stands between it and the `=`
// or `->` that ends the head.
//
// The set is collected over the whole block before it is consulted, so a `let`
// shadows for the block rather than from its own line onward. That is the
// conservative direction: it drops an edge rather than inventing one.
func fsharpLocallyBoundNames(stripped string) map[string]bool {
	out := map[string]bool{}
	inHead := false
	lambdaHead := false
	definiendum := ""
	definiendumEnd := -1
	var params []string
	closeHead := func(headEnd int) {
		for _, name := range params {
			out[name] = true
		}
		if definiendum != "" && definiendumEnd >= 0 && definiendumEnd <= headEnd &&
			strings.TrimSpace(stripped[definiendumEnd:headEnd]) == "" {
			out[definiendum] = true
		}
		inHead, lambdaHead, definiendum, definiendumEnd, params = false, false, "", -1, nil
	}
	prevEnd := 0
	for _, m := range fsharpIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		gapStart := prevEnd
		gap := stripped[gapStart:start]
		prevEnd = end
		if inHead {
			if term := fsharpHeadTerminator(gap); term >= 0 {
				closeHead(gapStart + term)
			}
		}
		word := stripped[start:end]
		if fsharpKeyword(word) {
			if fsharpBindingHeadKeyword(word) {
				if inHead {
					closeHead(start)
				}
				inHead = true
				// A lambda head binds parameters only; it names nothing.
				lambdaHead = word == "fun" || word == "function"
			}
			continue
		}
		if !inHead {
			continue
		}
		switch {
		case lambdaHead:
			params = append(params, word)
		case definiendumEnd < 0:
			if fsharpDotFollows(stripped, end) {
				continue // a qualifier, not the name being bound
			}
			definiendum, definiendumEnd = word, end
		case strings.Contains(gap, ":"):
			// a type in an annotation, not a binder
		default:
			params = append(params, word)
		}
	}
	if inHead {
		closeHead(len(stripped))
	}
	return out
}

// fsharpHeadTerminator returns the offset in gap of the `=` or `->` that ends a
// binding head, or -1 when the gap does not close one.
func fsharpHeadTerminator(gap string) int {
	equals := strings.Index(gap, "=")
	arrow := strings.Index(gap, "->")
	switch {
	case equals < 0:
		return arrow
	case arrow < 0:
		return equals
	case arrow < equals:
		return arrow
	default:
		return equals
	}
}

// fsharpJuxtapositionCallIdentifiers returns the names applied by juxtaposition
// in one block. callableNames is the set of callable bindings visible in the
// file; a name outside it is never reported, which is what keeps an ordinary
// value reference or an unknown identifier from becoming a call.
func fsharpJuxtapositionCallIdentifiers(block string, callableNames map[string]bool) map[string]struct{} {
	out := map[string]struct{}{}
	if len(callableNames) == 0 {
		return out
	}
	stripped := maskFSharpLiteralsAndComments(block)
	boundLocally := fsharpLocallyBoundNames(stripped)
	inHead := false
	prevEnd := 0
	for _, m := range fsharpIdentRe.FindAllStringIndex(stripped, -1) {
		start, end := m[0], m[1]
		gap := stripped[prevEnd:start]
		prevEnd = end
		if inHead && (strings.Contains(gap, "=") || strings.Contains(gap, "->")) {
			inHead = false
		}
		word := stripped[start:end]
		if fsharpKeyword(word) {
			if fsharpBindingHeadKeyword(word) {
				inHead = true
			}
			continue
		}
		if inHead {
			continue // a binder or a parameter name
		}
		if !callableNames[word] {
			continue
		}
		if boundLocally[word] {
			continue // a parameter or local binding shadowing the file-level name
		}
		if start > 0 {
			switch stripped[start-1] {
			case '.', '?', '`', '#', '\'', '%':
				continue // member access, optional argument, quoted or interpolated
			}
		}
		piped := fsharpPipelineApplied(stripped, start)
		if !fsharpHeadPosition(stripped, start) && !piped {
			continue // a trailing argument, not the head
		}
		if !fsharpArgumentFollows(stripped, end) && !piped {
			continue // a plain value reference
		}
		out[word] = struct{}{}
	}
	return out
}

// maskFSharpLiteralsAndComments blanks the F# literal and comment forms that
// span lines, then hands the rest to the shared stripper.
//
// The shared stripper ends a quoted run at the first newline, which is right for
// a language whose strings cannot cross one. F# has three that can: a
// triple-quoted string, a verbatim `@"..."` string (where `""` is an escaped
// quote), and a `(* ... *)` block comment, which nests. Their contents leaked
// into the scan, and the juxtaposition scanner reads bare identifiers, so a
// worked example inside a documentation string — `add 1 2` — was indistinguishable
// from the call it documents and became a CALLS edge.
//
// Masked bytes keep their line structure: the offside-rule checks below measure
// columns, so a mask that collapsed lines would move every following token.
func maskFSharpLiteralsAndComments(content string) string {
	raw := []byte(content)
	for i := 0; i < len(raw); i++ {
		switch {
		case raw[i] == '(' && i+1 < len(raw) && raw[i+1] == '*':
			// F# block comments nest, so the first `*)` does not necessarily
			// close the one that opened here.
			depth, j := 1, i+2
			for j+1 < len(raw) && depth > 0 {
				switch {
				case raw[j] == '(' && raw[j+1] == '*':
					depth++
					j += 2
				case raw[j] == '*' && raw[j+1] == ')':
					depth--
					j += 2
				default:
					j++
				}
			}
			if depth > 0 {
				j = len(raw)
			}
			maskBytes(raw, i, j)
			i = j - 1
		case raw[i] == '/' && i+1 < len(raw) && raw[i+1] == '/':
			j := i + 2
			for j < len(raw) && raw[j] != '\n' && raw[j] != '\r' {
				j++
			}
			maskBytes(raw, i, j)
			i = j - 1
		case raw[i] == '"' && i+2 < len(raw) && raw[i+1] == '"' && raw[i+2] == '"':
			j := i + 3
			for j+2 < len(raw) && !(raw[j] == '"' && raw[j+1] == '"' && raw[j+2] == '"') {
				j++
			}
			if j+2 < len(raw) {
				j += 3
			} else {
				j = len(raw)
			}
			maskBytes(raw, i, j)
			i = j - 1
		case raw[i] == '@' && i+1 < len(raw) && raw[i+1] == '"':
			j := i + 2
			for j < len(raw) {
				if raw[j] != '"' {
					j++
					continue
				}
				if j+1 < len(raw) && raw[j+1] == '"' {
					j += 2 // an escaped quote inside a verbatim string
					continue
				}
				j++
				break
			}
			maskBytes(raw, i, j)
			i = j - 1
		case raw[i] == '"':
			// An ordinary string, masked here so a `(*` or `//` inside one
			// cannot open a comment that swallows the rest of the file.
			j := i + 1
			for j < len(raw) && raw[j] != '\n' && raw[j] != '\r' {
				if raw[j] == '\\' {
					j += 2
					continue
				}
				if raw[j] == '"' {
					j++
					break
				}
				j++
			}
			maskBytes(raw, i, j)
			i = j - 1
		}
	}
	return stripCodeLiteralsAndComments(string(raw))
}

// fsharpOpenedModules returns the modules an F# file opens, in source order.
// `open Ledger` is what makes another module's functions callable by their bare
// name, so it is what tells the juxtaposition scan which names outside this file
// can stand in application position.
func fsharpOpenedModules(content string) []string {
	stripped := maskFSharpLiteralsAndComments(content)
	seen := map[string]bool{}
	var modules []string
	for _, match := range fsharpOpenRe.FindAllStringSubmatch(stripped, -1) {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		modules = append(modules, match[1])
	}
	return modules
}

// fsharpModuleDeclarationMatches reports whether a module symbol's declared name
// is the module an `open` names. A file writes `module Paket.UpdateProcess` and
// is opened as either the full path or, from inside the namespace, its tail.
func fsharpModuleDeclarationMatches(declared, opened string) bool {
	return declared == opened ||
		strings.HasSuffix(declared, "."+opened) ||
		strings.HasSuffix(opened, "."+declared)
}

// fsharpOpenedCallableNames is the set of callable names an F# file's `open`
// declarations bring into scope unqualified. Without it the juxtaposition scan
// only knew this file's own functions, so `open Ledger` followed by
// `add total 5` — the ordinary shape of a multi-file F# project — produced no
// edge, while the qualified `Ledger.add total 5` on the next line produced one.
//
// Only members of the opened module itself count; a sibling module in the same
// file is not brought into scope by opening its neighbour.
func fsharpOpenedCallableNames(opened []string, symbolsByShortName map[string][]SymbolRecord) map[string]bool {
	if len(opened) == 0 {
		return nil
	}
	moduleIDs := map[string]bool{}
	for _, module := range opened {
		short := module
		if cut := strings.LastIndex(module, "."); cut >= 0 {
			short = module[cut+1:]
		}
		for _, symbol := range symbolsByShortName[short] {
			if symbol.Language == "F#" && symbol.Kind == "module" && fsharpModuleDeclarationMatches(symbol.Name, module) {
				moduleIDs[symbol.ID] = true
			}
		}
	}
	if len(moduleIDs) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, symbols := range symbolsByShortName {
		for _, symbol := range symbols {
			if symbol.Language != "F#" || !moduleIDs[symbol.ContainerID] {
				continue
			}
			if symbol.Kind != "function" && symbol.Kind != "method" {
				continue
			}
			if name := lastDottedCallSegment(symbol.Name); name != "" {
				names[name] = true
			}
		}
	}
	return names
}

// fsharpFileCallableNames is the set of callable bindings declared in one F#
// file. It is what bounds the juxtaposition scan: only a name that this file
// actually defines as a function or method can be reported as applied, so an
// unknown identifier standing in application position is left alone rather than
// resolved against a same-named symbol somewhere else in the repository.
func fsharpFileCallableNames(fileSymbols []SymbolRecord) map[string]bool {
	out := map[string]bool{}
	for _, symbol := range fileSymbols {
		if symbol.Language != "F#" {
			continue
		}
		if symbol.Kind != "function" && symbol.Kind != "method" {
			continue
		}
		// A member is qualified by its container (`Ledger.add`); the call site
		// writes the bare name.
		if name := lastDottedCallSegment(symbol.Name); name != "" {
			out[name] = true
		}
	}
	return out
}
