package sem

import (
	"fmt"
	"strings"
	"testing"
)

// TestCPlusPlusInClassMethodDeclarationsAreExtracted covers the declarations a
// C++ header exists to hold. A header declares an interface — constructors,
// destructor, const/static/noexcept/override members, operators — and defines
// almost none of it; the definitions live in the .cpp as `Type::method`. None
// of those declarations were extracted, so a class whose members are declared
// in the body and defined out of line contributed no method symbols at all: no
// symbol to search for, no CONTAINS edge, and nothing for a typed receiver to
// resolve a call to.
func TestCPlusPlusInClassMethodDeclarationsAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "ledger.hpp", `#pragma once

class Ledger {
public:
    Ledger();
    explicit Ledger(int seed);
    ~Ledger();
    int Add(int amount) const;
    static int Version();
    void Reset() noexcept;
    virtual int Over(int factor) const override;
    Ledger& operator=(const Ledger& other);
    int Inline(int x) { return x; }

private:
    int total_ = 0;
    int (*handler_)(int);
};

struct Config {
    int Load(const char* path);
    int size;
};
`)
	for _, want := range []string{
		"method:Ledger.Ledger",
		"method:Ledger.~Ledger",
		"method:Ledger.Add",
		"method:Ledger.Version",
		"method:Ledger.Reset",
		"method:Ledger.Over",
		"method:Ledger.Inline",
		"method:Config.Load",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	// Data members must stay data. A function-POINTER member is the shape that
	// looks most like a method declaration and is not one, and #174 classifies
	// it as a field; this must not take it back.
	for _, want := range []string{"field:Ledger.total_", "field:Ledger.handler_", "field:Config.size"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	if _, ok := got["method:Ledger.handler_"]; ok {
		t.Errorf("function-pointer member was reclassified as a method: %#v", mapKeys(got))
	}
}

// TestCPlusPlusOperatorDeclarationsStayUnextracted pins a deliberate omission.
// maskCPlusPlusOperatorCall rewrites `operator=(` to `op(` before tree-sitter
// sees the file, so the parsed name is not the written name AND every masked
// operator collapses onto the same `op` — extracting them would put two symbols
// with one name on a class that overloads more than one operator. Recovering
// the real name belongs with the mask.
func TestCPlusPlusOperatorDeclarationsStayUnextracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "ledger.hpp", `class Ledger {
public:
    Ledger& operator=(const Ledger& other);
    bool operator==(const Ledger& other) const;
    int Add(int amount) const;
};
`)
	if _, ok := got["method:Ledger.Add"]; !ok {
		t.Errorf("missing method:Ledger.Add: %#v", mapKeys(got))
	}
	for name := range got {
		if name == "method:Ledger.op" || name == "method:Ledger.operator=" || name == "method:Ledger.operator==" {
			t.Errorf("extracted an operator overload under a masked or colliding name %q: %#v", name, mapKeys(got))
		}
	}
}

// TestCPlusPlusMemberDeclarationScannerStaysInClassBodies pins the declarations
// that share a node type with an in-class method declaration and are not one.
// A local variable, a top-level prototype and a friend declaration are all
// `declaration` nodes; only the ones directly inside a class body are members.
func TestCPlusPlusMemberDeclarationScannerStaysInClassBodies(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "ledger.cpp", `int Peek(int x);

class Ledger {
public:
    int Add(int amount) const;
    friend int Peek(int x);
};

int Ledger::Add(int amount) const {
    Ledger other;
    int Shadow(int y);
    return amount + other.total_;
}
`)
	if _, ok := got["method:Ledger.Add"]; !ok {
		t.Errorf("missing method:Ledger.Add: %#v", mapKeys(got))
	}
	for _, unwanted := range []string{
		// Declared inside a function body, not a class body.
		"method:Ledger.other", "method:Ledger.Shadow", "method:Ledger.Add.Shadow",
		// A friend is not a member: it is granted access, not declared on the class.
		"method:Ledger.Peek",
		// A namespace-scope prototype has no container to be a member of.
		"method:Peek",
	} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("extracted %q, which is not an in-class method declaration: %#v", unwanted, mapKeys(got))
		}
	}
}

