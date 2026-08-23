//go:build darwin || dragonfly || freebsd || openbsd

package sem

import (
	"errors"
	"os"
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
	} else if !errors.Is(err, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("mount refusal error = %v, want %v", err, errGitWorktreeFallbackUnsafe)
	}
}

func TestGitWorktreePreflightNestedMarkerCannotMaskLaterKnownMount(t *testing.T) {
	repo := t.TempDir()
	markerDir := filepath.Join(repo, "marker")
	if err := os.Mkdir(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, ".git"), []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, resolvedRepo, err := newPathTraversalAnchor(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRepo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	const mount = "later-known-mount"
	root.mounts.addMountPoint(filepath.Join(resolvedRepo, mount))
	var budget gitWorktreePreflightBudget
	if !budget.admitDirectory("marker") || !budget.admitDirectory(mount) {
		t.Fatal("failed to admit the synthetic preflight seeds")
	}
	err = gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &budget, []string{"marker", mount},
	)
	if !errors.Is(err, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("marker-before-mount error = %v, want %v", err, errGitWorktreeFallbackUnsafe)
	}
}
