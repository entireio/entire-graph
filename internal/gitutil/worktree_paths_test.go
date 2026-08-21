package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const boundedPathOutputHelperEnv = "ENTIRE_GRAPH_TEST_BOUNDED_PATH_OUTPUT_HELPER"

func TestLiteralPathspecBatchEndLeavesWindowsCommandLineHeadroom(t *testing.T) {
	const (
		literalPrefix         = ":(literal)"
		windowsCommandLineMax = 32767
		fixedArgumentHeadroom = 1024
	)
	worstEscapedUnits := 2*literalPathspecBatchBytes +
		2*literalPathspecBatchCount + fixedArgumentHeadroom
	if worstEscapedUnits >= windowsCommandLineMax {
		t.Fatalf("worst-case escaped batch uses %d UTF-16 units, want less than %d", worstEscapedUnits, windowsCommandLineMax)
	}

	second := "b"
	firstAtLimit := strings.Repeat("a",
		literalPathspecBatchBytes-2*len(literalPrefix)-len(second),
	)
	if got := literalPathspecBatchEnd([]string{firstAtLimit, second}, 0); got != 2 {
		t.Fatalf("batch end at byte limit = %d, want 2", got)
	}
	firstOverLimit := firstAtLimit + "a"
	if got := literalPathspecBatchEnd([]string{firstOverLimit, second}, 0); got != 1 {
		t.Fatalf("batch end over byte limit = %d, want 1", got)
	}

	paths := make([]string, literalPathspecBatchCount+1)
	if got := literalPathspecBatchEnd(paths, 0); got != literalPathspecBatchCount {
		t.Fatalf("batch end over path-count limit = %d, want %d", got, literalPathspecBatchCount)
	}
}

func TestClassifyWorktreePathsBoundsStagedSubtreeConflict(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	write(t, repo, "src.go/staged.go", "package staged\n")
	gitCmd(t, repo, "add", "src.go/staged.go")
	stagedDirectory := filepath.Join(repo, "src.go")
	movedDirectory := filepath.Join(t.TempDir(), "staged-src.go")
	if err := os.Rename(stagedDirectory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "src.go", "package src\n")

	_, _, err := ClassifyWorktreePaths(t.Context(), repo, []string{"src.go"})
	if err == nil {
		t.Fatal("staged descendants of the requested regular file were accepted")
	}
	if !strings.Contains(err.Error(), "unexpected path") {
		t.Fatalf("ClassifyWorktreePaths error = %q, want unexpected path", err)
	}
}

func TestRunBoundedPathOutputRejectsUnexpectedStreamingOutput(t *testing.T) {
	if os.Getenv(boundedPathOutputHelperEnv) != "" {
		record := []byte("src.go/unexpected-staged-descendant.go\x00")
		for emitted := 0; emitted <= 8<<20; emitted += len(record) {
			if _, err := os.Stdout.Write(record); err != nil {
				time.Sleep(time.Hour)
				return
			}
		}
		time.Sleep(time.Hour)
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunBoundedPathOutputRejectsUnexpectedStreamingOutput$")
	t.Cleanup(func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	cmd.Env = append(os.Environ(), boundedPathOutputHelperEnv+"=1")
	cmd.WaitDelay = time.Second

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	_, err := runBoundedPathOutput(cmd, map[string]struct{}{"src.go": {}})
	elapsed := time.Since(started)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if cmd.ProcessState == nil {
		t.Fatal("runBoundedPathOutput did not reap helper process")
	}
	if err == nil {
		t.Fatal("unexpected streamed descendant was accepted")
	}
	if !strings.Contains(err.Error(), "unexpected path") {
		t.Fatalf("runBoundedPathOutput error = %q, want unexpected path", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("runBoundedPathOutput did not kill and reap helper before timeout after %s: %v", elapsed, ctx.Err())
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("runBoundedPathOutput took %s to reject and reap helper, want less than 5s", elapsed)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("runBoundedPathOutput allocated %d bytes for streamed output, want at most %d", allocated, 4<<20)
	}
}

func TestClassifyWorktreePathsUsesEffectiveExcludesAndLiteralNames(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, "nested/.gitignore", "*.generated\n")
	write(t, repo, "global-excludes", "*.private\n")
	gitCmd(t, repo, "config", "core.excludesFile", filepath.Join(repo, "global-excludes"))
	write(t, repo, ".git/info/exclude", "*.local-only\n")
	write(t, repo, "nested/[literal].generated", "generated\n")
	write(t, repo, "nested/l.generated", "competing glob match\n")
	write(t, repo, "nested/also.private", "private\n")
	write(t, repo, "nested/also.local-only", "local\n")
	write(t, repo, "nested/keep.go", "package keep\n")

	eligible, classifiedIgnored, err := ClassifyWorktreePaths(t.Context(), repo, []string{
		"nested/[literal].generated",
		"nested/also.private",
		"nested/also.local-only",
		"nested/keep.go",
		".git/config",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eligible["nested/keep.go"]; !ok {
		t.Fatalf("eligible worktree file was not classified: %#v", eligible)
	}
	for _, path := range []string{
		"nested/[literal].generated",
		"nested/also.private",
		"nested/also.local-only",
	} {
		if _, ok := classifiedIgnored[path]; !ok {
			t.Errorf("ignored worktree file %q was not classified: %#v", path, classifiedIgnored)
		}
	}
	if _, ok := eligible[".git/config"]; ok {
		t.Fatal("Git-internal path was classified as eligible")
	}
	if _, ok := classifiedIgnored[".git/config"]; ok {
		t.Fatal("Git-internal path was classified as explicitly re-includable")
	}
}

func TestIndexHasFilesUnderUsesLiteralPathspec(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, "src/build/a.go", "package build\n")
	write(t, repo, "vendor/[tracked]/a.go", "package tracked\n")
	write(t, repo, "vendor/m/a.go", "package m\n")
	write(t, repo, "build", "tracked file named build\n")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "tracked")

	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "src/build", want: true},
		{path: "build", want: false},
		{path: "vendor/[tracked]", want: true},
		// Without :(literal), [m] would match the tracked vendor/m directory.
		{path: "vendor/[m]", want: false},
	} {
		got, err := IndexHasFilesUnder(t.Context(), repo, test.path)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("IndexHasFilesUnder(%q) = %v, want %v", test.path, got, test.want)
		}
	}

	// The helper is a read-only probe.
	if _, err := os.Stat(filepath.Join(repo, "vendor", "[tracked]", "a.go")); err != nil {
		t.Fatal(err)
	}
}

