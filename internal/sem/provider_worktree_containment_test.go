package sem

import (
	"fmt"
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
// splits paths that filepath.Join plus os.Lstat treated alike — but readFallback
// re-verifies each of the three on its DESTINATION rather than accepting
// os.Root's outright refusal, so all three still resolve to repository content:
//
//   - a relative link resolving within the repository is followed directly by
//     os.Root, the same as before this change;
//   - a link that climbs out and returns is refused by os.Root's own
//     resolution, but its destination is still a repository file, so
//     readFallback follows it;
//   - an absolute link target is refused by os.Root for the same structural
//     reason — it will not rebase an absolute path onto itself — but
//     readFallback checks the absolute target's REAL location directly, which
//     the repository already owns.
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

	for _, name := range []string{"rel", "updown", "abs"} {
		path := name + "/a.ts"
		if content, ok := opened.read(path); !ok || content != "export const a = 1;\n" {
			t.Errorf("read(%q) = %q, %v; want the repository file, whether os.Root followed the link directly or readFallback verified its destination", path, content, ok)
		}
	}
}

// TestReadFallbackRefusesAnAbsoluteSymlinkThatEscapes is the negative case
// TestSymlinkedDirectoryResolutionInsideRepository's "abs" link does not cover:
// an absolute target this repository does NOT own must stay refused, exactly as
// os.Root refuses it, because readFallback verifies the destination rather than
// unconditionally trusting an absolute spelling.
func TestReadFallbackRefusesAnAbsoluteSymlinkThatEscapes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks require elevated privilege on Windows")
	}
	repo, outside := newContainmentFixture(t)
	if err := os.Symlink(outside, filepath.Join(repo, "abs-outside")); err != nil {
		t.Fatal(err)
	}
	opened := openWorktreeSource(t, repo, sourceOptions{})

	if content, ok := opened.read("abs-outside/secret.env"); ok {
		t.Fatalf("read(abs-outside/secret.env) = %q; an absolute link leaving the repository must still be refused", content)
	}
}

// TestReadFallbackFollowsASymlinkChainLongerThanOSRootsLimit covers the low
// finding: os.Root refuses to resolve a chain past its hardcoded 8-hop limit
// (rootMaxSymlinks), even when every hop stays inside the repository, so a
// tracked file nine or more relative symlinked directories deep used to come
// back with E_FILE_READ despite no escape attempt anywhere in the chain.
func TestReadFallbackFollowsASymlinkChainLongerThanOSRootsLimit(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks require elevated privilege on Windows")
	}
	repo, _ := newContainmentFixture(t)
	// 10 hops: one more than os.Root's rootMaxSymlinks (8), well under
	// filepath.EvalSymlinks's 255.
	const hops = 10
	target := "src"
	for i := hops - 1; i >= 0; i-- {
		name := fmt.Sprintf("link%d", i)
		if err := os.Symlink(target, filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
		target = name
	}
	opened := openWorktreeSource(t, repo, sourceOptions{})

	path := "link0/a.ts"
	if content, ok := opened.read(path); !ok || content != "export const a = 1;\n" {
		t.Fatalf("read(%q) = %q, %v; a %d-hop chain entirely inside the repository must still resolve", path, content, ok, hops)
	}
}

// TestExecuteOnlyRepositoryRootIsRefusedByTheListingPreflight pins where an
// execute-only repository root now stops. gitWorktreeSafeBeforeListing holds an
// os.Root over the repository before any listing runs (provider.go, "hold
// selected root"), so such a repository is refused by the LISTING, before
// openSource reaches its own os.OpenRoot at all. The permission branch below the
// listing is therefore defensive only — it still covers a root whose mode
// changes between the preflight and the read, and the listing paths that skip
// the preflight — and its behavior is pinned directly by the next test rather
// than through openSource.
func TestExecuteOnlyRepositoryRootIsRefusedByTheListingPreflight(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits, so this fixture proves nothing running as root")
	}
	repo, _ := newContainmentFixture(t)
	git(t, repo, "init")
	git(t, repo, "add", "src/a.ts")
	git(t, repo, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "-m", "init")
	if err := os.Chmod(repo, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })

	opened, err := openSource(t.Context(), repo, "", sourceOptions{})
	if err == nil {
		if opened.close != nil {
			_ = opened.close()
		}
		t.Fatal("openSource accepted an execute-only repository root; the listing preflight is expected to refuse it")
	}
	if !strings.Contains(err.Error(), "hold selected root") {
		t.Fatalf("openSource error = %v; want the listing preflight's root-hold refusal", err)
	}
}

// TestManuallyConfinedWorktreeSourceReadsAndConfines pins the reader openSource
// falls back to when it cannot hold an os.Root: no closer, in-repository reads
// still work, and every escape the rooted reader refuses is refused here too.
func TestManuallyConfinedWorktreeSourceReadsAndConfines(t *testing.T) {
	t.Parallel()
	repo, outside := newContainmentFixture(t)
	if err := os.WriteFile(filepath.Join(outside, "big.env"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := openManuallyConfinedWorktreeSource(repo, []string{"src/a.ts"}, ignoreMatcher{}, nil, 0)
	capped := openManuallyConfinedWorktreeSource(repo, []string{"src/a.ts"}, ignoreMatcher{}, nil, 16)

	if opened.close != nil {
		t.Error("manually confined source returned a closer; no os.Root was ever opened")
	}
	if content, ok := opened.read("src/a.ts"); !ok || content != "export const a = 1;\n" {
		t.Fatalf("read(src/a.ts) = %q, %v; the fallback reader must still read a repository file", content, ok)
	}
	if prefix, ok := opened.readPrefix("src/a.ts", 6); !ok || prefix != "export" {
		t.Fatalf("readPrefix(src/a.ts, 6) = %q, %v; want \"export\", true", prefix, ok)
	}
	if content, ok := opened.read("../secret.env"); ok {
		t.Errorf("fallback read escaped the repository root and returned %q", content)
	}
	if prefix, ok := opened.readPrefix("../secret.env", 8); ok {
		t.Errorf("fallback readPrefix escaped the repository root and returned %q", prefix)
	}
	if content, ok := capped.read("../big.env"); ok {
		t.Errorf("fallback read escaped the repository root and returned %q", content)
	}
	if record, ok := capped.oversize("../big.env"); ok {
		t.Errorf("fallback oversize registry described a file above the repository root: %+v", record)
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
