package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A Go file that go/parser accepts but the bundled tree-sitter grammar recovers
// from errors is reported as W_GO_GRAMMAR_GAP, not as an E_PARSE_ERROR partial
// failure. The warning must still reach every reader: the JSON payload and the
// agent coverage lines of both search and the relation verbs.
func TestGoGrammarGapStaysVisible(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Grammar Gap Test")
	git(t, repo, "config", "user.email", "grammar-gap@example.invalid")
	write(t, repo, "lib.go", "package demo\n\nfunc Helper() int { return 2 }\n")
	write(t, repo, "demo.go", "package demo\n\nfunc AfterNew() int {\n\tp := new(0)\n\t_ = p\n\treturn Helper()\n}\n")
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
	cacheDir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		err := Run(t.Context(), Options{
			Version: "test-version",
			Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
			Stdout:  &stdout,
			Stderr:  &stderr,
		}, args)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr.String())
		}
		return stdout.String()
	}

	raw := run("query", "--repo", repo, "--head", "--format", "json", "--cache-dir", cacheDir, "--query", "AfterNew")
	var response sem.SearchResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(response.PartialFailures) != 0 {
		t.Fatalf("valid Go reported as a partial failure: %+v", response.PartialFailures)
	}
	gap := false
	for _, warning := range response.Warnings {
		gap = gap || (warning.Code == sem.GoGrammarGapCode && warning.FilePath == "demo.go")
	}
	if !gap {
		t.Fatalf("JSON lost the grammar-gap warning: %+v", response.Warnings)
	}

	agent := run("query", "--repo", repo, "--head", "--format", "agent", "--cache-dir", cacheDir, "--query", "AfterNew")
	if !strings.Contains(agent, "Coverage: notice") || !strings.Contains(agent, "warning W_GO_GRAMMAR_GAP: demo.go") {
		t.Fatalf("query agent output lost the grammar-gap warning:\n%s", agent)
	}

	neighbors := run("neighbors", "--repo", repo, "--head", "--format", "agent", "--cache-dir", cacheDir, "--symbol", "Helper", "--direction", "in")
	if !strings.Contains(neighbors, "W_GO_GRAMMAR_GAP") || strings.Contains(neighbors, "E_PARSE_ERROR") {
		t.Fatalf("neighbors agent output:\n%s", neighbors)
	}
	if !strings.Contains(neighbors, "AfterNew") {
		t.Fatalf("the caller beside the grammar gap was lost:\n%s", neighbors)
	}
}
