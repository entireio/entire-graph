//go:build !windows

package sem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestGitMetadataGuardRejectsBareCrossFilesystemObjectStore(t *testing.T) {
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
	if err := os.Mkdir(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev", filepath.Join(repo, "objects")); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("bare cross-filesystem objects redirect passed the pre-subprocess metadata guard")
	}
}

func TestGitMetadataGuardRejectsCrossFilesystemAlternateObjectStore(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"), []byte("/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("cross-filesystem alternate object store passed the pre-subprocess metadata guard")
	}
}

func TestGitMetadataGuardRejectsRecursiveCrossFilesystemAlternateObjectStore(t *testing.T) {
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
	primaryObjects := filepath.Join(gitDir, "objects")
	alternateObjects := filepath.Join(repo, "alternate-objects")
	if err := os.MkdirAll(filepath.Join(primaryObjects, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(alternateObjects, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(primaryObjects, "info", "alternates"), []byte("../../alternate-objects\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alternateObjects, "info", "alternates"), []byte("/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("recursive cross-filesystem alternate object store passed the pre-subprocess metadata guard")
	}
}

func TestGitMetadataGuardChecksTheSixthRecursiveAlternateFile(t *testing.T) {
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
	primaryObjects := filepath.Join(gitDir, "objects")
	if err := os.MkdirAll(filepath.Join(primaryObjects, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	objects := []string{primaryObjects}
	for index := 1; index <= 6; index++ {
		next := filepath.Join(repo, fmt.Sprintf("alternate-%d", index))
		if err := os.MkdirAll(filepath.Join(next, "info"), 0o755); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, next)
	}
	for index := 0; index < len(objects)-1; index++ {
		if err := os.WriteFile(filepath.Join(objects[index], "info", "alternates"), []byte(filepath.ToSlash(objects[index+1])+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(objects[len(objects)-1], "info", "alternates"), []byte("/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("sixth recursive alternate file escaped the pre-subprocess metadata guard")
	}
}

func TestGitMetadataGuardRejectsBlockingSpecialMetadataFiles(t *testing.T) {
	for _, relative := range []string{"config", filepath.Join("objects", "info", "alternates")} {
		t.Run(relative, func(t *testing.T) {
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(gitDir, relative), 0o600); err != nil {
				t.Fatal(err)
			}

			done := make(chan bool, 1)
			go func() { done <- gitMetadataSafeForSubprocess(repo) }()
			select {
			case safe := <-done:
				if safe {
					t.Fatal("blocking special metadata file passed the pre-subprocess guard")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("metadata guard blocked while inspecting a special file")
			}
		})
	}
}

func TestGitMetadataGuardRejectsBlockingBareHEAD(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(repo, "HEAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- gitMetadataSafeForSubprocess(repo) }()
	select {
	case safe := <-done:
		if safe {
			t.Fatal("blocking bare HEAD passed the pre-subprocess guard")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("metadata guard blocked while inspecting a bare HEAD")
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
