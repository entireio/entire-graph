package cli

import (
	"context"
	"os"
	"testing"
)

// The signal watcher used to hand the caught signal off and cancel the run
// before restoring the default disposition. signal.Notify keeps routing signals
// into sigCh for as long as it is registered, and sigCh is a one-slot buffer
// nobody reads a second time, so every signal arriving in that window was
// BUFFERED rather than delivered -- a second Ctrl-C on a stuck run was
// swallowed. cancel() in particular walks and closes every child context, so
// the window it added is not a couple of instructions.
//
// The ordering is the fix and the ordering is what is asserted: restoring the
// default disposition must happen before anything else the handler does.
func TestHandleFirstSignalRestoresDefaultDispositionFirst(t *testing.T) {
	t.Parallel()

	var order []string
	caughtCh := make(chan os.Signal, 1)
	_, realCancel := context.WithCancel(context.Background())
	defer realCancel()

	stop := func() {
		order = append(order, "stop")
		// The handoff is the next thing the handler does, so an empty buffer
		// here is what proves the restore came first. A reordered handler has
		// already sent by this point.
		if len(caughtCh) != 0 {
			t.Errorf("the signal was handed off before the default disposition was restored: len(caughtCh) = %d, want 0", len(caughtCh))
		}
	}
	cancel := context.CancelFunc(func() {
		order = append(order, "cancel")
		realCancel()
	})

	handleFirstSignal(os.Interrupt, caughtCh, cancel, stop)

	if len(order) != 2 || order[0] != "stop" || order[1] != "cancel" {
		t.Fatalf("handler ran %v, want [stop cancel]: the default disposition must be restored before the run is cancelled", order)
	}
	select {
	case got := <-caughtCh:
		if got != os.Interrupt {
			t.Fatalf("caught signal = %v, want %v", got, os.Interrupt)
		}
	default:
		t.Fatal("the caught signal was never handed off, so runUnderSignals would report no signal at all")
	}
}
