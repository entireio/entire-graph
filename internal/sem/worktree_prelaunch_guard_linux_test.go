//go:build linux

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// A directory that is itself a mount point is never opened. It still declines
// Git enumeration, whose recursive walk would cross into it, but it is reported
// for the filesystem fallback to prune instead of failing the whole worktree
// closed (entireio/entire-graph#393: a sandbox's read-only bind mount inside
// the checkout made every working-tree query fail).
func TestGitWorktreePreflightSkipsMountPointBeforeReadingChildren(t *testing.T) {
	anchor, resolvedRoot, err := newPathTraversalAnchor("/", "/")
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var budget gitWorktreePreflightBudget
	if !budget.admitDirectory("proc") {
		t.Fatal("failed to admit the synthetic preflight seed")
	}
	mounts, err := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &budget, []string{"proc"},
	)
	if err == nil {
		t.Fatal("worktree preflight let Git enumerate a tree containing the /proc mount point")
	}
	if errors.Is(err, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("mount point error = %v, want a decline the filesystem fallback can honour", err)
	}
	if !slices.Equal(mounts, []string{"proc"}) {
		t.Fatalf("skipped mount points = %q, want [proc]", mounts)
	}
	if budget.directories != 1 {
		t.Fatalf("admitted directories = %d, want only the seed: the mount point's children were read", budget.directories)
	}
}

func TestGitWorktreePreflightNestedMarkerCannotMaskLaterMount(t *testing.T) {
	markerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(markerDir, ".git"), []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerRel, err := filepath.Rel("/", markerDir)
	if err != nil {
		t.Fatal(err)
	}
	anchor, resolvedRoot, err := newPathTraversalAnchor("/", "/")
	if err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Stat(markerDir)
	if err != nil {
		t.Fatal(err)
	}
	if !anchor.allows(markerInfo) {
		t.Skip("temporary directory is on a separate mount from the synthetic root anchor")
	}
	root, err := newSweepDirectoryRoot(resolvedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var markerOnlyBudget gitWorktreePreflightBudget
	if !markerOnlyBudget.admitDirectory(markerRel) {
		t.Fatal("failed to admit the nested-marker seed")
	}
	markerMounts, markerErr := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &markerOnlyBudget, []string{markerRel},
	)
	if markerErr == nil || errors.Is(markerErr, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("nested marker error = %v, want ordinary filesystem fallback", markerErr)
	}
	if len(markerMounts) != 0 {
		t.Fatalf("nested marker reported mount points %q, want none", markerMounts)
	}

	// The marker already declined Git enumeration, but the traversal has to go
	// on: the fallback is safe only if it learns about the mount point after it.
	var combinedBudget gitWorktreePreflightBudget
	if !combinedBudget.admitDirectory(markerRel) || !combinedBudget.admitDirectory("proc") {
		t.Fatal("failed to admit the combined preflight seeds")
	}
	combinedMounts, combinedErr := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &combinedBudget, []string{markerRel, "proc"},
	)
	if combinedErr == nil || errors.Is(combinedErr, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("marker-before-mount error = %v, want ordinary filesystem fallback", combinedErr)
	}
	if !slices.Equal(combinedMounts, []string{"proc"}) {
		t.Fatalf("marker-before-mount skipped mount points = %q, want [proc]", combinedMounts)
	}
}

func TestLinuxSweepMountInventoryFailureIsFallbackUnsafe(t *testing.T) {
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
	root.mountOnce.Do(func() {
		root.mountErr = errors.New("synthetic mount inventory failure")
	})

	_, err = root.openWithoutOpenat2(anchor, []string{"child"}, func() bool { return true })
	if !errors.Is(err, errSymlinkChainOffVolume) {
		t.Fatalf("mount inventory error = %v, want %v", err, errSymlinkChainOffVolume)
	}
}

func TestLinuxOpenat2SeccompDenialSelectsOnlyTheGuardedFallback(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "child"), 0o755); err != nil {
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

	opened, err := root.openAfterOpenat2Failure(anchor, []string{"child"}, func() bool { return true }, syscall.EPERM)
	if err != nil {
		t.Fatalf("guarded fallback after openat2 EPERM: %v", err)
	}
	if opened == nil {
		t.Fatal("guarded fallback after openat2 EPERM returned no directory")
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if linuxSweepOpenCanSkipLocalPathFailure(syscall.EPERM) {
		t.Fatal("EPERM must not directly select the ordinary filesystem fallback")
	}
}

func TestLinuxOpenat2SeccompFallbackRetainsMountInventoryFailure(t *testing.T) {
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
	inventoryErr := errors.New("synthetic mount inventory failure")
	root.mountOnce.Do(func() {
		root.mountErr = inventoryErr
	})

	opened, err := root.openAfterOpenat2Failure(anchor, []string{"child"}, func() bool { return true }, syscall.EPERM)
	if opened != nil {
		_ = opened.Close()
		t.Fatal("guarded fallback returned a directory after mount inventory failure")
	}
	if !errors.Is(err, errSymlinkChainOffVolume) || !errors.Is(err, inventoryErr) {
		t.Fatalf("guarded fallback error = %v, want both %v and %v", err, errSymlinkChainOffVolume, inventoryErr)
	}
}
