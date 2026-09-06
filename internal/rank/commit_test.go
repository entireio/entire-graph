package rank

import (
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// smallConnectedSnapshot is a single symbol (Target) called by two others in
// two different directories, resolved with high confidence -- structurally
// small, but with real, confirmed downstream reach.
func smallConnectedSnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files: []sem.FileRecord{{ID: "f:t", Path: "pkg/core/target.go", Language: "Go"}},
		Symbols: []sem.SymbolRecord{
			{ID: "s:target", Name: "Target", FilePath: "pkg/core/target.go", Language: "Go"},
			{ID: "s:callerA", Name: "CallerA", FilePath: "pkg/a/a.go", Language: "Go"},
			{ID: "s:callerB", Name: "CallerB", FilePath: "pkg/b/b.go", Language: "Go"},
		},
		Relations: []sem.RelationRecord{
			{FromID: "s:callerA", ToID: "s:target", Type: "CALLS", Resolution: "exact", Confidence: 0.95},
			{FromID: "s:callerB", ToID: "s:target", Type: "CALLS", Resolution: "exact", Confidence: 0.95},
		},
	}
}

func smallConnectedDiff() sem.Result {
	return sem.Result{
		Base: "h^", Head: "h",
		Files: []sem.FileChange{{
			Path: "pkg/core/target.go", Status: "M", Language: "Go",
			Changes: []sem.EntityChange{{
				Type: "signature_changed", Kind: "function", Name: "Target",
				DependentsCount: 2, DependentsEvidence: sem.EvidencePartial,
			}},
		}},
	}
}

// largeIsolatedDiff touches n symbols in one file that nothing in the
// snapshot references, all near-identical rewrites (high similarity).
func largeIsolatedDiff(n int) sem.Result {
	changes := make([]sem.EntityChange, 0, n)
	for i := 0; i < n; i++ {
		changes = append(changes, sem.EntityChange{
			Type: "body_changed", Kind: "function", Name: "Unreferenced",
			Similarity: 0.95, DependentsCount: 0, DependentsEvidence: sem.EvidencePartial,
		})
	}
	return sem.Result{
		Base: "h^", Head: "h",
		Files: []sem.FileChange{{Path: "pkg/isolated/big.go", Status: "M", Language: "Go", Changes: changes}},
	}
}

func isolatedSnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files:   []sem.FileRecord{{ID: "f:big", Path: "pkg/isolated/big.go", Language: "Go"}},
		Symbols: []sem.SymbolRecord{{ID: "s:unreferenced", Name: "Unreferenced", FilePath: "pkg/isolated/big.go", Language: "Go"}},
	}
}

// TestAnalyzeCommitConfirmedEvidenceScoresNormally: a commit whose evidence
// is entirely high-precision resolved relations must classify Confirmed and
// produce a real, positive impact score.
func TestAnalyzeCommitConfirmedEvidenceScoresNormally(t *testing.T) {
	t.Parallel()
	a := AnalyzeCommit("c1", time.Now(), smallConnectedDiff(), smallConnectedSnapshot(), DefaultWeights())
	if a.EvidenceState != sem.EvidenceConfirmed {
		t.Fatalf("EvidenceState = %q, want %q", a.EvidenceState, sem.EvidenceConfirmed)
	}
	if a.VerificationRequired {
		t.Fatal("VerificationRequired = true for confirmed evidence")
	}
	if a.ImpactScore <= 0 {
		t.Fatalf("ImpactScore = %v, want > 0", a.ImpactScore)
	}
	if a.AffectedDependents != 2 {
		t.Fatalf("AffectedDependents = %d, want 2", a.AffectedDependents)
	}
	if len(a.Notes) != 0 {
		t.Fatalf("Notes = %v, want none for confirmed evidence", a.Notes)
	}
}

// TestAnalyzeCommitPartialEvidenceProducesScoreAndMarksPartial: evidence
// produced by inference (e.g. type_inferred fan-out) is real, not
// fabricated, so it must still contribute a score, but the result must be
// visibly marked partial.
func TestAnalyzeCommitPartialEvidenceProducesScoreAndMarksPartial(t *testing.T) {
	t.Parallel()
	snapshot := smallConnectedSnapshot()
	snapshot.Relations[0].Resolution = "type_inferred"
	snapshot.Relations[1].Resolution = "type_inferred"
	a := AnalyzeCommit("c1", time.Now(), smallConnectedDiff(), snapshot, DefaultWeights())
	if a.EvidenceState != sem.EvidencePartial {
		t.Fatalf("EvidenceState = %q, want %q", a.EvidenceState, sem.EvidencePartial)
	}
	if a.VerificationRequired {
		t.Fatal("VerificationRequired = true for partial evidence, want false")
	}
	if a.ImpactScore <= 0 {
		t.Fatalf("ImpactScore = %v, want > 0", a.ImpactScore)
	}
	if len(a.Notes) == 0 {
		t.Fatal("partial evidence produced no explanatory note")
	}
}

