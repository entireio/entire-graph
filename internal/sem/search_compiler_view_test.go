package sem

import (
	"reflect"
	"slices"
	"testing"

	"github.com/entireio/entire-graph/internal/compiler"
)

// Independently authored from review F2: use the actual search pipeline with a
// deterministic adapter result. Only compiler evidence differs between runs.
func TestSearchCompilerEffectiveRelationsAcrossQueryStages(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "caller.go", "package p\nfunc BeaconStart() {}\n")
	write(t, repo, "target.go", "package p\nfunc BeaconTarget() {}\n")
	write(t, repo, "wrong.go", "package p\nfunc BeaconWrong() {}\n")
	write(t, repo, "candidate.go", "package p\nfunc BeaconCandidate() {}\n")
	for _, ranking := range []string{"current", "experimental-graph"} {
		t.Run(ranking, func(t *testing.T) {
			var original []RelationRecord
			options := SearchOptions{Worktree: true, IndexAllFiles: true, DisableCache: true, Profile: ProfileFull, TopK: 10, MaxContextBytes: 16000, Ranking: ranking}
			install := func(enriched bool) func(*ProviderSnapshot) {
				return func(snapshot *ProviderSnapshot) {
					ids := map[string]string{}
					for _, s := range snapshot.Symbols {
						ids[s.Name] = s.ID
					}
					snapshot.Relations = []RelationRecord{{FromID: ids["BeaconStart"], ToID: ids["BeaconWrong"], Type: "CALLS", Confidence: 1, Evidence: []Evidence{{FilePath: "caller.go", StartLine: 2}}}}
					original = append([]RelationRecord(nil), snapshot.Relations...)
					if enriched {
						snapshot.Header.Compiler = &CompilerOverlay{Report: compiler.Report{Status: "complete"}, Calls: []CompilerCallEvidence{
							{SourceSymbolID: ids["BeaconStart"], CallSiteLine: 2, StaticTargetIDs: []string{ids["BeaconWrong"]}, Reconciliation: "disputed_static_at_site", Evidence: compiler.Evidence{Category: compiler.DirectDeclaration, TargetSymbolID: ids["BeaconTarget"], Caller: compiler.Site{Path: "caller.go"}}},
							{SourceSymbolID: ids["BeaconStart"], Reconciliation: "candidate_only", Evidence: compiler.Evidence{Category: compiler.ImplementationCandidate, TargetSymbolID: ids["BeaconCandidate"]}},
						}}
					}
				}
			}
			options.afterSnapshotBuild = install(false)
			off, err := SearchRepository(t.Context(), repo, "fixture", "beacon", options)
			if err != nil {
				t.Fatal(err)
			}
			options.afterSnapshotBuild = install(true)
			on, err := SearchRepository(t.Context(), repo, "fixture", "beacon", options)
			if err != nil {
				t.Fatal(err)
			}
			boosted := func(response SearchResponse) []string {
				var names []string
				for _, r := range response.Results {
					if slices.Contains(r.Signals, "graph:callers") {
						names = append(names, r.SymbolName)
					}
				}
				return names
			}
			if !reflect.DeepEqual(boosted(off), []string{"BeaconWrong"}) || !reflect.DeepEqual(boosted(on), []string{"BeaconTarget"}) {
				t.Fatalf("caller boost view off=%v on=%v", boosted(off), boosted(on))
			}
			if len(original) != 1 {
				t.Fatal("fixture lost static relation")
			}
			// A query matching only the caller must expand using the same direct view.
			for _, query := range []string{"BeaconStart"} {
				result, err := SearchRepository(t.Context(), repo, "fixture", query, options)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, r := range result.Results {
					if r.SymbolName == "BeaconTarget" && slices.Contains(r.Signals, "graph:outgoing") {
						found = true
					}
					if (r.SymbolName == "BeaconWrong" || r.SymbolName == "BeaconCandidate") && slices.Contains(r.Signals, "graph:outgoing") {
						t.Fatalf("non-call expanded: %+v", r)
					}
				}
				if !found {
					t.Fatalf("confirmed target not expanded: %+v", result.Results)
				}
			}
		})
	}
}
