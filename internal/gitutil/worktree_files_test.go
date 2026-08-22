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

// TestSplitNULDirectoryEntriesKeepsOnlyCollapsedDirectories reproduces the
// trail finding on ListIgnoredWorktreeDirectoryEntries: `git ls-files
// --directory` only collapses a directory to a single trailing-slash entry
// when its ENTIRE content is classified the same way. A directory ignored
// only by file-pattern rules alongside other content is not collapsed, so git
// lists every one of its matched files individually — and the only consumer
// of this listing (the git-directory sweep's root list) already discards
// every entry without a trailing slash, so parsing them at all bought nothing
// but memory and CPU sized to every ignored file in the tree. This pins that
// the split itself now drops non-directory entries instead of materializing
// them.
func TestSplitNULDirectoryEntriesKeepsOnlyCollapsedDirectories(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		"build/",         // whole directory collapsed: keep
		"vendor/pkg.o",   // pattern-ignored file inside a mixed directory: drop
		"vendor/pkg2.o",  // same, a second one, also a duplicate-prefix check
		"dist/",          // another collapsed directory: keep
		"",               // trailing NUL from -z output: drop
		"README.md",      // an ordinary file entry with no trailing slash: drop
		"build/",         // duplicate of an already-kept directory: dedup
	}, "\x00")

	got := splitNULDirectoryEntries(raw)
	want := []string{"build/", "dist/"}
	if len(got) != len(want) {
		t.Fatalf("splitNULDirectoryEntries(...) = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitNULDirectoryEntries(...) = %#v, want %#v", got, want)
		}
	}
}

// TestListIgnoredWorktreeDirectoryEntriesDropsPatternIgnoredFilenames is the
// end-to-end form of the same finding: a real git checkout where a directory
// is ignored only by a file-pattern rule, mixed with content that is NOT
// ignored, so `--directory` cannot collapse it. Before the fix, every ignored
// filename inside such a directory reached the caller; after it, none does —
// the caller only ever wanted directory roots for the git-directory sweep.
func TestListIgnoredWorktreeDirectoryEntriesDropsPatternIgnoredFilenames(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, ".gitignore", "*.o\n")
	write(t, repo, "src/keep.go", "package src\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "tracked")

	// vendor/ mixes a pattern-ignored file with an untracked-but-not-ignored
	// one, so git cannot collapse the whole directory to "vendor/".
	write(t, repo, "vendor/dep.o", "object\n")
	write(t, repo, "vendor/keep.txt", "not ignored\n")
	// wholly ignored, and nothing else in it: this one DOES collapse.
	write(t, repo, "cache/a.o", "object\n")
	write(t, repo, "cache/b.o", "object\n")

	entries, err := ListIgnoredWorktreeDirectoryEntries(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry, "/") {
			t.Fatalf("listing leaked a non-directory entry %q: %#v", entry, entries)
		}
	}
	if !slices.Contains(entries, "cache/") {
		t.Fatalf("listing missing wholly-ignored directory %q: %#v", "cache/", entries)
	}
	if slices.Contains(entries, "vendor/") {
		t.Fatalf("listing collapsed a MIXED directory to %q, which git itself would not have done: %#v", "vendor/", entries)
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
