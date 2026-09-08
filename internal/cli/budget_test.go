package cli

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// budgetFailureCode is the wire-visible partial-failure code a truncated
// snapshot carries. Spelled out rather than referenced through the sem
// constant: this is the string a consumer greps for in the NDJSON.
const budgetFailureCode = "E_ANALYSIS_BUDGET_EXCEEDED"

// nestedRelationBombRepo builds a repository whose single file makes the
// relation phase quadratic. Each of the 500 nested function declarations
// textually contains every inner declaration's call sites and URL literals, so
// the phase rescans the same bytes once per enclosing symbol: ~52 KB of source
// produces ~386,000 relations and takes tens of seconds. Without a wall-clock
// ceiling there is nothing that stops it.
func nestedRelationBombRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	var head, tail strings.Builder
	for i := range 500 {
		fmt.Fprintf(&head, "function nestedFn%d(a, b) {\n  const x = fetch(\"https://example.com/v%d/items\");\n  helper%d(a, b);\n", i, i, i)
		tail.WriteString("}\n")
	}
	write(t, repo, "deep.js", head.String()+tail.String())
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "nested")
	return repo
}

// TestSymbolsMaxSecondsBoundsRunawayIndex is the end-to-end shape of the
// finding: before this change `symbols` rejected --max-seconds outright, so a
// caller had no way to bound an index at all and the run above took ~37s on the
// machine this was measured on.
func TestSymbolsMaxSecondsBoundsRunawayIndex(t *testing.T) {
	repo := nestedRelationBombRepo(t)

	var stdout, stderr bytes.Buffer
	started := time.Now()
	err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &stdout,
		Stderr: &stderr,
	}, []string{"symbols", "--repo", repo, "--format", "ndjson", "--max-seconds", "1", "--no-cache"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("symbols --max-seconds must bound the run, not fail it: %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), budgetFailureCode) {
		t.Fatalf("a truncated index must report %s in its summary; stdout tail %q", budgetFailureCode, tail(stdout.String()))
	}
	// Loose on purpose: this pins "a ceiling exists", not the host's speed.
	if elapsed > 20*time.Second {
		t.Fatalf("--max-seconds 1 did not bound the run: took %s", elapsed)
	}
}

// TestSymbolsBudgetTruncatedIndexIsNotCached pins that a truncated graph never
// becomes the cached answer for the tree. The record cache key deliberately
// does not include the budget, so a stored truncation would be handed to every
// later caller as if it were the complete index.
func TestSymbolsBudgetTruncatedIndexIsNotCached(t *testing.T) {
	repo := nestedRelationBombRepo(t)
	cacheDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if err := Run(t.Context(), Options{
		Env:    EntireEnv{RepoRoot: repo},
		Stdout: &stdout,
		Stderr: &stderr,
	}, []string{"symbols", "--repo", repo, "--format", "ndjson", "--max-seconds", "1", "--cache-dir", cacheDir}); err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if !strings.Contains(stdout.String(), budgetFailureCode) {
		t.Fatalf("expected a truncated index; stdout tail %q", tail(stdout.String()))
	}
	if !strings.Contains(stderr.String(), "was not cached") {
		t.Fatalf("a truncated index must say so on stderr, got %q", stderr.String())
	}
	if entries := cacheFileCount(t, cacheDir); entries != 0 {
		t.Fatalf("a budget-truncated index must not be cached, found %d cache file(s)", entries)
	}
}

// TestProviderMaxSecondsFlagValidation pins --max-seconds parsing on the bulk
// provider commands, matching the diff/commit rules: non-numeric and negative
// are rejected, 0 means unlimited.
func TestProviderMaxSecondsFlagValidation(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.js", "function validateToken(token) {\n  return Boolean(token);\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	run := func(args ...string) error {
		var stdout, stderr bytes.Buffer
		return Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &stdout, Stderr: &stderr}, args)
	}
	base := []string{"symbols", "--repo", repo, "--format", "ndjson", "--no-cache"}
	if err := run(append(append([]string{}, base...), "--max-seconds", "abc")...); err == nil {
		t.Fatal("non-numeric --max-seconds must be rejected")
	}
	if err := run(append(append([]string{}, base...), "--max-seconds", "-5")...); err == nil {
		t.Fatal("negative --max-seconds must be rejected")
	}
	if err := run(append(append([]string{}, base...), "--max-seconds")...); err == nil {
		t.Fatal("--max-seconds without a value must be rejected")
	}
	if err := run(append(append([]string{}, base...), "--max-seconds", "0")...); err != nil {
		t.Fatalf("--max-seconds 0 (unlimited) must be accepted, got %v", err)
	}
}

func cacheFileCount(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			count += cacheFileCount(t, dir+string(os.PathSeparator)+entry.Name())
			continue
		}
		count++
	}
	return count
}

func tail(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[len(s)-400:]
}
