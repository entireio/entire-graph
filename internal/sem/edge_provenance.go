package sem

import "encoding/json"

// Edge provenance.
//
// Every relation already carries the evidence for how it was established:
// Confidence, Resolution, Reason, Evidence and WarningCodes. What it did not
// carry was the conclusion — a single label saying whether the edge is a
// reference the parser actually resolved, a pattern match that stands in for
// one, or a reference it could see but not pin down.
//
// That difference decides whether a result can be acted on. "X calls Y" read
// off a resolved symbol and "X probably calls Y because the route string
// matched" are not the same claim, and a consumer that cannot tell them apart
// either trusts too much or discards too much. Graphify tags every edge
// EXTRACTED / INFERRED / AMBIGUOUS for this reason.
//
// This is a DERIVATION, not new analysis. It computes nothing the provider did
// not already know; it states the conclusion so a caller can filter on it
// without reimplementing the rules — and reimplementing them is exactly how two
// consumers end up disagreeing about what the same edge means.
const (
	// ProvenanceExtracted: the parser resolved a real reference. The target was
	// identified exactly, through the import graph, or as a definite symbol.
	ProvenanceExtracted = "EXTRACTED"

	// ProvenanceInferred: the edge exists because a pattern said so, not
	// because a reference was resolved. Route handlers, HTTP calls matched by
	// shape, emitted events, test association. These are real signals and they
	// are not the same kind of fact as a resolved call.
	ProvenanceInferred = "INFERRED"

	// ProvenanceAmbiguous: a reference was seen but not pinned down — resolved
	// only to a name, a package, a file, or not at all. The edge is evidence
	// that something points somewhere; it is not evidence of the target.
	ProvenanceAmbiguous = "AMBIGUOUS"
)

// heuristicEdgeTypes are the relation types that exist by pattern rather than
// by resolution. It mirrors the list reported in capabilities as
// HeuristicRelationTypes; the two are asserted equal by test, so the provenance
// label and the advertised capability can never drift apart.
var heuristicEdgeTypes = map[string]bool{
	"HANDLES_ROUTE": true,
	"HTTP_CALLS":    true,
	"EMITS":         true,
	"LISTENS_ON":    true,
	"HANDLES_TOOL":  true,
	"SIMILAR_TO":    true,
	"TESTS":         true,
}

// knownResolutions classifies every Resolution value the providers emit. It is
// exhaustive on purpose and asserted exhaustive by test: a new resolution added
// to a language backend must be classified deliberately here, not inherit
// AMBIGUOUS by falling off the end of a lookup. Silently defaulting is how a
// trust label stops being trustworthy.
//
// The bias throughout is conservative. Under-claiming costs a caller some
// filtering; over-claiming tells them a guess was a fact.
var knownResolutions = map[string]string{
	// The target was identified directly.
	"exact":           ProvenanceExtracted,
	"full":            ProvenanceExtracted,
	"resolved":        ProvenanceExtracted,
	"import_resolved": ProvenanceExtracted,

	// A definite target, reached by inference rather than read off the source.
	// Real, and not the same claim as a direct reference.
	"type_inferred": ProvenanceInferred,

	// A target was seen but not pinned down: a bare name, a package or file, a
	// pattern match, a signature that may fit more than one symbol, a call into
	// a dependency whose definition is outside this repository, or nothing.
	"name_only":       ProvenanceAmbiguous,
	"package":         ProvenanceAmbiguous,
	"file":            ProvenanceAmbiguous,
	"pattern":         ProvenanceAmbiguous,
	"signature":       ProvenanceAmbiguous,
	"shallow":         ProvenanceAmbiguous,
	"import_external": ProvenanceAmbiguous,
	"git_history":     ProvenanceAmbiguous,
	"none":            ProvenanceAmbiguous,
}

// EdgeProvenance classifies how a relation was established.
//
// Order matters, and it is the one judgement in this file: a heuristic edge is
// INFERRED even when its resolution looks exact. HANDLES_ROUTE with an exact
// target still exists because a route string matched, and calling that
// EXTRACTED would promote a pattern match to a resolved reference — the precise
// confusion this label exists to prevent.
func EdgeProvenance(relationType, resolution string) string {
	if heuristicEdgeTypes[relationType] {
		return ProvenanceInferred
	}
	if provenance, known := knownResolutions[resolution]; known {
		return provenance
	}
	// An empty or unrecognised resolution is not a claim about a verified
	// target. Absent or unknown evidence reads as ambiguous, never as
	// extracted — the direction a trust label has to fail in.
	return ProvenanceAmbiguous
}

// MarshalJSON adds provenance to every serialised relation.
//
// It is computed here rather than stored on the struct, and that is the whole
// point. An earlier version stamped the label when the relation was emitted,
// which looked equivalent and was not: later stages refine Resolution, so a
// stamped label described the edge as it was at emit while the fields beside it
// described the edge as it ended up. The compact snapshot, which recomputed
// from the final values, then disagreed with the native stream about the same
// edge — caught by the canonical-hash comparison in graph-bench.
//
// Deriving at serialisation makes that class of bug unreachable. There is
// nothing to keep in sync, nothing to forget at a new construction site, and no
// window in which the label and its inputs can disagree.
func (r RelationRecord) MarshalJSON() ([]byte, error) {
	// The alias sheds the method set, so marshalling the embedded value does
	// not recurse back into this function.
	type alias RelationRecord
	return json.Marshal(struct {
		alias
		Provenance string `json:"provenance,omitempty"`
	}{alias(r), EdgeProvenance(r.Type, r.Resolution)})
}
