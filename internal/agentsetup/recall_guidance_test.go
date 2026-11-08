package agentsetup

import (
	"strings"
	"testing"
)

// Every Brain-enabled guide names recall as the retrieval for prior decisions.
// The brief's facts section is a sample and query mixes facts with docs and
// history, so an agent asked "was this decided before?" needs the command
// spelled out (issue #335).
func TestBrainGuidesNameRecallForPriorDecisions(t *testing.T) {
	const recall = `entire brain recall "<question>" --json`
	for _, tc := range []struct {
		name   string
		active map[string]bool
		mode   Mode
		want   bool
	}{
		{"brain normal", map[string]bool{"brain": true}, ModeNormal, true},
		{"combined normal", map[string]bool{"brain": true, "graph": true}, ModeNormal, true},
		{"brain strict", map[string]bool{"brain": true}, ModeStrict, true},
		{"combined strict", map[string]bool{"brain": true, "graph": true}, ModeStrict, true},
		{"graph normal", map[string]bool{"graph": true}, ModeNormal, false},
		{"graph strict", map[string]bool{"graph": true}, ModeStrict, false},
	} {
		guide := guideFor(tc.active, tc.mode)
		if got := strings.Contains(guide, recall); got != tc.want {
			t.Errorf("%s: contains recall guidance = %v, want %v\n%s", tc.name, got, tc.want, guide)
		}
		if tc.want && strings.Index(guide, recall) < strings.Index(guide, `entire brain brief "<task>" --json`) {
			t.Errorf("%s: recall guidance should follow the brief command", tc.name)
		}
	}
}
