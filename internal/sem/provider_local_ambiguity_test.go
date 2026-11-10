package sem

import (
	"strings"
	"testing"
)

// A nearest declaration is not C++ overload resolution: choose(1.5) calls
// choose(double) in both orders. Keep the existing single candidate, but never
// advertise proximity as proof, even when it happens to select the real callee.
func TestSameFileOverloadCandidateIsNotExact(t *testing.T) {
	for _, tc := range []struct {
		name, declarations, nearestSignature string
	}{
		{"wrong nearest overload", "constexpr int choose(double x) { return 2; }\nconstexpr int choose(int x) { return 1; }\n", "int x"},
		{"right nearest overload by coincidence", "constexpr int choose(int x) { return 1; }\nconstexpr int choose(double x) { return 2; }\n", "double x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "overload.cpp", tc.declarations+`constexpr int caller() { return choose(1.5); }
static_assert(caller() == 2);
constexpr int unique(double x) { return 3; }
constexpr int uniqueCaller() { return unique(1.5); }
`)
			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
			if err != nil {
				t.Fatal(err)
			}
			var nearest, unique SymbolRecord
			for _, symbol := range snapshot.Symbols {
				if symbol.FilePath == "overload.cpp" && symbol.StartLine == 2 && symbol.Name == "choose" {
					nearest = symbol
				}
				if symbol.Name == "unique" {
					unique = symbol
				}
			}
			if nearest.ID == "" || !strings.Contains(nearest.Signature, tc.nearestSignature) || unique.ID == "" {
				t.Fatalf("missing expected overload/control symbols: nearest=%#v unique=%#v", nearest, unique)
			}
			calls := runCallsFrom(snapshot, "caller")
			if len(calls) != 1 || calls[0].ToID != nearest.ID {
				t.Fatalf("candidate cardinality/identity changed: got %#v, want only %s", calls, nearest.ID)
			}
			assertAmbiguousLocalCall(t, calls[0], "file")
			uniqueCalls := runCallsFrom(snapshot, "uniqueCaller")
			if len(uniqueCalls) != 1 || uniqueCalls[0].ToID != unique.ID || uniqueCalls[0].Resolution != "exact" || uniqueCalls[0].Confidence != 0.92 || uniqueCalls[0].RelationScope != "file" {
				t.Errorf("unique same-file call lost proven metadata: %#v, want %s", uniqueCalls, unique.ID)
			}
		})
	}
}

// Out-of-line mapping changes the endpoint, not the certainty of the original
// name lookup. Its explanatory reason must not erase the ambiguity either.
func TestSameFileOverloadMappingPreservesAmbiguity(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "ledger.hpp", `class Ledger {
public:
    int Add(int amount);
    double Add(double amount);
    double Run() { return Add(1.5); }
};
`)
	writeFile(t, repo, "ledger.cpp", `#include "ledger.hpp"
int Ledger::Add(int amount) { return amount; }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	var definition SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.FilePath == "ledger.cpp" && symbol.Name == "Add" && !symbol.bodyless {
			definition = symbol
		}
	}
	calls := runCallsFrom(snapshot, "Ledger.Run")
	if definition.ID == "" || len(calls) != 1 || calls[0].ToID != definition.ID {
		t.Fatalf("out-of-line endpoint changed: got %#v, want %#v", calls, definition)
	}
	assertAmbiguousLocalCall(t, calls[0], "module")
	if !strings.Contains(calls[0].Reason, "out-of-line definition") {
		t.Errorf("mapping explanation missing: %q", calls[0].Reason)
	}
}

// Ambiguous forwarding must obey the existing name_only filter, while return
// flow may remain as a low-confidence candidate. Unique forwarding must survive.
func TestSameFileOverloadDataFlowDoesNotClaimCertainForwarding(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "flow.ts", `function choose(value: number): number;
function choose(value: string): number;
function choose(value: unknown): number { return 1; }
function caller(value: string): number { return choose(value); }
function unique(value: string): number { return 3; }
function uniqueCaller(value: string): number { return unique(value); }
`)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, symbol := range snapshot.Symbols {
		if symbol.Name != "choose" || symbol.StartLine == 3 {
			ids[symbol.Name] = symbol.ID
		}
	}
	for _, name := range []string{"caller", "choose", "uniqueCaller", "unique"} {
		if ids[name] == "" {
			t.Fatalf("missing %s symbol", name)
		}
	}
	returnFound, uniqueForwardFound := false, false
	for _, relation := range snapshot.Relations {
		if relation.Type != "DATA_FLOWS" {
			continue
		}
		switch {
		case relation.FromID == ids["caller"] && relation.ToID == ids["choose"]:
			t.Errorf("ambiguous caller-to-callee forwarding survived: %#v", relation)
		case relation.FromID == ids["choose"] && relation.ToID == ids["caller"]:
			returnFound = true
			if relation.Resolution != "name_only" || relation.Confidence > 0.62 || relation.RelationScope != "file" {
				t.Errorf("return flow lost uncertainty: %#v", relation)
			}
		case relation.FromID == ids["uniqueCaller"] && relation.ToID == ids["unique"]:
			uniqueForwardFound = true
			if relation.Resolution != "exact" {
				t.Errorf("unique forwarding downgraded: %#v", relation)
			}
		}
	}
	if !returnFound || !uniqueForwardFound {
		t.Errorf("missing return candidate or unique forward control: return=%v uniqueForward=%v", returnFound, uniqueForwardFound)
	}
}

func assertAmbiguousLocalCall(t *testing.T, call RelationRecord, scope string) {
	t.Helper()
	if call.Resolution != "name_only" || call.Confidence > 0.62 || call.Confidence <= 0 || call.RelationScope != scope {
		t.Errorf("nearest candidate is not honestly classified: %#v", call)
	}
	if !strings.Contains(call.Reason, "ambiguous") || !strings.Contains(call.Reason, "nearest") || !strings.Contains(call.Reason, "candidate") {
		t.Errorf("reason does not explain the guess: %q", call.Reason)
	}
	if shallowRelationRetained(call.Type, call.Resolution) {
		t.Errorf("fast-profile retention must reject the ambiguous candidate: %#v", call)
	}
}
