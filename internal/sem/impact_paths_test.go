package sem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestImpactPathsOutputStorageBound(t *testing.T) {
	var edges []RelationRecord
	for i := 1; i <= 300; i++ {
		edges = append(edges, pathFixtureEdge(fmt.Sprintf("n%03d", i), fmt.Sprintf("n%03d", i-1), "CALLS", 1))
	}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	options.MaxOutputSteps = 10
	report, err := TraverseImpactPaths(t.Context(), "n000", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, result := range report.Results {
		for _, path := range result.Paths {
			steps += len(path.Steps)
		}
	}
	if steps > 10 || report.VisitedNodes != 301 || !reflect.DeepEqual(report.StopReasons, []string{"output_path_bound"}) {
		t.Fatalf("output is not independently bounded: steps=%d nodes=%d reasons=%v", steps, report.VisitedNodes, report.StopReasons)
	}
}

func TestImpactIdentityCompositions(t *testing.T) {
	relations := []RelationRecord{
		{FromID: "writer", ToID: "field-one", Type: "WRITES_FIELD", Confidence: 1},
		{FromID: "reader", ToID: "field-one", Type: "READS_FIELD", Confidence: 1},
		{FromID: "unrelated", ToID: "field-two", Type: "READS_FIELD", Confidence: 1},
		{FromID: "handler", ToID: "external:route:/one", Type: "HANDLES_ROUTE", Confidence: .7},
		{FromID: "client", ToID: "external:route:/one", Type: "HTTP_CALLS", Confidence: .7},
		{FromID: "wrong-client", ToID: "external:route:/two", Type: "HTTP_CALLS", Confidence: .7},
		{FromID: "emitter", ToID: "external:channel:one", Type: "EMITS", Confidence: .7},
		{FromID: "listener", ToID: "external:channel:one", Type: "LISTENS_ON", Confidence: .7},
		{FromID: "wrong-listener", ToID: "external:channel:two", Type: "LISTENS_ON", Confidence: .7},
		{FromID: "producer", ToID: "consumer", Type: "DATA_FLOWS", Confidence: .8},
		{FromID: "consumer", ToID: "unrelated-value", Type: "DATA_FLOWS", Confidence: .8},
	}
	for _, test := range []struct{ focus, want, forbidden string }{{"writer", "reader", "unrelated"}, {"handler", "client", "wrong-client"}, {"emitter", "listener", "wrong-listener"}, {"producer", "consumer", "unrelated-value"}} {
		options := DefaultImpactPathOptions()
		options.Depth = 0
		report, err := TraverseImpactPaths(context.Background(), test.focus, relations, options)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, result := range report.Results {
			if result.ID == test.forbidden {
				t.Fatalf("crossed mismatched identity %s", test.forbidden)
			}
			if result.ID == test.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing composition %s -> %s: %#v", test.focus, test.want, report)
		}
	}
}

func TestImpactPathsConstructionAndEvidenceBounds(t *testing.T) {
	edges := []RelationRecord{pathFixtureEdge("b", "a", "CALLS", 1), pathFixtureEdge("c", "a", "CALLS", 1)}
	for _, code := range []string{"adjacency_bound", "evidence_bound"} {
		options := DefaultImpactPathOptions()
		if code == "adjacency_bound" {
			options.MaxInputEdges = 1
		} else {
			options.MaxEvidenceBytes = 1
		}
		a, err := TraverseImpactPaths(t.Context(), "a", edges, options)
		if err != nil {
			t.Fatal(err)
		}
		b, err := TraverseImpactPaths(t.Context(), "a", []RelationRecord{edges[1], edges[0]}, options)
		if err != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("construction refusal depends on input order")
		}
		if len(a.Results) != 0 || !a.CountsLowerBounds || !reflect.DeepEqual(a.StopReasons, []string{code}) {
			t.Fatalf("invalid bounded construction: %+v", a)
		}
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	report, err := TraverseImpactPaths(ctx, "a", edges, DefaultImpactPathOptions())
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(report.StopReasons, []string{"timeout"}) {
		t.Fatalf("deadline: %+v %v", report, err)
	}
}

func TestImpactPathsCompilerCandidatesRemainSeparate(t *testing.T) {
	edges := []RelationRecord{
		pathFixtureEdge("caller", "focus", "CALLS", 1),
		pathFixtureEdge("caller", "focus", "X-entire-graph:COMPILER_IMPLEMENTATION_CANDIDATE", 0),
		pathFixtureEdge("upstream", "caller", "CALLS", 1),
	}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	options.MaxPaths = 1
	report, err := TraverseImpactPaths(t.Context(), "focus", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("missing dependents: %+v", report)
	}
	for _, result := range report.Results {
		if len(result.Paths) != 2 || result.Paths[0].Category != "structural" || result.Paths[1].Category != "compiler_candidate" || result.Paths[1].WeakestConfidence != 0 {
			t.Fatalf("candidate evidence conflated: %+v", result)
		}
	}
	options.MinConfidence = .1
	report, err = TraverseImpactPaths(t.Context(), "focus", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if len(result.Paths) != 1 || result.Paths[0].Category != "structural" {
			t.Fatalf("filter applied after traversal: %+v", result)
		}
	}
}

func TestImpactPathsDuplicateFactsDoNotConsumeWork(t *testing.T) {
	edge := pathFixtureEdge("b", "a", "CALLS", 1)
	options := DefaultImpactPathOptions()
	options.MaxEdges = 1
	report, err := TraverseImpactPaths(t.Context(), "a", []RelationRecord{edge, edge, edge}, options)
	if err != nil || report.Truncated || report.ExaminedEdges != 1 || len(report.Results) != 1 {
		t.Fatalf("duplicates consume traversal budget: %+v %v", report, err)
	}
}

func TestImpactPathsSourceRouteAndResourceDirections(t *testing.T) {
	for _, fixture := range []struct{ path, body, focus, required string }{
		{"network.go", "package fixture\nimport \"net/http\"\nfunc Serve(w http.ResponseWriter, r *http.Request) {}\nfunc Register() { http.HandleFunc(\"/independent-impact\", Serve) }\nfunc Client() { http.Get(\"http://localhost/independent-impact\") }\n", "Serve", "Client"},
		{"main.tf", "resource \"local_file\" \"origin\" {\n content = \"one\"\n filename = \"origin\"\n}\nresource \"local_file\" \"dependent\" {\n content = local_file.origin.content\n filename = \"dependent\"\n}\n", "resource.local_file.origin", "resource.local_file.dependent"},
	} {
		t.Run(fixture.path, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, fixture.path, fixture.body)
			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "independent-impact", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]string{}
			for _, symbol := range snapshot.Symbols {
				ids[symbol.Name] = symbol.ID
				ids[symbol.QualifiedName] = symbol.ID
				ids[lastSegment(symbol.ID)] = symbol.ID
			}
			if ids[fixture.focus] == "" || ids[fixture.required] == "" {
				t.Fatalf("missing fixture names: %v", ids)
			}
			options := DefaultImpactPathOptions()
			options.Depth = 0
			report, err := TraverseImpactPaths(t.Context(), ids[fixture.focus], snapshot.Relations, options)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, result := range report.Results {
				if result.ID == ids[fixture.required] {
					found = true
				}
			}
			if !found {
				t.Fatalf("required source dependency missing: %+v", report)
			}
		})
	}
}

