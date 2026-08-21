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

func TestProviderRecordsCacheKeyPreservesRuleFileOrder(t *testing.T) {
	repo := t.TempDir()
	for name, contents := range map[string]string{
		".first-rule":  "target.go\n",
		".second-rule": "!target.go\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	for _, tc := range []struct {
		name     string
		inOrder  ProviderSnapshotOptions
		reversed ProviderSnapshotOptions
	}{
		{
			name:     "ignore files",
			inOrder:  ProviderSnapshotOptions{IgnoreFiles: []string{".first-rule", ".second-rule"}},
			reversed: ProviderSnapshotOptions{IgnoreFiles: []string{".second-rule", ".first-rule"}},
		},
		{
			name:     "include files",
			inOrder:  ProviderSnapshotOptions{IncludeFiles: []string{".first-rule", ".second-rule"}},
			reversed: ProviderSnapshotOptions{IncludeFiles: []string{".second-rule", ".first-rule"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inOrderKey, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", tc.inOrder)
			if err != nil {
				t.Fatalf("key in order: %v", err)
			}
			reversedKey, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", tc.reversed)
			if err != nil {
				t.Fatalf("key reversed: %v", err)
			}
			if inOrderKey == reversedKey {
				t.Fatal("reversed rule-file order produced the same records cache key")
			}
		})
	}
}

// Ignore-file order is semantic: the later rule wins. A stream cached after a
// later re-include must therefore not be served to the same files in the
// reverse, later-deny order.
func TestProviderRecordsCachePreservesIgnoreFileOrder(t *testing.T) {
	repo := t.TempDir()
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".deny-env"), []byte(".env\n"), 0o600); err != nil {
		t.Fatalf("write deny rule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".allow-env"), []byte("!.env\n"), 0o600); err != nil {
		t.Fatalf("write allow rule: %v", err)
	}

	allowEnv := ProviderSnapshotOptions{
		Profile:     ProfileFull,
		IgnoreFiles: []string{".deny-env", ".allow-env"},
	}
	denyEnv := allowEnv
	denyEnv.IgnoreFiles = []string{".allow-env", ".deny-env"}

	const (
		version = "test-v1"
		tree    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mode    = "snapshot"
	)
	allowedRecords := []byte("{\"record_type\":\"file\",\"path\":\".env\"}\n")
	if err := StoreProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, allowEnv, allowedRecords, nil); err != nil {
		t.Fatalf("store allowing-order records: %v", err)
	}

	got, _, hit, err := LoadProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, allowEnv)
	if err != nil || !hit || !bytes.Contains(got, []byte(".env")) {
		t.Fatalf("allowing order did not return its .env stream: records=%q hit=%t err=%v", got, hit, err)
	}

	got, _, hit, err = LoadProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, denyEnv)
	if err != nil {
		t.Fatalf("load denying-order records: %v", err)
	}
	if hit || bytes.Contains(got, []byte(".env")) {
		t.Fatalf("reversed later-deny order reused allowing cache entry: records=%q hit=%t", got, hit)
	}

	// A rebuilt denying-order stream remains reusable for the identical order.
	if err := StoreProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, denyEnv, nil, nil); err != nil {
		t.Fatalf("store denying-order records: %v", err)
	}
	got, _, hit, err = LoadProviderRecords(t.Context(), repo, version, tree, mode, cacheDir, denyEnv)
	if err != nil || !hit || bytes.Contains(got, []byte(".env")) {
		t.Fatalf("identical denying order did not reuse its empty stream: records=%q hit=%t err=%v", got, hit, err)
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
// The cap can be set two ways and only one of them was ever tested. Both existing
// cap tests pass MaxFiles explicitly, so they pinned the option half of the term
// while ENTIRE_GRAPH_MAX_FILES stayed out of the key — which is how a capped
// index came to answer an uncapped search despite a test named for that exact
// hole. These cannot be t.Parallel: t.Setenv forbids it, which is itself part of
// why the env path was easy to leave uncovered.
func TestSearchSnapshotKeyDiscriminatesEnvFileCap(t *testing.T) {
	repo := t.TempDir()
	uncapped, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(maxSourceFilesEnv, "5")
	capped, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if capped == uncapped {
		t.Fatalf("%s=5 and uncapped search snapshots share a key", maxSourceFilesEnv)
	}
	// And the env var must land on the same term as the option: setting one to the
	// value the other already has must NOT produce a third key space.
	explicit, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{MaxFiles: 5})
	if err != nil {
		t.Fatal(err)
	}
	if explicit != capped {
		t.Fatalf("%s=5 and MaxFiles=5 key differently; the env var must resolve onto the option's term", maxSourceFilesEnv)
	}
	// The converse direction matters just as much: LOWERING the cap must not reuse
	// the larger entry, or a caller who asked to see less is handed more of the
	// tree than it asked for. Neither direction announces itself in the answer.
	t.Setenv(maxSourceFilesEnv, "2")
	lowered, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lowered == capped {
		t.Fatalf("%s=2 reuses the %s=5 entry", maxSourceFilesEnv, maxSourceFilesEnv)
	}
}

func TestProviderRecordsKeyDiscriminatesEnvFileCap(t *testing.T) {
	repo := t.TempDir()
	uncapped, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(maxSourceFilesEnv, "5")
	capped, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if capped == uncapped {
		t.Fatalf("%s=5 and uncapped provider records share a key", maxSourceFilesEnv)
	}
	explicit, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", ProviderSnapshotOptions{MaxFiles: 5})
	if err != nil {
		t.Fatal(err)
	}
	if explicit != capped {
		t.Fatalf("%s=5 and MaxFiles=5 key differently; the env var must resolve onto the option's term", maxSourceFilesEnv)
	}
	// The converse direction matters just as much: LOWERING the cap must not reuse
	// the larger entry, or a caller who asked to see less is handed more of the
	// tree than it asked for. Neither direction announces itself in the answer.
	t.Setenv(maxSourceFilesEnv, "2")
	lowered, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lowered == capped {
		t.Fatalf("%s=2 reuses the %s=5 entry", maxSourceFilesEnv, maxSourceFilesEnv)
	}
}

func TestCacheKeysCanonicalizeEffectiveFileCapSemantics(t *testing.T) {
	repo := t.TempDir()
	keys := func(value string) (string, string) {
		t.Helper()
		t.Setenv(maxSourceFilesEnv, value)
		searchKey, err := searchSnapshotKey(repo, "id", "v", "tree", ProviderSnapshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		recordsKey, err := providerRecordsKey(repo, "id", "v", "tree", "snapshot", ProviderSnapshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return searchKey, recordsKey
	}

	defaultSearch, defaultRecords := keys("")
	invalidSearch, invalidRecords := keys("invalid")
	if invalidSearch != defaultSearch || invalidRecords != defaultRecords {
		t.Fatal("invalid cap did not canonicalize to provider default in both cache keys")
	}

	unlimitedSearch, unlimitedRecords := keys("0")
	for _, value := range []string{"-1", "-999"} {
		searchKey, recordsKey := keys(value)
		if searchKey != unlimitedSearch || recordsKey != unlimitedRecords {
			t.Fatalf("unlimited cap %q did not canonicalize in both cache keys", value)
		}
	}
}

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
