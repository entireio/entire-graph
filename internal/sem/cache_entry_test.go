package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheEntryRequiresHashDerivedRelativePath(t *testing.T) {
	validKey := strings.Repeat("a", 64)
	entry, err := newCacheEntry("cache", "search", "v1", validKey)
	if err != nil {
		t.Fatalf("valid entry: %v", err)
	}
	want := filepath.Join("search", "v1", validKey+".json.gz")
	if entry.relative != want {
		t.Fatalf("relative path = %q, want %q", entry.relative, want)
	}

	for _, test := range []struct {
		name, family, version, key string
	}{
		{name: "short key", family: "search", version: "v1", key: strings.Repeat("a", 63)},
		{name: "uppercase key", family: "search", version: "v1", key: strings.Repeat("A", 64)},
		{name: "key traversal", family: "search", version: "v1", key: "../" + strings.Repeat("a", 61)},
		{name: "family traversal", family: "..", version: "v1", key: validKey},
		{name: "version traversal", family: "search", version: "../v1", key: validKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newCacheEntry("cache", test.family, test.version, test.key); err == nil {
				t.Fatal("malicious cache entry was accepted")
			}
		})
	}
}

func TestCacheEntryReadRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	outsideDir := filepath.Join(parent, "outside")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outsideDir, "search"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "outside", "search"), filepath.Join(cacheDir, "search")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	key := strings.Repeat("b", 64)
	outsideEntry, err := newCacheEntry(outsideDir, "search", "v1", key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(outsideEntry, cachedSearchSnapshot{Tree: "outside"}); err != nil {
		t.Fatal(err)
	}
	escapingEntry, err := newCacheEntry(cacheDir, "search", "v1", key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readSearchSnapshot(escapingEntry); err == nil {
		t.Fatal("cache read followed a symlink outside the opened root")
	}
}
