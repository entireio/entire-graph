package sem

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The on-disk caches persist SERIALIZED RECORDS, so the schema that shaped those
// bytes is part of the entry's identity. Nothing else in either envelope carries
// it: the cache-version constants track the caching machinery, and the provider
// version is the constant "dev" for every local build and "v0.0.0-ci" for every
// non-tag CI build (.github/workflows/release.yml), so two builds that disagree
// about the wire format routinely share a provider version.
//
// Reproduced before the fix by renaming one wire field (SymbolRecord.signature ->
// signature_v2) alongside a SchemaVersion bump and running the new binary against
// the old binary's cache: the warm run emitted `"signature"` and header
// schema_version "1.1" while the same binary's cold run emitted `"signature_v2"`.
// The tests below pin both caches against that.

// rewriteCachedJSON decompresses a cache artifact, applies mutate to the decoded
// object, and writes it back. It edits the persisted BYTES rather than the Go
// struct so the test models what is actually on disk after an upgrade — including
// the field simply being absent, which no in-memory fixture can express.
func rewriteCachedJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache artifact: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open cache artifact: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress cache artifact: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close cache reader: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(decoded, &object); err != nil {
		t.Fatalf("decode cache artifact: %v", err)
	}
	mutate(object)
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode cache artifact: %v", err)
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(encoded); err != nil {
		t.Fatalf("compress cache artifact: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close cache writer: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write cache artifact: %v", err)
	}
}

func TestProviderRecordsCacheMissesEntryFromAnotherSchemaVersion(t *testing.T) {
	t.Parallel()
	records := []byte(`{"record_type":"symbol","signature":"func Beta() int"}` + "\n")
	options := ProviderSnapshotOptions{Profile: ProfileFull}

	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			// A later build that bumped the wire format.
			name:   "foreign schema version",
			mutate: func(object map[string]any) { object["schema_version"] = "0.9" },
		},
		{
			// The real upgrade case: bytes written before the envelope carried the
			// field at all. These decode as "" and must not be mistaken for this
			// build's schema.
			name:   "schema version absent",
			mutate: func(object map[string]any) { delete(object, "schema_version") },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			cacheDir := t.TempDir()
			if err := storeProviderRecordsForTest(
				t.Context(), repo, "dev", "tree-1", "symbols", cacheDir, options, records, nil,
			); err != nil {
				t.Fatalf("store records: %v", err)
			}

			// Control: the entry this build wrote must serve this build, or the
			// inequality below would also pass for a cache that never hits.
			control, _, hit, err := LoadProviderRecords(
				t.Context(), repo, "dev", "tree-1", "tree-1", "symbols", cacheDir, options,
			)
			if err != nil {
				t.Fatalf("control load: %v", err)
			}
			if !hit || string(control) != string(records) {
				t.Fatalf("control: own entry must be served, got hit=%v records=%q", hit, control)
			}

			rewriteCachedJSON(t, soleCacheArtifact(t, cacheDir), testCase.mutate)

			cached, _, hit, err := LoadProviderRecords(
				t.Context(), repo, "dev", "tree-1", "tree-1", "symbols", cacheDir, options,
			)
			if err != nil {
				t.Fatalf("load after schema rewrite: %v", err)
			}
			if hit {
				t.Fatalf("records cache replayed a stream serialized under another schema: %s", cached)
			}
		})
	}
}

// The search snapshot is stored structured rather than as opaque bytes, so its
// schema travels on the snapshot header the entry already carries. Same gate,
// same reason: a snapshot decoded from another schema's JSON silently drops or
// zero-fills every field that build named differently.
func TestValidCachedSearchSnapshotRejectsAnotherSchemaVersion(t *testing.T) {
	t.Parallel()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		RepoKey:         "github.com/example/repo",
		Commit:          "commit",
		Tree:            "tree",
		Provider:        ProviderName,
		ProviderVersion: "test-version",
		Profile:         string(ProfileFull),
	}
	cache := newCachedSearchSnapshot("test-version", "commit", "tree", options, ProviderSnapshot{Header: header})
	if !validCachedSearchSnapshot(cache, header.RepoKey, "test-version", "tree", options) {
		t.Fatal("control: an entry written under this build's schema must be valid")
	}

	for _, foreign := range []string{"0.9", "2.0", ""} {
		cache.Snapshot.Header.SchemaVersion = foreign
		if validCachedSearchSnapshot(cache, header.RepoKey, "test-version", "tree", options) {
			t.Fatalf("search snapshot cache accepted an entry from schema %q", foreign)
		}
	}
}

// End-to-end for the search cache, against the persisted bytes rather than an
// in-memory fixture, so the "field is simply absent" upgrade case is covered too.
func TestSearchSnapshotCacheMissesPersistedEntryFromAnotherSchemaVersion(t *testing.T) {
	t.Parallel()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		RepoKey:         "github.com/example/repo",
		Commit:          "commit",
		Tree:            "tree",
		Provider:        ProviderName,
		ProviderVersion: "test-version",
		Profile:         string(ProfileFull),
	}
	cacheDir := t.TempDir()
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	stored := newCachedSearchSnapshot("test-version", "commit", "tree", options, ProviderSnapshot{Header: header})
	if err := writeSearchSnapshot(entry, stored); err != nil {
		t.Fatalf("write search snapshot: %v", err)
	}

	loaded, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatalf("control read: %v", err)
	}
	if !validCachedSearchSnapshot(loaded, header.RepoKey, "test-version", "tree", options) {
		t.Fatal("control: the entry this build persisted must round-trip as valid")
	}

	// Addressed by the entry's own root and relative path rather than through a
	// writePath() accessor: #160 removes that accessor and rewrites its other
	// callers to this idiom, so a new caller of it here would make package sem
	// fail to compile whichever of the two lands second.
	rewriteCachedJSON(t, filepath.Join(entry.root, entry.relative), func(object map[string]any) {
		snapshot, ok := object["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("persisted search snapshot has no snapshot object: %v", object)
		}
		snapshotHeader, ok := snapshot["Header"].(map[string]any)
		if !ok {
			t.Fatalf("persisted search snapshot has no header object: %v", snapshot)
		}
		delete(snapshotHeader, "schema_version")
	})

	rewritten, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatalf("read after schema rewrite: %v", err)
	}
	if validCachedSearchSnapshot(rewritten, header.RepoKey, "test-version", "tree", options) {
		t.Fatal("search snapshot cache accepted a persisted entry with no schema version")
	}
}

// soleCacheArtifact returns the one .json.gz beneath cacheDir, failing when the
// count is not exactly one so a test never silently rewrites the wrong file.
func soleCacheArtifact(t *testing.T, cacheDir string) string {
	t.Helper()
	var found []string
	if err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".json.gz") {
			found = append(found, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk cache dir: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one cache artifact beneath %s, found %v", cacheDir, found)
	}
	return found[0]
}
