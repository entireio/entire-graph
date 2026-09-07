package sem

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// These are evaluation interfaces, not additional product modes.
func graphRankingEvaluationOptions(arm string, compilerOptions *CompilerOptions) (SearchOptions, error) {
	options := SearchOptions{Worktree: true, Profile: ProfileFull, TopK: 8, MaxContextBytes: 4096, DisableCache: true, rankingEvaluationCapture: true}
	switch arm {
	case "current":
	case "current-expansion":
		options.rankingEvaluationExpansion = true
	case "uniform", "weighted", "weighted-compiler":
		options.Ranking = "experimental-graph"
		options.rankingEvaluationExpansion = true
		options.rankingEvaluationUniform = arm == "uniform"
		if arm == "weighted-compiler" {
			if compilerOptions == nil {
				return SearchOptions{}, fmt.Errorf("weighted-compiler requires explicit pinned compiler configuration")
			}
			copy := *compilerOptions
			copy.Require = true
			options.Compiler = &copy
		}
	default:
		return SearchOptions{}, fmt.Errorf("unknown evaluation arm %q", arm)
	}
	return options, nil
}

func TestGraphRankingEvaluationHarnessPlumbing(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "a.go", "package p\nfunc Beacon() int { return 7 }\n")
	// One single-arm retrieval smoke test; no timings, labels, scores or quality
	// comparisons are collected. Arm selection itself is a pure contract check.
	options, err := graphRankingEvaluationOptions("current", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := SearchRepository(t.Context(), repo, "harness-smoke", "Beacon", options)
	if err != nil || response.OperationInputs == nil || len(response.Results) == 0 {
		t.Fatalf("captured baseline plumbing: %v %+v", err, response)
	}
	for _, arm := range []string{"current-expansion", "uniform", "weighted"} {
		options, err := graphRankingEvaluationOptions(arm, nil)
		if err != nil || !options.rankingEvaluationCapture || !options.rankingEvaluationExpansion || options.Compiler != nil {
			t.Fatalf("arm %s: %+v %v", arm, options, err)
		}
	}
	if _, err := graphRankingEvaluationOptions("weighted-compiler", nil); err == nil {
		t.Fatal("compiler arm silently omitted backend")
	}
	compilerOptions := &CompilerOptions{}
	options, err = graphRankingEvaluationOptions("weighted-compiler", compilerOptions)
	if err != nil || !options.Compiler.Require || compilerOptions.Require {
		t.Fatal("compiler arm must require backend without mutating input")
	}
	if _, err := graphRankingEvaluationOptions("unknown", nil); err == nil {
		t.Fatal("unknown arm accepted")
	}
}

func TestGraphRankingEvaluationExpansionUsesIdenticalPreRankPool(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 12; index++ {
		body := fmt.Sprintf("package p\nfunc NeedleCandidate%02d() int { return %d }\n", index, index)
		if index == 11 {
			body = "package p\nfunc NeedleCandidate11() int { return HiddenTarget() }\n"
		}
		write(t, repo, fmt.Sprintf("candidate%02d.go", index), body)
	}
	write(t, repo, "hidden.go", "package p\nfunc HiddenTarget() int { return 99 }\n")

	type captured struct {
		ids    []string
		scores map[string]float64
		stats  SearchStats
	}
	run := func(arm string) captured {
		options, err := graphRankingEvaluationOptions(arm, nil)
		if err != nil {
			t.Fatal(err)
		}
		result := captured{scores: map[string]float64{}}
		options.rankingEvaluationCandidates = func(candidates []searchCandidate) {
			for _, candidate := range candidates {
				key := searchCandidateLocationKey(candidate)
				result.ids = append(result.ids, key)
				result.scores[key] = candidate.score
			}
			sort.Strings(result.ids)
		}
		response, searchErr := SearchRepository(t.Context(), repo, "harness-expansion", "needle candidate behavior", options)
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		result.stats = response.Stats
		return result
	}

	current := run("current")
	expanded := run("current-expansion")
	uniform := run("uniform")
	weighted := run("weighted")
	if expanded.stats.RankingExpansion == nil || expanded.stats.RankingExpansion.CandidatesAdded == 0 {
		t.Fatalf("expansion arm did no candidate work: %+v", expanded.stats.RankingExpansion)
	}
	if reflect.DeepEqual(current.ids, expanded.ids) {
		t.Fatal("current-expansion candidate pool is still identical to current")
	}
	if !reflect.DeepEqual(expanded.ids, uniform.ids) || !reflect.DeepEqual(expanded.ids, weighted.ids) {
		t.Fatalf("candidate pools differ: expanded=%v uniform=%v weighted=%v", expanded.ids, uniform.ids, weighted.ids)
	}
	for key, score := range current.scores {
		if expandedScore, exists := expanded.scores[key]; !exists || expandedScore != score {
			t.Fatalf("baseline score changed for %s: current=%v expansion=%v", key, score, expandedScore)
		}
	}
	if current.stats.RankingExpansion != nil {
		t.Fatalf("current unexpectedly requested evaluation expansion: %+v", current.stats.RankingExpansion)
	}
}

