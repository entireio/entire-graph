package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDefDefaultsItsCacheDirToThePlatformCacheDir: with no --cache-dir and no
// ENTIRE_PLUGIN_DATA_DIR, `def` must cache the committed tree under the
// platform's per-user cache directory like every other command does
// (resolveCacheDir). It used to fall back to the empty plugin dir only, so a
// def outside a plugin host never cached at all.
func TestDefDefaultsItsCacheDirToThePlatformCacheDir(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("HOME", cacheRoot)
	t.Setenv("LocalAppData", cacheRoot)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("platform has no per-user cache directory: %v", err)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "main.go", "package sample\n\nfunc Helper() int { return 1 }\n")
	git(t, repo, "add", "main.go")
	git(t, repo, "commit", "-m", "initial")

	hit := func() bool {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := Run(t.Context(), Options{
			Version: "def-cache-dir-test",
			Env:     EntireEnv{RepoRoot: repo},
			Stdout:  &out,
			Stderr:  &errOut,
		}, []string{"def", "--repo", repo, "--head", "--symbol", "Helper", "--format", "json"}); err != nil {
			t.Fatalf("def: %v\n%s", err, errOut.String())
		}
		var response struct {
			IndexCacheHit bool `json:"index_cache_hit"`
		}
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("decode %q: %v", out.String(), err)
		}
		return response.IndexCacheHit
	}
	if hit() {
		t.Fatal("first def on a fresh cache root reported a cache hit")
	}
	if !hit() {
		t.Fatal("second def on the same committed tree missed the cache: def ignored the platform cache dir")
	}
	if _, err := os.Stat(filepath.Join(base, cacheDirName)); err != nil {
		t.Fatalf("def wrote no cache under the platform cache dir: %v", err)
	}
}
