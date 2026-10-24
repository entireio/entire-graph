package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type nonComparableWriteContext struct {
	context.Context
	values map[string]string
}

func TestContextChunkWriterReusesOnlyTheSameCancellation(t *testing.T) {
	var out bytes.Buffer
	parent := contextChunkWriterFor(t.Context(), &out)
	if contextChunkWriterFor(t.Context(), parent) != parent {
		t.Fatal("same-context sink double wrapped")
	}
	child, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := contextChunkWriterFor(child, parent).Write([]byte("must not write")); !errors.Is(err, context.Canceled) {
		t.Fatalf("child cancellation lost: %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("canceled child wrote to parent")
	}
	custom := nonComparableWriteContext{Context: t.Context(), values: map[string]string{}}
	wrapped := contextChunkWriterFor(custom, &out)
	if _, err := contextChunkWriterFor(custom, wrapped).Write([]byte("valid")); err != nil {
		t.Fatal(err)
	}
}
