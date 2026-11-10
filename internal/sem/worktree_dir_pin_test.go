package sem

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestContainedRealDirPinsTheDirectoryItValidated is the deterministic half of
// the check-then-open race.
//
// Validating a pathname and then opening that pathname are two resolutions of
// the same string, and the filesystem can change in between: an in-repository
// directory renamed and replaced with a symlink in that window made the open
// follow the replacement outside the repository, with the containment check
// already passed. A descriptor cannot be redirected that way, and this asserts
// the fallback holds one.
func TestContainedRealDirPinsTheDirectoryItValidated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("swapping a directory for a symlink mid-read is not the Windows shape of this race")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	inside := filepath.Join(repo, "sub")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inside, "app.go"), []byte("package inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "app.go"), []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoRoot, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer repoRoot.Close()
	pinned := pinnedRootIdentity(repoRoot)
	file, _, ok := openContainedRegularFile(pinned, repo, "sub/app.go")
	if !ok {
		t.Fatal("a plain in-repository path was refused")
	}
	defer file.Close()

	// The swap the finding describes, performed AFTER validation and before the
	// read the handle is about to do.
	if err := os.Rename(inside, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, inside); err != nil {
		t.Skipf("filesystem does not support the replacement symlink: %v", err)
	}

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "package inside\n" {
		t.Fatalf("the opened file followed the replacement: read %q, want the directory that was validated", content)
	}
}

// TestContainedRealDirOpensTheChildThroughThePinnedRepository schedules the
// exact pre-open swap that a pathname open follows outside the repository. The
// production opener is injected only at the final confined operation, after
// canonicalization has selected "sub" but before repoRoot.OpenRoot resolves it.
// A stable outside symlink at that point must be refused.
func TestContainedRealDirOpensTheChildThroughThePinnedRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming a directory beneath an open root is not portable on Windows")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	inside := filepath.Join(repo, "sub")
	stash := filepath.Join(parent, "stash")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inside, "app.go"), []byte("package inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "app.go"), []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer repoRoot.Close()
	pinned := pinnedRootIdentity(repoRoot)

	swapped := false
	dirRoot, _, ok := containedRealDirWithOpen(
		pinned,
		repo,
		"sub/app.go",
		func(root *os.Root, relative string) (*os.Root, error) {
			if err := os.Rename(inside, stash); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, inside); err != nil {
				t.Skipf("filesystem does not support the replacement symlink: %v", err)
			}
			swapped = true
			return root.OpenRoot(relative)
		},
	)
	if dirRoot != nil {
		_ = dirRoot.Close()
	}
	if swapped {
		if err := os.Remove(inside); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stash, inside); err != nil {
			t.Fatal(err)
		}
	}
	if !swapped {
		t.Fatal("test did not schedule the directory swap")
	}
	if ok {
		t.Fatal("confined child open accepted a directory replaced by an outside symlink")
	}
}

