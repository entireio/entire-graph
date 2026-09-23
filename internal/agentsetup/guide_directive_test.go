package agentsetup

import (
	"strings"
	"testing"
)

// TestNormalGuideStaysDirective is the structural guard described in guide.go's header.
//
// It exists because the softening it pins against has already happened once and was not
// caught: on 2026-09-14 the normal-mode guide lost its "first action" obligation and gained
// three self-assessed exits, and nothing failed. Measured afterwards, agents made 332 graph
// calls against 96,987 exploration calls and no session was graph-first.
//
// The regression class is specific, so the assertions are too: an obligation must be present,
// self-assessed exits must not be, a failed query must retire the question rather than the
// tool, and the capability-only benchmark wording must stay out of the shipped guides.
func TestNormalGuideStaysDirective(t *testing.T) {
	t.Parallel()

	const obligation = "Your FIRST action on any task that requires finding code MUST be ONE Graph query"
	const command = `entire graph query --repo . --profile full --query "<task>"`

	for name, guide := range map[string]string{"GraphGuide": GraphGuide, "CombinedGuide": CombinedGuide} {
		if !strings.Contains(guide, obligation) {
			t.Errorf("%s lost the first-action obligation: agents do not call a tool they are told is optional", name)
		}
		if !strings.Contains(guide, command) {
			t.Errorf("%s no longer shows the exact command to run", name)
		}

		// Each of these shipped, and each is an exit a model takes almost always, because
		// sufficiency is self-assessed and skipping is the locally cheaper move.
		for _, exit := range []string{
			"Directly inspect source when the task already provides sufficient locations",
			"Skip ceremonial queries",
			"do not require a redundant",
		} {
			if strings.Contains(guide, exit) {
				t.Errorf("%s reintroduced the self-assessed exit %q", name, exit)
			}
		}

		// One failed query must not end graph use for the session. That is not hypothetical:
		// the guide named a verb a shipped binary did not have, every call errored, and the
		// unscoped fallback clause turned it into permanent abandonment.
		if !strings.Contains(guide, "FOR THAT QUERY ONLY") {
			t.Errorf("%s lost the scoped fallback: a failure must retire the question, not the tool", name)
		}
		if strings.Contains(guide, "continue with useful remaining tools") {
			t.Errorf("%s reintroduced the unscoped abandonment clause", name)
		}

		// The A/B wording is for harnesses. Shipping it is how this broke the first time.
		if strings.Contains(guide, BenchmarkNeutralGraphCapability) {
			t.Errorf("%s embeds BenchmarkNeutralGraphCapability; benchmark-neutral wording must not ship", name)
		}
	}

	if strings.Contains(BenchmarkNeutralGraphCapability, "MUST") || strings.Contains(BenchmarkNeutralGraphCapability, "FIRST action") {
		t.Error("BenchmarkNeutralGraphCapability is no longer neutral; an imperative there is an arm-asymmetric instruction")
	}

	// Graph before Brain in the combined guide. An obligation printed under a paragraph that
	// already offers a way to find code reads as the optional one of the two.
	graphAt := strings.Index(CombinedGuide, obligation)
	brainAt := strings.Index(CombinedGuide, `entire brain brief "<task>" --json`)
	if graphAt < 0 || brainAt < 0 || graphAt > brainAt {
		t.Errorf("combined guide does not put the Graph obligation before the Brain brief (graph=%d brain=%d)", graphAt, brainAt)
	}
}
