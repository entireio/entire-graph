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
