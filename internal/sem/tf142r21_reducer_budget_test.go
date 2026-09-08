package sem

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestTF142R21PipelineStopsReducingOnceTheBudgetIsGone closes the last hole in
// the wall-clock ceiling's coverage of the parse phase. Every stop check lived
// on the worker side (processProviderFile) or on the coordinator's context; the
// reduction itself polled nothing. Reducing is not bookkeeping -- it emits the
// file record plus that file's entire symbol list into the caller's sink -- and
// the coordinator's cancellation branch deliberately drains and reduces every
// buffered result before returning, so a budget that expired mid-run still paid
// for those reductions in full.
func TestTF142R21PipelineStopsReducingOnceTheBudgetIsGone(t *testing.T) {
	t.Parallel()

	paths := make([]string, 24)
	for i := range paths {
		paths[i] = fmt.Sprintf("pkg%02d.go", i)
	}

	expired := false
	reduced := 0
	err := runProviderFilePipeline(
		t.Context(),
		func() error {
			if expired {
				return context.DeadlineExceeded
			}
			return nil
		},
		paths, 1,
		func(_ context.Context, index int, path string) providerFileResult {
			return providerFileResult{index: index, path: path}
		},
		func(providerFileResult) error {
			reduced++
			// The budget runs out while the first result is being emitted.
			expired = true
			return nil
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pipeline = %v, want context.DeadlineExceeded", err)
	}
	if reduced != 1 {
		t.Fatalf("reduced %d result(s) after the budget expired, want exactly the one in flight when it did", reduced)
	}
}

// TestTF142R21PipelineWithoutAStopIsUnchanged pins that the poll is opt-in:
// a nil stop leaves the pipeline byte-for-byte the pipeline it was.
func TestTF142R21PipelineWithoutAStopIsUnchanged(t *testing.T) {
	t.Parallel()

	paths := make([]string, 24)
	for i := range paths {
		paths[i] = fmt.Sprintf("pkg%02d.go", i)
	}
	var order []string
	err := runProviderFilePipeline(t.Context(), nil, paths, 4,
		func(_ context.Context, index int, path string) providerFileResult {
			return providerFileResult{index: index, path: path}
		},
		func(result providerFileResult) error {
			order = append(order, result.path)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("pipeline = %v, want nil", err)
	}
	if len(order) != len(paths) {
		t.Fatalf("reduced %d of %d paths", len(order), len(paths))
	}
	for i, path := range order {
		if path != paths[i] {
			t.Fatalf("reduction order diverged at %d: got %q, want %q", i, path, paths[i])
		}
	}
}

// TestTF142R21StopIsPolledInTheCancellationDrain pins the branch the finding
// named: on cancellation the coordinator drains every buffered result and
// reduces it before returning, which is exactly where an already-expired
// ceiling must not buy another file's worth of emission.
func TestTF142R21StopIsPolledInTheCancellationDrain(t *testing.T) {
	t.Parallel()

	paths := make([]string, 32)
	for i := range paths {
		paths[i] = fmt.Sprintf("pkg%02d.go", i)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reduced := 0
	err := runProviderFilePipeline(ctx,
		func() error { return ctx.Err() },
		paths, 2,
		func(_ context.Context, index int, path string) providerFileResult {
			return providerFileResult{index: index, path: path}
		},
		func(providerFileResult) error {
			reduced++
			// Stop the run from inside the first reduction, so the coordinator
			// enters its drain with results already buffered behind this one.
			cancel()
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pipeline = %v, want context.Canceled", err)
	}
	if reduced != 1 {
		t.Fatalf("the cancellation drain reduced %d result(s), want only the one already in flight", reduced)
	}
}
