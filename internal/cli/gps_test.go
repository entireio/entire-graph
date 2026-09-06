package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGPSSpecAndContextCommands(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	opts := Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}
	if err := Run(t.Context(), opts, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Token lifetime\nrequirements:\n  - id: REQ-1\n    description: Token lifetime is configurable.\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "token.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(t.Context(), opts, []string{"context", "--repo", repo, "--query", "token lifetime", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "REQ-1") {
		t.Fatalf("context omitted matched requirement: %s", out.String())
	}
}

func TestGPSAnchorBindAndResolve(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	opts := Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}
	if err := Run(t.Context(), opts, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Authentication\nrequirements:\n  - id: REQ-1\n    description: A user authenticates.\nanchors:\n  - id: ANCHOR-1\n    requirement: REQ-1\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package auth\nfunc Authenticate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), opts, []string{"anchor", "bind", "--repo", repo, "--id", "ANCHOR-1", "--symbol", "Authenticate", "--file", "auth.go"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(t.Context(), opts, []string{"anchor", "resolve", "--repo", repo, "--id", "ANCHOR-1", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"VALID\"") {
		t.Fatalf("binding did not resolve as valid: %s", out.String())
	}
}
