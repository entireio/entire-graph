package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestGrepTreePathsInFilesMatchesFilteredWholeTree(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	contents := map[string]string{
		"src/text.go": "// ScOpEdNeEdLe\n", "src/binary.go": "// scopedneedle\x00\n",
		"text.go":    "// scopedneedle outside subdirectory\n",
		"ignored.go": "// scopedneedle ignored\n", "src/absent.go": "// no match\n",
		"src/space name.go": "// scopedneedle space\n", "-leading.go": "// scopedneedle leading\n",
	}
	files := []string{"src/text.go", "src/binary.go", "src/absent.go", "src/space name.go", "-leading.go"}
	if runtime.GOOS != "windows" {
		contents["src/[abc]*?.go"] = "// scopedneedle literal\n"
		contents["src/abc-other.go"] = "// scopedneedle glob decoy\n"
		contents[":(exclude)magic.go"] = "// scopedneedle magic\n"
		contents["src/line\nbreak:target.go"] = "// scopedneedle newline\n"
		files = append(files, "src/[abc]*?.go", ":(exclude)magic.go", "src/line\nbreak:target.go")
	}
	for path, content := range contents {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	// A committed-tree scan must not see these dirty bytes.
	if err := os.WriteFile(filepath.Join(repo, "src/text.go"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_LITERAL_PATHSPECS", "1") // the Git subprocess must neutralize this
	files = append(files, files[0])        // duplicates must not duplicate postings across batches
	original := append([]string(nil), files...)
	allowed := map[string]bool{}
	for _, path := range files {
		allowed[path] = true
	}
	for _, textOnly := range []bool{true, false} {
		whole, err := grepTreePaths(t.Context(), repo, "HEAD", []string{"scopedneedle"}, textOnly, false)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, path := range whole {
			if allowed[path] {
				want = append(want, path)
			}
		}
		sort.Strings(want)
		for _, budget := range []int{1, grepPathBatchBytes()} {
			got, err := grepTreePathsInFiles(t.Context(), repo, "HEAD", []string{"scopedneedle"}, files, textOnly, budget)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("textOnly=%v budget=%d: got %q, want %q", textOnly, budget, got, want)
			}
		}
	}
	if !reflect.DeepEqual(files, original) {
		t.Fatal("mutated caller inventory")
	}
	// --repo may be a subtree. Its inventory and Git's emitted paths are
	// relative to that directory, not the checkout root (which has a decoy).
	subtree, err := GrepTreePathsIncludingBinaryInFiles(t.Context(), filepath.Join(repo, "src"), "HEAD", []string{"scopedneedle"}, []string{"text.go"})
	if err != nil || !reflect.DeepEqual(subtree, []string{"text.go"}) {
		t.Fatalf("subdirectory grep = %q, %v", subtree, err)
	}
	got, err := GrepTreePathsInFiles(t.Context(), repo, "HEAD", []string{"no such needle"}, files)
	if err != nil || len(got) != 0 {
		t.Fatalf("no matches = %q, %v", got, err)
	}
}

func TestGrepTreePathsInFilesEmptyInvalidAndCancelled(t *testing.T) {
	for _, files := range [][]string{nil, {}} {
		got, err := GrepTreePathsIncludingBinaryInFiles(t.Context(), "nonexistent-repository", "HEAD", []string{"needle"}, files)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty inventory invoked Git: %q, %v", got, err)
		}
	}
	for _, tree := range []string{"", "--help", "HEAD\x00"} {
		if _, err := GrepTreePathsInFiles(t.Context(), "", tree, []string{"needle"}, nil); err == nil {
			t.Fatalf("accepted tree %q", tree)
		}
	}
	for _, path := range []string{"", "bad\x00path"} {
		if _, err := GrepTreePathsInFiles(t.Context(), "", "HEAD", []string{"needle"}, []string{path}); err == nil || !strings.Contains(err.Error(), "file name") {
			t.Fatalf("invalid path %q: %v", path, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := GrepTreePathsInFiles(ctx, "", "HEAD", []string{"needle"}, []string{"a.go"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled grep: %v", err)
	}
}

func TestGrepTreePathsInFilesDoesNotReadExcludedBlob(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for path, content := range map[string]string{"a.go": "// needle allowed\n", "z.go": "// needle ignored\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	blob := gitOutput(t, repo, "rev-parse", "HEAD:z.go")
	blobPath := filepath.Join(repo, ".git", "objects", blob[:2], blob[2:])
	if err := os.Chmod(blobPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	// This makes scanning-and-filtering observably different from avoiding the
	// blob entirely: the whole-tree control must fail to read it.
	if _, err := GrepTreePathsIncludingBinary(t.Context(), repo, "HEAD", []string{"needle"}); err == nil {
		t.Fatal("whole-tree control did not encounter missing blob")
	}
	got, err := GrepTreePathsIncludingBinaryInFiles(t.Context(), repo, "HEAD", []string{"needle"}, []string{"a.go"})
	if err != nil || !reflect.DeepEqual(got, []string{"a.go"}) {
		t.Fatalf("scoped grep = %q, %v", got, err)
	}
	// A failure after a successful first batch must not leak incomplete postings.
	got, err = grepTreePathsInFiles(t.Context(), repo, "HEAD", []string{"needle"}, []string{"a.go", "z.go"}, false, 1)
	if err == nil || len(got) != 0 {
		t.Fatalf("partial postings escaped failed batch: %q, %v", got, err)
	}
}
