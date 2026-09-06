package sem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestListedWorktreeFilteringReturnsCancellationBeforeSuccess(t *testing.T) {
	ctx := &cancelAfterContext{cancel: 2}
	gitDirs := newGitDirExcluder(context.Background(), t.TempDir())
	listed := make([]string, 64)
	kinds := make([]listedPathKind, len(listed))
	for index := range listed {
		listed[index] = filepath.ToSlash(filepath.Join("src", "file", string(rune('a'+index))))
		kinds[index] = listedPathRegular
	}
	paths, err := filterListedWorktreePaths(ctx, listed, kinds, gitDirs, ignoreMatcher{}, nil, func(string) bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("filter error = %v, want cancellation", err)
	}
	if paths != nil {
		t.Fatalf("filtered paths = %#v, want no successful listing", paths)
	}
}

func TestPromotedWorktreeFilteringReturnsCancellationBeforeVisiting(t *testing.T) {
	ctx := &cancelAfterContext{cancel: 2}
	gitDirs := newGitDirExcluder(context.Background(), t.TempDir())
	paths, err := filterPromotedWorktreePaths(ctx, []string{"a.go", "b.go", "c.go"}, gitDirs)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("filter error = %v, want cancellation", err)
	}
	if paths != nil {
		t.Fatalf("filtered paths = %#v, want no successful listing", paths)
	}
}

func TestPromoteUnverifiedGitDirsStopsBeforeLaterDirectoryOnCancellation(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.MkdirAll(filepath.Join(repo, name, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repo, name, "refs"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &cancelAfterContext{cancel: int(^uint(0) >> 1)}
	gitDirs := newGitDirExcluder(ctx, repo)
	gitDirs.hiddenEvidence = 1
	gitDirs.observedDirs = []string{"first", "second"}
	ctx.cancel = ctx.calls + 3
	gitDirs.promoteUnverifiedGitDirs()
	if err := gitDirs.listedObservationError(); !errors.Is(err, context.Canceled) {
		t.Fatalf("promotion error = %v, want cancellation", err)
	}
	if _, ok := gitDirs.targets["first"]; !ok {
		t.Fatalf("first observed directory was not promoted before cancellation")
	}
	if _, ok := gitDirs.targets["second"]; ok {
		t.Fatalf("second observed directory was promoted after cancellation")
	}
}
