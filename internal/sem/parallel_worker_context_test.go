package sem

import (
	"context"
	"errors"
	"testing"
)

// TestWorkerStopReportsCancellationBeforeBudget pins workerStop's two decisions
// and the order between them. The parse and dependents workers both stop on it,
// so the distinction matters: a cancellation is an error the caller asked for
// and its result is never reduced, while a budget stop is a partial result the
// reducer keeps and re-derives at the same index on its own.
func TestWorkerStopReportsCancellationBeforeBudget(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	always := func() bool { return true }
	never := func() bool { return false }

	if err, budget := workerStop(context.Background(), never); err != nil || budget {
		t.Fatalf("live context, in budget: workerStop = (%v, %v), want (nil, false)", err, budget)
	}
	if err, budget := workerStop(context.Background(), nil); err != nil || budget {
		t.Fatalf("live context, no budget at all: workerStop = (%v, %v), want (nil, false)", err, budget)
	}
	if err, budget := workerStop(context.Background(), always); err != nil || !budget {
		t.Fatalf("live context, over budget: workerStop = (%v, %v), want (nil, true)", err, budget)
	}
	if err, budget := workerStop(canceled, never); !errors.Is(err, context.Canceled) || budget {
		t.Fatalf("canceled context, in budget: workerStop = (%v, %v), want (context.Canceled, false)", err, budget)
	}
	// Cancellation wins: reporting this as a budget stop would turn a canceled
	// run into a truncated success carrying W_ANALYSIS_BUDGET_EXCEEDED warnings
	// about a budget that never expired.
	if err, budget := workerStop(canceled, always); !errors.Is(err, context.Canceled) || budget {
		t.Fatalf("canceled context, also over budget: workerStop = (%v, %v), want (context.Canceled, false)", err, budget)
	}
	// A canceled context must be reported even when the caller supplies no
	// budget function, which is the pipeline's shape when no budget is set.
	if err, budget := workerStop(canceled, nil); !errors.Is(err, context.Canceled) || budget {
		t.Fatalf("canceled context, no budget at all: workerStop = (%v, %v), want (context.Canceled, false)", err, budget)
	}
}