func TestImpactPathsEveryProofIsContiguousOriginalEvidence(t *testing.T) {
	random := rand.New(rand.NewSource(30905))
	var edges []RelationRecord
	for i := 0; i < 500; i++ {
		edges = append(edges, pathFixtureEdge(fmt.Sprintf("n%03d", random.Intn(100)), fmt.Sprintf("n%03d", random.Intn(100)), "CALLS", .7))
	}
	facts := map[string]bool{}
	for _, edge := range edges {
		facts[impactStepKey(ImpactPathStep{FromID: edge.FromID, ToID: edge.ToID, Relation: edge.Type, Direction: "in", Resolution: edge.Resolution, Confidence: edge.Confidence, Evidence: edge.Evidence})] = true
	}
	for _, limit := range []int{1, 7, 500, 20000} {
		options := DefaultImpactPathOptions()
		options.Depth = 0
		options.MaxEdges = limit
		report, err := TraverseImpactPaths(t.Context(), "n000", edges, options)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range report.Results {
			for _, path := range result.Paths {
				at := "n000"
				seen := map[string]bool{at: true}
				strength := 1.0
				for _, step := range path.Steps {
					if !facts[impactStepKey(step)] || step.ToID != at || seen[step.FromID] {
						t.Fatalf("invalid or repeated proof: %+v", path)
					}
					at = step.FromID
					seen[at] = true
					strength = math.Min(strength, step.Confidence)
				}
				if at != result.ID || strength != path.WeakestConfidence {
					t.Fatalf("incorrect proof target/strength: %+v", result)
				}
			}
		}
		shuffled := append([]RelationRecord(nil), edges...)
		random.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		other, err := TraverseImpactPaths(t.Context(), "n000", shuffled, options)
		if err != nil || !reflect.DeepEqual(report, other) {
			t.Fatal("bounded proof selection changed after shuffle")
		}
	}
}

func BenchmarkImpactPathsThousandDependents(b *testing.B) {
	edges := make([]RelationRecord, 1000)
	for i := range edges {
		edges[i] = pathFixtureEdge(fmt.Sprintf("node%04d", i), "focus", "CALLS", .9)
	}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		report, err := TraverseImpactPaths(b.Context(), "focus", edges, options)
		if err != nil || len(report.Results) != len(edges) || report.Truncated {
			b.Fatal("incomplete stress result")
		}
	}
}

func TestImpactPathsRepeatedEvidenceOutputBound(t *testing.T) {
	var edges []RelationRecord
	for i := 1; i <= 20; i++ {
		edge := pathFixtureEdge(fmt.Sprintf("n%02d", i), fmt.Sprintf("n%02d", i-1), "CALLS", 1)
		edge.Evidence[0].Detail = strings.Repeat("evidence", 40)
		edges = append(edges, edge)
	}
	options := DefaultImpactPathOptions()
	options.Depth = 0
	options.MaxEvidenceBytes = 15000
	report, err := TraverseImpactPaths(t.Context(), "n00", edges, options)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report.Results)
	if err != nil || len(encoded) > options.MaxEvidenceBytes || !reflect.DeepEqual(report.StopReasons, []string{"output_evidence_bound"}) || report.VisitedNodes != 21 {
		t.Fatalf("repeated evidence is not bounded: bytes=%d report=%+v err=%v", len(encoded), report, err)
	}
}
