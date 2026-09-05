package sem

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func pathFixtureEdge(from, to, kind string, confidence float64) RelationRecord {
	return RelationRecord{FromID: from, ToID: to, Type: kind, Confidence: confidence, Resolution: "resolved", Evidence: []Evidence{{Kind: "fixture", FilePath: from + ".go", StartLine: 1}}}
}

func TestImpactPathsChainDiamondCycleAndShuffle(t *testing.T) {
	edges := []RelationRecord{pathFixtureEdge("b", "a", "CALLS", .7), pathFixtureEdge("c", "b", "CALLS", .9), pathFixtureEdge("d", "c", "CALLS", .9), pathFixtureEdge("e", "d", "CALLS", .9), pathFixtureEdge("a", "e", "CALLS", 1), pathFixtureEdge("x", "a", "CALLS", 1), pathFixtureEdge("d", "x", "CALLS", 1)}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	expected, err := TraverseImpactPaths(t.Context(), "a", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Truncated || len(expected.Results) != 5 {
		t.Fatalf("unexpected closure: %+v", expected)
	}
	for _, result := range expected.Results {
		if result.ID == "d" {
			if len(result.Paths) != 2 || result.Paths[0].WeakestConfidence != 1 || len(result.Paths[0].Steps) != 2 {
				t.Fatalf("lost strongest path: %+v", result)
			}
		}
	}
	for seed := int64(0); seed < 20; seed++ {
		shuffled := append([]RelationRecord(nil), edges...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, err := TraverseImpactPaths(t.Context(), "a", shuffled, options)
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("order changed: %v", err)
		}
	}
}

func TestImpactPathsPolicyAndTerminalTests(t *testing.T) {
	edges := []RelationRecord{pathFixtureEdge("test", "a", "TESTS", 1), pathFixtureEdge("test", "a", "CALLS", 1), pathFixtureEdge("unrelated", "test", "CALLS", 1), pathFixtureEdge("container", "a", "CONTAINS", 1), pathFixtureEdge("other", "a", "FILE_CHANGES_WITH", 1), pathFixtureEdge("consumer", "a", "PARAM_TYPE", 1)}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	report, err := TraverseImpactPaths(t.Context(), "a", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 || report.Results[0].ID != "consumer" || report.Results[1].ID != "test" {
		t.Fatalf("invalid propagation: %+v", report)
	}
}

func TestImpactPathsBoundsAndCancellation(t *testing.T) {
	edges := []RelationRecord{pathFixtureEdge("b", "a", "CALLS", 1), pathFixtureEdge("c", "b", "CALLS", 1), pathFixtureEdge("d", "c", "CALLS", 1)}
	for _, code := range []string{"depth_bound", "node_bound", "edge_bound", "frontier_bound", "incomplete_source_graph"} {
		options := DefaultImpactPathOptions()
		options.Depth = 0
		switch code {
		case "depth_bound":
			options.Depth = 1
		case "node_bound":
			options.MaxNodes = 1
		case "edge_bound":
			options.MaxEdges = 1
		case "frontier_bound":
			options.MaxFrontier = 1
		case "incomplete_source_graph":
			options.GraphPartial = true
		}
		report, err := TraverseImpactPaths(t.Context(), "a", edges, options)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Truncated || !report.CountsLowerBounds || !reflect.DeepEqual(report.StopReasons, []string{code}) {
			t.Fatalf("missing %s: %+v", code, report)
		}
		for _, result := range report.Results {
			for _, path := range result.Paths {
				for _, step := range path.Steps {
					found := false
					for _, edge := range edges {
						if edge.FromID == step.FromID && edge.ToID == step.ToID && edge.Type == step.Relation && reflect.DeepEqual(edge.Evidence, step.Evidence) {
							found = true
						}
					}
					if !found {
						t.Fatal("invented path")
					}
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := TraverseImpactPaths(ctx, "a", edges, DefaultImpactPathOptions()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestImpactPathsRetainsLongerStrongerPath(t *testing.T) {
	edges := []RelationRecord{pathFixtureEdge("z", "a", "CALLS", .3), pathFixtureEdge("b", "a", "CALLS", 1), pathFixtureEdge("c", "b", "CALLS", 1), pathFixtureEdge("z", "c", "CALLS", 1)}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	options.MaxPaths = 1
	report, err := TraverseImpactPaths(t.Context(), "a", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if result.ID == "z" && (result.Paths[0].WeakestConfidence != 1 || len(result.Paths[0].Steps) != 3) {
			t.Fatalf("short weak path displaced stronger explanation: %+v", result)
		}
	}
}

// Independent source fixtures verify the real extractor direction before the
// pure path algorithm is trusted; expected A<-B<-C<-D<-E is hand-derived.
func TestImpactPathsFromIndependentSourceFixtures(t *testing.T) {
	fixtures := map[string]string{
		"chain.go": "package fixture\nfunc A() {}\nfunc B() { A() }\nfunc C() { B() }\nfunc D() { C() }\nfunc E() { D() }\n",
		"chain.ts": "function A() {}\nfunction B() { A(); }\nfunction C() { B(); }\nfunction D() { C(); }\nfunction E() { D(); }\n",
		"chain.py": "def A():\n    pass\ndef B():\n    A()\ndef C():\n    B()\ndef D():\n    C()\ndef E():\n    D()\n",
	}
	for path, source := range fixtures {
		t.Run(path, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, path, source)
			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "fixture", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{}
			for _, symbol := range snapshot.Symbols {
				ids[symbol.Name] = symbol.ID
			}
			for _, name := range []string{"A", "B", "C", "D", "E"} {
				if ids[name] == "" {
					t.Fatalf("missing %s declaration", name)
				}
			}
			options := DefaultImpactPathOptions()
			options.Depth = 0
			report, err := TraverseImpactPaths(t.Context(), ids["A"], snapshot.Relations, options)
			if err != nil {
				t.Fatal(err)
			}
			reached := map[string]int{}
			for _, result := range report.Results {
				reached[result.ID] = len(result.Paths[0].Steps)
			}
			for i, name := range []string{"B", "C", "D", "E"} {
				if reached[ids[name]] != i+1 {
					t.Fatalf("%s depth=%d, expected %d", name, reached[ids[name]], i+1)
				}
			}
		})
	}
}

func TestImpactPathsHighDegreeDeterministicBudget(t *testing.T) {
	var edges []RelationRecord
	for i := 0; i < 200; i++ {
		edges = append(edges, pathFixtureEdge(fmt.Sprintf("node%03d", i), "focus", "CALLS", 1))
	}
	options := DefaultImpactPathOptions()
	options.MaxEdges = 7
	options.Depth = 0
	expected, err := TraverseImpactPaths(t.Context(), "focus", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	if expected.ExaminedEdges != 7 || len(expected.Results) != 7 || !expected.Truncated {
		t.Fatalf("unbounded hub: %+v", expected)
	}
	for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
		edges[i], edges[j] = edges[j], edges[i]
	}
	actual, err := TraverseImpactPaths(t.Context(), "focus", edges, options)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("budget result depends on input order")
	}
}

func TestImpactPathsDistinctByteEvidence(t *testing.T) {
	first := pathFixtureEdge("b", "a", "CALLS", 1)
	second := first
	first.Evidence = []Evidence{{Kind: "fixture", FilePath: "\xff.go"}}
	second.Evidence = []Evidence{{Kind: "fixture", FilePath: "\xfe.go"}}
	options := DefaultImpactPathOptions()
	a, err := TraverseImpactPaths(t.Context(), "a", []RelationRecord{first, second}, options)
	if err != nil {
		t.Fatal(err)
	}
	b, err := TraverseImpactPaths(t.Context(), "a", []RelationRecord{second, first}, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || len(a.Results) != 1 || len(a.Results[0].Paths) != 2 {
		t.Fatal("distinct byte evidence collapsed or reordered")
	}
}
