package gate

import (
	"fmt"
	"sort"
)

// MaxHops bounds the blast-radius walk. Two is the useful ceiling: one hop
// answers "who calls this", two answers "who is exposed if the caller does not
// absorb the change", and three starts returning most of the repository for any
// symbol a utility function touches.
const MaxHops = 2

// riskFindingLimit caps how many risk findings are reported. The rest are still
// counted in the entity list and the review order; this only bounds the section.
const riskFindingLimit = 10

// Risk annotates each entity with its graph dependent count and reports the
// breaking changes that have dependents.
//
// Only breaking changes open a finding. A body change with a hundred callers is
// not a risk finding: its signature still holds, so the callers still compile
// and still mean what they meant. Reporting it would bury the changes that can
// actually break a caller.
//
// Entities are annotated in place; the returned findings are the reportable
// subset, most dependents first.
func Risk(entities []ChangedEntity, ix *Index, hops int) []Finding {
	if hops < 1 {
		hops = 1
	}
	if hops > MaxHops {
		hops = MaxHops
	}

	var findings []Finding
	for i := range entities {
		entity := &entities[i]
		ids := ix.Resolve(entity.Name, entity.Path)
		if len(ids) == 0 {
			continue
		}
		if entity.SymbolID == "" {
			entity.SymbolID = ids[0]
		}

		dependents := ix.Dependents(ids, hops)
		entity.Dependents = len(dependents)
		if !entity.ChangeType.Breaking() || len(dependents) == 0 {
			continue
		}

		findings = append(findings, Finding{
			Dimension: DimRisk,
			Subject:   entity.Anchor,
			Summary: fmt.Sprintf("%s %s has %d dependent(s) within %d hop(s)",
				entity.Kind, entity.ChangeType, len(dependents), hops),
			Evidence: dependentEvidence(dependents),
		})
	}

	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if len(a.Evidence) != len(b.Evidence) {
			return len(a.Evidence) > len(b.Evidence)
		}
		if a.Subject.Path != b.Subject.Path {
			return a.Subject.Path < b.Subject.Path
		}
		return a.Subject.Name < b.Subject.Name
	})
	if len(findings) > riskFindingLimit {
		findings = findings[:riskFindingLimit]
	}
	return findings
}

// evidencePerFinding bounds how many dependents are named under one finding.
// The count in the summary stays exact; this only limits the listing.
const evidencePerFinding = 5

func dependentEvidence(dependents []Symbol) []string {
	shown := dependents
	if len(shown) > evidencePerFinding {
		shown = shown[:evidencePerFinding]
	}
	evidence := make([]string, 0, len(shown)+1)
	for _, d := range shown {
		if d.Path == "" {
			evidence = append(evidence, d.Name+" (unresolved)")
			continue
		}
		evidence = append(evidence, fmt.Sprintf("%s @ %s:%d", d.Name, d.Path, d.Line))
	}
	if rest := len(dependents) - len(shown); rest > 0 {
		evidence = append(evidence, fmt.Sprintf("... and %d more", rest))
	}
	return evidence
}
