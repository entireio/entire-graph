package sem

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
)

// TestGraphRankDevelopmentAblations is an opt-in diagnostic over independently
// authored candidate graphs. It is not an end-to-end retrieval measurement.
func TestGraphRankDevelopmentAblations(t *testing.T) {
	input, output := os.Getenv("ENTIRE_GRAPH_RANK_FIXTURES"), os.Getenv("ENTIRE_GRAPH_RANK_RESULTS")
	if input == "" || output == "" {
		t.Skip("explicit independent development fixtures/output required")
	}
	var fixtures []struct {
		ID        string             `json:"id"`
		Lexical   map[string]float64 `json:"lexical"`
		Relations []RelationRecord   `json:"relations"`
		Required  []string           `json:"required"`
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, fixture := range fixtures {
		weighted, diagnostics, err := graphRankScores(t.Context(), fixture.Lexical, fixture.Relations)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(fixture.Lexical))
		for id := range fixture.Lexical {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		index := map[string]int{}
		seeds := make([]float64, len(ids))
		maxLex := 0.0
		for _, score := range fixture.Lexical {
			maxLex = math.Max(maxLex, score)
		}
		for i, id := range ids {
			index[id] = i
			seeds[i] = fixture.Lexical[id] / maxLex
		}
		// Uniform graph ablation keeps the same eligible relation families and
		// candidate set, but removes confidence/family/direction weighting.
		unique := map[[2]int]bool{}
		for _, r := range fixture.Relations {
			switch r.Type {
			case "CALLS", "CONSTRUCTS", "ASYNC_CALLS", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE":
			default:
				continue
			}
			a, aok := index[r.FromID]
			b, bok := index[r.ToID]
			if aok && bok && r.Confidence > 0 && r.Confidence <= 1 {
				unique[[2]int{a, b}] = true
				unique[[2]int{b, a}] = true
			}
		}
		edges := make([]graphRankTransition, 0, len(unique))
		for pair := range unique {
			edges = append(edges, graphRankTransition{pair[0], pair[1], 1})
		}
		rank, _, _, err := personalizedPageRank(t.Context(), seeds, edges, .25, 25, 1e-8)
		if err != nil {
			t.Fatal(err)
		}
		maxGraph := 0.0
		for _, value := range rank {
			maxGraph = math.Max(maxGraph, value)
		}
		unweighted := map[string]float64{}
		for i, id := range ids {
			unweighted[id] = .8*seeds[i] + .2*rank[i]/maxGraph
		}
		result := struct {
			ID          string               `json:"id"`
			Required    []string             `json:"required"`
			Current     map[string]float64   `json:"current"`
			Unweighted  map[string]float64   `json:"unweighted"`
			Weighted    map[string]float64   `json:"weighted"`
			Diagnostics graphRankDiagnostics `json:"diagnostics"`
			Scope       string               `json:"scope"`
		}{fixture.ID, fixture.Required, fixture.Lexical, unweighted, weighted, diagnostics, "pure candidate graph; not retrieval or release evidence"}
		if err = json.NewEncoder(file).Encode(result); err != nil {
			t.Fatal(err)
		}
	}
}
