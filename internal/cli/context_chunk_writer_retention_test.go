package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// blockingWriter parks inside Write until release is closed, then records the
// bytes it actually observed. It is the sink shape contextChunkWriter exists
// for: a pipe whose reader has stopped draining, where the write cannot be
// interrupted and the only way out is to abandon the goroutine performing it.
type blockingWriter struct {
	entered  chan struct{}
	release  chan struct{}
	observed chan []byte
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		observed: make(chan []byte, 1),
	}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	w.observed <- append([]byte(nil), p...)
	return len(p), nil
}

// TestContextChunkWriterDoesNotRetainCallerBufferAfterCancel pins the
// io.Writer contract: an implementation must not retain p after Write returns.
// Returning on ctx.Done leaves the write goroutine running, so handing it the
// caller's slice let a canceled Write's leftover goroutine read a buffer the
// caller was free to reuse the instant Write returned — json.Encoder and
// io.MultiWriter, the two callers here, both reuse theirs.
//
// Before the fix the sink saw the caller's LATER bytes; with -race it is a
// reported data race as well.
func TestContextChunkWriterDoesNotRetainCallerBufferAfterCancel(t *testing.T) {
	t.Parallel()

	sink := newBlockingWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &contextChunkWriter{ctx: ctx, w: sink}

	payload := bytes.Repeat([]byte("a"), writeBytesChunkSize)
	go func() {
		<-sink.entered
		cancel()
	}()

	if _, err := writer.Write(payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write = %v, want context.Canceled", err)
	}

	// The caller is now free to reuse its buffer, and does.
	for i := range payload {
		payload[i] = 'b'
	}
	close(sink.release)

	observed := <-sink.observed
	if bytes.IndexByte(observed, 'b') >= 0 {
		t.Fatal("the abandoned write goroutine read the caller's buffer after Write returned: it must be handed a copy it owns")
	}
	if len(observed) != writeBytesChunkSize {
		t.Fatalf("sink observed %d bytes, want %d", len(observed), writeBytesChunkSize)
	}
}

// TestContextChunkWriterStartsNoGoroutineAfterCancel pins the other half of the
// bound: an abandoned goroutine is a per-writer accident, not a per-Write one.
// Once the context is done every later Write must refuse before starting a
// write, or a blocked sink would strand one goroutine per call.
func TestContextChunkWriterStartsNoGoroutineAfterCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := 0
	writer := &contextChunkWriter{ctx: ctx, w: writerFunc(func(p []byte) (int, error) {
		started++
		return len(p), nil
	})}
	for range 3 {
		if _, err := writer.Write([]byte("payload")); !errors.Is(err, context.Canceled) {
			t.Fatalf("Write = %v, want context.Canceled", err)
		}
	}
	if started != 0 {
		t.Fatalf("started %d write(s) on a canceled context, want 0", started)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

var _ io.Writer = writerFunc(nil)
