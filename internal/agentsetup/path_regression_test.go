package agentsetup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestResolveContainedLandingKeepsAliasesConfined(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this regression uses native Unix symlink spelling")
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	tests := []struct {
		name, want string
		missing    bool
	}{
		{name: "alias/file", want: filepath.Join("dir", "file")},
		{name: "alias/", want: "dir" + string(filepath.Separator)},
		{name: "dir/new", want: filepath.Join("dir", "new"), missing: true},
		{name: "dir/new/", want: filepath.Join("dir", "new") + string(filepath.Separator), missing: true},
		{name: "escape", want: "escape"},
		{name: "alias/../file", want: "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveContainedLanding(root, tt.name, tt.missing)
			if err != nil || got != tt.want {
				t.Fatalf("resolveContainedLanding(%q, missing=%v) = %q, %v; want %q", tt.name, tt.missing, got, err, tt.want)
			}
		})
	}
	if got, err := resolveContainedLanding(root, "../outside", false); err != nil || got != "../outside" {
		t.Fatalf("above-root spelling should be left for os.Root: %q, %v", got, err)
	}
	if _, err := resolveContainedLanding(root, "dir/file/", false); err == nil {
		t.Fatal("trailing separator on a regular file accepted")
	}
	if got, err := resolveContainedLanding(root, "dir/./", false); err != nil || got != "dir"+string(filepath.Separator) {
		t.Fatalf("terminal dot directory = %q, %v", got, err)
	}
}

func TestResolveContainedLandingRejectsSymlinkHopLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix symlinks are required")
	}
	repo := t.TempDir()
	if err := os.Symlink("b", filepath.Join(repo, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(repo, "b")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, err = resolveContainedLanding(root, "a", false)
	if err == nil || !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("loop resolution error = %v, want ELOOP", err)
	}
}

func TestResolvePathAndCountLinksReportsHopsAndTraversalFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix symlinks are required")
	}
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "dir", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(base, "one")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one", filepath.Join(base, "two")); err != nil {
		t.Fatal(err)
	}
	resolved, hops, err := resolvePathAndCountLinks(filepath.Join(base, "two", ".", "file"))
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := filepath.EvalSymlinks(filepath.Join(base, "two", ".", "file"))
	if err != nil {
		t.Fatal(err)
	}
	if hops < 2 || resolved != wantResolved {
		t.Fatalf("resolved=%q hops=%d err=%v; want %q and at least 2 hops", resolved, hops, err, wantResolved)
	}
	if _, _, err := resolvePathAndCountLinks(filepath.Join(base, "dir", "file", "child")); err == nil || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("file traversal error = %v, want ENOTDIR", err)
	}
	if _, _, err := resolvePathAndCountLinks(filepath.Join(base, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing path error = %v, want not-exist", err)
	}
}

func TestResolvePathAndCountLinksCountsAbsolutePrefixAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix symlinks are required")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	actual := filepath.Join(base, "actual")
	if err := os.MkdirAll(filepath.Join(actual, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, root); err != nil {
		t.Fatal(err)
	}
	resolved, hops, err := resolvePathAndCountLinks(filepath.Join(root, "nested"))
	wantResolved, evalErr := filepath.EvalSymlinks(filepath.Join(root, "nested"))
	if err != nil || evalErr != nil || hops < 1 || resolved != wantResolved {
		t.Fatalf("absolute prefix resolution = %q, %d, %v; want %q and at least 1 hop", resolved, hops, err, wantResolved)
	}
}
