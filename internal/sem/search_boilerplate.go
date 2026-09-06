package sem

import "strings"

// Boilerplate protocol members are lexical magnets
// ================================================
//
// From an agent's account of a search over an aggregation package: "Four `toString()` hits. In an
// aggregation package every class has a `toString` that concatenates field names, so `toString` is a
// lexical magnet for any query containing domain nouns. It should be deranked BY CONSTRUCTION, not by
// score."
//
// That is right, and the reason is structural rather than statistical. A protocol member's body is a
// list of the type's own field names, so it matches any query that names the domain, in EVERY class
// of the package, at once. Its term overlap with a domain query is therefore evidence about the
// package's vocabulary and not about where the fix goes — but term overlap is exactly what the
// ranker scores. No amount of tuning fixes that, because the signal itself is spurious.
//
// What is keyed on, and what could not be
// ---------------------------------------
//
// The structural half is general and does the real work:
//
//   - the member's signature is fixed by the LANGUAGE, not by the program: a fixed name at a fixed
//     arity. `toString()` taking one argument is not the protocol member, it is a method that happens
//     to share a name, and it is not deranked.
//   - a trivial accessor is recognized from its shape alone — a `get`/`set`/`is` prefix at the arity
//     the prefix implies — with no name list at all.
//
// The other half is a name set, and it is unavoidable: WHICH names are protocol members is defined by
// each language's standard library, not by anything observable in the repository. `toString`,
// `hashCode`, `__repr__` and Go's `String` are boilerplate because Java, Python and Go say so. The
// set is kept to members whose entire purpose is to render or compare the type — nothing whose body
// can legitimately contain domain logic, which is why `compareTo`, `clone` and Go's `Error` are
// deliberately absent.
//
// The derank is multiplicative and modest, and it is REVOKED when the query asks about rendering or
// names the member itself: "toString drops the leading zero" is a question about a toString, and the
// derank must not hide the answer to the question that was asked.
const (
	// searchBoilerplatePrior is the multiplicative correction. It is a demotion, not a filter: a
	// repository whose only match really is a `toString` still returns it, just below anything else
	// that matched.
	searchBoilerplatePrior = 0.6

	// searchBoilerplateSignal marks a deranked candidate so the correction is visible in the payload
	// rather than being an unexplained score.
	searchBoilerplateSignal = "boilerplate-prior"

	// A data table must be long, dense and branch-free before it is treated as one. Sized from the
	// php-cs-fixer rule sets: those bodies run 40+ lines of `'rule' => value,` with no control flow.
	searchLiteralTableMinLines   = 8
	searchLiteralTableMinEntries = 5
	searchLiteralTableDensity    = 0.6

	// searchAccessorMaxSpanLines is how long a `get`/`set`/`is` member may be and still be trivial.
	// Past it the accessor is doing something — validation, lazy construction, a computed view — and
	// is a legitimate fix site.
	searchAccessorMaxSpanLines = 4
)

// searchProtocolMembers maps a language-defined rendering/comparison member to the exact number of
// parameters it takes in that role. A member with any other arity is a different method that shares
// the name and is left alone.
//
// Receivers are not counted: `self`, `this` and Rust's `&self` are dropped before the count, so one
// entry covers a language whether or not it spells the receiver in the signature.
// searchControlFlowKeywords are the line openers that make a body a DECISION rather than a table.
var searchControlFlowKeywords = []string{"if", "for", "while", "switch", "match", "case", "try", "foreach", "elseif", "else"}

var searchProtocolMembers = map[string]int{
	// Java, Kotlin, Scala, JavaScript, TypeScript
	"toString": 0,
	"hashCode": 0,
	"equals":   1,
	"toJSON":   0,
	// C#
	"ToString":    0,
	"GetHashCode": 0,
	"Equals":      1,
	// Go: the Stringer protocol. `Error` is NOT here — an error's message is often the logic.
	"String": 0,
	// Python
	"__str__":  0,
	"__repr__": 0,
	"__hash__": 0,
	"__eq__":   1,
	// Ruby
	"to_s":    0,
	"inspect": 0,
	// PHP
	"__toString": 0,
	// Rust: the Display/Debug formatter, whose only parameter is the formatter itself.
	"fmt": 1,
}

