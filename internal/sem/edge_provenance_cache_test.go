package sem

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRelationProvenanceCacheUpgradeAndReplay(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	const version, commit, tree = "same-provider", "same-commit", "same-tree"
	options := ProviderSnapshotOptions{}
	transaction, err := BeginProviderRecordsCache(t.Context(), repo, version, commit, tree, "snapshot", cacheDir, options)
	if err != nil {
		t.Fatal(err)
	}
	// A same-source, same-provider 1.3 entry cannot serve pre-provenance bytes.
	oldKey, err := providerRecordsKeyForSchema("1.3", repo, transaction.repositoryKey, version, commit, tree, "snapshot", transaction.options)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := providerRecordsKey(repo, transaction.repositoryKey, version, commit, tree, "snapshot", transaction.options)
	if err != nil {
		t.Fatal(err)
	}
	if oldKey == newKey {
		t.Fatal("raw-record cache can replay pre-provenance output")
	}
	oldSearch, err := searchSnapshotKeyForSchema("1.3", repo, transaction.repositoryKey, version, tree, options)
	if err != nil {
		t.Fatal(err)
	}
	newSearch, err := searchSnapshotKey(repo, transaction.repositoryKey, version, tree, options)
	if err != nil {
		t.Fatal(err)
	}
	if oldSearch == newSearch {
		t.Fatal("search cache can reuse a header without relation_provenance")
	}
	relation := RelationRecord{RecordType: "relation", Type: "CALLS", Resolution: "package", WarningCodes: []string{}}
	var stream bytes.Buffer
	if err := json.NewEncoder(&stream).Encode(relation); err != nil {
		t.Fatal(err)
	}
	header := SnapshotHeader{SchemaVersion: SchemaVersion, Provider: ProviderName, ProviderVersion: version, RepoKey: transaction.repositoryKey, Commit: commit, Tree: tree, Profile: string(transaction.options.Profile)}
	spool, err := spoolRecords(transaction, stream.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Store(spool, nil, header); err != nil {
		t.Fatal(err)
	}
	replay, _, hit := transaction.Load()
	if !hit {
		t.Fatal("new record stream was not cached")
	}
	if !bytes.Equal(replay, stream.Bytes()) || !bytes.Contains(replay, []byte(`"provenance":"EXTRACTED"`)) {
		t.Fatalf("cache replay lost provenance: %s", replay)
	}
}
