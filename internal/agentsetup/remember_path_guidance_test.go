package agentsetup

import (
	"sort"
	"strings"
	"testing"
)

// rememberPathRule is the wording Brain's guide uses, verbatim
// (entireio/entire-brain#605). Brain's cross-product check compares the two
// rendered guides BYTE FOR BYTE, so holding a substring of this rule is not
// enough: reflow the line break or reword the example and Graph drifts in
// silence, with the failure surfacing in Brain's CI against Brain's own change.
//
// Without the rule an agent kebab-cases a segment, remember rejects the path,
// and it retries by trial and error (entire-brain#588).
const rememberPathRule = "Each path segment uses only lowercase letters, digits and underscores: write\n" +
	"project.ci_cache.layout, not project.ci-cache.layout.\n"

// removedMCPWrite is the sentence Brain deleted when 0.4.0 removed its MCP
// server. Graph went on rendering it, so every repository Graph set up told
// its agents to write memories with a tool that no longer exists.
var removedMCPWrite = []string{"brain_remember", "Over MCP"}

// brainEnabledGuides are the guides that tell an agent to run remember: both
// modes, Brain alone and Brain beside Graph. The count is asserted so a guide
// that stops mentioning remember fails here instead of being skipped.
const brainEnabledGuides = 4

func TestBrainGuidesStateTheRememberPathRuleVerbatim(t *testing.T) {
	var checked []string
	for name, guide := range AllGuides() {
		if !strings.Contains(name, "brain") {
			continue
		}
		checked = append(checked, name)
		remember := strings.Index(guide, "entire brain remember")
		if remember < 0 {
			t.Errorf("%s: a Brain-enabled guide no longer shows remember", name)
			continue
		}
		rule := strings.Index(guide, rememberPathRule)
		if rule < 0 {
			t.Errorf("%s: guide shows remember --path but not the rule verbatim; want:\n%s", name, rememberPathRule)
			continue
		}
		if rule < remember {
			t.Errorf("%s: the path rule sits above the command it qualifies", name)
		}
	}
	sort.Strings(checked)
	if len(checked) != brainEnabledGuides {
		t.Errorf("checked %d Brain-enabled guides %v, want %d; AllGuides no longer renders what this test believes it does",
			len(checked), checked, brainEnabledGuides)
	}
}

func TestNoGuideOffersTheRemovedMCPWrite(t *testing.T) {
	for name, guide := range AllGuides() {
		for _, gone := range removedMCPWrite {
			if strings.Contains(guide, gone) {
				t.Errorf("%s: guide still mentions %q; Brain removed its MCP server in 0.4.0, so this directs an agent at a tool that does not exist", name, gone)
			}
		}
	}
}
