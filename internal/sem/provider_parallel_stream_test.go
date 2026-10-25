package sem

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// indexedStreamingPipelineRecordBound is the most records the pipeline may
// hold produced but not yet consumed, in batch units independent of item size.
// The window is computed here rather than read from the implementation, so a
// pipeline that admits past it cannot pass by moving its own limit. A running
// item holds the batch being filled (or one full batch blocked in flush) plus
// its channel's buffered batches; an item that has finished holds only its
// buffered batches; the consumer holds at most one more batch mid-iteration.
func indexedStreamingPipelineRecordBound(count, workers int) int64 {
	window := min(count, workers*providerStreamLookahead)
	running := min(window, workers)
	batches := running*(providerStreamBufferBatches+1) + (window-running)*providerStreamBufferBatches + 1
	return int64(batches * providerStreamBatchRecords)
}

func TestIndexedStreamingPipelineBoundsAndOrdersLargeOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const files, workers, records = 17, 8, 10000
	// The liveness interlock below fires at record providerStreamBatchRecords;
	// with fewer records per file it would silently never run and the pin
	// would pass vacuously.
	if records <= providerStreamBatchRecords {
		t.Fatalf("records (%d) must exceed the batch size (%d) for the liveness pin to fire", records, providerStreamBatchRecords)
	}
	var outstanding, peak atomic.Int64
	firstConsumed := make(chan struct{})
	nextFile, nextRecord := 0, 0
	err := runIndexedStreamingPipeline(ctx, files, workers,
		func(ctx context.Context, index int, emit func(int)) int {
			for record := range records {
				if ctx.Err() != nil {
					return record
				}
				n := outstanding.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				emit(record)
				if index == 0 && record == providerStreamBatchRecords {
					// The consumer must see output before the first file
					// finishes. Handoffs are batched, so the earliest a record
					// can be visible is when its batch fills (or the file
					// ends); by one record past the batch size the first batch
					// has flushed, and the consumer must have received it
					// while this producer is still mid-file.
					select {
					case <-firstConsumed:
					case <-ctx.Done():
						return record
					}
				}
			}
			return records
		},
		func(index, record int) error {
			outstanding.Add(-1)
			if index != nextFile || record != nextRecord {
				t.Errorf("got file/record %d/%d, want %d/%d", index, record, nextFile, nextRecord)
			}
			if index == 0 && record == 0 {
				close(firstConsumed)
			}
			nextRecord++
			return nil
		},
		func(index, count int) error {
			if index != nextFile || count != records || nextRecord != records {
				t.Errorf("incomplete file %d: result=%d, consumed=%d", index, count, nextRecord)
			}
			nextFile++
			nextRecord = 0
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// Buffered batches are bounded by the admission window. Each active worker
	// and the consumer can hold one additional batch, independent of file size.
	if max := indexedStreamingPipelineRecordBound(files, workers); peak.Load() > max {
		t.Fatalf("buffered %d records, limit %d", peak.Load(), max)
	}
	if nextFile != files || outstanding.Load() != 0 {
		t.Fatalf("incomplete stream: files=%d outstanding=%d", nextFile, outstanding.Load())
	}
}

func TestIndexedStreamingPipelineStopsAndJoinsBlockedProducers(t *testing.T) {
	for _, stop := range []string{"consumer-error", "reducer-error", "cancel"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			consumerErr := errors.New("consumer stopped")
			var started, finished atomic.Int64
			allStarted := make(chan struct{})
			err := runIndexedStreamingPipeline(ctx, 100, 8,
				func(ctx context.Context, index int, emit func(int)) int {
					if started.Add(1) == 8 {
						close(allStarted)
					}
					defer finished.Add(1)
					select {
					case <-allStarted:
					case <-ctx.Done():
						return index
					}
					if stop == "reducer-error" && index == 0 {
						return index
					}
					for ctx.Err() == nil {
						emit(index)
					}
					return index
				},
				func(_, _ int) error {
					if stop == "cancel" {
						cancel()
						return nil
					}
					return consumerErr
				},
				func(index, _ int) error {
					if stop == "reducer-error" && index == 0 {
						return consumerErr
					}
					t.Error("unfinished producer reduced")
					return nil
				})
			want := consumerErr
			if stop == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			// The completed first item can admit one more process callback
			// before the reducer returns its error.
			maxStarted := int64(8)
			if stop == "reducer-error" {
				maxStarted++
			}
			if started.Load() < 8 || started.Load() > maxStarted || finished.Load() != started.Load() {
				t.Fatalf("workers not joined: started=%d finished=%d", started.Load(), finished.Load())
			}
		})
	}
}

