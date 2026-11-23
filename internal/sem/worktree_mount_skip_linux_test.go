//go:build linux

package sem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	root.readMountTable = func() (map[string]struct{}, error) { return table, nil }
	return root, anchor
}

func TestLinuxSweepClassifiesOnlyTheFinalComponentAsAMountPoint(t *testing.T) {
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
			admit := func() bool { return true }

			// openat2 reports a mount crossing as EXDEV without saying where.
			opened, err := root.openAfterOpenat2Failure(anchor, testCase.components, admit, syscall.EXDEV)
			requireMountRefusal(t, "EXDEV classification", opened, err, testCase.wantMount)

			// The mountinfo fallback walks component by component. Without a
			// table entry it opens the ordinary directory, as it did before.
			opened, err = root.openWithoutOpenat2(anchor, testCase.components, admit)
			if len(testCase.mounts) == 0 {
				if opened != nil {
					_ = opened.Close()
				}
				if err != nil {
					t.Fatalf("mountinfo fallback error without a mount = %v", err)
				}
				return
			}
			requireMountRefusal(t, "mountinfo fallback", opened, err, testCase.wantMount)
		})
	}
}

// requireMountRefusal checks that an open refused with errSymlinkChainOffVolume
// and that it classified a mount point exactly when wantMount says so.
func requireMountRefusal(t *testing.T, route string, opened *os.File, err error, wantMount bool) {
	t.Helper()
	if opened != nil {
		_ = opened.Close()
		t.Fatalf("%s opened a directory at or below a mount point", route)
	}
	if !errors.Is(err, errSymlinkChainOffVolume) {
		t.Fatalf("%s error = %v, want %v", route, err, errSymlinkChainOffVolume)
	}
	if got := errors.Is(err, errWorktreeMountPoint); got != wantMount {
		t.Fatalf("%s error = %v, mount point = %v, want %v", route, err, got, wantMount)
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
		root.readMountTable = func() (map[string]struct{}, error) { return nil, inventoryErr }

		_, err = root.openAfterOpenat2Failure(anchor, []string{"mnt"}, admit, syscall.EXDEV)
		if !errors.Is(err, errSymlinkChainOffVolume) || !errors.Is(err, inventoryErr) {
			t.Fatalf("EXDEV error = %v, want both %v and %v", err, errSymlinkChainOffVolume, inventoryErr)
		}
		if errors.Is(err, errWorktreeMountPoint) {
			t.Fatalf("EXDEV error = %v: an unreadable mount table must not classify a mount point", err)
		}
	})
	t.Run("ancestor mounted after the inventory was cached", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		root, anchor := syntheticMountRoot(t, repo, "a", "a/b")
		resolvedB := filepath.Join(filepath.Clean(root.file.Name()), "a", "b")
		// The walk cached a table that knew only a/b; a is mounted since.
		root.mountOnce.Do(func() { root.mountPoints = map[string]struct{}{resolvedB: {}} })
		for _, route := range []struct {
			name string
			open func() (*os.File, error)
		}{
			{"EXDEV", func() (*os.File, error) {
				return root.openAfterOpenat2Failure(anchor, []string{"a", "b"}, admit, syscall.EXDEV)
			}},
			{"mountinfo fallback", func() (*os.File, error) {
				return root.openWithoutOpenat2(anchor, []string{"a", "b"}, admit)
			}},
		} {
			opened, err := route.open()
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, errSymlinkChainOffVolume) || errors.Is(err, errWorktreeMountPoint) {
				t.Fatalf("%s error = %v: a mounted ancestor must fail closed, not skip a/b", route.name, err)
			}
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

// Git's working-tree grep reads tracked paths wherever they are mounted, so
// neither grep route may run for a search whose listing skipped a mount point:
// not the needle grep, and not the index preselection a 10,000-file worktree
// takes. The same repository with the mount gone takes both again, which shows
// the fixture really reaches the large-repository branch.
func TestWorktreeSearchKeepsGitGrepOutOfASkippedMount(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "keep.go", "package keep\n// sentinel alphaone\nfunc KeptBesideTheMount() {}\n")
	writeFile(t, repo, "tracked/t.go", "package tracked\nfunc CommittedUnderTheMount() {}\n")
	fillerDir := filepath.Join(repo, "filler")
	if err := os.MkdirAll(fillerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < minGitGrepPreselectionFiles; index++ {
		path := filepath.Join(fillerDir, fmt.Sprintf("file_%05d.go", index))
		if err := os.WriteFile(path, []byte("package filler\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "large tracked worktree")
	mountPoint := filepath.Join(repo, "tracked")
	if err := syscall.Mount(t.TempDir(), mountPoint, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("bind-mount a directory over a tracked one: %v", err)
	}
	mounted := true
	unmount := func() {
		if mounted {
			mounted = false
			if err := syscall.Unmount(mountPoint, 0); err != nil {
				t.Errorf("unmount %s: %v", mountPoint, err)
			}
		}
	}
	t.Cleanup(unmount)

	preselect := func() searchFileSelection {
		t.Helper()
		selection, err := preselectSearchFiles(
			t.Context(), repo, buildSearchQuery("sentinel alphaone"), searchQuery{},
			SearchOptions{Worktree: true, Profile: ProfileSyntaxOnly, TopK: 5, DisableCache: true},
			ProviderSnapshotOptions{Worktree: true, NoNetwork: true, Profile: ProfileSyntaxOnly},
			ProviderSnapshot{}, false,
		)
		if err != nil {
			t.Fatal(err)
		}
		return selection
	}

	skipped := preselect()
	if skipped.gitGrepUsable || strings.Contains(skipped.preselectionBackend, "git") {
		t.Fatalf("with a skipped mount point: git grep usable = %v, backend = %q, want neither Git route",
			skipped.gitGrepUsable, skipped.preselectionBackend)
	}

	unmount()
	plain := preselect()
	if !plain.gitGrepUsable || plain.preselectionBackend != "git-index-grep+go-content" {
		t.Fatalf("without the mount: git grep usable = %v, backend = %q, want the Git index preselection",
			plain.gitGrepUsable, plain.preselectionBackend)
	}
}

// mountRefusingReadersFor opens repo the way openSource does once a mount point
// was skipped, and hands back the registry its readers note oversized files in.
func mountRefusingReadersFor(t *testing.T, repo string, maxReadBytes int64, registry **oversizeRegistry) (
	func(string) (string, bool), func(string, int) (string, bool),
) {
	t.Helper()
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	noted := newOversizeRegistry(root, repo)
	read, readPrefix, closeFiles, err := mountRefusingWorktreeReaders(repo, pinnedRootIdentity(root), maxReadBytes, noted)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFiles() })
	if registry != nil {
		*registry = noted
	}
	return read, readPrefix
}

// The readers reopen the repository by name, so they refuse to start on a
// directory other than the one the source pinned.
func TestMountRefusingReadersRefuseARepointedRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "keep.go", "package keep\n")
	other, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, pinned := range map[string]os.FileInfo{"another directory": other, "no identity": nil} {
		if _, _, _, err := mountRefusingWorktreeReaders(repo, pinned, 0, newOversizeRegistry(nil, repo)); !errors.Is(err, errSymlinkChainOffVolume) {
			t.Errorf("%s: err = %v, want the readers refused", name, err)
		}
	}
}

