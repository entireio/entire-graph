package agentsetup

import (
	"strings"
	"testing"
)

// These tests check the rendered instruction artifacts, not consuming-agent behavior
// or savings. A missing shared paragraph or a contradictory strict-mode instruction
// must be caught in every product/mode combination. Discovery obligations have their
// own guards in guide_directive_test.go and strict_test.go.
func conditionalReadGuides() map[string]string {
	guides := make(map[string]string)
	for _, mode := range []Mode{ModeNormal, ModeStrict} {
		for name, active := range map[string]map[string]bool{
			"Graph":    {"graph": true},
			"Brain":    {"brain": true},
			"Combined": {"graph": true, "brain": true},
		} {
			guides[string(mode)+" "+name] = strings.Join(strings.Fields(guideFor(active, mode)), " ")
		}
	}
	return guides
}

func TestEveryGuideScopesCompletenessToObservedSource(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"[complete] certifies a structurally whole displayed body",
				"unchanged by rendering, for the source view observed by that query",
				"does not certify dependencies, later source freshness, or task resolution",
				"Do not reread the same unchanged span merely to duplicate it",
			} {
				if !strings.Contains(guide, required) {
					t.Errorf("rendered guide missing observed-source boundary %q", required)
				}
			}
		})
	}
}

func TestEveryGuideKeepsUnmarkedSourceUncertified(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"An unmarked result is not certified",
				"a partial window or a whole body whose marker did not fit",
				"Retrieve only the specific additional span, surrounding context, caller, contract, or second site required for the task",
			} {
				if !strings.Contains(guide, required) {
					t.Errorf("rendered guide missing targeted-read instruction %q", required)
				}
			}
			if strings.Contains(guide, "An unmarked body is a fragment") {
				t.Error("rendered guide incorrectly treats absent certification as proof of a fragment")
			}
		})
	}
}

func TestEveryGuideRefreshesPossiblyChangedSource(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"edits, formatting, generation, checkout, or another writer",
				"verify that current span before reusing remembered output",
				"A --head result does not cover working-tree changes absent from that snapshot",
			} {
				if !strings.Contains(guide, required) {
					t.Errorf("rendered guide missing source-change boundary %q", required)
				}
			}
			if strings.Contains(guide, "never to re-confirm what it already showed you") {
				t.Error("rendered guide forbids refreshing potentially stale source")
			}
		})
	}
}

// Some editing tools refuse to change a file the agent has not read in the session. A
// guide that lets [complete] stand in for that read would make the first edit fail, and
// an agent unable to tell whether source changed must not default to skipping the read.
func TestEveryGuideKeepsToolRequiredReadsAndDefaultsUnknownFreshnessToRead(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"The marker never waives a read your editing tool requires before it changes a file: when the tool requires one, perform that read",
				"If you cannot establish that a span is unchanged since the query, treat it as changed and read it",
			} {
				if !strings.Contains(guide, required) {
					t.Errorf("rendered guide missing read-safety instruction %q", required)
				}
			}
			for _, forbidden := range []string{
				"skip the pre-edit read",
				"skip the read",
				"without reading",
				"no need to read",
			} {
				if strings.Contains(strings.ToLower(guide), forbidden) {
					t.Errorf("rendered guide tells agents to skip a read via %q", forbidden)
				}
			}
		})
	}
}

func TestStrictCompletenessDoesNotExemptStaleOrMissingSource(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		if !strings.HasPrefix(name, string(ModeStrict)+" ") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(guide, "the marker does not remove the need to check a stale result or required source outside that body") {
				t.Error("strict guide does not preserve stale and missing-source follow-up for marked bodies")
			}
			for _, forbidden := range []string{
				"or the focused source inspection required before editing",
				"A result marked [complete] IS that source, so it is not one of those cases",
			} {
				if strings.Contains(guide, forbidden) {
					t.Errorf("strict guide retained unsafe unconditional read policy %q", forbidden)
				}
			}
		})
	}
}

func TestConditionalReadsDoNotOfferADiscoveryOptOut(t *testing.T) {
	t.Parallel()
	for name, guide := range conditionalReadGuides() {
		for _, selfAssessed := range []string{
			"if you already understand",
			"when the result is sufficient",
			"if the context is sufficient",
			"at your discretion",
			"if you judge",
		} {
			if strings.Contains(strings.ToLower(guide), selfAssessed) {
				t.Errorf("%s offers a general self-assessed opt-out via %q", name, selfAssessed)
			}
		}
	}
}

func TestStrictGraphDiscoveryRequestsTheMarkedAgentFormat(t *testing.T) {
	t.Parallel()
	const command = `entire graph query --repo . --profile full --head --format agent --query "<task>"`
	for name, guide := range conditionalReadGuides() {
		if name != "strict Graph" && name != "strict Combined" {
			continue
		}
		if !strings.Contains(guide, command) {
			t.Errorf("%s must request the agent format that carries [complete] while retaining --head", name)
		}
	}
}