// searchBoilerplateIntentTerms are the words that make a query BE about rendering or identity. When
// one is present the derank is revoked wholesale: the caller asked about the very members it would
// otherwise hide.
var searchBoilerplateIntentTerms = []string{
	"tostring", "to_s", "hashcode", "hash", "equals", "equality", "repr", "str",
	"display", "debug", "format", "formatting", "print", "printing", "render",
	"serialize", "serialization", "json", "inspect", "getter", "setter", "accessor",
}

// applySearchBoilerplatePrior deranks language-protocol members and trivial accessors in place.
//
// It runs beside the file-class prior, on scored candidates and before selection, so a deranked
// member competes for a result slot at its corrected score rather than being removed: recall is
// unchanged, only the order is.
func applySearchBoilerplatePrior(candidates []searchCandidate, q searchQuery) {
	if searchQuerySupplied(q, searchBoilerplateIntentTerms...) {
		return
	}
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.score <= 0 {
			continue
		}
		if !searchBoilerplateSymbol(candidate.result, q) {
			continue
		}
		candidate.score *= searchBoilerplatePrior
		candidate.result.Signals = appendUnique(candidate.result.Signals, searchBoilerplateSignal)
	}
}

// searchBoilerplateSymbol reports whether a result is a protocol member or a trivial accessor.
func searchBoilerplateSymbol(result SearchResult, q searchQuery) bool {
	name := result.SymbolName
	if name == "" || result.Kind == "" {
		return false
	}
	if !searchEnclosableSymbolKind(result.Kind) {
		return false
	}
	// A caller who typed the member's own name is asking about it.
	if q.identifierTokens[strings.ToLower(name)] {
		return false
	}
	parameters, known := searchSignatureParameterCount(result.Signature, name)
	if arity, protocol := searchProtocolMembers[name]; protocol {
		return known && parameters == arity
	}
	if searchLiteralTableBody(result) {
		return true
	}
	return searchTrivialAccessor(name, result, parameters, known, q)
}

// searchLiteralTableBody reports whether a callable's body is a DATA TABLE rather than behaviour: a
// long literal collection with no control flow. It is the multi-line sibling of the one-line data
// declaration already damped in scoreSearchCandidates, and it fails the same way — the table spells
// out every name the issue uses, so term overlap scores it like the implementation while containing
// nothing to fix.
//
// Measured on php-cs-fixer/php-cs-fixer-7523 (a tokenizer bug): ranks 1 and 2 were
// `PSR12Set.getRules` and `SymfonySet.getRules`, two rule-configuration arrays, at identical scores
// (37.1786). They pushed the real fixer to rank 4 and the gold file `src/Tokenizer/TokensAnalyzer.php`
// off the list entirely. With no line anchor the agent then blind-swept that file for 77,718 B where
// the no-tool baseline used anchored reads totalling 10,784 B.
//
// Deliberately conservative: a table must be long enough to be a table (not a two-entry early
// return), dense in entry separators, and free of the control flow that would make it a decision.
func searchLiteralTableBody(result SearchResult) bool {
	body := result.Snippet
	if body == "" {
		return false
	}
	lines := strings.Split(body, "\n")
	if len(lines) < searchLiteralTableMinLines {
		return false
	}
	entries, control := 0, 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "=>") || strings.HasSuffix(trimmed, ",") {
			entries++
		}
		for _, keyword := range searchControlFlowKeywords {
			if strings.HasPrefix(trimmed, keyword) {
				control++
				break
			}
		}
	}
	if control > 0 {
		return false
	}
	return entries >= searchLiteralTableMinEntries &&
		float64(entries) >= searchLiteralTableDensity*float64(len(lines))
}

