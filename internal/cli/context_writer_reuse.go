package cli

import (
	"context"
	"io"
	"reflect"
)

// Run already wraps each sink. Reuse that wrapper when an internal streaming
// entry point receives the same context, while retaining protection for direct
// callers and stricter child contexts. Custom non-comparable contexts fall back
// to wrapping rather than panicking during an interface comparison.
func contextChunkWriterFor(ctx context.Context, out io.Writer) io.Writer {
	if existing, ok := out.(*contextChunkWriter); ok {
		if kind := reflect.TypeOf(ctx); kind != nil && kind.Comparable() && existing.ctx == ctx {
			return existing
		}
	}
	return &contextChunkWriter{ctx: ctx, w: out}
}
