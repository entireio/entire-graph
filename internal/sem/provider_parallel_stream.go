package sem

import (
	"context"
	"sync"
)

// providerStreamBatchRecords is how many records a worker accumulates before
// one channel handoff, and providerStreamBufferBatches how many full batches a
// slot's channel holds. Per-RECORD handoffs made the scheduler the hot path: a
// quarter-million records each paid a channel send/receive, and every one that
// found the consumer parked (or a tiny buffer full) paid a kernel wakeup —
// measured at ~31% of snapshot CPU in runtime.pthread_cond_signal. Batching
// divides the handoffs by the batch size; the consumer still sees records in
// exactly the order the worker produced them, so output bytes are unchanged.
// The bound on unconsumed records per admitted item is
// (providerStreamBufferBatches+1)*providerStreamBatchRecords plus the batch
// being filled, and a finished item holds only its buffered batches — the
// backpressure the old 32-record buffer existed to provide, at a size that no
// longer starves the producers between consumer wakeups.
const (
	providerStreamBatchRecords  = 256
	providerStreamBufferBatches = 2
)

// providerStreamWindowFactor is how many items per worker may be admitted ahead
// of the consumer. Per-file cost is heavily skewed, and with only workers items
// admitted one slow file at the head of the order idled the rest: the reducer
// sat in chanrecv for ~59% of a cold index while ~1.6 of 8 workers stayed busy.
// The window has to hold everything the other workers can finish while the
// head is still running, so it scales with the worker count. Measured on cold
// indexes of four repositories, 16 gets 90-99% of the speedup 32 gets (8 gets
// 71-99%, 4 gets 46-85%) at half the backlog. The backlog stays bounded: a
// running item holds at most providerStreamBufferBatches+1 batches, a finished
// one its providerStreamBufferBatches buffered batches, and the consumer one
// more, so at most workers*(providerStreamBufferBatches+1) +
// (window-workers)*providerStreamBufferBatches + 1 batches are unconsumed.
const providerStreamWindowFactor = 16

// runIndexedStreamingPipeline preserves index order without retaining an entire
// item's output. At most workers items run process at once, but up to
// workers*providerStreamWindowFactor items (the window) may be admitted ahead
// of the consumer, each with a bounded batch buffer, so later items keep the
// workers busy while an earlier one is still producing. The consumer takes run
// tokens itself, in index order: the item it is waiting on is always running or
// finished, so producers blocked on full buffers can never hold the tokens it
// needs, and nothing is admitted after the consumer returns. A slow consumer or
// earlier item backpressures the producers. Results contain only metadata
// needed after that item's stream has been consumed.
func runIndexedStreamingPipeline[T, R any](
	ctx context.Context,
	count, workers int,
	process func(context.Context, int, func(T)) R,
	emit func(int, T) error,
	reduce func(int, R) error,
) error {
	if count == 0 || ctx.Err() != nil {
		return ctx.Err()
	}
	workers = min(count, boundedProviderWorkerCount(workers))
	window := min(count, workers*providerStreamWindowFactor)
	workerCtx, cancel := context.WithCancel(ctx)
	var group sync.WaitGroup
	defer func() {
		cancel()
		group.Wait()
	}()
	type slot struct {
		batches chan []T
		result  R
	}
	// One token per item inside process. A worker frees its token before it
	// closes its channel, so once the consumer has seen an item end, that
	// item's token is free again.
	running := make(chan struct{}, workers)
	start := func(index int) *slot {
		out := &slot{batches: make(chan []T, providerStreamBufferBatches)}
		group.Go(func() {
			defer close(out.batches)
			defer func() { <-running }()
			// The batch allocates lazily on first append: many files emit
			// nothing, and the final flush would otherwise leave a dead
			// replacement buffer behind on every file. No per-record
			// cancellation check: that was the last per-record acquisition of
			// the shared context mutex in this hot path, and flush's select
			// already observes cancellation once per batch, which is as often
			// as anything downstream can act on it.
			var batch []T
			flush := func() {
				if len(batch) == 0 {
					return
				}
				select {
				case out.batches <- batch:
				case <-workerCtx.Done():
				}
				batch = nil
			}
			out.result = process(workerCtx, index, func(value T) {
				if batch == nil {
					batch = make([]T, 0, providerStreamBatchRecords)
				}
				batch = append(batch, value)
				if len(batch) == providerStreamBatchRecords {
					flush()
				}
			})
			flush()
		})
		return out
	}
	// A ring over the window: items [index, admitted) are admitted and not yet
	// consumed, and admitted-index never exceeds window.
	slots := make([]*slot, window)
	admitted := 0
	admit := func() {
		slots[admitted%window] = start(admitted)
		admitted++
	}
consume:
	for index := range count {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Start everything a free token and the window allow before
			// blocking, so the first workers items start before anything is
			// consumed. When nothing is admitted ahead every token is free, so
			// index is always admitted after this loop.
		admitting:
			for admitted < count && admitted-index < window {
				select {
				case running <- struct{}{}:
					admit()
				default:
					break admitting
				}
			}
			// Wait for the head's next batch or a freed token, whichever comes
			// first: a slow head must not stop admission behind it.
			var tokens chan<- struct{}
			if admitted < count && admitted-index < window {
				tokens = running
			}
			head := slots[index%window]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case tokens <- struct{}{}:
				admit()
			case batch, ok := <-head.batches:
				if !ok {
					// Closing the channel publishes the final result, including
					// when process emitted no values.
					if err := reduce(index, head.result); err != nil {
						return err
					}
					slots[index%window] = nil
					continue consume
				}
				for i := range batch {
					if err := emit(index, batch[i]); err != nil {
						return err
					}
				}
			}
		}
	}
	return ctx.Err()
}
