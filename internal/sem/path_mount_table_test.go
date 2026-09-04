package sem

import (
	"errors"
	"sync"
	"testing"
)

func countingMountReader(points map[string]struct{}, errs ...error) (func() (map[string]struct{}, error), *int) {
	calls := 0
	remaining := errs
	return func() (map[string]struct{}, error) {
		calls++
		if len(remaining) > 0 {
			err := remaining[0]
			remaining = remaining[1:]
			return nil, err
		}
		return points, nil
	}, &calls
}

func TestMountTableCacheReadsOncePerScope(t *testing.T) {
	t.Parallel()
	points := map[string]struct{}{"/": {}, "/proc": {}}
	read, calls := countingMountReader(points)
	cache := &mountTableCache{}

	for attempt := 0; attempt < 3; attempt++ {
		got, err := cache.load(read)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(got) != len(points) {
			t.Fatalf("got %d mount points, want %d", len(got), len(points))
		}
	}
	if *calls != 1 {
		t.Fatalf("read the mount table %d times, want 1", *calls)
	}
}

func TestMountTableCacheServesAnEmptyTable(t *testing.T) {
	t.Parallel()
	read, calls := countingMountReader(map[string]struct{}{})
	cache := &mountTableCache{}

	for attempt := 0; attempt < 2; attempt++ {
		got, err := cache.load(read)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got == nil {
			t.Fatal("load returned a nil mount-point set")
		}
	}
	if *calls != 1 {
		t.Fatalf("read the mount table %d times, want 1", *calls)
	}
}

// A failed read must not be remembered. Caching it would let one transient
// failure refuse every path for the rest of the operation, where the
// per-resolver read this cache replaces retried on the next path.
func TestMountTableCacheRetriesAfterAFailedRead(t *testing.T) {
	t.Parallel()
	failure := errors.New("mount table unavailable")
	points := map[string]struct{}{"/": {}}
	read, calls := countingMountReader(points, failure, failure)
	cache := &mountTableCache{}

	if _, err := cache.load(read); !errors.Is(err, failure) {
		t.Fatalf("first load returned %v, want %v", err, failure)
	}
	if _, err := cache.load(read); !errors.Is(err, failure) {
		t.Fatalf("second load returned %v, want %v", err, failure)
	}
	got, err := cache.load(read)
	if err != nil {
		t.Fatalf("third load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d mount points, want 1", len(got))
	}
	if _, err := cache.load(read); err != nil {
		t.Fatalf("fourth load: %v", err)
	}
	if *calls != 3 {
		t.Fatalf("read the mount table %d times, want 3", *calls)
	}
}

func TestMountTableCacheReadsOnceUnderConcurrency(t *testing.T) {
	t.Parallel()
	points := map[string]struct{}{"/": {}}
	read, calls := countingMountReader(points)
	cache := &mountTableCache{}

	var start, done sync.WaitGroup
	start.Add(1)
	for worker := 0; worker < 16; worker++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			if _, err := cache.load(read); err != nil {
				t.Errorf("load: %v", err)
			}
		}()
	}
	start.Done()
	done.Wait()

	if *calls != 1 {
		t.Fatalf("read the mount table %d times, want 1", *calls)
	}
}

// A new scope must not inherit the previous scope's snapshot: an operation that
// declares a boundary is asking for a table read before it resolves anything.
func TestMountTableScopeTakesAFreshSnapshot(t *testing.T) {
	t.Parallel()
	read, calls := countingMountReader(map[string]struct{}{"/": {}})

	first := &mountTableCache{}
	if _, err := first.load(read); err != nil {
		t.Fatalf("first scope load: %v", err)
	}
	second := &mountTableCache{}
	if _, err := second.load(read); err != nil {
		t.Fatalf("second scope load: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("read the mount table %d times across two scopes, want 2", *calls)
	}
}

func TestBeginMountTableScopeReplacesTheActiveCache(t *testing.T) {
	before := activeMountTable.Load()
	beginMountTableScope()
	after := activeMountTable.Load()
	if after == before {
		t.Fatal("beginMountTableScope kept the previous cache")
	}
	if after == nil {
		t.Fatal("beginMountTableScope left no active cache")
	}
}
