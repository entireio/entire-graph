package sem

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGraphRankingCandidateContracts(t *testing.T) {
	base := []searchCandidate{{score: 10, result: SearchResult{SymbolID: "a", FilePath: "a.go", StartLine: 1, EndLine: 3}}, {score: 5, result: SearchResult{SymbolID: "b", FilePath: "b.go", StartLine: 1, EndLine: 3}}}
	for _, signal := range []string{"exact-symbol", "exact-code-token"} {
		candidates := append([]searchCandidate(nil), base...)
		candidates[1].result.Signals = []string{signal}
		before := append([]searchCandidate(nil), candidates...)
		diag, err := rerankSearchCandidates(t.Context(), candidates, nil, false)
		if err != nil || diag.Fallback != "exact_match_preserved" || !reflect.DeepEqual(candidates, before) {
			t.Fatalf("exact fallback changed candidates: %+v %v", diag, err)
		}
	}
	candidates := append([]searchCandidate(nil), base...)
	diag, err := rerankSearchCandidates(t.Context(), candidates, []RelationRecord{pathFixtureEdge("a", "b", "CALLS", 1)}, false)
	if err != nil || diag.Fallback != "" || len(candidates) != 2 {
		t.Fatalf("rerank: %+v %v", diag, err)
	}
	for i, c := range candidates {
		if c.result.FilePath != base[i].result.FilePath || c.result.StartLine != base[i].result.StartLine || c.result.EndLine != base[i].result.EndLine {
			t.Fatal("reranking changed source location")
		}
		if c.result.Ranking == nil || math.Abs(c.score/10-c.result.Ranking.Combined) > 1e-12 {
			t.Fatalf("components disagree: %+v", c)
		}
	}
	diag, err = rerankSearchCandidates(t.Context(), candidates, nil, true)
	if err != nil || diag.Fallback != "deep_fusion_not_evaluated" {
		t.Fatalf("deep fallback: %+v %v", diag, err)
	}
}

func TestGraphRankingSearchCaptureAndFreshness(t *testing.T) {
	repo := t.TempDir()
	before := "package p\nfunc Alpha() string { return \"before\" }\n"
	after := "package p\nfunc Alpha() string { return \"after!\" }\n"
	write(t, repo, "a.go", before)
	write(t, repo, "b.go", "package p\nfunc Beta() {}\n")
	opts := SearchOptions{Worktree: true, Ranking: "experimental-graph", MaxIndexedFiles: 1, DisableCache: true}
	opts.afterSourceSelection = func() {
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(after), 0600); err != nil {
			t.Fatal(err)
		}
	}
	response, err := SearchRepository(t.Context(), repo, "fixture", "Alpha", opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(response.Results)
	if !strings.Contains(string(encoded), "before") || strings.Contains(string(encoded), "after!") {
		t.Fatalf("mixed capture: %s", encoded)
	}
	if response.Stats.Ranking == nil || response.Stats.Ranking.Fallback != "exact_match_preserved" {
		t.Fatalf("missing fallback: %+v", response.Stats.Ranking)
	}
	opts.afterSourceSelection = nil
	fresh, err := SearchRepository(t.Context(), repo, "fixture", "Alpha", opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(fresh.Results)
	if !strings.Contains(string(encoded), "after!") {
		t.Fatalf("stale new operation: %s", encoded)
	}
}

func TestGraphRankingSearchExactScopeAndGuidance(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "go.mod", "module example.org/fixture\n\ngo 1.24\n")
	write(t, repo, "allowed/charge.go", "package allowed\nfunc ChargeInvoice() int { return 7 }\n")
	write(t, repo, "allowed/charge_test.go", "package allowed\nimport \"testing\"\nfunc TestChargeInvoice(t *testing.T) { if ChargeInvoice()!=7 { t.Fatal(\"bad\") } }\n")
	write(t, repo, "outside/charge.go", "package outside\nfunc ChargeInvoice() int { return 9 }\n")
	// Explicit exclusion is applied before candidate creation and graph ranking.
	write(t, repo, ".graphignore", "outside/\n")
	options := SearchOptions{Worktree: true, Profile: ProfileFull, TopK: 5, MaxContextBytes: 4096, DisableCache: true}
	current, err := SearchRepository(t.Context(), repo, "fixture", "ChargeInvoice", options)
	if err != nil {
		t.Fatal(err)
	}
	options.Ranking = "experimental-graph"
	graph, err := SearchRepository(t.Context(), repo, "fixture", "ChargeInvoice", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Results, graph.Results) || !reflect.DeepEqual(current.VerifyCommand, graph.VerifyCommand) {
		t.Fatalf("exact query results/guidance changed\ncurrent=%+v\ngraph=%+v", current.Results, graph.Results)
	}
	if len(graph.Results) == 0 || graph.Results[0].SymbolName != "ChargeInvoice" {
		t.Fatal("exact symbol lost")
	}
	for _, r := range graph.Results {
		if strings.HasPrefix(r.FilePath, "outside/") {
			t.Fatal("excluded result entered topology")
		}
	}
	if graph.VerifyCommand == nil {
		t.Fatal("fixture must exercise verification guidance")
	}
}

func TestGraphRankingUnrelatedHubAndTransitionBudget(t *testing.T) {
	lexical := map[string]float64{"a": 2, "b": 1}
	edges := []RelationRecord{pathFixtureEdge("a", "b", "CALLS", 1)}
	want, _, err := graphRankScores(t.Context(), lexical, edges)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		edges = append(edges, pathFixtureEdge("hub", string(rune(i)), "CALLS", 1))
	}
	got, _, err := graphRankScores(t.Context(), lexical, edges)
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("outside candidate hub affected rank")
	}
	lexical = map[string]float64{}
	for i := 0; i < 102; i++ {
		lexical[string(rune(i))] = 1
	}
	edges = nil
	for from := range lexical {
		for to := range lexical {
			if from != to {
				edges = append(edges, pathFixtureEdge(from, to, "CALLS", 1))
			}
		}
	}
	got, diag, err := graphRankScores(t.Context(), lexical, edges)
	if err != nil || diag.Fallback != "transition_bound" || !reflect.DeepEqual(got, lexical) {
		t.Fatalf("transition limit changed baseline: %+v %v", diag, err)
	}
}

func TestGraphRankingInputScanBoundAndCoverage(t *testing.T) {
	lexical := map[string]float64{"a": 2, "b": 1, "isolated": 1}
	edges := []RelationRecord{pathFixtureEdge("a", "b", "CALLS", 1), pathFixtureEdge("outside", "unknown", "CALLS", 1)}
	_, diag, err := graphRankScores(t.Context(), lexical, edges)
	if err != nil || diag.InputRelations != 2 || diag.ExaminedRelations != 2 || diag.ConnectedNodes != 2 || diag.Nodes != 3 {
		t.Fatalf("coverage %+v %v", diag, err)
	}
	input := make([]RelationRecord, 100001)
	got, diag, err := graphRankScores(t.Context(), lexical, input)
	if err != nil || diag.Fallback != "input_relation_bound" || diag.ExaminedRelations != 0 || !reflect.DeepEqual(got, lexical) {
		t.Fatalf("unbounded irrelevant scan %+v %v", diag, err)
	}
}
