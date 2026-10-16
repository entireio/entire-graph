package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Retain trail 223's adaptive-renderer regression when consolidating its
// remaining method-recognition correction into trail 244.
func TestAdaptiveExactAnswerKeepsReplayFlagAndSessionOmissionPolicy(t *testing.T) {
	for _, capped := range []bool{false, true} {
		response := exactNameFixture("Resolve", 1)
		response.Results = response.Results[:1]
		var out bytes.Buffer
		exact, err := writeAgentSearchAdaptiveFor(&out, response, 4096,
			agentSearchRender{exactName: true, topK: 1, sessionCapped: capped})
		if err != nil {
			t.Fatal(err)
		}
		if !exact || !exactNameDeclShown(out.String(), response.Results[0]) {
			t.Fatalf("session capped=%v lost its exact-answer replay exclusion: %q", capped, out.String())
		}
		if out.Len() > 4096 || !strings.Contains(out.String(), "more may exist") {
			t.Fatalf("session capped=%v lost its bounded omission disclosure: %q", capped, out.String())
		}
		if strings.Contains(out.String(), "raise --top-k") == capped {
			t.Fatalf("session capped=%v changed its omission policy: %q", capped, out.String())
		}
	}
}
