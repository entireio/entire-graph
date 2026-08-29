//go:build linux || darwin || dragonfly || freebsd || openbsd

package sem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitMetadataConfigGuardChecksCoreWorktreeMountBeforeLookup(t *testing.T) {
	repo, gitDir := gitConfigPreflightFixture(t)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\nworktree = ../trap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver, err := newSameVolumePathResolver(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	trap := filepath.Join(repo, "trap")
	if _, err := os.Lstat(trap); !os.IsNotExist(err) {
		t.Fatalf("synthetic mount target Lstat = %v, want missing", err)
	}
	resolver.mounts.addMountPoint(filepath.Clean(resolver.anchor.mapBase(trap)))
	if gitMetadataDirectoryPathsSafeWithResolver(resolver, gitDir) {
		t.Fatal("core.worktree known mount passed metadata preflight")
	}
}
