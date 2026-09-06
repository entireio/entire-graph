package sem

import (
	"strings"
	"testing"
)

// TestRelationEvidenceStateHighPrecisionResolutionsAreConfirmed pins the behavior the ADR
// language calls "fully resolved": the resolution values shallowCallRelationRetained already
// treats as high-precision single-target matches must classify as Confirmed, so a genuinely
// resolved answer gains only the new field, never a new visible caveat.
func TestRelationEvidenceStateHighPrecisionResolutionsAreConfirmed(t *testing.T) {
	t.Parallel()
	for _, resolution := range []string{"exact", "package", "import_resolved", "import_external", ""} {
		relation := RelationRecord{Type: "CALLS", Resolution: resolution, Confidence: 0.9}
		if got := RelationEvidenceState(relation); got != EvidenceConfirmed {
			t.Errorf("RelationEvidenceState(resolution=%q) = %q, want %q", resolution, got, EvidenceConfirmed)
		}
	}
}

// TestRelationEvidenceStatePartialForInferredResolutions covers the shapes dynamic dispatch and
// statistical (non-code-derived) evidence already leave in the data: a Go interface call fanned
// out to an implementation (type_inferred), a signature-matched call, and co-change history.
func TestRelationEvidenceStatePartialForInferredResolutions(t *testing.T) {
	t.Parallel()
	for _, resolution := range []string{"type_inferred", "signature", "git_history"} {
		relation := RelationRecord{Type: "CALLS", Resolution: resolution}
		if got := RelationEvidenceState(relation); got != EvidencePartial {
			t.Errorf("RelationEvidenceState(resolution=%q) = %q, want %q", resolution, got, EvidencePartial)
		}
	}
}

// TestRelationEvidenceStateRequiresVerificationForWeakMatches covers the shapes reflection,
// dynamically-built calls, and generated bindings leave behind: a bare name/text match with no
// resolved target.
func TestRelationEvidenceStateRequiresVerificationForWeakMatches(t *testing.T) {
	t.Parallel()
	for _, resolution := range []string{"name_only", "pattern"} {
		relation := RelationRecord{Type: "CALLS", Resolution: resolution}
		if got := RelationEvidenceState(relation); got != EvidenceRequiresVerification {
			t.Errorf("RelationEvidenceState(resolution=%q) = %q, want %q", resolution, got, EvidenceRequiresVerification)
		}
	}
}

// TestRelationEvidenceStateWeakPatternWarningOverridesResolution asserts the WEAK_PATTERN
// warning code (provider.go's existing per-edge "this match is heuristic" signal) always wins,
// even over an otherwise high-precision resolution -- a warning the graph already attaches must
// not be silently dropped by the new classification layered on top of it.
func TestRelationEvidenceStateWeakPatternWarningOverridesResolution(t *testing.T) {
	t.Parallel()
	relation := RelationRecord{Type: "HANDLES_ROUTE", Resolution: "exact", WarningCodes: []string{"WEAK_PATTERN"}}
	if got := RelationEvidenceState(relation); got != EvidenceRequiresVerification {
		t.Fatalf("RelationEvidenceState with WEAK_PATTERN = %q, want %q", got, EvidenceRequiresVerification)
	}
}

// TestRelationEvidenceStateHeuristicRelationTypeIsPartial asserts every relation kind
// Capabilities() already declares heuristic (HeuristicRelationTypes) is at most Partial even
// when it happens to carry a high-precision resolution string -- the relation KIND itself is
// documented as pattern-based, not resolved code.
func TestRelationEvidenceStateHeuristicRelationTypeIsPartial(t *testing.T) {
	t.Parallel()
	for _, relationType := range heuristicRelationTypes {
		relation := RelationRecord{Type: relationType, Resolution: "exact"}
		if got := RelationEvidenceState(relation); got != EvidencePartial {
			t.Errorf("RelationEvidenceState(type=%s, resolution=exact) = %q, want %q", relationType, got, EvidencePartial)
		}
	}
}

func TestWorstEvidenceState(t *testing.T) {
	t.Parallel()
	if got := WorstEvidenceState(); got != EvidenceConfirmed {
		t.Fatalf("WorstEvidenceState() = %q, want %q", got, EvidenceConfirmed)
	}
	if got := WorstEvidenceState(EvidenceConfirmed, EvidencePartial); got != EvidencePartial {
		t.Fatalf("WorstEvidenceState(confirmed, partial) = %q, want %q", got, EvidencePartial)
	}
	if got := WorstEvidenceState(EvidenceConfirmed, EvidencePartial, EvidenceRequiresVerification); got != EvidenceRequiresVerification {
		t.Fatalf("WorstEvidenceState(confirmed, partial, requires_verification) = %q, want %q", got, EvidenceRequiresVerification)
	}
}

// TestLanguageEvidenceStateFlagsInventoryOnlyLanguages asserts a language the graph never runs
// call/reference resolution for (Forth is inventory-only per inventory_languages.go) cannot be
// reported Confirmed no matter what a per-edge resolution might otherwise say, because there is
// no relation extraction backing it at all.
func TestLanguageEvidenceStateFlagsInventoryOnlyLanguages(t *testing.T) {
	t.Parallel()
	if got := LanguageEvidenceState("Forth"); got != EvidenceRequiresVerification {
		t.Fatalf("LanguageEvidenceState(Forth) = %q, want %q", got, EvidenceRequiresVerification)
	}
	if got := LanguageEvidenceState("Go"); got != EvidenceConfirmed {
		t.Fatalf("LanguageEvidenceState(Go) = %q, want %q", got, EvidenceConfirmed)
	}
}

func TestEvidenceVerificationNoteNamesTheSubjectAndAVerificationPath(t *testing.T) {
	t.Parallel()
	note := EvidenceVerificationNote("Widget.Render")
	if note == "" {
		t.Fatal("EvidenceVerificationNote returned empty string")
	}
	for _, want := range []string{"Widget.Render", "NOT proof of absence", "tests"} {
		if !strings.Contains(note, want) {
			t.Fatalf("EvidenceVerificationNote() = %q, want it to contain %q", note, want)
		}
	}
}
