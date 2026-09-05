package sem

import (
	"strings"
	"testing"
)

func TestCPlusPlusConstrainedDeclarationTypeRelations(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "factory.hpp", `struct Input {};
struct Product {};
template<class T> constexpr bool Pred() { return true; }
class Factory {
public:
 template<class T> requires (Pred<T>()) Product Make(Input value);
};`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "review")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	var method SymbolRecord
	for _, s := range snapshot.Symbols {
		byID[s.ID] = s
		if s.Name == "Make" {
			method = s
		}
	}
	if !method.signatureTypesKnown || method.paramTypeText != "Input" || method.returnTypeText != "Product" {
		t.Fatalf("missing AST types: %#v", method)
	}
	found := map[string]bool{}
	for _, r := range snapshot.Relations {
		if r.FromID == method.ID {
			found[r.Type+":"+byID[r.ToID].Name] = true
		}
	}
	for _, key := range []string{"PARAM_TYPE:Input", "RETURNS_TYPE:Product"} {
		if !found[key] {
			t.Errorf("missing %s: %v", key, found)
		}
	}
}

func TestCPlusPlusSharedDeclarationSpecifiers(t *testing.T) {
	for _, specifier := range []string{"virtual", "static", "constexpr"} {
		t.Run(specifier, func(t *testing.T) {
			before := memberIndex(t, "a.hpp", `class A { public: int f(), g(); };`)
			after := memberIndex(t, "a.hpp", `class A { public: `+specifier+` int f(), g(); };`)
			for _, key := range []string{"method:A.f", "method:A.g"} {
				a, b := before[key], after[key]
				if !strings.Contains(b.Signature, specifier) || a.BodyHash == b.BodyHash || a.Fingerprint == b.Fingerprint {
					t.Errorf("%s API edit lost: before=%#v after=%#v", key, a, b)
				}
			}
		})
	}
}

func TestCPlusPlusDefinitionUsesASTNameAndNamespace(t *testing.T) {
	for _, tc := range []struct {
		name, definition, target string
	}{
		{"namespace block", `namespace acct { int Ledger::Add(int amount) { return amount; } }`, "definition.cpp"},
		{"fully qualified", `int acct::Ledger::Add(int amount) { return amount; }`, "definition.cpp"},
		{"parameter mention", `int Add(decltype(&acct::Ledger::Add) member) { return 7; }`, "ledger.hpp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "ledger.hpp", `namespace acct { class Ledger { public: int Add(int amount); }; }`)
			writeFile(t, repo, "definition.cpp", "#include \"ledger.hpp\"\n"+tc.definition)
			writeFile(t, repo, "main.cpp", "#include \"ledger.hpp\"\nint run() { acct::Ledger ledger; return ledger.Add(3); }")
			snapshot, err := BuildProviderSnapshot(t.Context(), repo, "review")
			if err != nil {
				t.Fatal(err)
			}
			byID := map[string]SymbolRecord{}
			for _, symbol := range snapshot.Symbols {
				byID[symbol.ID] = symbol
			}
			var targets []string
			for _, relation := range snapshot.Relations {
				if relation.Type == "CALLS" && byID[relation.FromID].Name == "run" {
					targets = append(targets, byID[relation.ToID].FilePath)
				}
			}
			if len(targets) != 1 || targets[0] != tc.target {
				t.Fatalf("run targets %v, want %s", targets, tc.target)
			}
		})
	}
}

func TestCPlusPlusInheritedDefinitionKeepsConfidence(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "base.hpp", `class Base { public: int Fetch(); };
class Derived : public Base {};`)
	writeFile(t, repo, "base.cpp", "#include \"base.hpp\"\nint Base::Fetch() { return 1; }")
	writeFile(t, repo, "main.cpp", "#include \"base.hpp\"\nint run() { Derived d; return d.Fetch(); }")
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "review")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	byName := map[string][]SymbolRecord{}
	var from, base, derived, declaration SymbolRecord
	for _, s := range snapshot.Symbols {
		byID[s.ID] = s
		byName[s.Name] = append(byName[s.Name], s)
		switch s.Name {
		case "run":
			from = s
		case "Base":
			base = s
		case "Derived":
			derived = s
		case "Fetch":
			if s.bodyless {
				declaration = s
			}
		}
	}
	// Exercise receiver resolution with a known inheritance map. C++ base
	// extraction is separate from the redirection behavior under test.
	relations := receiverCallRelations(from, "int run() { Derived d; return d.Fetch(); }",
		map[string]map[string]SymbolRecord{base.ID: {"Fetch": declaration}},
		map[string]string{derived.ID: base.ID}, nil, byName, nil, nil, nil, "", nil, nil, nil, nil, nil, swiftFileTypes{})
	for _, r := range relations {
		if r.Type == "CALLS" && byID[r.FromID].Name == "run" && byID[r.ToID].Name == "Fetch" {
			if byID[r.ToID].FilePath != "base.cpp" || r.Confidence > 0.82 || !strings.Contains(r.Reason, "inherited") {
				t.Fatalf("lost inherited resolution metadata: %#v target=%#v", r, byID[r.ToID])
			}
			return
		}
	}
	t.Fatal("missing inherited call")
}