func TestIndexHasFilesUnderRejectsDirectoryFileConflictExactFile(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, "build/tracked.py", "def tracked():\n    return 'tracked'\n")
	gitCmd(t, repo, "add", "build/tracked.py")
	gitCmd(t, repo, "commit", "-m", "tracked build directory")

	got, err := IndexHasFilesUnder(t.Context(), repo, "build")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("tracked descendant under build/ was not detected")
	}

	write(t, repo, "build/secret.py", "def secret():\n    return 'secret'\n")
	blob := gitInputOutput(t, repo, "tracked file named build\n", "hash-object", "-w", "--stdin")
	gitCmd(t, repo, "rm", "--cached", "-q", "build/tracked.py")
	gitCmd(t, repo, "update-index", "--add", "--cacheinfo", "100644", blob, "build")

	got, err = IndexHasFilesUnder(t.Context(), repo, "build")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("exact indexed file build was treated as a tracked descendant of build/")
	}
	if _, err := os.Stat(filepath.Join(repo, "build", "secret.py")); err != nil {
		t.Fatal(err)
	}
}

func TestTreeContainsPathsAndNestedIgnoreOrder(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, ".gitignore", "root only\n")
	write(t, repo, "[literal].go", "package literal\n")
	write(t, repo, "l.go", "package competitor\n")
	write(t, repo, "nested/.gitignore", "first\n")
	write(t, repo, "nested/deep/.gitignore", "second\n")
	gitCmd(t, repo, "add", "-f", ".")
	gitCmd(t, repo, "commit", "-m", "tree")

	members, err := TreeContainsPaths(t.Context(), repo, "HEAD", []string{
		"[literal].go",
		"nested",
		"missing.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := members["[literal].go"]; !ok {
		t.Fatalf("literal tree member was absent: %#v", members)
	}
	if _, ok := members["missing.go"]; ok {
		t.Fatalf("missing tree path was reported present: %#v", members)
	}
	if _, ok := members["nested"]; ok {
		t.Fatalf("tree directory was reported as a file member: %#v", members)
	}

	ignores, err := FirstTreeNestedIgnorePaths(t.Context(), repo, "HEAD", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nested/.gitignore"}; !reflect.DeepEqual(ignores, want) {
		t.Fatalf("first nested HEAD ignore paths = %v, want %v", ignores, want)
	}
}

