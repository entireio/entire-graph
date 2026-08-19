package sem

import (
	"context"
	"time"
)

// budgetGate answers "is the wall-clock budget gone?" from the CLOCK, not only
// from the work context's error.
//
// context.WithDeadline reports expiry through a runtime timer: ctx.Err() flips
// only once that timer has fired, which is strictly after the deadline passed.
// The lag is the platform's timer granularity -- sub-microsecond on Linux and
// macOS, up to one system tick (~15.6 ms) on Windows. A loop that observes the
// budget ONLY through ctx.Err() therefore keeps working for the whole lag after
// the budget is actually gone, and for a short input that window is enough to
// run an entire preprocessing pass on an already-expired budget. That is not a
// stopwatch flake: it is a real, platform-dependent overshoot in the product.
//
// Comparing the clock against the deadline directly makes the observation
// level-triggered, so it is true the instant the budget is gone on every
// platform, whatever the timer does. The context is still the mechanism that
// stops blocking work (the file pipeline selects on ctx.Done); the gate is the
// mechanism that stops polling work.
type budgetGate struct {
	work context.Context
	// deadline is the zero time when the caller opted into no budget. Only an
	// opt-in MaxDuration deadline belongs here: a deadline that came from the
	// CALLER's own context stays the context's business, so classifyStop can
	// keep telling the two apart.
	deadline time.Time
	now      func() time.Time
}

// newBudgetGate pairs the work context with the opt-in deadline derived from
// MaxDuration. Pass the zero time when MaxDuration is unset; the gate then
// reports exactly what the context reports and costs one ctx.Err() call.
func newBudgetGate(work context.Context, deadline time.Time) budgetGate {
	return budgetGate{work: work, deadline: deadline, now: time.Now}
}

// err reports the work context's stop reason, treating a deadline the clock has
// already passed as context.DeadlineExceeded even when the context's timer has
// not fired yet.
func (g budgetGate) err() error {
	if g.work == nil {
		return nil
	}
	if err := g.work.Err(); err != nil {
		return err
	}
	if !g.deadline.IsZero() && !g.now().Before(g.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// expired is the predicate form of err.
func (g budgetGate) expired() bool { return g.err() != nil }

// reader wraps a content reader so that every read refuses once the budget is
// gone. This is the structural close for the "a loop keeps reading whole files
// after the budget expired" class: the relation-phase producers are a
// hand-written sequence of functions that each build a full slice before
// returning, and the expensive ones are expensive because they scan file
// CONTENT per symbol or per file. They all read through the one contentReader
// they are handed, so gating the reader bounds every one of them -- including
// producers nobody has audited yet -- to at most one in-flight read past
// expiry, without changing a single producer signature.
//
// An unbudgeted, uncancelled run is unaffected byte for byte: err() is nil, so
// the wrapper is one context read per file read and the same content is
// returned.
func (g budgetGate) reader(read contentReader) contentReader {
	if read == nil {
		return nil
	}
	return func(path string) (string, bool) {
		if g.expired() {
			return "", false
		}
		return read(path)
	}
}
