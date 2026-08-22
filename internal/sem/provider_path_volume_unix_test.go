//go:build !windows

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeStatThroughSymlinksRejectsACrossFilesystemTarget(t *testing.T) {
	repo := t.TempDir()
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	devInfo, err := os.Stat("/dev")
	if err != nil {
		t.Fatal(err)
	}
	repoDevice, repoOK := fileSystemDevice(repoInfo)
	devDevice, devOK := fileSystemDevice(devInfo)
	if !repoOK || !devOK {
		t.Fatal("platform did not expose filesystem device identities")
	}
	if repoDevice == devDevice {
		t.Skip("/dev is not a distinct filesystem on this host")
	}
	link := filepath.Join(repo, "external")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatal(err)
	}
	if _, err := safeStatThroughSymlinks(repo, link); !errors.Is(err, errSymlinkChainOffVolume) {
		t.Fatalf("safeStatThroughSymlinks cross-filesystem error = %v, want %v", err, errSymlinkChainOffVolume)
	}
}

func TestGitMetadataGuardRejectsCrossFilesystemObjectStore(t *testing.T) {
	repo := t.TempDir()
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	devInfo, err := os.Stat("/dev")
	if err != nil {
		t.Fatal(err)
	}
	repoDevice, repoOK := fileSystemDevice(repoInfo)
	devDevice, devOK := fileSystemDevice(devInfo)
	if !repoOK || !devOK {
		t.Fatal("platform did not expose filesystem device identities")
	}
	if repoDevice == devDevice {
		t.Skip("/dev is not a distinct filesystem on this host")
	}
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev", filepath.Join(gitDir, "objects")); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("cross-filesystem .git/objects redirect passed the pre-subprocess metadata guard")
	}
}

func TestGitDirPointerTargetRejectsAnInRepoLinkThatLeavesTheFilesystem(t *testing.T) {
	repo := t.TempDir()
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	devInfo, err := os.Stat("/dev")
	if err != nil {
		t.Fatal(err)
	}
	repoDevice, repoOK := fileSystemDevice(repoInfo)
	devDevice, devOK := fileSystemDevice(devInfo)
	if !repoOK || !devOK {
		t.Fatal("platform did not expose filesystem device identities")
	}
	if repoDevice == devDevice {
		t.Skip("/dev is not a distinct filesystem on this host")
	}
	writeFile(t, repo, ".git", "gitdir: admin-link\n")
	if err := os.Symlink("/dev", filepath.Join(repo, "admin-link")); err != nil {
		t.Fatal(err)
	}
	if target, ok, hidden := gitDirPointerTarget(repo, ""); ok || hidden {
		t.Fatalf("gitDirPointerTarget = (%q, %v, hidden %v), want (_, false, false)", target, ok, hidden)
	}
}

func TestPrunedTreeSweepRefusesACrossFilesystemRedirectAndWarns(t *testing.T) {
	repo := t.TempDir()
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	devInfo, err := os.Stat("/dev")
	if err != nil {
		t.Fatal(err)
	}
	repoDevice, repoOK := fileSystemDevice(repoInfo)
	devDevice, devOK := fileSystemDevice(devInfo)
	if !repoOK || !devOK {
		t.Fatal("platform did not expose filesystem device identities")
	}
	if repoDevice == devDevice {
		t.Skip("/dev is not a distinct filesystem on this host")
	}
	if err := os.Symlink("/dev", filepath.Join(repo, "ignored")); err != nil {
		t.Fatal(err)
	}
	excluder := newGitDirExcluder(t.Context(), repo)
	excluder.observePrunedSubtree("ignored")
	if excluder.directoriesRead != 0 {
		t.Fatalf("directoriesRead = %d, want 0: the sweep followed the redirect", excluder.directoriesRead)
	}
	if excluder.hiddenEvidence == 0 {
		t.Fatal("cross-filesystem sweep refusal did not create fail-closed hidden evidence")
	}
	warnings := excluder.sweepUnreadableDirWarning()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Detail, "ignored") {
		t.Fatalf("warnings = %+v, want one warning naming ignored", warnings)
	}
}
