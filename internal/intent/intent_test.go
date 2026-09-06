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

func TestLoadRejectsYAMLAliases(t *testing.T) {
	repo := t.TempDir()
	if err := Init(repo); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Authentication\nrequirements:\n  - &requirement\n    id: REQ-1\n    description: A user authenticates.\n  - *requirement\n"
	if err := os.WriteFile(filepath.Join(repo, Root, "specs", "auth.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(repo); err == nil {
		t.Fatal("accepted YAML alias")
	}
}

func TestLoadVerificationPolicyValidatesAndDigestsCanonicalScopes(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, Root, VerificationPolicyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "version: 1\nscopes:\n  - id: unit\n    command: go test ./internal/intent\n  - id: cli\n    command: go test ./internal/cli\n    setup_command: go mod download\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadVerificationPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Digest == "" || policy.Scopes[0].ID != "cli" {
		t.Fatalf("policy = %#v", policy)
	}
	if err := policy.VerifyScope("cli", "go test ./internal/cli", "go mod download"); err != nil {
		t.Fatal(err)
	}
	if err := policy.VerifyScope("cli", "go test ./...", "go mod download"); err == nil {
		t.Fatal("accepted mismatched command metadata")
	}
	if err := os.WriteFile(path, []byte("version: 1\nscopes:\n  - id: cli\n    command: go test ./...\n  - id: cli\n    command: go test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerificationPolicy(repo); err == nil {
		t.Fatal("accepted duplicate policy scope")
	}
	if err := os.WriteFile(path, []byte("version: 1\nscopes: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerificationPolicy(repo); err == nil {
		t.Fatal("accepted an empty policy")
	}
}
