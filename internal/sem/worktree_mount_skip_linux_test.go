//go:build linux

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// syntheticMountRoot holds repo with a mount table the test controls, so the
// mount-point classification can be checked without privileges.
func syntheticMountRoot(t *testing.T, repo string, mounts ...string) (*sweepDirectoryRoot, pathTraversalAnchor) {
	t.Helper()
	anchor, resolvedRepo, err := newPathTraversalAnchor(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRepo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	table := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		table[filepath.Join(resolvedRepo, mount)] = struct{}{}
	}
	root.mountOnce.Do(func() { root.mountPoints = table })
	return root, anchor
}

func TestLinuxSweepClassifiesOnlyTheFinalComponentAsAMountPoint(t *testing.T) {
	admit := func() bool { return true }
	for _, testCase := range []struct {
		name       string
		mounts     []string
		components []string
		wantMount  bool
	}{
		{"directory is the mount point", []string{"mnt"}, []string{"mnt"}, true},
		{"nested directory is the mount point", []string{"a/mnt"}, []string{"a", "mnt"}, true},
		{"ancestor is a mount point", []string{"a"}, []string{"a", "child"}, false},
		{"mount table does not name it", nil, []string{"mnt"}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, filepath.Join(testCase.components...)), 0o755); err != nil {
				t.Fatal(err)
			}
			root, anchor := syntheticMountRoot(t, repo, testCase.mounts...)

			// openat2 reports a mount crossing as EXDEV without saying where.
			opened, err := root.openAfterOpenat2Failure(anchor, testCase.components, admit, syscall.EXDEV)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("EXDEV classification returned a directory")
			}
			if !errors.Is(err, errSymlinkChainOffVolume) {
				t.Fatalf("EXDEV error = %v, want %v", err, errSymlinkChainOffVolume)
			}
			if got := errors.Is(err, errWorktreeMountPoint); got != testCase.wantMount {
				t.Fatalf("EXDEV error = %v, mount point = %v, want %v", err, got, testCase.wantMount)
			}

			// The mountinfo fallback walks component by component. Without a
			// table entry it opens the ordinary directory, as it did before.
			opened, err = root.openWithoutOpenat2(anchor, testCase.components, admit)
			if opened != nil {
				_ = opened.Close()
			}
			if len(testCase.mounts) == 0 {
				if err != nil {
					t.Fatalf("mountinfo fallback error without a mount = %v", err)
				}
				return
			}
			if opened != nil {
				t.Fatal("mountinfo fallback opened a directory at or below a mount point")
			}
			if !errors.Is(err, errSymlinkChainOffVolume) {
				t.Fatalf("mountinfo fallback error = %v, want %v", err, errSymlinkChainOffVolume)
			}
			if got := errors.Is(err, errWorktreeMountPoint); got != testCase.wantMount {
				t.Fatalf("mountinfo fallback error = %v, mount point = %v, want %v", err, got, testCase.wantMount)
			}
		})
	}
}

