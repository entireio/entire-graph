package rank

import (
	"math"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// DeveloperProfile is the final, explainable output of the ranking pipeline:
// GitHub reach (BaseScore, preserved from the original Hacker House formula)
// combined with Entire's engineering-impact evidence
// (EngineeringImpact/Commits), traceable all the way down to individual
// CommitAnalysis records.
type DeveloperProfile struct {
	Username string `json:"username"`
	Stars    int    `json:"stars"`
	UserPRs  int    `json:"user_prs"`
	TotalPRs int    `json:"total_prs"`

	// BaseScore is the untouched GitHub reach signal: min(stars *
	// (userPRs/totalPRs), 10000). NormalizedBaseScore rescales it onto 0-100
	// (see NormalizeBaseScore) so it can be combined with EngineeringImpact.
	BaseScore           float64 `json:"base_score"`
	NormalizedBaseScore float64 `json:"normalized_base_score"`
	// EngineeringImpact is the recency-weighted average of the developer's
	// analyzed commits' ImpactScore (see AggregateDeveloper) — the Entire-
	// derived signal that, per the product spec, dominates FinalScore.
	EngineeringImpact float64 `json:"engineering_impact"`
	// FinalScore combines the two above using Weights (see FinalScore).
	FinalScore float64 `json:"final_score"`

	CommitsAnalyzed int `json:"commits_analyzed"`
	// EvidenceState is the worst state among the developer's analyzed commits
	// (sem.WorstEvidenceState) — see CommitAnalysis.EvidenceState for what
	// contributes to each commit's state. A developer with zero analyzed
	// commits reports EvidenceConfirmed: there is no uncertain evidence to
	// flag, only an absence of engineering-impact evidence, which
	// CommitsAnalyzed == 0 already communicates on its own.
	EvidenceState        sem.EvidenceState `json:"evidence_state"`
	VerificationRequired bool              `json:"verification_required"`

	// Aggregate evidence counts (sums across Commits), for the "why this
	// score" bullet list — see Explain.
	ChangedSymbols        int    `json:"changed_symbols"`
	AffectedRelationships int    `json:"affected_relationships"`
	AffectedDependents    int    `json:"affected_dependents"`
	ModulesAffected       int    `json:"modules_affected"`
	RoutesAffected        int    `json:"routes_affected"`
	InterfacesAffected    int    `json:"interfaces_affected"`
	SemanticImpact        string `json:"semantic_impact"`

	Commits []CommitAnalysis `json:"commits,omitempty"`
}

// AggregateDeveloper folds a developer's analyzed commits into a
// DeveloperProfile: engineering impact, evidence state, and the evidence
// counts Explain reports. stars/userPRs/totalPRs feed BaseScore directly
// (Section 3's preserved formula); commits is normally sourced from
// AnalyzeCommit, one call per PR/commit the caller chose to analyze.
//
// now is the reference time for recency weighting (see Weights.RecencyHalfLifeDays);
// pass time.Now() in production code and a fixed value in tests so results are
// deterministic.
func AggregateDeveloper(username string, stars, userPRs, totalPRs int, commits []CommitAnalysis, w Weights, now time.Time) DeveloperProfile {
	w = w.Normalize()
	baseScore := BaseScore(stars, userPRs, totalPRs)
	profile := DeveloperProfile{
		Username:            username,
		Stars:               stars,
		UserPRs:             userPRs,
		TotalPRs:            totalPRs,
		BaseScore:           baseScore,
		NormalizedBaseScore: NormalizeBaseScore(baseScore),
		CommitsAnalyzed:     len(commits),
		EvidenceState:       sem.EvidenceConfirmed,
		Commits:             commits,
	}

	if len(commits) == 0 {
		profile.FinalScore = FinalScore(profile.NormalizedBaseScore, 0, w)
		return profile
	}

	weightedSum, weightTotal := 0.0, 0.0
	states := make([]sem.EvidenceState, 0, len(commits))
	for _, c := range commits {
		weight := recencyWeight(c.Timestamp, now, w.RecencyHalfLifeDays)
		weightedSum += c.ImpactScore * weight
		weightTotal += weight
		states = append(states, c.EvidenceState)

		profile.ChangedSymbols += c.ChangedSymbols
		profile.AffectedRelationships += c.AffectedRelationships
		profile.AffectedDependents += c.AffectedDependents
		profile.ModulesAffected += c.ModulesAffected
		profile.RoutesAffected += c.RoutesAffected
		profile.InterfacesAffected += c.InterfacesAffected
	}
	if weightTotal == 0 {
		// Every commit had zero recency weight (e.g. every timestamp is far
		// older than the half-life, or all timestamps are unknown and recency
		// weighting is disabled some other way) — fall back to an unweighted
		// mean rather than dividing by zero. One enormous commit still cannot
		// dominate here: it is one term in an average of CommitsAnalyzed terms,
		// each already capped to [0,100] by AnalyzeCommit's saturating
		// normalization.
		for _, c := range commits {
			weightedSum += c.ImpactScore
			weightTotal++
		}
	}

	profile.EngineeringImpact = clamp(weightedSum/weightTotal, 0, 100)
	profile.EvidenceState = sem.WorstEvidenceState(states...)
	profile.VerificationRequired = profile.EvidenceState == sem.EvidenceRequiresVerification
	profile.SemanticImpact = dominantSemanticImpact(commits)
	profile.FinalScore = FinalScore(profile.NormalizedBaseScore, profile.EngineeringImpact, w)
	return profile
}

// recencyWeight halves a commit's contribution every halfLifeDays of age.
// halfLifeDays <= 0 or an unknown timestamp both mean "no recency signal" and
// weight the commit as 1 (an ordinary, unweighted term in the average).
func recencyWeight(commitTime, now time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 || commitTime.IsZero() {
		return 1
	}
	age := now.Sub(commitTime).Hours() / 24
	if age < 0 {
		age = 0 // a commit timestamped after `now` (clock skew) is not penalized or boosted
	}
	return math.Pow(0.5, age/halfLifeDays)
}

// dominantSemanticImpact picks the developer-level SemanticImpact label as
// the most common label among their analyzed commits (ties broken toward the
// higher-impact label), which is more representative of a developer's body
// of work than an average of already-bucketed strings would be.
func dominantSemanticImpact(commits []CommitAnalysis) string {
	counts := map[string]int{}
	for _, c := range commits {
		counts[c.SemanticImpact]++
	}
	best, bestCount := "low", -1
	for _, label := range []string{"high", "medium", "low"} {
		if counts[label] > bestCount {
			best, bestCount = label, counts[label]
		}
	}
	return best
}
