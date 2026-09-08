package sem

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndexedStreamingPipelineBoundsAndOrdersLargeOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const files, workers, records = 17, 8, 10000
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
				if index == 0 && record == 0 {
					// The consumer must see output before the first file finishes.
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
	// One value can be held by the consumer and one by each blocked producer,
	// in addition to the fixed channel buffers, independent of file size.
	if max := int64(workers*(providerStreamBufferRecords+1) + 1); peak.Load() > max {
		t.Fatalf("buffered %d records, limit %d", peak.Load(), max)
	}
	if nextFile != files || outstanding.Load() != 0 {
		t.Fatalf("incomplete stream: files=%d outstanding=%d", nextFile, outstanding.Load())
	}
}

func TestIndexedStreamingPipelineStopsAndJoinsBlockedProducers(t *testing.T) {
	for _, stop := range []string{"consumer-error", "cancel"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			consumerErr := errors.New("consumer stopped")
			var started, finished atomic.Int64
			err := runIndexedStreamingPipeline(ctx, 100, 8,
				func(ctx context.Context, index int, emit func(int)) int {
					started.Add(1)
					defer finished.Add(1)
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
				func(_, _ int) error { t.Error("unfinished producer reduced"); return nil })
			want := consumerErr
			if stop == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			if started.Load() != 8 || finished.Load() != started.Load() {
				t.Fatalf("workers not joined: started=%d finished=%d", started.Load(), finished.Load())
			}
		})
	}
}
