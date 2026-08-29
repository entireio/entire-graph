package sem

import "testing"

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

func mapKeys(in map[string]Entity) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}
