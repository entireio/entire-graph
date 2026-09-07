package rank

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

// Saturation constants for CommitAnalysis's sub-scores (see saturate). Each is
// the count at which that component is "half credited" (score 50); chosen so
// a handful of touched symbols/dependents/modules already registers as real
// impact, while a commit would need to reach far beyond ordinary change size
// to approach 100 on that component alone. Named so the tuning is visible and
// adjustable in one place instead of buried in expressions.
const (
	// structuralHalfCredit is deliberately much larger than the others: raw
	// changed-symbol count is the metric closest to "diff size", and the
	// product spec is explicit that diff size alone must not win. A high half
	// -credit means even a large, isolated commit (many changed symbols, zero
	// downstream reach) cannot approach the structural component's cap on
	// count alone -- it still needs real dependent/architectural/semantic
	// evidence to score competitively with a small, well-connected change.
	structuralHalfCredit = 20.0
	dependentHalfCredit  = 10.0
	moduleHalfCredit     = 4.0
	reachHalfCredit      = 3.0 // routes + interfaces touched
)

// callRelationTypes, typeRelationTypes, routeRelationTypes, and
// interfaceRelationTypes are the same relation-type vocabulary impact.go and
// neighbors.go already group by (impactCallRelation/impactTypeRelation and
// the HANDLES_* / IMPLEMENTS family) — repeated here as plain string-set
// membership rather than imported, since internal/cli's groupings are
// unexported presentation helpers (byte-budgeted rendering, call-site
// resolution) this package has no use for. The DATA these lists select is
// identical; nothing here computes a relationship the graph did not already
// report.
var (
	callRelationTypes      = map[string]bool{"CALLS": true, "CONSTRUCTS": true, "ASYNC_CALLS": true}
	typeRelationTypes      = map[string]bool{"USES_TYPE": true, "PARAM_TYPE": true, "RETURNS_TYPE": true}
	routeRelationTypes     = map[string]bool{"HANDLES_ROUTE": true, "HANDLES_GRPC": true, "HANDLES_GRAPHQL": true, "HANDLES_TRPC": true, "HANDLES_TOOL": true}
	interfaceRelationTypes = map[string]bool{"IMPLEMENTS": true, "EXTENDS": true, "INHERITS": true}
)

// CommitAnalysis is one commit/PR's Entire-derived engineering-impact
// evidence: the counts a reader can trace a score back to, the sub-scores
// each count normalized into, and the evidence state that says how much of
// this to trust at face value.
type CommitAnalysis struct {
	Commit    string    `json:"commit"`
	Timestamp time.Time `json:"timestamp,omitempty"`

	// ImpactScore is the 0-100 CommitImpactScore: the weighted sum of the five
	// sub-scores below (see Weights).
	ImpactScore float64 `json:"impact_score"`
	// EvidenceState and VerificationRequired reuse the existing evidence-state
	// model (sem.EvidenceState) unchanged: confirmed/partial/requires_verification,
	// with VerificationRequired true only for requires_verification. This is
	// the SAME classification neighbors/impact/diff already expose per relation
	// and per dependents scan — see RelationEvidenceState/DependentsEvidence
	// below for where each contributing signal's state comes from.
	EvidenceState        sem.EvidenceState `json:"evidence_state"`
	VerificationRequired bool              `json:"verification_required"`
	SemanticImpact       string            `json:"semantic_impact"` // "low" | "medium" | "high"

	// Evidence counts a reader can trace ImpactScore back to.
	ChangedSymbols        int `json:"changed_symbols"`
	AffectedRelationships int `json:"affected_relationships"` // total relations consulted: dependents + routes + interfaces + type consumers
	AffectedDependents    int `json:"affected_dependents"`    // resolved callers, plus a heuristic fallback for symbols the current snapshot can no longer resolve (see AnalyzeCommit)
	ModulesAffected       int `json:"modules_affected"`
	RoutesAffected        int `json:"routes_affected"`
	InterfacesAffected    int `json:"interfaces_affected"`

	// Sub-scores (each 0-100), exposed so "why this ImpactScore" is answerable
	// without recomputing anything.
	StructuralScore    float64 `json:"structural_score"`
	DependentScore     float64 `json:"dependent_score"`
	ArchitecturalScore float64 `json:"architectural_score"`
	SemanticScore      float64 `json:"semantic_score"`
	EvidenceScore      float64 `json:"evidence_score"`

	// UnresolvedRelationships counts the relations/dependents signals folded
	// into this analysis that classified below EvidenceConfirmed — i.e. the
	// number Notes' "Entire could not resolve N relationships" refers to.
	UnresolvedRelationships int      `json:"unresolved_relationships"`
	Notes                   []string `json:"notes,omitempty"`
}

