package agentsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func mkrepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The reported shape: entire-brain cloned INSIDE a project, so the clone
// carries its own .git. Context walks to the nearest one, so init-agents run
// from the clone writes the guide there — invisible to the project, with no
// error. OuterRepo is what lets the command say so.
func TestOuterRepoFindsTheProjectAroundANestedClone(t *testing.T) {
	project := mkrepo(t, t.TempDir())
	clone := mkrepo(t, filepath.Join(project, "entire-brain"))

	if got := OuterRepo(clone); got != project {
		t.Fatalf("OuterRepo(%s) = %q, want the project root %q", clone, got, project)
	}
}

// The ordinary case must stay silent, or the warning becomes noise everyone
// learns to ignore.
func TestOuterRepoIsEmptyForATopLevelRepo(t *testing.T) {
	// A repo whose ancestors hold no .git.
	parent := t.TempDir()
	repo := mkrepo(t, filepath.Join(parent, "project"))

	if got := OuterRepo(repo); got != "" {
		t.Fatalf("a top-level repository must report no outer repo, got %q", got)
	}
	if got := OuterRepo(""); got != "" {
		t.Fatalf("an empty root must report no outer repo, got %q", got)
	}
}

// A worktree or submodule stores .git as a FILE, not a directory. Those are
// still repositories and still nest, so the check uses Lstat and must not care
// which it is.
func TestOuterRepoDetectsAGitFileNotJustADirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := mkrepo(t, filepath.Join(project, "nested"))

	if got := OuterRepo(inner); got != project {
		t.Fatalf("a .git FILE marks a repository too; OuterRepo = %q, want %q", got, project)
	}
}

// OuterRepo walks UP for an ancestor .git and never checked the starting
// point, so a plain directory inside a repository came back with that ancestor
// and the caller announced "<root> is a git repository inside <outer>" about a
// path that is not a repository at all -- suggesting a remedy that addresses
// nothing.
//
// This fires on an ordinary layout, not a contrived one: a monorepo
// subdirectory, or anything under a dotfiles-tracked home directory.
func TestOuterRepoRequiresAnInnerRepository(t *testing.T) {
	t.Parallel()

	outer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A PLAIN directory inside the repo: there is no nesting to report.
	plain := filepath.Join(outer, "packages", "web")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := OuterRepo(plain); got != "" {
		t.Errorf("a plain subdirectory is not a nested repository, but OuterRepo returned %q", got)
	}

	// A real nested clone still reports, or the fix would disable the feature.
	inner := filepath.Join(outer, "entire-brain")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := OuterRepo(inner); got != outer {
		t.Errorf("a genuine nested clone must still be reported: got %q, want %q", got, outer)
	}

	// A .git FILE is how a worktree marks its root, so it counts too.
	wt := filepath.Join(outer, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := OuterRepo(wt); got != outer {
		t.Errorf("a worktree root carries .git as a FILE and must still be reported: got %q", got)
	}

	// And a repository with no outer repository reports nothing.
	lone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(lone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := OuterRepo(lone); got != "" {
		t.Errorf("a top-level repository has no outer repository, got %q", got)
	}
}
