package sem

import (
	"bytes"
	"encoding/json"
)

// MarshalJSON preserves a measured zero cosine on semantically matched rows.
// Lexical-only results retain the existing omission of semantic_score.
func (result SearchResult) MarshalJSON() ([]byte, error) {
	type alias SearchResult
	matched := result.SemanticScore != 0
	for _, signal := range result.Signals {
		if signal == semanticSignal {
			matched = true
			break
		}
	}
	var score *float64
	if matched {
		value := result.SemanticScore
		score = &value
	}
	// Leave HTML escaping to the outer encoder. CLI JSON/NDJSON encoders
	// disable it so source such as generic declarations stays byte-identical.
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		alias
		SemanticScore *float64 `json:"semantic_score,omitempty"`
	}{alias(result), score}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