// evidenceStateScore maps EvidenceState onto the EvidenceScore sub-score.
// requires_verification is scored 30, not 0: per the product spec, incomplete
// evidence must reduce CONFIDENCE, not masquerade as zero impact — that
// distinction is why EvidenceScore is only 10% of ImpactScore rather than a
// multiplier on the other four components. A commit with real, if uncertain,
// structural/dependent/architectural/semantic evidence keeps that evidence's
// full weight; only the evidence-quality slice itself is discounted.
func evidenceStateScore(state sem.EvidenceState) float64 {
	switch state {
	case sem.EvidenceConfirmed:
		return 100
	case sem.EvidencePartial:
		return 60
	default: // sem.EvidenceRequiresVerification, or an unrecognized/empty state
		return 30
	}
}

// symbolMatch is a changed entity resolved (or not) against the current
// snapshot: the ID(s) it maps to now, if any, plus whether resolution was
// possible at all.
type symbolMatch struct {
	change  sem.EntityChange
	symbols []sem.SymbolRecord
}

// changedSymbolsForFile resolves every EntityChange in a FileChange against
// the current snapshot by (path, name) — the same identity a caller reading
// the diff would look the symbol up by. A rename/move is resolved at its NEW
// path/name, matching where the code actually lives now; a removed symbol
// naturally resolves to nothing (it is gone), which is exactly the case the
// DependentsCount/DependentsEvidence fallback in AnalyzeCommit exists for.
func changedSymbolsForFile(snapshot sem.ProviderSnapshot, file sem.FileChange) []symbolMatch {
	path := file.Path
	if file.OldPath != "" && file.Path == "" {
		path = file.OldPath
	}
	matches := make([]symbolMatch, 0, len(file.Changes))
	for _, change := range file.Changes {
		entityPath := path
		if change.NewPath != "" {
			entityPath = change.NewPath
		}
		name := change.Name
		if change.NewName != "" {
			name = change.NewName
		}
		var symbols []sem.SymbolRecord
		if name != "" {
			for _, s := range snapshot.Symbols {
				if s.FilePath == entityPath && s.Name == name {
					symbols = append(symbols, s)
				}
			}
		}
		matches = append(matches, symbolMatch{change: change, symbols: symbols})
	}
	return matches
}

// commitEvidence accumulates the relation-derived signals a single commit's
// changed symbols touch, gathered in ONE pass over snapshot.Relations (the
// same one-pass-over-the-relation-stream shape impact.go's
// buildImpactResponseFromReader uses) rather than one query per symbol.
type commitEvidence struct {
	resolvedCallerIDs map[string]bool
	routes            int
	interfaces        int
	typeConsumers     int
	externalModules   map[string]bool
	states            []sem.EvidenceState
}

func newCommitEvidence() *commitEvidence {
	return &commitEvidence{
		resolvedCallerIDs: map[string]bool{},
		externalModules:   map[string]bool{},
	}
}

// gatherCommitEvidence walks the snapshot's relation and file streams once,
// classifying every relation that touches a changed symbol or a changed
// file. It never fabricates a relationship: every count comes directly from
// a sem.RelationRecord the graph already produced.
func gatherCommitEvidence(snapshot sem.ProviderSnapshot, changedIDs, changedFileIDs map[string]bool) *commitEvidence {
	evidence := newCommitEvidence()
	if len(changedIDs) == 0 && len(changedFileIDs) == 0 {
		return evidence
	}
	symbolsByID := make(map[string]sem.SymbolRecord, len(snapshot.Symbols))
	for _, s := range snapshot.Symbols {
		symbolsByID[s.ID] = s
	}
	filesByID := make(map[string]sem.FileRecord, len(snapshot.Files))
	for _, f := range snapshot.Files {
		filesByID[f.ID] = f
	}

	for _, r := range snapshot.Relations {
		switch {
		case callRelationTypes[r.Type] && changedIDs[r.ToID] && !changedIDs[r.FromID]:
			// A caller of the changed symbol: the "downstream dependents" signal.
			// !changedIDs[r.FromID] excludes a call from one changed symbol to
			// another IN THE SAME COMMIT — that is internal to the change, not a
			// dependent of it.
			evidence.resolvedCallerIDs[r.FromID] = true
			evidence.states = append(evidence.states, sem.RelationEvidenceState(r))
			if caller, ok := symbolsByID[r.FromID]; ok && caller.FilePath != "" {
				evidence.externalModules[filepath.Dir(caller.FilePath)] = true
			}
		case routeRelationTypes[r.Type] && changedIDs[r.FromID]:
			evidence.routes++
			evidence.states = append(evidence.states, sem.RelationEvidenceState(r))
		case interfaceRelationTypes[r.Type] && (changedIDs[r.FromID] || changedIDs[r.ToID]):
			evidence.interfaces++
			evidence.states = append(evidence.states, sem.RelationEvidenceState(r))
		case typeRelationTypes[r.Type] && (changedIDs[r.FromID] || changedIDs[r.ToID]):
			evidence.typeConsumers++
			evidence.states = append(evidence.states, sem.RelationEvidenceState(r))
		case r.Type == "FILE_CHANGES_WITH" && (changedFileIDs[r.FromID] || changedFileIDs[r.ToID]):
			other := r.ToID
			if changedFileIDs[r.ToID] {
				other = r.FromID
			}
			if f, ok := filesByID[other]; ok && f.Path != "" {
				evidence.externalModules[filepath.Dir(f.Path)] = true
			}
			evidence.states = append(evidence.states, sem.RelationEvidenceState(r))
		}
	}
	return evidence
}

