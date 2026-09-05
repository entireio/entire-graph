package sem

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
)

// graphRankTransition is an experiment-local transition, not a graph fact.
type graphRankTransition struct {
	from, to int
	weight   float64
}
type graphRankDiagnostics struct {
	InputRelations    int     `json:"input_relations"`
	ExaminedRelations int     `json:"examined_relations"`
	ConnectedNodes    int     `json:"connected_nodes"`
	Nodes             int     `json:"nodes"`
	Transitions       int     `json:"transitions"`
	Iterations        int     `json:"iterations"`
	Residual          float64 `json:"residual"`
	Fallback          string  `json:"fallback,omitempty"`
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
// Experimental query integration remains default off pending release gates.
func graphRankScores(ctx context.Context, lexical map[string]float64, relations []RelationRecord) (map[string]float64, graphRankDiagnostics, error) {
	return graphRankScoresWithPolicy(ctx, lexical, relations, false)
}

func graphRankScoresWithPolicy(ctx context.Context, lexical map[string]float64, relations []RelationRecord, uniform bool) (map[string]float64, graphRankDiagnostics, error) {
	diagnostics := graphRankDiagnostics{Nodes: len(lexical), InputRelations: len(relations)}
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
	if len(relations) > 100000 {
		diagnostics.Fallback = "input_relation_bound"
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
		diagnostics.ExaminedRelations++
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
		reverseFactor := .5
		if uniform {
			weight = 1
			reverseFactor = 1
		}
		forward, reverse := key{from, to, relation.Type}, key{to, from, relation.Type}
		weights[forward] = math.Max(weights[forward], weight)
		weights[reverse] = math.Max(weights[reverse], weight*reverseFactor)
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
	connected := map[int]bool{}
	for _, edge := range edges {
		connected[edge.from] = true
		connected[edge.to] = true
	}
	diagnostics.ConnectedNodes = len(connected)
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

// rerankSearchCandidates applies only to the existing candidate pool. A global
// exact-match fallback preserves the entire current exact-query ordering, not
// just its top result. Deep sparse fusion requires its own evaluation arm.
func rerankSearchCandidates(ctx context.Context, candidates []searchCandidate, relations []RelationRecord, deep bool) (graphRankDiagnostics, error) {
	return rerankSearchCandidatesWithPolicy(ctx, candidates, relations, deep, false)
}

func rerankSearchCandidatesWithPolicy(ctx context.Context, candidates []searchCandidate, relations []RelationRecord, deep, uniform bool) (graphRankDiagnostics, error) {
	diagnostics := graphRankDiagnostics{}
	if deep {
		diagnostics.Fallback = "deep_fusion_not_evaluated"
		return diagnostics, nil
	}
	lexical := map[string]float64{}
	keys := make([]string, len(candidates))
	maximum := 0.0
	for i, candidate := range candidates {
		if searchResultHasSignal(candidate.result, "exact-symbol") || searchResultHasSignal(candidate.result, "exact-code-token") {
			diagnostics.Fallback = "exact_match_preserved"
			return diagnostics, nil
		}
		if candidate.score < 0 || math.IsNaN(candidate.score) || math.IsInf(candidate.score, 0) {
			diagnostics.Fallback = "nonpositive_or_invalid_lexical_score"
			return diagnostics, nil
		}
		key := candidate.result.SymbolID
		if key == "" {
			key = "rank-region:" + extractionIdentity(candidate.result.FilePath, strconv.Itoa(candidate.result.StartLine), strconv.Itoa(candidate.result.EndLine))
		}
		keys[i] = key
		lexical[key] = math.Max(lexical[key], candidate.score)
		maximum = math.Max(maximum, candidate.score)
	}
	scores, diagnostics, err := graphRankScoresWithPolicy(ctx, lexical, relations, uniform)
	if err != nil || diagnostics.Fallback != "" {
		return diagnostics, err
	}
	for i := range candidates {
		graphComponent := (scores[keys[i]] - .8*lexical[keys[i]]/maximum) / .2
		lexicalComponent := candidates[i].score / maximum
		combined := .8*lexicalComponent + .2*graphComponent
		candidates[i].result.Ranking = &SearchRankingComponents{Lexical: lexicalComponent, Graph: graphComponent, Combined: combined}
		candidates[i].score = combined * maximum
	}
	return diagnostics, nil
}

type SearchRankingComponents struct {
	Lexical  float64 `json:"lexical"`
	Graph    float64 `json:"graph"`
	Combined float64 `json:"combined"`
}
