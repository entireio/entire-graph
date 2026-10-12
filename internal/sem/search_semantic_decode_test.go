package sem

import (
	"strings"
	"testing"
)

func TestSemanticDecodeRejectsExcessBeforeAllocating(t *testing.T) {
	for _, raw := range []string{
		`{"embeddings":[[1],[2]]}`,
		`{"embeddings":[[` + strings.Repeat("0,", semanticMaxDimension) + `1]]}`,
		`{"embeddings":[[1]],"embeddings":[[1]]}`,
		`{"embeddings":[[1]]} {}`,
	} {
		if _, err := decodeSemanticEmbedResponse([]byte(raw), 1); err == nil {
			t.Fatal("accepted excess or malformed response")
		}
	}
	got, err := decodeSemanticEmbedResponse([]byte(`{"extra":{},"embeddings":[[0,1]],"model":"test"}`), 1)
	if err != nil || got.Model != "test" || len(got.Embeddings) != 1 || len(got.Embeddings[0]) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}
