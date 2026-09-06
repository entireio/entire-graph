package rank

import (
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// Demo builds the deterministic, three-developer fixture the product spec
// asks for (Section 12): every commit runs through the SAME AnalyzeCommit and
// AggregateDeveloper this package uses for a real repository — nothing here
// is a hardcoded score — so the demo is a genuine exercise of the scoring
// pipeline, not a canned result.
//
// It exists so `entire graph rank demo` produces a convincing, reproducible
// result with no repository, network access, or setup: exactly what a
// hackathon demo needs.
//
//   - Developer A ("alice"): a small commit to a heavily-depended-on,
//     multi-module-reaching symbol. Few changed symbols, high dependent and
//     architectural reach.
//   - Developer B ("bob"): a large commit (many changed symbols) confined to
//     one file nothing else in the snapshot references. Demonstrates that
//     diff size alone does not win: despite touching far more symbols than
//     alice, bob's dependent/architectural/semantic signals stay near zero.
//   - Developer C ("carol"): a commit reaching real downstream relationships,
//     but every one of them is the shape Entire could not resolve with
//     confidence (a bare name-only match). Demonstrates that incomplete
//     evidence still produces a real score with a verification path — not a
//     confirmed zero.
func Demo(w Weights) []DeveloperProfile {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	snapshot := demoSnapshot()

	alice := AnalyzeCommit("a1ice000001", now.AddDate(0, 0, -2), demoCommitAlice(), snapshot, w)
	bob := AnalyzeCommit("b0b0000001", now.AddDate(0, 0, -1), demoCommitBob(), snapshot, w)
	carol := AnalyzeCommit("ca40l00001", now.AddDate(0, 0, -5), demoCommitCarol(), snapshot, w)

	return []DeveloperProfile{
		// Stars/PR counts are deliberately modest and close together: the point
		// of this fixture is the ENGINEERING IMPACT spread (see the comment
		// above), not a base-score blowout burying it. bob's larger PR share
		// still gives him a slightly higher base score than alice -- exactly
		// the "GitHub reach is still a real, if secondary, signal" case -- but
		// his near-zero engineering impact keeps him from winning overall.
		AggregateDeveloper("alice", 420, 6, 40, []CommitAnalysis{alice}, w, now),
		AggregateDeveloper("bob", 300, 22, 40, []CommitAnalysis{bob}, w, now),
		AggregateDeveloper("carol", 90, 3, 40, []CommitAnalysis{carol}, w, now),
	}
}

// demoSnapshot is a small, plausible multi-module app: an auth package a
// billing handler and an admin API route both depend on, plus an interface
// implementation, so alice's and carol's single-symbol changes have
// real, multi-module relationships to be measured against.
func demoSnapshot() sem.ProviderSnapshot {
	return sem.ProviderSnapshot{
		Files: []sem.FileRecord{
			{ID: "f:auth", Path: "internal/auth/token.go", Language: "Go"},
			{ID: "f:billing", Path: "internal/billing/charge.go", Language: "Go"},
			{ID: "f:api", Path: "internal/api/admin.go", Language: "Go"},
			{ID: "f:notifier", Path: "internal/notify/notifier.go", Language: "Go"},
			{ID: "f:legacy", Path: "internal/legacy/formatting.go", Language: "Go"},
			{ID: "f:reporting", Path: "internal/reporting/report.go", Language: "Go"},
		},
		Symbols: []sem.SymbolRecord{
			{ID: "s:ValidateToken", Name: "ValidateToken", QualifiedName: "auth.ValidateToken", Kind: "function", FilePath: "internal/auth/token.go", Language: "Go"},
			{ID: "s:ChargeCard", Name: "ChargeCard", QualifiedName: "billing.ChargeCard", Kind: "function", FilePath: "internal/billing/charge.go", Language: "Go"},
			{ID: "s:AdminHandler", Name: "AdminHandler", QualifiedName: "api.AdminHandler", Kind: "function", FilePath: "internal/api/admin.go", Language: "Go"},
			{ID: "s:Notifier", Name: "Notifier", QualifiedName: "notify.Notifier", Kind: "interface", FilePath: "internal/notify/notifier.go", Language: "Go"},
			{ID: "s:EmailNotifier", Name: "EmailNotifier", QualifiedName: "notify.EmailNotifier", Kind: "struct", FilePath: "internal/notify/notifier.go", Language: "Go"},
			// legacy formatting helpers: bob's large, isolated commit touches
			// symbols shaped like these that nothing else in the snapshot calls.
			{ID: "s:padLeft", Name: "padLeft", QualifiedName: "legacy.padLeft", Kind: "function", FilePath: "internal/legacy/formatting.go", Language: "Go"},
			{ID: "s:ReportHandler", Name: "ReportHandler", QualifiedName: "reporting.ReportHandler", Kind: "function", FilePath: "internal/reporting/report.go", Language: "Go"},
		},
		Relations: []sem.RelationRecord{
			// alice's ValidateToken is called from billing and the admin API,
			// each in a different module -- real, resolved dependent/reach evidence.
			{FromID: "s:ChargeCard", ToID: "s:ValidateToken", Type: "CALLS", Resolution: "exact", Confidence: 0.95},
			{FromID: "s:AdminHandler", ToID: "s:ValidateToken", Type: "CALLS", Resolution: "exact", Confidence: 0.95},
			{FromID: "s:AdminHandler", ToID: "external:route:/admin", Type: "HANDLES_ROUTE", Resolution: "pattern", Confidence: 0.7, RelationScope: "external", TargetKind: "route"},
			{FromID: "s:EmailNotifier", ToID: "s:Notifier", Type: "IMPLEMENTS", Resolution: "exact", Confidence: 0.9},
			// carol's Notifier.Send is reached only through relationships the
			// graph could confirm nothing stronger than a bare name match for --
			// e.g. a dynamically dispatched call through an interface value built
			// at runtime -- so RelationEvidenceState classifies these
			// requires_verification even though they are real evidence.
			{FromID: "s:ChargeCard", ToID: "s:Notifier", Type: "CALLS", Resolution: "name_only", Confidence: 0.4},
			{FromID: "s:AdminHandler", ToID: "s:Notifier", Type: "CALLS", Resolution: "name_only", Confidence: 0.4},
			{FromID: "s:ReportHandler", ToID: "s:Notifier", Type: "CALLS", Resolution: "name_only", Confidence: 0.4},
		},
	}
}

// demoCommitAlice: one small, high-leverage change. Renaming ValidateToken's
// signature is exactly the shape that breaks callers, which is why
// "signature_changed" carries the highest semanticWeight.
func demoCommitAlice() sem.Result {
	return sem.Result{
		Base: "a1ice000001^", Head: "a1ice000001",
		Files: []sem.FileChange{{
			Path: "internal/auth/token.go", Status: "M", Language: "Go",
			Changes: []sem.EntityChange{{
				Type: "signature_changed", Kind: "function", Name: "ValidateToken",
				OldSignature:    "func ValidateToken(token string) bool",
				NewSignature:    "func ValidateToken(token string) (bool, error)",
				DependentsCount: 2, DependentsEvidence: sem.EvidencePartial,
			}},
		}},
	}
}

// demoCommitBob: a large, self-contained commit -- many changed symbols, all
// in one file nothing else in the snapshot references, all cosmetic
// (body_changed with high similarity). Demonstrates diff size without reach.
func demoCommitBob() sem.Result {
	changes := make([]sem.EntityChange, 0, 18)
	for i := 0; i < 18; i++ {
		changes = append(changes, sem.EntityChange{
			Type: "body_changed", Kind: "function", Name: "padLeft",
			Similarity:      0.94, // near-identical rewrite: reformatting, not restructuring
			DependentsCount: 0, DependentsEvidence: sem.EvidencePartial,
		})
	}
	return sem.Result{
		Base: "b0b0000001^", Head: "b0b0000001",
		Files: []sem.FileChange{{
			Path: "internal/legacy/formatting.go", Status: "M", Language: "Go",
			Changes: changes,
		}},
	}
}

// demoCommitCarol: reaches real downstream relationships (Notifier, called
// from two other modules), but every relation reaching it is a bare
// name-only match -- the shape a dynamically-dispatched notifier value
// leaves behind. Structural/dependent evidence is real; its confidence is not.
func demoCommitCarol() sem.Result {
	return sem.Result{
		Base: "ca40l00001^", Head: "ca40l00001",
		Files: []sem.FileChange{{
			Path: "internal/notify/notifier.go", Status: "M", Language: "Go",
			Changes: []sem.EntityChange{
				{
					Type: "body_changed", Kind: "interface", Name: "Notifier",
					Similarity:      0.4,
					DependentsCount: 3, DependentsEvidence: sem.EvidenceRequiresVerification,
				},
				{
					Type: "body_changed", Kind: "struct", Name: "EmailNotifier",
					Similarity:      0.5,
					DependentsCount: 0, DependentsEvidence: sem.EvidencePartial,
				},
			},
		}},
	}
}