// semanticWeight scores one EntityChange's semantic significance on a 0-1
// scale, drawn only from fields AnalyzeGitRangeWithOptions already computes
// (Type, Similarity) — no invented meaning, no LLM. Higher means the change
// is more likely to be behavior-affecting rather than cosmetic.
func semanticWeight(change sem.EntityChange) float64 {
	switch change.Type {
	case "signature_changed":
		return 1.0 // a call site's contract moved
	case "removed":
		return 0.9 // callers of this may now be broken
	case "added":
		return 0.6 // new capability, but nothing existing depended on it yet
	case "body_changed":
		if change.Similarity > 0 {
			// Near-clone tracking ran: a low similarity to its prior body is a
			// bigger rewrite, so it scores higher than a near-identical edit.
			return clamp(1-change.Similarity, 0.2, 0.9)
		}
		return 0.5
	case "renamed", "moved":
		return 0.3 // identity change; the implementation itself did not move
	default:
		return 0.4
	}
}

// semanticImpactLabel buckets a 0-100 semantic score into the three-tier
// label the product spec's explanation output uses.
func semanticImpactLabel(score float64) string {
	switch {
	case score >= 67:
		return "high"
	case score >= 34:
		return "medium"
	default:
		return "low"
	}
}

// AnalyzeCommit computes one commit's CommitImpactScore from its semantic
// diff (diff) and the repository's current relation graph (snapshot). Both
// are exactly what `entire graph diff`/`impact`/`neighbors` already produce —
// see internal/cli/rank.go for how a real commit is turned into these two
// values. This function does no I/O, which is what makes it directly testable
// against literal fixtures.
func AnalyzeCommit(commit string, timestamp time.Time, diff sem.Result, snapshot sem.ProviderSnapshot, w Weights) CommitAnalysis {
	w = w.Normalize()

	filesByPath := make(map[string]sem.FileRecord, len(snapshot.Files))
	for _, f := range snapshot.Files {
		filesByPath[f.Path] = f
	}

	changedIDs := map[string]bool{}
	changedFileIDs := map[string]bool{}
	var allMatches []symbolMatch
	changedSymbols := 0
	dependentsEvidenceStates := []sem.EvidenceState{}
	languageStates := []sem.EvidenceState{}
	semanticTotal, semanticCount := 0.0, 0

	for _, file := range diff.Files {
		if file.Language != "" {
			languageStates = append(languageStates, sem.LanguageEvidenceState(file.Language))
		}
		if f, ok := filesByPath[file.Path]; ok {
			changedFileIDs[f.ID] = true
		}
		if file.OldPath != "" {
			if f, ok := filesByPath[file.OldPath]; ok {
				changedFileIDs[f.ID] = true
			}
		}
		matches := changedSymbolsForFile(snapshot, file)
		allMatches = append(allMatches, matches...)
		for _, m := range matches {
			changedSymbols++
			semanticTotal += semanticWeight(m.change)
			semanticCount++
			for _, s := range m.symbols {
				changedIDs[s.ID] = true
			}
		}
	}

	evidence := gatherCommitEvidence(snapshot, changedIDs, changedFileIDs)

	dependentsTotal := 0
	for _, m := range allMatches {
		if len(m.symbols) > 0 {
			continue // counted once, globally, via evidence.resolvedCallerIDs below
		}
		dependentsTotal += m.change.DependentsCount
		// A real diff/analyze result always sets DependentsEvidence (see
		// setDependentsEvidence in dependents.go); a zero-value EntityChange
		// literal from a caller that built one by hand does not. Treat an
		// unset state as the documented dependents-scan default (Partial)
		// rather than letting an empty sem.EvidenceState silently win
		// WorstEvidenceState's fold over a real, worse-but-defined state.
		state := m.change.DependentsEvidence
		if state == "" {
			state = sem.EvidencePartial
		}
		dependentsEvidenceStates = append(dependentsEvidenceStates, state)
	}
	dependentsTotal += len(evidence.resolvedCallerIDs)

	affectedRelationships := len(evidence.resolvedCallerIDs) + evidence.routes + evidence.interfaces + evidence.typeConsumers

	structuralScore := saturate(float64(changedSymbols), structuralHalfCredit)
	dependentScore := saturate(float64(dependentsTotal), dependentHalfCredit)
	architecturalScore := 0.6*saturate(float64(len(evidence.externalModules)), moduleHalfCredit) +
		0.4*saturate(float64(evidence.routes+evidence.interfaces), reachHalfCredit)

	semanticAvg := 0.4 // neutral default when a commit changed no resolvable entity
	if semanticCount > 0 {
		semanticAvg = semanticTotal / float64(semanticCount)
	}
	semanticScore := clamp(semanticAvg*100, 0, 100)

	allStates := append([]sem.EvidenceState{}, evidence.states...)
	allStates = append(allStates, dependentsEvidenceStates...)
	allStates = append(allStates, languageStates...)
	worst := sem.WorstEvidenceState(allStates...)
	evidenceScore := evidenceStateScore(worst)

	impact := structuralScore*w.Structural +
		dependentScore*w.Dependents +
		architecturalScore*w.Architectural +
		semanticScore*w.Semantic +
		evidenceScore*w.Evidence

	unresolved := 0
	for _, s := range allStates {
		if s == sem.EvidenceRequiresVerification {
			unresolved++
		}
	}

	analysis := CommitAnalysis{
		Commit:                  commit,
		Timestamp:               timestamp,
		ImpactScore:             clamp(impact, 0, 100),
		EvidenceState:           worst,
		VerificationRequired:    worst == sem.EvidenceRequiresVerification,
		SemanticImpact:          semanticImpactLabel(semanticScore),
		ChangedSymbols:          changedSymbols,
		AffectedRelationships:   affectedRelationships,
		AffectedDependents:      dependentsTotal,
		ModulesAffected:         len(evidence.externalModules),
		RoutesAffected:          evidence.routes,
		InterfacesAffected:      evidence.interfaces,
		StructuralScore:         structuralScore,
		DependentScore:          dependentScore,
		ArchitecturalScore:      architecturalScore,
		SemanticScore:           semanticScore,
		EvidenceScore:           evidenceScore,
		UnresolvedRelationships: unresolved,
	}
	analysis.Notes = commitNotes(analysis)
	return analysis
}

