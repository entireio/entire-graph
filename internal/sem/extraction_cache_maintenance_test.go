package sem

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func extractionMaintenanceSortCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	previous := extractionMaintenanceSort
	var calls atomic.Int64
	extractionMaintenanceSort = func(items []extractionMaintenanceItem) {
		calls.Add(1)
		previous(items)
	}
	t.Cleanup(func() { extractionMaintenanceSort = previous })
	return &calls
}

func writeExtractionMaintenanceEntries(t *testing.T, entry cacheEntry, count int, size int) []string {
	t.Helper()
	directory := filepath.Join(entry.root, filepath.Dir(entry.relative))
	keys := make([]string, count)
	for index := range keys {
		keys[index] = fmt.Sprintf("%064x", index+1)
		path := filepath.Join(directory, keys[index]+".json.gz")
		if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, size), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(int64(index+1), 0), time.Unix(int64(index+1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestMaintainExtractionCacheSkipsEvictionSortWhenUnderQuota(t *testing.T) {
	entry, err := newCacheEntry(t.TempDir(), "extraction-maintenance", "v1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	writeExtractionMaintenanceEntries(t, entry, 3, 10)
	calls := extractionMaintenanceSortCounter(t)

	if _, _, ok := maintainExtractionCache(entry, 1, 1, 1000, 100); !ok {
		t.Fatal("under-quota publication was rejected")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("eviction sort calls = %d, want 0 while both quota thresholds have headroom", got)
	}
}

func TestMaintainExtractionCacheSortsAndEvictsOldestWhenOverQuota(t *testing.T) {
	entry, err := newCacheEntry(t.TempDir(), "extraction-maintenance", "v1", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	keys := writeExtractionMaintenanceEntries(t, entry, 4, 10)
	calls := extractionMaintenanceSortCounter(t)

	if _, _, ok := maintainExtractionCache(entry, 1, 1, 1000, 4); !ok {
		t.Fatal("over-quota publication was rejected after eviction")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("eviction sort calls = %d, want 1", got)
	}
	directory := filepath.Join(entry.root, filepath.Dir(entry.relative))
	for index, key := range keys {
		_, statErr := os.Stat(filepath.Join(directory, key+".json.gz"))
		if index < len(keys)-1 && !os.IsNotExist(statErr) {
			t.Fatalf("old entry %s survived eviction: %v", key, statErr)
		}
		if index == len(keys)-1 && statErr != nil {
			t.Fatalf("newest entry %s was evicted: %v", key, statErr)
		}
	}
}
