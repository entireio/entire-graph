package sem

import (
	"regexp"
	"strings"
)

// C++-specific receiver typing.
//
// The generic localVarTypes scanner types a local only from an assignment whose
// right-hand side names the type: `x = new T(...)` or `x = T(...)`. C++'s most
// common way to create a local names the type on the LEFT and assigns nothing
// at all:
//
//	Ledger ledger;              // default construction
//	Ledger ledger(seed);        // direct initialisation
//	Ledger ledger{seed};        // brace initialisation
//	const Ledger& ledger = ...; // declared reference
//
// None of those are assignments of a constructor call, so the receiver carried
// no inferred type and every `ledger.Add(...)` on it resolved to nothing —
// while `Ledger ledger = Ledger();` and `new Ledger()` both resolved. The hole
// was in reading declarations, not in C++ receiver resolution.
//
// The scanner stays deliberately narrow. A declaration must start a statement
// (line start, `;`, `{` or `}`), its type must be capitalized — the same
// conservative stance the generic scanner takes — and it must end at `;`, `(`,
// `{` or `=`. That excludes argument positions (`f(Ledger x)` is preceded by
// `(`), template arguments (`vector<Ledger> v` matches on `vector`, which is
// not capitalized, and `Ledger` inside the brackets is preceded by `<`), and
// wrapped expressions, whose lines end at `)` or `,`.
var cppDeclaredLocalRe = regexp.MustCompile(
	`(?m)(?:^|[;{}])[ \t]*` + // statement start
		`(?:(?:const|constexpr|consteval|static|volatile|mutable|thread_local|inline|extern|register|struct|class|typename|unsigned|signed)[ \t]+)*` + // declaration specifiers
		`(?:[A-Za-z_]\w*[ \t]*::[ \t]*)*` + // optional namespace qualification
		`([A-Z]\w*)` + // the type name
		`(?:[ \t]*<[^<>;{}()]*>)?` + // optional template arguments
		`(?:[ \t]*[*&]+[ \t]*|[ \t]+)` + // pointer/reference marker, or plain whitespace
		`([A-Za-z_]\w*)[ \t]*(?:;|\(|\{|=[^=])`, // the declared name, then a declarator terminator
)

// cppDeclarationKeywords are words that can stand where a capitalized type name
// stands and are not types. Without this a macro-heavy line reads as a
// declaration.
var cppDeclarationKeywords = map[string]bool{
	"NULL": true, "TRUE": true, "FALSE": true, "return": true,
}

// cppLocalVarTypes infers variable -> type-name pairs from C++ declarations
// that carry the type on the left of the name. A name declared twice with two
// different types is dropped rather than guessed at, matching the stance of the
// Kotlin and Swift local scanners.
func cppLocalVarTypes(block string) map[string]string {
	// Each statement must begin at a match anchor, and the anchor character is
	// consumed by the previous match: without this two declarations sharing a
	// line (`Ledger a; Ledger b;`) would yield only the first. Breaking the
	// block at every `;` puts each statement at a line start instead.
	stripped := strings.ReplaceAll(stripCodeLiteralsAndComments(block), ";", ";\n")
	out := map[string]string{}
	conflicted := map[string]bool{}
	for _, m := range cppDeclaredLocalRe.FindAllStringSubmatch(stripped, -1) {
		typeName, name := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if typeName == "" || name == "" || cppDeclarationKeywords[typeName] || cppDeclarationKeywords[name] {
			continue
		}
		if conflicted[name] {
			continue
		}
		if existing, seen := out[name]; seen {
			if existing != typeName {
				delete(out, name)
				conflicted[name] = true
			}
			continue
		}
		out[name] = typeName
	}
	return out
}