// TestFallbackNeverReadsThroughASwappedDirectory drives the whole race against
// readFallback. The assertion is one-directional -- it fails only when content
// from OUTSIDE the repository is actually observed -- so it cannot fail
// spuriously once the read goes through a pinned handle.
func TestFallbackNeverReadsThroughASwappedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("swapping a directory for a symlink mid-read is not the Windows shape of this race")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	inside := filepath.Join(repo, "sub")
	stash := filepath.Join(parent, "stash")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inside, "app.go"), []byte("package inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "app.go"), []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer repoRoot.Close()
	pinned := pinnedRootIdentity(repoRoot)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Swap the in-repository directory for a symlink pointing outside,
			// then put it back, as fast as the filesystem allows.
			if os.Rename(inside, stash) == nil {
				_ = os.Symlink(outside, inside)
				_ = os.Remove(inside)
				_ = os.Rename(stash, inside)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	for i := 0; i < 3000; i++ {
		if content, ok := readFallback(pinned, repo, "sub/app.go", 0, nil); ok {
			if strings.Contains(content, "package outside") {
				t.Fatalf("iteration %d: the fallback read content from outside the repository", i)
			}
		}
		if content, ok := readPrefixFallback(pinned, repo, "sub/app.go", 32); ok {
			if strings.Contains(content, "package outside") {
				t.Fatalf("iteration %d: the prefix fallback read content from outside the repository", i)
			}
		}
	}
}

// TestFallbackOpensOnlyThroughTheDirectoryHandle is a source-level guard for
// the two descriptor-relative opens the fallback's confinement depends on.
//
// The difference between opening the leaf through a pinned directory descriptor
// and re-opening a validated pathname shows up solely in the window between the
// containment check and the read, which no deterministic test can schedule. The
// structure, however, is checkable: openContainedRegularFile is the one place a
// fallback read turns a repository-relative path into an open file, and it must
// reach the filesystem only through the handle. A reviewer changing it back to a
// pathname open gets this failure instead of a race that reproduces on someone
// else's machine.
func TestFallbackOpensOnlyThroughTheDirectoryHandle(t *testing.T) {
	source, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	// Normalize first. Git for Windows checks this file out with CRLF endings, so
	// a "\n}\n" terminator never matches there and the scan below would run over
	// the WHOLE file -- reporting every os.Stat in the package as a finding
	// against this one function. A guard that reads the source has to be immune
	// to how the source was checked out.
	text := strings.ReplaceAll(string(source), "\r\n", "\n")
	const wrapperMarker = "func containedRealDir("
	wrapperStart := strings.Index(text, wrapperMarker)
	if wrapperStart < 0 {
		t.Fatalf("containedRealDir is gone; the confinement guard no longer describes the code")
	}
	wrapperBody := text[wrapperStart:]
	wrapperEnd := strings.Index(wrapperBody, "\n}\n")
	if wrapperEnd < 0 {
		t.Fatalf("could not find the end of containedRealDir; the guard cannot bound what it scans")
	}
	wrapperBody = wrapperBody[:wrapperEnd]
	if !strings.Contains(wrapperBody, "containedRealDirWithOpen(pinned, repo, relPath, (*os.Root).OpenRoot)") {
		t.Errorf("containedRealDir no longer wires the confined os.Root opener:\n%s", wrapperBody)
	}

	const marker = "func openContainedRegularFile("
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("openContainedRegularFile is gone; this guard no longer describes the code")
	}
	body := text[start:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of openContainedRegularFile; the guard cannot bound what it scans")
	}
	body = body[:end]
	if !strings.Contains(body, "dirRoot.Open(") || !strings.Contains(body, "dirRoot.Lstat(") {
		t.Errorf("the fallback no longer opens the leaf through the pinned directory handle:\n%s", body)
	}
	for _, banned := range []string{"os.Open(", "os.ReadFile(", "os.Lstat(", "os.Stat("} {
		if strings.Contains(body, banned) {
			t.Errorf("the fallback reaches the filesystem by pathname via %s, which the check-then-open race exploits:\n%s", banned, body)
		}
	}

	const confinedMarker = "func containedRealDirWithOpen("
	start = strings.Index(text, confinedMarker)
	if start < 0 {
		t.Fatalf("containedRealDirWithOpen is gone; the confinement guard no longer describes the code")
	}
	body = text[start:]
	end = strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of containedRealDirWithOpen; the guard cannot bound what it scans")
	}
	body = body[:end]
	if !strings.Contains(body, "repoRoot.Stat(\".\")") || !strings.Contains(body, "openDir(repoRoot, relativeDir)") {
		t.Errorf("the fallback no longer identity-checks and descends through the repository handle:\n%s", body)
	}
	for _, banned := range []string{"os.OpenRoot(realDir)", "os.Stat(confirmedDir)"} {
		if strings.Contains(body, banned) {
			t.Errorf("containedRealDir restored pathname-based child validation via %s:\n%s", banned, body)
		}
	}
}
