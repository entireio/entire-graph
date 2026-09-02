package sem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoKeyNormalizesRelativeRepoRoot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "relative-root")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	if got, want := RepoKey(t.Context(), "."), RepoKey(t.Context(), repo); got != want {
		t.Fatalf("RepoKey(.) = %q, RepoKey(%q) = %q", got, repo, want)
	}
	if got, want := RepoKey(t.Context(), "."), "local/relative-root"; got != want {
		t.Fatalf("RepoKey(.) = %q, want %q", got, want)
	}
}
