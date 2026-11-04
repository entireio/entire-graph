package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreDescriptorUsesCurrentTreeAndRequiresBuild(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "1")
	repo := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache-\u009b")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "fixture@example.test")
	git("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Render() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
	var out bytes.Buffer
	opts := Options{Stdout: &out, Stderr: &out, Version: "store-contract-test"}
	args := []string{"--repo", repo, "--cache-dir", cache}
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	var missing struct {
		Unavailable bool `json:"store_unavailable"`
	}
	if err := json.Unmarshal(out.Bytes(), &missing); err != nil || !missing.Unavailable {
		t.Fatalf("missing=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := runIndex(context.Background(), opts, append(args, "--format", "json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out.String(), '\u009b') {
		t.Fatalf("descriptor emitted a raw C1 control: %q", out.String())
	}
	var d struct {
		Version int    `json:"contract_version"`
		Path    string `json:"path"`
		Commit  string `json:"commit"`
		Symbols int    `json:"symbols"`
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Commit == "" || d.Symbols == 0 || !filepath.IsAbs(d.Path) {
		t.Fatalf("descriptor=%s", out.String())
	}
	if !strings.ContainsRune(d.Path, '\u009b') {
		t.Fatalf("JSON escaping changed the decoded cache path: %q", d.Path)
	}
	if _, err := os.Stat(d.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Other() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "new tree")
	out.Reset()
	if err := runStore(context.Background(), opts, args); err != nil {
		t.Fatal(err)
	}
	missing.Unavailable = false
	if err := json.Unmarshal(out.Bytes(), &missing); err != nil || !missing.Unavailable {
		t.Fatalf("served prior tree: %s (%v)", out.String(), err)
	}
}

// TestIndexWritesTheStoreWithoutOptIn pins the sidecar default: with
// ENTIRE_GRAPH_SEARCH_STORE unset, `index` (and a cold `def --head`) write the
// sqlite sidecar that `graph store` describes, and an explicit "0" still opts
// out. Break caught: the default flipped back, the opt-out ignored, or the
// lookup cache-miss write dropping the sidecar again.
func TestIndexWritesTheStoreWithoutOptIn(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "")
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "fixture@example.test")
	git("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Render() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")

	type descriptor struct {
		Unavailable bool   `json:"store_unavailable"`
		Path        string `json:"path"`
	}
	describe := func(t *testing.T, cache string) descriptor {
		t.Helper()
		var out bytes.Buffer
		opts := Options{Stdout: &out, Stderr: &out, Version: "store-default-test"}
		if err := runStore(context.Background(), opts, []string{"--repo", repo, "--cache-dir", cache}); err != nil {
			t.Fatal(err)
		}
		var d descriptor
		if err := json.Unmarshal(out.Bytes(), &d); err != nil {
			t.Fatalf("store output %q: %v", out.String(), err)
		}
		return d
	}
	index := func(t *testing.T, cache string) {
		t.Helper()
		var out bytes.Buffer
		opts := Options{Stdout: &out, Stderr: &out, Version: "store-default-test"}
		if err := runIndex(context.Background(), opts, []string{"--repo", repo, "--cache-dir", cache, "--format", "json"}); err != nil {
			t.Fatalf("index: %v %s", err, out.String())
		}
	}
	requireAvailable := func(t *testing.T, d descriptor) {
		t.Helper()
		if d.Unavailable || d.Path == "" {
			t.Fatalf("store unavailable without opt-in: %+v", d)
		}
		if _, err := os.Stat(d.Path); err != nil {
			t.Fatalf("store path does not exist: %v", err)
		}
	}

	t.Run("unset writes the store", func(t *testing.T) {
		os.Unsetenv("ENTIRE_GRAPH_SEARCH_STORE")
		cache := filepath.Join(t.TempDir(), "cache")
		index(t, cache)
		requireAvailable(t, describe(t, cache))
	})
	t.Run("zero opts out", func(t *testing.T) {
		t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "0")
		cache := filepath.Join(t.TempDir(), "cache")
		index(t, cache)
		if d := describe(t, cache); !d.Unavailable {
			t.Fatalf("ENTIRE_GRAPH_SEARCH_STORE=0 still wrote a store: %+v", d)
		}
	})
	t.Run("cold def --head writes the store", func(t *testing.T) {
		os.Unsetenv("ENTIRE_GRAPH_SEARCH_STORE")
		cache := filepath.Join(t.TempDir(), "cache")
		var out bytes.Buffer
		opts := Options{Stdout: &out, Stderr: &out, Version: "store-default-test"}
		if err := runDef(context.Background(), opts, []string{"--repo", repo, "--head", "--cache-dir", cache, "Render"}); err != nil {
			t.Fatalf("def: %v %s", err, out.String())
		}
		requireAvailable(t, describe(t, cache))
	})
}

