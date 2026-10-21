package sem

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSemanticJSONHonorsEncoderEscaping(t *testing.T) {
	for _, signals := range [][]string{nil, {semanticSignal}} {
		result := SearchResult{Snippet: "Map<T> & value", Signals: signals}
		for _, escape := range []bool{false, true} {
			var out bytes.Buffer
			encoder := json.NewEncoder(&out)
			encoder.SetEscapeHTML(escape)
			if err := encoder.Encode(result); err != nil {
				t.Fatal(err)
			}
			want := `"snippet":"Map<T> & value"`
			if escape {
				want = `"snippet":"Map\u003cT\u003e \u0026 value"`
			}
			if !strings.Contains(out.String(), want) {
				t.Fatalf("signals=%v escape=%t: want %s in %s", signals, escape, want, out.String())
			}
			if strings.Contains(out.String(), `"semantic_score":0`) != (len(signals) > 0) {
				t.Fatalf("signals=%v escape=%t: measured-zero provenance lost: %s", signals, escape, out.String())
			}
		}
	}
}

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