func TestGraphRankingEvaluationExpansionKeepsSearchRepositoryFileScope(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "chosen.go", "package p\nfunc ChosenNeedle() int { return HiddenTarget() }\n")
	write(t, repo, "hidden.go", "package p\nfunc HiddenTarget() int { return 99 }\n")
	options, err := graphRankingEvaluationOptions("current-expansion", nil)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxIndexedFiles = 1
	var captured []searchCandidate
	options.rankingEvaluationCandidates = func(candidates []searchCandidate) {
		captured = append([]searchCandidate(nil), candidates...)
	}
	response, err := SearchRepository(t.Context(), repo, "harness-scope", "chosen.go ChosenNeedle", options)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range captured {
		if candidate.result.FilePath != "chosen.go" {
			t.Fatalf("candidate escaped explicit one-file search scope: %+v", candidate.result)
		}
	}
	if response.Stats.RankingExpansion == nil || response.Stats.RankingExpansion.CandidatesAdded != 0 {
		t.Fatalf("out-of-scope neighbor was expanded: %+v", response.Stats.RankingExpansion)
	}
}

func TestGraphRankingEvaluationExpansionBoundsAndFallback(t *testing.T) {
	seeds := []searchCandidate{{score: 10, result: SearchResult{SymbolID: "seed"}}}
	symbols := map[string]SymbolRecord{"seed": {ID: "seed"}}
	relations := make([]RelationRecord, graphRankRelationLimit+1)
	scope := map[string]bool{}
	_, diagnostics, err := expandGraphRankEvaluationCandidates(t.Context(), seeds, buildSearchQuery("needle"), relations, symbols, scope, func(string) (string, bool) { return "", false }, nil, SearchOptions{})
	if err != nil || diagnostics.Fallback != "input_relation_bound" || diagnostics.ExaminedRelations != 0 {
		t.Fatalf("relation fallback: %+v %v", diagnostics, err)
	}

	for index := 0; index < graphRankExpansionLimit+3; index++ {
		id := fmt.Sprintf("target-%02d", index)
		path := fmt.Sprintf("target-%02d.go", index)
		symbols[id] = SymbolRecord{ID: id, Name: id, FilePath: path, StartLine: 1, EndLine: 1}
		scope[path] = true
		relations[index] = RelationRecord{FromID: "seed", ToID: id, Type: "CALLS", Confidence: 1}
	}
	relations = relations[:graphRankExpansionLimit+3]
	read := func(string) (string, bool) { return "func target() {}", true }
	first, firstDiagnostics, err := expandGraphRankEvaluationCandidates(t.Context(), seeds, buildSearchQuery("needle"), relations, symbols, scope, read, nil, SearchOptions{MaxRegionLines: 10, MaxSnippetLines: 10})
	if err != nil || len(first) != graphRankExpansionLimit || !firstDiagnostics.Truncated {
		t.Fatalf("candidate cap: len=%d diagnostics=%+v err=%v", len(first), firstDiagnostics, err)
	}
	shuffled := append([]RelationRecord(nil), relations...)
	for left, right := 0, len(shuffled)-1; left < right; left, right = left+1, right-1 {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	}
	shuffled = append(shuffled, shuffled...)
	second, secondDiagnostics, err := expandGraphRankEvaluationCandidates(t.Context(), seeds, buildSearchQuery("needle"), shuffled, symbols, scope, read, nil, SearchOptions{MaxRegionLines: 10, MaxSnippetLines: 10})
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstDiagnostics, secondDiagnostics) {
		if secondDiagnostics.InputRelations != 2*firstDiagnostics.InputRelations || secondDiagnostics.ExaminedRelations != 2*firstDiagnostics.ExaminedRelations {
			t.Fatalf("diagnostic input work mismatch: first=%+v second=%+v", firstDiagnostics, secondDiagnostics)
		}
		firstDiagnostics.InputRelations, firstDiagnostics.ExaminedRelations = secondDiagnostics.InputRelations, secondDiagnostics.ExaminedRelations
		if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstDiagnostics, secondDiagnostics) {
			t.Fatalf("bounded expansion changed under relation reorder/duplicates: first=%+v second=%+v", firstDiagnostics, secondDiagnostics)
		}
	}
}