// TestAnalyzeCommitRequiresVerificationIsNotZeroImpact is the single most
// important behavioral rule in the product spec: an unresolved/degenerate
// analysis must set VerificationRequired, but the score must NOT collapse to
// zero — unresolved is not proof of zero impact.
func TestAnalyzeCommitRequiresVerificationIsNotZeroImpact(t *testing.T) {
	t.Parallel()
	snapshot := smallConnectedSnapshot()
	snapshot.Relations[0].Resolution = "name_only"
	snapshot.Relations[1].Resolution = "name_only"
	a := AnalyzeCommit("c1", time.Now(), smallConnectedDiff(), snapshot, DefaultWeights())
	if a.EvidenceState != sem.EvidenceRequiresVerification {
		t.Fatalf("EvidenceState = %q, want %q", a.EvidenceState, sem.EvidenceRequiresVerification)
	}
	if !a.VerificationRequired {
		t.Fatal("VerificationRequired = false, want true")
	}
	if a.ImpactScore <= 0 {
		t.Fatalf("ImpactScore = %v, want > 0 -- unresolved evidence must not be scored as zero impact", a.ImpactScore)
	}
	// The structural/dependent/architectural/semantic evidence itself must
	// keep its full weight; only EvidenceScore is discounted (see
	// evidenceStateScore). AffectedDependents still reports the 2 relations
	// that were found, even though their resolution could not be confirmed.
	if a.AffectedDependents != 2 {
		t.Fatalf("AffectedDependents = %d, want 2 (found, even if unresolved)", a.AffectedDependents)
	}
	joined := strings.Join(a.Notes, " ")
	if !strings.Contains(joined, "not be interpreted as a complete impact assessment") {
		t.Fatalf("Notes = %v, want a caveat that this is not a complete impact assessment", a.Notes)
	}
}

// TestAnalyzeCommitStructuralImpactBeatsDiffSize is Section 13's "large diff
// vs structural impact" requirement: a commit touching far more symbols but
// reaching nothing else in the graph must NOT automatically outscore a
// commit touching one symbol with real, confirmed downstream reach.
func TestAnalyzeCommitStructuralImpactBeatsDiffSize(t *testing.T) {
	t.Parallel()
	w := DefaultWeights()
	small := AnalyzeCommit("small", time.Now(), smallConnectedDiff(), smallConnectedSnapshot(), w)
	large := AnalyzeCommit("large", time.Now(), largeIsolatedDiff(40), isolatedSnapshot(), w)

	if large.ChangedSymbols <= small.ChangedSymbols {
		t.Fatalf("test setup invalid: large diff (%d) must touch more symbols than small (%d)",
			large.ChangedSymbols, small.ChangedSymbols)
	}
	if large.ImpactScore >= small.ImpactScore {
		t.Fatalf("large, isolated diff (%d symbols, score %.1f) scored >= a small, well-connected one (%d symbols, score %.1f)",
			large.ChangedSymbols, large.ImpactScore, small.ChangedSymbols, small.ImpactScore)
	}
}

// TestAnalyzeCommitInventoryOnlyLanguageRequiresVerification: a language the
// graph only inventories has no relation extraction at all, so a commit in
// it must never present its (necessarily empty) relationship evidence as
// confirmed.
func TestAnalyzeCommitInventoryOnlyLanguageRequiresVerification(t *testing.T) {
	t.Parallel()
	diff := sem.Result{
		Base: "h^", Head: "h",
		Files: []sem.FileChange{{
			Path: "scripts/deploy.4th", Status: "M", Language: "Forth",
			Changes: []sem.EntityChange{{Type: "body_changed", Kind: "function", Name: "Deploy"}},
		}},
	}
	a := AnalyzeCommit("c1", time.Now(), diff, sem.ProviderSnapshot{}, DefaultWeights())
	if !a.VerificationRequired {
		t.Fatalf("inventory-only-language commit VerificationRequired = false, want true: %#v", a)
	}
}
