package agentsetup

import (
	"strings"
	"testing"
)

// Every Brain-enabled guide that tells an agent to run `remember --path` also
// says what a path may contain. Without it agents kebab-case a segment, the
// path is rejected, and they retry by trial and error (issue #588).
func TestBrainGuidesSpellOutTheRememberPathRule(t *testing.T) {
	const rule = "lowercase letters, digits and underscores"
	for _, tc := range []struct {
		name   string
		active map[string]bool
		mode   Mode
	}{
		{"brain normal", map[string]bool{"brain": true}, ModeNormal},
		{"combined normal", map[string]bool{"brain": true, "graph": true}, ModeNormal},
		{"brain strict", map[string]bool{"brain": true}, ModeStrict},
		{"combined strict", map[string]bool{"brain": true, "graph": true}, ModeStrict},
	} {
		guide := guideFor(tc.active, tc.mode)
		remember := strings.Index(guide, "entire brain remember")
		if remember < 0 {
			t.Fatalf("%s: guide no longer shows remember", tc.name)
		}
		if !strings.Contains(guide[remember:], rule) {
			t.Errorf("%s: guide shows remember --path but not the path rule %q", tc.name, rule)
		}
	}
}
