//go:build linux || darwin || dragonfly || freebsd || openbsd

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSameVolumePathResolverChecksMountBeforeLstat(t *testing.T) {
	repo := t.TempDir()
	resolver, err := newSameVolumePathResolver(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	candidate := filepath.Join(repo, "metadata-that-does-not-exist")
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate Lstat error = %v, want a missing path", err)
	}
	resolver.mounts.addMountPoint(filepath.Clean(resolver.anchor.mapBase(candidate)))

	checks := map[string]func() error{
		"open": func() error {
			opened, _, err := resolver.open(candidate)
			if opened != nil {
				_ = opened.Close()
			}
			return err
		},
		"lstat": func() error {
			_, err := resolver.lstat(candidate)
			return err
		},
		"readlink": func() error {
			_, err := resolver.readlink(candidate)
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, errPathCrossesKnownMount) {
				t.Fatalf("resolver error for nonexistent synthetic mount point = %v, want %v", err, errPathCrossesKnownMount)
			}
		})
	}
}

func TestGitMetadataGuardTreatsMissingKnownMountAsUnsafe(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver, err := newSameVolumePathResolver(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	objects := resolver.anchor.mapBase(filepath.Join(gitDir, "objects"))
	resolver.mounts.addMountPoint(objects)

	if gitMetadataDirectoryPathsSafeWithResolver(resolver, gitDir) {
		t.Fatal("missing .git/objects known to be a mount point was treated as safely absent")
	}
}

func TestPathMountGuardTrustsSelectedMountOnly(t *testing.T) {
	guard := makePathMountGuard(
		string(filepath.Separator),
		filepath.Join(string(filepath.Separator), "workspace", "repo"),
		map[string]struct{}{
			string(filepath.Separator):                                                     {},
			filepath.Join(string(filepath.Separator), "workspace"):                         {},
			filepath.Join(string(filepath.Separator), "workspace", "repo", "nested-mount"): {},
		},
	)
	if got, want := mountPathKey(guard.selectedMount), mountPathKey(filepath.Join(string(filepath.Separator), "workspace")); got != want {
		t.Fatalf("selected mount = %q, want %q", guard.selectedMount, filepath.Join(string(filepath.Separator), "workspace"))
	}
	for _, rel := range []string{"workspace", filepath.Join("workspace", "repo"), filepath.Join("workspace", "repo", "file.go")} {
		if err := guard.beforeLookup(rel); err != nil {
			t.Fatalf("trusted path %q was rejected: %v", rel, err)
		}
	}
	if err := guard.beforeLookup(filepath.Join("workspace", "repo", "nested-mount")); !errors.Is(err, errPathCrossesKnownMount) {
		t.Fatalf("nested mount error = %v, want %v", err, errPathCrossesKnownMount)
	}
	if err := guard.beforeLookup("outside"); !errors.Is(err, errSymlinkChainOffVolume) {
		t.Fatalf("path outside selected mount error = %v, want %v", err, errSymlinkChainOffVolume)
	}
}

func TestPathMountGuardNeverFoldsAPathIntoTrust(t *testing.T) {
	root := string(filepath.Separator)
	for name, collision := range map[string]string{
		"case":          filepath.Join(root, "Workspace"),
		"normalization": filepath.Join(root, "workspac\u00e9"),
	} {
		t.Run(name, func(t *testing.T) {
			trustedBase := filepath.Join(root, "workspace", "repo")
			if name == "normalization" {
				trustedBase = filepath.Join(root, "workspace\u0301", "repo")
			}
			guard := makePathMountGuard(root, trustedBase, map[string]struct{}{
				root:      {},
				collision: {},
			})
			if guard.selectedMount != root {
				t.Fatalf("folding selected unrelated mount %q for trusted base %q", guard.selectedMount, trustedBase)
			}
			if err := guard.beforeLookup(strings.TrimPrefix(collision, root)); !errors.Is(err, errPathCrossesKnownMount) {
				t.Fatalf("collision lookup error = %v, want %v", err, errPathCrossesKnownMount)
			}
		})
	}
}

func TestSameVolumePathResolverRejectsKnownMountBeforeLookup(t *testing.T) {
	resolver, err := newSameVolumePathResolver(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	root := resolver.mounts.root
	candidates := make([]string, 0, len(resolver.mounts.mountPoints))
	for mount := range resolver.mounts.mountPoints {
		rel, err := filepath.Rel(root, mount)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		candidates = append(candidates, mount)
	}
	if len(candidates) == 0 {
		t.Skip("host exposes no nested mount point beneath the filesystem root")
	}
	// Prefer a shallow system mount such as /proc or /dev so only ordinary,
	// local ancestors are inspected before the mount-table guard fires.
	sort.Slice(candidates, func(i, j int) bool {
		left := len(splitNativePathComponents(candidates[i]))
		right := len(splitNativePathComponents(candidates[j]))
		if left != right {
			return left < right
		}
		return candidates[i] < candidates[j]
	})

	opened, _, err := resolver.open(candidates[0])
	if opened != nil {
		_ = opened.Close()
		t.Fatalf("resolver opened known mount point %q", candidates[0])
	}
	if !errors.Is(err, errPathCrossesKnownMount) {
		t.Fatalf("resolver error for known mount point %q = %v, want %v", candidates[0], err, errPathCrossesKnownMount)
	}
}
