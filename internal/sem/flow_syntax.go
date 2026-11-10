package sem

// flowSyntax reports which value-expression shapes a language can express, so
// flow extraction runs only the pattern families that can match real code.
// The flow patterns are text regexes shared across languages, and before this
// table every family scanned every body: a Python conditional pattern ran over
// every Go body in a repository and could only ever produce a false edge,
// because the shape it matches is a syntax error in Go. Routing is therefore
// both a speed and a precision rule — a family is skipped ONLY where its shape
// cannot parse, so no real edge is lost.
//
// An unknown or unlisted language reports every family available: skipping is
// an optimization that must never drop a real edge, so uncertainty keeps the
// scan. Keep entries to what the shape's grammar actually allows, not to where
// the shape is idiomatic.
type flowSyntax struct {
	// ternary is the C-style `cond ? a() : b()` conditional expression.
	ternary bool
	// pythonCond is the Python `a() if cond else b()` conditional expression.
	pythonCond bool
	// orFallback is the word operator `a() or b()` in value position.
	orFallback bool
	// logicalFallback is `a() || b()` or `a() ?? b()` in value position.
	logicalFallback bool
}

func flowSyntaxFor(language string) flowSyntax {
	switch language {
	case "Go":
		// No ternary, no `if/else` expression, no `or`; `||` is legal on
		// boolean-returning calls, and that flow is real.
		return flowSyntax{logicalFallback: true}
	case "Python":
		return flowSyntax{pythonCond: true, orFallback: true}
	case "JavaScript", "TypeScript", "Java", "C#", "Swift", "Dart":
		return flowSyntax{ternary: true, logicalFallback: true}
	case "C", "C++":
		// `or` is a legal spelling of `||` here too: a C++ alternative token,
		// and in C the <iso646.h> macro. Guard rules exclude ` or ` lines from
		// the generic scanners, so dropping the family would lose real edges.
		return flowSyntax{ternary: true, orFallback: true, logicalFallback: true}
	case "Ruby", "PHP", "Perl":
		// Ternary plus both fallback spellings (`||` and the word `or`).
		return flowSyntax{ternary: true, orFallback: true, logicalFallback: true}
	case "Kotlin", "Scala":
		// No C ternary, but `a() or b()` parses: Kotlin's infix `or` and
		// Scala's arbitrary infix methods.
		return flowSyntax{orFallback: true, logicalFallback: true}
	case "Rust", "Bash":
		// Expression-if languages without a C ternary or a word `or`; `||` is legal.
		return flowSyntax{logicalFallback: true}
	case "Lua":
		return flowSyntax{orFallback: true}
	case "Elixir":
		return flowSyntax{orFallback: true, logicalFallback: true}
	default:
		return flowSyntax{ternary: true, pythonCond: true, orFallback: true, logicalFallback: true}
	}
}