// searchTrivialAccessor reports whether a member is a plain field accessor: the conventional prefix
// at the arity the prefix implies, and a body short enough that there is nothing in it but the field.
//
// The span check is what keeps this a shape rule rather than a naming rule. A `getTotal()` that
// computes a total over 30 lines is where a bug about totals lives; a three-line one is the field.
func searchTrivialAccessor(name string, result SearchResult, parameters int, known bool, q searchQuery) bool {
	if !known {
		return false
	}
	property, arity, ok := searchAccessorShape(name)
	if !ok || parameters != arity {
		return false
	}
	if span := result.EndLine - result.StartLine; span < 0 || span > searchAccessorMaxSpanLines {
		return false
	}
	// The property the accessor exposes may itself be what the caller asked about.
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	return !words[strings.ToLower(property)]
}

// searchAccessorShape splits a conventional accessor name into the property it exposes and the arity
// the convention implies, or reports that the name is not one.
func searchAccessorShape(name string) (string, int, bool) {
	for _, shape := range []struct {
		prefix string
		arity  int
	}{
		{prefix: "get", arity: 0},
		{prefix: "is", arity: 0},
		{prefix: "has", arity: 0},
		{prefix: "set", arity: 1},
	} {
		rest, found := strings.CutPrefix(name, shape.prefix)
		if !found || rest == "" {
			continue
		}
		// `getter` is not an accessor for a property called `ter`: the convention requires a word
		// boundary, spelled as a capital or an underscore.
		if rest[0] != '_' && !(rest[0] >= 'A' && rest[0] <= 'Z') {
			continue
		}
		return strings.TrimPrefix(rest, "_"), shape.arity, true
	}
	return "", 0, false
}

// searchSignatureParameterCount counts the parameters in a signature, dropping a receiver, and
// reports false when the signature carries no parameter list to count.
//
// It reads the balanced parenthesis group that follows the member's own NAME, not the first group in
// the signature. Go puts the receiver first — `func (l Level) String() string` — so reading the first
// group counts the receiver as the only parameter and makes every Go protocol member look like it
// takes one argument.
func searchSignatureParameterCount(signature, name string) (int, bool) {
	open := searchSignatureParameterListStart(signature, name)
	if open < 0 {
		return 0, false
	}
	depth := 0
	end := -1
	for offset := open; offset < len(signature); offset++ {
		switch signature[offset] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			depth--
			if depth == 0 && signature[offset] == ')' {
				end = offset
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return 0, false
	}
	inner := strings.TrimSpace(signature[open+1 : end])
	if inner == "" {
		return 0, true
	}
	count := 0
	for _, part := range searchSplitTopLevel(inner) {
		part = strings.TrimSpace(part)
		if part == "" || searchReceiverParameter(part) {
			continue
		}
		count++
	}
	return count, true
}

// searchSignatureParameterListStart locates the `(` that opens the member's parameter list: the one
// right after its name, or the first one when the name is not spelled in the signature.
func searchSignatureParameterListStart(signature, name string) int {
	if name != "" {
		for offset := 0; ; {
			index := strings.Index(signature[offset:], name)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(name)
			offset = end
			if start > 0 && searchIdentifierByte(signature[start-1]) {
				continue
			}
			rest := strings.TrimLeft(signature[end:], " \t")
			if strings.HasPrefix(rest, "(") {
				return len(signature) - len(rest)
			}
		}
	}
	return strings.IndexByte(signature, '(')
}

// searchReceiverParameter reports whether a parameter is the receiver rather than an argument.
func searchReceiverParameter(part string) bool {
	normalized := strings.ToLower(strings.TrimLeft(part, "&*"))
	normalized = strings.TrimPrefix(normalized, "mut ")
	switch normalized {
	case "self", "this", "cls", "&self", "&mut self":
		return true
	}
	return false
}

// searchSplitTopLevel splits a parameter list on commas that are not nested inside brackets.
func searchSplitTopLevel(inner string) []string {
	parts := make([]string, 0, 4)
	depth := 0
	start := 0
	for offset := 0; offset < len(inner); offset++ {
		switch inner[offset] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:offset])
				start = offset + 1
			}
		}
	}
	return append(parts, inner[start:])
}
