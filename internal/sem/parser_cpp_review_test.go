package sem

import (
	"strings"
	"testing"
)

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
