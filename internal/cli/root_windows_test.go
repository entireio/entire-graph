//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveRepoDoesNotProbeUNCCeiling(t *testing.T) {
	boundary := t.TempDir()
	repo := filepath.Join(boundary, "repo")
	child := filepath.Join(repo, "nested")
	for _, dir := range []string{filepath.Join(repo, ".git"), child} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(child)
	t.Setenv("PATH", t.TempDir())
	// TEST-NET must remain inert text. Git would canonicalize every ceiling
	// entry and can therefore make an SMB connection before repository lookup.
	t.Setenv("GIT_CEILING_DIRECTORIES", `\\192.0.2.1\entire-graph\ceiling;`+boundary)

	type result struct {
		repo string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := resolveRepo(t.Context(), EntireEnv{}, "")
		done <- result{repo: got, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.repo != repo {
			t.Fatalf("resolveRepo = (%q, %v), want (%q, nil)", got.repo, got.err, repo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repository discovery did not return promptly; UNC ceiling must not be probed")
	}
}
