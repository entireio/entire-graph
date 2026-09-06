package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/intent"
	"github.com/entireio/entire-graph/internal/sem"
)

func copyGPSFixture(t *testing.T, name string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.CopyFS(repo, os.DirFS(filepath.Join("testdata", "gps", name))); err != nil {
		t.Fatal(err)
	}
	return repo
}

func gpsGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

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

func TestGPSEvidenceClassificationMarksPartialRelationshipsIncomplete(t *testing.T) {
	evidence := gpsEvidenceClassification(sem.ProviderSnapshot{Header: sem.SnapshotHeader{Stats: sem.ProviderStats{CompletenessLevel: "degraded"}, PartialFailures: []sem.PartialFailure{{Code: "E_PARSE_ERROR", FilePath: "dynamic.go"}}}})
	if evidence[0]["status"] != "confirmed" || evidence[1]["status"] != "unverified" || evidence[2]["status"] != "incomplete" {
		t.Fatalf("evidence classification = %#v", evidence)
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

func TestGPSContextFallsBackToCodeWithoutSpecs(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\nfunc TokenLifetime() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"context", "--repo", repo, "--query", "token lifetime", "--format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NO_SPECS") || !strings.Contains(out.String(), "token.go") {
		t.Fatalf("context did not return code fallback: %s", out.String())
	}
}

func TestGPSContextRejectsUnsatisfiableBudget(t *testing.T) {
	err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: t.TempDir()}}, []string{"context", "--query", "token", "--max-context-bytes", "1"})
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("context budget error = %v", err)
	}
}

func TestGPSCheckReportsDeltaNotRequested(t *testing.T) {
	repo := t.TempDir()
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "token.yaml"), []byte("version: 1\nid: SPEC-1\ntitle: Token\nrequirements:\n  - id: REQ-1\n    description: Token lifetime.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"change_delta":"not_requested"`) {
		t.Fatalf("missing no-base delta state: %s", out.String())
	}
}

