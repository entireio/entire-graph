package sem

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A focused lookup takes the gz entry's size and modification time, recorded
// in the sidecar beside the digest, as proof the entry is the one the sidecar
// mirrors. Any change to either brings the hash back, and the hash catches a
// changed entry. Break caught: an entry rewritten after its sidecar was built
// is answered from the sidecar as though it were unchanged.
func TestFocusedLookupHashesAnEntryWhoseStampChanged(t *testing.T) {
	fixture := indexFocusFixture(t)
	rewriteEntryChangingItsStamp(t, focusFixtureEntryPath(t, fixture))
	_, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false, leafCallsFocus)
	if err != nil {
		t.Fatal(err)
	}
	if focused {
		t.Fatal("the entry's stamp changed, yet the sidecar answered without the hash that would catch its new bytes")
	}
}

// The warm full load (query --head, and a lookup's fallback) checks the
// sidecar against the entry before answering from it. Break caught: the full
// read serving a rewritten entry's sidecar as a cache hit, by skipping that
// check or trusting a recorded stamp or digest the entry no longer has.
func TestFullLoadNeverServesTheSidecarOfAChangedEntry(t *testing.T) {
	fixture := indexFocusFixture(t)
	rewriteEntryChangingItsStamp(t, focusFixtureEntryPath(t, fixture))
	_, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("the entry's stamp changed, yet the sidecar answered as a cache hit without the hash that would catch its new bytes")
	}
}

// rewriteEntryChangingItsStamp replaces the entry's bytes with as many bytes of
// garbage and moves its modification time a second on, so its stamp no longer
// matches the one its sidecar recorded.
func rewriteEntryChangingItsStamp(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, int(info.Size())), 0o600); err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

// focusFixtureEntryPath is the gz entry the fixture's index wrote: the one
// *.json.gz file under its cache directory.
func focusFixtureEntryPath(t *testing.T, fixture focusFixture) string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(fixture.cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json.gz") {
			entries = append(entries, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one complete entry, got %q", entries)
	}
	return entries[0]
}
