package sem

// Cross-language type resolution.
//
// A bare type name in a signature is resolved against the workspace-wide
// short-name index, which is keyed by name alone. Nothing in that index says
// which language a declaration was written in, so a workspace-unique name
// matched confidently across a language boundary: an Erlang `-record(point,
// ...)` became the resolved type of an R function whose parameter happened to
// be called `point`. The edge is not merely low-confidence, it is impossible —
// R cannot name an Erlang record — and it is unstable as well, because adding a
// second `point` anywhere in the corpus makes the short name ambiguous and the
// invented edge silently disappears.
//
// Plain "same language only" is the wrong repair: C++ and Objective-C compile C
// headers verbatim, Swift imports C and Objective-C declarations through the
// Clang importer, every JVM language names Java classes directly, TypeScript
// and JavaScript share one type system, and F# names C# types. Those edges are
// real and must survive. What the resolver needs is a language *compatibility*
// relation: which languages can legitimately name each other's type
// declarations.

// typeSharingLanguageGroups lists sets of languages that genuinely share type
// declarations, i.e. where source in one language may name a type declared in
// another by its bare name. A language may appear in more than one group, so
// the resulting relation is symmetric and reflexive but deliberately NOT
// transitive: ClojureScript shares declarations with Clojure (`.cljc` sources
// are read by both) and Clojure shares them with Java (JVM class names appear
// in type hints), yet ClojureScript cannot name a Java class.
//
// Languages absent from every group are compatible only with themselves. Pairs
// deliberately left out because their interop is never a bare source-level type
// reference: Elixir/Erlang (Erlang records are macros to Elixir, not types),
// Dart/Java (platform channels are string-keyed), Ruby/C and Python/C
// (extensions are written in C, and the dynamic language never names the C
// struct), Go/C (cgo references are qualified `C.Foo`).
var typeSharingLanguageGroups = [][]string{
	// One header set. C++ and Objective-C consume C headers unchanged, and
	// Swift imports C and Objective-C declarations via bridging headers.
	// Objective-C++ is inventory-only today but belongs to the same family.
	{"C", "C++", "Objective-C", "Objective-C++", "Swift"},
	// The JVM. All of these compile to JVM classes and name Java types
	// directly in signatures and type hints.
	{"Java", "Kotlin", "Scala", "Groovy", "Clojure"},
	// Clojure dialects: `.cljc` is read by both the Clojure and the
	// ClojureScript reader.
	{"Clojure", "ClojureScript"},
	// TypeScript is a superset of JavaScript, declaration files describe
	// JavaScript, and JSDoc annotations in JavaScript name TypeScript types.
	{"JavaScript", "TypeScript"},
	// The CLR. F# names C# types directly.
	{"C#", "F#"},
}

// typeSharingLanguages is the symmetric closure of typeSharingLanguageGroups:
// language -> the set of other languages whose type declarations it may name.
var typeSharingLanguages = buildTypeSharingLanguages(typeSharingLanguageGroups)

func buildTypeSharingLanguages(groups [][]string) map[string]map[string]bool {
	compatible := map[string]map[string]bool{}
	for _, group := range groups {
		for _, a := range group {
			for _, b := range group {
				if a == b {
					continue
				}
				if compatible[a] == nil {
					compatible[a] = map[string]bool{}
				}
				compatible[a][b] = true
			}
		}
	}
	return compatible
}

// languagesShareTypes reports whether a bare type name written in language
// `from` may bind to a type declared in language `target`. Identical languages
// always share; different languages share only when they appear together in one
// typeSharingLanguageGroups entry. An unknown (empty) language shares with
// nothing but itself, so a symbol the parser could not attribute never anchors
// a cross-language edge.
func languagesShareTypes(from, target string) bool {
	if from == target {
		return true
	}
	return typeSharingLanguages[from][target]
}
