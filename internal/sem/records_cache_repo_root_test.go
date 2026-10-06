package sem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// encodeRecordStreamForTest encodes records exactly as the CLI's record encoder
// does (json.Encoder, no HTML escaping), which is the stream the records cache
// stores and restamps.
func encodeRecordStreamForTest(t *testing.T, records ...any) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

// TestRestampRepoRootWhenPathSpellsTheFieldName pins that a checkout path which
// itself contains the field's literal spelling, `"repo_root":`, still restamps.
// The path is a JSON string value, so every quote in it is encoded as \", and
// the located field (`"repo_root":` + the encoded root) can match only the real
// key: there is exactly one occurrence no matter what the path spells.
func TestRestampRepoRootWhenPathSpellsTheFieldName(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{
		{"field name in path", `/src/a"repo_root":b/repo`, `/src/c"repo_root":d/repo`},
		{"field name and value in path", `/src/"repo_root":"/src/x/repo`, `/src/"repo_root":"/src/y/repo`},
		{"recorded root repeated in another field", `/src/repo_root/one`, `/src/repo_root/two`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := SnapshotHeader{RepoRoot: tc.from, RepoKey: `"repo_root":` + tc.from, Tree: "t"}
			symbol := map[string]string{"record_type": "symbol", "path": "a.go"}
			records := encodeRecordStreamForTest(t, header, symbol)

			restamped, ok := restampProviderRecordsRepoRoot(records, tc.from, tc.to)
			if !ok {
				t.Fatalf("restamp missed for root %q:\n%s", tc.from, records)
			}
			header.RepoRoot = tc.to
			if want := encodeRecordStreamForTest(t, header, symbol); !bytes.Equal(restamped, want) {
				t.Fatalf("restamped stream:\n%s\nwant:\n%s", restamped, want)
			}
		})
	}
}

// TestProviderRecordsCacheReplaysAcrossCheckoutsNamedLikeTheField is the same
// claim end to end on a real filesystem: two worktrees of one repository whose
// directory names contain `"repo_root":` share a records entry, and the second
// is served the first one's stream restamped with its own root.
func TestProviderRecordsCacheReplaysAcrossCheckoutsNamedLikeTheField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`Windows forbids '"' and ':' in file names`)
	}
	parent := t.TempDir()
	first := filepath.Join(parent, `a"repo_root":"x`)
	second := filepath.Join(parent, `b"repo_root":"y`)
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Skipf("filesystem refuses the name: %v", err)
	}
	git(t, first, "init")
	git(t, first, "config", "user.name", "Entire Graph Test")
	git(t, first, "config", "user.email", "graph@example.com")
	// A remote gives both checkouts one repository identity; without one the
	// identity is the directory basename and the entries would not be shared.
	git(t, first, "remote", "add", "origin", "https://github.com/example/project.git")
	write(t, first, "a.go", "package a\n\nfunc A() {}\n")
	git(t, first, "add", ".")
	git(t, first, "commit", "-m", "init")
	git(t, first, "worktree", "add", "--detach", second)

	const (
		version = "test-v1"
		mode    = "snapshot"
	)
	ctx := t.Context()
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	commit, tree, err := resolveCommittedHEAD(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := BeginProviderRecordsCache(ctx, first, version, commit, tree, mode, cacheDir, options)
	if err != nil {
		t.Fatal(err)
	}
	header := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: version,
		RepoKey:         transaction.repositoryKey,
		RepoRoot:        transaction.absRepo,
		Commit:          commit,
		Tree:            tree,
		Profile:         string(transaction.options.Profile),
	}
	symbol := map[string]string{"record_type": "symbol", "path": "a.go"}
	if err := transaction.Store(encodeRecordStreamForTest(t, header, symbol), nil, header); err != nil {
		t.Fatal(err)
	}

	replay, err := BeginProviderRecordsCache(ctx, second, version, commit, tree, mode, cacheDir, options)
	if err != nil {
		t.Fatal(err)
	}
	if replay.absRepo == transaction.absRepo {
		t.Fatalf("premise lost: both checkouts resolved to %q", replay.absRepo)
	}
	records, _, hit := replay.Load()
	if !hit {
		t.Fatalf("a checkout named %q missed the entry its sibling %q stored", replay.absRepo, transaction.absRepo)
	}
	header.RepoRoot = replay.absRepo
	if want := encodeRecordStreamForTest(t, header, symbol); !bytes.Equal(records, want) {
		t.Fatalf("replayed stream:\n%s\nwant:\n%s", records, want)
	}
}