func TestGPSCheckCommittedBaseReportsSpecOnlyDelta(t *testing.T) {
	repo := t.TempDir()
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\nfunc Lifetime() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(repo, ".entire", "graph", "specs", "token.yaml")
	if err := os.WriteFile(specPath, []byte("version: 1\nid: SPEC-1\ntitle: Token\nrequirements:\n  - id: REQ-1\n    description: Token lifetime.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "initial gps fixture")
	baseCmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	baseOutput, err := baseCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, []byte("version: 1\nid: SPEC-1\ntitle: Token\nrequirements:\n  - id: REQ-1\n    description: Token lifetime is configurable.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "change intent only")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo, "--head", "--base", strings.TrimSpace(string(baseOutput))}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "GPS-DELTA-SPEC-ONLY") {
		t.Fatalf("missing spec-only delta: %s", out.String())
	}
}

func TestGPSCheckDeltaIgnoresCommentOnlyAnchoredFileChange(t *testing.T) {
	repo := t.TempDir()
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Token\nrequirements:\n  - id: REQ-1\n    description: Token.\nanchors:\n  - id: ANCHOR-1\n    requirement: REQ-1\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "token.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\nfunc Lifetime() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"anchor", "bind", "--repo", repo, "--id", "ANCHOR-1", "--symbol", "Lifetime", "--file", "token.go"}); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "baseline")
	base := gpsRevision(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\n// Lifetime is configurable.\nfunc Lifetime() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", "token.go")
	gpsGit(t, repo, "commit", "-m", "comment only")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo, "--head", "--base", base}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "GPS-DELTA-ANCHOR") {
		t.Fatalf("comment-only edit produced anchor delta: %s", out.String())
	}
}

func TestGPSWhyHistoryReturnsLocalCheckpointProvenance(t *testing.T) {
	repo := t.TempDir()
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	spec := "version: 1\nid: SPEC-1\ntitle: Token\nrequirements:\n  - id: REQ-1\n    description: Token.\nanchors:\n  - id: ANCHOR-1\n    requirement: REQ-1\n"
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "token.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\nfunc Lifetime() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"anchor", "bind", "--repo", repo, "--id", "ANCHOR-1", "--symbol", "Lifetime", "--file", "token.go"}); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "implementation\n\nEntire-Checkpoint: gps-history")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"why", "--repo", repo, "--head", "--symbol", "Lifetime", "--file", "token.go", "--history", "--history-limit", "1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"AVAILABLE"`) || !strings.Contains(out.String(), "gps-history") {
		t.Fatalf("missing local history provenance: %s", out.String())
	}
}

func TestGPSCapturedCommittedViewDetectsHeadChange(t *testing.T) {
	repo := t.TempDir()
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "first")
	view, err := gpsCaptureView(t.Context(), repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "token.go"), []byte("package token\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "second")
	if !view.inputChanged(t.Context(), repo) {
		t.Fatal("committed input change was not detected")
	}
}

func TestGPSSpecValidateAggregatesStructuredDiagnostics(t *testing.T) {
	repo := t.TempDir()
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"spec", "init", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"missing-title.yaml": "version: 1\nid: SPEC-1\nrequirements:\n  - id: REQ-1\n    description: valid\n",
		"unknown-field.yaml": "version: 1\nid: SPEC-2\ntitle: Invalid\nrequirements:\n  - id: REQ-2\n    description: valid\nunknown: true\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"spec", "validate", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Path string `json:"path"`
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Valid || len(response.Diagnostics) != 2 || response.Diagnostics[0].Path >= response.Diagnostics[1].Path {
		t.Fatalf("unexpected aggregate response: %s", out.String())
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "gps", "golden", "spec-validate-invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion   string   `json:"schema_version"`
		Valid           bool     `json:"valid"`
		DiagnosticCodes []string `json:"diagnostic_codes"`
	}
	if err := json.Unmarshal(golden, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != "1.0" || contract.Valid || len(contract.DiagnosticCodes) != len(response.Diagnostics) || response.Diagnostics[0].Code != contract.DiagnosticCodes[0] || response.Diagnostics[1].Code != contract.DiagnosticCodes[1] {
		t.Fatalf("validation contract changed: %s", out.String())
	}
}

func TestGPSGitFixtureCodeOnlyGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "token auth fixture")
	baseOutput, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package auth\nfunc Authenticate(user, password string) (string, bool) { return \"\", false }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", "auth.go")
	gpsGit(t, repo, "commit", "-m", "change authentication")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo, "--head", "--base", strings.TrimSpace(string(baseOutput))}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-code-only.json", out.Bytes())
}

func TestGPSGitFixtureDeletedAnchorAndTestMappingGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte("version: 1\nid: SPEC-AUTH-001\ntitle: Token authentication\nrequirements:\n  - id: REQ-AUTH-TOKEN\n    description: Valid credentials produce an access token.\nacceptance:\n  - id: ACC-AUTH-TOKEN\n    requirement: REQ-AUTH-TOKEN\n    description: Authentication returns a token.\nanchors:\n  - id: ANCHOR-AUTH\n    requirement: REQ-AUTH-TOKEN\ntests:\n  - id: TEST-AUTH-TOKEN\n    acceptance: ACC-AUTH-TOKEN\n    selector:\n      name: TestAuthenticateReturnsToken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "add reviewed auth mapping")
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"anchor", "bind", "--repo", repo, "--id", "ANCHOR-AUTH", "--symbol", "Authenticate", "--file", "auth.go"}); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "bind auth anchor")
	base := gpsRevision(t, repo)
	if err := os.Remove(filepath.Join(repo, "auth.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte("version: 1\nid: SPEC-AUTH-001\ntitle: Token authentication\nrequirements:\n  - id: REQ-AUTH-TOKEN\n    description: Valid credentials produce an access token.\nacceptance:\n  - id: ACC-AUTH-TOKEN\n    requirement: REQ-AUTH-TOKEN\n    description: Authentication returns a token.\nanchors:\n  - id: ANCHOR-AUTH\n    requirement: REQ-AUTH-TOKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", "-A")
	gpsGit(t, repo, "commit", "-m", "remove auth implementation and mapping")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo, "--head", "--base", base}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-deleted-anchor-test-mapping.json", out.Bytes())
}

func TestGPSGitFixtureDirtyWorktreeAndHeadGoldenContracts(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "token auth fixture")
	if err := os.Remove(filepath.Join(repo, "auth_test.go")); err != nil {
		t.Fatal(err)
	}
	var worktree bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &worktree}, []string{"check", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-dirty-worktree.json", worktree.Bytes())
	var head bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &head}, []string{"check", "--repo", repo, "--head"}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-head-clean.json", head.Bytes())
}

func TestGPSGitFixtureIgnoredPathGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte("version: 1\nid: SPEC-AUTH-001\ntitle: Ignored test source\nrequirements:\n  - id: REQ-IGNORED\n    description: Ignored files are not indexed.\nacceptance:\n  - id: ACC-IGNORED\n    requirement: REQ-IGNORED\n    description: A declared test must be visible.\ntests:\n  - id: TEST-IGNORED\n    acceptance: ACC-IGNORED\n    selector:\n      name: TestIgnoredPath\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "declare ignored test mapping")
	if err := os.WriteFile(filepath.Join(repo, "ignored.go"), []byte("package auth\nfunc TestIgnoredPath() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "check-ignore", "ignored.go")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-ignored-path.json", out.Bytes())
}

func TestGPSGitFixtureMalformedIntentValidationGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.MkdirAll(filepath.Join(repo, ".entire", "graph", "anchors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "anchors", "malformed.yaml"), []byte("version: 1\nanchors:\n  - id: ANCHOR-AUTH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "add malformed binding")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"spec", "validate", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	assertGPSValidationGolden(t, "spec-validate-malformed-binding.json", out.Bytes())
}

func TestGPSGitFixturePartialGraphGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.WriteFile(filepath.Join(repo, "broken.go"), []byte("package auth\nfunc Broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "add malformed source")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"check", "--repo", repo, "--head"}); err != nil {
		t.Fatal(err)
	}
	assertGPSCheckGolden(t, "check-partial-graph.json", out.Bytes())
}

func TestGPSGitFixtureAmbiguousBindingGoldenContract(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	if err := os.WriteFile(filepath.Join(repo, "other.go"), []byte("package auth\nfunc Authenticate() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "specs", "auth.yaml"), []byte("version: 1\nid: SPEC-AUTH-001\ntitle: Ambiguous anchor\nrequirements:\n  - id: REQ-AUTH\n    description: Authentication is anchored.\nanchors:\n  - id: ANCHOR-AUTH\n    requirement: REQ-AUTH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".entire", "graph", "anchors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".entire", "graph", "anchors", "auth.yaml"), []byte("version: 1\nanchors:\n  - id: ANCHOR-AUTH\n    symbol_id: removed-symbol\n    selector:\n      qualified_name: Authenticate\n      file: auth.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-m", "add ambiguous anchor candidates")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"anchor", "resolve", "--repo", repo, "--head", "--id", "ANCHOR-AUTH"}); err != nil {
		t.Fatal(err)
	}
	assertGPSAnchorGolden(t, "anchor-resolve-ambiguous.json", out.Bytes())
}

func TestGPSGitFixtureUnbornHeadFails(t *testing.T) {
	repo := t.TempDir()
	gpsGit(t, repo, "init")
	err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}}, []string{"check", "--repo", repo, "--head"})
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("check --head on unborn repository error = %v, want HEAD resolution failure", err)
	}
}

func gpsRevision(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func assertGPSCheckGolden(t *testing.T, name string, output []byte) {
	t.Helper()
	var response struct {
		SchemaVersion  string `json:"schema_version"`
		ChangeDelta    string `json:"change_delta"`
		Disposition    string `json:"disposition"`
		RepositoryView struct {
			Kind string `json:"kind"`
		} `json:"repository_view"`
		Findings []struct {
			ID string `json:"id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion      string   `json:"schema_version"`
		ChangeDelta        string   `json:"change_delta"`
		Disposition        string   `json:"disposition"`
		RepositoryViewKind string   `json:"repository_view_kind"`
		FindingIDs         []string `json:"finding_ids"`
	}
	readGPSGolden(t, name, &contract)
	ids := make([]string, len(response.Findings))
	for i, finding := range response.Findings {
		ids[i] = finding.ID
	}
	if response.SchemaVersion != contract.SchemaVersion || response.ChangeDelta != contract.ChangeDelta || response.Disposition != contract.Disposition || response.RepositoryView.Kind != contract.RepositoryViewKind || strings.Join(ids, ",") != strings.Join(contract.FindingIDs, ",") {
		t.Fatalf("GPS check contract %s changed: %s", name, output)
	}
}

