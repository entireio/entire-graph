package sem

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type cancelAfterContext struct {
	calls  int
	cancel int
}

func (c *cancelAfterContext) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }

func (c *cancelAfterContext) Done() <-chan struct{} { return nil }

func (c *cancelAfterContext) Err() error {
	c.calls++
	if c.calls >= c.cancel {
		return context.Canceled
	}
	return nil
}

func (c *cancelAfterContext) Value(key any) any { return context.Background().Value(key) }

func TestGitDirListedObservationStopsBeforeLaterPathsOnCancellation(t *testing.T) {
	ctx := &cancelAfterContext{cancel: 9}
	excluder := newGitDirExcluder(ctx, t.TempDir())
	excluder.observeListedPaths(
		[]string{"first/file.go", "second/file.go"},
		nil,
	)
	if err := excluder.listedObservationError(); !errors.Is(err, context.Canceled) {
		t.Fatalf("listed observation error = %v, want cancellation", err)
	}
	if !slices.Equal(excluder.observedDirs, []string{"first"}) {
		t.Fatalf("observed directories = %#v, want only first path chain", excluder.observedDirs)
	}
}
