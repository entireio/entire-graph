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

// heuristicEdgeTypes marks relations whose existence is pattern-derived, even
// when their target is resolved. This is broader than the global, language-
// independent HeuristicRelationTypes capability: protocol-specific RPC handlers
// remain attributed to the languages that extract them in the capability matrix.
var heuristicEdgeTypes = map[string]bool{
	"HANDLES_ROUTE":   true,
	"HANDLES_GRPC":    true,
	"HANDLES_GRAPHQL": true,
	"HANDLES_TRPC":    true,
	"HTTP_CALLS":      true,
	"EMITS":           true,
	"LISTENS_ON":      true,
	"HANDLES_TOOL":    true,
	"SIMILAR_TO":      true,
	"TESTS":           true,
}

// knownResolutions classifies the resolution vocabulary emitted by the provider.
// A source-level guard checks constant construction and assignment sites; fixture
// and serialization tests cover runtime paths. Unknown values remain ambiguous.
//
// The bias throughout is conservative. Under-claiming costs a caller some
// filtering; over-claiming tells them a guess was a fact.
var knownResolutions = map[string]string{
	// The target was identified directly.
	"exact":           ProvenanceExtracted,
	"full":            ProvenanceExtracted,
	"resolved":        ProvenanceExtracted,
	"import_resolved": ProvenanceExtracted,

	// "package" belongs here, not below, despite the name. Both producers emit
	// it only after narrowing to exactly one symbol — Go's same-package call
	// resolution returns it when len(samePkg) == 1, and Dart's same-directory
	// resolution when there is a single match — so the target is specific, not
	// a package-wide guess. shallowCallRelationRetained says the same thing in
	// the other direction: it groups "package" with "exact" and
	// "import_resolved" as the single-target, high-precision resolutions.
	//
	// Reading it as ambiguous mislabelled a large share of Go and Dart CALLS
	// edges, which is the common case this label exists to be useful for.
	"package": ProvenanceExtracted,

	// A definite target, reached by inference rather than read off the source.
	// Real, and not the same claim as a direct reference.
	"type_inferred": ProvenanceInferred,

	// A target was seen but not pinned down: a bare name, a package or file, a
	// pattern match, a signature that may fit more than one symbol, a call into
	// a dependency whose definition is outside this repository, or nothing.
	"name_only":       ProvenanceAmbiguous,
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

// MarshalJSON adds the optional schema-1.4 provenance field to native JSON.
// Deriving it from final Type/Resolution values avoids a stale stored label after
// resolution refinement. The compact format stores those inputs unchanged and
// recovers the same label when decoded relations are serialized as native JSON.
// SCIP is a lossy navigation projection and does not carry this annotation.
// See docs/snapshot-format.md and the serialized contract tests in
// edge_provenance_serialization_test.go. Go callers use EdgeProvenance directly.
func (r RelationRecord) MarshalJSON() ([]byte, error) {
	// The alias sheds the method set, so marshalling the embedded value does
	// not recurse back into this function.
	type alias RelationRecord
	return json.Marshal(struct {
		alias
		Provenance string `json:"provenance"`
	}{alias(r), EdgeProvenance(r.Type, r.Resolution)})
}