// A slow first item must not keep idle workers from processing the rest of
// the admitted window. Interlock on completion rather than wall-clock timing.
func TestIndexedStreamingPipelineLooksAheadWithBoundedAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const workers = 2
	const window = workers * providerStreamLookahead
	var started, active, peak atomic.Int64
	windowFinished := make(chan struct{})
	next := 0
	err := runIndexedStreamingPipeline(ctx, window*3, workers,
		func(ctx context.Context, index int, emit func(int)) int {
			started.Add(1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			if index == 0 {
				select {
				case <-windowFinished:
				case <-ctx.Done():
					return index
				}
				if got := started.Load(); got != window {
					t.Errorf("admitted %d items before consuming the first; want %d", got, window)
				}
			}
			// Alternate empty and nonempty results to cover both handoffs.
			if index%2 == 0 {
				emit(index)
			}
			if index == window-1 {
				close(windowFinished)
			}
			return index
		},
		func(index, value int) error {
			if index != next || value != index || index%2 != 0 {
				t.Errorf("out-of-order emission: index=%d value=%d next=%d", index, value, next)
			}
			return nil
		},
		func(index, value int) error {
			if index != next || value != index {
				t.Errorf("out-of-order reduction: index=%d value=%d next=%d", index, value, next)
			}
			next++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if next != window*3 || peak.Load() > workers || active.Load() != 0 {
		t.Fatalf("reduced=%d peak workers=%d active=%d", next, peak.Load(), active.Load())
	}
}

// A slow head must not idle the other workers: while item 0 is still running,
// later items run to completion until the window is full, and no item past the
// window starts until the head has been consumed. With only workers items
// admitted, 7 items finished behind a blocked head and the rest of the workers
// sat idle.
func TestIndexedStreamingPipelineAdmitsWindowPastSlowHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const files, workers = 300, 8
	window := min(files, workers*providerStreamLookahead)
	if window <= workers {
		t.Fatalf("window (%d) must exceed workers (%d) for a slow head to be overtaken", window, workers)
	}
	// Each later item emits exactly what its buffer holds, so it finishes
	// without waiting for the consumer and keeps every buffered batch: a full
	// window is the largest backlog finished items can build.
	records := providerStreamBufferBatches * providerStreamBatchRecords
	var started, finished, outstanding, peak atomic.Int64
	overtaken := make(chan struct{})
	nextFile, nextRecord := 0, 0
	err := runIndexedStreamingPipeline(ctx, files, workers,
		func(ctx context.Context, index int, emit func(int)) int {
			started.Add(1)
			if index == 0 {
				select {
				case <-overtaken:
				case <-time.After(5 * time.Second):
					t.Errorf("%d later items finished while the head ran, want %d", finished.Load(), window-1)
				case <-ctx.Done():
					return 0
				}
				// Give a pipeline that admits past the window time to start more.
				time.Sleep(20 * time.Millisecond)
				if got := started.Load(); got != int64(window) {
					t.Errorf("started %d items before the head was consumed, want the window, %d", got, window)
				}
			}
			for record := range records {
				n := outstanding.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				emit(record)
			}
			if index != 0 && finished.Add(1) == int64(window-1) {
				close(overtaken)
			}
			return records
		},
		func(index, record int) error {
			outstanding.Add(-1)
			if index != nextFile || record != nextRecord {
				t.Errorf("got file/record %d/%d, want %d/%d", index, record, nextFile, nextRecord)
			}
			nextRecord++
			return nil
		},
		func(index, count int) error {
			if index != nextFile || count != records || nextRecord != records {
				t.Errorf("incomplete file %d: result=%d, consumed=%d", index, count, nextRecord)
			}
			nextFile++
			nextRecord = 0
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if max := indexedStreamingPipelineRecordBound(files, workers); peak.Load() > max {
		t.Fatalf("buffered %d records, limit %d", peak.Load(), max)
	}
	if nextFile != files || outstanding.Load() != 0 {
		t.Fatalf("incomplete stream: files=%d outstanding=%d", nextFile, outstanding.Load())
	}
}

// Producers whose output exceeds their buffer block holding a worker. Behind a
// slow head they can hold every worker but the head's: jobs are taken in index
// order, so the head always has one and the stream drains. A pipeline that let
// later items race the head for workers deadlocked here.
func TestIndexedStreamingPipelineLargeItemsBehindSlowHeadDrain(t *testing.T) {
	const files, workers = 200, 8
	records := (providerStreamBufferBatches+1)*providerStreamBatchRecords + 1
	for rep := range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		var consumed atomic.Int64
		go func() {
			done <- runIndexedStreamingPipeline(ctx, files, workers,
				func(ctx context.Context, index int, emit func(int)) int {
					if index%(workers*2) == 0 {
						time.Sleep(time.Millisecond)
					}
					for record := range records {
						emit(record)
					}
					return records
				},
				func(int, int) error { consumed.Add(1); return nil },
				func(int, int) error { return nil })
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("rep %d: %v", rep, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("rep %d: deadlocked after %d of %d records", rep, consumed.Load(), files*records)
		}
		cancel()
		if got := consumed.Load(); got != int64(files*records) {
			t.Fatalf("rep %d: consumed %d records, want %d", rep, got, files*records)
		}
	}
}

// Cancelling from outside the consumer, mid-stream, while producers sit in
// every state the window allows (running, blocked on a full buffer, finished
// but unconsumed, not yet admitted), returns the context's error promptly,
// joins every producer before returning, and leaves no goroutine behind.
func TestIndexedStreamingPipelineCancelMidStreamJoinsWithoutLeak(t *testing.T) {
	const files, workers = 400, 8
	baseline := runtime.NumGoroutine()
	for rep := range 20 {
		ctx, cancel := context.WithCancel(t.Context())
		cancelAt := int64(1 + rep*1543%20000)
		trigger := make(chan struct{})
		go func() {
			select {
			case <-trigger:
			case <-ctx.Done():
			}
			cancel()
		}()
		var started, finished, consumed atomic.Int64
		done := make(chan error, 1)
		go func() {
			done <- runIndexedStreamingPipeline(ctx, files, workers,
				func(ctx context.Context, index int, emit func(int)) int {
					started.Add(1)
					defer finished.Add(1)
					records := 50 // finishes into its buffer
					if index%5 == 0 {
						records = 2000 // blocks on its buffer
					}
					for record := range records {
						// Like the real producers, check for cancellation only
						// now and then, so a stopped producer still has a
						// partial batch to hand off on its way out; and exit
						// slowly, so returning before the join is visible.
						if record%64 == 0 && ctx.Err() != nil {
							time.Sleep(time.Millisecond)
							return record
						}
						emit(record)
					}
					return records
				},
				func(int, int) error {
					if consumed.Add(1) == cancelAt {
						close(trigger)
						// The cancel comes from another goroutine. Hold the
						// consumer until it lands, so the stream cannot drain
						// first and return nil on a busy runner; producers keep
						// running and filling their buffers meanwhile.
						select {
						case <-ctx.Done():
						case <-time.After(5 * time.Second):
							t.Errorf("rep %d: the external cancel never landed", rep)
						}
					}
					return nil
				},
				func(int, int) error { return nil })
		}()
		var err error
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("rep %d: did not return after cancellation (deadlock)", rep)
		}
		joined := started.Load() == finished.Load()
		cancel()
		// Failures stop the reps but not the test: the leak check below
		// still runs, so a leaking pipeline is reported as one.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("rep %d: got %v, want %v", rep, err, context.Canceled)
			break
		}
		if !joined {
			t.Errorf("rep %d: returned before joining every producer", rep)
			break
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		t.Fatalf("%d goroutines leaked; pipeline stacks:\n%s", n-baseline, pipelineStacks(stacks))
	}
}

// pipelineStacks keeps the goroutine dumps that mention the pipeline.
func pipelineStacks(all string) string {
	var kept []string
	for _, g := range strings.Split(all, "\n\n") {
		if strings.Contains(g, "runIndexedStreamingPipeline") {
			kept = append(kept, g)
		}
	}
	return strings.Join(kept, "\n\n")
}
