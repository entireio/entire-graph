package sem

import (
	"fmt"
	"testing"
)

// TestTF142R15OverrideRelationsStopsMidScan reproduces the finding at
// provider.go:4002: shouldStop was consulted once BEFORE overrideRelations was
// called, but not once inside it, so a large class hierarchy -- many
// EXTENDS/IMPLEMENTS edges, each sorting its subtype's whole method set --
// kept consuming CPU and memory past a deadline that had already expired,
// until the entire OVERRIDES slice was materialized and handed back to a
// caller that would then immediately discard it.
func TestTF142R15OverrideRelationsStopsMidScan(t *testing.T) {
	t.Parallel()
	const edges = 3000
	relations := make([]RelationRecord, edges)
	methodsByContainer := make(map[string]map[string]SymbolRecord, edges*2)
	for i := range edges {
		sub := fmt.Sprintf("sub%d", i)
		super := fmt.Sprintf("super%d", i)
		relations[i] = RelationRecord{
			RecordType: "relation", FromID: sub, ToID: super,
			Type: "EXTENDS", TargetKind: "symbol",
		}
		methodsByContainer[sub] = map[string]SymbolRecord{
			"run": {ID: sub + ":run", Name: "run"},
		}
		methodsByContainer[super] = map[string]SymbolRecord{
			"run": {ID: super + ":run", Name: "run"},
		}
	}

	// Control: unbudgeted, every edge with a resolved override produces one
	// OVERRIDES relation.
	unbudgeted := overrideRelations(relations, methodsByContainer, nil)
	if len(unbudgeted) != edges {
		t.Fatalf("fixture must produce one OVERRIDES relation per edge: got %d, want %d", len(unbudgeted), edges)
	}

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	stopped := overrideRelations(relations, methodsByContainer, stop)
	if visited > budgetPollStride*2 {
		t.Fatalf("overrideRelations polled shouldStop only %d times before stopping ('never' if this is 0): "+
			"it must poll inside its own loop, not just rely on the caller's check before the call", visited)
	}
	if len(stopped) >= len(unbudgeted) {
		t.Fatalf("a stopped scan must not complete: %d relations stopped vs %d unbudgeted", len(stopped), len(unbudgeted))
	}
}

// TestTF142R15OverrideRelationsUnbudgetedAreUnchanged pins the control: a nil
// stop predicate must not alter the result relative to the pre-fix signature.
func TestTF142R15OverrideRelationsUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	relations := []RelationRecord{
		{RecordType: "relation", FromID: "Dog", ToID: "Animal", Type: "EXTENDS", TargetKind: "symbol"},
	}
	methodsByContainer := map[string]map[string]SymbolRecord{
		"Dog":    {"speak": {ID: "Dog:speak", Name: "speak"}},
		"Animal": {"speak": {ID: "Animal:speak", Name: "speak"}},
	}
	got := overrideRelations(relations, methodsByContainer, nil)
	if len(got) != 1 || got[0].Type != "OVERRIDES" || got[0].FromID != "Dog:speak" || got[0].ToID != "Animal:speak" {
		t.Fatalf("overrideRelations(nil stop) = %#v, want one OVERRIDES Dog:speak -> Animal:speak", got)
	}
}
