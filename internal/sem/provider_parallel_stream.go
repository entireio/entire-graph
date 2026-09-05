package sem

import (
	"context"
	"sync"
)

const providerStreamBufferRecords = 32

// runIndexedStreamingPipeline preserves index order without retaining an entire
// item's output. At most workers items are admitted, each with a fixed record
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
		values chan T
		result R
	}
	start := func(index int) *slot {
		out := &slot{values: make(chan T, providerStreamBufferRecords)}
		group.Go(func() {
			defer close(out.values)
			out.result = process(workerCtx, index, func(value T) {
				if workerCtx.Err() != nil {
					return
				}
				select {
				case out.values <- value:
				case <-workerCtx.Done():
				}
			})
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
			case value, ok := <-out.values:
				if !ok {
					// Closing the channel publishes the final result, including
					// when process emitted no values.
					if err := reduce(index, out.result); err != nil {
						return err
					}
					goto next
				}
				if err := emit(index, value); err != nil {
					return err
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
