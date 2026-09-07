package sem

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	graphRankNodeLimit       = 2000
	graphRankRelationLimit   = 100000
	graphRankTransitionLimit = 10000
	graphRankExpansionLimit  = 20
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

// graphRankExpansionDiagnostics describes the evaluation-only candidate-pool
// ablation. It is separate from ranking diagnostics because the control arm
// applies this expansion while retaining current ranking.
type graphRankExpansionDiagnostics struct {
	Requested          bool   `json:"requested"`
	State              string `json:"state"`
	SeedNodes          int    `json:"seed_nodes"`
	InputRelations     int    `json:"input_relations"`
	ExaminedRelations  int    `json:"examined_relations"`
	EligibleLinks      int    `json:"eligible_links"`
	CandidatesAdded    int    `json:"candidates_added"`
	SourceReadFailures int    `json:"source_read_failures,omitempty"`
	Truncated          bool   `json:"truncated,omitempty"`
	Fallback           string `json:"fallback,omitempty"`
}

func graphRankRelationPolicy(relation RelationRecord, uniform bool) (weight, reverseFactor float64, ok bool) {
	switch relation.Type {
	case "CALLS", "CONSTRUCTS", "ASYNC_CALLS":
		weight = 1
	case "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE":
		weight = .5
	default:
		return 0, 0, false
	}
	confidence := relation.Confidence
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence <= 0 || confidence > 1 {
		return 0, 0, false
	}
	switch {
	case confidence >= .9:
	case confidence >= .7:
		weight *= .7
	default:
		weight *= .3
	}
	reverseFactor = .5
	if uniform {
		return 1, 1, true
	}
	return weight, reverseFactor, true
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
	if len(ids) > graphRankNodeLimit {
		diagnostics.Fallback = "node_bound"
		return current, diagnostics, nil
	}
	if maximum == 0 {
		diagnostics.Fallback = "no_positive_seeds"
		return current, diagnostics, nil
	}
	if len(relations) > graphRankRelationLimit {
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
		weight, reverseFactor, ok := graphRankRelationPolicy(relation, uniform)
		if !ok {
			continue
		}
		forward, reverse := key{from, to, relation.Type}, key{to, from, relation.Type}
		weights[forward] = math.Max(weights[forward], weight)
		weights[reverse] = math.Max(weights[reverse], weight*reverseFactor)
		if len(weights) > graphRankTransitionLimit {
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

// expandGraphRankEvaluationCandidates adds the bounded one-hop neighborhood
// used by every candidate-expansion evaluation arm. Production modes never set
// the internal option that calls it. Eligibility is exactly the weighted P4
// adjacency policy; uniform and weighted arms therefore vary ranking weights,
// not the candidate pool they receive.
func expandGraphRankEvaluationCandidates(ctx context.Context, seeds []searchCandidate, q searchQuery, relations []RelationRecord, symbolsByID map[string]SymbolRecord, candidateScope map[string]bool, read contentReader, languages map[string]string, options SearchOptions) ([]searchCandidate, graphRankExpansionDiagnostics, error) {
	diagnostics := graphRankExpansionDiagnostics{Requested: true, State: "applied", InputRelations: len(relations)}
	if err := ctx.Err(); err != nil {
		return nil, diagnostics, err
	}
	seedScores := make(map[string]float64, len(seeds))
	for _, candidate := range seeds {
		if candidate.result.SymbolID != "" && candidate.score > 0 && !math.IsNaN(candidate.score) && !math.IsInf(candidate.score, 0) && candidate.score > seedScores[candidate.result.SymbolID] {
			seedScores[candidate.result.SymbolID] = candidate.score
		}
	}
	diagnostics.SeedNodes = len(seedScores)
	if len(seedScores) == 0 {
		diagnostics.State, diagnostics.Fallback = "fallback", "no_symbol_seeds"
		return nil, diagnostics, nil
	}
	if len(seedScores) > graphRankNodeLimit {
		diagnostics.State, diagnostics.Fallback = "fallback", "node_bound"
		return nil, diagnostics, nil
	}
	if len(relations) > graphRankRelationLimit {
		diagnostics.State, diagnostics.Fallback = "fallback", "input_relation_bound"
		return nil, diagnostics, nil
	}
	type proposal struct {
		symbol    SymbolRecord
		seedID    string
		seedScore float64
		weight    float64
		direction string
		relation  string
	}
	type expansionLink struct {
		seed, target, direction, relation string
	}
	links := map[expansionLink]proposal{}
	for _, relation := range relations {
		diagnostics.ExaminedRelations++
		if err := ctx.Err(); err != nil {
			return nil, diagnostics, err
		}
		weight, reverseFactor, ok := graphRankRelationPolicy(relation, false)
		if !ok {
			continue
		}
		pairs := []struct {
			seed, target, direction string
			weight                  float64
		}{{relation.FromID, relation.ToID, "outgoing", weight}, {relation.ToID, relation.FromID, "incoming", weight * reverseFactor}}
		for _, pair := range pairs {
			seedScore, seeded := seedScores[pair.seed]
			symbol, known := symbolsByID[pair.target]
			if !seeded || !known || !candidateScope[symbol.FilePath] || pair.seed == pair.target || seedScores[pair.target] > 0 {
				continue
			}
			link := expansionLink{pair.seed, pair.target, pair.direction, relation.Type}
			candidate := proposal{symbol: symbol, seedID: pair.seed, seedScore: seedScore, weight: pair.weight, direction: pair.direction, relation: relation.Type}
			if previous, exists := links[link]; !exists || candidate.weight > previous.weight {
				links[link] = candidate
			}
			if len(links) > graphRankTransitionLimit {
				diagnostics.EligibleLinks = len(links)
				diagnostics.State, diagnostics.Fallback = "fallback", "transition_bound"
				return nil, diagnostics, nil
			}
		}
	}
	diagnostics.EligibleLinks = len(links)
	best := map[string]proposal{}
	proposalLess := func(left, right proposal) bool {
		ls, rs := left.seedScore*left.weight, right.seedScore*right.weight
		if ls != rs {
			return ls > rs
		}
		if left.symbol.ID != right.symbol.ID {
			return left.symbol.ID < right.symbol.ID
		}
		if left.seedID != right.seedID {
			return left.seedID < right.seedID
		}
		if left.relation != right.relation {
			return left.relation < right.relation
		}
		return left.direction < right.direction
	}
	for _, candidate := range links {
		previous, exists := best[candidate.symbol.ID]
		if !exists || proposalLess(candidate, previous) {
			best[candidate.symbol.ID] = candidate
		}
	}
	proposals := make([]proposal, 0, len(best))
	for _, candidate := range best {
		proposals = append(proposals, candidate)
	}
	sort.Slice(proposals, func(i, j int) bool {
		left, right := proposals[i], proposals[j]
		return proposalLess(left, right)
	})
	if len(proposals) > graphRankExpansionLimit {
		proposals = proposals[:graphRankExpansionLimit]
		diagnostics.Truncated = true
	}
	out := make([]searchCandidate, 0, len(proposals))
	for _, proposal := range proposals {
		if err := ctx.Err(); err != nil {
			return nil, diagnostics, err
		}
		content, ok := read(proposal.symbol.FilePath)
		if !ok {
			diagnostics.SourceReadFailures++
			continue
		}
		lines := strings.Split(content, "\n")
		start, end := clampRegion(proposal.symbol.StartLine, proposal.symbol.EndLine, len(lines))
		if start == 0 {
			diagnostics.SourceReadFailures++
			continue
		}
		if end-start+1 > options.MaxRegionLines {
			end = minInt(len(lines), start+options.MaxRegionLines-1)
		}
		snippetStart, snippetEnd := focusedSnippetRegion(start, end, start, options.MaxSnippetLines)
		score := derivedSearchScore(proposal.seedScore, .28*proposal.seedScore+2.2*proposal.weight+searchPathPrior(q, proposal.symbol.FilePath))
		candidate := searchCandidate{
			score: score, baseScore: score, aliases: append([]string(nil), proposal.symbol.Aliases...),
			result: SearchResult{FilePath: proposal.symbol.FilePath, StartLine: start, EndLine: end, FocusLine: start,
				SnippetStartLine: snippetStart, SnippetEndLine: snippetEnd, Language: languages[proposal.symbol.FilePath],
				Kind: proposal.symbol.Kind, SymbolID: proposal.symbol.ID, SymbolName: proposal.symbol.Name,
				QualifiedName: proposal.symbol.QualifiedName, Signature: proposal.symbol.Signature,
				SymbolStartLine: proposal.symbol.StartLine, SymbolEndLine: proposal.symbol.EndLine,
				Signals: []string{"graph-rank-expansion", "graph:" + strings.ToLower(proposal.relation), "graph:" + proposal.direction},
				Snippet: strings.Join(lines[snippetStart-1:snippetEnd], "\n")},
		}
		out = append(out, candidate)
	}
	sortSearchCandidates(out)
	diagnostics.CandidatesAdded = len(out)
	if diagnostics.SourceReadFailures > 0 {
		diagnostics.State = "partial"
	}
	return out, diagnostics, nil
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
