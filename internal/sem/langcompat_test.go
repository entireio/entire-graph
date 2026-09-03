package sem

import (
	"fmt"
	"sort"
	"testing"
)

// typeEdgeSet returns the signature-type relations of a snapshot keyed
// "FromLanguage/FromName->ToLanguage/ToName", so a test can assert both that a
// legitimate edge survives and that an impossible one is gone.
func typeEdgeSet(t *testing.T, snapshot ProviderSnapshot) map[string]RelationRecord {
	t.Helper()
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	edges := map[string]RelationRecord{}
	for _, relation := range snapshot.Relations {
		switch relation.Type {
		case "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE":
		default:
			continue
		}
		from, okFrom := byID[relation.FromID]
		to, okTo := byID[relation.ToID]
		if !okFrom || !okTo {
			continue
		}
		edges[fmt.Sprintf("%s/%s->%s/%s", from.Language, from.Name, to.Language, to.Name)] = relation
	}
	return edges
}

func edgeKeys(edges map[string]RelationRecord) []string {
	keys := make([]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestTypeReferenceSkipsLanguagesThatCannotShareDeclarations pins the reported
// defect: an Erlang record declaration bound as the resolved type of R and
// Clojure functions that merely reused the bare name `point`. Neither language
// can name an Erlang record.
func TestTypeReferenceSkipsLanguagesThatCannotShareDeclarations(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "geo.erl", `-module(geo).
-export([origin/0]).
-record(point, {x = 0, y = 0}).

origin() ->
    #point{x = 0, y = 0}.
`)
	writeFile(t, repo, "make.R", `make_point <- function(point, scale) {
  point
}
`)
	writeFile(t, repo, "sum.clj", `(defn point-sum [point other]
  (+ point other))
`)
	// Control: a same-language type edge in the same snapshot, so the test
	// cannot pass merely because signature-type relations stopped being
	// emitted at all.
	writeFile(t, repo, "shape.go", `package geom

type Rect struct{ W int }

func Area(r Rect) int { return r.W }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := typeEdgeSet(t, snapshot)
	for _, impossible := range []string{
		"R/make_point->Erlang/point",
		"Clojure/point-sum->Erlang/point",
	} {
		if relation, ok := edges[impossible]; ok {
			t.Fatalf("cross-language type edge %s was emitted (resolution=%s scope=%s); all edges: %v",
				impossible, relation.Resolution, relation.RelationScope, edgeKeys(edges))
		}
	}
	if _, ok := edges["Go/Area->Go/Rect"]; !ok {
		t.Fatalf("same-language control edge Go/Area->Go/Rect missing; all edges: %v", edgeKeys(edges))
	}
}

// TestTypeReferenceKeepsSharedDeclarationLanguages guards the other half: the
// language pairs that genuinely share type declarations must keep resolving
// across the boundary, in both directions. Every expectation here was observed
// on the pre-fix build, so a regression means the fix destroyed a real edge.
func TestTypeReferenceKeepsSharedDeclarationLanguages(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// C header type used from C++, and a C++ type used from C.
	writeFile(t, repo, "wid.h", "struct Widget { int w; };\n")
	writeFile(t, repo, "render.cpp", "void renderWidget(Widget w) { }\n")
	// There is deliberately no C-names-C++ fixture. That direction is impossible
	// -- `void use_shape(Shape s)` in a .c file does not compile as C at all --
	// so the case it used to assert pinned the defect rather than the contract.
	// Objective-C++ is the only family member that may name a C++ class, and it
	// is inventory-only today, so it emits no type edge to assert on. The
	// legitimate direction is covered by C++/renderWidget->C/Widget above.
	// C struct used from Swift (bridging header).
	writeFile(t, repo, "vec.h", "struct Vec3 { float x; };\n")
	writeFile(t, repo, "use_vec.swift", "func useVec(v: Vec3) { }\n")
	// JVM languages naming Java classes, and Java naming a Kotlin class.
	writeFile(t, repo, "Invoice.java", "public class Invoice { public int id; }\n")
	writeFile(t, repo, "Billing.kt", "fun billInvoice(i: Invoice) { }\n")
	writeFile(t, repo, "Ledger.java", "public class Ledger { public int id; }\n")
	writeFile(t, repo, "Book.scala", "object Book { def postLedger(l: Ledger): Unit = {} }\n")
	writeFile(t, repo, "Voucher.java", "public class Voucher { public int id; }\n")
	writeFile(t, repo, "V.groovy", "class V { def useVoucher(Voucher v) { } }\n")
	writeFile(t, repo, "Route.kt", "class Route(val id: Int)\n")
	writeFile(t, repo, "UseRoute.java", "public class UseRoute { void take(Route r) { } }\n")
	// TypeScript and JavaScript, both directions.
	writeFile(t, repo, "order.js", "export class Order { }\n")
	writeFile(t, repo, "ship.ts", "export function shipOrder(o: Order): void { }\n")
	writeFile(t, repo, "cart.ts", "export class CartX { n: number = 0 }\n")
	writeFile(t, repo, "use_cart.js", "export function takeCart(/** @type {CartX} */ CartX) { return CartX }\n")
	// Clojure dialects. The declaration is `.cljc` because that is the only
	// Clojure source both readers accept: `.clj` is JVM-only, so a `.cljs`
	// consumer genuinely cannot name it, and asserting that it could would pin
	// the defect rather than the contract.
	writeFile(t, repo, "tick.cljc", "(defrecord Ticket [id])\n")
	writeFile(t, repo, "usetick.cljs", "(defn use-ticket [Ticket] Ticket)\n")
	// CLR: F# naming a C# class.
	writeFile(t, repo, "Basket.cs", "public class Basket { public int N; }\n")
	writeFile(t, repo, "basket.fs", "let fillBasket (b: Basket) = b\n")

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := typeEdgeSet(t, snapshot)
	for _, want := range []string{
		"C++/renderWidget->C/Widget",
		"Swift/useVec->C/Vec3",
		"Kotlin/billInvoice->Java/Invoice",
		"Scala/postLedger->Java/Ledger",
		"Groovy/useVoucher->Java/Voucher",
		"Java/take->Kotlin/Route",
		"TypeScript/shipOrder->JavaScript/Order",
		"JavaScript/takeCart->TypeScript/CartX",
		"ClojureScript/use-ticket->Clojure/Ticket",
		"F#/fillBasket->C#/Basket",
	} {
		if _, ok := edges[want]; !ok {
			t.Fatalf("shared-declaration edge %s was dropped; all edges: %v", want, edgeKeys(edges))
		}
	}
}

// TestForeignDeclarationNoLongerHidesTheRealType covers the instability half of
// the defect. Because the impossible declaration used to count towards
// ambiguity, one foreign same-name symbol suppressed the real, resolvable edge:
// the invented answer and the missing answer are the same bug.
func TestForeignDeclarationNoLongerHidesTheRealType(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "geo.erl", `-module(geo).
-export([origin/0]).
-record(point, {x = 0, y = 0}).

origin() ->
    #point{x = 0, y = 0}.
`)
	writeFile(t, repo, "point.go", `package geom

type point struct{ X int }
`)
	writeFile(t, repo, "use.go", `package geom

func Shift(p point) int { return p.X }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := typeEdgeSet(t, snapshot)
	relation, ok := edges["Go/Shift->Go/point"]
	if !ok {
		t.Fatalf("Go/Shift->Go/point missing; the Erlang record still shadows it; all edges: %v", edgeKeys(edges))
	}
	if relation.Resolution != "name_only" {
		t.Fatalf("resolution = %q, want name_only", relation.Resolution)
	}
	if _, ok := edges["Go/Shift->Erlang/point"]; ok {
		t.Fatalf("Go function resolved to the Erlang record; all edges: %v", edgeKeys(edges))
	}
}

// TestLanguagesShareTypesRelation pins the compatibility relation itself:
// reflexive, symmetric, and deliberately not transitive.
func TestLanguagesShareTypesRelation(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{"Go", "Go"},
		{"Java", "Kotlin"},
		{"Scala", "Groovy"},
		{"Clojure", "Java"},
		{"Clojure", "ClojureScript"},
		{"TypeScript", "JavaScript"},
		{"C#", "F#"},
		// Sourcing runs the library's functions in the calling shell, so the
		// naming works in whichever direction the `source` is written.
		{"Bash", "Zsh"},
		// Swift imports Objective-C through the Clang importer, and Objective-C
		// names @objc Swift declarations through the generated <Module>-Swift.h.
		{"Swift", "Objective-C"},
		// C and Objective-C share in both directions, and for one reason in
		// both: the Objective-C label follows the FILE. Objective-C compiles a
		// C header unchanged, and a `.h` holding a single `#import` is labelled
		// Objective-C while still declaring the plain C a `.c` includes. This is
		// the only C-family pair that is symmetric at the LANGUAGE level, and it
		// is still not symmetric in effect: candidateSharesDeclarations keeps an
		// `@interface` or a selector out of reach of C, and
		// TestCNamesOnlyPlainCDeclarationsInObjectiveCFiles pins that.
		{"C", "Objective-C"},
	} {
		if !languagesShareTypes(pair[0], pair[1]) {
			t.Fatalf("languagesShareTypes(%q, %q) = false, want true", pair[0], pair[1])
		}
		if !languagesShareTypes(pair[1], pair[0]) {
			t.Fatalf("relation is not symmetric for %q/%q", pair[0], pair[1])
		}
	}
	// The C family is one-way. C++ and Objective-C compile C headers unchanged
	// and Swift imports both, but C has no way to name a C++ class or template,
	// and an Objective-C `.m` cannot name a C++ type either -- only `.mm` can.
	// Asserting these as symmetric licensed exactly the impossible edges this
	// file exists to remove.
	for _, pair := range [][2]string{
		{"C++", "C"},
		{"C++", "Objective-C"},
		{"Objective-C++", "C"},
		{"Objective-C++", "C++"},
		{"Objective-C++", "Objective-C"},
		{"Swift", "C"},
		{"Swift", "C++"},
	} {
		if !languagesShareTypes(pair[0], pair[1]) {
			t.Fatalf("languagesShareTypes(%q, %q) = false, want true", pair[0], pair[1])
		}
		if languagesShareTypes(pair[1], pair[0]) {
			t.Fatalf("languagesShareTypes(%q, %q) = true; that direction is impossible", pair[1], pair[0])
		}
	}
	for _, pair := range [][2]string{
		{"R", "Erlang"},
		{"Clojure", "Erlang"},
		{"Go", "Python"},
		{"Elixir", "Erlang"},
		{"Dart", "Java"},
		{"Ruby", "C"},
		{"Go", "C"},
		// Not transitive: ClojureScript shares with Clojure and Clojure
		// shares with Java, but ClojureScript cannot name a Java class.
		{"ClojureScript", "Java"},
		{"Java", "TypeScript"},
		// An unattributed symbol anchors nothing.
		{"", "Go"},
	} {
		if languagesShareTypes(pair[0], pair[1]) {
			t.Fatalf("languagesShareTypes(%q, %q) = true, want false", pair[0], pair[1])
		}
		if languagesShareTypes(pair[1], pair[0]) {
			t.Fatalf("languagesShareTypes(%q, %q) = true, want false", pair[1], pair[0])
		}
	}
	for _, group := range typeSharingLanguageGroups {
		for _, language := range group {
			if _, ok := treeSitterLanguageNames()[language]; !ok {
				t.Fatalf("typeSharingLanguageGroups names %q, which no file extension maps to", language)
			}
		}
	}
}

// treeSitterLanguageNames is the set of language names the parser can attribute
// to a file, so the compatibility table cannot drift onto names that never
// appear on a symbol.
func treeSitterLanguageNames() map[string]struct{} {
	names := map[string]struct{}{}
	for _, spec := range treeSitterLanguages {
		names[spec.language] = struct{}{}
	}
	for _, spec := range inventoryLanguageExtensions {
		names[spec.language] = struct{}{}
	}
	return names
}

// TestCallResolutionStaysInsideCompatibleLanguages is the second half of the
// cross-language repair. resolveTypeReference was filtered first because that is
// where the Erlang-record-becomes-an-R-type case surfaced, but the same
// name-keyed index feeds call, constructor, inheritance and test resolution, and
// each of those picks a target the same way.
//
// Measured on origin/main over the corpus below, all four CALLS edges resolve by
// type_inferred and two of them are wrong:
//
//	Go/run     -> Go/Point.process        correct
//	Kotlin/... -> Java/Shape.area         correct (compatible languages)
//	Python/run -> Go/Point.process        WRONG — Python has its own Point.process
//	Ruby/run   -> Go/Point.process        WRONG — Ruby has its own Point.process
//
// The edges are not merely low-confidence: Python and Ruby each declare the
// method the call actually reaches, and the resolver chose Go's because the
// index is keyed by bare name. Filtering CANDIDATES rather than emitted edges is
// what turns this into a repair instead of a deletion — with the impossible
// declarations out of the candidate set, the real ones resolve, so the edge
// count is unchanged and two edges move from a wrong target to the right one.
func TestCallResolutionStaysInsideCompatibleLanguages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go/a.go", `package a

type Point struct{ X int }

func (p Point) process() int { return 1 }
func run(point Point) int    { return point.process() }
`)
	writeFile(t, repo, "py/a.py", `class Point:
    def process(self):
        return 1

def run(point: Point):
    return Point().process()
`)
	writeFile(t, repo, "rb/a.rb", `class Point
  def process
    1
  end
end

def run
  Point.new.process
end
`)
	// A BARE-NAME call (no receiver) takes a different resolution path from the
	// receiver-typed calls above — resolveCallTargets rather than the type
	// helpers — so it is covered explicitly. Lua defines `helper` and calls it;
	// nothing else may claim that call, and Lua must not claim R's.
	writeFile(t, repo, "lua/a.lua", `function helper(x)
  return x
end

function driver()
  return helper(1)
end
`)
	writeFile(t, repo, "r/a.R", `helper <- function(x) {
  x
}

driver2 <- function() {
  helper(1)
}
`)
	// Kotlin naming a Java type is a REAL cross-language edge and must survive;
	// a same-language-only rule would delete it.
	writeFile(t, repo, "java/Shape.java", `public class Shape { public int area() { return 1; } }
`)
	writeFile(t, repo, "kt/use.kt", `fun useShape(s: Shape): Int { return s.area() }
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	language := map[string]string{}
	for _, symbol := range snapshot.Symbols {
		language[symbol.ID] = symbol.Language
	}

	// Every relation must join languages that can actually name each other.
	for _, relation := range snapshot.Relations {
		fromLanguage, okFrom := language[relation.FromID]
		toLanguage, okTo := language[relation.ToID]
		if !okFrom || !okTo {
			continue
		}
		if !languagesShareTypes(fromLanguage, toLanguage) {
			t.Errorf("%s edge crosses a language boundary that cannot be crossed: %s (%s) -> %s (%s)",
				relation.Type, relation.FromID, fromLanguage, relation.ToID, toLanguage)
		}
	}

	// And the edges that should exist still do — including the compatible
	// cross-language one, whose loss is the failure mode of a naive fix.
	for _, want := range [][2]string{
		{"run", "Point.process"},
		{"useShape", "Shape.area"},
	} {
		if !hasRelationByLastSegment(snapshot.Relations, "CALLS", want[0], want[1]) {
			t.Errorf("missing CALLS %s->%s: %#v", want[0], want[1], relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

// TestBareNameCallDoesNotCrossLanguages covers the one resolution path the
// receiver-based test above cannot reach: a bare free-function call with no
// receiver at all.
//
// That path ends at the globally-unique gate in resolveCallTargets
// ("direct call expression matched globally unique symbol name", name_only,
// confidence 0.68). The gate fires when exactly ONE symbol in the whole
// workspace carries the called name — so a name defined only in a foreign
// language is, by construction, globally unique, and used to resolve straight
// across the boundary.
//
// The fixture is built so the two halves discriminate independently:
//   - alpha_only exists ONLY in Python and is called from Python, in a
//     DIFFERENT file. It must resolve, which proves the gate is reachable and
//     that the filter has not simply disabled cross-file resolution.
//   - beta_only exists ONLY in Ruby and is called from Python. It must NOT
//     resolve, because Python cannot call a Ruby function.
//
// Without the second half a filter that broke all cross-file bare-name calls
// would pass; without the first half a filter that did nothing would pass.
func TestBareNameCallDoesNotCrossLanguages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "py/caller.py", `def driver_py():
    return alpha_only(1)
`)
	writeFile(t, repo, "py/defs.py", `def alpha_only(x):
    return x + 1
`)
	writeFile(t, repo, "py/caller2.py", `def driver2_py():
    return beta_only(1)
`)
	writeFile(t, repo, "rb/defs.rb", `def beta_only(x)
  x + 1
end
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	language := map[string]string{}
	for _, symbol := range snapshot.Symbols {
		language[symbol.ID] = symbol.Language
	}

	// The gate must still work within one language, across files.
	if !hasRelationByLastSegment(snapshot.Relations, "CALLS", "driver_py", "alpha_only") {
		t.Fatalf("a cross-file bare-name call inside one language must still resolve: %#v",
			relationsOfType(snapshot.Relations, "CALLS"))
	}
	// And must not reach across a boundary the languages cannot cross.
	for _, relation := range relationsOfType(snapshot.Relations, "CALLS") {
		fromLanguage, toLanguage := language[relation.FromID], language[relation.ToID]
		if fromLanguage != "" && toLanguage != "" && !languagesShareTypes(fromLanguage, toLanguage) {
			t.Fatalf("a bare-name call resolved across a language boundary: %s (%s) -> %s (%s), resolution %s",
				relation.FromID, fromLanguage, relation.ToID, toLanguage, relation.Resolution)
		}
	}
}

// TestClojureScriptOnlyBindsPortableClojureDeclarations covers the case a
// language-pair rule cannot express.
//
// `.clj` (JVM-only) and `.cljc` (portable) are BOTH the "Clojure" language —
// parser.go maps them to the same name — so allowing the Clojure/ClojureScript
// pair wholesale lets a `.cljs` consumer bind a declaration it can never name.
// Measured before the refinement, this fixture produced both:
//
//	ClojureScript(src/app.cljs) -> Clojure(src/jvmonly.clj)    WRONG
//	ClojureScript(src/app.cljs) -> Clojure(src/portable.cljc)  correct
//
// The test asserts both halves, because a refinement that simply severed the
// dialect pair would remove the wrong edge and the right one together.
func TestClojureScriptOnlyBindsPortableClojureDeclarations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/jvmonly.clj", `(ns jvmonly)

(defrecord JvmOnlyThing [x])
`)
	writeFile(t, repo, "src/portable.cljc", `(ns portable)

(defrecord PortableThing [y])
`)
	writeFile(t, repo, "src/app.cljs", `(ns app)

(defn use-jvm [^JvmOnlyThing a] a)

(defn use-portable [^PortableThing b] b)
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	fileOf := map[string]string{}
	for _, symbol := range snapshot.Symbols {
		fileOf[symbol.ID] = symbol.FilePath
	}

	// Only edges FROM the ClojureScript consumer are the question here. The
	// .clj file's own DEFINES/CONTAINS edges point into itself and are correct;
	// counting those would fail the test for the right behaviour.
	portable, jvmOnly := false, false
	for _, relation := range snapshot.Relations {
		if fileOf[relation.FromID] != "src/app.cljs" {
			continue
		}
		switch fileOf[relation.ToID] {
		case "src/portable.cljc":
			portable = true
		case "src/jvmonly.clj":
			jvmOnly = true
		}
	}
	if jvmOnly {
		t.Errorf("ClojureScript bound a JVM-only .clj declaration it cannot name: %#v", snapshot.Relations)
	}
	if !portable {
		t.Errorf("the portable .cljc declaration must still resolve; severing the dialect pair is not the fix: %#v", snapshot.Relations)
	}
}

// TestPortableClojureConsumerMayNameEitherDialect pins the direction the extension rule
// has to read.
//
// `.cljc` is compiled by BOTH readers, and a reader conditional -- `#?(:cljs ...)` -- is
// exactly how a portable file references a declaration that exists in only one dialect.
// Deciding compatibility from the CANDIDATE's extension alone rejected every such
// reference, dropping real CALLS and type edges out of portable code.
//
// The single-dialect direction is asserted with it because that is the rule this must not
// loosen: a `.cljs` reader still cannot read `.clj`.
func TestPortableClojureConsumerMayNameEitherDialect(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name                               string
		fromLang, fromPath, toLang, toPath string
		want                               bool
	}{
		{
			name:     "a portable consumer reaches a ClojureScript-only declaration",
			fromLang: "Clojure", fromPath: "src/portable.cljc",
			toLang: "ClojureScript", toPath: "src/browser.cljs",
			want: true,
		},
		{
			name:     "a ClojureScript consumer still cannot read a JVM-only declaration",
			fromLang: "ClojureScript", fromPath: "src/app.cljs",
			toLang: "Clojure", toPath: "src/jvmonly.clj",
			want: false,
		},
		{
			name:     "a ClojureScript consumer reaches a portable declaration",
			fromLang: "ClojureScript", fromPath: "src/app.cljs",
			toLang: "Clojure", toPath: "src/portable.cljc",
			want: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := candidateSharesDeclarations(
				SymbolRecord{Language: testCase.fromLang, FilePath: testCase.fromPath},
				SymbolRecord{Language: testCase.toLang, FilePath: testCase.toPath},
			)
			if got != testCase.want {
				t.Fatalf("candidateSharesDeclarations(%s, %s) = %v, want %v",
					testCase.fromPath, testCase.toPath, got, testCase.want)
			}
		})
	}
}

// resolvedEdgeSet is typeEdgeSet widened to every symbol-to-symbol relation, so
// a test can assert on a CALLS edge as well as a signature-type one.
func resolvedEdgeSet(t *testing.T, snapshot ProviderSnapshot) map[string]RelationRecord {
	t.Helper()
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	edges := map[string]RelationRecord{}
	for _, relation := range snapshot.Relations {
		if relation.Type == "CONTAINS" {
			continue
		}
		from, okFrom := byID[relation.FromID]
		to, okTo := byID[relation.ToID]
		if !okFrom || !okTo {
			continue
		}
		edges[fmt.Sprintf("%s %s/%s->%s/%s", relation.Type, from.Language, from.Name, to.Language, to.Name)] = relation
	}
	return edges
}

// C++ may name Objective-C-LABELLED declarations only because the label follows
// the FILE: a header holding any Objective-C syntax is labelled Objective-C even
// where it also declares the plain C structs and functions a `.cpp` compiles
// unchanged. Treating every candidate under that label as visible to C++ also
// handed it the declarations that are Objective-C in their own right.
//
// Measured on the fixture below before the refinement, all three edges resolved
// and two of them are impossible:
//
//	C++/renderShape  -> Objective-C/Shape        WRONG — an @interface class
//	C++/callIt       -> Objective-C/computeArea  WRONG — a selector, message-send only
//	C++/renderWidget -> Objective-C/Widget       correct — a plain C struct
//
// The last one is the reason the pair is kept rather than removed, so it is
// asserted here too: the narrowing must not swallow it.
func TestCppNamesOnlyPlainCDeclarationsInObjectiveCFiles(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "shape.h", `#import <Foundation/Foundation.h>

@interface Shape : NSObject
- (int)area;
@end
`)
	writeFile(t, repo, "impl.m", `#import "shape.h"

@implementation Renderer
- (int)computeArea { return 1; }
@end
`)
	writeFile(t, repo, "render.cpp", `void renderShape(Shape s) { }
int callIt() { return computeArea(); }
`)
	// The legitimate half of the pair: an Objective-C-labelled header (it is
	// labelled that way because of the `#import`) declaring a plain C struct,
	// named from a `.cpp`.
	writeFile(t, repo, "legit.h", `#import <Foundation/Foundation.h>

struct Widget { int w; };
`)
	writeFile(t, repo, "legit.cpp", `void renderWidget(Widget w) { }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := resolvedEdgeSet(t, snapshot)
	for _, impossible := range []string{
		"USES_TYPE C++/renderShape->Objective-C/Shape",
		"PARAM_TYPE C++/renderShape->Objective-C/Shape",
		"CALLS C++/callIt->Objective-C/computeArea",
		"DATA_FLOWS Objective-C/computeArea->C++/callIt",
	} {
		if relation, ok := edges[impossible]; ok {
			t.Fatalf("C++ bound an Objective-C-only declaration: %s (resolution=%s); all edges: %v",
				impossible, relation.Resolution, edgeKeys(edges))
		}
	}
	// Still resolves: narrowing the candidates must not cost the plain C
	// declarations that are the only reason C++ -> Objective-C exists.
	for _, want := range []string{
		"USES_TYPE C++/renderWidget->Objective-C/Widget",
		"PARAM_TYPE C++/renderWidget->Objective-C/Widget",
	} {
		if _, ok := edges[want]; !ok {
			t.Fatalf("plain C declaration in an Objective-C-labelled header was dropped: %s; all edges: %v", want, edgeKeys(edges))
		}
	}
}

// C reaches Objective-C-LABELLED declarations for one reason only, the same one
// that earns C++ the pair: the label follows the FILE. A `.h` is labelled
// Objective-C the moment it holds a single `#import` or `@interface`, and such
// headers keep declaring the plain C structs and functions a `.c` includes and
// compiles verbatim.
//
// Measured on the fixture below before the `C -> Objective-C` edge existed, with
// the plain-C `gadget.h` as the control -- the same two declarations, the same
// `.c` consumer, differing only in the header's label:
//
//	C/renderGadget -> C/Gadget                    resolved   (control)
//	C/callGadget   -> C/gadgetSize                resolved   (control)
//	C/renderWidget -> Objective-C/Widget          DROPPED    (plain C struct)
//	C/callWidth    -> Objective-C/widgetWidth     DROPPED    (plain C function)
//
// The widening must not cost the narrowing it sits on top of, so the other half
// is asserted here as well: an `@interface` and a selector stay unreachable from
// C, which cannot even declare them.
func TestCNamesOnlyPlainCDeclarationsInObjectiveCFiles(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// Labelled Objective-C because of the `#import`, yet most of it is plain C.
	writeFile(t, repo, "shape.h", `#import <Foundation/Foundation.h>

@interface Shape : NSObject
- (int)area;
@end

struct Widget { int w; };

static inline int widgetWidth(struct Widget w) { return w.w; }
`)
	writeFile(t, repo, "impl.m", `#import "shape.h"

@implementation Renderer
- (int)computeArea { return 1; }
@end
`)
	// The control: the same two plain C declarations in a header with no
	// Objective-C syntax, so it keeps the C label.
	writeFile(t, repo, "gadget.h", `struct Gadget { int g; };

static inline int gadgetSize(struct Gadget g) { return g.g; }
`)
	writeFile(t, repo, "use.c", `#include "shape.h"
#include "gadget.h"

void renderWidget(struct Widget w) { }
void renderShape(Shape s) { }
int callWidth(void) { struct Widget w; return widgetWidth(w); }
int callIt(void) { return computeArea(); }
void renderGadget(struct Gadget g) { }
int callGadget(void) { struct Gadget g; return gadgetSize(g); }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := resolvedEdgeSet(t, snapshot)
	// The control fixes the baseline: if these are missing, C type and call
	// resolution broke generally and the assertions below say nothing about the
	// language pair.
	for _, control := range []string{
		"USES_TYPE C/renderGadget->C/Gadget",
		"CALLS C/callGadget->C/gadgetSize",
	} {
		if _, ok := edges[control]; !ok {
			t.Fatalf("same-language C control edge is missing, the fixture proves nothing: %s; all edges: %v", control, edgeKeys(edges))
		}
	}
	// The widening: the plain C half of an Objective-C-labelled header is
	// reachable from a `.c` exactly as the control is.
	for _, want := range []string{
		"USES_TYPE C/renderWidget->Objective-C/Widget",
		"PARAM_TYPE C/renderWidget->Objective-C/Widget",
		"CALLS C/callWidth->Objective-C/widgetWidth",
	} {
		if _, ok := edges[want]; !ok {
			t.Fatalf("plain C declaration in an Objective-C-labelled header is unreachable from a C source: %s; all edges: %v", want, edgeKeys(edges))
		}
	}
	// The narrowing it must not undo: the Objective-C half stays out of reach.
	for _, impossible := range []string{
		"USES_TYPE C/renderShape->Objective-C/Shape",
		"PARAM_TYPE C/renderShape->Objective-C/Shape",
		"CALLS C/callIt->Objective-C/computeArea",
		"DATA_FLOWS Objective-C/computeArea->C/callIt",
	} {
		if relation, ok := edges[impossible]; ok {
			t.Fatalf("C bound an Objective-C-only declaration: %s (resolution=%s); all edges: %v",
				impossible, relation.Resolution, edgeKeys(edges))
		}
	}
}

// A DUAL-USE header -- the `#ifdef __cplusplus` / `extern "C" {` header written
// precisely SO a C translation unit can include it -- is labelled C++:
// looksLikeCPlusPlusHeader fires on the `extern "C"` marker itself. C names no
// C++ target in typeSharingLanguageEdges, and correctly so, because a template,
// an overload set, a class and a namespaced function are declarations a `.c`
// genuinely cannot name. The consequence was that a `.c` including such a header
// reached NOTHING in it.
//
// Measured on the fixture below before the fix, with the plain-C `gadget.h` as
// the control -- the same two declarations, the same `.c` consumer, differing
// only in the header that holds them:
//
//	C/renderGadget -> C/Gadget            resolved   (control)
//	C/callGadget   -> C/gadgetSize        resolved   (control)
//	C/renderWidget -> C++/Widget          DROPPED    (declared `extern "C"`)
//	C/callWidth    -> C++/widgetWidth     DROPPED    (declared `extern "C"`)
//
// Both halves are pinned here, because the widening is per DECLARATION and not
// per language: what the header put under C linkage is reachable, and the C++
// half of the SAME header -- a class, a namespaced function and an overload set
// -- is not.
func TestCNamesOnlyExternCDeclarationsInCPlusPlusHeaders(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// Labelled C++ because of the `extern "C"` marker, yet its C-linkage block
	// is plain C that a `.c` includes and compiles verbatim.
	writeFile(t, repo, "engine.h", `#ifndef ENGINE_H
#define ENGINE_H

#ifdef __cplusplus
extern "C" {
#endif

struct Widget { int w; };

static inline int widgetWidth(struct Widget w) { return w.w; }

#ifdef __cplusplus
}
#endif

#ifdef __cplusplus

namespace detail {
inline int helperCount(void) { return 1; }
}

inline int overloaded(int a) { return a; }
inline double overloaded(double a) { return a * 2; }

class Engine {
public:
  int run() { return 0; }
};

#endif

#endif
`)
	// The control: the same two plain C declarations in a header with no C++
	// syntax at all, so it keeps the C label.
	writeFile(t, repo, "gadget.h", `struct Gadget { int g; };

static inline int gadgetSize(struct Gadget g) { return g.g; }
`)
	writeFile(t, repo, "use.c", `#include "engine.h"
#include "gadget.h"

void renderWidget(struct Widget w) { }
int callWidth(void) { struct Widget w; return widgetWidth(w); }
void renderEngine(Engine e) { }
int callHelper(void) { return helperCount(); }
int callOverloaded(void) { return overloaded(1); }
void renderGadget(struct Gadget g) { }
int callGadget(void) { struct Gadget g; return gadgetSize(g); }
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// Precondition, and the first thing that broke: an `extern "C" {` block was
	// blanked out of the source before the C++ grammar ever saw it, and -- since
	// the canonical dual-use header closes with a bare `}` rather than the
	// annotated `}  // extern "C"` -- the blanking ran to END OF FILE. The
	// header contributed no declarations at all, so no language rule could have
	// made one reachable.
	declared := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == "engine.h" {
			declared[symbol.Name] = symbol
		}
	}
	for _, name := range []string{"Widget", "widgetWidth", "helperCount", "overloaded", "Engine"} {
		if _, ok := declared[name]; !ok {
			t.Fatalf("engine.h declares %q and the parser produced no symbol for it; declared: %v", name, sortedSymbolNames(declared))
		}
	}

	edges := resolvedEdgeSet(t, snapshot)
	// The control fixes the baseline: if these are missing, C type and call
	// resolution broke generally and the assertions below say nothing about the
	// language pair.
	for _, control := range []string{
		"USES_TYPE C/renderGadget->C/Gadget",
		"CALLS C/callGadget->C/gadgetSize",
	} {
		if _, ok := edges[control]; !ok {
			t.Fatalf("same-language C control edge is missing, the fixture proves nothing: %s; all edges: %v", control, edgeKeys(edges))
		}
	}
	// The widening: what the header declared `extern "C"` is reachable from a
	// `.c` exactly as the control is.
	for _, want := range []string{
		"USES_TYPE C/renderWidget->C++/Widget",
		"PARAM_TYPE C/renderWidget->C++/Widget",
		"CALLS C/callWidth->C++/widgetWidth",
	} {
		if _, ok := edges[want]; !ok {
			t.Fatalf("declaration inside `extern \"C\"` is unreachable from a C source: %s; all edges: %v", want, edgeKeys(edges))
		}
	}
	// The narrowing the widening must not cost: the C++ half of the SAME header
	// stays out of reach. A `.c` cannot name a class, resolve an overload set or
	// call a namespaced function, and none of the three carries C linkage.
	for _, impossible := range []string{
		"USES_TYPE C/renderEngine->C++/Engine",
		"PARAM_TYPE C/renderEngine->C++/Engine",
		"CALLS C/callHelper->C++/helperCount",
		"CALLS C/callOverloaded->C++/overloaded",
		"DATA_FLOWS C++/helperCount->C/callHelper",
		"DATA_FLOWS C++/overloaded->C/callOverloaded",
	} {
		if relation, ok := edges[impossible]; ok {
			t.Fatalf("C bound a C++-only declaration: %s (resolution=%s); all edges: %v",
				impossible, relation.Resolution, edgeKeys(edges))
		}
	}
}

func sortedSymbolNames(symbols map[string]SymbolRecord) []string {
	names := make([]string, 0, len(symbols))
	for name := range symbols {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Objective-C names Swift declarations through the generated `<Module>-Swift.h`
// header, and that header holds only what Swift exposes to the Objective-C
// runtime. Binding by bare name alone offered it the whole module.
//
// Measured on the fixture below before the refinement:
//
//	Objective-C/useLedger  -> Swift/Ledger    WRONG — a struct is never bridged
//	Objective-C/useEngine  -> Swift/Engine    WRONG — a pure-Swift root class
//	Objective-C/go         -> Swift/freeFunc  WRONG — a global func is not bridged
//	Objective-C/useBridged -> Swift/Bridged   correct — @objc class Bridged: NSObject
func TestObjectiveCNamesOnlySwiftDeclarationsExposedToIt(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "models.swift", `struct Ledger { var id: Int }
class Engine { func start() {} }
@objc class Bridged: NSObject { }
func freeFunc(x: Int) -> Int { return x }
`)
	writeFile(t, repo, "use.m", `#import <Foundation/Foundation.h>
#import "Proj-Swift.h"

@implementation Consumer
- (void)useLedger:(Ledger *)l { }
- (void)useEngine:(Engine *)e { }
- (void)useBridged:(Bridged *)b { }
- (void)go { freeFunc(1); }
@end
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	edges := resolvedEdgeSet(t, snapshot)
	for _, impossible := range []string{
		"USES_TYPE Objective-C/useLedger->Swift/Ledger",
		"USES_TYPE Objective-C/useEngine->Swift/Engine",
		"CALLS Objective-C/go->Swift/freeFunc",
	} {
		if relation, ok := edges[impossible]; ok {
			t.Fatalf("Objective-C bound a Swift declaration absent from the generated header: %s (resolution=%s); all edges: %v",
				impossible, relation.Resolution, edgeKeys(edges))
		}
	}
	// Still resolves: an `@objc` class IS in `<Module>-Swift.h`, and that edge is
	// the whole reason the Objective-C -> Swift direction exists.
	if _, ok := edges["USES_TYPE Objective-C/useBridged->Swift/Bridged"]; !ok {
		t.Fatalf("the @objc Swift class edge was dropped; all edges: %v", edgeKeys(edges))
	}
}

// swiftDeclarationVisibleToObjectiveC decides from a declaration's own
// signature, so the shapes it must read are pinned directly: the attribute in
// every spelling it can carry, and the inheritance clause that separates a
// bridgeable class from a pure-Swift root class.
func TestSwiftDeclarationVisibleToObjectiveC(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind      string
		signature string
		want      bool
	}{
		{"class", "@objc class Bridged: NSObject", true},
		{"class", "@objcMembers class Members: NSObject", true},
		{"class", "@objc(RenamedThing) class Renamed: NSObject", true},
		{"class", "class Engine: NSObject", true},
		{"class", "class Sub: Engine", true},
		{"class", "class Engine", false},
		{"struct", "struct Ledger", false},
		{"enum", "enum Plain", false},
		{"enum", "@objc enum Level: Int", true},
		{"protocol", "protocol Pinger", false},
		{"protocol", "@objc protocol Pinger", true},
		{"function", "func freeFunc(x: Int) -> Int", false},
		// Unproven shapes are kept: a member's exposure follows its container,
		// which a candidate-level check cannot see, and an unparsed signature
		// says nothing at all.
		{"method", "func start()", true},
		{"class", "", true},
	} {
		got := swiftDeclarationVisibleToObjectiveC(SymbolRecord{Kind: tc.kind, Signature: tc.signature, Language: "Swift"})
		if got != tc.want {
			t.Fatalf("swiftDeclarationVisibleToObjectiveC(%s %q) = %v, want %v", tc.kind, tc.signature, got, tc.want)
		}
	}
}
