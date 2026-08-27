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
