package agentsetup

import (
	"strings"
	"testing"
)

// The pre-edit read is what makes a Graph call additive rather than substitutive, and
// it used to be mandated unconditionally in every guide and both modes. Measured
// consequence, already committed in this repository: the tool makes +19.6% MORE Read
// calls than the no-tool baseline while total tool calls fall 14.5%
// (internal/sem/search_span_merge.go:20-23, n=55 paired). "Bodies removed greps and
// added reads."
//
// These guard the exception that fixes it. They are deliberately paired with
// TestNormalGuideStaysDirective, which guards the opposite failure: that file explains
// why permissive wording is fatal here, and the exception below must not become a way
// back to it.
func TestEveryGuideMakesThePreEditReadConditionalOnCompleteness(t *testing.T) {
	t.Parallel()

	// Every guide, both modes. verificationGuide is shared by all six renderings, so a
	// change that reaches only the normal Graph guide has missed five of them.
	guides := map[string]string{
		"GraphGuide":      GraphGuide,
		"CombinedGuide":   CombinedGuide,
		"BrainGuide":      BrainGuide(),
		"strict Graph":    guideFor(map[string]bool{"graph": true}, ModeStrict),
		"strict Combined": guideFor(map[string]bool{"graph": true, "brain": true}, ModeStrict),
		"strict Brain":    guideFor(map[string]bool{"brain": true}, ModeStrict),
	}
	for name, guide := range guides {
		if !strings.Contains(guide, "[complete]") {
			t.Errorf("%s never mentions the [complete] marker, so the agent re-reads a body it was just handed", name)
		}
		// An unmarked body is a fragment and must still be read. Without this half the
		// exception reads as "skip the read", which is the opposite failure.
		if !strings.Contains(guide, "An unmarked body is a fragment") {
			t.Errorf("%s dropped the rule that an unmarked body must still be read", name)
		}
	}
}

// Strict mode LOOKED like it already banned the redundant read -- "Do not re-read files
// or retrieved records that Graph or Brain already answered for" -- and then exempted
// "the focused source inspection required before editing" in the very next sentence,
// which is precisely the read in question. The ban was therefore inert.
func TestStrictModeDoesNotExemptTheOneReadItIsMeantToStop(t *testing.T) {
	t.Parallel()

	const carveOut = "or the focused source inspection required before editing"
	strictCombined := guideFor(map[string]bool{"graph": true, "brain": true}, ModeStrict)
	if strings.Contains(strictCombined, carveOut) {
		t.Errorf("strict mode reinstated the carve-out %q, which exempts the exact read the rule exists to stop", carveOut)
	}
	if !strings.Contains(strictCombined, "A result marked [complete] IS") {
		t.Error("strict mode no longer says a [complete] result is itself the source, so the re-read ban stays inert")
	}
}

// The exception must key on something the TOOL printed, never on the agent's own
// assessment. guide.go's header records why: on 2026-09-14 the normal guide gained
// three self-assessed exits and graph use collapsed to 0.34% of locate calls, because
// "sufficiency is self-assessed, and it assesses as true nearly always."
func TestTheReadExceptionIsNotSelfAssessed(t *testing.T) {
	t.Parallel()

	for name, guide := range map[string]string{"GraphGuide": GraphGuide, "CombinedGuide": CombinedGuide} {
		for _, selfAssessed := range []string{
			"if you already understand",
			"when the result is sufficient",
			"if the context is sufficient",
			"at your discretion",
			"if you judge",
		} {
			if strings.Contains(strings.ToLower(guide), selfAssessed) {
				t.Errorf("%s makes the read exception self-assessed via %q; it must key on the printed [complete] marker", name, selfAssessed)
			}
		}
	}
}
