package sem

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ImpactPathOptions bounds graph work independently from presentation. Depth
// zero means closure under this policy, subject to all other limits.
type ImpactPathOptions struct {
	Depth          int     `json:"depth"`
	MaxNodes       int     `json:"max_nodes"`
	MaxEdges       int     `json:"max_edges"`
	MaxFrontier    int     `json:"max_frontier"`
	MaxOutputSteps int     `json:"max_output_steps"`
	MaxPaths       int     `json:"max_paths"`
	MinConfidence  float64 `json:"min_confidence"`
	GraphPartial   bool    `json:"graph_partial"`
}

func DefaultImpactPathOptions() ImpactPathOptions {
	return ImpactPathOptions{Depth: 2, MaxNodes: 5000, MaxEdges: 20000, MaxFrontier: 20000, MaxPaths: 2, MaxOutputSteps: 20000}
}

// ImpactPathStep preserves the original fact as well as traversal direction.
type ImpactPathStep struct {
	FromID     string     `json:"from_id"`
	ToID       string     `json:"to_id"`
	Relation   string     `json:"relation"`
	Direction  string     `json:"direction"`
	Resolution string     `json:"resolution,omitempty"`
	Confidence float64    `json:"confidence"`
	Evidence   []Evidence `json:"evidence,omitempty"`
}

type ImpactEvidencePath struct {
	Steps             []ImpactPathStep `json:"steps"`
	WeakestConfidence float64          `json:"weakest_confidence"`
}

type ImpactPathResult struct {
	ID    string               `json:"id"`
	Paths []ImpactEvidencePath `json:"paths"`
}

type ImpactPathReport struct {
	Policy            string             `json:"policy"`
	Limits            ImpactPathOptions  `json:"limits"`
	Results           []ImpactPathResult `json:"results"`
	Truncated         bool               `json:"truncated"`
	StopReasons       []string           `json:"stop_reasons,omitempty"`
	VisitedNodes      int                `json:"visited_nodes"`
	ExaminedEdges     int                `json:"examined_edges"`
	AdmittedStates    int                `json:"admitted_states"`
	CountsLowerBounds bool               `json:"counts_lower_bounds"`
}

type impactArc struct {
	target, mode, key string
	step              ImpactPathStep
}
type impactPredecessor struct {
	id, mode      string
	parent, depth int
	arc           impactArc
	strength      float64
}

func impactPathPolicy(relation string) (direction, mode string) {
	switch relation {
	case "CALLS", "CONSTRUCTS", "ASYNC_CALLS", "USES_TYPE", "PARAM_TYPE", "RETURNS_TYPE", "EXTENDS", "INHERITS", "IMPLEMENTS", "OVERRIDES":
		return "in", "dependency"
	case "TESTS":
		return "in", "test"
	default:
		return "", ""
	}
}

