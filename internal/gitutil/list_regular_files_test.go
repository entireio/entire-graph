package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsRegularTreeMode(t *testing.T) {
	regular := []string{"100644", "100755"}
	other := []string{"120000", "160000", "040000", "40000", "", "notamode", "100000x"}
	for _, mode := range regular {
		if !isRegularTreeMode(mode) {
			t.Errorf("isRegularTreeMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range other {
		if isRegularTreeMode(mode) {
			t.Errorf("isRegularTreeMode(%q) = true, want false", mode)
		}
	}
}

// TestListRegularFilesExcludesSymlinksAndGitlinks is the regression: a tracked
// symlink is a blob whose content is its target path, so listing it as a source
// file feeds that path to a parser as if it were code.
func TestListRegularFilesExcludesSymlinksAndGitlinks(t *testing.T) {
	repo := newTreeFixture(t)

	all, err := ListFiles(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	regular, err := ListRegularFiles(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// ListFiles is deliberately unchanged: its other caller enumerates paths for
	// git log co-change, where a symlink costs nothing.
	if want := []string{"exec.sh", "link.go", "nested/deep.go", "real.go"}; !reflect.DeepEqual(all, want) {
		t.Errorf("ListFiles = %v, want %v", all, want)
	}
	if want := []string{"exec.sh", "nested/deep.go", "real.go"}; !reflect.DeepEqual(regular, want) {
		t.Errorf("ListRegularFiles = %v, want %v", regular, want)
	}
}

// newTreeFixture builds a repository carrying one of each tree mode this
// repository can produce without a network: a regular file, an executable, a
// nested regular file, and a symlink. This repository's own HEAD has no
// non-regular entries, so the regression is not reachable without a fixture.
func newTreeFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--quiet", "-b", "trunk")
	git("config", "user.name", "Entire Graph Test")
	git("config", "user.email", "graph@example.com")
	git("config", "commit.gpgsign", "false")
	git("config", "tag.gpgSign", "false")
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("real.go", "package a\n\nfunc A() {}\n", 0o644)
	write("exec.sh", "#!/bin/sh\necho hi\n", 0o755)
	write("nested/deep.go", "package nested\n", 0o644)
	if err := os.Symlink("real.go", filepath.Join(repo, "link.go")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	git("add", "-A")
	git("commit", "-m", "fixture")
	return repo
}
