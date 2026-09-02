package sem

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFallbackRefusesAReplacedRepositoryRoot is the TOCTOU the os.Root pin
// exists to close, on the one path that does not go through the root.
//
// openSource pins the worktree with os.OpenRoot, but the fallback readers take
// the repository PATHNAME and re-resolve it: filepath.Join, EvalSymlinks and
// ReadFile all run at read time. If the pathname is a symlink (or is renamed and
// replaced) after the root is pinned, a path that triggers the fallback is
// validated against, and read from, the replacement tree — so content from
// outside the originally selected worktree reaches output while the held root
// still points at the tree the caller asked for.
func TestFallbackRefusesAReplacedRepositoryRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("repointing a directory symlink mid-scan is not the Windows shape of this race")
	}
	parent := t.TempDir()
	selected := filepath.Join(parent, "selected")
	replacement := filepath.Join(parent, "replacement")
	for _, dir := range []string{selected, replacement} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(selected, "app.go"), []byte("package selected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "app.go"), []byte("package replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(parent, "repo")
	if err := os.Symlink(selected, repo); err != nil {
		t.Skipf("filesystem does not support the symlinked repository root: %v", err)
	}

	// What openSource does: pin the worktree once, for the source's lifetime.
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	pinned := pinnedRootIdentity(root, repo)
	if pinned == nil {
		t.Fatal("the pinned root has no identity to compare against")
	}

	// Sanity: before the swap the fallback answers from the selected tree.
	if got, ok := readFallback(pinned, repo, "app.go", 0, nil); !ok || got != "package selected\n" {
		t.Fatalf("fallback before the swap = %q, ok=%v; want the selected worktree", got, ok)
	}

	// The race: the pathname is repointed while the root descriptor still holds
	// the tree the caller selected.
	if err := os.Remove(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, repo); err != nil {
		t.Fatal(err)
	}

	got, ok := readFallback(pinned, repo, "app.go", 0, nil)
	if ok {
		t.Fatalf("fallback read %q from the REPLACEMENT tree after the root was pinned elsewhere", got)
	}
	if _, ok := readPrefixFallback(pinned, repo, "app.go", 8); ok {
		t.Fatal("prefix fallback read from the replacement tree after the root was pinned elsewhere")
	}

	// Non-over-refusal: the pinned tree itself is still readable by its real path.
	if got, ok := readFallback(pinnedRootIdentity(nil, selected), selected, "app.go", 0, nil); !ok || got != "package selected\n" {
		t.Fatalf("a repository read through its own unchanged path = %q, ok=%v; want the file", got, ok)
	}
}
