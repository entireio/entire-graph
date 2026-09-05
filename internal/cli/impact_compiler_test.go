package cli

import (
	"context"
	"reflect"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
	"github.com/entireio/entire-graph/internal/sem"
)

// Independent review-F2 fixture: the compiler redirects Handler's exact static
// call from Wrong to Orders. Route calls Handler; Candidate merely has a
// possible implementation relationship to Orders. Expected callers are derived
// from these three facts, never from another resolver's output.
func TestImpactCompilerEffectiveViewAllDepths(t *testing.T) {
	snapshot := impactFixtureSnapshot()
	snapshot.Symbols = append(snapshot.Symbols,
		sem.SymbolRecord{ID: "wrong", Name: "Wrong", Kind: "function", FilePath: "wrong.go", StartLine: 1},
		sem.SymbolRecord{ID: "candidate", Name: "Candidate", Kind: "function", FilePath: "candidate.go", StartLine: 1})
	snapshot.Relations[0] = sem.RelationRecord{FromID: "caller1", ToID: "wrong", Type: "CALLS", Confidence: 0.7, Evidence: []sem.Evidence{{FilePath: "web.go", StartLine: 6}}}
	snapshot.Header.Compiler = &sem.CompilerOverlay{Report: compiler.Report{Status: "complete"}, Calls: []sem.CompilerCallEvidence{
		{SourceSymbolID: "caller1", CallSiteLine: 6, Reconciliation: "disputed_static_at_site", StaticTargetIDs: []string{"wrong"}, Evidence: compiler.Evidence{Category: compiler.DirectDeclaration, TargetSymbolID: "orders", Caller: compiler.Site{Path: "web.go"}}},
		{SourceSymbolID: "candidate", Reconciliation: "candidate_only", Evidence: compiler.Evidence{Category: compiler.ImplementationCandidate, TargetSymbolID: "orders", Caller: compiler.Site{Path: "candidate.go"}}},
	}}
	original := append([]sem.RelationRecord(nil), snapshot.Relations...)
	for _, depth := range []int{1, 2, 3, 0} {
		flags := impactFlags{Symbol: "Orders", Depth: depth, Limit: 15, PathOptions: sem.DefaultImpactPathOptions()}
		flags.PathOptions.Depth = depth
		response, err := buildImpactOperationResponse(context.Background(), snapshot, flags, nil)
		if err != nil {
			t.Fatal(err)
		}
		expected := 2
		if depth == 1 {
			expected = 1
		}
		if response.Callers.Total != expected || response.Callers.Entries[0].Endpoint.ID != "caller1" {
			t.Fatalf("depth %d callers: %+v", depth, response.Callers)
		}
		for _, entry := range response.Callers.Entries {
			if entry.Endpoint.ID == "candidate" {
				t.Fatal("candidate became confirmed caller")
			}
		}
		if depth == 3 || depth == 0 {
			foundCandidate := false
			for _, result := range response.Traversal.Results {
				if result.ID == "candidate" {
					foundCandidate = true
					for _, path := range result.Paths {
						if path.Category != "compiler_candidate" {
							t.Fatal("candidate path became structural")
						}
					}
				}
			}
			if !foundCandidate {
				t.Fatal("deep view lost candidate")
			}
		}
		flags.Symbol = "Wrong"
		response, err = buildImpactOperationResponse(context.Background(), snapshot, flags, nil)
		if err != nil || response.Callers.Total != 0 {
			t.Fatalf("disputed call remains at depth %d: %+v %v", depth, response.Callers, err)
		}
		flags.Symbol = "Handler"
		response, err = buildImpactOperationResponse(context.Background(), snapshot, flags, nil)
		if err != nil || response.Callees.Total != 1 || response.Callees.Entries[0].Endpoint.ID != "orders" {
			t.Fatalf("callee view differs: %+v %v", response.Callees, err)
		}
	}
	if !reflect.DeepEqual(original, snapshot.Relations) {
		t.Fatal("query mutated native static relations")
	}
	snapshot.Header.Compiler = nil
	flags := impactFlags{Symbol: "Wrong", Depth: 2, Limit: 15}
	expected := buildImpactResponseFromReader(snapshot, flags, nil)
	actual, err := buildImpactOperationResponse(context.Background(), snapshot, flags, nil)
	if err != nil || !reflect.DeepEqual(expected, actual) || actual.Callers.Total != 2 {
		t.Fatalf("compiler-off changed: %+v %v", actual, err)
	}
}
