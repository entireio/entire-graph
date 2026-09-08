package gitutil

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRunCarriesTheContextErrorWhenTheSubprocessIsKilled pins the reason a
// caller's own deadline stays legible.
//
// exec.CommandContext kills the child when the context ends, and Wait then
// reports the SIGNAL ("signal: killed"), never the cause. run formats that into
// its message, so before this a caller that set a deadline received an error
// indistinguishable from an ordinary Git failure, and errors.Is against
// context.DeadlineExceeded could not hold. It surfaced as a load-dependent CI
// failure: the same command completes before the deadline on an idle machine
// and is killed mid-flight on a busy one.
func TestRunCarriesTheContextErrorWhenTheSubprocessIsKilled(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")

	// Already expired: the child is killed as soon as it starts, on every
	// platform and at any load.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := run(ctx, repo, "git", "rev-parse", "--show-prefix")
	if err == nil {
		t.Fatal("an expired context must not produce a successful git read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false, got %v", err)
	}
	// The argv and Git's own message are what make the failure diagnosable, so
	// carrying the cause must not cost them.
	if !strings.Contains(err.Error(), "rev-parse --show-prefix") {
		t.Errorf("error text lost the argv: %v", err)
	}
}

// TestRunLeavesAnOrdinaryGitFailureUnwrapped is the negative control: a failure
// that is not the context's must not be relabelled as one.
func TestRunLeavesAnOrdinaryGitFailureUnwrapped(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")

	_, err := run(t.Context(), repo, "git", "rev-parse", "--verify", "refs/heads/does-not-exist")
	if err == nil {
		t.Fatal("resolving a missing ref must fail")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("an ordinary git failure was labelled a context error: %v", err)
	}
}
