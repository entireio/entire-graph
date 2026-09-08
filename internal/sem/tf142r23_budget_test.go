package sem

import (
	"context"
	"testing"
	"time"
)

// TestTF142R23CallerDeadlineEqualToBudgetIsCallerOwned pins the boundary of the
// caller-vs-budget split. When the caller's own context deadline lands on the
// SAME instant as the opt-in MaxDuration deadline, the caller's context is done
// and the documented contract is that its expiry is returned as an error, not
// swallowed into a nil-error truncation. A strict `Before` comparison
// misclassifies exactly this tie.
func TestTF142R23CallerDeadlineEqualToBudgetIsCallerOwned(t *testing.T) {
	t.Parallel()
	base := time.Now()
	shared := base.Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), shared)
	defer cancel()
	workCtx, cancelWork := context.WithDeadline(ctx, shared)
	defer cancelWork()
	now := func() time.Time { return shared.Add(time.Millisecond) }
	gate := newBudgetGate(workCtx, shared, now)

	if isOptInBudgetExceeded(ctx, gate, shared, time.Hour, context.DeadlineExceeded) {
		t.Fatal("a caller deadline equal to the budget deadline is caller-owned: it must surface as an error, not classify as opt-in truncation")
	}
}

// TestTF142R23CallerDeadlineAfterBudgetStaysOptIn is the other direction: a
// caller deadline strictly LATER than the budget deadline means MaxDuration is
// the binding ceiling, so expiry is our truncation to report.
func TestTF142R23CallerDeadlineAfterBudgetStaysOptIn(t *testing.T) {
	t.Parallel()
	base := time.Now()
	budgetDL := base.Add(time.Hour)
	callerDL := budgetDL.Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), callerDL)
	defer cancel()
	workCtx, cancelWork := context.WithDeadline(ctx, budgetDL)
	defer cancelWork()
	now := func() time.Time { return budgetDL.Add(time.Millisecond) }
	gate := newBudgetGate(workCtx, budgetDL, now)

	if !isOptInBudgetExceeded(ctx, gate, budgetDL, time.Hour, context.DeadlineExceeded) {
		t.Fatal("a caller deadline later than the budget deadline must stay an opt-in truncation")
	}
}

// TestTF142R23BudgetBindsFirstEvenAfterTheCallerDeadlinePasses pins the
// causal reading of the split: the owner of the stop is whoever's deadline
// fired FIRST. A budget deadline strictly earlier than the caller's is the one
// that stopped the work, so expiry stays an opt-in truncation even once the
// clock has since run past the caller's deadline too while the run finished up.
func TestTF142R23BudgetBindsFirstEvenAfterTheCallerDeadlinePasses(t *testing.T) {
	t.Parallel()
	base := time.Now()
	budgetDL := base.Add(time.Hour)
	callerDL := budgetDL.Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), callerDL)
	defer cancel()
	workCtx, cancelWork := context.WithDeadline(ctx, budgetDL)
	defer cancelWork()
	// The clock is past BOTH deadlines by the time the stop is classified.
	now := func() time.Time { return callerDL.Add(time.Minute) }
	gate := newBudgetGate(workCtx, budgetDL, now)

	if !isOptInBudgetExceeded(ctx, gate, budgetDL, time.Hour, context.DeadlineExceeded) {
		t.Fatal("the budget deadline fired first, so this is an opt-in truncation even though the caller deadline has since passed as well")
	}
}