func TestLinuxSweepMountPointClassificationFailsClosed(t *testing.T) {
	admit := func() bool { return true }
	t.Run("unreadable mount table", func(t *testing.T) {
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
		root.mountOnce.Do(func() { root.mountErr = inventoryErr })

		_, err = root.openAfterOpenat2Failure(anchor, []string{"mnt"}, admit, syscall.EXDEV)
		if !errors.Is(err, errSymlinkChainOffVolume) || !errors.Is(err, inventoryErr) {
			t.Fatalf("EXDEV error = %v, want both %v and %v", err, errSymlinkChainOffVolume, inventoryErr)
		}
		if errors.Is(err, errWorktreeMountPoint) {
			t.Fatalf("EXDEV error = %v: an unreadable mount table must not classify a mount point", err)
		}
	})
	t.Run("renamed held root", func(t *testing.T) {
		parent := t.TempDir()
		repo := filepath.Join(parent, "repo")
		if err := os.MkdirAll(filepath.Join(repo, "mnt"), 0o755); err != nil {
			t.Fatal(err)
		}
		root, anchor := syntheticMountRoot(t, repo, "mnt")
		if err := os.Rename(repo, filepath.Join(parent, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := root.openAfterOpenat2Failure(anchor, []string{"mnt"}, admit, syscall.EXDEV)
		if !errors.Is(err, errSymlinkChainOffVolume) {
			t.Fatalf("EXDEV error = %v, want %v", err, errSymlinkChainOffVolume)
		}
		if errors.Is(err, errWorktreeMountPoint) {
			t.Fatalf("EXDEV error = %v: a stale held-root name must not classify a mount point", err)
		}
	})
}

// TestWorktreeSourceFilesSkipsARealReadOnlyMount is the reported shape: a
// read-only tmpfs mounted inside a Git checkout, as the Claude Code sandbox
// does at .claude/.cc-writes. Mounting needs privileges, so the test skips
// where it cannot mount; the synthetic tests above cover the classification.
func TestWorktreeSourceFilesSkipsARealReadOnlyMount(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "keep.go", "package keep\nfunc KeptBesideTheMount() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "source")
	mountPoint := filepath.Join(repo, ".claude", ".cc-writes")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount("tmpfs", mountPoint, "tmpfs", syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, "size=64k"); err != nil {
		t.Skipf("mount a read-only tmpfs inside the checkout: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(mountPoint, 0); err != nil {
			t.Errorf("unmount %s: %v", mountPoint, err)
		}
	})

	paths, warnings, err := worktreeSourceFiles(t.Context(), repo, ignoreMatcher{}, false, nil)
	if err != nil {
		t.Fatalf("a mount point inside the worktree failed the whole listing: %v", err)
	}
	if !containsWorktreePath(paths, "keep.go") {
		t.Fatalf("paths = %q, want keep.go", paths)
	}
	if !hasProviderWarning(warnings, "W_GIT_WORKTREE_FALLBACK") {
		t.Fatalf("warnings = %+v, want W_GIT_WORKTREE_FALLBACK: Git must not enumerate across the mount", warnings)
	}
	if !hasProviderWarningDetail(warnings, "W_WALK_MOUNT_SKIPPED", ".claude/.cc-writes") {
		t.Fatalf("warnings = %+v, want W_WALK_MOUNT_SKIPPED naming .claude/.cc-writes", warnings)
	}
}

// A mount over a TRACKED directory: the index still names files there, so
// every route that asks for a path by name has to refuse it, not just the walk.
func TestWorktreeSnapshotAndSearchSkipAMountOverTrackedFiles(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "keep.go", "package keep\nfunc KeptBesideTheMount() {}\n")
	writeFile(t, repo, "tracked/t.go", "package tracked\nfunc CommittedUnderTheMount() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "source")
	outside := t.TempDir()
	writeFile(t, outside, "t.go", "package tracked\nfunc MountedFromOutsideTheRepo() {}\n")
	mountPoint := filepath.Join(repo, "tracked")
	if err := syscall.Mount(outside, mountPoint, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("bind-mount a directory over a tracked one: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(mountPoint, 0); err != nil {
			t.Errorf("unmount %s: %v", mountPoint, err)
		}
	})

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatalf("a bind mount inside the worktree failed the snapshot: %v", err)
	}
	if !snapshotHasSymbol(snapshot, "KeptBesideTheMount") {
		t.Fatalf("snapshot files = %#v, want keep.go indexed", snapshot.Files)
	}
	for _, name := range []string{"CommittedUnderTheMount", "MountedFromOutsideTheRepo"} {
		if snapshotHasSymbol(snapshot, name) {
			t.Fatalf("snapshot indexed %s from below the skipped mount point", name)
		}
	}
	if !hasProviderWarningDetail(snapshot.Header.Warnings, "W_WALK_MOUNT_SKIPPED", "tracked") {
		t.Fatalf("warnings = %+v, want W_WALK_MOUNT_SKIPPED naming tracked", snapshot.Header.Warnings)
	}

	response, err := SearchRepository(t.Context(), repo, "test-version", "MountedFromOutsideTheRepo", SearchOptions{Worktree: true})
	if err != nil {
		t.Fatalf("search beside a bind mount: %v", err)
	}
	for _, path := range SearchResponsePaths(response) {
		if path == "tracked" || strings.HasPrefix(path, "tracked/") {
			t.Fatalf("search returned %q from below the skipped mount point", path)
		}
	}
}
