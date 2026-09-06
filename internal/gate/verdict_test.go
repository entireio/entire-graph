package gate

import "testing"

func allAvailable() Availability {
	return Availability{Risk: true, Coverage: true, Companions: true, Clones: true}
}

func TestDecideRevertsOnUncheckedBreakingChangeWithDependents(t *testing.T) {
	entities := []ChangedEntity{{
		Anchor:     Anchor{Name: "VerifyToken"},
		ChangeType: SignatureChanged,
		Dependents: 14,
		Coverage:   Unchecked,
	}}

	if got := Decide(entities, nil, allAvailable()); got != Revert {
		t.Fatalf("Decide = %s, want %s", got, Revert)
	}
}

func TestDecideDoesNotRevertWhenTheBreakingChangeIsCovered(t *testing.T) {
	entities := []ChangedEntity{{
		Anchor:     Anchor{Name: "VerifyToken"},
		ChangeType: SignatureChanged,
		Dependents: 14,
		Coverage:   Verified,
	}}

	if got := Decide(entities, nil, allAvailable()); got != Keep {
		t.Fatalf("Decide = %s, want %s", got, Keep)
	}
}

func TestDecideCapsAtContinueWhenCoverageDidNotRun(t *testing.T) {
	// This is the degradation rule, and it is the reason Decide takes
	// Availability at all. revert reads "has dependents AND no covering test".
	// With coverage absent, nothing has a covering test, so every
	// dependent-bearing change would satisfy the rule — a revert manufactured
	// by a missing input rather than by anything wrong with the change.
	entities := []ChangedEntity{{
		Anchor:     Anchor{Name: "VerifyToken"},
		ChangeType: SignatureChanged,
		Dependents: 14,
		Coverage:   NoResolver,
	}}
	findings := []Finding{{Dimension: DimRisk, Subject: Anchor{Name: "VerifyToken"}}}

	got := Decide(entities, findings, Availability{Risk: true, Coverage: false})
	if got != Continue {
		t.Fatalf("Decide = %s, want %s (coverage unavailable must not reach revert)", got, Continue)
	}
	if note := DegradationNote(Availability{Risk: true, Coverage: false}); note == "" {
		t.Fatal("a capped verdict must explain itself; DegradationNote was empty")
	}
}

func TestDecideCapsAtContinueWhenRiskDidNotRun(t *testing.T) {
	entities := []ChangedEntity{{
		Anchor:     Anchor{Name: "VerifyToken"},
		ChangeType: SignatureChanged,
		Coverage:   Unchecked,
	}}

	got := Decide(entities, nil, Availability{Risk: false, Coverage: true})
	if got != Continue {
		t.Fatalf("Decide = %s, want %s", got, Continue)
	}
}

func TestDecideIsUnusableWhenNoDimensionProducedEvidence(t *testing.T) {
	// Reporting revert here would be a false accusation, and reporting keep
	// would be a false reassurance. The honest answer is that Gate could not
	// check, which is what exit code 5 is for.
	entities := []ChangedEntity{{Anchor: Anchor{Name: "VerifyToken"}, ChangeType: Removed}}

	got := Decide(entities, nil, Availability{})
	if got != Unusable {
		t.Fatalf("Decide = %s, want %s", got, Unusable)
	}
	if got.ExitCode() != 5 {
		t.Fatalf("exit code = %d, want 5", got.ExitCode())
	}
}

func TestDecideContinuesOnAnyUncheckedEntity(t *testing.T) {
	// An unchecked entity with no dependents opens no finding, but it is still
	// something nobody looked at, and reporting keep would bury it.
	entities := []ChangedEntity{{
		Anchor:     Anchor{Name: "Helper"},
		ChangeType: BodyChanged,
		Coverage:   Unchecked,
	}}

	if got := Decide(entities, nil, allAvailable()); got != Continue {
		t.Fatalf("Decide = %s, want %s", got, Continue)
	}
}

func TestVerdictExitCodes(t *testing.T) {
	// The exit codes are the contract with a pre-push hook and with CI, so they
	// are pinned rather than left to the switch's ordering.
	for verdict, want := range map[Verdict]int{Keep: 0, Continue: 1, Revert: 2, Unusable: 5} {
		if got := verdict.ExitCode(); got != want {
			t.Errorf("%s exit code = %d, want %d", verdict, got, want)
		}
	}
}

func TestBreakingChangeClassification(t *testing.T) {
	for change, want := range map[ChangeType]bool{
		Removed:          true,
		Renamed:          true,
		SignatureChanged: true,
		BodyChanged:      false,
		Added:            false,
	} {
		if got := change.Breaking(); got != want {
			t.Errorf("%s.Breaking() = %v, want %v", change, got, want)
		}
	}
}
