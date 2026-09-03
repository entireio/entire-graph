package sem

import (
	"path"
	"strings"
)

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
	// Shells. `source ./lib.sh` executes the library's functions in the calling
	// shell, so a `.zsh` caller genuinely names functions declared in a `.sh`
	// file. The extensions map to different language labels (Bash and Zsh), and
	// without this pair a sourced helper lost its CALLS edge entirely.
	{"Bash", "Zsh"},
	// The JVM. All of these compile to JVM classes and name Java types
	// directly in signatures and type hints.
	{"Java", "Kotlin", "Scala", "Groovy", "Clojure"},
	// Clojure dialects. The sharing here is narrower than the language pair
	// suggests and is refined by file extension in candidateSharesDeclarations:
	// `.clj` and `.cljc` are BOTH the "Clojure" language, but only `.cljc` is
	// read by the ClojureScript reader.
	{"Clojure", "ClojureScript"},
	// TypeScript is a superset of JavaScript, declaration files describe
	// JavaScript, and JSDoc annotations in JavaScript name TypeScript types.
	{"JavaScript", "TypeScript"},
	// The CLR. F# names C# types directly.
	{"C#", "F#"},
}

// typeSharingLanguageEdges lists the pairs whose interoperability runs ONE WAY,
// keyed by the naming language. A symmetric group cannot express the C family:
// C++ and Objective-C compile C headers unchanged and Swift imports both through
// the Clang importer, but C has no way to name a C++ class or template, and an
// Objective-C `.m` cannot name a C++ type either -- only `.mm` can. Listing the
// family as one symmetric set therefore licensed exactly the impossible edges
// this file exists to remove, just in the other direction.
//
// Swift reaches C++ as well as C and Objective-C: direct C++ interoperability
// imports C++ types and functions, so `Swift -> C++` is a real edge. The reverse
// is still refused -- C++ has no way to name a Swift declaration.
//
// Objective-C reaches Swift the other way, through the generated
// `<Module>-Swift.h` header: an `@objc` class or method is declared there and is
// named directly from Objective-C, so those type, inheritance, constructor and
// call edges are real.
//
// C++ reaches Objective-C-LABELLED declarations because the label follows the
// file, not the declaration: a header holding any Objective-C syntax is labelled
// Objective-C even where it also declares plain C structs and functions, which
// C++ compiles unchanged. Refusing the pair dropped those edges wholesale.
var typeSharingLanguageEdges = map[string][]string{
	"C++":           {"C", "Objective-C"},
	"Objective-C":   {"C", "Swift"},
	"Objective-C++": {"C", "C++", "Objective-C"},
	"Swift":         {"C", "Objective-C", "C++"},
}

// typeSharingLanguages is the symmetric closure of typeSharingLanguageGroups
// plus the one-way edges above: language -> the set of other languages whose
// type declarations it may name.
var typeSharingLanguages = buildTypeSharingLanguages(typeSharingLanguageGroups, typeSharingLanguageEdges)

func buildTypeSharingLanguages(groups [][]string, edges map[string][]string) map[string]map[string]bool {
	compatible := map[string]map[string]bool{}
	for from, targets := range edges {
		for _, target := range targets {
			if compatible[from] == nil {
				compatible[from] = map[string]bool{}
			}
			compatible[from][target] = true
		}
	}
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

// sharedTypeCandidates filters a candidate list from the WORKSPACE-WIDE
// short-name index down to declarations the referring symbol could actually
// name.
//
// The index is keyed by name alone, so every lookup into it is a cross-language
// lookup by construction. resolveTypeReference was filtered first because the
// Erlang-record-becomes-an-R-type case was found there, but the same index feeds
// call, constructor and inheritance resolution, and each of those selects
// a target the same way: take the candidates with this name, pick one. Measured
// before this filter, a Ruby `Point.new.process` resolved to a GO method
// (`Ruby/make -> Go/Point.process`, resolution=type_inferred) purely because Go
// declared the only other `Point`.
//
// Filtering the CANDIDATES rather than the emitted edges matters for the same
// reason it did in resolveTypeReference: an impossible foreign declaration must
// not count towards ambiguity either, or it suppresses the real edge instead of
// merely adding a false one.
//
// Same-file and same-package candidate lists are NOT routed through here. They
// cannot cross a language boundary in the first place, and filtering them would
// cost a pass over every lookup for no possible change in result.
//
// TESTS resolution reads the same index and is also NOT routed through here: a
// test name is a convention, not a type reference, so type interop is the wrong
// question to ask of it. See testRelations.
func sharedTypeCandidates(from SymbolRecord, candidates []SymbolRecord) []SymbolRecord {
	filtered := candidates[:0:0]
	for _, candidate := range candidates {
		if candidateSharesDeclarations(from, candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

// clojurePortableExt is the only Clojure source extension both readers accept.
const clojurePortableExt = ".cljc"

// candidateSharesDeclarations is languagesShareTypes plus the refinements that
// a language name alone cannot express.
//
// Clojure is the case that forces this. `.clj` (JVM-only) and `.cljc`
// (portable) are both the "Clojure" language, so a language-pair rule lets
// ClojureScript bind a declaration it can never actually name. Measured before
// this refinement, a `.cljs` consumer resolved USES_TYPE to BOTH:
//
//	ClojureScript(src/app.cljs) -> Clojure(src/jvmonly.clj)    WRONG
//	ClojureScript(src/app.cljs) -> Clojure(src/portable.cljc)  correct
//
// The extension is consulted only for a CROSS-language pair. Within one
// language the question does not arise, and a same-language `.clj` to `.clj`
// reference is exactly what should resolve.
//
// The rule is symmetric and lands correctly in both directions: a ClojureScript
// symbol always comes from `.cljs`, so a Clojure referrer never binds one,
// which is right — a JVM Clojure file cannot name a ClojureScript-only
// declaration either.
func candidateSharesDeclarations(from, candidate SymbolRecord) bool {
	if !languagesShareTypes(from.Language, candidate.Language) {
		return false
	}
	if from.Language == candidate.Language {
		return true
	}
	if isClojureDialect(from.Language) && isClojureDialect(candidate.Language) {
		// A PORTABLE consumer may name either dialect. `.cljc` is compiled by both
		// readers, and a reader conditional -- `#?(:cljs ...)` -- is exactly how it
		// references a declaration that exists in only one of them. Deciding from
		// the candidate's extension alone rejected every such reference.
		if strings.EqualFold(path.Ext(from.FilePath), clojurePortableExt) {
			return true
		}
		// A single-dialect consumer still needs a portable declaration: a `.cljs`
		// reader cannot read `.clj`, and a `.clj` reader cannot read `.cljs`.
		return strings.EqualFold(path.Ext(candidate.FilePath), clojurePortableExt)
	}
	return true
}

func isClojureDialect(language string) bool {
	return language == "Clojure" || language == "ClojureScript"
}
