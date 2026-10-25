package gitutil

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func grepTreeFilesFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	// Blobs must hold the bytes written: Git for Windows enables autocrlf
	// system-wide, which would fold the CRLF the line-splitting test pins.
	git(t, repo, "config", "core.autocrlf", "false")
	for path, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func grepTreeFiles(t *testing.T, repo, treeish string, patterns, exclude []string) []string {
	t.Helper()
	var paths []string
	if err := GrepTreePatternFiles(t.Context(), repo, treeish, patterns, exclude, func(path string) error {
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}

// Exclusion is what keeps the alias grep off blobs the corpus refused, so an
// excluded path must never be reported while every other match still is.
func TestGrepTreePatternFilesSkipsExcludedPaths(t *testing.T) {
	repo := grepTreeFilesFixture(t, map[string]string{
		"src/value.go":            "func readInt() {}\n",
		"vendor/grammar/parser.c": "int readInt(void);\n",
		"vendor/grammar/keep.c":   "int readInt(void);\n",
	})
	patterns := []string{"readInt"}
	if got := grepTreeFiles(t, repo, "HEAD", patterns, nil); !reflect.DeepEqual(got, []string{"src/value.go", "vendor/grammar/keep.c", "vendor/grammar/parser.c"}) {
		t.Fatalf("fixture drift, unexcluded matches = %q", got)
	}
	got := grepTreeFiles(t, repo, "HEAD", patterns, []string{"vendor/grammar/parser.c", "does/not/exist.c"})
	if want := []string{"src/value.go", "vendor/grammar/keep.c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matches with parser.c excluded = %q, want %q", got, want)
	}
}

// -l reports each matching file once however many lines match, which is what
// lets Git stop reading a file at its first match.
func TestGrepTreePatternFilesReportsEachFileOnce(t *testing.T) {
	repo := grepTreeFilesFixture(t, map[string]string{
		"many.go": strings.Repeat("func readInt() {}\n", 50),
	})
	if got := grepTreeFiles(t, repo, "HEAD", []string{"readInt"}, nil); !reflect.DeepEqual(got, []string{"many.go"}) {
		t.Fatalf("matches = %q, want one record for many.go", got)
	}
}

// macOS Git precomposes Unicode pathspec arguments, so excluding a decomposed
// name would drop its precomposed twin. Non-ASCII names must therefore never
// be excluded: the twin is a file the caller wants searched.
func TestGrepTreePatternFilesNeverExcludesNonASCIIPaths(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "core.precomposeUnicode", "true")
	blob := gitInputOutput(t, repo, "func readInt() {}\n", "hash-object", "-w", "--stdin")
	precomposed, decomposed := "café.go", "café.go"
	var tree strings.Builder
	for _, name := range []string{precomposed, decomposed} {
		fmt.Fprintf(&tree, "100644 blob %s\t%s\x00", blob, name)
	}
	treeID := gitInputOutput(t, repo, tree.String(), "mktree", "-z")
	commit := gitInputOutput(t, repo, "", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", treeID, "-m", "x")
	got := grepTreeFiles(t, repo, commit, []string{"readInt"}, []string{decomposed})
	found := false
	for _, path := range got {
		found = found || path == precomposed
	}
	if !found {
		t.Fatalf("excluding the decomposed name dropped the precomposed file: matches = %q", got)
	}
}

func TestGrepExcludePathspecsHonoursBudgetAndOrder(t *testing.T) {
	long := strings.Repeat("d/", grepExcludePathspecBytes/2) + "f.c"
	var paths []string
	paths = append(paths, "first.c", long, "café.c", "tab\tname.c")
	for index := 0; index < 2000; index++ {
		paths = append(paths, fmt.Sprintf("vendor/generated/file_%04d.c", index))
	}
	pathspecs := grepExcludePathspecs(paths)
	if len(pathspecs) < 2 || pathspecs[0] != grepExcludePathspecMagic+"first.c" || pathspecs[1] != grepExcludePathspecMagic+"vendor/generated/file_0000.c" {
		var head []string
		for _, pathspec := range pathspecs[:min(3, len(pathspecs))] {
			head = append(head, pathspec[:min(60, len(pathspec))])
		}
		t.Fatalf("pathspecs lost the caller's order or kept an unfit path: %q", head)
	}
	used := 0
	for index, pathspec := range pathspecs {
		path := strings.TrimPrefix(pathspec, grepExcludePathspecMagic)
		if path == long || !printableASCII(path) {
			t.Fatalf("pathspec %d excludes an unfit path %q", index, path[:min(60, len(path))])
		}
		used += grepExcludePathspecCost(path)
	}
	if used > grepExcludePathspecBytes {
		t.Fatalf("pathspecs cost %d bytes, over the %d budget", used, grepExcludePathspecBytes)
	}
	if len(pathspecs) == len(paths)-3 {
		t.Fatalf("budget never applied: all %d fit paths were excluded", len(pathspecs))
	}
}

func TestListTreeBlobsReportsSizesRelativeToRepo(t *testing.T) {
	repo := grepTreeFilesFixture(t, map[string]string{
		"top.txt":      "12345",
		"sub/inner.go": "package sub\n",
	})
	blobs, err := ListTreeBlobs(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := []TreeBlob{{Path: "sub/inner.go", Size: 12}, {Path: "top.txt", Size: 5}}; !reflect.DeepEqual(blobs, want) {
		t.Fatalf("blobs = %#v, want %#v", blobs, want)
	}
	blobs, err = ListTreeBlobs(t.Context(), filepath.Join(repo, "sub"), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if want := []TreeBlob{{Path: "inner.go", Size: 12}}; !reflect.DeepEqual(blobs, want) {
		t.Fatalf("subdirectory blobs = %#v, want %#v", blobs, want)
	}
}

func TestScanBlobLinesAtRevStopsAtFirstAcceptedLine(t *testing.T) {
	repo := grepTreeFilesFixture(t, map[string]string{
		"big.txt": "alpha\nbeta\r\n\ngamma",
	})
	var seen []string
	found, err := ScanBlobLinesAtRev(t.Context(), repo, "HEAD", "big.txt", func(line string) bool {
		seen = append(seen, line)
		return line == "beta\r"
	})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want the beta line accepted", found, err)
	}
	if want := []string{"alpha", "beta\r"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("visited %q, want %q (stop at the accepted line, split on LF only)", seen, want)
	}
	seen = nil
	found, err = ScanBlobLinesAtRev(t.Context(), repo, "HEAD", "big.txt", func(line string) bool {
		seen = append(seen, line)
		return false
	})
	if err != nil || found {
		t.Fatalf("found=%v err=%v, want no line accepted", found, err)
	}
	if want := []string{"alpha", "beta\r", "", "gamma"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("visited %q, want every line including the unterminated last one", seen)
	}
	if _, err := ScanBlobLinesAtRev(t.Context(), repo, "HEAD", "missing.txt", func(string) bool { return true }); err == nil {
		t.Fatal("a missing blob reported no error")
	}
}
