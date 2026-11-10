package gitutil

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestGitCommandsPreserveContextDeadline(t *testing.T) {
	for _, name := range []string{"run", "runWithStderr"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			args := []string{"-test.run=^TestGitCommandContextHelper$", "--", "wait-for-context"}
			var err error
			if name == "run" {
				_, err = run(ctx, t.TempDir(), os.Args[0], args...)
			} else {
				_, _, err = runWithStderr(ctx, t.TempDir(), os.Args[0], args...)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancelled %s returned %v, want context deadline", name, err)
			}
		})
	}
}

func TestGitCommandContextHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "wait-for-context" {
		return
	}
	time.Sleep(10 * time.Second)
	os.Exit(23)
}

func TestTreeEntryMetadataBatchPreservesCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			_, err := treeEntryMetadataBatch(ctx, t.TempDir(), "HEAD", []string{"main.go"})
			if !errors.Is(err, want) {
				t.Fatalf("metadata lookup returned %v, want %v", err, want)
			}
		})
	}
}
