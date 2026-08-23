package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestWriteBytesWithContextReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	buf := &bytes.Buffer{}
	err := writeBytesWithContext(ctx, buf, bytes.Repeat([]byte("x"), writeBytesChunkSize*2))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeBytesWithContext = %v, want context.Canceled", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes after cancel, want 0", buf.Len())
	}
}

func TestWriteBytesWithContextWritesAll(t *testing.T) {
	want := []byte("hello cache hit")
	var got bytes.Buffer
	if err := writeBytesWithContext(context.Background(), &got, want); err != nil {
		t.Fatalf("writeBytesWithContext: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("got %q, want %q", got.Bytes(), want)
	}
}
