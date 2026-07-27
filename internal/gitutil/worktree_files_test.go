package gitutil

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListWorktreeFilesAppliesEveryExcludeSource pins the reason this function
// exists: it must report the same set `git status` would, including the exclude
// sources a root-.gitignore reader cannot see.
func TestListWorktreeFilesAppliesEveryExcludeSource(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, ".gitignore", "root_ignored/\n")
	write(t, repo, "excludes-global", "*.generated\n")
	gitCmd(t, repo, "config", "core.excludesFile", filepath.Join(repo, "excludes-global"))
	write(t, repo, ".git/info/exclude", "private/\n")
	write(t, repo, "src/keep.go", "package src\n")
	write(t, repo, "sub/.gitignore", "vendored/\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "tracked")

	write(t, repo, "src/untracked.go", "package src\n")
	write(t, repo, "root_ignored/a.go", "package a\n")
	write(t, repo, "private/b.go", "package b\n")
	write(t, repo, "sub/vendored/c.go", "package c\n")
	write(t, repo, "src/d.generated", "generated\n")

	files, err := ListWorktreeFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"src/keep.go", "src/untracked.go", ".gitignore", "sub/.gitignore"} {
		if !slices.Contains(files, want) {
			t.Fatalf("listing missing %q: %#v", want, files)
		}
	}
	for _, unwanted := range []string{
		"root_ignored/a.go", "private/b.go", "sub/vendored/c.go", "src/d.generated",
	} {
		if slices.Contains(files, unwanted) {
			t.Fatalf("listing included excluded path %q: %#v", unwanted, files)
		}
	}

	ignored, err := ListIgnoredWorktreeFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root_ignored/a.go", "private/b.go", "sub/vendored/c.go"} {
		if !slices.Contains(ignored, want) {
			t.Fatalf("ignored listing missing %q: %#v", want, ignored)
		}
	}
}

// TestBatchFileReaderRefusesOversizeBlob guards the read cap: an oversize blob is
// never materialized, and is still described exactly from the streamed digest.
func TestBatchFileReaderRefusesOversizeBlob(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	small := "package small\n"
	large := strings.Repeat("package large // padding\n", 4096) // ~100 KiB
	write(t, repo, "small.go", small)
	write(t, repo, "large.go", large)
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "two files")

	batch, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = batch.Close() }()
	batch.SetMaxBytes(int64(len(large)) - 1)

	content, ok, err := batch.ReadFile("large.go")
	if err != nil {
		t.Fatal(err)
	}
	if ok || content != "" {
		t.Fatalf("oversize blob was materialized: ok=%v bytes=%d", ok, len(content))
	}
	blob, isOversize := batch.OversizeBlob("large.go")
	if !isOversize {
		t.Fatal("oversize blob was not recorded")
	}
	sum := sha256.Sum256([]byte(large))
	if blob.Bytes != int64(len(large)) || blob.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("oversize blob = %#v, want bytes=%d hash=%s", blob, len(large), hex.EncodeToString(sum[:]))
	}
	if blob.Lines != strings.Count(large, "\n") {
		t.Fatalf("oversize blob lines = %d, want %d", blob.Lines, strings.Count(large, "\n"))
	}

	// The reader must still be usable: the refused blob was drained, not left in
	// the pipe.
	content, ok, err = batch.ReadFile("small.go")
	if err != nil || !ok || content != small {
		t.Fatalf("read after refusal: content=%q ok=%v err=%v", content, ok, err)
	}
	if _, isOversize := batch.OversizeBlob("small.go"); isOversize {
		t.Fatal("a blob under the cap was recorded as oversize")
	}
}
