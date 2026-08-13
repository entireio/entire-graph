package sem

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderRecordsCacheHitAndMiss(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cacheDir := t.TempDir()
	const (
		version = "test-v1"
		tree    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mode    = "snapshot"
	)
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	records := []byte("{\"record_type\":\"header\"}\n{\"record_type\":\"symbol\"}\n")
	summary := &SnapshotSummary{RecordType: "summary", Stats: ProviderStats{Files: 3}}

	if err := StoreProviderRecords(context.Background(), repo, version, tree, mode, cacheDir, opts, records, summary); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, gotSummary, hit, err := LoadProviderRecords(context.Background(), repo, version, tree, mode, cacheDir, opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit after store")
	}
	if !bytes.Equal(got, records) {
		t.Fatalf("records mismatch: got %q want %q", got, records)
	}
	if gotSummary == nil || gotSummary.Stats.Files != 3 {
		t.Fatalf("summary not round-tripped: %#v", gotSummary)
	}

	// Each discriminator must independently invalidate the entry.
	cases := []struct {
		name    string
		version string
		tree    string
		mode    string
		opts    ProviderSnapshotOptions
	}{
		{"different mode", version, tree, "symbols", opts},
		{"different tree", version, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", mode, opts},
		{"different profile", version, tree, mode, ProviderSnapshotOptions{Profile: ProfileFast}},
		{"different version", "other", tree, mode, opts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, hit, err := LoadProviderRecords(context.Background(), repo, tc.version, tc.tree, tc.mode, cacheDir, tc.opts)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if hit {
				t.Fatal("expected cache miss")
			}
		})
	}
}

func TestProviderRecordsCacheLoadRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	cacheDir := filepath.Join(parent, "cache")
	outsideCacheDir := filepath.Join(parent, "outside")
	for _, dir := range []string{repo, cacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const (
		version = "test-v1"
		tree    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mode    = "snapshot"
	)
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	if err := StoreProviderRecords(t.Context(), repo, version, tree, mode, outsideCacheDir, opts, []byte("outside\n"), nil); err != nil {
		t.Fatalf("seed records cache: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside", "records"), filepath.Join(cacheDir, "records")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, hit, err := LoadProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, opts); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("provider-records loader followed a symlink outside the opened root")
	}
}

func TestProviderRecordsCacheWorktreeBypassed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cacheDir := t.TempDir()
	opts := ProviderSnapshotOptions{Profile: ProfileFull, Worktree: true}
	records := []byte("x")

	// Store is a no-op under --worktree, and Load never reports a hit.
	if err := StoreProviderRecords(context.Background(), repo, "v", "tree", "snapshot", cacheDir, opts, records, nil); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, _, hit, err := LoadProviderRecords(context.Background(), repo, "v", "tree", "snapshot", cacheDir, opts); err != nil || hit {
		t.Fatalf("worktree load: hit=%v err=%v", hit, err)
	}
}

func TestCompactAndNativeRecordCachesDoNotCollide(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	cacheDir := t.TempDir()
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	const (
		version     = "test-v1"
		tree        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		nativeMode  = "snapshot"
		compactMode = "snapshot:compact-ndjson-v1"
	)
	if err := StoreProviderRecords(context.Background(), repo, version, tree, nativeMode, cacheDir, opts, []byte("native\n"), nil); err != nil {
		t.Fatal(err)
	}
	if err := StoreProviderRecords(context.Background(), repo, version, tree, compactMode, cacheDir, opts, []byte("compact\n"), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, want string }{{nativeMode, "native\n"}, {compactMode, "compact\n"}} {
		got, _, hit, err := LoadProviderRecords(context.Background(), repo, version, tree, tc.mode, cacheDir, opts)
		if err != nil || !hit || string(got) != tc.want {
			t.Fatalf("load %s = %q hit=%t err=%v", tc.mode, got, hit, err)
		}
	}
}

func TestProviderRecordsCacheKeyIncludesIgnoreFileContent(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	ignore := filepath.Join(repo, "ignore.txt")
	if err := os.WriteFile(ignore, []byte("first"), 0o600); err != nil {
		t.Fatalf("write ignore: %v", err)
	}
	opts := ProviderSnapshotOptions{Profile: ProfileFull, IgnoreFiles: []string{ignore}}

	before, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatalf("key before: %v", err)
	}
	if err := os.WriteFile(ignore, []byte("second"), 0o600); err != nil {
		t.Fatalf("rewrite ignore: %v", err)
	}
	after, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatalf("key after: %v", err)
	}
	if before == after {
		t.Fatal("expected key to change when ignore-file content changes")
	}
}

// A capped build must never answer an uncapped caller: the cap SHAPES the graph, so a snapshot
// built under one is missing everything past it. Measured on this repository before the fix, a
// cap-5 build wrote 28 symbols and the next uncapped search was served those 28 rather than
// rebuilding to 5740 — 99.5% of the graph silently absent, and one capped ingest poisoned every
// later query on the same tree.
func TestProviderRecordsKeyDiscriminatesFileCap(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	capped := ProviderSnapshotOptions{MaxFiles: 5}
	uncapped := ProviderSnapshotOptions{}
	a, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", capped)
	if err != nil {
		t.Fatal(err)
	}
	b, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", uncapped)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("capped and uncapped runs share a records cache key: a truncated graph would be served to an uncapped caller")
	}
}

// Repo identity prefixes every symbol ID this cache stores, so serving one repository's records to
// another hands back IDs attributed to the wrong project. searchSnapshotKey already folded identity
// in; this key did not.
func TestProviderRecordsKeyDiscriminatesRepoIdentity(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	opts := ProviderSnapshotOptions{}
	a, err := providerRecordsKey(repo, "gh/ownerone/probe", "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := providerRecordsKey(repo, "gh/ownertwo/probe", "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("records stored under gh/ownerone/probe would be served to gh/ownertwo/probe")
	}
}

// The search snapshot cache had the same cap hole; identity was already keyed upstream.
func TestSearchSnapshotKeyDiscriminatesFileCap(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	a, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{MaxFiles: 5})
	if err != nil {
		t.Fatal(err)
	}
	b, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("capped and uncapped search snapshots share a key")
	}
}
