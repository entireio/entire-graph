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
// Each admitted item holds at most providerStreamBufferBatches batches in its
// channel; each active worker holds at most one more, and the consumer holds
// one. The lookahead window is bounded independently of repository size.
//
// providerStreamLookahead is how many items per worker may be admitted ahead of
// the consumer. Per-file cost is heavily skewed: while one slow file at the head
// of the order is still running, the window has to hold everything the other
// workers can finish, so it scales with the worker count. The backlog is
// window*providerStreamBufferBatches + workers + 1 batches, so a wider window
// with shallower buffers spends the same memory on more items: 16 items of 2
// batches per worker bound the same backlog as 4 items of 8 batches, for any
// worker count (67,840 records with 8 workers).
const (
	providerStreamBatchRecords  = 256
	providerStreamBufferBatches = 2
	providerStreamLookahead     = 16
)

// runIndexedStreamingPipeline preserves index order without retaining an entire
// item's output. A bounded window of items is admitted, each with a bounded
// batch buffer, and at most workers process callbacks run at once. Workers can
// move on as soon as an item finishes, without waiting for its output to be
// consumed in index order. A slow consumer still backpressures the producers.
// Workers take jobs from a FIFO queue filled in index order, so the item the
// consumer is waiting on is always running or finished: producers blocked on
// full buffers behind it can never hold the worker it needs.
// Results contain only metadata needed after that item's stream is consumed.
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
		index   int
		batches chan []T
		result  R
	}
	window := min(count, workers*providerStreamLookahead)
	jobs := make(chan *slot, window)
	defer close(jobs)
	for range workers {
		group.Go(func() {
			for {
				if workerCtx.Err() != nil {
					return
				}
				var out *slot
				select {
				case <-workerCtx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					out = job
				}
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
				out.result = process(workerCtx, out.index, func(value T) {
					if batch == nil {
						batch = make([]T, 0, providerStreamBatchRecords)
					}
					batch = append(batch, value)
					if len(batch) == providerStreamBatchRecords {
						flush()
					}
				})
				flush()
				close(out.batches)
			}
		})
	}
	start := func(index int) *slot {
		out := &slot{index: index, batches: make(chan []T, providerStreamBufferBatches)}
		jobs <- out
		return out
	}
	slots := make([]*slot, window)
	for i := range window {
		slots[i] = start(i)
	}
	for index := range count {
		out := slots[index%window]
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
		if next := index + window; next < count {
			slots[index%window] = start(next)
		} else {
			slots[index%window] = nil
		}
	}
	return ctx.Err()
}
