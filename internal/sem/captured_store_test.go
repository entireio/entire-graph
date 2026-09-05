package sem

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCapturedStoreConcurrentAcquisition(t *testing.T) {
	for _, limit := range []int64{0, 64 << 20} {
		var reads atomic.Int32
		store := newCapturedStore(t.Context(), func(path string) (string, bool) { reads.Add(1); return "package fixture\nfunc A() {}\n", true }, limit)
		defer store.close()
		var wg sync.WaitGroup
		for range 30 {
			wg.Go(func() {
				source, ok, err := store.acquire("fixture.go")
				if err != nil || !ok || source.content != "package fixture\nfunc A() {}\n" {
					t.Errorf("capture=%+v %v %v", source, ok, err)
				}
			})
		}
		wg.Wait()
		if reads.Load() != 1 {
			t.Fatalf("read %d times", reads.Load())
		}
		if store.memory > limit {
			t.Fatalf("retained %d bytes above budget %d", store.memory, limit)
		}
		directory := store.directory
		if err := store.close(); err != nil {
			t.Fatal(err)
		}
		if directory != "" {
			if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backing survived close: %v", err)
			}
		}
	}
}

func TestCapturedStoreFailureAndMutation(t *testing.T) {
	reads := 0
	live := "A"
	available := false
	store := newCapturedStore(t.Context(), func(string) (string, bool) { reads++; return live, available }, 1)
	defer store.close()
	if _, ok, err := store.acquire("missing"); ok || err != nil {
		t.Fatal("expected unavailable")
	}
	available = true
	if _, ok, _ := store.acquire("missing"); ok {
		t.Fatal("failed acquisition retried against changed input")
	}
	first, _, _ := store.acquire("source")
	live = "B"
	second, _, _ := store.acquire("source")
	live = "A"
	third, _, _ := store.acquire("source")
	if first != second || first != third || reads != 2 {
		t.Fatal("captured input changed or was reacquired")
	}
}

func TestCapturedStoreSpillIntegrityAndEqualContentPaths(t *testing.T) {
	store := newCapturedStore(t.Context(), func(string) (string, bool) { return "same", true }, 0)
	defer store.close()
	var wg sync.WaitGroup
	for _, path := range []string{"a", "b", "c"} {
		wg.Go(func() {
			if _, ok, err := store.acquire(path); err != nil || !ok {
				t.Errorf("spill: %v", err)
			}
		})
	}
	wg.Wait()
	entry := store.entries["a"]
	if err := store.root.WriteFile(entry.backing, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.acquire("a"); err == nil || ok {
		t.Fatal("corrupted backing accepted")
	}
}

func TestCapturedStoreCancellationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	release := make(chan struct{})
	store := newCapturedStore(ctx, func(string) (string, bool) { close(entered); <-release; return "source", true }, 0)
	done := make(chan error)
	go func() { _, _, err := store.acquire("a"); done <- err }()
	<-entered
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := store.close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, _, err := store.acquire("a"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed=%v", err)
	}
}
