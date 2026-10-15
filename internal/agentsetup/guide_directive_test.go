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

	const obligation = "Your first action on a task that requires finding code MUST be a Graph query"

	// The obligation covers EVERY question, not the first one. "MUST be ONE Graph query" was
	// obeyed to the letter in a fresh-environment baseline: one query, then grep and sed for the
	// rest of the task, zero def/neighbors/impact calls even on a blast-radius task.
	const everyQuestion = "each later locate or relationship question MUST also go through Graph"

	// The commands are pinned whole. --format agent is a cost decision (json measured 17595 B
	// against agent's 6011 B for the same results; see guide.go), --head is the cache (every
	// unflagged baseline call rebuilt the working tree for 13-16 s), --profile full is what makes
	// query --head reuse the cache `index` warms, and 4096 is the budget study 5 measured best.
	// impact rejects --format agent and def's agent format is its text format, so those two lines
	// must not ask for it.
	commands := []string{
		`entire graph query --repo . --profile full --head --format agent --max-context-bytes 4096 --query "<task>"`,
		`entire graph def --repo . --head --max-context-bytes 4096 <Name>`,
		`entire graph neighbors --repo . --head --format agent --max-context-bytes 4096 --symbol <Name> --direction in`,
		`entire graph impact --repo . --head --max-context-bytes 4096 --symbol <Name>`,
	}

	// Each of these is an obligation the baseline showed agents skipping or misreading.
	obligations := map[string]string{
		"grep is for literals only":   "Use grep/rg only for literal strings, config",
		"grep is not for references":  "not grep, for definitions, callers, references and dependents",
		"--head is the default":       "Drop --head only when the answer depends on uncommitted edits",
		"no timeout wrapper":          "it needs no timeout wrapper",
		"coverage notice is not loss": "It does not mean the results are partial",
	}

	for name, guide := range map[string]string{"GraphGuide": GraphGuide, "CombinedGuide": CombinedGuide} {
		if !strings.Contains(guide, obligation) {
			t.Errorf("%s lost the first-action obligation: agents do not call a tool they are told is optional", name)
		}
		if !strings.Contains(guide, everyQuestion) {
			t.Errorf("%s lost the every-question obligation: a first-query-only rule is followed once and then grep takes over", name)
		}
		for _, command := range commands {
			if !strings.Contains(guide, command) {
				t.Errorf("%s no longer shows the exact command %q", name, command)
			}
		}
		// Compared with whitespace folded, so rewrapping the guide does not trip the pin.
		flat := strings.Join(strings.Fields(guide), " ")
		for what, text := range obligations {
			if !strings.Contains(flat, strings.Join(strings.Fields(text), " ")) {
				t.Errorf("%s lost an obligation (%s): %q", name, what, text)
			}
		}
		// "ONE" capped graph use at a single call. No cap of any spelling may come back.
		for _, cap := range []string{"ONE Graph query", "one Graph query", "a single Graph query", "only once"} {
			if strings.Contains(guide, cap) {
				t.Errorf("%s reintroduced a one-query cap %q", name, cap)
			}
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
