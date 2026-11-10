package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeadCacheIsSharedAcrossWorktreesUnderEachRoot pins the cross-worktree
// half of the path-free cache key: a second linked worktree at the same commit
// is served the first one's entry, and everything path-bearing in what it is
// served names the SECOND worktree.
//
// The first worktree is deleted before the reads that quote source, so a root
// left pointing at it cannot pass by reading files that happen to still exist
// there: neighbors opens its git reader and quotes call sites through
// Header.RepoRoot (callsite.go).
func TestHeadCacheIsSharedAcrossWorktreesUnderEachRoot(t *testing.T) {
	parent := t.TempDir()
	main := filepath.Join(parent, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	initDoctorRepo(t, main, "git@github.com:example/shared.git")
	write(t, main, "app/run.go", `package app

func helper() string { return "h" }

// Run frobnicates the ledger through the helper.
func Run() string {
	return helper() // the ledger call site
}
`)
	git(t, main, "add", "-A")
	git(t, main, "commit", "-q", "-m", "initial")
	wtA, wtB := filepath.Join(parent, "wtA"), filepath.Join(parent, "wtB")
	git(t, main, "worktree", "add", "-q", "--detach", wtA, "HEAD")
	git(t, main, "worktree", "add", "-q", "--detach", wtB, "HEAD")

	cacheDir := t.TempDir()
	run := func(repo string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := Run(t.Context(), Options{
			Version: "0.1.0",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &out,
		}, append(args, "--repo", repo, "--head", "--cache-dir", cacheDir)); err != nil {
			t.Fatalf("%v in %s: %v\n%s", args, repo, err, out.String())
		}
		return out.String()
	}
	type queryResponse struct {
		RepoRoot string `json:"repo_root"`
		Stats    struct {
			IndexCacheHit bool `json:"index_cache_hit"`
		} `json:"stats"`
		Results []struct {
			SymbolName string `json:"symbol_name"`
		} `json:"results"`
	}
	query := func(repo string) (queryResponse, string) {
		t.Helper()
		raw := run(repo, "query", "--profile", "full", "--format", "json", "--query", "Run frobnicates the ledger")
		var response queryResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatalf("query json invalid:\n%s\n%v", raw, err)
		}
		return response, raw
	}

	first, _ := query(wtA)
	if first.Stats.IndexCacheHit || first.RepoRoot != wtA {
		t.Fatalf("first query: hit=%v repo_root=%q, want a cold build in %q", first.Stats.IndexCacheHit, first.RepoRoot, wtA)
	}
	second, raw := query(wtB)
	if !second.Stats.IndexCacheHit {
		t.Fatal("a second worktree at the same commit missed the first one's entry")
	}
	if second.RepoRoot != wtB || strings.Contains(raw, wtA) {
		t.Fatalf("shared entry served under the wrong root: repo_root=%q (want %q), output names %q: %t",
			second.RepoRoot, wtB, wtA, strings.Contains(raw, wtA))
	}
	if len(second.Results) == 0 || len(first.Results) != len(second.Results) {
		t.Fatalf("shared entry changed the answer: %d results vs %d", len(second.Results), len(first.Results))
	}

	if err := os.RemoveAll(wtA); err != nil {
		t.Fatal(err)
	}
	git(t, main, "worktree", "prune")

	var neighbors struct {
		RepoRoot      string `json:"repo_root"`
		IndexCacheHit bool   `json:"index_cache_hit"`
	}
	rawNeighbors := run(wtB, "neighbors", "--profile", "full", "--symbol", "helper", "--direction", "in", "--format", "json")
	if err := json.Unmarshal([]byte(rawNeighbors), &neighbors); err != nil {
		t.Fatalf("neighbors json invalid:\n%s\n%v", rawNeighbors, err)
	}
	if !neighbors.IndexCacheHit || neighbors.RepoRoot != wtB {
		t.Fatalf("neighbors: hit=%v repo_root=%q, want a hit served under %q", neighbors.IndexCacheHit, neighbors.RepoRoot, wtB)
	}
	text := run(wtB, "neighbors", "--profile", "full", "--symbol", "helper", "--direction", "in", "--format", "text")
	if !strings.Contains(text, "the ledger call site") {
		t.Fatalf("neighbors in the second worktree could not quote the call site through the served root:\n%s", text)
	}
}
