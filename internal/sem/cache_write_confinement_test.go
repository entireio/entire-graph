package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cache directory's descendants are named by this program — two constants and a SHA-256
// digest — so a symlink at one of them was planted by whatever owns those bytes. That is the
// scanned repository whenever --cache-dir or ENTIRE_PLUGIN_DATA_DIR resolves inside a checkout,
// and following it puts the artifact wherever the repository chose. The write boundary must be
// the one entry.open already reads through (TestCacheEntryReadRejectsSymlinkEscape).

func TestCacheEntryWriteRejectsSymlinkedFamilyEscape(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		family string
		write  func(cacheEntry) error
	}{
		{
			name:   "search snapshot",
			family: "search",
			write: func(entry cacheEntry) error {
				return writeSearchSnapshot(entry, cachedSearchSnapshot{Tree: "planted"})
			},
		},
		{
			name:   "provider records",
			family: "records",
			write: func(entry cacheEntry) error {
				return writeProviderRecords(entry, cachedProviderRecords{Tree: "planted"})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			cacheDir := filepath.Join(parent, "cache")
			outsideDir := filepath.Join(parent, "outside")
			if err := os.MkdirAll(cacheDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outsideDir, 0o700); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, filepath.Join("..", "outside"), filepath.Join(cacheDir, test.family))

			entry, err := newCacheEntry(cacheDir, test.family, "v1", strings.Repeat("c", 64))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.write(entry); err == nil {
				t.Error("cache write followed a symlink out of the opened root")
			}

			escaped, err := os.ReadDir(outsideDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(escaped) != 0 {
				names := make([]string, 0, len(escaped))
				for _, item := range escaped {
					names = append(names, item.Name())
				}
				t.Errorf("cache write escaped the cache directory: %v", names)
			}
			// The symlink must survive as a symlink: replacing it would be a second way to
			// silently change where the next run writes.
			info, err := os.Lstat(filepath.Join(cacheDir, test.family))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("planted symlink was replaced with %v", info.Mode())
			}
		})
	}
}

// os.Root stops a link LEAVING the cache directory and follows one that stays inside it. With the
// cache directory at a checkout root — `--cache-dir .`, or an ENTIRE_PLUGIN_DATA_DIR under the
// repository — `.git` is inside it, so the internal link is the one that matters most.
func TestCacheEntryWriteRejectsSymlinkedFamilyInsideRoot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		family string
		write  func(cacheEntry) error
	}{
		{
			name:   "search snapshot",
			family: "search",
			write: func(entry cacheEntry) error {
				return writeSearchSnapshot(entry, cachedSearchSnapshot{Tree: "planted"})
			},
		},
		{
			name:   "provider records",
			family: "records",
			write: func(entry cacheEntry) error {
				return writeProviderRecords(entry, cachedProviderRecords{Tree: "planted"})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cacheDir := t.TempDir()
			gitDir := filepath.Join(cacheDir, ".git")
			if err := os.MkdirAll(gitDir, 0o700); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, ".git", filepath.Join(cacheDir, test.family))

			entry, err := newCacheEntry(cacheDir, test.family, "v1", strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.write(entry); err == nil {
				t.Error("cache write followed a symlink that stays inside the opened root")
			}
			planted, err := os.ReadDir(gitDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(planted) != 0 {
				names := make([]string, 0, len(planted))
				for _, item := range planted {
					names = append(names, item.Name())
				}
				t.Errorf("cache write landed in the repository directory: %v", names)
			}
		})
	}
}

// The version component is a second planting site, and on a case-insensitive filesystem a link
// spelled differently still answers the lookup, so the refusal must not be an exact-case compare.
func TestCacheEntryWriteRejectsSymlinkedVersion(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	outsideDir := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(cacheDir, "search"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join("..", "..", "outside"), filepath.Join(cacheDir, "search", "v1"))

	entry, err := newCacheEntry(cacheDir, "search", "v1", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(entry, cachedSearchSnapshot{Tree: "planted"}); err == nil {
		t.Error("cache write followed a symlinked version component")
	}
	escaped, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(escaped) != 0 {
		t.Errorf("cache write escaped through the version component: %d entries", len(escaped))
	}
}

// A symlinked cache ROOT is the supported deployment — the cache on another volume, in a shared
// location, outside a container's writable layer — and it is the caller's own path, resolved by
// OpenRoot on both sides. It must keep working.
func TestCacheEntryWriteFollowsSymlinkedCacheRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	cacheDir := filepath.Join(parent, "link")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, realDir, cacheDir)

	entry, err := newCacheEntry(cacheDir, "search", "v1", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(entry, cachedSearchSnapshot{Tree: "linked-root"}); err != nil {
		t.Fatalf("write through a symlinked cache root: %v", err)
	}
	cached, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatalf("read back through a symlinked cache root: %v", err)
	}
	if cached.Tree != "linked-root" {
		t.Errorf("round-tripped tree = %q, want %q", cached.Tree, "linked-root")
	}
	artifact := filepath.Join(realDir, "search", "v1", strings.Repeat("d", 64)+".json.gz")
	info, err := os.Lstat(artifact)
	if err != nil {
		t.Fatalf("artifact beneath the link target: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("artifact mode = %v, want 0600", mode)
	}
}

// The artifact name itself is the one component an attacker can predict without planting a
// directory, so record that the install is a rename over it rather than a write through it.
func TestCacheEntryWriteReplacesSymlinkedArtifact(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	key := strings.Repeat("e", 64)
	if err := os.MkdirAll(filepath.Join(cacheDir, "search", "v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(parent, "victim.txt")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, victim, filepath.Join(cacheDir, "search", "v1", key+".json.gz"))

	entry, err := newCacheEntry(cacheDir, "search", "v1", key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(entry, cachedSearchSnapshot{Tree: "replaced"}); err != nil {
		t.Fatalf("write over a symlinked artifact name: %v", err)
	}
	contents, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "untouched" {
		t.Errorf("symlinked artifact name was written through: victim = %q", contents)
	}
}

// A failed write must not leave its temporary behind, and a successful one must leave exactly the
// artifact: the temporary is created inside the destination directory, so a leak there is
// indistinguishable from a cache entry to any cleanup that globs the directory.
func TestCacheEntryWriteLeavesOnlyTheArtifact(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	entry, err := newCacheEntry(cacheDir, "records", "v1", strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProviderRecords(entry, cachedProviderRecords{Tree: "only"}); err != nil {
		t.Fatal(err)
	}
	items, err := os.ReadDir(filepath.Join(cacheDir, "records", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name() != strings.Repeat("f", 64)+".json.gz" {
		names := make([]string, 0, len(items))
		for _, item := range items {
			names = append(names, item.Name())
		}
		t.Errorf("cache directory holds %v, want only the artifact", names)
	}
}
