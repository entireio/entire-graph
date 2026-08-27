package sem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCommittedAndWorktreeAgreeOnSymlinks pins the invariant the two listings
// used to break: one repository, one answer, whether or not --worktree is set.
//
// A tracked symlink is a blob whose content is its target path. The committed
// listing used to emit a file record for it, so the string "real.go" was handed
// to a parser as though it were source, while the working-tree listing refused
// the same entry. That divergence also meant a repository's graph changed shape
// depending on which mode produced it.
func TestCommittedAndWorktreeAgreeOnSymlinks(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--quiet", "-b", "trunk")
	git("config", "user.name", "Entire Graph Test")
	git("config", "user.email", "graph@example.com")
	git("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "real.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.go", filepath.Join(repo, "link.go")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	git("add", "-A")
	git("commit", "-m", "fixture")

	paths := func(worktree bool) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		err := StreamSnapshot(context.Background(), repo, "test", ProviderSnapshotOptions{Worktree: worktree}, func(record any) error {
			if file, ok := record.(FileRecord); ok {
				seen[file.Path] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("StreamSnapshot(worktree=%v): %v", worktree, err)
		}
		return seen
	}
	committed, worktree := paths(false), paths(true)
	if committed["link.go"] {
		t.Error("committed snapshot listed a symlink as a source file")
	}
	if worktree["link.go"] {
		t.Error("worktree snapshot listed a symlink as a source file")
	}
	if !committed["real.go"] || !worktree["real.go"] {
		t.Errorf("the regular file went missing: committed=%v worktree=%v", committed, worktree)
	}
	if len(committed) != len(worktree) {
		t.Errorf("committed and worktree disagree: %v vs %v", committed, worktree)
	}
}

// TestWorktreeExcludesIndexSymlinkWithoutCoreSymlinks covers the same invariant
// on the configuration where lstat cannot see it.
//
// With core.symlinks=false -- the default wherever the filesystem cannot make
// symlinks, and common on Windows -- Git writes a tracked mode-120000 entry to
// disk as an ordinary file whose contents are the link target. lstat reports a
// regular file, so a worktree listing that trusts lstat admits the symlink as
// source while the committed listing excludes it by mode. That is the same
// divergence this file exists to prevent, arriving from the other side.
func TestWorktreeExcludesIndexSymlinkWithoutCoreSymlinks(t *testing.T) {
	origin := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(origin, "init", "--quiet", "-b", "trunk")
	git(origin, "config", "user.name", "Entire Graph Test")
	git(origin, "config", "user.email", "graph@example.com")
	git(origin, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(origin, "real.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.go", filepath.Join(origin, "link.go")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	git(origin, "add", "-A")
	git(origin, "commit", "-m", "fixture")

	// Clone with symlinks disabled: link.go lands as a regular file holding
	// "real.go", while the index still records mode 120000.
	checkout := filepath.Join(t.TempDir(), "checkout")
	git(t.TempDir(), "-c", "core.symlinks=false", "clone", "--quiet", origin, checkout)
	info, err := os.Lstat(filepath.Join(checkout, "link.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Skipf("this platform materialized the symlink anyway (%v); the case under test cannot arise", info.Mode())
	}

	paths := func(worktree bool) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		err := StreamSnapshot(context.Background(), checkout, "test", ProviderSnapshotOptions{Worktree: worktree}, func(record any) error {
			if file, ok := record.(FileRecord); ok {
				seen[file.Path] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("StreamSnapshot(worktree=%v): %v", worktree, err)
		}
		return seen
	}
	committed, worktree := paths(false), paths(true)
	if worktree["link.go"] {
		t.Error("worktree listed an index symlink materialized as a regular file")
	}
	if committed["link.go"] {
		t.Error("committed listed a symlink")
	}
	if !committed["real.go"] || !worktree["real.go"] {
		t.Errorf("the regular file went missing: committed=%v worktree=%v", committed, worktree)
	}
}