// TestCStructMembersAreNotMethods keeps the change to C++. C has no member
// functions: a callable-looking member of a C struct is a function pointer, and
// #174 classifies it as data.
func TestCStructMembersAreNotMethods(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "buffer.c", `struct Buffer {
    int (*on_flush)(int);
    int len;
};
`)
	if _, ok := got["field:Buffer.on_flush"]; !ok {
		t.Errorf("missing field:Buffer.on_flush: %#v", mapKeys(got))
	}
	if _, ok := got["method:Buffer.on_flush"]; ok {
		t.Errorf("a C struct member was extracted as a method: %#v", mapKeys(got))
	}
}

// TestCPlusPlusMemberFunctionTemplatesAreExtracted covers a declaration the
// class body does not hold directly. `template<class T> T Get();` parses as a
// declaration inside a template_declaration, and the template_declaration is
// what sits in the class body — so a scanner that required the class body to be
// the DIRECT parent dropped every member function template, which is most of
// the interface of a container or a traits class.
func TestCPlusPlusMemberFunctionTemplatesAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "box.hpp", `template<class T> T FreeGet();

class Box {
public:
    template<class U> U Get();
    template<class U> U* GetPtr();
    template<class U> U& GetRef();
    int Plain();
};
`)
	for _, want := range []string{"method:Box.Get", "method:Box.GetPtr", "method:Box.GetRef", "method:Box.Plain"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	// A namespace-scope function template is not a member of anything; stepping
	// through the template wrapper must not reach outside a class body.
	if _, ok := got["method:Box.FreeGet"]; ok {
		t.Errorf("namespace-scope template extracted as a member: %#v", mapKeys(got))
	}
}

// TestCPlusPlusCommaSeparatedMemberDeclarations covers a declaration that
// declares more than one method. C++ allows `void Start(), Stop();` and
// tree-sitter hangs both declarators off the one node; reading only the node's
// first `declarator` field kept `Start` and lost `Stop` entirely — the field
// pass deliberately ignores plain function declarators, so nothing else would
// have picked it up.
func TestCPlusPlusCommaSeparatedMemberDeclarations(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "runner.hpp", `class Runner {
public:
    void Start(), Stop();
    int a, b;
};
`)
	for _, want := range []string{"method:Runner.Start", "method:Runner.Stop", "field:Runner.a", "field:Runner.b"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	// Methods sharing a declaration must not share a fingerprint: rename
	// matching reads two symbols with one fingerprint as one symbol moving.
	start, stop := got["method:Runner.Start"], got["method:Runner.Stop"]
	if start.Fingerprint == stop.Fingerprint {
		t.Errorf("Start and Stop share fingerprint %q (signatures %q / %q)", start.Fingerprint, start.Signature, stop.Signature)
	}
}

// TestCPlusPlusDeeplyPointedReturnTypeIsExtracted covers the same fixed-budget
// trap on the other declarator walk. A pointer return type wraps the
// function_declarator once per `*`, so a method returning `int********` nests
// eight levels and a walk bounded by a picked number never reaches the
// function_declarator — the method disappears with no diagnostic. The parse
// tree is the bound.
func TestCPlusPlusDeeplyPointedReturnTypeIsExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "deep.hpp", `class Deep {
public:
    int *One();
    int ********Eight();
    int *********Nine();
};
`)
	for _, want := range []string{"method:Deep.One", "method:Deep.Eight", "method:Deep.Nine"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
}