// TraverseImpactPaths enumerates bounded simple paths. Results describe possible
// structural effects, never a proof that an unreturned site is safe to change.
func TraverseImpactPaths(ctx context.Context, focus string, relations []RelationRecord, options ImpactPathOptions) (ImpactPathReport, error) {
	report := ImpactPathReport{Policy: "experimental-structural-v1", Limits: options, Results: []ImpactPathResult{}}
	if focus == "" || options.Depth < 0 || options.MaxNodes < 1 || options.MaxEdges < 1 || options.MaxFrontier < 1 || options.MaxOutputSteps < 1 || options.MaxPaths < 1 || options.MaxPaths > 16 || math.IsNaN(options.MinConfidence) || math.IsInf(options.MinConfidence, 0) || options.MinConfidence < 0 || options.MinConfidence > 1 {
		return report, errors.New("invalid impact path options")
	}
	stop := func(code string) {
		for _, existing := range report.StopReasons {
			if existing == code {
				return
			}
		}
		report.StopReasons = append(report.StopReasons, code)
		report.Truncated = true
		report.CountsLowerBounds = true
	}
	if options.GraphPartial {
		stop("incomplete_source_graph")
	}
	terminalTests := map[string]bool{}
	for _, relation := range relations {
		if err := ctx.Err(); err != nil {
			stop("cancellation")
			return report, err
		}
		if relation.Type == "TESTS" {
			terminalTests[relation.FromID] = true
		}
	}
	adjacency := map[string][]impactArc{}
	for _, relation := range relations {
		if err := ctx.Err(); err != nil {
			stop("cancellation")
			return report, err
		}
		direction, mode := impactPathPolicy(relation.Type)
		if direction == "" || relation.FromID == "" || relation.ToID == "" || math.IsNaN(relation.Confidence) || math.IsInf(relation.Confidence, 0) || relation.Confidence < options.MinConfidence || relation.Confidence > 1 {
			continue
		}
		from, to := relation.ToID, relation.FromID
		if direction == "out" {
			from, to = relation.FromID, relation.ToID
		}
		if terminalTests[to] {
			mode = "test"
		}
		evidence := append([]Evidence(nil), relation.Evidence...)
		sort.Slice(evidence, func(i, j int) bool {
			return impactEvidenceKey(evidence[i]) < impactEvidenceKey(evidence[j])
		})
		step := ImpactPathStep{FromID: relation.FromID, ToID: relation.ToID, Relation: relation.Type, Direction: direction, Resolution: relation.Resolution, Confidence: relation.Confidence, Evidence: evidence}
		key := impactStepKey(step)
		adjacency[from] = append(adjacency[from], impactArc{target: to, mode: mode, key: key, step: step})
	}
	for id, arcs := range adjacency {
		if err := ctx.Err(); err != nil {
			stop("cancellation")
			return report, err
		}
		sort.Slice(arcs, func(i, j int) bool {
			a, b := arcs[i], arcs[j]
			if a.mode != b.mode {
				return a.mode < b.mode
			}
			if a.target != b.target {
				return a.target < b.target
			}
			return a.key < b.key
		})
		adjacency[id] = arcs
	}
	states := []impactPredecessor{{id: focus, mode: "dependency", parent: -1, strength: 1}}
	seenNodes := map[string]bool{focus: true}
	best := map[string][]int{}
	pathKey := func(index int) string {
		parts := make([]string, states[index].depth)
		for at, position := index, len(parts)-1; position >= 0; position-- {
			parts[position] = states[at].arc.key
			at = states[at].parent
		}
		return impactIdentity(parts...)
	}
	better := func(a, b int) bool {
		if states[a].strength != states[b].strength {
			return states[a].strength > states[b].strength
		}
		if states[a].depth != states[b].depth {
			return states[a].depth < states[b].depth
		}
		return pathKey(a) < pathKey(b)
	}
	exhausted := false
	frontier := []int{0}
	for len(frontier) > 0 && !exhausted {
		sort.Slice(frontier, func(i, j int) bool {
			a, b := states[frontier[i]], states[frontier[j]]
			if a.mode != b.mode {
				return a.mode < b.mode
			}
			if a.id != b.id {
				return a.id < b.id
			}
			if a.arc.key != b.arc.key {
				return a.arc.key < b.arc.key
			}
			return frontier[i] < frontier[j]
		})
		nextFrontier := []int{}
		for _, cursor := range frontier {
			if exhausted {
				break
			}
			if err := ctx.Err(); err != nil {
				stop("cancellation")
				return report, err
			}
			state := states[cursor]
			if state.mode == "test" || strings.HasPrefix(state.id, "external:") {
				continue
			}
			for _, arc := range adjacency[state.id] {
				if report.ExaminedEdges >= options.MaxEdges {
					stop("edge_bound")
					exhausted = true
					break
				}
				report.ExaminedEdges++
				cyclic := false
				for at := cursor; at >= 0; at = states[at].parent {
					if states[at].id == arc.target && states[at].mode == arc.mode {
						cyclic = true
						break
					}
				}
				if cyclic {
					continue
				}
				if options.Depth > 0 && state.depth >= options.Depth {
					stop("depth_bound")
					continue
				}
				if !seenNodes[arc.target] && len(seenNodes) >= options.MaxNodes {
					stop("node_bound")
					exhausted = true
					break
				}
				if len(states) >= options.MaxFrontier {
					stop("frontier_bound")
					exhausted = true
					break
				}
				next := impactPredecessor{id: arc.target, mode: arc.mode, parent: cursor, depth: state.depth + 1, arc: arc, strength: math.Min(state.strength, arc.step.Confidence)}
				states = append(states, next)
				nextFrontier = append(nextFrontier, len(states)-1)
				seenNodes[arc.target] = true
				path := len(states) - 1
				paths := best[next.id]
				duplicate := false
				for _, existing := range paths {
					if pathKey(existing) == pathKey(path) {
						duplicate = true
						break
					}
				}
				if !duplicate {
					paths = append(paths, path)
					sort.Slice(paths, func(i, j int) bool { return better(paths[i], paths[j]) })
					if len(paths) > options.MaxPaths {
						paths = paths[:options.MaxPaths]
					}
					best[next.id] = paths
				}
			}
		}
		frontier = nextFrontier
	}
	ids := make([]string, 0, len(best))
	for id := range best {
		if id != focus {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	outputSteps := 0
	for _, id := range ids {
		result := ImpactPathResult{ID: id}
		for _, index := range best[id] {
			if err := ctx.Err(); err != nil {
				stop("cancellation")
				return report, err
			}
			state := states[index]
			if state.depth > options.MaxOutputSteps-outputSteps {
				stop("output_path_bound")
				continue
			}
			path := ImpactEvidencePath{WeakestConfidence: state.strength, Steps: make([]ImpactPathStep, state.depth)}
			for at, position := index, state.depth-1; position >= 0; position-- {
				path.Steps[position] = states[at].arc.step
				at = states[at].parent
			}
			outputSteps += state.depth
			result.Paths = append(result.Paths, path)
		}
		if len(result.Paths) > 0 {
			report.Results = append(report.Results, result)
		}
	}
	report.VisitedNodes = len(seenNodes)
	report.AdmittedStates = len(states)
	sort.Strings(report.StopReasons)
	return report, nil
}

// Length prefixes retain byte-exact identities, including non-UTF-8 Git paths;
// JSON replacement of invalid UTF-8 must not merge distinct evidence records.
func impactIdentity(fields ...string) string {
	var out strings.Builder
	for _, field := range fields {
		out.WriteString(strconv.Itoa(len(field)))
		out.WriteByte(':')
		out.WriteString(field)
	}
	return out.String()
}

func impactEvidenceKey(e Evidence) string {
	return impactIdentity(e.Kind, e.FilePath, strconv.Itoa(e.StartLine), strconv.Itoa(e.EndLine), e.Detail)
}

func impactStepKey(step ImpactPathStep) string {
	fields := []string{step.FromID, step.ToID, step.Relation, step.Direction, step.Resolution, strconv.FormatFloat(step.Confidence, 'g', -1, 64), strconv.Itoa(len(step.Evidence))}
	for _, e := range step.Evidence {
		fields = append(fields, impactEvidenceKey(e))
	}
	return impactIdentity(fields...)
}
