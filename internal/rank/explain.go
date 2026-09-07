package rank

import (
	"fmt"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

// Explain renders the developer-level trace the product spec asks for:
// Developer -> Final Score -> Engineering Impact -> Commit -> Entire Graph
// evidence, in one bounded block a CLI can print directly.
func (p DeveloperProfile) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Developer: %s\n\n", p.Username)
	fmt.Fprintf(&b, "Final Score: %.1f\n\n", p.FinalScore)
	fmt.Fprintf(&b, "Base GitHub Score: %.0f / 10000 (%.1f / 100 normalized)\n", p.BaseScore, p.NormalizedBaseScore)
	fmt.Fprintf(&b, "Engineering Impact: %.1f / 100\n\n", p.EngineeringImpact)

	if p.CommitsAnalyzed == 0 {
		b.WriteString("0 commits analyzed -- engineering impact is not yet established for this developer.\n")
		b.WriteString("Final Score reflects GitHub reach only until at least one commit is analyzed.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%d commit%s analyzed\n\n", p.CommitsAnalyzed, pluralSuffix(p.CommitsAnalyzed))
	b.WriteString("Entire evidence:\n")
	fmt.Fprintf(&b, "  %d changed symbols\n", p.ChangedSymbols)
	fmt.Fprintf(&b, "  %d affected relationships\n", p.AffectedRelationships)
	fmt.Fprintf(&b, "  %d modules affected\n", p.ModulesAffected)
	fmt.Fprintf(&b, "  %d downstream dependents\n", p.AffectedDependents)
	if p.RoutesAffected > 0 {
		fmt.Fprintf(&b, "  %d API routes affected\n", p.RoutesAffected)
	}
	if p.InterfacesAffected > 0 {
		fmt.Fprintf(&b, "  %d interfaces/implementations affected\n", p.InterfacesAffected)
	}
	fmt.Fprintf(&b, "  Semantic impact: %s\n\n", strings.ToUpper(p.SemanticImpact[:1])+p.SemanticImpact[1:])

	fmt.Fprintf(&b, "Evidence: %s\n", strings.ToUpper(string(p.EvidenceState)))
	if note := aggregateEvidenceNote(p.Commits); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
		b.WriteString("\n")
	}
	return b.String()
}

// Explain renders one commit's evidence breakdown: score, the counts it is
// traceable to, and — when relevant — the verification caveat.
func (c CommitAnalysis) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Commit: %s\n", shortCommit(c.Commit))
	fmt.Fprintf(&b, "Impact Score: %.1f\n\n", c.ImpactScore)
	fmt.Fprintf(&b, "Changed symbols: %d\n", c.ChangedSymbols)
	fmt.Fprintf(&b, "Affected dependents: %d\n", c.AffectedDependents)
	fmt.Fprintf(&b, "Modules affected: %d\n", c.ModulesAffected)
	if c.RoutesAffected > 0 {
		fmt.Fprintf(&b, "Routes affected: %d\n", c.RoutesAffected)
	}
	if c.InterfacesAffected > 0 {
		fmt.Fprintf(&b, "Interfaces affected: %d\n", c.InterfacesAffected)
	}
	fmt.Fprintf(&b, "Semantic impact: %s\n\n", strings.ToUpper(c.SemanticImpact))
	fmt.Fprintf(&b, "Evidence: %s\n", strings.ToUpper(string(c.EvidenceState)))
	fmt.Fprintf(&b, "Verification required: %s\n", yesNo(c.VerificationRequired))
	if len(c.Notes) > 0 {
		b.WriteString("\n")
		for _, note := range c.Notes {
			b.WriteString(note)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// aggregateEvidenceNote picks one representative note from the developer's
// commits to surface at the profile level: the note attached to the WORST
// evidence state present, since that is the caveat a reader most needs before
// trusting EngineeringImpact at face value.
func aggregateEvidenceNote(commits []CommitAnalysis) string {
	var best *CommitAnalysis
	for i := range commits {
		c := &commits[i]
		if len(c.Notes) == 0 {
			continue
		}
		if best == nil || evidenceRankOrder(c.EvidenceState) > evidenceRankOrder(best.EvidenceState) {
			best = c
		}
	}
	if best == nil {
		return ""
	}
	return strings.Join(best.Notes, " ")
}

func evidenceRankOrder(s sem.EvidenceState) int {
	switch s {
	case sem.EvidenceConfirmed:
		return 0
	case sem.EvidencePartial:
		return 1
	default:
		return 2
	}
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func yesNo(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
