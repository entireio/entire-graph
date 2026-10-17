package sem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Reject extra rows and coordinates while decoding, before hostile tiny numbers
// can expand a bounded JSON body into a much larger float slice.
func decodeSemanticEmbedResponse(raw []byte, rows int) (semanticEmbedResponse, error) {
	var out semanticEmbedResponse
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return out, fmt.Errorf("expected object")
	}
	seen := false
	for d.More() {
		field, err := d.Token()
		if err != nil {
			return out, err
		}
		switch field {
		case "model":
			if err := d.Decode(&out.Model); err != nil {
				return out, err
			}
		case "embeddings":
			if seen {
				return out, fmt.Errorf("duplicate embeddings")
			}
			seen = true
			token, err := d.Token()
			if err != nil || token != json.Delim('[') {
				return out, fmt.Errorf("expected embeddings array")
			}
			for d.More() {
				if len(out.Embeddings) >= rows {
					return out, fmt.Errorf("too many embeddings")
				}
				token, err := d.Token()
				if err != nil || token != json.Delim('[') {
					return out, fmt.Errorf("expected vector")
				}
				var vector []float64
				for d.More() {
					if len(vector) >= semanticMaxDimension {
						return out, fmt.Errorf("too many coordinates")
					}
					token, err := d.Token()
					if err != nil {
						return out, err
					}
					number, ok := token.(float64)
					if !ok {
						return out, fmt.Errorf("expected coordinate")
					}
					vector = append(vector, number)
				}
				if _, err := d.Token(); err != nil {
					return out, err
				}
				out.Embeddings = append(out.Embeddings, vector)
			}
			if _, err := d.Token(); err != nil {
				return out, err
			}
		default:
			var ignored json.RawMessage
			if err := d.Decode(&ignored); err != nil {
				return out, err
			}
		}
	}
	if _, err := d.Token(); err != nil {
		return out, err
	}
	if _, err := d.Token(); err != io.EOF {
		return out, fmt.Errorf("trailing response data")
	}
	return out, nil
}
