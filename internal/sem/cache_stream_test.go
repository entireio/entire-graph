package sem

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// cacheEntry.write used json.Encoder, which marshals the whole value into one buffer before
// writing. Persisting a large snapshot therefore held the snapshot plus its entire uncompressed
// encoding. The write must stream: allocation during a write has to stay far below the size of
// the encoding.
func TestCacheEntryWriteStreamsEncoding(t *testing.T) {
	// ~48 MiB of JSON, in values that need no per-element allocation to encode.
	value := make([]string, 48<<10)
	line := strings.Repeat("x", 1<<10)
	for index := range value {
		value[index] = line
	}
	encodedSize := uint64(len(value) * (len(line) + 3))

	entry := testCacheEntry(t)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := entry.write("stream", value); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	// Only a jsonv2 toolchain has a streaming marshaler; the !goexperiment.jsonv2 fallback buffers
	// by design, so the bound applies there alone. The decode check below runs on both.
	if cacheEncodeStreams && allocated > encodedSize/4 {
		t.Fatalf("cache write allocated %d bytes for a %d-byte encoding; it buffers instead of streaming", allocated, encodedSize)
	}

	file, err := entry.open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(value) || decoded[0] != line || decoded[len(decoded)-1] != line {
		t.Fatal("streamed cache artifact does not decode to the value written")
	}
}

// The streamed encoding must be byte-identical to what json.Encoder with SetEscapeHTML(false)
// produced, so artifacts written by either build are the same bytes and readers see no change.
func TestEncodeCacheValueMatchesV1Encoder(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n\n// Validate checks <tokens> & \"quotes\"   é.\nfunc ValidateToken(raw string) (bool, error) { return raw != \"\", nil }\n\ntype Session struct{ ID string }\n\nfunc (s *Session) Refresh() error { _, err := ValidateToken(s.ID); return err }\n")
	write(t, repo, "web.ts", "export function handler(req: Request): Response { return fetch(\"/api/<x>&y\") as any }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	values := []any{
		cachedSearchSnapshot{},
		cacheGenerationMarker{Generation: "abc<&>"},
		map[string]any{"html": "<script>&amp;</script>", "sep": "  ", "bad": "\xff", "n": 1.5e300, "nil": nil},
	}
	for _, profile := range []Profile{ProfileFull, ProfileFast, ProfileSyntaxOnly} {
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, newCachedSearchSnapshot("test-version", snapshot.Header.Commit, snapshot.Header.Tree, ProviderSnapshotOptions{Profile: profile}, snapshot))
	}
	for index, value := range values {
		var legacy bytes.Buffer
		encoder := json.NewEncoder(&legacy)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		var streamed bytes.Buffer
		if err := encodeCacheValue(&streamed, value); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(legacy.Bytes(), streamed.Bytes()) {
			t.Fatalf("value %d: streamed encoding differs from json.Encoder\nlegacy=%q\nstreamed=%q", index, legacy.String(), streamed.String())
		}
	}
}

