package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestSnapshotQueryCommandLoadsCompactArtifact(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package sample\n\nfunc caller() { callee() }\nfunc callee() {}\n")
	var compact bytes.Buffer
	if err := Run(t.Context(), Options{Version: "compact-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &compact, Stderr: io.Discard}, []string{"snapshot", "--repo", repo, "--worktree", "--format", "compact-ndjson"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "graph.compact.ndjson")
	if err := os.WriteFile(path, compact.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := sem.LoadCompactSnapshot(bytes.NewReader(compact.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Snapshot.Symbols) == 0 || len(index.Snapshot.Relations) == 0 {
		t.Fatalf("fixture did not produce symbols and relations")
	}
	want := index.Query(sem.CompactSnapshotQuery{Symbol: index.Snapshot.Symbols[0].ID, FromID: index.Snapshot.Relations[0].FromID, Relation: index.Snapshot.Relations[0].Type})
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Stdout: &out}, []string{"snapshot-query", "--input", path, "--symbol", index.Snapshot.Symbols[0].ID, "--from", index.Snapshot.Relations[0].FromID, "--relation", index.Snapshot.Relations[0].Type, "--format", "ndjson"}); err != nil {
		t.Fatal(err)
	}
	got := decodeNativeSnapshotRecords(t, out.Bytes())
	wantRecords := make([]any, 0, len(want.Symbols)+len(want.Relations))
	for _, symbol := range want.Symbols {
		wantRecords = append(wantRecords, symbol)
	}
	for _, relation := range want.Relations {
		wantRecords = append(wantRecords, relation)
	}
	if got, want := snapshotPublicProjection(t, got), snapshotPublicProjection(t, wantRecords); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot-query output differs\n got=%s\nwant=%s", got, want)
	}
}

func TestSnapshotQueryRejectsInvalidFilterCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"snapshot-query", "--input", "unused.compact.ndjson"},
		{"snapshot-query", "--input", "unused.compact.ndjson", "--relation", "CALLS"},
		{"snapshot-query", "--input", "unused.compact.ndjson", "--symbol", "name", "--format", "json"},
	} {
		if err := Run(t.Context(), Options{Stdout: io.Discard}, args); err == nil {
			t.Fatalf("expected validation error for %v", args)
		}
	}
}
