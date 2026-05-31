package sem

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProviderSnapshotEmitsContractRecords(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "auth.py", `import json

def validate_token(token):
    return bool(token)

def check_token(token):
    return validate_token(token)
`)
	writeFile(t, repo, "server.ts", `export function handleRoute() {
  return "/users/{id}"
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.SchemaVersion != "1.0" {
		t.Fatalf("schema version = %q", snapshot.Header.SchemaVersion)
	}
	if snapshot.Header.Provider != ProviderName {
		t.Fatalf("provider = %q", snapshot.Header.Provider)
	}
	if snapshot.Header.Stats.CompletenessLevel != "ok" {
		t.Fatalf("completeness = %q", snapshot.Header.Stats.CompletenessLevel)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("files = %#v", snapshot.Files)
	}

	var validate SymbolRecord
	for _, symbol := range snapshot.Symbols {
		if symbol.QualifiedName == "validate_token" {
			validate = symbol
		}
	}
	if validate.ID == "" {
		t.Fatalf("missing validate_token in %#v", snapshot.Symbols)
	}
	if validate.StableIDVersion != StableSymbolIDVersion {
		t.Fatalf("stable id version = %q", validate.StableIDVersion)
	}
	if !strings.Contains(validate.ID, "local/") || !strings.Contains(validate.ID, ":Python:auth.py:function:validate_token") {
		t.Fatalf("stable id = %q", validate.ID)
	}

	seenRelations := map[string]bool{}
	for _, relation := range snapshot.Relations {
		seenRelations[relation.Type] = true
		if relation.WarningCodes == nil {
			t.Fatalf("warning_codes should be an array in %#v", relation)
		}
	}
	for _, want := range []string{"DEFINES", "IMPORTS", "CALLS", "HANDLES_ROUTE"} {
		if !seenRelations[want] {
			t.Fatalf("missing %s in %#v", want, snapshot.Relations)
		}
	}
}

func TestWriteSnapshotNDJSON(t *testing.T) {
	snapshot := ProviderSnapshot{
		Header: SnapshotHeader{
			SchemaVersion:   SchemaVersion,
			Provider:        ProviderName,
			ProviderVersion: "test",
		},
		Files: []FileRecord{{RecordType: "file", Path: "main.go", Blob: "abc"}},
		Symbols: []SymbolRecord{{
			RecordType:      "symbol",
			ID:              "id",
			StableIDVersion: StableSymbolIDVersion,
			Kind:            "function",
			Name:            "main",
			QualifiedName:   "main",
			FilePath:        "main.go",
			Language:        "Go",
		}},
		Relations: []RelationRecord{{RecordType: "relation", FromID: "file", ToID: "id", Type: "DEFINES", WarningCodes: []string{}}},
	}

	var out bytes.Buffer
	if err := WriteSnapshotNDJSON(&out, snapshot); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d:\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("invalid json line %q: %v", line, err)
		}
	}
}

func writeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
