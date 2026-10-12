package sem

import (
	"encoding/json"
	"testing"
)

func TestSemanticJSONDistinguishesMeasuredZeroFromNoMatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  SearchResult
		present bool
	}{{"matched zero", SearchResult{Signals: []string{semanticSignal}}, true}, {"unmatched", SearchResult{}, false}, {"nonzero", SearchResult{SemanticScore: 0.4}, true}} {
		data, err := json.Marshal(tc.result)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		score, present := fields["semantic_score"]
		if present != tc.present {
			t.Errorf("%s: %s", tc.name, data)
		}
		if tc.name == "matched zero" && string(score) != "0" {
			t.Errorf("zero score=%s", score)
		}
	}
}
