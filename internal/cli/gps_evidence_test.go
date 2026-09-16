package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/intent"
)

func gpsEvidenceRepo(t *testing.T) string {
	t.Helper()
	repo := copyGPSFixture(t, "token-auth")
	gpsGit(t, repo, "init", "-q")
	gpsGit(t, repo, "config", "user.name", "GPS Test")
	gpsGit(t, repo, "config", "user.email", "gps@example.invalid")
	gpsGit(t, repo, "config", "core.hooksPath", "/dev/null")
	gpsGit(t, repo, "add", ".")
	gpsGit(t, repo, "commit", "-qm", "fixture")
	return repo
}

func TestGPSExecutionEvidenceProvenance(t *testing.T) {
	for _, mode := range []string{"current", "parsed_pass", "failed_exit", "failed_exit_parsed", "failed_result", "wrong_tree", "wrong_platform", "wrong_timeout", "old_commit", "other_repo", "missing_intent", "missing_clean_tree", "legacy", "policy", "dirty_worktree", "dirty_at_execution", "changed_during_execution", "untracked_evidence", "empty_parsed"} {
		t.Run(mode, func(t *testing.T) {
			repo := gpsEvidenceRepo(t)
			path := filepath.Join(t.TempDir(), "evidence.json")
			if mode == "untracked_evidence" {
				path = filepath.Join(repo, "evidence.json")
			}
			command := "true"
			if mode == "failed_exit" {
				command = "exit 1"
			}
			if mode == "changed_during_execution" {
				command = "printf '\n// changed\n' >> auth.go"
			}
			if mode == "dirty_at_execution" {
				mustGPSWrite(t, filepath.Join(repo, "untracked.txt"), "dirty")
			}
			var out bytes.Buffer
			if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"verify", "--repo", repo, "--test", command, "--record-baseline", path}); err != nil {
				t.Fatal(err)
			}
			baseline, err := readVerifyBaseline(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "STALE"
			switch mode {
			case "current", "untracked_evidence":
				want = "CURRENT"
			case "failed_exit":
				want = "FAILED"
			case "parsed_pass":
				baseline.Parser = "go test"
				baseline.Results = verifyResults{"test": verifyStatusPass}
				want = "CURRENT"
			case "failed_exit_parsed":
				baseline.ExitCode = 1
				baseline.Parser = "go test"
				baseline.Results = verifyResults{"test": verifyStatusPass}
				want = "FAILED"
			case "wrong_tree":
				baseline.Tree = strings.Repeat("0", 40)
			case "wrong_platform":
				baseline.Platform = "other"
			case "wrong_timeout":
				baseline.TimeoutMillis = 1
			case "failed_result":
				baseline.Results = verifyResults{"test": verifyStatusFail}
				want = "FAILED"
			case "old_commit":
				gpsGit(t, repo, "commit", "--allow-empty", "-qm", "next")
			case "other_repo":
				baseline.Repo = t.TempDir()
			case "missing_intent":
				baseline.IntentDigest = ""
			case "missing_clean_tree":
				baseline.CleanTree = false
			case "legacy":
				baseline = verifyBaseline{FormatVersion: 1, Results: verifyResults{"old": verifyStatusPass}}
			case "policy":
				baseline.PolicyDigest = "different"
			case "dirty_worktree":
				mustGPSWrite(t, filepath.Join(repo, "auth.go"), "package auth\n")
			case "dirty_at_execution":
				if err := os.Remove(filepath.Join(repo, "untracked.txt")); err != nil {
					t.Fatal(err)
				}
			case "empty_parsed":
				baseline.Parser = "go test"
				want = "UNAVAILABLE"
			}
			data, err := json.Marshal(baseline)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			set, err := intent.Load(repo)
			if err != nil {
				t.Fatal(err)
			}
			for _, head := range []bool{false, true} {
				view, err := gpsCaptureView(t.Context(), repo, head)
				if err != nil {
					t.Fatal(err)
				}
				got := gpsExecutionEvidence(t.Context(), repo, path, set, view)
				if got["status"] != want {
					t.Fatalf("head=%v got %v want %s", head, got, want)
				}
			}
		})
	}
}

func TestGPSReviewDisposition(t *testing.T) {
	for _, mode := range []string{"changed_test", "partial_graph", "partial_base", "unchanged", "removed_test_mapping"} {
		t.Run(mode, func(t *testing.T) {
			repo := copyGPSFixture(t, "token-auth")
			gpsGit(t, repo, "init", "-q")
			gpsGit(t, repo, "config", "user.name", "Review Fixture")
			gpsGit(t, repo, "config", "user.email", "review@example.invalid")
			gpsGit(t, repo, "config", "core.hooksPath", "/dev/null")
			if mode == "partial_base" {
				mustGPSWrite(t, filepath.Join(repo, "broken.go"), "package auth\nfunc Broken( {\n")
			}
			gpsGit(t, repo, "add", ".")
			gpsGit(t, repo, "commit", "-qm", "base")
			base := gpsRevision(t, repo)
			if mode == "changed_test" {
				path := filepath.Join(repo, "auth_test.go")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.Replace(string(data), "tokenLifetimeSeconds <= 0", "tokenLifetimeSeconds < 0", 1))
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			} else if mode == "removed_test_mapping" {
				path := filepath.Join(repo, ".entire/graph/specs/auth.yaml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.Split(string(data), "  - id: TEST-AUTH-EXPIRY")[0])
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			} else if mode == "partial_base" {
				if err := os.Remove(filepath.Join(repo, "broken.go")); err != nil {
					t.Fatal(err)
				}
			} else if mode == "partial_graph" {
				if err := os.WriteFile(filepath.Join(repo, "broken.go"), []byte("package auth\nfunc Broken( {\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			gpsGit(t, repo, "add", ".")
			gpsGit(t, repo, "commit", "--allow-empty", "-qm", mode)
			var out bytes.Buffer
			if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"review", "--repo", repo, "--base", base}); err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			want := "REVIEW_REQUIRED"
			if mode == "partial_graph" || mode == "partial_base" {
				want = "INCOMPLETE"
			}
			if mode == "unchanged" {
				want = "PASS"
			}
			if result["disposition"] != want {
				t.Fatalf("%s disposition want %s: %s", mode, want, out.String())
			}
			if mode == "changed_test" || mode == "removed_test_mapping" {
				if !strings.Contains(out.String(), `"id":"REQ-AUTH-EXPIRY"`) || result["tests"] == nil {
					t.Fatalf("missing affected requirement/tests: %s", out.String())
				}
			}
			if want == "INCOMPLETE" && !strings.Contains(out.String(), "GPS-COMPLETENESS-INCOMPLETE") {
				t.Fatal(out.String())
			}
		})
	}
}

func mustGPSWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
