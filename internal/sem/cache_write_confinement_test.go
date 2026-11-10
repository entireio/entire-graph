package sem

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The cache directory's descendants are named by this program — two constants and a SHA-256
// digest — so a symlink at one of them was planted by whatever owns those bytes. That is the
// scanned repository whenever --cache-dir or ENTIRE_PLUGIN_DATA_DIR resolves inside a checkout,
// and following it puts the artifact wherever the repository chose. Escaping links must be
// rejected on write just as entry.open rejects them on read
// (TestCacheEntryReadRejectsSymlinkEscape); writes intentionally go further and reject in-root
// aliases that reads may follow.

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

// Descendant redirects are refused even when they look operationally benign and remain inside
// the cache root. This was a working layout before the confinement boundary was made strict, so
// pin the compatibility decision explicitly instead of letting the .git-specific case imply it.
// Operators that want to relocate a cache should name the backing directory as the cache root or
// make the root itself a symlink; family and version components belong to Entire Graph.
func TestCacheEntryWriteRejectsBenignInRootFamilyAlias(t *testing.T) {
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
			backing := filepath.Join(cacheDir, "bigstore")
			if err := os.Mkdir(backing, 0o700); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, "bigstore", filepath.Join(cacheDir, test.family))

			entry, err := newCacheEntry(cacheDir, test.family, "v1", strings.Repeat("9", 64))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.write(entry); err == nil {
				t.Error("cache write followed a family alias that stays inside the cache root")
			}
			items, err := os.ReadDir(backing)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 {
				t.Errorf("cache write landed behind the in-root alias: %d entries", len(items))
			}
		})
	}
}

// The version component is a second planting site, so it needs the same strict redirect refusal
// as the family component.
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
	// The artifact holds derivative repository content, so the explicit Chmod that overrides the
	// umask has to survive the move to a confined create. Windows has no mode bits to assert:
	// Go synthesizes FileMode there from the read-only attribute alone, so a file created 0600
	// reads back 0666. Asserting it on the platforms that have it is the whole of the check.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("artifact mode = %v, want 0600", mode)
		}
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

// stubDirEntryInfo is an os.FileInfo whose only interesting field is the mode, so
// a redirecting entry can be presented to the guard on a platform that cannot
// create one. Windows directory junctions are the case in point: they need
// privileges and an mklink subprocess to make, and they are reported through a
// mode bit the guard either honours or does not.
type stubDirEntryInfo struct{ mode os.FileMode }

func (stubDirEntryInfo) Name() string        { return "cache" }
func (stubDirEntryInfo) Size() int64         { return 0 }
func (i stubDirEntryInfo) Mode() os.FileMode { return i.mode }
func (stubDirEntryInfo) ModTime() time.Time  { return time.Time{} }
func (i stubDirEntryInfo) IsDir() bool       { return i.mode.IsDir() }
func (stubDirEntryInfo) Sys() any            { return nil }

// TestCacheComponentRedirectErrorHonoursEveryRedirectingMode pins the guard to the
// platform's own notion of "this entry redirects". A ModeSymlink-only comparison
// is right on Unix and wrong on Windows, which reports a symlink as ModeSymlink
// but a directory junction or mount point as ModeIrregular — so a junction planted
// at the family or version component passed the check and was then descended into
// and followed. The ModeIrregular row therefore expects a refusal only on Windows,
// where it is also the only place it can be exercised; elsewhere it must NOT
// refuse, or an ordinary irregular entry would break writes on platforms that
// have no reparse points at all.
func TestCacheComponentRedirectErrorHonoursEveryRedirectingMode(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		mode       os.FileMode
		wantRefuse bool
	}{
		{name: "plain directory", mode: os.ModeDir | 0o700, wantRefuse: false},
		{name: "symlink", mode: os.ModeSymlink | 0o777, wantRefuse: true},
		{name: "windows reparse point", mode: os.ModeIrregular | os.ModeDir, wantRefuse: runtime.GOOS == "windows"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := cacheComponentRedirectError("family", stubDirEntryInfo{mode: testCase.mode})
			if testCase.wantRefuse && err == nil {
				t.Fatalf("mode %s was descended into; it can redirect the write on %s", testCase.mode, runtime.GOOS)
			}
			if !testCase.wantRefuse && err != nil {
				t.Fatalf("mode %s was refused on %s: %v", testCase.mode, runtime.GOOS, err)
			}
		})
	}
}

