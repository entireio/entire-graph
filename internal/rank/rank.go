// Package rank is the Hacker House developer-ranking domain: it turns the
// existing GitHub reach signal (stars * PR share) and Entire Graph's existing
// impact/neighbors/semantic-diff evidence into one explainable developer
// score.
//
// This package holds no I/O. Every function here is a pure transform over
// data the rest of entire-graph already computes (sem.Result from
// AnalyzeGitRangeWithOptions, sem.ProviderSnapshot from
// BuildProviderSnapshot, and sem.EvidenceState/RelationEvidenceState from the
// evidence-state work). The CLI wiring in internal/cli/rank.go is the only
// place that touches git or the filesystem; that split keeps every rule in
// this file testable with literal fixtures, the same way impact_test.go and
// neighbors_evidence_test.go test the CLI package.
//
// Entire is the EVIDENCE PROVIDER here, not an oracle: nothing in this
// package invents a relationship the graph did not report, and nothing here
// turns "the graph could not resolve this" into "this does not exist" — see
// evidenceScore and sem.EvidenceState for how that distinction survives all
// the way to the final score.
package rank

import "math"

// Weights configures every ratio this package uses, named so none of them is
// a magic number buried in an expression. DefaultWeights returns the values
// specified for the Hacker House ranking concept; callers that want a
// different mix (e.g. to weight recent commits harder, or to trust GitHub
// reach less) construct their own Weights instead of editing constants.
type Weights struct {
	// Commit-impact sub-weights (CommitImpactScore). Conventionally sum to 1.0;
	// Normalize rescales them if they do not.
	Structural    float64 // affected symbols/files
	Dependents    float64 // downstream dependents/callers
	Architectural float64 // architectural reach (modules, routes, interfaces)
	Semantic      float64 // semantic change significance
	Evidence      float64 // evidence quality/completeness

	// Final-score weights (base GitHub reach vs. Entire engineering impact).
	// Conventionally sum to 1.0; Normalize rescales them if they do not.
	BaseReach         float64
	EngineeringImpact float64

	// RecencyHalfLifeDays halves a commit's weight in the developer aggregate
	// every N days of age. Zero disables recency weighting (a plain average),
	// which is also what AggregateDeveloper falls back to when a commit's
	// timestamp is unknown.
	RecencyHalfLifeDays float64
}

// DefaultWeights is the weighting the product spec asks for: 30/25/20/15/10
// for commit-impact components, 40/60 base-reach/engineering-impact for the
// final score, and a 30-day recency half-life so a developer's profile
// reflects their recent work more than a commit from a year ago without
// discarding history entirely.
func DefaultWeights() Weights {
	return Weights{
		Structural:    0.30,
		Dependents:    0.25,
		Architectural: 0.20,
		Semantic:      0.15,
		Evidence:      0.10,

		BaseReach:         0.40,
		EngineeringImpact: 0.60,

		RecencyHalfLifeDays: 30,
	}
}

// Normalize rescales the two weight groups so each sums to 1.0, leaving
// Weights unchanged if they already do (up to floating-point noise) or if a
// group sums to zero (nothing to rescale by — the caller gets back what it
// gave, which will simply score everything in that group as 0).
func (w Weights) Normalize() Weights {
	commitSum := w.Structural + w.Dependents + w.Architectural + w.Semantic + w.Evidence
	if commitSum > 0 {
		w.Structural /= commitSum
		w.Dependents /= commitSum
		w.Architectural /= commitSum
		w.Semantic /= commitSum
		w.Evidence /= commitSum
	}
	finalSum := w.BaseReach + w.EngineeringImpact
	if finalSum > 0 {
		w.BaseReach /= finalSum
		w.EngineeringImpact /= finalSum
	}
	return w
}

// saturate maps a non-negative count onto 0-100 with diminishing returns:
// x=0 -> 0, x=k -> 50, x->infinity -> 100. This is the normalization the
// product spec asks for ("a huge repository or huge commit does not
// automatically dominate the ranking") without needing corpus-wide
// percentile statistics, which a hackathon-scale, single-repo ranking run
// has no population to compute from. k is the count at which a component is
// "half credited"; callers pick it per metric (see commit.go).
func saturate(x, k float64) float64 {
	if x <= 0 || k <= 0 {
		return 0
	}
	return 100 * x / (x + k)
}

// clamp bounds a value to [low, high].
func clamp(x, low, high float64) float64 {
	return math.Max(low, math.Min(high, x))
}
