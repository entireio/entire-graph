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
	writeFile(t, repo, "shape.hpp", "class Shape { public: int n; };\n")
	writeFile(t, repo, "use_shape.c", "void use_shape(Shape s) { }\n")
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
	// Clojure dialects.
	writeFile(t, repo, "tick.clj", "(defrecord Ticket [id])\n")
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
		"C/use_shape->C++/Shape",
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
		{"C", "C++"},
		{"C++", "Objective-C"},
		{"Swift", "C"},
		{"Java", "Kotlin"},
		{"Scala", "Groovy"},
		{"Clojure", "Java"},
		{"Clojure", "ClojureScript"},
		{"TypeScript", "JavaScript"},
		{"C#", "F#"},
	} {
		if !languagesShareTypes(pair[0], pair[1]) {
			t.Fatalf("languagesShareTypes(%q, %q) = false, want true", pair[0], pair[1])
		}
		if !languagesShareTypes(pair[1], pair[0]) {
			t.Fatalf("relation is not symmetric for %q/%q", pair[0], pair[1])
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
