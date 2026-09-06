package intent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidatesReferencesAndBindings(t *testing.T) {
	repo := t.TempDir()
	if err := Init(repo); err != nil {
		t.Fatal(err)
	}
	spec := `version: 1
id: SPEC-1
title: Authentication
requirements:
  - id: REQ-1
    description: A user receives a token.
acceptance:
  - id: ACC-1
    requirement: REQ-1
    description: A token is returned.
anchors:
  - id: ANCHOR-1
    requirement: REQ-1
tests:
  - id: TEST-1
    acceptance: ACC-1
    selector:
      name: TestToken
`
	if err := os.WriteFile(filepath.Join(repo, Root, "specs", "auth.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Specs) != 1 || set.Digest == "" {
		t.Fatalf("unexpected intent set: %+v", set)
	}
	if err := SaveBinding(repo, Binding{ID: "UNKNOWN", SymbolID: "symbol"}, false); err == nil {
		t.Fatal("accepted undeclared binding")
	}
}
