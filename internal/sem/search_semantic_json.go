package sem

import "encoding/json"

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
	return json.Marshal(struct {
		alias
		SemanticScore *float64 `json:"semantic_score,omitempty"`
	}{alias(result), score})
}
