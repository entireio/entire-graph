package sem

import "testing"

// TestCPlusPlusDefaultConstructedReceiverResolvesCalls covers the most common
// way a C++ local is created. `Ledger ledger;` is default construction — no
// `new`, no `= Ledger()`, no initialiser of any kind — and it is the form the
// language's own style guides reach for. The generic local-variable scanner
// only understands assignment shapes (`x = new T(...)`, `x = T(...)`), so a
// declaration that carries its type and nothing else produced no inferred type
// at all, and every method call on that receiver resolved to nothing.
func TestCPlusPlusDefaultConstructedReceiverResolvesCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "ledger.cpp", `#include <string>

class Ledger {
public:
    int Add(int amount) const { return total_ + amount; }
    int Scale(int factor) const { return total_ * factor; }
    int Offset(int by) const { return total_ - by; }
    int Ref(int by) const { return total_ + by; }

private:
    int total_ = 0;
};

int LedgerDouble(int amount) {
    Ledger ledger;
    return ledger.Add(amount) * 2;
}

int LedgerScale(int factor) {
    Ledger ledger(factor);
    return ledger.Scale(factor);
}

int LedgerOffset(int by) {
    const Ledger ledger{by};
    return ledger.Offset(by);
}

int LedgerThroughPointer(Ledger* source, int by) {
    Ledger* ledger = source;
    return ledger->Ref(by);
}

int Ref(int by) { return by; }
`)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	// The resolution is asserted, not just the edge: `type_inferred` is the only
	// path that reads the receiver's type, so a bare-name fallback resolving the
	// same edge by luck cannot make this test pass.
	for _, want := range [][2]string{
		{"LedgerDouble", "Ledger.Add"},
		{"LedgerScale", "Ledger.Scale"},
		{"LedgerOffset", "Ledger.Offset"},
		{"LedgerThroughPointer", "Ledger.Ref"},
	} {
		if !hasRelationByLastSegmentWithResolution(snapshot.Relations, "CALLS", want[0], want[1], "type_inferred") {
			t.Fatalf("missing type-inferred C++ CALLS %s->%s: %#v", want[0], want[1], relationsOfType(snapshot.Relations, "CALLS"))
		}
	}
}

// TestCPlusPlusDeclaredLocalScannerStaysNarrow pins the shapes the declaration
// scanner must NOT read as a local declaration. Every one of them would type a
// name that is not a variable, and a wrong receiver type resolves calls to the
// wrong symbol — a worse outcome than the missing edge this scanner exists to
// supply.
func TestCPlusPlusDeclaredLocalScannerStaysNarrow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		block string
		want  map[string]string
	}{
		{"default construction", "Ledger ledger;", map[string]string{"ledger": "Ledger"}},
		{"direct initialisation", "Ledger ledger(seed);", map[string]string{"ledger": "Ledger"}},
		{"brace initialisation", "Ledger ledger{seed};", map[string]string{"ledger": "Ledger"}},
		{"const reference", "const Ledger& ledger = other;", map[string]string{"ledger": "Ledger"}},
		{"pointer", "Ledger *ledger = source;", map[string]string{"ledger": "Ledger"}},
		{"namespace qualified", "acct::Ledger ledger;", map[string]string{"ledger": "Ledger"}},
		{"two on one line", "Ledger a; Ledger b;", map[string]string{"a": "Ledger", "b": "Ledger"}},
		{"struct specifier", "struct Ledger ledger;", map[string]string{"ledger": "Ledger"}},
		// A template argument is not the declared type: the type token is the
		// container, and `Ledger` here is preceded by `<`.
		{"template argument", "std::vector<Ledger> ledgers;", map[string]string{}},
		// Parameters are introduced by `(`, never by a statement boundary. A
		// defaulted parameter is the case the statement anchor alone rejects:
		// its `=` is a declarator terminator, so widening the anchor to `(`
		// would read the parameter as a local.
		{"parameter list", "int f(Ledger ledger) { return 0; }", map[string]string{}},
		{"defaulted parameter", "int f(Ledger ledger = base) { return 0; }", map[string]string{}},
		// A catch clause opens with `(` for the same reason.
		{"catch clause", "try { g(); } catch (LedgerError err) { h(); }", map[string]string{}},
		// A wrapped argument ends at `,` or `)`, not at a declarator terminator.
		{"wrapped argument", "call(first,\n     Ledger second);", map[string]string{}},
		// A local struct definition declares no variable.
		{"local type definition", "struct Ledger { int total; };", map[string]string{}},
		// A lowercase type stays out: the capitalization guard is what keeps the
		// heuristic from reading arbitrary two-word statements as declarations.
		{"lowercase type", "unsigned long total;", map[string]string{}},
		// String and comment content is masked before scanning.
		{"inside a string", "log(\"Ledger ledger;\");", map[string]string{}},
		{"inside a comment", "// Ledger ledger;", map[string]string{}},
		// A name declared twice with two types is dropped rather than guessed.
		{"conflicting declarations", "Ledger x;\nif (c) { Account x; }", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cppLocalVarTypes(tc.block)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for name, typeName := range tc.want {
				if got[name] != typeName {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
