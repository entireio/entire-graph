package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/intent"
	"github.com/entireio/entire-graph/internal/sem"
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
	if err := os.MkdirAll(filepath.Join(repo, ".entire", "graph", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	decision := "version: 1\nid: ADR-1\ntitle: Authentication policy\ndecision: Use explicit authentication.\naffects:\n  - SPEC-1\nanchors:\n  - ANCHOR-1\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "decisions", "auth.yaml"), []byte(decision), 0o644); err != nil {
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
	out.Reset()
	if err := Run(t.Context(), opts, []string{"context", "--repo", repo, "--query", "authenticates", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "approved_anchor") {
		t.Fatalf("context omitted resolved anchor evidence: %s", out.String())
	}
	out.Reset()
	if err := Run(t.Context(), opts, []string{"why", "--repo", repo, "--symbol", "Authenticate", "--file", "auth.go", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "REQ-1") || !strings.Contains(out.String(), "ADR-1") {
		t.Fatalf("why omitted declared requirement: %s", out.String())
	}
}

func TestGPSCheckReportsUnresolvedDeclaredTest(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	opts := Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}
	if err := Run(t.Context(), opts, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Authentication\nrequirements:\n  - id: REQ-1\n    description: A user authenticates.\nacceptance:\n  - id: ACC-1\n    requirement: REQ-1\n    description: Authentication succeeds.\ntests:\n  - id: TEST-1\n    acceptance: ACC-1\n    selector:\n      name: TestMissing\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), opts, []string{"check", "--repo", repo, "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "GPS-MAPPING-UNRESOLVED") {
		t.Fatalf("check omitted unresolved declared test: %s", out.String())
	}
}

func TestResolveBindingIsUnverifiableForPartialFile(t *testing.T) {
	binding := intent.Binding{ID: "ANCHOR-1", SymbolID: "missing", Selector: intent.Selector{File: "auth.go"}}
	result := resolveBinding(binding, sem.ProviderSnapshot{Header: sem.SnapshotHeader{PartialFailures: []sem.PartialFailure{{FilePath: "auth.go", Code: "E_PARSE_ERROR"}}}})
	if got := result["state"]; got != "UNVERIFIABLE" {
		t.Fatalf("state = %v, want UNVERIFIABLE", got)
	}
}

func TestResolveBindingProposesButDoesNotApplyRebind(t *testing.T) {
	binding := intent.Binding{ID: "ANCHOR-1", SymbolID: "old", Selector: intent.Selector{QualifiedName: "Authenticate", File: "auth.go"}}
	snapshot := sem.ProviderSnapshot{Symbols: []sem.SymbolRecord{{ID: "new", QualifiedName: "Authenticate", FilePath: "auth.go"}}}
	result := resolveBinding(binding, snapshot)
	if got := result["state"]; got != "CANDIDATE_REBIND" {
		t.Fatalf("state = %v, want CANDIDATE_REBIND", got)
	}
}

func TestGPSCheckBaseRequiresCommittedView(t *testing.T) {
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: t.TempDir()}, Stdout: &out}, []string{"check", "--base", "HEAD"})
	if err == nil || !strings.Contains(err.Error(), "requires --head") {
		t.Fatalf("check --base error = %v, want committed-view requirement", err)
	}
}