// symlinkFixture is a worktree whose links all lead into mnt, the directory the
// tests below treat as a skipped mount point.
func symlinkFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "keep.go", "package keep\n")
	writeFile(t, repo, "src/util.ts", "export const util = 1\n")
	writeFile(t, repo, "dir/file.ts", "export const listed = 1\n")
	writeFile(t, repo, "mnt/file.ts", "export const mounted = 1\n")
	writeFile(t, repo, "mnt/secret.ts", "export const secret = 1\n")
	for link, target := range map[string]string{"alias": "mnt", "src/up": "../mnt", "link.ts": "mnt/secret.ts"} {
		if err := os.Symlink(target, filepath.Join(repo, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe.ts"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

// swapForLink replaces the directory rel with a symlink to target, the way a
// concurrent writer could after the listing.
func swapForLink(t *testing.T, repo, rel, target string) {
	t.Helper()
	path := filepath.Join(repo, rel)
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func requireReads(t *testing.T, read func(string) (string, bool), want bool, paths ...string) {
	t.Helper()
	for _, rel := range paths {
		if _, ok := read(rel); ok != want {
			t.Errorf("read(%q) ok = %v, want %v", rel, ok, want)
		}
	}
}

// Once a mount point was skipped, every read opens its file in one step that
// refuses links, so no spelling reaches mnt through one: not a by-name path
// through a symlinked directory, and not a LISTED path whose directory was
// replaced by a link after the listing. A FIFO in a file's place is refused
// without blocking the reader.
func TestMountRefusingReadersFollowNoLink(t *testing.T) {
	t.Parallel()
	repo := symlinkFixture(t)
	read, readPrefix := mountRefusingReadersFor(t, repo, 0, nil)
	prefix := func(rel string) (string, bool) { return readPrefix(rel, 16) }

	linked := []string{"alias/secret.ts", "src/up/secret.ts", "src/up/../mnt/secret.ts", "src/../alias/secret.ts", "link.ts"}
	plain, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	for _, rel := range linked {
		if content, err := plain.ReadFile(rel); err != nil || !strings.Contains(string(content), "secret") {
			t.Fatalf("fixture is wrong: %s does not resolve into mnt: %q, %v", rel, content, err)
		}
	}
	requireReads(t, read, false, append(linked, "pipe.ts")...)
	requireReads(t, prefix, false, append(linked, "pipe.ts")...)
	requireReads(t, read, true, "keep.go", "src/util.ts", "dir/file.ts")
	requireReads(t, prefix, true, "keep.go", "src/util.ts", "dir/file.ts")

	swapForLink(t, repo, "dir", "mnt")
	if content, err := os.ReadFile(filepath.Join(repo, "dir", "file.ts")); err != nil || !strings.Contains(string(content), "mounted") {
		t.Fatalf("fixture is wrong: dir/file.ts does not resolve into mnt: %q, %v", content, err)
	}
	requireReads(t, read, false, "dir/file.ts")
	requireReads(t, prefix, false, "dir/file.ts")
}

// The oversize digest opens through the same refusing open as the readers.
func TestMountRefusingReadersDigestOversizedFilesThroughTheSameOpen(t *testing.T) {
	t.Parallel()
	repo := symlinkFixture(t)
	writeFile(t, repo, "dir/big.ts", strings.Repeat("x", 64)+"\n")
	var registry *oversizeRegistry
	read, _ := mountRefusingReadersFor(t, repo, 16, &registry)
	if _, ok := read("dir/big.ts"); ok {
		t.Fatal("read an oversized file")
	}
	if record, ok := registry.lookup("dir/big.ts"); !ok || record.Hash == "" || record.Bytes != 65 {
		t.Fatalf("oversize record = %+v, %v, want a digest of the oversized file", record, ok)
	}

	swapForLink(t, repo, "dir", "mnt")
	registry.note("dir/secret.ts", 64)
	if record, ok := registry.lookup("dir/secret.ts"); !ok || record.Hash != "" {
		t.Fatalf("oversize record = %+v, %v, want no digest through the swapped link", record, ok)
	}
}

// Without openat2 the same refusals come from opening one component at a time
// against the mount table, the file itself included.
// openWithoutOpenat2Reader reports whether openFileWithoutOpenat2 opens a
// regular file at each path below root.
func openWithoutOpenat2Reader(root *sweepDirectoryRoot, anchor pathTraversalAnchor) func(string) (string, bool) {
	return func(rel string) (string, bool) {
		_, components, err := sweepRelativeComponents(rel)
		if err != nil {
			return "", false
		}
		file, err := root.openFileWithoutOpenat2(anchor, components)
		if err != nil {
			return "", false
		}
		defer file.Close()
		info, err := file.Stat()
		return "", err == nil && info.Mode().IsRegular()
	}
}

func TestMountRefusingOpenWithoutOpenat2(t *testing.T) {
	t.Parallel()
	repo := symlinkFixture(t)
	root, anchor := syntheticMountRoot(t, repo, "mnt", "src/util.ts")
	open := openWithoutOpenat2Reader(root, anchor)
	requireReads(t, open, false, "mnt/file.ts", "src/util.ts", "alias/secret.ts", "src/up/secret.ts", "link.ts", "pipe.ts")
	requireReads(t, open, true, "keep.go", "dir/file.ts")
	swapForLink(t, repo, "dir", "mnt")
	requireReads(t, open, false, "dir/file.ts")
}

// The mount table spells a mount point its own way, and a filesystem that
// folds case or Unicode normalization resolves another spelling into it. The
// open without openat2 refuses every spelling that folds to a mount point, the
// file itself or a directory on the way, the way the mount guard's rejections
// do. This filesystem does not fold, so a folded match here is a conservative
// refusal of a distinct file; that is the direction the rule errs in.
func TestMountRefusingOpenWithoutOpenat2FoldsMountSpellings(t *testing.T) {
	t.Parallel()
	repo := symlinkFixture(t)
	root, anchor := syntheticMountRoot(t, repo, "KEEP.go", "DIR")
	open := openWithoutOpenat2Reader(root, anchor)
	requireReads(t, open, false, "keep.go", "dir/file.ts")
	requireReads(t, open, true, "src/util.ts")
}

// With a real mount the whole worktree source refuses a listed path whose
// directory became a link into the skipped mount point after the listing, and
// the refusing open turns away the mount point and a single-file bind mount
// even when the by-name guard is bypassed.
func TestWorktreeSourceRefusesAListedPathSwappedIntoASkippedMount(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "keep.go", "package keep\n")
	writeFile(t, repo, "bound.go", "package keep\n")
	writeFile(t, repo, "dir/file.ts", "export const listed = 1\n")
	if err := os.MkdirAll(filepath.Join(repo, "mnt"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFile(t, outside, "file.ts", "export const mountedFromOutside = 1\n")
	mount := func(source, target string) {
		t.Helper()
		if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
			t.Skipf("bind-mount %s: %v", target, err)
		}
		t.Cleanup(func() {
			if err := syscall.Unmount(target, 0); err != nil {
				t.Errorf("unmount %s: %v", target, err)
			}
		})
	}
	mount(outside, filepath.Join(repo, "mnt"))

	opened, err := openSource(t.Context(), repo, "", sourceOptions{})
	if err != nil {
		t.Fatalf("open the worktree beside a bind mount: %v", err)
	}
	if opened.close != nil {
		t.Cleanup(func() { _ = opened.close() })
	}
	if !slices.Equal(opened.worktreeMounts, []string{"mnt"}) || !slices.Contains(opened.paths, "dir/file.ts") {
		t.Fatalf("mounts = %q, paths = %q, want mnt skipped and dir/file.ts listed", opened.worktreeMounts, opened.paths)
	}
	requireReads(t, opened.read, true, "dir/file.ts")
	swapForLink(t, repo, "dir", "mnt")
	if content, _ := os.ReadFile(filepath.Join(repo, "dir", "file.ts")); !strings.Contains(string(content), "mountedFromOutside") {
		t.Fatalf("fixture is wrong: dir/file.ts reads %q, not the mounted file", content)
	}
	requireReads(t, opened.read, false, "dir/file.ts")
	requireReads(t, func(rel string) (string, bool) { return opened.readPrefix(rel, 16) }, false, "dir/file.ts")

	mount(filepath.Join(outside, "file.ts"), filepath.Join(repo, "bound.go"))
	read, _ := mountRefusingReadersFor(t, repo, 0, nil)
	requireReads(t, read, false, "mnt/file.ts", "bound.go")
	requireReads(t, read, true, "keep.go")
}
