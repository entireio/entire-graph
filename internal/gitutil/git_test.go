package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestListFilesHandlesNewlinesInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	path := "dir/line\nbreak.py"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("def ok():\n    return True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add newline path")

	files, err := ListFiles(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != path {
		t.Fatalf("files = %#v, want %#v", files, []string{path})
	}
}

func TestChangedFilesHandlesNewlinesAndTabsInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Sem Test")
	git(t, repo, "config", "user.email", "sem@example.com")

	path := "dir/line\nbreak\tfile.py"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("def ok():\n    return True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add path")
	base := gitOutput(t, repo, "rev-parse", "HEAD")

	if err := os.WriteFile(full, []byte("def ok():\n    return False\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update path")
	head := gitOutput(t, repo, "rev-parse", "HEAD")

	files, err := ChangedFiles(t.Context(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Status != "M" || files[0].Path != path {
		t.Fatalf("files = %#v, want modified path %#v", files, path)
	}
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