// TestCPlusPlusMemberTemplateCarriesItsTemplateHead pins what a member function
// template reports. The class body holds the `template_declaration`, and the
// declaration inside it is only the part after `template<...>`, so reading the
// inner node left the head, its parameters and any constraints out of the
// signature, the body hash and the source range. Two overloads separated only
// by their template parameters then shared one fingerprint — which rename
// matching reads as one symbol — and an edit confined to the head produced no
// entity change at all, so nothing downstream saw the file move.
func TestCPlusPlusMemberTemplateCarriesItsTemplateHead(t *testing.T) {
	t.Parallel()
	one := memberIndex(t, "one.hpp", `class Ledger {
public:
    template<class T>
    T Get();
};
`)
	two := memberIndex(t, "two.hpp", `class Ledger {
public:
    template<class T, class U>
    T Get();
};
`)
	first, ok := one["method:Ledger.Get"]
	if !ok {
		t.Fatalf("missing method:Ledger.Get: %#v", mapKeys(one))
	}
	second, ok := two["method:Ledger.Get"]
	if !ok {
		t.Fatalf("missing method:Ledger.Get: %#v", mapKeys(two))
	}
	if !strings.Contains(first.Signature, "template<class T>") {
		t.Errorf("signature dropped the template head: %q", first.Signature)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Errorf("template heads %q and %q share fingerprint %s", first.Signature, second.Signature, first.Fingerprint)
	}
	if first.BodyHash == second.BodyHash {
		t.Errorf("template heads %q and %q share body hash %s", first.Signature, second.Signature, first.BodyHash)
	}
	// The head is on line 3 and the declaration on line 4; the symbol must span
	// the whole construct, or a reviewer following the range lands past it.
	if first.StartLine != 3 {
		t.Errorf("range starts at line %d, want the template head on line 3 (%d-%d)", first.StartLine, first.StartLine, first.EndLine)
	}
}

// TestCPlusPlusFunctionPointerReturningMemberIsAMethod separates the two C++
// member shapes that both put a declarator in parentheses.
// `int (*Factory())(double);` is a METHOD returning a function pointer: the
// name sits on a function_declarator nested inside the parens. It was rejected
// by the declaration pass and picked up by the field pass instead — whose
// descendant search jumps straight past that nested declarator — so a member
// function was reported as data. `int (*handler_)(int);` has the same outer
// shape, holds a bare identifier in its parens, and must stay a field.
func TestCPlusPlusFunctionPointerReturningMemberIsAMethod(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "factory.hpp", `class Ledger {
public:
    int (*Factory())(double);
    int (*handler_)(int);
    int (*table_[4])(int);
};
`)
	factory, ok := got["method:Ledger.Factory"]
	if !ok {
		t.Fatalf("missing method:Ledger.Factory: %#v", mapKeys(got))
	}
	if !strings.Contains(factory.Signature, "Factory") {
		t.Errorf("unexpected signature %q", factory.Signature)
	}
	if _, ok := got["field:Ledger.Factory"]; ok {
		t.Errorf("the same declaration is in the graph twice, as field and method: %#v", mapKeys(got))
	}
	// Function-POINTER data members keep their classification.
	for _, want := range []string{"field:Ledger.handler_", "field:Ledger.table_"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, mapKeys(got))
		}
	}
	for _, unwanted := range []string{"method:Ledger.handler_", "method:Ledger.table_"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("function-pointer member reclassified as a method: %q", unwanted)
		}
	}
}

// TestCPlusPlusReceiverCallResolvesToTheOutOfLineDefinition pins where a call on
// a typed C++ receiver lands. Extracting in-class declarations gave
// `methodsByContainer` a `Ledger.Add` entry, and that higher-confidence typed
// path then beat the unique-name fallback that used to find the real code — so
// `l.Add(3)` started resolving to the header's BODYLESS declaration instead of
// the definition in the .cpp. The implementation lost its callers, and impact
// on it reported nothing.
func TestCPlusPlusReceiverCallResolvesToTheOutOfLineDefinition(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "ledger.hpp", `#pragma once
class Ledger {
public:
    int Add(int amount) const;
};
`)
	writeFile(t, repo, "ledger.cpp", `#include "ledger.hpp"
int Ledger::Add(int amount) const { return amount + 1; }
`)
	writeFile(t, repo, "main.cpp", `#include "ledger.hpp"
int run() {
    Ledger* l = new Ledger();
    return l->Add(3);
}
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, symbol := range snapshot.Symbols {
		byID[symbol.ID] = symbol
	}
	var targets []string
	for _, relation := range snapshot.Relations {
		if relation.Type != "CALLS" {
			continue
		}
		if from := byID[relation.FromID]; from.Name != "run" {
			continue
		}
		to := byID[relation.ToID]
		targets = append(targets, fmt.Sprintf("%s:%s@%s:%d", to.Kind, to.Name, to.FilePath, to.StartLine))
	}
	want := "function:Add@ledger.cpp:2"
	if len(targets) != 1 || targets[0] != want {
		t.Errorf("run() calls %v, want exactly [%s] — the definition, not the header declaration", targets, want)
	}
}

func mapKeys(in map[string]Entity) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}
