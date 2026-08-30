package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// sameNamedRepo creates <parent>/<name> as a committed git repository with no
// remote, so its repo_key is `local/<name>`. Two of these under different
// parents are two DIFFERENT repositories that publish the SAME key.
func sameNamedRepo(t *testing.T, parent, name, file, content string) string {
	t.Helper()
	repo := filepath.Join(parent, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initDoctorRepo(t, repo, "")
	write(t, repo, file, content)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// TestRepoKeyLocalIsNotGloballyUnique pins the boundary of the contract doctor
// now publishes: only the gh/ half of the rule is globally unique. Two
// unrelated repositories that share a directory basename and have no
// github.com origin publish the SAME repo_key, so a consumer that treats
// repo_key match as proof of repository identity is wrong. Measured, not
// assumed — this is the property the RepoKey doc comment states, and the test
// exists so the doc cannot quietly start claiming uniqueness.
func TestRepoKeyLocalIsNotGloballyUnique(t *testing.T) {
	t.Parallel()

	left := sameNamedRepo(t, t.TempDir(), "tools", "alpha.go", "package alpha\n\nfunc Alpha() string { return \"a\" }\n")
	right := sameNamedRepo(t, t.TempDir(), "tools", "beta.go", "package beta\n\nfunc Beta() string { return \"b\" }\n")

	leftKey := sem.RepoKey(t.Context(), left)
	rightKey := sem.RepoKey(t.Context(), right)
	if leftKey != "local/tools" || rightKey != "local/tools" {
		t.Fatalf("local repo keys = %q and %q, want both %q", leftKey, rightKey, "local/tools")
	}

	// The gh/ half of the same rule DOES separate two owners, which is why the
	// contract distinguishes the halves instead of condemning the whole key.
	orgA := t.TempDir()
	initDoctorRepo(t, orgA, "git@github.com:org-a/tools.git")
	orgB := t.TempDir()
	initDoctorRepo(t, orgB, "git@github.com:org-b/tools.git")
	if a, b := sem.RepoKey(t.Context(), orgA), sem.RepoKey(t.Context(), orgB); a == b {
		t.Fatalf("github repo keys collided: both %q", a)
	} else if a != "gh/org-a/tools" || b != "gh/org-b/tools" {
		t.Fatalf("github repo keys = %q and %q, want gh/org-a/tools and gh/org-b/tools", a, b)
	}
}

// TestCollidingRepoKeysDoNotShareCacheEntries is the guard that makes the
// collision inert. Both persistent cache families hash the absolute repository
// path beside the repo key, so two repositories that publish the same
// `local/tools` AND stand at a byte-identical tree — the only configuration in
// which every other keyed term also agrees — still get their own entry and
// still report their own repo_root.
//
// Without the path term the second repository is served the first's entry and
// reports the FIRST repository's root as its own, which is a foreign snapshot
// handed to a consumer under a matching repo_key. ADR 0002 names identity-only
// keying as a future change it deliberately did not take; this test is what
// stops that change landing while `local/<basename>` can collide.
func TestCollidingRepoKeysDoNotShareCacheEntries(t *testing.T) {
	t.Parallel()

	const source = "package fixture\n\nfunc Shared() string { return \"s\" }\n"
	left := sameNamedRepo(t, t.TempDir(), "tools", "shared.go", source)
	right := sameNamedRepo(t, t.TempDir(), "tools", "shared.go", source)

	// The premise: same key, same tree, different repository.
	if leftKey, rightKey := sem.RepoKey(t.Context(), left), sem.RepoKey(t.Context(), right); leftKey != rightKey {
		t.Fatalf("premise lost: repo keys %q and %q no longer collide; update this test with the new rule", leftKey, rightKey)
	}
	if leftTree, rightTree := rev(t, left, "HEAD^{tree}"), rev(t, right, "HEAD^{tree}"); leftTree != rightTree {
		t.Fatalf("premise lost: trees %q and %q differ", leftTree, rightTree)
	}

	cacheDir := t.TempDir()
	for _, repo := range []string{left, right} {
		var out bytes.Buffer
		if err := Run(t.Context(), Options{
			Version: "0.1.0",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &out,
		}, []string{"index", "--repo", repo, "--cache-dir", cacheDir, "--format", "json"}); err != nil {
			t.Fatalf("index %s: %v\n%s", repo, err, out.String())
		}
		var summary struct {
			RepoRoot string `json:"repo_root"`
			CacheHit bool   `json:"index_cache_hit"`
		}
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
			t.Fatalf("index summary invalid:\n%s\n%v", out.String(), err)
		}
		if summary.CacheHit {
			t.Fatalf("index %s reported a cache hit; a colliding repository's entry was served", repo)
		}
		if summary.RepoRoot != repo {
			t.Fatalf("index repo_root = %q, want %q: a foreign repository's snapshot was served under a matching repo_key", summary.RepoRoot, repo)
		}
	}

	if entries := countCacheEntries(t, cacheDir); entries != 2 {
		t.Fatalf("cache holds %d entries for two colliding repositories, want 2", entries)
	}
}

func countCacheEntries(t *testing.T, cacheDir string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".json.gz") {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk cache dir: %v", err)
	}
	return count
}

// TestDoctorRepoKeyIsIndependentOfHowTheRepoRootIsSpelled closes the gap the
// handshake left open. resolveRepo returns --repo and ENTIRE_REPO_ROOT verbatim
// and falls back to ".", while every path that BUILDS a snapshot derives the key
// from filepath.Abs of the same input. The two therefore disagreed for any
// unnormalised spelling: the local/ half of the rule is the last path element, so
// "." published local/. — a namespace shared by every repository on the machine,
// and never the one the snapshot would carry. A consumer comparing its own
// prediction against that report would reject a native snapshot, or accept a
// foreign one, on the strength of a value no build ever emits.
func TestDoctorRepoKeyIsIndependentOfHowTheRepoRootIsSpelled(t *testing.T) {
	repo := t.TempDir()
	initDoctorRepo(t, repo, "")
	want := "local/" + filepath.Base(repo)

	t.Chdir(repo)
	for _, spelling := range []struct {
		name string
		root string
	}{
		{"absolute", repo},
		{"working directory", "."},
		{"uncleaned absolute", repo + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".."},
		{"trailing dot", repo + string(filepath.Separator) + "."},
	} {
		t.Run(spelling.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Run(t.Context(), Options{
				Version: "0.1.0",
				Env:     EntireEnv{RepoRoot: spelling.root, PluginDataDir: t.TempDir()},
				Stdout:  &out,
			}, []string{"doctor", "--json"}); err != nil {
				t.Fatalf("doctor: %v", err)
			}
			var report map[string]any
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("doctor json invalid:\n%s\n%v", out.String(), err)
			}
			if got := report["repo_key"]; got != want {
				t.Fatalf("doctor repo_key for root %q = %v, want %q (report: %s)", spelling.root, got, want, out.String())
			}
		})
	}
}
