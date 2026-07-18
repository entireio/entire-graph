package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestListFilesHandlesNewlinesInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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

func TestGrepIndexMatchesUsesFixedStringsAndUnstagedContent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for path, content := range map[string]string{
		"src/target.go": "package source\nfunc Initial() {}\n",
		"src/other.go":  "package source\nfunc Other() {}\n",
	} {
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
	if err := os.WriteFile(filepath.Join(repo, "src/target.go"), []byte("package source\nfunc NeedlePattern() {}\nfunc AnotherNeedlePattern() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, err := GrepIndexMatches(t.Context(), repo, []string{"NeedlePattern"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("grep match count = %d, want 2: %#v", len(matches), matches)
	}
	for _, match := range matches {
		if match.Path != "src/target.go" || match.Text != "NeedlePattern" {
			t.Fatalf("grep match = %#v, want path src/target.go and exact fixed-string text", match)
		}
	}
	empty, err := GrepIndexMatches(t.Context(), repo, []string{"absent-fixed-string"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("no-match grep results = %#v", empty)
	}
}

func TestGrepTreeMatchesUsesCommittedTreeAndStripsTreeishPrefix(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	path := "src/target:with-colon.go"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package source\nfunc CommittedNeedle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(full, []byte("package source\nfunc DirtyNeedle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := GrepTreeMatches(t.Context(), repo, "HEAD", []string{"CommittedNeedle"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(committed) != 1 || committed[0].Path != path || committed[0].Text != "CommittedNeedle" {
		t.Fatalf("committed grep = %#v", committed)
	}
	dirty, err := GrepTreeMatches(t.Context(), repo, "HEAD", []string{"DirtyNeedle"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 0 {
		t.Fatalf("tree grep observed dirty worktree content: %#v", dirty)
	}
}

func TestGrepTreePathsMatchesTextAPIAndHandlesUnusualPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	paths := []string{"src/ordinary.go", "src/line\nbreak:target.go"}
	for _, path := range paths {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package source\n// ExactTreeNeedle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	commit := gitOutput(t, repo, "rev-parse", "HEAD")

	got, err := GrepTreePaths(t.Context(), repo, commit, []string{"ExactTreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(paths)
	if !reflect.DeepEqual(got, paths) {
		t.Fatalf("path-only grep = %#v, want %#v", got, paths)
	}
	textMatches, err := GrepTreeMatches(t.Context(), repo, commit, []string{"ExactTreeNeedle"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	textPaths := make([]string, len(textMatches))
	for index, match := range textMatches {
		textPaths[index] = match.Path
	}
	sort.Strings(textPaths)
	if !reflect.DeepEqual(got, textPaths) {
		t.Fatalf("path-only/text grep mismatch: paths=%#v text=%#v", got, textPaths)
	}
	noHit, err := GrepTreePaths(t.Context(), repo, commit, []string{"AbsentTreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(noHit) != 0 {
		t.Fatalf("path-only no-hit grep = %#v", noHit)
	}
}

func TestChangedFilesHandlesNewlinesAndTabsInPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

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

func TestFileCochangesHandlesQuotedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`Windows filenames cannot contain '"' or '\'`)
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	// '"' and '\' force git to C-quote the path even under core.quotePath=false;
	// the non-ASCII byte is what plain quotePath would octal-escape. Only -z
	// yields the raw path that matches the snapshot's file keys.
	special := "dir/wéird\"na\\me.py"
	other := "dir/other.py"
	writeBoth := func(content string) {
		t.Helper()
		for _, p := range []string{special, other} {
			full := filepath.Join(repo, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, repo, "add", ".")
	}
	// Two commits touching both files so the pair's co-change count reaches 2.
	writeBoth("v1\n")
	git(t, repo, "commit", "-m", "add files")
	writeBoth("v2\n")
	git(t, repo, "commit", "-m", "update files")

	revision := gitOutput(t, repo, "rev-parse", "HEAD")
	pairs, err := FileCochanges(t.Context(), repo, revision, 256)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pairs {
		if (p.Left == special && p.Right == other) || (p.Left == other && p.Right == special) {
			found = true
			if p.Count < 2 {
				t.Fatalf("co-change count = %d, want >= 2", p.Count)
			}
		}
	}
	if !found {
		t.Fatalf("FileCochanges dropped the raw quoted-path pair; got %#v", pairs)
	}
}

func TestFileCochangesUsesExactRevision(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	writePair := func(paths [2]string, content string) {
		t.Helper()
		for _, path := range paths {
			full := filepath.Join(repo, path)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git(t, repo, "add", ".")
	}
	oldPair := [2]string{"old_a.go", "old_b.go"}
	writePair(oldPair, "v1\n")
	git(t, repo, "commit", "-m", "old pair one")
	writePair(oldPair, "v2\n")
	git(t, repo, "commit", "-m", "old pair two")
	pinned := gitOutput(t, repo, "rev-parse", "HEAD")

	newPair := [2]string{"new_a.go", "new_b.go"}
	writePair(newPair, "v1\n")
	git(t, repo, "commit", "-m", "new pair one")
	writePair(newPair, "v2\n")
	git(t, repo, "commit", "-m", "new pair two")

	pairs, err := FileCochanges(t.Context(), repo, pinned, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFileCochangePair(pairs, oldPair) {
		t.Fatalf("pinned history lost old pair: %#v", pairs)
	}
	if hasFileCochangePair(pairs, newPair) {
		t.Fatalf("pinned history leaked commits after %s: %#v", pinned, pairs)
	}
}

func hasFileCochangePair(pairs []FileCochange, paths [2]string) bool {
	for _, pair := range pairs {
		if (pair.Left == paths[0] && pair.Right == paths[1]) ||
			(pair.Left == paths[1] && pair.Right == paths[0]) {
			return true
		}
	}
	return false
}

func TestBatchFileReaderReadsMultipleFilesFromHead(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	for path, content := range map[string]string{
		"a.go":     "package a\nfunc A() {}\n",
		"dir/b.go": "package dir\nfunc B() {}\n",
	} {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add files")

	reader, err := NewBatchFileReader(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, path := range []string{"a.go", "dir/b.go"} {
		batched, ok, err := reader.ReadFile(path)
		if err != nil {
			t.Fatalf("batch read %s: %v", path, err)
		}
		if !ok {
			t.Fatalf("batch read %s: not found", path)
		}
		shown, ok, err := ShowFile(t.Context(), repo, "HEAD", path)
		if err != nil {
			t.Fatalf("show %s: %v", path, err)
		}
		if !ok || batched != shown {
			t.Fatalf("batch read %s = %q (ok %v), want %q", path, batched, ok, shown)
		}
	}
	if _, ok, err := reader.ReadFile("missing.go"); err != nil || ok {
		t.Fatalf("missing read ok=%v err=%v, want ok=false err=nil", ok, err)
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
