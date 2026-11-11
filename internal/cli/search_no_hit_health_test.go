package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

// A query that matches nothing, with no graph built or cached, still states a
// health status rather than leaving completeness.health unset.
func TestNoHitQueryWithoutAGraphReportsHealth(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "No Hit Health Test")
	git(t, repo, "config", "user.email", "no-hit-health@example.invalid")
	for index := 0; index < 40; index++ {
		write(t, repo, fmt.Sprintf("pkg/file%02d.go", index), fmt.Sprintf("package pkg\n\nfunc Thing%02d() {}\n", index))
	}
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")

	var stdout, stderr bytes.Buffer
	cacheDir := t.TempDir()
	err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: cacheDir},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}, []string{"query", "--repo", repo, "--head", "--cache-dir", cacheDir, "--format", "json", "--query", "zzzunmatchedneedlezzz"})
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var response sem.SearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(response.Results) != 0 {
		t.Fatalf("the needle matched: %+v", response.Results)
	}
	if response.Completeness.Health.Status != "ok" {
		t.Fatalf("health = %+v, want an explicit ok", response.Completeness.Health)
	}
}