func TestCacheEntryWriteDigestMatchesStoredBytes(t *testing.T) {
	entry := testCacheEntry(t)
	written, err := entry.writeDigest("digest", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := entry.digest()
	if err != nil {
		t.Fatal(err)
	}
	if written != stored {
		t.Fatal("digest of the stored artifact differs from the digest of the bytes written")
	}
	// Any change to the stored bytes must change the digest.
	path := filepath.Join(entry.root, entry.relative)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content[:len(content)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if truncated, err := entry.digest(); err != nil || truncated == written {
		t.Fatalf("truncated artifact digest = %x, %v; want a different digest", truncated, err)
	}
}

// The digest re-read uses the same confined open as every reader, so an entry reachable only
// through a symlink out of the cache directory cannot verify.
func TestCacheEntryDigestRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside, err := newCacheEntry(filepath.Join(parent, "outside"), "search", "v1", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := outside.write("seed", "outside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "outside", "search"), filepath.Join(cacheDir, "search")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inside, err := newCacheEntry(cacheDir, "search", "v1", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outside.digest(); err != nil {
		t.Fatalf("control: the outside entry itself must be readable: %v", err)
	}
	if _, err := inside.digest(); err == nil {
		t.Fatal("digest read followed a symlink out of the cache directory")
	}
}

func preindexTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\nfunc ValidateToken() bool { return true }\nfunc Login() bool { return ValidateToken() }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// A preindex entry that does not hold exactly the bytes written (truncated or corrupted after
// the rename, or unreadable) must fail the explicit index command, which promises a durable
// artifact. Both the persist and its one retry see the damage here.
func TestPreindexRejectsCorruptedPersistedEntry(t *testing.T) {
	for _, damage := range []struct {
		name  string
		apply func(t *testing.T, path string)
	}{
		{"truncated", func(t *testing.T, path string) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content[:len(content)/2], 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupted", func(t *testing.T, path string) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content[len(content)/2] ^= 0xff
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(damage.name, func(t *testing.T) {
			repo := preindexTestRepo(t)
			cacheDir := t.TempDir()
			verifications := 0
			_, _, err := preindexProviderSnapshotWithPersistenceVerifier(
				t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFast}, cacheDir,
				func(entry cacheEntry) (cacheDigest, error) {
					verifications++
					damage.apply(t, filepath.Join(entry.root, entry.relative))
					return entry.digest()
				},
			)
			if err == nil {
				t.Fatal("preindex accepted a damaged persisted entry")
			}
			if verifications != 2 {
				t.Fatalf("verifications = %d, want the persist and one retry", verifications)
			}
		})
	}
}

// A mismatch on the first verification only (a concurrent writer replaced the entry) is retried,
// and the retry's intact entry is what later queries load.
func TestPreindexRetriesOneMismatchedPersist(t *testing.T) {
	repo := preindexTestRepo(t)
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFast}
	verifications := 0
	built, hit, err := preindexProviderSnapshotWithPersistenceVerifier(
		t.Context(), repo, "test-version", options, cacheDir,
		func(entry cacheEntry) (cacheDigest, error) {
			verifications++
			if verifications == 1 {
				return cacheDigest{}, errors.New("simulated unreadable entry")
			}
			return entry.digest()
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if hit || verifications != 2 {
		t.Fatalf("hit=%t verifications=%d; want a miss verified twice", hit, verifications)
	}
	loaded, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", options, cacheDir)
	if err != nil || !hit {
		t.Fatalf("persisted preindex not served: hit=%t err=%v", hit, err)
	}
	if !reflect.DeepEqual(loaded, built) {
		t.Fatal("served preindex differs from the snapshot preindex returned")
	}
}

// Equivalence: the persisted artifact is the same bytes the previous persist path produced
// (json.Encoder of the envelope), and it decodes to the snapshot preindex returned.
func TestPreindexPersistedEntryEquivalentToLegacyEncoding(t *testing.T) {
	repo := preindexTestRepo(t)
	for _, profile := range []Profile{ProfileFull, ProfileFast, ProfileSyntaxOnly} {
		cacheDir := t.TempDir()
		var persisted cacheEntry
		snapshot, _, err := preindexProviderSnapshotWithPersistenceVerifier(
			t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: profile}, cacheDir,
			func(entry cacheEntry) (cacheDigest, error) {
				persisted = entry
				return entry.digest()
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		cached, err := readSearchSnapshot(persisted)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cached.Snapshot, snapshot) {
			t.Fatalf("%s: persisted snapshot does not decode to the returned snapshot", profile)
		}
		file, err := persisted.open()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := io.ReadAll(reader)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		var legacy bytes.Buffer
		encoder := json.NewEncoder(&legacy)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(newCachedSearchSnapshot("test-version", snapshot.Header.Commit, snapshot.Header.Tree, ProviderSnapshotOptions{Profile: profile}, snapshot)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, legacy.Bytes()) {
			t.Fatalf("%s: persisted JSON differs from the legacy json.Encoder encoding", profile)
		}
	}
}