// commitNotes writes the human-readable caveat this commit's evidence state
// earns. Confirmed evidence gets no note — there is nothing to caveat.
// Partial and requires_verification each get a note proportionate to what
// went wrong, always making the same point the product spec insists on:
// unresolved is not zero.
func commitNotes(a CommitAnalysis) []string {
	switch a.EvidenceState {
	case sem.EvidenceConfirmed:
		return nil
	case sem.EvidencePartial:
		return []string{
			"Some evidence behind this score was produced by inference or a documented heuristic " +
				"(e.g. interface fan-out, co-change history) rather than a single resolved reference. " +
				"It is real evidence, not a fabrication, but treat the score as an estimate.",
		}
	default: // requires_verification
		note := "Entire could not resolve one or more relationships with confidence " +
			"(dynamic dispatch, reflection, generated code, or a language it only inventories can all hide real relationships)."
		if a.UnresolvedRelationships > 0 {
			note = pluralizeUnresolved(a.UnresolvedRelationships) + " could not be resolved with confidence " +
				"(dynamic dispatch, reflection, generated code, or a language it only inventories can all hide real relationships)."
		}
		return []string{
			note,
			"The score is based on available evidence only and must not be interpreted as a complete impact assessment " +
				"or as proof that no further relationships exist.",
			"Verify manually: `entire graph rank commit " + a.Commit + " --format json` for the full evidence breakdown, " +
				"`entire graph neighbors --symbol <changed-symbol> --relation CALLS --direction in` for a specific changed " +
				"symbol's callers, or the tests covering the changed files.",
		}
	}
}

func pluralizeUnresolved(n int) string {
	if n == 1 {
		return "1 relationship"
	}
	return strconv.Itoa(n) + " relationships"
}
