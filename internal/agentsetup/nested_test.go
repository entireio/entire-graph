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
	// A repo whose ancestors hold no .git. A temp dir alone does not promise
	// that: on a machine whose home folder is a git repository, the temp dir
	// is inside one. Fence the walk at the test's own tree.
	parent := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(parent))
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
	// Not parallel: the last case sets GIT_CEILING_DIRECTORIES, and t.Setenv
	// is unavailable to a parallel test.
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

	// And a repository with no outer repository reports nothing -- once the
	// walk is fenced at the test's own tree, since the temp dir itself may sit
	// inside a repository (a home folder under version control).
	lone := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(lone))
	if err := os.MkdirAll(filepath.Join(lone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := OuterRepo(lone); got != "" {
		t.Errorf("a top-level repository has no outer repository, got %q", got)
	}
}

// TestOuterRepoHonoursGitCeilingDirectories: the nested-repository warning
// must not name a repository git itself would refuse to see. Without the
// ceiling, a home folder under version control is the "outer repository" of
// every clone on the machine.
func TestOuterRepoHonoursGitCeilingDirectories(t *testing.T) {
	outer := mkrepo(t, t.TempDir())
	inner := mkrepo(t, filepath.Join(outer, "clone"))
	nestedDeeper := mkrepo(t, filepath.Join(outer, "a", "b", "clone"))

	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	if got := OuterRepo(inner); got != outer {
		t.Fatalf("without a ceiling the outer repository must be found: got %q, want %q", got, outer)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", outer)
	if got := OuterRepo(inner); got != "" {
		t.Fatalf("a ceiling at the outer repository must hide it: got %q", got)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(outer, "a"))
	if got := OuterRepo(nestedDeeper); got != "" {
		t.Fatalf("a ceiling between the clone and the outer repository must stop the walk: got %q", got)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(outer, "a", "b", "clone", "unrelated"))
	if got := OuterRepo(nestedDeeper); got != outer {
		t.Fatalf("a ceiling below the start must change nothing: got %q, want %q", got, outer)
	}
}
