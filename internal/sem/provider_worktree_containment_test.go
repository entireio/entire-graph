package sem

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newContainmentFixture builds a working tree whose repository root has a
// sibling directory holding files the snapshot must never read: repoRoot/src/a.ts
// is the only source file, and outside/ sits one level above it.
func newContainmentFixture(t *testing.T) (repo, outside string) {
	t.Helper()
	outside = t.TempDir()
	repo = filepath.Join(outside, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "a.ts"), []byte("export const a = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.env"), []byte("SECRET_TOKEN=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, outside
}

func openWorktreeSource(t *testing.T, repo string, options sourceOptions) openedSource {
	t.Helper()
	opened, err := openSource(t.Context(), repo, "", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if opened.close != nil {
			if err := opened.close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}
	})
	return opened
}

// TestWorktreeReadRefusesPathAboveRepositoryRoot is the traversal itself. The
// reader takes repository-authored paths — jsLocalImportCandidates turns an
// import specifier into exactly this shape — so a path that climbs above the
// root must be refused rather than joined onto it.
func TestWorktreeReadRefusesPathAboveRepositoryRoot(t *testing.T) {
	t.Parallel()
	repo, _ := newContainmentFixture(t)
	opened := openWorktreeSource(t, repo, sourceOptions{})

	if content, ok := opened.read("../secret.env"); ok {
		t.Fatalf("read escaped the repository root and returned %q", content)
	}
	if prefix, ok := opened.readPrefix("../secret.env", 8); ok {
		t.Fatalf("readPrefix escaped the repository root and returned %q", prefix)
	}
}

// TestWorktreeReadRefusesSymlinkedDirectoryComponent covers the half os.Lstat on
// the joined path never saw: the guard inspects only the final component, so a
// symlinked directory earlier in the path was followed out of the repository.
func TestWorktreeReadRefusesSymlinkedDirectoryComponent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Creating a directory symlink on Windows needs SeCreateSymbolicLinkPrivilege,
		// which CI runners do not grant, so this input cannot be built there.
		t.Skip("directory symlinks require elevated privilege on Windows")
	}
	repo, outside := newContainmentFixture(t)
	if err := os.Symlink(outside, filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	opened := openWorktreeSource(t, repo, sourceOptions{})

	if content, ok := opened.read("link/secret.env"); ok {
		t.Fatalf("read followed a symlinked directory out of the repository and returned %q", content)
	}
	if prefix, ok := opened.readPrefix("link/secret.env", 8); ok {
		t.Fatalf("readPrefix followed a symlinked directory out of the repository and returned %q", prefix)
	}
}

// TestOversizeRegistryRefusesPathAboveRepositoryRoot covers the third read. The
// registry defers the digest, so refusing the read is not enough on its own: the
// deferred filedigest pass has to be confined too, or it reports the size, hash
// and line count of a file outside the repository.
func TestOversizeRegistryRefusesPathAboveRepositoryRoot(t *testing.T) {
	t.Parallel()
	repo, outside := newContainmentFixture(t)
	if err := os.WriteFile(filepath.Join(outside, "big.env"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := openWorktreeSource(t, repo, sourceOptions{maxReadBytes: 16})

	if content, ok := opened.read("../big.env"); ok {
		t.Fatalf("read escaped the repository root and returned %q", content)
	}
	if record, ok := opened.oversize("../big.env"); ok {
		t.Fatalf("oversize registry described a file above the repository root: %+v", record)
	}
}

// TestWorktreeSourceReadsRepositoryFilesAndClosesItsRoot pins the behavior the
// confinement must not cost: in-repository reads still work, and the source now
// owns a handle, so it must hand the caller a closer.
func TestWorktreeSourceReadsRepositoryFilesAndClosesItsRoot(t *testing.T) {
	t.Parallel()
	repo, _ := newContainmentFixture(t)
	opened := openWorktreeSource(t, repo, sourceOptions{})

	content, ok := opened.read("src/a.ts")
	if !ok || content != "export const a = 1;\n" {
		t.Fatalf("read(src/a.ts) = %q, %v; want the file content", content, ok)
	}
	prefix, ok := opened.readPrefix("src/a.ts", 6)
	if !ok || prefix != "export" {
		t.Fatalf("readPrefix(src/a.ts, 6) = %q, %v; want %q", prefix, ok, "export")
	}
	if opened.close == nil {
		t.Fatal("worktree source returned no closer; the root it opens would leak for the process lifetime")
	}
}

// TestSymlinkedDirectoryResolutionInsideRepository pins the part of this change
// that is NOT containment, so the claim in openSource's comment is checked
// rather than asserted. os.Root resolves every component inside the root, which
// splits paths that filepath.Join plus os.Lstat treated alike:
//
//   - a relative link resolving within the repository is still followed;
//   - a link that climbs out and comes back is refused, even though its target
//     is a repository file;
//   - an absolute link target is refused for the same reason, because os.Root
//     will not rebase an absolute path onto itself — the target being inside the
//     repository does not save it.
//
// The last two are the behavior a repository can lose: `git ls-files --cached`
// lists files under a directory that the index records but the working tree has
// replaced with a link, and those files now reach provider_parallel's
// E_FILE_READ path instead of being parsed.
func TestSymlinkedDirectoryResolutionInsideRepository(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Same reason as the escape case above: creating a directory symlink on
		// Windows needs SeCreateSymbolicLinkPrivilege, which CI runners do not grant,
		// so none of these three inputs can be built there.
		t.Skip("directory symlinks require elevated privilege on Windows")
	}
	repo, _ := newContainmentFixture(t)
	for _, link := range []struct{ name, target string }{
		{"rel", "src"},
		{"updown", filepath.Join("..", filepath.Base(repo), "src")},
		{"abs", filepath.Join(repo, "src")},
	} {
		if err := os.Symlink(link.target, filepath.Join(repo, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	opened := openWorktreeSource(t, repo, sourceOptions{})

	if content, ok := opened.read("rel/a.ts"); !ok || content != "export const a = 1;\n" {
		t.Fatalf("read(rel/a.ts) = %q, %v; a relative link resolving inside the repository must still be followed", content, ok)
	}
	if content, ok := opened.read("updown/a.ts"); ok {
		t.Fatalf("read(updown/a.ts) = %q; a link leaving the repository is refused even when it returns to it", content)
	}
	if content, ok := opened.read("abs/a.ts"); ok {
		t.Fatalf("read(abs/a.ts) = %q; an absolute link target is refused even when it points inside the repository", content)
	}
}

// TestJSImportCandidatesCanClimbAboveRepositoryRoot records why the reader is
// the right boundary: the candidate paths are derived from an import specifier
// written in a repository file, and filepath.Join normalizes the climb.
func TestJSImportCandidatesCanClimbAboveRepositoryRoot(t *testing.T) {
	t.Parallel()
	candidates := jsLocalImportCandidates("src/a.ts", "../../secret.env")
	if len(candidates) != 1 || candidates[0] != "../secret.env" {
		t.Fatalf("jsLocalImportCandidates = %q; want [../secret.env]", candidates)
	}
}
