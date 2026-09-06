package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestIndexBuildsAndReusesHeadCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", `package auth

func ValidateToken(token string) bool { return token != "" }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	run := func() indexResponse {
		t.Helper()
		var output bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &output,
		}, []string{"index", "--repo", repo, "--head", "--format", "json"})
		if err != nil {
			t.Fatal(err)
		}
		var response indexResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatalf("decode index response %q: %v", output.String(), err)
		}
		return response
	}

	first := run()
	if first.FormatVersion != 1 || first.Provider != "entire-graph" || first.ProviderVersion != "test-version" {
		t.Fatalf("unexpected index product binding: %#v", first)
	}
	if first.IndexCacheHit {
		t.Fatal("first index unexpectedly hit cache")
	}
	if first.RepoRoot != repo || first.Commit == "" || first.Tree == "" || first.Profile != "full" {
		t.Fatalf("unexpected index provenance: %#v", first)
	}
	if first.Counts.Files != 1 || first.Counts.Symbols == 0 {
		t.Fatalf("unexpected index counts: %#v", first.Counts)
	}
	if first.Warnings == nil {
		t.Fatal("warnings must encode as an array")
	}

	second := run()
	if !second.IndexCacheHit {
		t.Fatal("second index did not hit cache")
	}
}

func TestIndexRepeatableIgnoreAndIncludeFilesShareCanonicalCacheKey(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "keep.go", "package sample\nfunc Keep() {}\n")
	write(t, repo, "ignored_one.go", "package sample\nfunc IgnoredOne() {}\n")
	write(t, repo, "ignored_two.go", "package sample\nfunc IgnoredTwo() {}\n")
	write(t, repo, "reopened_one.go", "package sample\nfunc ReopenedOne() {}\n")
	write(t, repo, "reopened_two.go", "package sample\nfunc ReopenedTwo() {}\n")
	write(t, repo, ".ignore-one", "ignored_one.go\nreopened_one.go\n")
	write(t, repo, ".ignore-two", "ignored_two.go\nreopened_two.go\n")
	write(t, repo, ".include-one", "reopened_one.go\n")
	write(t, repo, ".include-two", "reopened_two.go\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	var output bytes.Buffer
	err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
		Stdout:  &output,
	}, []string{
		"index", "--repo", repo,
		"--ignore-file", ".ignore-one",
		"--ignore-file", ".ignore-two",
		"--include-file", ".include-one",
		"--include-file", ".include-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	var response indexResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode index response %q: %v", output.String(), err)
	}
	if response.Profile != "full" {
		t.Fatalf("default index profile = %q, want full", response.Profile)
	}
	if response.Counts.Files != 3 {
		t.Fatalf("indexed files = %d, want keep plus two reopened files", response.Counts.Files)
	}

	_, cacheHit, err := sem.PreindexProviderSnapshot(t.Context(), repo, "test-version", sem.ProviderSnapshotOptions{
		Profile:      sem.ProfileFull,
		IgnoreFiles:  []string{".ignore-one", ".ignore-two"},
		IncludeFiles: []string{".include-one", ".include-two"},
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("index flags did not populate the canonical cache key used by search/neighbors")
	}
}

// TestIndexDefaultsItsCacheAndRejectsWorktree replaces an expectation the default
// cache directory made obsolete: `index` with no --cache-dir and no
// ENTIRE_PLUGIN_DATA_DIR used to be an error, because there was nowhere durable to
// write. It now falls back to the platform's per-user cache directory, so the only
// remaining refusal is --worktree, which has no durable tree hash to key on.
func TestIndexDefaultsItsCacheAndRejectsWorktree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	// Point the platform cache root at a temp dir so the test writes nothing to the
	// developer's real cache. (Honoured by os.UserCacheDir everywhere except macOS,
	// which is why the assertion below only requires that the command succeed.)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &bytes.Buffer{}}, []string{"index", "--repo", repo, "--format", "json"}); err != nil {
		t.Fatalf("index with no cache flag or env must default its cache directory: %v", err)
	}
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()}, Stdout: &bytes.Buffer{}}, []string{"index", "--worktree"}); err == nil {
		t.Fatal("expected --worktree to fail")
	}
}

// TestResolveCacheDirDefaultsToPlatformCacheDir pins the precedence that turned
// committed-tree caching on for anyone who had not exported
// ENTIRE_PLUGIN_DATA_DIR by hand: explicit flag, then the plugin data dir, then
// the platform's per-user cache directory.
func TestResolveCacheDirDefaultsToPlatformCacheDir(t *testing.T) {
	if got := resolveCacheDir("/flag/dir", "/env/dir"); got != "/flag/dir" {
		t.Fatalf("--cache-dir must win, got %q", got)
	}
	if got := resolveCacheDir("", "/env/dir"); got != "/env/dir" {
		t.Fatalf("ENTIRE_PLUGIN_DATA_DIR must win over the default, got %q", got)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("platform has no per-user cache directory: %v", err)
	}
	want := filepath.Join(base, cacheDirName)
	if got := resolveCacheDir("", ""); got != want {
		t.Fatalf("resolveCacheDir with nothing set = %q, want the platform cache dir %q", got, want)
	}
}

func TestIndexReportsPartialFailuresAndCompleteness(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "supported.py", "def validate_token(token):\n    return bool(token)\n")
	write(t, repo, "unsupported.f90", "subroutine unsupported\nend subroutine unsupported\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	var output bytes.Buffer
	if err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()},
		Stdout:  &output,
	}, []string{"index", "--repo", repo, "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	var response indexResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode index response %q: %v", output.String(), err)
	}
	if len(response.PartialFailures) != 1 ||
		response.PartialFailures[0].Code != "E_UNSUPPORTED_LANGUAGE" ||
		response.PartialFailures[0].FilePath != "unsupported.f90" {
		t.Fatalf("partial failures = %#v", response.PartialFailures)
	}
	if response.Counts.PartialFailures != len(response.PartialFailures) || response.Counts.CompletenessLevel == "ok" {
		t.Fatalf("counts do not expose incomplete index: %#v", response.Counts)
	}
	if response.Completeness.Languages["Python"].Files != 1 {
		t.Fatalf("completeness = %#v", response.Completeness)
	}
}