func TestTreeContainsPathsSupportsNewlinePathFromTreeObject(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	blob := gitInputOutput(t, repo, "package newline\n", "hash-object", "-w", "--stdin")
	cmd := exec.Command("git", "mktree", "-z")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(fmt.Sprintf("100644 blob %s\tline\nbreak.go%c", blob, byte(0)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git mktree newline path: %v\n%s", err, out)
	}
	tree := strings.TrimSpace(string(out))

	members, err := TreeContainsPaths(t.Context(), repo, tree, []string{"line\nbreak.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := members["line\nbreak.go"]; !ok {
		t.Fatalf("newline tree member was absent: %#v", members)
	}
}

func TestTreeHelpersUseTheRepoSubdirectoryPathBasis(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, "outside.go", "package outside\n")
	write(t, repo, "scope/[literal].go", "package literal\n")
	write(t, repo, "scope/l.go", "package competitor\n")
	write(t, repo, "scope/nested/.gitignore", "*.generated\n")
	gitCmd(t, repo, "add", "-f", ".")
	gitCmd(t, repo, "commit", "-m", "tree")
	scope := filepath.Join(repo, "scope")

	members, err := TreeContainsPaths(t.Context(), scope, "HEAD", []string{
		"[literal].go",
		"outside.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := members["[literal].go"]; !ok {
		t.Fatalf("subdirectory-relative literal member was absent: %#v", members)
	}
	if _, ok := members["outside.go"]; ok {
		t.Fatalf("path outside the --repo subdirectory was admitted: %#v", members)
	}

	ignores, err := FirstTreeNestedIgnorePaths(t.Context(), scope, "HEAD", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nested/.gitignore"}; !reflect.DeepEqual(ignores, want) {
		t.Fatalf("subdirectory nested ignore paths = %v, want %v", ignores, want)
	}
}

func TestFirstTreeNestedIgnorePathsDoesNotCapOrdinaryTreeFiles(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")

	emptyBlob := gitInputOutput(t, repo, "", "hash-object", "-w", "--stdin")
	nestedTree := gitInputOutput(t, repo,
		fmt.Sprintf("100644 blob %s\t.gitignore%c", emptyBlob, byte(0)),
		"mktree", "-z",
	)
	const ordinaryFiles = (1 << 16) + 1
	var treeInput strings.Builder
	for index := 0; index < ordinaryFiles; index++ {
		fmt.Fprintf(&treeInput, "100644 blob %s\ta%05d.go%c", emptyBlob, index, byte(0))
	}
	fmt.Fprintf(&treeInput, "040000 tree %s\tnested%c", nestedTree, byte(0))
	tree := gitInputOutput(t, repo, treeInput.String(), "mktree", "-z")

	ignores, err := FirstTreeNestedIgnorePaths(t.Context(), repo, tree, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nested/.gitignore"}; !reflect.DeepEqual(ignores, want) {
		t.Fatalf("large-tree nested ignore paths = %v, want %v", ignores, want)
	}
	if _, err := FirstTreeNestedIgnorePaths(
		t.Context(), repo, tree, nestedIgnoreCandidateMaxCount+1,
	); err == nil {
		t.Fatal("oversized retained-candidate limit was accepted")
	}
}

func gitInputOutput(t *testing.T, repo, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestFirstWorktreeNestedIgnorePathsPreservesProviderOrder(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	write(t, repo, "b/.gitignore", "tracked\n")
	gitCmd(t, repo, "add", "b/.gitignore")
	gitCmd(t, repo, "commit", "-m", "tracked")
	write(t, repo, "a/.gitignore", "untracked\n")
	write(t, repo, ".git/info/exclude", "ignored/\n")
	write(t, repo, "ignored/.gitignore", "ignored\n")

	withoutIncludes, err := FirstWorktreeNestedIgnorePaths(t.Context(), repo, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/.gitignore", "b/.gitignore"}; !reflect.DeepEqual(withoutIncludes, want) {
		t.Fatalf("worktree nested ignore paths without explicit includes = %v, want %v", withoutIncludes, want)
	}

	paths, err := FirstWorktreeNestedIgnorePaths(t.Context(), repo, 3, func(path string) bool {
		return path == "ignored/.gitignore"
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/.gitignore", "b/.gitignore", "ignored/.gitignore"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("worktree nested ignore paths = %v, want %v", paths, want)
	}
}

func TestBoundedNestedIgnorePathsReportOverflow(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.name", "T")
	gitCmd(t, repo, "config", "user.email", "t@example.com")
	for _, name := range []string{"a/.gitignore", "b/.gitignore", "c/.gitignore"} {
		write(t, repo, name, "# policy\n")
	}
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "nested ignores")

	if _, err := BoundedTreeNestedIgnorePaths(t.Context(), repo, "HEAD", 2); err == nil ||
		!strings.Contains(err.Error(), "exceed 2 paths") {
		t.Fatalf("bounded tree nested-ignore overflow = %v", err)
	}
	if _, err := BoundedWorktreeNestedIgnorePaths(t.Context(), repo, 2, nil); err == nil ||
		!strings.Contains(err.Error(), "exceed 2 paths") {
		t.Fatalf("bounded worktree nested-ignore overflow = %v", err)
	}

	for _, list := range []func() ([]string, error){
		func() ([]string, error) {
			return BoundedTreeNestedIgnorePaths(t.Context(), repo, "HEAD", 3)
		},
		func() ([]string, error) {
			return BoundedWorktreeNestedIgnorePaths(t.Context(), repo, 3, nil)
		},
	} {
		paths, err := list()
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"a/.gitignore", "b/.gitignore", "c/.gitignore"}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("bounded nested-ignore paths = %v, want %v", paths, want)
		}
	}
}

func TestVisitWorktreePathsStreamsAndStopsAtVisitorBound(t *testing.T) {
	repo := t.TempDir()
	gitCmd(t, repo, "init")
	for index := 0; index < 20; index++ {
		write(t, repo, fmt.Sprintf("p-%02d.go", index), "package sample\n")
	}

	var got []string
	err := VisitWorktreePaths(t.Context(), repo, false, func(path string) bool {
		got = append(got, path)
		return len(got) < 3
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p-00.go", "p-01.go", "p-02.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded streamed worktree paths = %v, want %v", got, want)
	}
}