func TestGraphRankingEvaluationExpansionHonorsScopeReadFailureAndCancellation(t *testing.T) {
	seeds := []searchCandidate{{score: 10, result: SearchResult{SymbolID: "seed"}}}
	symbols := map[string]SymbolRecord{
		"seed":     {ID: "seed"},
		"allowed":  {ID: "allowed", FilePath: "allowed.go", StartLine: 1, EndLine: 1},
		"excluded": {ID: "excluded", FilePath: "excluded.go", StartLine: 1, EndLine: 1},
	}
	relations := []RelationRecord{
		{FromID: "seed", ToID: "allowed", Type: "CALLS", Confidence: 1},
		{FromID: "seed", ToID: "excluded", Type: "CALLS", Confidence: 1},
	}
	reads := map[string]int{}
	read := func(path string) (string, bool) {
		reads[path]++
		return "", false
	}
	_, diagnostics, err := expandGraphRankEvaluationCandidates(t.Context(), seeds, buildSearchQuery("needle"), relations, symbols, map[string]bool{"allowed.go": true}, read, nil, SearchOptions{MaxRegionLines: 10, MaxSnippetLines: 10})
	if err != nil || diagnostics.State != "partial" || diagnostics.SourceReadFailures != 1 || reads["allowed.go"] != 1 || reads["excluded.go"] != 0 {
		t.Fatalf("scope/read diagnostics: reads=%v diagnostics=%+v err=%v", reads, diagnostics, err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := expandGraphRankEvaluationCandidates(cancelled, seeds, buildSearchQuery("needle"), relations, symbols, map[string]bool{"allowed.go": true}, read, nil, SearchOptions{}); err != context.Canceled {
		t.Fatalf("cancelled expansion error = %v", err)
	}
}

func TestGraphRankingEvaluationExpansionEqualProductTieIsDeterministic(t *testing.T) {
	seeds := []searchCandidate{
		{score: 10, result: SearchResult{SymbolID: "seed-a"}},
		{score: 7, result: SearchResult{SymbolID: "seed-b"}},
	}
	symbols := map[string]SymbolRecord{
		"seed-a": {ID: "seed-a"},
		"seed-b": {ID: "seed-b"},
		"target": {ID: "target", Name: "target", FilePath: "target.go", StartLine: 1, EndLine: 1},
	}
	relations := []RelationRecord{
		{FromID: "seed-a", ToID: "target", Type: "CALLS", Confidence: .7},
		{FromID: "seed-b", ToID: "target", Type: "CALLS", Confidence: .9},
	}
	run := func(input []RelationRecord) []searchCandidate {
		candidates, diagnostics, err := expandGraphRankEvaluationCandidates(
			t.Context(), seeds, buildSearchQuery("needle"), input, symbols,
			map[string]bool{"target.go": true}, func(string) (string, bool) { return "func target() {}", true }, nil,
			SearchOptions{MaxRegionLines: 10, MaxSnippetLines: 10},
		)
		if err != nil || diagnostics.CandidatesAdded != 1 {
			t.Fatalf("tie expansion: candidates=%+v diagnostics=%+v err=%v", candidates, diagnostics, err)
		}
		return candidates
	}
	forward := run(relations)
	reverse := run([]RelationRecord{relations[1], relations[0]})
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("equal-product proposal changed under reorder: forward=%+v reverse=%+v", forward, reverse)
	}
}
