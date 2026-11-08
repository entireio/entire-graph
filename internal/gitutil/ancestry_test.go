package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func ancestryGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ancestryWrite(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ancestryRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	ancestryGit(t, repo, "init", "-q")
	ancestryGit(t, repo, "config", "user.name", "Ancestry Test")
	ancestryGit(t, repo, "config", "user.email", "ancestry@example.invalid")
	return repo
}

func ancestryCommit(t *testing.T, repo, message string) {
	t.Helper()
	ancestryGit(t, repo, "add", "-A")
	ancestryGit(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", message)
}

// The changed-path list is the ancestor fallback's whole safety argument, so
// it must be complete: a rename is both of its paths (rename detection would
// collapse it into the destination alone), a deletion is its old path, and a
// path with a newline survives -z intact. In a subdirectory checkout the
// list is limited to and relative to that scope, the coordinates the store's
// own file paths use.
func TestTreeChangedPathsListsBothRenameSidesDeletionsAndScope(t *testing.T) {
	repo := ancestryRepo(t)
	ancestryWrite(t, repo, "sub/legacy.go", "package sub\n\nfunc Legacy() int { return 1 }\n")
	ancestryWrite(t, repo, "sub/deep/doomed.go", "package deep\n\nfunc Doomed() {}\n")
	ancestryWrite(t, repo, "top.go", "package top\n")
	ancestryCommit(t, repo, "base")
	base := ancestryGit(t, repo, "rev-parse", "HEAD^{tree}")
	ancestryGit(t, repo, "mv", "sub/legacy.go", "sub/renamed.go")
	ancestryGit(t, repo, "rm", "-q", "sub/deep/doomed.go")
	ancestryWrite(t, repo, "top.go", "package top\n\nfunc Top() {}\n")
	// A newline in a path is the case -z exists for; Windows cannot name one.
	newline := runtime.GOOS != "windows"
	if newline {
		ancestryWrite(t, repo, "sub/new\nline.go", "package sub\n")
	}
	ancestryCommit(t, repo, "change")
	head := ancestryGit(t, repo, "rev-parse", "HEAD^{tree}")
	// The rename is detectable: plain `git diff` with renames on reports it as
	// one pair, which is exactly the collapse the list must not perform.
	if renames := ancestryGit(t, repo, "diff", "--find-renames", "--name-status", base, head); !strings.Contains(renames, "R100") {
		t.Fatalf("fixture rename is not detected as a rename, so it cannot pin both sides:\n%s", renames)
	}

	got, err := TreeChangedPaths(t.Context(), repo, base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sub/deep/doomed.go", "sub/legacy.go", "sub/new\nline.go", "sub/renamed.go", "top.go"}
	if !newline {
		want = slices.DeleteFunc(want, func(path string) bool { return strings.Contains(path, "\n") })
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %q, want %q", got, want)
	}

	scoped, err := TreeChangedPaths(t.Context(), filepath.Join(repo, "sub"), base, head, "sub/")
	if err != nil {
		t.Fatal(err)
	}
	wantScoped := []string{"deep/doomed.go", "legacy.go", "new\nline.go", "renamed.go"}
	if !newline {
		wantScoped = slices.DeleteFunc(wantScoped, func(path string) bool { return strings.Contains(path, "\n") })
	}
	if !reflect.DeepEqual(scoped, wantScoped) {
		t.Fatalf("scoped changed paths = %q, want %q", scoped, wantScoped)
	}

	for _, bad := range [][2]string{{"HEAD", head}, {base, "--output=x"}, {base[:12], head}} {
		if _, err := TreeChangedPaths(t.Context(), repo, bad[0], bad[1], ""); err == nil {
			t.Fatalf("accepted non-object tree names %q", bad)
		}
	}
}

// A tree the comparison cannot read is a failure, never a shorter list. The
// fixture removes the old side of a changed subtree, which is what a partial
// clone without that tree looks like once lazy fetching is forbidden.
func TestTreeChangedPathsFailsOnAMissingTreeObject(t *testing.T) {
	repo := ancestryRepo(t)
	ancestryWrite(t, repo, "aaa/first.go", "package aaa\n")
	ancestryWrite(t, repo, "mmm/second.go", "package mmm\n")
	ancestryCommit(t, repo, "base")
	base := ancestryGit(t, repo, "rev-parse", "HEAD^{tree}")
	missing := ancestryGit(t, repo, "rev-parse", "HEAD:mmm")
	ancestryWrite(t, repo, "aaa/first.go", "package aaa\n\nfunc First() {}\n")
	ancestryWrite(t, repo, "mmm/second.go", "package mmm\n\nfunc Second() {}\n")
	ancestryCommit(t, repo, "change")
	head := ancestryGit(t, repo, "rev-parse", "HEAD^{tree}")
	if got, err := TreeChangedPaths(t.Context(), repo, base, head, ""); err != nil || len(got) != 2 {
		t.Fatalf("control: changed paths = %q, %v", got, err)
	}

	objectPath := filepath.Join(repo, ".git", "objects", missing[:2], missing[2:])
	// Git writes objects read-only, which Windows refuses to delete.
	if err := os.Chmod(objectPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove loose tree object: %v", err)
	}
	got, err := TreeChangedPaths(t.Context(), repo, base, head, "")
	if err == nil {
		t.Fatalf("missing tree object produced a list instead of an error: %q", got)
	}
	if got != nil {
		t.Fatalf("failed comparison still returned paths: %q", got)
	}
}

// The walk follows first parents only: a merged side branch's commits are not
// ancestors the fallback may serve from.
func TestFirstParentCommitTreesFollowsFirstParentsOnly(t *testing.T) {
	repo := ancestryRepo(t)
	ancestryWrite(t, repo, "a.go", "package a\n")
	ancestryCommit(t, repo, "root")
	root := ancestryGit(t, repo, "rev-parse", "HEAD")
	ancestryGit(t, repo, "checkout", "-q", "-b", "side")
	ancestryWrite(t, repo, "side.go", "package a\n")
	ancestryCommit(t, repo, "side")
	side := ancestryGit(t, repo, "rev-parse", "HEAD")
	ancestryGit(t, repo, "checkout", "-q", "-")
	ancestryWrite(t, repo, "main.go", "package a\n")
	ancestryCommit(t, repo, "main")
	mainline := ancestryGit(t, repo, "rev-parse", "HEAD")
	ancestryGit(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "merge", "-q", "--no-ff", "--no-edit", "side")
	merge := ancestryGit(t, repo, "rev-parse", "HEAD")

	chain, err := FirstParentCommitTrees(t.Context(), repo, merge, 10)
	if err != nil {
		t.Fatal(err)
	}
	var commits []string
	for _, entry := range chain {
		commits = append(commits, entry.Commit)
		if want := ancestryGit(t, repo, "rev-parse", entry.Commit+"^{tree}"); entry.Tree != want {
			t.Fatalf("commit %s tree = %s, want %s", entry.Commit, entry.Tree, want)
		}
	}
	if want := []string{merge, mainline, root}; !reflect.DeepEqual(commits, want) {
		t.Fatalf("first-parent chain = %q, want %q (side commit %s must not appear)", commits, want, side)
	}
	if bounded, err := FirstParentCommitTrees(t.Context(), repo, merge, 2); err != nil || len(bounded) != 2 {
		t.Fatalf("count 2 returned %d entries, %v", len(bounded), err)
	}
	if _, err := FirstParentCommitTrees(t.Context(), repo, "HEAD", 2); err == nil {
		t.Fatal("accepted a ref name instead of a full commit object name")
	}
}
