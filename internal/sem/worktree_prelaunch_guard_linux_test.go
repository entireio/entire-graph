//go:build linux

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGitWorktreePreflightRefusesMountBeforeReadingChildren(t *testing.T) {
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
	err = gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &budget, []string{"proc"},
	)
	if err == nil {
		t.Fatal("worktree preflight crossed the /proc mount boundary")
	}
	if !errors.Is(err, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("mount refusal error = %v, want %v", err, errGitWorktreeFallbackUnsafe)
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
	markerErr := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &markerOnlyBudget, []string{markerRel},
	)
	if markerErr == nil || errors.Is(markerErr, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("nested marker error = %v, want ordinary filesystem fallback", markerErr)
	}

	var combinedBudget gitWorktreePreflightBudget
	if !combinedBudget.admitDirectory(markerRel) || !combinedBudget.admitDirectory("proc") {
		t.Fatal("failed to admit the combined preflight seeds")
	}
	combinedErr := gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &combinedBudget, []string{markerRel, "proc"},
	)
	if !errors.Is(combinedErr, errGitWorktreeFallbackUnsafe) {
		t.Fatalf("marker-before-mount error = %v, want %v", combinedErr, errGitWorktreeFallbackUnsafe)
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

func TestLinuxOpenat2SeccompDenialIsNotAnOrdinaryPermissionFailure(t *testing.T) {
	if linuxSweepOpenCanSkipLocalPathFailure(syscall.EPERM) {
		t.Fatal("EPERM must not directly select fallback when a safety syscall may have been denied by policy")
	}
}