// TestColdIndexSurfacesStoreWriteFailureAsWarning is the cache-MISS half of
// TestPreindexHitSurfacesStoreRepairFailureAsWarning: a cold build whose sqlite
// sidecar cannot be written (here a non-empty directory sits where it belongs,
// as a full disk would refuse it) still succeeds, and says so with a
// W_SEARCH_STORE_UNAVAILABLE warning in the JSON it prints (and, for explain,
// in its text too). Break caught: a cold-path sidecar write failure dropped
// silently, so `index` or a lookup reports a clean success while `graph store`
// says store_unavailable.
func TestColdIndexSurfacesStoreWriteFailureAsWarning(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "fixture@example.test")
	git("config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package sample\nfunc Render() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "main.go")
	git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
	const version = "store-write-failure-test"
	runWithStdin := func(t *testing.T, stdin string, verb func(context.Context, Options, []string) error, args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		options := Options{Stdout: &out, Stderr: &stderr, Stdin: strings.NewReader(stdin), Version: version}
		if err := verb(context.Background(), options, args); err != nil {
			t.Fatalf("a sidecar write failure failed the command: %v %s", err, stderr.String())
		}
		return out.Bytes()
	}
	run := func(t *testing.T, verb func(context.Context, Options, []string) error, args ...string) []byte {
		t.Helper()
		return runWithStdin(t, "", verb, args...)
	}
	// coldCacheWithBlockedSidecar returns a cache directory with no complete
	// entry for the fixture's tree and a non-empty directory where that
	// entry's sidecar will be written. An index run without the store first
	// names the entry, then the entry is removed so the next build is cold.
	coldCacheWithBlockedSidecar := func(t *testing.T) string {
		t.Helper()
		cache := filepath.Join(t.TempDir(), "cache")
		t.Setenv("ENTIRE_GRAPH_SEARCH_STORE", "0")
		run(t, runIndex, "--repo", repo, "--cache-dir", cache, "--profile", "full", "--format", "json")
		os.Unsetenv("ENTIRE_GRAPH_SEARCH_STORE")
		var entries []string
		if err := filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json.gz") {
				entries = append(entries, path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("want one complete entry, got %q", entries)
		}
		if err := os.Remove(entries[0]); err != nil {
			t.Fatal(err)
		}
		sidecar := strings.TrimSuffix(entries[0], ".json.gz") + ".sqlite"
		if err := os.MkdirAll(filepath.Join(sidecar, "blocker"), 0o700); err != nil {
			t.Fatal(err)
		}
		return cache
	}
	type response struct {
		CacheHit bool `json:"index_cache_hit"`
		Warnings []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Detail   string `json:"detail"`
		} `json:"warnings"`
	}
	requireWarning := func(t *testing.T, output []byte) {
		t.Helper()
		var got response
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatalf("output %q: %v", output, err)
		}
		if got.CacheHit {
			t.Fatalf("the build was not cold: %s", output)
		}
		for _, warning := range got.Warnings {
			if warning.Code == "W_SEARCH_STORE_UNAVAILABLE" && warning.Severity == "warning" && warning.Detail != "" {
				return
			}
		}
		t.Fatalf("sidecar write failure not surfaced as a W_SEARCH_STORE_UNAVAILABLE warning: %s", output)
	}

	t.Run("index", func(t *testing.T) {
		cache := coldCacheWithBlockedSidecar(t)
		requireWarning(t, run(t, runIndex, "--repo", repo, "--cache-dir", cache, "--profile", "full", "--format", "json"))
	})
	t.Run("cold def --head", func(t *testing.T) {
		cache := coldCacheWithBlockedSidecar(t)
		requireWarning(t, run(t, runDef, "--repo", repo, "--head", "--cache-dir", cache, "--profile", "full", "--format", "json", "Render"))
	})
	// explain reads the build error from stdin and prints no other warnings,
	// so its text form must carry this one too.
	const buildError = "main.go:2:1: undefined: Render\n"
	explainArgs := func(cache, format string) []string {
		return []string{"--repo", repo, "--head", "--cache-dir", cache, "--profile", "full", "--no-echo", "--format", format}
	}
	t.Run("cold explain --head", func(t *testing.T) {
		cache := coldCacheWithBlockedSidecar(t)
		requireWarning(t, runWithStdin(t, buildError, runExplain, explainArgs(cache, "json")...))
	})
	t.Run("cold explain --head text", func(t *testing.T) {
		cache := coldCacheWithBlockedSidecar(t)
		output := string(runWithStdin(t, buildError, runExplain, explainArgs(cache, "text")...))
		if !strings.Contains(output, "\n- warning W_SEARCH_STORE_UNAVAILABLE\n") {
			t.Fatalf("explain text does not carry the store warning:\n%s", output)
		}
	})
}
