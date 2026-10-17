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
