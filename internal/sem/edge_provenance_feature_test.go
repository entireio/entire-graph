package sem

import "testing"

// schemaFeatures is this repo's mechanism for letting a consumer detect a
// newly-populated optional field without inspecting every record, and ADR 0001's
// additive-minor contract depends on it. A field that ships without an entry is
// invisible to capability detection: schema_version stays put and a downstream
// reader (Entire Brain) has no advertised signal that provenance now exists.
//
// The list's own rule is "only add entries when the field is actually
// populated", so this asserts both halves: advertised, and present on every
// serialised relation.
func TestRelationProvenanceIsAdvertisedAndPopulated(t *testing.T) {
	if !contains(schemaFeatures, "relation_provenance") {
		t.Fatalf("schemaFeatures must advertise relation_provenance: %#v", schemaFeatures)
	}

	// Advertising the feature promises a value on every relation, so an absent
	// or unrecognised resolution must still yield one.
	for _, resolution := range []string{"", "unknown-resolution"} {
		if got := EdgeProvenance("CALLS", resolution); got == "" {
			t.Fatalf("EdgeProvenance(CALLS, %q) = empty; the advertised field must always be populated", resolution)
		}
	}
	for resolution := range knownResolutions {
		if got := EdgeProvenance("CALLS", resolution); got == "" {
			t.Fatalf("EdgeProvenance(CALLS, %q) = empty for a known resolution", resolution)
		}
	}
	for relationType := range heuristicEdgeTypes {
		if got := EdgeProvenance(relationType, "exact"); got != ProvenanceInferred {
			t.Fatalf("EdgeProvenance(%q, exact) = %q, want %q", relationType, got, ProvenanceInferred)
		}
	}
}
