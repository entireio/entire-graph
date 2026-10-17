package cli

import (
	"context"
	"errors"
	"io"
	"testing"
)

type reviewShortWriter struct {
	n     int
	err   error
	calls int
}

func (w *reviewShortWriter) Write(p []byte) (int, error) { w.calls++; return w.n, w.err }
func TestContextChunkWriterPreservesShortWriteContract(t *testing.T) {
	sentinel := errors.New("sink failed")
	for _, tc := range []struct {
		name string
		n    int
		err  error
		want error
	}{{"partial nil", 2, nil, io.ErrShortWrite}, {"partial error", 2, sentinel, sentinel}, {"zero nil", 0, nil, io.ErrShortWrite}} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &reviewShortWriter{n: tc.n, err: tc.err}
			w := &contextChunkWriter{ctx: context.Background(), w: sink}
			n, err := w.Write([]byte("hello"))
			if n != tc.n || !errors.Is(err, tc.want) || sink.calls != 1 {
				t.Fatalf("n=%d err=%v calls=%d", n, err, sink.calls)
			}
		})
	}
}
