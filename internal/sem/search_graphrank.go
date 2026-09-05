package sem

import (
	"context"
	"errors"
	"math"
	"sort"
)

// graphRankTransition is an experiment-local transition, not a graph fact.
type graphRankTransition struct {
	from, to int
	weight   float64
}
type graphRankDiagnostics struct {
	Nodes, Transitions, Iterations int
	Residual                       float64
	Fallback                       string
}

func personalizedPageRank(ctx context.Context, seeds []float64, edges []graphRankTransition, alpha float64, iterations int, tolerance float64) ([]float64, int, float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	if math.IsNaN(alpha) || math.IsInf(alpha, 0) || alpha <= 0 || alpha > 1 || iterations < 1 || math.IsNaN(tolerance) || math.IsInf(tolerance, 0) || tolerance < 0 {
		return nil, 0, 0, errors.New("invalid graph ranking settings")
	}
	sum := 0.0
	for _, seed := range seeds {
		if math.IsNaN(seed) || math.IsInf(seed, 0) || seed < 0 {
			return nil, 0, 0, errors.New("invalid lexical seed")
		}
		sum += seed
	}
	if math.IsInf(sum, 0) {
		return nil, 0, 0, errors.New("lexical seed sum overflow")
	}
	normalized := make([]float64, len(seeds))

	ordered := append([]graphRankTransition(nil), edges...)
	for _, edge := range ordered {
		if edge.from < 0 || edge.from >= len(seeds) || edge.to < 0 || edge.to >= len(seeds) || math.IsNaN(edge.weight) || math.IsInf(edge.weight, 0) || edge.weight < 0 {
			return nil, 0, 0, errors.New("invalid graph transition")
		}
	}
	if sum == 0 {
		return normalized, 0, 0, nil
	}
	for i, seed := range seeds {
		normalized[i] = seed / sum
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.weight < b.weight
	})
	totals := make([]float64, len(seeds))
	for _, edge := range ordered {
		totals[edge.from] += edge.weight
		if math.IsInf(totals[edge.from], 0) {
			return nil, 0, 0, errors.New("graph weight overflow")
		}
	}
	rank := append([]float64(nil), normalized...)
	residual := 0.0
	for iteration := 1; iteration <= iterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, iteration - 1, residual, err
		}
		dangling := 0.0
		for i, value := range rank {
			if totals[i] == 0 {
				dangling += value
			}
		}
		next := make([]float64, len(rank))
		for i, seed := range normalized {
			next[i] = (alpha + (1-alpha)*dangling) * seed
		}
		for _, edge := range ordered {
			if totals[edge.from] > 0 {
				next[edge.to] += (1 - alpha) * rank[edge.from] * (edge.weight / totals[edge.from])
			}
		}
		residual = 0
		for i, value := range next {
			residual += math.Abs(value - rank[i])
		}
		rank = next
		if residual < tolerance {
			return rank, iteration, residual, nil
		}
	}
	return rank, iterations, residual, nil
}

// graphRankScores is rerank-only. Callers must preserve their existing exact
// match precedence, file diversity and byte budgeting after using these scores.
// No product entrypoint calls this experimental core until integration gates.
func graphRankScores(ctx context.Context, lexical map[string]float64, relations []RelationRecord) (map[string]float64, graphRankDiagnostics, error) {
	diagnostics := graphRankDiagnostics{Nodes: len(lexical)}
	if err := ctx.Err(); err != nil {
		return nil, diagnostics, err
	}
	current := make(map[string]float64, len(lexical))
	ids := make([]string, 0, len(lexical))
	maximum := 0.0
	for id, score := range lexical {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
			return nil, diagnostics, errors.New("invalid lexical score")
		}
		ids = append(ids, id)
		current[id] = score
		maximum = math.Max(maximum, score)
	}
	if len(ids) > 2000 {
		diagnostics.Fallback = "node_bound"
		return current, diagnostics, nil
	}
	if maximum == 0 {
		diagnostics.Fallback = "no_positive_seeds"
		return current, diagnostics, nil
	}
	sort.Strings(ids)
	index := map[string]int{}
	seeds := make([]float64, len(ids))
	for i, id := range ids {
		index[id] = i
		seeds[i] = lexical[id] / maximum
	}
	type key struct {
		from, to int
		relation string
	}
	weights := map[key]float64{}
	for _, relation := range relations {
		if err := ctx.Err(); err != nil {
			return nil, diagnostics, err
		}
		from, ok := index[relation.FromID]
		if !ok {
			continue
		}
		to, ok := index[relation.ToID]
		if !ok {
			continue
		}
		weight := 0.0
		switch relation.Type {
		case "CALLS", "CONSTRUCTS", "ASYNC_CALLS":
			weight = 1
		case "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE":
			weight = .5
		default:
			continue
		}
		confidence := relation.Confidence
		if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence <= 0 || confidence > 1 {
			continue
		}
		switch {
		case confidence >= .9:
		case confidence >= .7:
			weight *= .7
		default:
			weight *= .3
		}
		forward, reverse := key{from, to, relation.Type}, key{to, from, relation.Type}
		weights[forward] = math.Max(weights[forward], weight)
		weights[reverse] = math.Max(weights[reverse], weight*.5)
		if len(weights) > 10000 {
			diagnostics.Fallback = "transition_bound"
			return current, diagnostics, nil
		}
	}
	keys := make([]key, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.relation < b.relation
	})
	edges := make([]graphRankTransition, 0, len(keys))
	for _, k := range keys {
		edges = append(edges, graphRankTransition{k.from, k.to, weights[k]})
	}
	diagnostics.Transitions = len(edges)
	rank, iterations, residual, err := personalizedPageRank(ctx, seeds, edges, .25, 25, 1e-8)
	diagnostics.Iterations, diagnostics.Residual = iterations, residual
	if err != nil {
		return nil, diagnostics, err
	}
	maxRank := 0.0
	for _, score := range rank {
		maxRank = math.Max(maxRank, score)
	}
	result := map[string]float64{}
	for i, id := range ids {
		result[id] = .8*seeds[i] + .2*rank[i]/maxRank
	}
	return result, diagnostics, nil
}
