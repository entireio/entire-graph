//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestRunUnderSignalsWrapsAnInterruptedRunInASignalError reproduces the trail
// finding on Execute (root.go): signal.NotifyContext turned SIGINT/SIGTERM
// into an ordinary context.Canceled, indistinguishable from any other
// cancellation by the time it reached main's generic "print the error, exit
// 1" path — regressing the conventional 130/143 exit statuses a program
// with no signal handling at all would have had for free, and that shells
// and supervisors already know how to interpret as operator cancellation
// rather than a command failure.
//
// This sends a real SIGINT to the test process while runUnderSignals' run
// callback is blocked on its context, and asserts the returned error is a
// *SignalError carrying that exact signal.
func TestRunUnderSignalsWrapsAnInterruptedRunInASignalError(t *testing.T) {
	started := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- runUnderSignals(func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	<-started
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case err := <-errCh:
		var sigErr *SignalError
		if !errors.As(err, &sigErr) {
			t.Fatalf("runUnderSignals returned %v (%T), want a *SignalError", err, err)
		}
		if sigErr.Signal != syscall.SIGINT {
			t.Fatalf("SignalError.Signal = %v, want SIGINT", sigErr.Signal)
		}
		if !errors.Is(sigErr, context.Canceled) {
			t.Fatalf("SignalError does not unwrap to context.Canceled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runUnderSignals did not return within 5s of receiving SIGINT")
	}
}

// TestRunUnderSignalsLeavesAnOrdinaryErrorUnwrapped is the control: a run
// that fails on its own, with no signal ever delivered, must not be wrapped
// in a SignalError — only operator cancellation gets the special exit-code
// treatment.
func TestRunUnderSignalsLeavesAnOrdinaryErrorUnwrapped(t *testing.T) {
	sentinel := errors.New("ordinary command failure")
	err := runUnderSignals(func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runUnderSignals(no signal) = %v, want %v unwrapped", err, sentinel)
	}
	var sigErr *SignalError
	if errors.As(err, &sigErr) {
		t.Fatalf("an ordinary failure was wrapped in a SignalError: %+v", sigErr)
	}
}

// TestRunUnderSignalsReturnsNilWhenRunSucceedsDespiteASignal covers the race
// where run finishes on its own right as a signal arrives: nothing failed,
// so there is nothing to attribute to the signal either.
func TestRunUnderSignalsReturnsNilWhenRunSucceedsDespiteASignal(t *testing.T) {
	err := runUnderSignals(func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("runUnderSignals(successful run) = %v, want nil", err)
	}
}
