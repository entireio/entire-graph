package rank

import (
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

func commitWithScore(commit string, score float64, state sem.EvidenceState, when time.Time) CommitAnalysis {
	return CommitAnalysis{
		Commit: commit, ImpactScore: score, EvidenceState: state,
		VerificationRequired: state == sem.EvidenceRequiresVerification,
		SemanticImpact:       "medium", Timestamp: when,
	}
}

func TestAggregateDeveloperZeroCommitsHasConfirmedStateAndZeroImpact(t *testing.T) {
	t.Parallel()
	now := time.Now()
	p := AggregateDeveloper("dev", 100, 5, 10, nil, DefaultWeights(), now)
	if p.CommitsAnalyzed != 0 {
		t.Fatalf("CommitsAnalyzed = %d, want 0", p.CommitsAnalyzed)
	}
	if p.EngineeringImpact != 0 {
		t.Fatalf("EngineeringImpact = %v, want 0", p.EngineeringImpact)
	}
	if p.EvidenceState != sem.EvidenceConfirmed {
		t.Fatalf("EvidenceState = %q, want %q (nothing analyzed, nothing to distrust)", p.EvidenceState, sem.EvidenceConfirmed)
	}
	// BaseScore must still be computed even with no commits analyzed yet.
	if p.BaseScore != BaseScore(100, 5, 10) {
		t.Fatalf("BaseScore = %v, want %v", p.BaseScore, BaseScore(100, 5, 10))
	}
}

func TestAggregateDeveloperEvidenceStateIsWorstAcrossCommits(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commits := []CommitAnalysis{
		commitWithScore("c1", 80, sem.EvidenceConfirmed, now),
		commitWithScore("c2", 70, sem.EvidencePartial, now),
		commitWithScore("c3", 60, sem.EvidenceRequiresVerification, now),
	}
	p := AggregateDeveloper("dev", 100, 5, 10, commits, DefaultWeights(), now)
	if p.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("EvidenceState = %q, want %q", p.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if !p.VerificationRequired {
		t.Fatal("VerificationRequired = false, want true")
	}
	if p.CommitsAnalyzed != 3 {
		t.Fatalf("CommitsAnalyzed = %d, want 3", p.CommitsAnalyzed)
	}
}

// TestAggregateDeveloperManyCommitsAreNotDominatedByOneOutlier: a weighted
// AVERAGE, not a sum, means one enormous commit score cannot single-handedly
// carry a developer's whole profile past what their other commits support.
func TestAggregateDeveloperManyCommitsAreNotDominatedByOneOutlier(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commits := []CommitAnalysis{commitWithScore("huge", 100, sem.EvidenceConfirmed, now)}
	for i := 0; i < 9; i++ {
		commits = append(commits, commitWithScore("small", 10, sem.EvidenceConfirmed, now))
	}
	// Disable recency weighting so this test isolates the averaging behavior.
	w := DefaultWeights()
	w.RecencyHalfLifeDays = 0
	p := AggregateDeveloper("dev", 0, 0, 1, commits, w, now)
	if p.EngineeringImpact >= 50 {
		t.Fatalf("EngineeringImpact = %v, one outlier commit dominated a 10-commit average", p.EngineeringImpact)
	}
	want := (100.0 + 10*9) / 10 // = 19
	if diff := p.EngineeringImpact - want; diff > 0.01 || diff < -0.01 {
		t.Fatalf("EngineeringImpact = %v, want %v (plain average, no recency weighting)", p.EngineeringImpact, want)
	}
}

// TestAggregateDeveloperRecencyWeightingFavorsRecentCommits: with recency
// weighting enabled, a recent low-scoring commit and an old high-scoring one
// must average closer to the recent commit than an unweighted mean would.
func TestAggregateDeveloperRecencyWeightingFavorsRecentCommits(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commits := []CommitAnalysis{
		commitWithScore("old", 90, sem.EvidenceConfirmed, now.AddDate(0, 0, -365)),
		commitWithScore("recent", 10, sem.EvidenceConfirmed, now),
	}
	w := DefaultWeights() // RecencyHalfLifeDays: 30
	p := AggregateDeveloper("dev", 0, 0, 1, commits, w, now)
	unweightedMean := 50.0
	if p.EngineeringImpact >= unweightedMean {
		t.Fatalf("EngineeringImpact = %v, want it pulled below the unweighted mean (%v) toward the recent commit's score",
			p.EngineeringImpact, unweightedMean)
	}
}

// TestAggregateDeveloperEvidenceCountsSumAcrossCommits pins the developer-
// level "Entire evidence" bullet list (Section 9's explainability example) as
// a straightforward sum of each analyzed commit's counts.
func TestAggregateDeveloperEvidenceCountsSumAcrossCommits(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commits := []CommitAnalysis{
		{Commit: "c1", ChangedSymbols: 3, AffectedRelationships: 5, ModulesAffected: 2, AffectedDependents: 4, EvidenceState: sem.EvidenceConfirmed, SemanticImpact: "high"},
		{Commit: "c2", ChangedSymbols: 2, AffectedRelationships: 1, ModulesAffected: 1, AffectedDependents: 2, EvidenceState: sem.EvidenceConfirmed, SemanticImpact: "high"},
	}
	p := AggregateDeveloper("dev", 0, 0, 1, commits, DefaultWeights(), now)
	if p.ChangedSymbols != 5 || p.AffectedRelationships != 6 || p.ModulesAffected != 3 || p.AffectedDependents != 6 {
		t.Fatalf("aggregate evidence counts = %+v, want ChangedSymbols=5 AffectedRelationships=6 ModulesAffected=3 AffectedDependents=6", p)
	}
}
