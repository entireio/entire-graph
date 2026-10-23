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
// The bound on admitted-but-unconsumed records per worker is
// (providerStreamBufferBatches+1)*providerStreamBatchRecords plus the batch
// being filled — a few MB across eight workers, the backpressure the old
// 32-record buffer existed to provide, at a size that no longer starves the
// producers between consumer wakeups.
const (
	providerStreamBatchRecords  = 256
	providerStreamBufferBatches = 2
)

// runIndexedStreamingPipeline preserves index order without retaining an entire
// item's output. At most workers items are admitted, each with a bounded batch
// buffer. A slow consumer or earlier item backpressures the producers. Results
// contain only metadata needed after that item's stream has been consumed.
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
	start := func(index int) *slot {
		out := &slot{batches: make(chan []T, providerStreamBufferBatches)}
		group.Go(func() {
			defer close(out.batches)
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
	slots := make([]*slot, workers)
	for i := range workers {
		slots[i] = start(i)
	}
	for index := range count {
		out := slots[index%workers]
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case batch, ok := <-out.batches:
				if !ok {
					// Closing the channel publishes the final result, including
					// when process emitted no values.
					if err := reduce(index, out.result); err != nil {
						return err
					}
					goto next
				}
				for i := range batch {
					if err := emit(index, batch[i]); err != nil {
						return err
					}
				}
			}
		}
	next:
		if next := index + workers; next < count {
			slots[index%workers] = start(next)
		} else {
			slots[index%workers] = nil
		}
	}
	return ctx.Err()
}
