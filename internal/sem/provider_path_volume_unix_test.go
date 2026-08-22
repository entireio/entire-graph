//go:build !windows

package sem

import (
	"errors"
	"os"
	"path/filepath"
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
