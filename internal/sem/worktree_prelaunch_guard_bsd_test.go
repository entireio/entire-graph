//go:build darwin || dragonfly || freebsd || openbsd

package sem

import (
	"path/filepath"
	"testing"
)

func TestGitWorktreePreflightChecksMountBeforeChildLookup(t *testing.T) {
	repo := t.TempDir()
	anchor, resolvedRepo, err := newPathTraversalAnchor(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	const missing = "known-mount-that-does-not-exist"
	root.mounts.addMountPoint(filepath.Join(resolvedRepo, missing))
	var budget gitWorktreePreflightBudget
	if !budget.admitDirectory(missing) {
		t.Fatal("failed to admit the synthetic preflight seed")
	}
	if err := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &budget, []string{missing},
	); err == nil {
		t.Fatal("worktree preflight looked through a known mount point")
	}
}
