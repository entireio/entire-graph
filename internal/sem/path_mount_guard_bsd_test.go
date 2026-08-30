//go:build darwin || dragonfly || freebsd || openbsd

package sem

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBSDMountPathRequiresTerminator(t *testing.T) {
	if path, ok := bsdMountPath([]int8{'/', 'd', 'e', 'v', 0, 'x'}); !ok || path != "/dev" {
		t.Fatalf("terminated mount path = (%q, %v), want (%q, true)", path, ok, "/dev")
	}
	if path, ok := bsdMountPath([]int8{'/', 'd', 'e', 'v'}); ok || path != "" {
		t.Fatalf("unterminated mount path = (%q, %v), want (empty, false)", path, ok)
	}
}

func BenchmarkReadBSDMountPoints(b *testing.B) {
	for range b.N {
		if _, err := readBSDMountPoints(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBSDSweepChecksMountBeforeLstat(t *testing.T) {
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
	candidate := filepath.Join(resolvedRepo, "directory-that-does-not-exist")
	root.mounts.addMountPoint(candidate)

	opened, err := root.Open(anchor, filepath.Base(candidate), func() bool { return true })
	if opened != nil {
		_ = opened.Close()
		t.Fatalf("sweep opened synthetic mount point %q", candidate)
	}
	if !errors.Is(err, errPathCrossesKnownMount) {
		t.Fatalf("sweep error for nonexistent synthetic mount point = %v, want %v", err, errPathCrossesKnownMount)
	}
}
