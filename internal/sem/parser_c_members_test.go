package sem

import "testing"

func memberIndex(t *testing.T, path, content string) map[string]Entity {
	t.Helper()
	entities, _ := TreeSitterParser{}.Parse(path, content)
	out := make(map[string]Entity, len(entities))
	for _, entity := range entities {
		out[entity.Kind+":"+entity.Name] = entity
	}
	return out
}

// TestCStructFieldsAreExtracted covers data members of C structs, which were
// skipped outright. A C struct is the language's only aggregate type, so
// dropping its members left every C type an opaque name: no field symbol to
// search for, no CONTAINS edge, and no target for a field read.
func TestCStructFieldsAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "buffer.c", `struct Buffer {
    char *data;
    size_t len;
    char name[32];
    int (*on_flush)(int);
};
`)
	for _, want := range []string{"field:Buffer.data", "field:Buffer.len", "field:Buffer.name", "field:Buffer.on_flush"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, got)
		}
	}
}

// TestCPlusPlusClassFieldsAreExtracted covers the same for C++ classes and
// structs, including a member with an initialiser.
func TestCPlusPlusClassFieldsAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "ledger.hpp", `class Ledger {
public:
    int Add(int amount) const;

private:
    int total_ = 0;
    int plain_;
    std::string name_;
};
`)
	for _, want := range []string{"field:Ledger.total_", "field:Ledger.plain_", "field:Ledger.name_"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, got)
		}
	}
	// An in-class method DECLARATION is also a field_declaration node in
	// tree-sitter-cpp. It is a method, not data, and must not be extracted as a
	// field by the member pass.
	if _, ok := got["field:Ledger.Add"]; ok {
		t.Errorf("method declaration Add was extracted as a field: %#v", got)
	}
}

// TestCPlusPlusInitialisedMemberIsNotAMethod pins a tree-sitter-cpp quirk:
// `int total_ = 0;` inside a class body parses as a function_definition whose
// body is a pure_virtual_clause, because `= 0` is also the pure-virtual marker.
// Taken at face value it produced a METHOD named after a data member, with the
// member's declaration as its signature — which then joined the class's method
// inventory and competed for receiver call resolution.
func TestCPlusPlusInitialisedMemberIsNotAMethod(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "ledger.cpp", `class Ledger {
public:
    virtual int Total() const = 0;

private:
    int total_ = 0;
};
`)
	if _, ok := got["method:Ledger.total_"]; ok {
		t.Errorf("data member total_ was extracted as a method: %#v", got)
	}
	if _, ok := got["field:Ledger.total_"]; !ok {
		t.Errorf("data member total_ was not extracted as a field: %#v", got)
	}
	// A genuine pure-virtual method has a function_declarator and must keep its
	// method kind.
	if _, ok := got["method:Ledger.Total"]; !ok {
		t.Errorf("pure virtual method Total should stay a method: %#v", got)
	}
}

// TestCFamilyReferenceMembersAreExtracted covers members whose declarator is a
// reference: `int &value;` and `Widget &&item;`. The declarator chain unwrapper
// already knew how to reach through a reference_declarator, but the member pass
// never dispatched to it, so reference members — the ordinary way a C++ class
// holds a non-owning collaborator — stayed missing from the symbol graph.
func TestCFamilyReferenceMembersAreExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "window.hpp", `class Window {
    int &value;
    Widget &&item;
    int plain;
};
`)
	for _, want := range []string{"field:Window.value", "field:Window.item", "field:Window.plain"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, got)
		}
	}
}

// TestCFamilyInlineTypeDefinitionSurvivesMemberExtraction pins that extracting
// a member does not swallow a type DEFINED in the same declaration. The member
// pass stops the walk at the declaration, so `struct Inner { int value; }
// inner;` lost the `Inner` symbol that the walk used to reach by falling
// through — a type disappearing is a worse regression than the member was a
// fix.
func TestCFamilyInlineTypeDefinitionSurvivesMemberExtraction(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ name, path, source string }{
		{"C", "outer.c", `struct Outer {
    struct Inner { int value; } inner;
    int other;
};
`},
		{"C++", "outer.cpp", `class Outer {
    class Inner { public: int value; } inner;
    int other;
};
`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := memberIndex(t, testCase.path, testCase.source)
			for _, want := range []string{"field:Outer.inner", "field:Outer.other", "field:Inner.value"} {
				if _, ok := got[want]; !ok {
					t.Errorf("missing %q: %#v", want, got)
				}
			}
			if _, structOK := got["struct:Inner"]; !structOK {
				if _, classOK := got["class:Inner"]; !classOK {
					t.Errorf("inline type Inner lost its symbol: %#v", got)
				}
			}
		})
	}
}

// TestCFamilyAnonymousInlineAggregateIsNotHoisted is the other half of the
// rule: an anonymous aggregate declares no type, and its members are reached
// through the member that holds it (`outer.u.i`), so they must not be filed as
// members of the enclosing type.
func TestCFamilyAnonymousInlineAggregateIsNotHoisted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "packet.c", `struct Packet {
    union { int i; float f; } value;
};
`)
	if _, ok := got["field:Packet.value"]; !ok {
		t.Errorf("missing field:Packet.value: %#v", got)
	}
	for _, unwanted := range []string{"field:Packet.i", "field:Packet.f"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("anonymous union member hoisted as %q: %#v", unwanted, got)
		}
	}
}

// TestCFamilyDeeplyNestedDeclaratorIsExtracted covers a declarator that nests
// deeper than any fixed step budget. tree-sitter models each `*` as its own
// pointer_declarator, so `int ********deep;` is eight levels; a walk bounded by
// a picked number stops one short and drops the member with no diagnostic. The
// parse tree is the bound: every step narrows the span, so the walk terminates
// on its own.
func TestCFamilyDeeplyNestedDeclaratorIsExtracted(t *testing.T) {
	t.Parallel()
	got := memberIndex(t, "deep.c", `struct Deep {
    int *one;
    int ********eight;
    int *********nine;
};
`)
	for _, want := range []string{"field:Deep.one", "field:Deep.eight", "field:Deep.nine"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: %#v", want, got)
		}
	}
}
