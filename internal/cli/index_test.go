package cli

import (
	"bytes"
	"encoding/json"
	"testing"
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
		}, []string{"index", "--repo", repo, "--head", "--profile", "syntax-only", "--format", "json"})
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
	if first.RepoRoot != repo || first.Commit == "" || first.Tree == "" || first.Profile != "syntax-only" {
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

func TestIndexRequiresDurableCacheAndRejectsWorktree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo}, Stdout: &bytes.Buffer{}}, []string{"index"}); err == nil {
		t.Fatal("expected index without a durable cache to fail")
	}
	if err := Run(t.Context(), Options{Version: "test", Env: EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()}, Stdout: &bytes.Buffer{}}, []string{"index", "--worktree"}); err == nil {
		t.Fatal("expected --worktree to fail")
	}
}
