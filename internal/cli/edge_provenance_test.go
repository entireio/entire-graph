package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestRelationProvenanceCLIFormatsAndWarmCache(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "main.go", "package sample\nfunc Caller() { Callee() }\nfunc Callee() {}\n")
	git(t, repo, "add", "main.go")
	git(t, repo, "commit", "-qm", "fixture")
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		opts := Options{Version: "provenance-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out, Stderr: io.Discard}
		args = append(args, "--repo", repo, "--cache-dir", cacheDir)
		if err := Run(t.Context(), opts, args); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	assertCalls := func(data []byte) {
		t.Helper()
		if len(bytes.TrimSpace(data)) == 0 {
			t.Fatal("expected relation output, got an empty stream")
		}
		calls := 0
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var record struct {
				RecordType string `json:"record_type"`
				Type       string `json:"type"`
				Resolution string `json:"resolution"`
				Provenance string `json:"provenance"`
			}
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatal(err)
			}
			if record.RecordType == "relation" && record.Type == "CALLS" {
				calls++
				if record.Provenance != "EXTRACTED" {
					t.Errorf("resolved CALLS has provenance %q: %s", record.Provenance, line)
				}
			}
		}
		if calls == 0 {
			t.Fatal("fixture emitted no CALLS relations")
		}
	}
	native := run("snapshot", "--format", "ndjson")
	callerID := ""
	for _, record := range decodeNativeSnapshotRecords(t, native) {
		if symbol, ok := record.(sem.SymbolRecord); ok && symbol.Name == "Caller" {
			callerID = symbol.ID
		}
	}
	if callerID == "" {
		t.Fatal("fixture did not emit Caller")
	}
	assertCalls(native)
	replay := run("snapshot", "--format", "ndjson")
	assertCalls(replay)
	if !bytes.Equal(native, replay) {
		t.Fatal("warm native cache changed public output")
	}
	compact := run("snapshot", "--format", "compact-ndjson")
	if !bytes.Equal(compact, run("snapshot", "--format", "compact-ndjson")) {
		t.Fatal("warm compact cache changed public output")
	}
	var decoded []any
	if _, err := sem.DecodeCompactSnapshot(bytes.NewReader(compact), func(record any) error { decoded = append(decoded, record); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshotPublicProjection(t, decoded), snapshotPublicProjection(t, decodeNativeSnapshotRecords(t, native))) {
		t.Fatal("compact/native public projections disagree")
	}
	artifact := filepath.Join(t.TempDir(), "snapshot.ndjson")
	if err := os.WriteFile(artifact, compact, 0600); err != nil {
		t.Fatal(err)
	}
	var query bytes.Buffer
	if err := Run(t.Context(), Options{Stdout: &query, Stderr: io.Discard}, []string{"snapshot-query", "--input", artifact, "--from", callerID, "--relation", "CALLS", "--format", "ndjson"}); err != nil {
		t.Fatal(err)
	}
	assertCalls(query.Bytes())
	edges := run("edges")
	assertCalls(edges)
	if !bytes.Equal(edges, run("edges")) {
		t.Fatal("warm edge cache changed public output")
	}
}
