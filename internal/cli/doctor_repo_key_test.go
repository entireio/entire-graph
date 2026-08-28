package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func initDoctorRepo(t *testing.T, dir, remote string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if remote != "" {
		cmd := exec.Command("git", "remote", "add", "origin", remote)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v\n%s", err, out)
		}
	}
}

// TestDoctorReportsRepoKeyAndSchemaVersion pins the seam handshake. entire-brain
// runs `graph doctor --json` before every snapshot; reporting the repo_key this
// binary WILL stamp into the snapshot, plus the schema version it speaks, lets
// the consumer verify compatibility in milliseconds instead of discovering a
// mismatch after a full (up to 30 minute) snapshot run.
func TestDoctorReportsRepoKeyAndSchemaVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remote  string
		wantKey func(dir string) string
	}{
		{
			name:    "github-remote",
			remote:  "git@github.com:example/repo.git",
			wantKey: func(string) string { return "gh/example/repo" },
		},
		{
			name:    "no-remote",
			remote:  "",
			wantKey: func(dir string) string { return "local/" + filepath.Base(dir) },
		},
		{
			name:    "gitlab-remote",
			remote:  "https://gitlab.com/acme/widget.git",
			wantKey: func(dir string) string { return "local/" + filepath.Base(dir) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			initDoctorRepo(t, repo, tc.remote)

			var out bytes.Buffer
			if err := Run(t.Context(), Options{
				Version: "0.1.0",
				Env:     EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()},
				Stdout:  &out,
			}, []string{"doctor", "--json"}); err != nil {
				t.Fatalf("doctor: %v", err)
			}
			var report map[string]any
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("doctor json invalid:\n%s\n%v", out.String(), err)
			}
			if got, want := report["repo_key"], tc.wantKey(repo); got != want {
				t.Fatalf("doctor repo_key = %v, want %q (report: %s)", got, want, out.String())
			}
			if got, want := report["schema_version"], sem.SchemaVersion; got != want {
				t.Fatalf("doctor schema_version = %v, want %q", got, want)
			}
		})
	}
}

// TestDoctorRepoKeyMatchesSnapshotHeader is the anti-drift gate: whatever
// doctor advertises must be byte-identical to what the snapshot header carries,
// or the handshake is worse than no handshake at all.
func TestDoctorRepoKeyMatchesSnapshotHeader(t *testing.T) {
	repo := t.TempDir()
	initDoctorRepo(t, repo, "")

	var doctorOut bytes.Buffer
	if err := Run(t.Context(), Options{
		Version: "0.1.0",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()},
		Stdout:  &doctorOut,
	}, []string{"doctor", "--json"}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(doctorOut.Bytes(), &report); err != nil {
		t.Fatalf("doctor json invalid: %v", err)
	}
	if got, want := report["repo_key"], sem.RepoKey(t.Context(), repo); got != want {
		t.Fatalf("doctor repo_key = %v, want the provider's own %q", got, want)
	}
}

// TestRepoKeyContractGoldenVectors is the shared contract table. entire-brain
// asserts the SAME vectors in providerSemanticRepoKey's test, so changing the
// rule on either side breaks a test on both rather than silently splitting the
// seam.
func TestRepoKeyContractGoldenVectors(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"git@github.com:example/repo.git", "gh/example/repo"},
		{"https://github.com/example/repo.git", "gh/example/repo"},
		{"https://github.com/example/repo", "gh/example/repo"},
		{"ssh://git@github.com/example/repo.git", "gh/example/repo"},
		{"http://github.com/example/repo.git", "gh/example/repo"},
		{"https://github.com/example/nested/repo.git", ""},
		{"https://gitlab.com/acme/widget.git", ""},
		{"git@bitbucket.org:acme/widget.git", ""},
		{"https://git.corp.internal/acme/widget.git", ""},
		{"", ""},
	} {
		repo := t.TempDir()
		initDoctorRepo(t, repo, tc.remote)
		want := tc.want
		if want == "" {
			want = "local/" + filepath.Base(repo)
		}
		if got := sem.RepoKey(t.Context(), repo); got != want {
			t.Fatalf("sem.RepoKey(remote=%q) = %q, want %q", tc.remote, got, want)
		}
	}
}
