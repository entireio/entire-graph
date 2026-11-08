package sem

import "context"

// cancellableReader wraps a content reader so that every read refuses once ctx
// is done. This is the structural close for the "a loop keeps reading whole
// files after the run was cancelled" class: the relation-phase producers are a
// hand-written sequence of functions that each build a full slice before
// returning, and the expensive ones are expensive because they scan file
// CONTENT per symbol or per file. They all read through the one contentReader
// they are handed, so gating the reader stops every one of them -- including
// producers nobody has audited yet -- within one in-flight read of the
// cancellation, without changing a single producer signature.
//
// A refused read looks like an unreadable file, so whatever a producer built
// after the cancellation is incomplete. That is safe only because the caller
// checks ctx.Err() before it emits a summary and returns the error instead
// (streamSnapshotWithWorkerCount, selectiveSearchSnapshotFromFull): a
// cancelled run never publishes what the refused reads left out.
//
// An uncancelled run is unaffected byte for byte: the wrapper is one ctx.Err()
// call per file read, and the same content is returned.
func cancellableReader(ctx context.Context, read contentReader) contentReader {
	if read == nil {
		return nil
	}
	return func(path string) (string, bool) {
		if ctx.Err() != nil {
			return "", false
		}
		return read(path)
	}
}
