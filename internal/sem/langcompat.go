package sem

import (
	"path"
	"regexp"
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
//
// C reaches them for exactly that reason and no other, which is why it is the
// single target C names. A `.h` is labelled Objective-C the moment it contains
// one `#import` or one `@interface` (see looksLikeObjectiveC), and such headers
// routinely go on declaring the plain C structs, typedefs and functions a `.c`
// includes and compiles verbatim. Measured before this edge, over a fixture
// whose `.c` includes an Objective-C-labelled header and an otherwise identical
// plain-C one:
//
//	C/renderGadget -> C/Gadget                    resolved
//	C/renderWidget -> Objective-C/Widget          DROPPED (same declaration)
//
// The edge stays one-way at the family level: C still cannot name a C++ class or
// template, nor a Swift declaration, so nothing else moves. It is narrowed by
// kind in candidateSharesDeclarations exactly as the C++ edge is -- an
// `@interface` or a selector is no more nameable from C than from C++.
var typeSharingLanguageEdges = map[string][]string{
	"C":             {"Objective-C"},
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
// always share; different languages share when they appear together in a
// symmetric typeSharingLanguageGroups relationship or in a directional
// typeSharingLanguageEdges entry. An unknown (empty) language shares with
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
// Same-file and same-package candidate lists are NOT routed through here.
// Their resolution scopes are already direct declaration evidence, so filtering
// them would cost a pass over every lookup without strengthening this guard.
//
// TESTS resolution reads the same index and is also NOT routed through here: a
// test name is a convention, not a type reference, so type interop is the wrong
// question to ask of it. See testRelations.
//
// Import-resolved calls are the other exception, for the same reason in a
// different shape: an import whose module path resolves to the callee's FILE is
// direct evidence the two files interoperate, and that outranks a language-pair
// heuristic about naming types. Both spellings of such a call are excepted --
// the qualified `frobnicate.compute()` in importedReceiverCallTargets and the
// bare `compute()` in resolveCallTargets -- and both apply the same rule: prefer
// these candidates, and consult the unfiltered set only when it names exactly
// one target.
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
	if candidate.Language == "C++" && namesOnlyTheCLinkageHalfOfCPlusPlus(from.Language) {
		// C++ as a LANGUAGE stays unreachable from these consumers, which is why
		// neither pair is in typeSharingLanguageEdges and neither is added: a
		// template, an overload set, a namespaced function and a class are
		// declarations a C or Objective-C source cannot name, and licensing
		// either pair would bind all of them.
		//
		// One construct is different, and it is the reason a `.h` gets the C++
		// label in the first place. `extern "C" { ... }` is how a C++ header
		// states which of its declarations have C linkage, i.e. exactly which
		// ones a `.c` may include and call. The dual-use header -- guarded with
		// `#ifdef __cplusplus` / `extern "C" {` and written precisely SO a C
		// translation unit can include it -- is labelled C++ by
		// looksLikeCPlusPlusHeader, which fires on that very marker. Measured
		// over a fixture whose `.c` includes such a header and an otherwise
		// identical plain-C control:
		//
		//	C/renderGadget -> C/Gadget            resolved (control)
		//	C/renderWidget -> C++/Widget          DROPPED  (same declaration)
		//	C/callWidth    -> C++/widgetWidth     DROPPED  (same declaration)
		//
		// So the widening is per DECLARATION, not per language: only what the
		// header itself put under C linkage is offered. Everything outside the
		// block -- the template, the class, the overload, the namespaced
		// function -- keeps cLinkage false and stays filtered out.
		//
		// An Objective-C `.m` is the same consumer in a different file
		// extension, and the guards make that literal: compiled as Objective-C,
		// `__cplusplus` is not defined, so the preprocessor deletes
		// `extern "C" {` and its closing `}` and the `.m` compiles the plain C
		// between them exactly as a `.c` does. Measured over the same fixture
		// with a `.m` consumer and the plain-C control beside it:
		//
		//	Objective-C/renderGadget -> C/Gadget          resolved (control)
		//	Objective-C/renderWidget -> C++/Widget        DROPPED  (same declaration)
		//	Objective-C/callWidth    -> C++/widgetWidth   DROPPED  (same declaration)
		//
		// Objective-C++ is NOT here: a `.mm` is a C++ translation unit and names
		// the whole header, C-linkage or not, so it holds the `C++` edge at the
		// language level instead.
		return candidate.cLinkage
	}
	if !languagesShareTypes(from.Language, candidate.Language) {
		return false
	}
	if from.Language == candidate.Language {
		return true
	}
	if isCFamilyPlainConsumer(from.Language) && candidate.Language == "Objective-C" {
		// Both pairs exist only because the Objective-C LABEL follows the file:
		// a header holding any Objective-C syntax is Objective-C even where it
		// also declares the plain C structs and functions a `.c` or `.cpp`
		// compiles unchanged. The declarations that are Objective-C in their own
		// right -- an `@interface`/`@implementation` (kind "class"), an
		// `@protocol` (kind "interface"), and a method, which is reachable only
		// by message send -- are not among them, and binding one made the
		// consumer name a class it cannot even declare. Measured before this
		// refinement, over a fixture with an `@interface Shape` header and a
		// `.m` method:
		//
		//	C++/renderShape -> Objective-C/Shape        WRONG (@interface)
		//	C++/callIt      -> Objective-C/computeArea  WRONG (message send)
		//	C++/renderWidget -> Objective-C/Widget      correct (plain C struct)
		//
		// C is held to the same rule, not a looser one: it reaches the label for
		// the same reason and is even less able to name an Objective-C class
		// than C++ is.
		return !objectiveCOnlyDeclaration(candidate.Kind)
	}
	if from.Language == "Objective-C" && candidate.Language == "Swift" {
		return swiftDeclarationVisibleToObjectiveC(candidate)
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

// namesOnlyTheCLinkageHalfOfCPlusPlus reports whether a language may name a
// C++-LABELLED declaration only when that declaration itself has C linkage. C
// and Objective-C both compile the `extern "C"` half of a dual-use header
// verbatim and neither can name the C++ half. C++ and Objective-C++ are absent
// deliberately: both are C++ translation units, and routing them through this
// exception would hide every ordinary C++ declaration from them.
func namesOnlyTheCLinkageHalfOfCPlusPlus(language string) bool {
	return language == "C" || language == "Objective-C"
}

// isCFamilyPlainConsumer reports whether a language compiles the plain C half of
// an Objective-C-labelled file verbatim. C and C++ both do, and neither can name
// the Objective-C half; Objective-C++ is excluded because it genuinely speaks
// Objective-C and must keep naming classes and selectors.
func isCFamilyPlainConsumer(language string) bool {
	return language == "C" || language == "C++"
}

func isClojureDialect(language string) bool {
	return language == "Clojure" || language == "ClojureScript"
}

// objectiveCOnlyDeclaration reports whether a declaration the Objective-C
// parser produced is an Objective-C construct rather than one of the plain C
// declarations that share the file's language label. `class` is
// `@interface`/`@implementation` (tree-sitter-objc reuses the same node for
// `@class Foo;`), `interface` is `@protocol`, and `method` is a
// `method_definition` -- a selector, callable only as `[receiver selector]`.
// Everything else the extractor emits from an Objective-C file (function,
// struct, enum, union, typedef, field, variable, macro) is C that a C++
// translation unit compiles verbatim.
func objectiveCOnlyDeclaration(kind string) bool {
	switch kind {
	case "class", "interface", "method":
		return true
	}
	return false
}

// swiftObjCExposureAttributeRe matches the attributes that put a Swift
// declaration into the generated header: `@objc`, `@objc(RenamedThing)` and
// `@objcMembers`. The extractor keeps a declaration's attributes in its
// signature, including when they sit on their own line above it.
var swiftObjCExposureAttributeRe = regexp.MustCompile(`@objc(?:Members)?\b`)

// swiftDeclarationVisibleToObjectiveC reports whether a Swift declaration could
// appear in the generated `<Module>-Swift.h` header, which is the ONLY way an
// Objective-C source names a Swift declaration. The language pair is real --
// that header exists and is imported -- but it covers far less than the whole
// Swift module, and binding by bare name alone produced edges no compiler
// could: measured before this refinement,
//
//	Objective-C/useLedger -> Swift/Ledger    WRONG (a struct is never bridged)
//	Objective-C/useEngine -> Swift/Engine    WRONG (a pure-Swift root class)
//	Objective-C/go        -> Swift/freeFunc  WRONG (a global func is not bridged)
//	Objective-C/useBridged -> Swift/Bridged  correct (@objc class Bridged: NSObject)
//
// Only what the language rules make impossible is refused. A declaration whose
// exposure this cannot decide from its own signature -- a member, an extension,
// a subclass whose chain to NSObject runs through classes a candidate-level
// check cannot follow -- is kept, because dropping a candidate here deletes the
// edge rather than merely adding one.
func swiftDeclarationVisibleToObjectiveC(candidate SymbolRecord) bool {
	signature := candidate.Signature
	if signature == "" {
		// Nothing to judge on: leave the candidate exactly as it was.
		return true
	}
	if swiftObjCExposureAttributeRe.MatchString(signature) {
		return true
	}
	switch candidate.Kind {
	case "struct", "enum", "protocol", "function":
		// Categorical, and none of them is reachable by adding an attribute
		// elsewhere: a Swift struct is never bridged; an enum reaches
		// Objective-C only as `@objc enum`, a protocol only as `@objc protocol`,
		// and a global `func` is never emitted into the generated header at all.
		return false
	case "class":
		// A Swift class is exposed only by inheriting an Objective-C class --
		// `@objc` on a root class is rejected by the compiler -- so a
		// declaration with no inheritance clause at all cannot be in the header.
		return swiftClassDeclaresInheritance(signature)
	}
	return true
}

// swiftClassDeclaresInheritance reports whether a Swift class signature names
// anything after its inheritance colon. A generic constraint (`class Box<T:
// Equatable>`) counts, which keeps a root class that only looks inherited --
// the direction that keeps an edge rather than deleting one.
func swiftClassDeclaresInheritance(signature string) bool {
	keyword := strings.Index(signature, "class ")
	if keyword < 0 {
		return true
	}
	return strings.Contains(signature[keyword+len("class "):], ":")
}