// Opening the component before checking it is what lets the write keep using the verified object.
// The injected opener deterministically models the result of a name swap by returning a handle to
// a different directory than the parent currently exposes at name. A regression to
// Lstat-before-open without a post-open identity check would accept that handle and make this
// production-path call succeed.
func TestOpenCacheComponentRejectsIdentityMismatch(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Mkdir("search", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("replacement", 0o700); err != nil {
		t.Fatal(err)
	}

	opened, err := openCacheComponent(root, "search", func(parent *os.Root, _ string) (*os.Root, error) {
		return parent.OpenRoot("replacement")
	})
	if err == nil {
		_ = opened.Close()
		t.Fatal("replacement directory matched the cache component opened by production")
	}
	if !strings.Contains(err.Error(), "changed identity while it was opened") {
		t.Fatalf("unexpected identity-mismatch error: %v", err)
	}
}

// TestCacheTempNameSuffixFormatsGeneratorWords pins the stable, separator-free
// name shape without consulting crypto/rand. The production source is
// math/rand/v2.Uint64, whose process-local generator has no entropy-error path.
func TestCacheTempNameSuffixFormatsGeneratorWords(t *testing.T) {
	t.Parallel()
	words := []uint64{0x0123456789abcdef, 0xfedcba9876543210}
	index := 0
	suffix := cacheTempNameSuffix(func() uint64 {
		word := words[index]
		index++
		return word
	})
	if want := "0123456789abcdeffedcba9876543210"; suffix != want {
		t.Fatalf("cacheTempNameSuffix = %q, want %q", suffix, want)
	}
}

// TestCreateRootTempRetriesAnOccupiedCandidate pins O_EXCL as the boundary: a
// predicted candidate is neither followed nor overwritten, and a fresh name is
// tried instead. The injected source makes the collision deterministic.
func TestCreateRootTempRetriesAnOccupiedCandidate(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	occupiedName := ".snapshot-" + strings.Repeat("0", 32) + ".json.gz"
	if err := root.WriteFile(occupiedName, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	words := []uint64{0, 0, 0, 1}
	index := 0
	file, name, err := createRootTempWithSource(root, "snapshot", func() uint64 {
		word := words[index]
		index++
		return word
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if want := ".snapshot-00000000000000000000000000000001.json.gz"; name != want {
		t.Fatalf("temporary cache file named %q, want %q after one collision", name, want)
	}
	contents, err := root.ReadFile(occupiedName)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "occupied" {
		t.Fatalf("occupied candidate was overwritten: %q", contents)
	}
}

// TestCreateRootTempReportsExhaustedCandidates pins the retry bound and its
// failure contract. Repeated prediction can deny this cache write, but O_EXCL
// must still preserve the occupied entry on every attempt.
func TestCreateRootTempReportsExhaustedCandidates(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	occupiedName := ".records-" + strings.Repeat("0", 32) + ".json.gz"
	if err := root.WriteFile(occupiedName, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	file, name, err := createRootTempWithSource(root, "records", func() uint64 {
		calls++
		return 0
	})
	if err == nil {
		if file != nil {
			_ = file.Close()
		}
		t.Fatal("sixteen occupied candidates did not fail the cache write")
	}
	if file != nil || name != "" {
		t.Fatalf("exhausted create returned file=%v name=%q", file, name)
	}
	if want := "create temporary cache file in " + root.Name() + ": no unused name"; err.Error() != want {
		t.Fatalf("exhausted create error = %q, want %q", err, want)
	}
	if calls != 32 {
		t.Fatalf("random source calls = %d, want 32 for sixteen two-word candidates", calls)
	}
	contents, readErr := root.ReadFile(occupiedName)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "occupied" {
		t.Fatalf("occupied candidate was overwritten after exhaustion: %q", contents)
	}
}

func TestCreateRootTempUsesHexadecimalRuntimeNames(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, name, err := createRootTemp(root, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\.snapshot-[0-9a-f]{32}\.json\.gz$`).MatchString(name) {
		t.Fatalf("temporary cache file named %q, want .snapshot-<32 hexadecimal>.json.gz", name)
	}
}

var _ fs.FileInfo = stubDirEntryInfo{}
