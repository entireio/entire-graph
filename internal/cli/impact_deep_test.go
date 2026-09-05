package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestImpactDeepCLIChainAndBudgets(t *testing.T) {
	repo := t.TempDir()
	body := "package p\nfunc A() {}\nfunc B() { A() }\nfunc C() { B() }\nfunc D() { C() }\nfunc E() { D() }\n"
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, depth := range []string{"2", "4", "all"} {
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Stdout: &out}, []string{"impact", "--repo", repo, "--symbol", "A", "--depth", depth, "--format", "json"}); err != nil {
			t.Fatal(err)
		}
		var response impactResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if depth == "2" {
			if response.Traversal != nil {
				t.Fatal("default depth output changed")
			}
			continue
		}
		if response.OperationInputs == nil {
			t.Fatal("deeper operation lacks captured input provenance")
		}
		if response.Traversal == nil || len(response.Traversal.Results) != 4 {
			t.Fatalf("deep chain %#v", response.Traversal)
		}
	}
	flags, err := parseImpactFlags([]string{"--symbol", "A", "--depth", "all", "--max-nodes", "2", "--max-edges", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.PathOptions.Depth != 0 || flags.PathOptions.MaxNodes != 2 || flags.PathOptions.MaxEdges != 3 {
		t.Fatal("work limits not propagated")
	}
	response := impactResponse{Depth: 0, Focus: &neighborEndpoint{ID: "a"}, Traversal: &sem.ImpactPathReport{Policy: "fixture", Truncated: true, StopReasons: []string{"edge_bound"}, Results: []sem.ImpactPathResult{{ID: strings.Repeat("long", 100), Paths: []sem.ImpactEvidencePath{{Steps: []sem.ImpactPathStep{{FromID: "b", ToID: "a", Relation: "CALLS", Direction: "in"}}}}}}}}
	var out bytes.Buffer
	if err := writeDeepImpact(&out, response, nil, 90); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 90 || !strings.Contains(out.String(), "truncated") {
		t.Fatalf("budget or notice: %q", out.String())
	}
}

func TestImpactDeepFlagsRejectIgnoredOrInvalidControls(t *testing.T) {
	for _, args := range [][]string{
		{"--symbol", "missing", "--depth", "all", "--relations", "CONTAINS"},
		{"--symbol", "missing", "--depth", "all", "--min-confidence", "NaN"},
		{"--symbol", "missing", "--depth", "all", "--max-paths", "17"},
		{"--symbol", "missing", "--depth", "2", "--max-edges", "10"},
		{"--symbol", "missing", "--max-input-edges", "1"},
	} {
		if _, err := parseImpactFlags(args); err == nil {
			t.Fatalf("accepted ignored/invalid controls %v", args)
		}
	}
}

func TestImpactDeepTextBoundsEveryBudget(t *testing.T) {
	response := impactResponse{Depth: 0, Focus: &neighborEndpoint{ID: "a"}, Traversal: &sem.ImpactPathReport{Policy: "fixture", Results: []sem.ImpactPathResult{{ID: "b", Paths: []sem.ImpactEvidencePath{{Category: "structural", Steps: []sem.ImpactPathStep{{FromID: "b", ToID: "a", Relation: "CALLS", Direction: "in"}}}}}}}}
	for budget := 1; budget <= 600; budget++ {
		var out bytes.Buffer
		if err := writeDeepImpact(&out, response, nil, budget); err != nil {
			t.Fatal(err)
		}
		if out.Len() > budget {
			t.Fatalf("budget %d produced %d bytes", budget, out.Len())
		}
		if budget >= len("!truncated\n") && budget < 150 && !strings.Contains(out.String(), "truncated") {
			t.Fatalf("missing reserved notice at %d: %q", budget, out.String())
		}
	}
}