func assertGPSValidationGolden(t *testing.T, name string, output []byte) {
	t.Helper()
	var response struct {
		SchemaVersion string `json:"schema_version"`
		Valid         bool   `json:"valid"`
		Diagnostics   []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion   string   `json:"schema_version"`
		Valid           bool     `json:"valid"`
		DiagnosticCodes []string `json:"diagnostic_codes"`
	}
	readGPSGolden(t, name, &contract)
	if response.SchemaVersion != contract.SchemaVersion || response.Valid != contract.Valid || len(response.Diagnostics) != len(contract.DiagnosticCodes) {
		t.Fatalf("GPS validation contract %s changed: %s", name, output)
	}
	for i, diagnostic := range response.Diagnostics {
		if diagnostic.Code != contract.DiagnosticCodes[i] {
			t.Fatalf("GPS validation contract %s changed: %s", name, output)
		}
	}
}

func assertGPSAnchorGolden(t *testing.T, name string, output []byte) {
	t.Helper()
	var response struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		Candidates []any  `json:"candidates"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	var contract struct {
		ID             string `json:"id"`
		State          string `json:"state"`
		CandidateCount int    `json:"candidate_count"`
	}
	readGPSGolden(t, name, &contract)
	if response.ID != contract.ID || response.State != contract.State || len(response.Candidates) != contract.CandidateCount {
		t.Fatalf("GPS anchor contract %s changed: %s", name, output)
	}
}

func readGPSGolden(t *testing.T, name string, target any) {
	t.Helper()
	golden, err := os.ReadFile(filepath.Join("testdata", "gps", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(golden, target); err != nil {
		t.Fatal(err)
	}
}

func TestGPSContextQuotaPreservesSnippetAndInferredTest(t *testing.T) {
	repo := copyGPSFixture(t, "token-auth")
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"context", "--repo", repo, "--query", "token", "--max-context-bytes", "1600"}); err != nil {
		t.Fatal(err)
	}
	if len(out.Bytes()) > 1600 || !bytes.Contains(out.Bytes(), []byte(`"snippet"`)) || !bytes.Contains(out.Bytes(), []byte(`"inferred_tests"`)) || !bytes.Contains(out.Bytes(), []byte("TestAuthenticateReturnsToken")) {
		t.Fatalf("quota did not preserve bounded source and inferred tests: %s", out.String())
	}
}

func TestGPSContextSerializedUTF8StaysWithinMinimumBudget(t *testing.T) {
	repo := t.TempDir()
	var out bytes.Buffer
	query := strings.Repeat("token \u00e9 ", 200)
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"context", "--repo", repo, "--query", query, "--max-context-bytes", "512"}); err != nil {
		t.Fatal(err)
	}
	if len(out.Bytes()) > minimumGPSContextBytes || !json.Valid(out.Bytes()) {
		t.Fatalf("serialized context is not within its UTF-8 budget: %d bytes: %s", len(out.Bytes()), out.String())
	}
	var response struct {
		Budget struct {
			RenderedBytes int `json:"rendered_bytes"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Budget.RenderedBytes != len(out.Bytes()) {
		t.Fatalf("reported bytes = %d, serialized bytes = %d", response.Budget.RenderedBytes, len(out.Bytes()))
	}
}
