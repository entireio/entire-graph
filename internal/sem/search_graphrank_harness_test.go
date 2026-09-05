package sem

import (
	"fmt"
	"testing"
)

// These are evaluation interfaces, not additional product modes. Expansion is
// an explicit identity control until a separately budgeted expansion exists.
func graphRankingEvaluationOptions(arm string, compilerOptions *CompilerOptions) (SearchOptions, error) {
	options := SearchOptions{Worktree: true, Profile: ProfileFull, TopK: 8, MaxContextBytes: 4096, DisableCache: true, rankingEvaluationCapture: true}
	switch arm {
	case "current", "current-expansion":
	case "uniform", "weighted", "weighted-compiler":
		options.Ranking = "experimental-graph"
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
		if err != nil || !options.rankingEvaluationCapture || options.Compiler != nil {
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
