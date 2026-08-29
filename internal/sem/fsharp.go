package sem

// F# juxtaposition call extraction. F# applies a function by writing its
// arguments beside it — `add ledger amount`, with no dot and no parentheses —
// and that is the language's ordinary call syntax. None of the existing
// scanners see it: fsharpDottedCallRe/fsharpDottedApplyRe need a `.`,
// fsharpPipelineCallRe needs a `|>`, and the generic scanner needs `name(`. So
// a module of functions calling each other produced no CALLS edge at all,
// while `capabilities --json` advertises CALLS for F#.
//
// Reading juxtaposition is harder than reading a pipe, because the pipe makes
// the applied position unambiguous and whitespace does not: in `List.map helper
// add` every one of the three names is written the same way and only one is
// applied. The approach here is the one OCaml already uses in this package
// (ocamlBareCallSites), which is the closest relative F# has — same ML binding
// forms, same application-by-juxtaposition. Three things together make it
// precise enough:
//
//   - only names that are KNOWN CALLABLES in the file are considered, so an
//     unrecognized name is never guessed into a call;
//   - a binding head (between `let`/`and`/`fun`/`member`/... and the `=` or
//     `->` that starts the body) applies nothing — every name in it is a binder
//     or a parameter;
//   - the name must stand in head position AND be followed by an argument, or
//     else be applied through a pipe operator, which is what separates
//     `add x 1` from `List.map helper add` and from a bare value reference.

import (
	"regexp"
	"strings"
)

var fsharpIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_']*`)

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

// fsharpArgumentFollows reports whether what comes after a name can start an
// argument: an identifier that is not a keyword, a literal, or an opening
// bracket. `f <| x` also applies f, with the argument behind the operator. A
// following `=`, `)`, `,`, `->`, keyword or other operator means the name was a
// definition, a record field, or a plain value reference, not an application.
func fsharpArgumentFollows(s string, end int) bool {
	i := end
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
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
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i--
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

// fsharpJuxtapositionCallIdentifiers returns the names applied by juxtaposition
// in one block. callableNames is the set of callable bindings visible in the
// file; a name outside it is never reported, which is what keeps an ordinary
// value reference or an unknown identifier from becoming a call.
func fsharpJuxtapositionCallIdentifiers(block string, callableNames map[string]bool) map[string]struct{} {
	out := map[string]struct{}{}
	if len(callableNames) == 0 {
		return out
	}
	stripped := stripCodeLiteralsAndComments(block)
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
