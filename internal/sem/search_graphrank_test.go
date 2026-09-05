package sem

import (
	"math"
	"reflect"
	"testing"
)

func TestPageRankHandDerivedIteration(t *testing.T) {
	// s=(1,0,0); only A->B. First step: .25 stays at A, .75 reaches B.
	rank, iterations, _, err := personalizedPageRank(t.Context(), []float64{1, 0, 0}, []graphRankTransition{{0, 1, 1}}, .25, 1, 1e-8)
	if err != nil || iterations != 1 || !reflect.DeepEqual(rank, []float64{.25, .75, 0}) {
		t.Fatalf("first step=%v %v", rank, err)
	}
	// Second step: B's dangling .75 returns to seeds: A=.25+.75*.75.
	rank, _, _, err = personalizedPageRank(t.Context(), []float64{1, 0, 0}, []graphRankTransition{{0, 1, 1}}, .25, 2, 1e-8)
	if err != nil || !reflect.DeepEqual(rank, []float64{.8125, .1875, 0}) {
		t.Fatalf("dangling=%v %v", rank, err)
	}
}

func TestPageRankMassAndInvalidWeights(t *testing.T) {
	rank, _, _, err := personalizedPageRank(t.Context(), []float64{2, 1, 1}, []graphRankTransition{{0, 1, 1}, {1, 0, 1}}, .25, 25, 1e-8)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for _, score := range rank {
		sum += score
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Fatalf("mass=%v", sum)
	}
	for _, weight := range []float64{-1, math.Inf(1), math.NaN()} {
		if _, _, _, err := personalizedPageRank(t.Context(), []float64{1}, []graphRankTransition{{0, 0, weight}}, .25, 25, 1e-8); err == nil {
			t.Fatal("invalid weight accepted")
		}
	}
}

func TestGraphRankScopeDuplicatesAndOrder(t *testing.T) {
	lexical := map[string]float64{"a": 2, "b": 1, "c": .5}
	edges := []RelationRecord{pathFixtureEdge("a", "b", "CALLS", 1), pathFixtureEdge("b", "c", "USES_TYPE", .7)}
	expected, diag, err := graphRankScores(t.Context(), lexical, edges)
	if err != nil {
		t.Fatal(err)
	}
	extra := []RelationRecord{edges[1], edges[0], edges[0], pathFixtureEdge("outside", "a", "CALLS", 1), pathFixtureEdge("c", "a", "CONTAINS", 1)}
	got, other, err := graphRankScores(t.Context(), lexical, extra)
	if diag.InputRelations != len(edges) || other.InputRelations != len(extra) || diag.ExaminedRelations != len(edges) || other.ExaminedRelations != len(extra) {
		t.Fatalf("input work not reported: %+v %+v", diag, other)
	}
	// Input work changes; eligible topology and numeric convergence do not.
	diag.InputRelations, diag.ExaminedRelations = 0, 0
	other.InputRelations, other.ExaminedRelations = 0, 0
	if err != nil || !reflect.DeepEqual(got, expected) || diag != other {
		t.Fatalf("scope or duplicate changed ranking: %v %v", got, err)
	}
	if len(got) != 3 {
		t.Fatal("expanded candidates")
	}
}

func TestGraphRankFallback(t *testing.T) {
	for _, lexical := range []map[string]float64{nil, {"a": 0}} {
		got, diag, err := graphRankScores(t.Context(), lexical, nil)
		if err != nil || diag.Fallback != "no_positive_seeds" || len(got) != len(lexical) {
			t.Fatalf("fallback=%+v %v", diag, err)
		}
	}
	scores := map[string]float64{}
	for i := 0; i < 2001; i++ {
		scores[string(rune(i))] = 1
	}
	got, diag, err := graphRankScores(t.Context(), scores, nil)
	if err != nil || diag.Fallback != "node_bound" || !reflect.DeepEqual(got, scores) {
		t.Fatal("resource fallback changed scores")
	}
}
