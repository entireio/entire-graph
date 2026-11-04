package sem

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A focused lookup takes the gz entry's size and modification time, recorded
// in the sidecar beside the digest, as proof the entry is the one the sidecar
// mirrors, so it does not hash the entry. Any change to either brings the hash
// back, and the hash catches a changed entry.
//
// The first half shows the hash is skipped by changing the entry's bytes
// behind an unchanged stamp, which only a skipped hash can miss.
func TestFocusedLookupHashesTheEntryOnlyWhenItsStampChanged(t *testing.T) {
	fixture := indexFocusFixture(t)
	path := focusFixtureEntryPath(t, fixture)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, int(info.Size())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	loadLeafFocus(t, fixture)

	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	_, focused, _, err := LoadFocusedProviderSnapshot(t.Context(), fixture.repo, fixture.version,
		fixture.options, fixture.cacheDir, false, leafCallsFocus)
	if err != nil {
		t.Fatal(err)
	}
	if focused {
		t.Fatal("the entry's stamp changed, yet the sidecar answered without the hash that would catch its new bytes")
	}
}

// focusFixtureEntryPath is the gz entry the fixture's index wrote.
func focusFixtureEntryPath(t *testing.T, fixture focusFixture) string {
	t.Helper()
	absRepo, err := filepath.Abs(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	options, err := CaptureProviderCachePolicy(absRepo, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	_, tree, err := resolveCommittedHEAD(t.Context(), absRepo)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(absRepo, repoKey(t.Context(), absRepo), fixture.version, tree, options, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(fixture.cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(entry.root, entry.relative)
}
