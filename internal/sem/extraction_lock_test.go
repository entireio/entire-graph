package sem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractionQuotaIndependentOperations(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", "4")
	t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES", "20000")
	directory := t.TempDir()
	caches := []*extractionCache{
		{directory: directory, repository: "quota", build: "test"},
		{directory: directory, repository: "quota", build: "test"},
	}
	// Interleave operations after each has observed free capacity. A stale
	// operation-local reservation used to allow the sixth write past the quota.
	for i, owner := range []int{0, 1, 1, 1, 0, 0, 1, 0} {
		source := captureSource(fmt.Sprintf("f%d.go", i), "package p\nfunc A(){}\n")
		language, _ := languageForPath(source.path)
		caches[owner].extract(resolveProfile(ProfileFull), language, source, 4096)
		entry, _, _ := caches[owner].entry(resolveProfile(ProfileFull), language, source, 4096)
		assertExtractionDiskQuota(t, entry, 4, 20000)
	}
}

func assertExtractionDiskQuota(t *testing.T, entry cacheEntry, maxEntries int, maxBytes int64) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(entry.root, filepath.Dir(entry.relative)))
	if err != nil {
		t.Fatal(err)
	}
	count, size := 0, int64(0)
	for _, item := range entries {
		if !strings.HasSuffix(item.Name(), ".json.gz") {
			continue
		}
		info, err := item.Info()
		if err != nil {
			t.Fatal(err)
		}
		count++
		size += info.Size()
	}
	if count > maxEntries || size > maxBytes {
		t.Fatalf("quota exceeded: entries=%d bytes=%d", count, size)
	}
}

func TestExtractionAdmissionContentionAndNoFollow(t *testing.T) {
	entry, _ := newCacheEntry(t.TempDir(), "extraction-test", "v1", strings.Repeat("a", 64))
	lock, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockExtractionAdmission(entry); err == nil {
		other.Close()
		t.Error("two live writers admitted")
	}
	lock.Close()
	lock, err = lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal("closed writer retained lock", err)
	}
	lock.Close()
	leaf := filepath.Join(entry.root, filepath.Dir(entry.relative), ".admission.lock")
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	if lock, err := lockExtractionAdmission(entry); err == nil {
		lock.Close()
		t.Fatal("redirected lock admitted")
	}
	bytes, _ := os.ReadFile(target)
	if string(bytes) != "unchanged" {
		t.Fatal("lock modified target")
	}
}

func TestExtractionQuotaSubprocesses(t *testing.T) {
	if directory := os.Getenv("ENTIRE_GRAPH_TEST_QUOTA_CHILD"); directory != "" {
		cache := &extractionCache{directory: directory, repository: "subprocess-quota", build: "test"}
		language, _ := languageForPath("a.go")
		for i := 0; i < 12; i++ {
			source := captureSource(fmt.Sprintf("f%d-%d.go", os.Getpid(), i), "package p\nfunc A(){}\n")
			cache.extract(resolveProfile(ProfileFull), language, source, 4096)
		}
		return
	}
	directory := t.TempDir()
	var children []*exec.Cmd
	for i := 0; i < 3; i++ {
		child := exec.Command(os.Args[0], "-test.run=^TestExtractionQuotaSubprocesses$", "-test.count=1")
		child.Env = append(os.Environ(), "ENTIRE_GRAPH_TEST_QUOTA_CHILD="+directory, "ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES=4", "ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES=20000")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
	}
	for _, child := range children {
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	cache := &extractionCache{directory: directory, repository: "subprocess-quota", build: "test"}
	language, _ := languageForPath("a.go")
	entry, _, _ := cache.entry(resolveProfile(ProfileFull), language, captureSource("a.go", ""), 4096)
	assertExtractionDiskQuota(t, entry, 4, 20000)
}

func TestExtractionAdmissionRemovesOnlyOwnedOrphanTemps(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "orphans", build: "test"}
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nfunc A(){}\n")
	entry, _, _ := cache.entry(resolveProfile(ProfileFull), language, source, 4096)
	lock, err := lockExtractionAdmission(entry)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	directory := filepath.Join(entry.root, filepath.Dir(entry.relative))
	orphan := ".extract-" + strings.Repeat("a", 32) + ".json.gz"
	unrelated := ".extract-not-owned.json.gz"
	for _, name := range []string{orphan, unrelated} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("crashed writer"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache.extract(resolveProfile(ProfileFull), language, source, 4096)
	if _, err := os.Stat(filepath.Join(directory, orphan)); !os.IsNotExist(err) {
		t.Fatal("orphan temp survived maintenance")
	}
	if _, err := os.Stat(filepath.Join(directory, unrelated)); err != nil {
		t.Fatal("unowned file removed", err)
	}
}
