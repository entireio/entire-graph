package gitutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if runtime.GOOS == "windows" {
		// A colon names an NTFS alternate data stream on Windows rather than a
		// tracked file. Keep the cross-platform committed-tree assertion while
		// exercising the colon-delimited display-prefix case on platforms where
		// colons are valid path bytes.
		path = "src/target with space.go"
	}
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

// TestGrepTreePathsIncludingBinaryFindsFilesGrepTreePathsExcludes pins the
// only behavioral difference between the two functions: a blob Git itself
// classifies as binary because of an early embedded NUL byte. GrepTreePaths
// (which passes -I) must silently exclude it from its result even though it
// contains a matching pattern; GrepTreePathsIncludingBinary must still find
// it, and both functions must agree on an ordinary text file.
func TestGrepTreePathsIncludingBinaryFindsFilesGrepTreePathsExcludes(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	textPath := "src/text.go"
	binaryPath := "src/binary.go"
	for path, content := range map[string]string{
		textPath:   "package source\n// TreeNeedle\n",
		binaryPath: "package source\n// TreeNeedle\x00 embedded nul\n",
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
	commit := gitOutput(t, repo, "rev-parse", "HEAD")

	textOnly, err := GrepTreePaths(t.Context(), repo, commit, []string{"TreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(textOnly) != 1 || textOnly[0] != textPath {
		t.Fatalf("GrepTreePaths = %#v, want only %#v (the NUL-containing file must be excluded by -I)", textOnly, []string{textPath})
	}

	includingBinary, err := GrepTreePathsIncludingBinary(t.Context(), repo, commit, []string{"TreeNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(includingBinary)
	want := []string{binaryPath, textPath}
	if !reflect.DeepEqual(includingBinary, want) {
		t.Fatalf("GrepTreePathsIncludingBinary = %#v, want %#v", includingBinary, want)
	}
}

func TestGrepIndexMatchesPreservesUnicodeCaseFoldingLocale(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	const path = "src/unicode.go"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package source\n// wéird\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add unicode content")

	// Locale names vary across platforms. Find one under which this Git build
	// supports the Unicode case fold before asserting that GrepIndexMatches
	// preserves it; the regression forced LC_ALL=C after this probe succeeded.
	unicodeLocale := ""
	for _, candidate := range []string{"C.UTF-8", "en_US.UTF-8", "en_US.utf8"} {
		cmd := exec.Command("git", "grep", "-q", "-i", "-F", "-e", "WÉIRD", "--")
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), "LC_ALL="+candidate, "LANG="+candidate)
		if err := cmd.Run(); err == nil {
			unicodeLocale = candidate
			break
		}
	}
	if unicodeLocale == "" {
		t.Skip("installed Git has no available UTF-8 locale with Unicode case folding")
	}
	t.Setenv("LC_ALL", unicodeLocale)
	t.Setenv("LANG", unicodeLocale)

	matches, err := GrepIndexMatches(t.Context(), repo, []string{"WÉIRD"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != path || matches[0].Text != "wéird" {
		t.Fatalf("unicode grep matches = %#v, want one case-folded match in %s", matches, path)
	}
}

func TestTreeGrepsIgnoreReplaceRefsAndOverrideHostileEnvironment(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	originalOID := gitInputOutput(t, repo, "OriginalNeedle\n", "hash-object", "-w", "--stdin")
	replacementOID := gitInputOutput(t, repo, "replacement without it\n", "hash-object", "-w", "--stdin")
	tree := gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", originalOID, byte(0)), "mktree", "-z")
	git(t, repo, "update-ref", "refs/replace/"+originalOID, replacementOID)
	// Preserve a hostile inherited assignment and prove production commands
	// append their canonical raw-object override after it. The control below
	// removes the variable entirely so Git demonstrably honors the replacement.
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")
	if got := gitOutputHonoringReplaceRefs(t, repo, "cat-file", "-p", originalOID); got != "replacement without it" {
		t.Fatalf("control Git replacement view = %q, want replacement content", got)
	}

	paths, err := GrepTreePaths(t.Context(), repo, tree, []string{"OriginalNeedle"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"file.go"}) {
		t.Fatalf("raw tree grep paths = %#v, want file.go despite replacement", paths)
	}
	matches, err := GrepTreeMatches(t.Context(), repo, tree, []string{"OriginalNeedle"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != "file.go" || matches[0].Text != "OriginalNeedle" {
		t.Fatalf("raw tree grep matches = %#v, want original blob match", matches)
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

type countingWriteCloser struct {
	io.WriteCloser
	commands    []string
	beforeWrite func(string)
}

func (w *countingWriteCloser) Write(p []byte) (int, error) {
	command := string(p)
	if w.beforeWrite != nil {
		w.beforeWrite(command)
	}
	w.commands = append(w.commands, command)
	return w.WriteCloser.Write(p)
}

func (w *countingWriteCloser) countCommands(prefix string) int {
	count := 0
	for _, command := range w.commands {
		if strings.HasPrefix(command, prefix) {
			count++
		}
	}
	return count
}

func TestNewBatchFileReaderReportsUnsupportedBatchCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell shim")
	}
	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	const script = `#!/bin/sh
if [ "$1" = "rev-parse" ] && [ "$2" = "--show-prefix" ]; then
	printf '\n'
	exit 0
fi
if [ "$1" = "cat-file" ] && [ "$2" = "--batch-command" ]; then
	printf '%s\n' 'error: unknown option batch-command' >&2
	exit 129
fi
exit 2
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	reader, err := NewBatchFileReader(t.Context(), t.TempDir(), "HEAD")
	if reader != nil {
		_ = reader.Close()
		t.Fatal("unsupported batch-command returned a reader")
	}
	if err == nil || !strings.Contains(err.Error(), "Git 2.36 or newer required") ||
		!strings.Contains(err.Error(), "unknown option batch-command") {
		t.Fatalf("unsupported batch-command error = %v, want version requirement and Git diagnostic", err)
	}
}

func TestBatchFileReaderProtocolFailureIsStickyAndRetiresProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell protocol shim")
	}
	for _, test := range []struct {
		name      string
		mode      string
		maxBytes  int64
		wantError string
	}{
		{name: "wrong separator", mode: "separator", wantError: "missing trailing newline separator"},
		{name: "short oversized body", mode: "partial", maxBytes: 1, wantError: "blob body length 2, want 5"},
		{name: "mismatched info missing header", mode: "info-missing", wantError: `info missing header "other missing"`},
		{name: "mismatched contents missing header", mode: "contents-missing", wantError: `contents missing header "other missing"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			binDir := t.TempDir()
			requestLog := filepath.Join(t.TempDir(), "requests.log")
			fakeGit := filepath.Join(binDir, "git")
			const objectID = "1111111111111111111111111111111111111111"
			const script = `#!/bin/sh
if [ "$1" = "rev-parse" ] && [ "$2" = "--show-prefix" ]; then
	printf '\n'
	exit 0
fi
if [ "$1" = "cat-file" ] && [ "$2" = "--batch-command" ]; then
	while IFS= read -r line; do
		printf '%s\n' "$line" >> "$ENTIRE_GRAPH_BATCH_REQUEST_LOG"
		case "$line" in
			"info 0000000000000000000000000000000000000000")
				printf '%s missing\n' '0000000000000000000000000000000000000000'
				;;
			info\ *)
				if [ "$ENTIRE_GRAPH_BATCH_MODE" = "info-missing" ]; then
					printf 'other missing\n'
				elif [ "$ENTIRE_GRAPH_BATCH_MODE" = "partial" ]; then
					printf '%s blob 5\n' '1111111111111111111111111111111111111111'
				else
					printf '%s blob 3\n' '1111111111111111111111111111111111111111'
				fi
				;;
			contents\ *)
				if [ "$ENTIRE_GRAPH_BATCH_MODE" = "contents-missing" ]; then
					printf 'other missing\n'
				elif [ "$ENTIRE_GRAPH_BATCH_MODE" = "partial" ]; then
					printf '%s blob 5\nab' '1111111111111111111111111111111111111111'
					exit 0
				fi
				printf '%s blob 3\nabc!' '1111111111111111111111111111111111111111'
				;;
		esac
	done
	exit 0
fi
exit 2
`
			if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			t.Setenv("ENTIRE_GRAPH_BATCH_REQUEST_LOG", requestLog)
			t.Setenv("ENTIRE_GRAPH_BATCH_MODE", test.mode)

			reader, err := NewBatchFileReader(t.Context(), t.TempDir(), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			reader.SetMaxBytes(test.maxBytes)
			if _, ok, firstErr := reader.ReadFile("file.go"); firstErr == nil || ok ||
				!strings.Contains(firstErr.Error(), test.wantError) {
				t.Fatalf("first malformed response = (ok %v, err %v), want %q", ok, firstErr, test.wantError)
			} else {
				// Even a locally invalid path must return the sticky protocol cause after
				// poisoning, rather than hiding it or reaching stdin.
				if _, ok, secondErr := reader.ReadFile("../file.go"); secondErr == nil || ok || secondErr.Error() != firstErr.Error() {
					t.Fatalf("second read = (ok %v, err %v), want original poison %q", ok, secondErr, firstErr)
				}
				if closeErr := reader.Close(); closeErr == nil || closeErr.Error() != firstErr.Error() {
					t.Fatalf("close after poison = %v, want original protocol error %q", closeErr, firstErr)
				}
			}
			logged, err := os.ReadFile(requestLog)
			if err != nil {
				t.Fatal(err)
			}
			commands := strings.Split(strings.TrimSpace(string(logged)), "\n")
			want := []string{
				"info 0000000000000000000000000000000000000000",
				"info HEAD:file.go",
			}
			if test.mode != "info-missing" {
				want = append(want, "contents "+objectID)
			}
			if !reflect.DeepEqual(commands, want) {
				t.Fatalf("protocol commands after poison = %#v, want exactly %#v and no second request", commands, want)
			}
		})
	}
}

func TestBatchFileReaderRejectsNonBlobBeforeRequestingContent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, "plain.py"), []byte("def plain():\n    return True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "plain.py")
	git(t, repo, "commit", "-m", "gitlink target")
	target := gitOutput(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000", target, "module.py")
	git(t, repo, "commit", "-m", "record gitlink")

	reader, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	contentWrites := &countingWriteCloser{WriteCloser: reader.stdin}
	reader.stdin = contentWrites
	reader.SetMaxBytes(1)

	if content, ok, err := reader.ReadFile("module.py"); err != nil || ok || content != "" {
		t.Fatalf("gitlink batch read = (%q, %v, %v), want metadata-only refusal", content, ok, err)
	}
	if got := contentWrites.countCommands("contents "); got != 0 {
		t.Fatalf("gitlink issued %d content requests, want none: %#v", got, contentWrites.commands)
	}
	if got := contentWrites.countCommands("info "); got != 1 {
		t.Fatalf("gitlink issued %d metadata requests, want one: %#v", got, contentWrites.commands)
	}

	// A real blob still reaches the content batch. The one-byte ceiling makes it
	// stream through the existing oversize digest path rather than materializing.
	if content, ok, err := reader.ReadFile("plain.py"); err != nil || ok || content != "" {
		t.Fatalf("oversized blob batch read = (%q, %v, %v), want refusal", content, ok, err)
	}
	if got := contentWrites.countCommands("contents "); got != 1 {
		t.Fatalf("blob issued %d content requests, want one: %#v", got, contentWrites.commands)
	}
	if _, ok := reader.OversizeBlob("plain.py"); !ok {
		t.Fatal("blob preflight bypassed the existing oversize digest registry")
	}
}

func TestBatchFileReaderIgnoresReplaceRefMovedAcrossInfoAndContents(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	const content = "package pinned\n"
	if err := os.WriteFile(filepath.Join(repo, "file.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "file.go")
	git(t, repo, "commit", "-m", "replace-race fixture")
	originalOID := gitOutput(t, repo, "rev-parse", "HEAD:file.go")
	replacementLeaf := gitInputOutput(t, repo, "replacement\n", "hash-object", "-w", "--stdin")
	replacementTree := gitInputOutput(
		t,
		repo,
		fmt.Sprintf("100644 blob %s\tleaf%c", replacementLeaf, byte(0)),
		"mktree", "-z",
	)
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")

	reader, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	writes := &countingWriteCloser{WriteCloser: reader.stdin}
	reader.stdin = writes
	moved := false
	controlType := ""
	writes.beforeWrite = func(command string) {
		if moved || !strings.HasPrefix(command, "contents ") {
			return
		}
		moved = true
		// This runs after the info response but before contents reaches Git. Every
		// production Git process has raw-object semantics, so the new replacement
		// cannot turn this exact blob OID into a tree body.
		git(t, repo, "update-ref", "refs/replace/"+originalOID, replacementTree)
		controlType = gitOutputHonoringReplaceRefs(t, repo, "cat-file", "-t", originalOID)
	}

	got, ok, err := reader.ReadFile("file.go")
	if err != nil || !ok || got != content {
		t.Fatalf("read across moving replacement = (%q, %v, %v), want original blob", got, ok, err)
	}
	if !moved {
		t.Fatal("fixture did not move the replacement between info and contents")
	}
	if controlType != "tree" {
		t.Fatalf("control Git replacement type = %q, want tree", controlType)
	}
	if got := writes.countCommands("info "); got != 1 {
		t.Fatalf("metadata commands = %d, want one: %#v", got, writes.commands)
	}
	if got := writes.countCommands("contents "); got != 1 {
		t.Fatalf("content commands = %d, want one original-blob request: %#v", got, writes.commands)
	}
	// The replacement target was a tree. Returning the original bytes and keeping
	// the session usable proves that no non-blob body entered or desynchronized it.
	again, ok, err := reader.ReadFile("file.go")
	if err != nil || !ok || again != content {
		t.Fatalf("read after moving replacement = (%q, %v, %v), want synchronized original blob", again, ok, err)
	}
}

func TestLimitedFileReaderUsesRawKnownOIDDespiteReplaceRefs(t *testing.T) {
	t.Run("nonblob replacement is ignored", func(t *testing.T) {
		repo := t.TempDir()
		git(t, repo, "init")
		originalOID := gitInputOutput(t, repo, "package original\n", "hash-object", "-w", "--stdin")
		rootTree := gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", originalOID, byte(0)), "mktree", "-z")
		replacementLeaf := gitInputOutput(t, repo, "leaf\n", "hash-object", "-w", "--stdin")
		replacementTree := gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tleaf%c", replacementLeaf, byte(0)), "mktree", "-z")
		t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")

		reader := NewLimitedFileReader(t.Context(), repo, rootTree, 1024)
		t.Cleanup(func() { _ = reader.Close() })
		if err := reader.Prime([]string{"file.go"}); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "update-ref", "refs/replace/"+originalOID, replacementTree)
		if got := gitOutputHonoringReplaceRefs(t, repo, "cat-file", "-t", originalOID); got != "tree" {
			t.Fatalf("control Git replacement type = %q, want tree", got)
		}
		content, err := NewBatchFileReader(t.Context(), repo, rootTree)
		if err != nil {
			t.Fatal(err)
		}
		content.SetMaxBytes(1024)
		writes := &countingWriteCloser{WriteCloser: content.stdin}
		content.stdin = writes
		reader.content = content

		result, err := reader.ReadFile("file.go")
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != LimitedFileContent || result.Content != "package original\n" {
			t.Fatalf("known OID with tree replacement = %#v, want original raw blob", result)
		}
		if got := writes.countCommands("info "); got != 1 {
			t.Fatalf("known-OID metadata commands = %d, want one: %#v", got, writes.commands)
		}
		if got := writes.countCommands("contents "); got != 1 {
			t.Fatalf("raw known OID issued %d content commands, want one original-blob request: %#v", got, writes.commands)
		}
	})

	t.Run("oversized blob replacement is ignored", func(t *testing.T) {
		repo := t.TempDir()
		git(t, repo, "init")
		originalOID := gitInputOutput(t, repo, "small\n", "hash-object", "-w", "--stdin")
		rootTree := gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", originalOID, byte(0)), "mktree", "-z")
		oversizedContent := strings.Repeat("replacement line\n", 64)
		replacementOID := gitInputOutput(t, repo, oversizedContent, "hash-object", "-w", "--stdin")
		const ceiling = int64(32)
		t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")

		reader := NewLimitedFileReader(t.Context(), repo, rootTree, ceiling)
		t.Cleanup(func() { _ = reader.Close() })
		if err := reader.Prime([]string{"file.go"}); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "update-ref", "refs/replace/"+originalOID, replacementOID)
		if got := gitOutputHonoringReplaceRefs(t, repo, "cat-file", "-s", originalOID); got != strconv.Itoa(len(oversizedContent)) {
			t.Fatalf("control Git replacement size = %q, want %d", got, len(oversizedContent))
		}
		content, err := NewBatchFileReader(t.Context(), repo, rootTree)
		if err != nil {
			t.Fatal(err)
		}
		content.SetMaxBytes(ceiling)
		writes := &countingWriteCloser{WriteCloser: content.stdin}
		content.stdin = writes
		reader.content = content

		result, err := reader.ReadFile("file.go")
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != LimitedFileContent || result.Content != "small\n" || result.Bytes != int64(len("small\n")) {
			t.Fatalf("known OID with oversized replacement = %#v, want original small raw blob", result)
		}
		if digest, ok := content.OversizeBlob("file.go"); ok {
			t.Fatalf("ignored replacement produced an oversize digest: %#v", digest)
		}
		if got := writes.countCommands("info "); got != 1 {
			t.Fatalf("raw known-OID metadata commands = %d, want one: %#v", got, writes.commands)
		}
		if got := writes.countCommands("contents "); got != 1 {
			t.Fatalf("raw known-OID content commands = %d, want one original-blob read: %#v", got, writes.commands)
		}
	})
}

func TestBatchFileReaderRejectsInvalidPathWithoutDesync(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	const content = "package plain\n"
	if err := os.WriteFile(filepath.Join(repo, "plain.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "plain.go")
	git(t, repo, "commit", "-m", "plain file")

	reader, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, path := range []string{"", ".", "./plain.go", "../plain.go", "/plain.go", "dir//plain.go", "dir/../plain.go", "dir/"} {
		if got, ok, err := reader.ReadFile(path); err == nil || ok || got != "" {
			t.Fatalf("invalid batch path %q = (%q, %v, %v), want pre-write error", path, got, ok, err)
		}
	}
	got, ok, err := reader.ReadFile("plain.go")
	if err != nil || !ok || got != content {
		t.Fatalf("plain read after invalid paths = (%q, %v, %v), want exact content", got, ok, err)
	}
}

func TestGitObjectReadersUseRepoSubdirectoryPrefix(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.MkdirAll(filepath.Join(repo, "scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "package scope\n"
	if err := os.WriteFile(filepath.Join(repo, "scope", "file.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subdirectory object fixture")
	subdir := filepath.Join(repo, "scope")

	prefix, err := RepoPrefix(t.Context(), subdir)
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "scope/" {
		t.Fatalf("repository prefix = %q, want scope/", prefix)
	}
	commandRoot, err := RepoCommandRoot(t.Context(), subdir)
	if err != nil {
		t.Fatal(err)
	}
	commandRootInfo, err := os.Stat(commandRoot)
	if err != nil {
		t.Fatal(err)
	}
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(commandRootInfo, repoInfo) {
		t.Fatalf("repository command root = %q, want same directory as %q", commandRoot, repo)
	}
	shown, ok, err := ShowFile(t.Context(), subdir, "HEAD", "file.go")
	if err != nil || !ok || shown != content {
		t.Fatalf("subdirectory ShowFile = (%q, %v, %v), want exact content", shown, ok, err)
	}
	limited, err := ReadFileLimited(t.Context(), subdir, "HEAD", "file.go", 1024)
	if err != nil || limited.Status != LimitedFileContent || limited.Content != content {
		t.Fatalf("subdirectory ReadFileLimited = (%#v, %v), want exact content", limited, err)
	}
	batch, err := NewBatchFileReader(t.Context(), subdir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	batched, ok, err := batch.ReadFile("file.go")
	if err != nil || !ok || batched != content {
		t.Fatalf("subdirectory batch read = (%q, %v, %v), want exact content", batched, ok, err)
	}
}

// TestBlobSizeAtRevProbeSucceedsFromARepoSubdirectory is an adversarial check
// against the trail finding claiming treeEntryMetadata's ls-tree, run with a
// subdirectory `repo` as its cwd, re-applies repoTreePath's already-prefixed
// treePath a second time (e.g. "scope/scope/file"). ls-tree's pathspec here
// carries the `:(top,literal)` magic (treeMetadataLiteralPrefix), which
// anchors a literal pathspec to the REPOSITORY ROOT regardless of the
// process's cwd -- verified independently with a bare `git ls-tree` from a
// subdirectory. If the claimed double-prefix bug were real, the probe would
// silently return blobProbeUnknown for the second-level path below, and
// ReadFileLimited would fall through to materializing the oversized blob
// with `git show` -- exactly the memory-bound violation the finding warns
// about. This asserts the PROBE succeeds (LimitedFileOversize with the exact
// size, no content read) rather than merely that some result comes back.
func TestBlobSizeAtRevProbeSucceedsFromARepoSubdirectory(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(repo, "scope", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	const ceiling = 1 << 10
	oversized := strings.Repeat("x", ceiling+1)
	if err := os.WriteFile(filepath.Join(repo, "scope", "big.txt"), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scope", "nested", "big.txt"), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subdirectory oversize fixture")
	subdir := filepath.Join(repo, "scope")

	for _, path := range []string{"big.txt", "nested/big.txt"} {
		detailed, err := ReadFileLimited(t.Context(), subdir, "HEAD", path, ceiling)
		if err != nil {
			t.Fatalf("ReadFileLimited(%q): %v", path, err)
		}
		if detailed.Status != LimitedFileOversize || detailed.Bytes != ceiling+1 || detailed.Content != "" {
			t.Fatalf("ReadFileLimited(subdir, HEAD, %q, %d) = %#v, want oversize status with %d bytes and"+
				" no content read: a double-prefixed pathspec would miss the tree entry entirely and this"+
				" falls back to materializing the whole oversized blob", path, ceiling, detailed, ceiling+1)
		}
	}
}

func TestBatchFileReaderRejectsLineUnsafePathsWithoutDesync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines or carriage returns")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	files := map[string]string{
		"name.go":       "package plain\n",
		"name.go\r":     "package carriage\n",
		"line\nname.go": "package newline\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "line protocol fixture")

	batch, err := NewBatchFileReader(t.Context(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	for _, path := range []string{"name.go\r", "line\nname.go"} {
		if content, ok, err := batch.ReadFile(path); err == nil || ok || content != "" {
			t.Fatalf("unsafe batch read %q = (%q, %v, %v), want pre-write error", path, content, ok, err)
		}
	}
	plain, ok, err := batch.ReadFile("name.go")
	if err != nil || !ok || plain != files["name.go"] {
		t.Fatalf("plain sibling after unsafe reads = (%q, %v, %v), want exact content", plain, ok, err)
	}

	carriage, err := ReadFileLimited(t.Context(), repo, "HEAD", "name.go\r", 1024)
	if err != nil || carriage.Status != LimitedFileContent || carriage.Content != files["name.go\r"] || carriage.Bytes != int64(len(files["name.go\r"])) {
		t.Fatalf("typed trailing-CR read = (%#v, %v), want exact content and size", carriage, err)
	}
	carriage, err = ReadFileLimited(t.Context(), repo, "HEAD", "name.go\r", int64(len(files["name.go\r"])-1))
	if err != nil || carriage.Status != LimitedFileOversize || carriage.Bytes != int64(len(files["name.go\r"])) {
		t.Fatalf("capped trailing-CR read = (%#v, %v), want metadata-only oversize", carriage, err)
	}

	unsafeRevision, err := NewBatchFileReader(t.Context(), repo, "HEAD\n")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unsafeRevision.Close() })
	if unsafeRevision.IsPathSafe("name.go") {
		t.Fatal("revision newline was not included in batch request safety check")
	}
	if _, _, err := unsafeRevision.ReadFile("name.go"); err == nil {
		t.Fatal("batch accepted a revision newline before writing its request")
	}
}

func TestShowFileClassifiesErrorsByStderrNotPath(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	// Path deliberately contains the substring "Path" — the old classifier
	// treated any error mentioning "Path" as a missing file, and ShowFile's
	// wrapped error always echoed the argv (which includes the path).
	const path = "src/PathHelper.go"
	const content = "package src\n\nfunc Help() {}\n"
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add path helper")

	// Regression: a bad rev on a path containing "Path" must surface a real
	// error, not be swallowed as file-absent.
	if _, ok, err := ShowFile(t.Context(), repo, "BADREV", path); err == nil {
		t.Fatalf("ShowFile with bad rev = (ok %v, err nil), want non-nil error", ok)
	}

	// A syntactically valid but unwritten object ID and an existing blob ID can
	// both make an untyped `git show REV:PATH` emit a path-looking diagnostic.
	// Neither is a valid tree revision, so both must remain hard errors.
	unwrittenCmd := exec.Command("git", "hash-object", "--stdin")
	unwrittenCmd.Dir = repo
	unwrittenCmd.Stdin = strings.NewReader("entire-graph pr36 unwritten object\n")
	unwrittenOut, err := unwrittenCmd.Output()
	if err != nil {
		t.Fatalf("git hash-object --stdin: %v", err)
	}
	unwrittenOID := strings.TrimSpace(string(unwrittenOut))
	blobOID := gitOutput(t, repo, "rev-parse", "HEAD:"+path)

	for _, tc := range []struct {
		name string
		rev  string
		path string
	}{
		{name: "unwritten full object ID", rev: unwrittenOID, path: path},
		{name: "blob object", rev: blobOID, path: path},
		{name: "marker phrase in invalid revision", rev: "not found", path: path},
		{name: "marker phrase in outside path", rev: "HEAD", path: "../not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, ok, err := ShowFile(t.Context(), repo, tc.rev, tc.path); err == nil || ok || out != "" {
				t.Fatalf("ShowFile(%q, %q) = (%q, ok %v, err %v), want (\"\", false, non-nil)", tc.rev, tc.path, out, ok, err)
			}
		})
	}

	// A genuinely missing path at a valid rev is still reported as absent.
	if out, ok, err := ShowFile(t.Context(), repo, "HEAD", "src/DoesNotExist.go"); err != nil || ok || out != "" {
		t.Fatalf("ShowFile missing path = (%q, ok %v, err %v), want (\"\", false, nil)", out, ok, err)
	}

	// An existing file at HEAD returns its content.
	if out, ok, err := ShowFile(t.Context(), repo, "HEAD", path); err != nil || !ok || out != content {
		t.Fatalf("ShowFile existing = (%q, ok %v, err %v), want (%q, true, nil)", out, ok, err, content)
	}
}

// ShowFileLimited must refuse an oversized blob rather than materialize it, and
// must keep every other ShowFile behavior: absent is absent, a real failure is
// an error, and a blob at exactly the ceiling still reads.
func TestShowFileLimitedBoundsTheReadNotTheAnswer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const ceiling = 1 << 10
	// A newline in the path is the case that reaches this reader at all: the
	// cat-file batch protocol is line based, so those paths take the one-shot
	// `git show` route and nothing else bounds them.
	const oversizedPath = "big\nname.txt"
	files := map[string]string{
		oversizedPath: strings.Repeat("x", ceiling+1),
		"exact.txt":   strings.Repeat("y", ceiling),
		"small.txt":   "small\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "size fixture")

	if out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", oversizedPath, ceiling); err != nil || ok || out != "" {
		t.Errorf("oversized blob = (%d bytes, ok %v, err %v), want (\"\", false, nil)", len(out), ok, err)
	}
	detailed, err := ReadFileLimited(t.Context(), repo, "HEAD", oversizedPath, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if detailed.Status != LimitedFileOversize || detailed.Bytes != ceiling+1 || detailed.Content != "" {
		t.Errorf("typed oversized blob = %#v, want status oversize, %d bytes, and no content", detailed, ceiling+1)
	}
	// The ceiling is inclusive, so the boundary blob is still an answer. This is
	// what distinguishes a bounded read from an off-by-one refusal.
	if out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "exact.txt", ceiling); err != nil || !ok || out != files["exact.txt"] {
		t.Errorf("blob at the ceiling = (%d bytes, ok %v, err %v), want (%d bytes, true, nil)", len(out), ok, err, ceiling)
	}
	detailed, err = ReadFileLimited(t.Context(), repo, "HEAD", "exact.txt", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if detailed.Status != LimitedFileContent || detailed.Bytes != ceiling || detailed.Content != files["exact.txt"] {
		t.Errorf("typed blob at ceiling = %#v, want content status with %d bytes", detailed, ceiling)
	}
	if out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "small.txt", ceiling); err != nil || !ok || out != files["small.txt"] {
		t.Errorf("small blob = (%q, ok %v, err %v), want (%q, true, nil)", out, ok, err, files["small.txt"])
	}
	if out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "does-not-exist.txt", ceiling); err != nil || ok || out != "" {
		t.Errorf("missing path = (%q, ok %v, err %v), want (\"\", false, nil)", out, ok, err)
	}
	detailed, err = ReadFileLimited(t.Context(), repo, "HEAD", "does-not-exist.txt", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if detailed.Status != LimitedFileMissing || detailed.Bytes != 0 || detailed.Content != "" {
		t.Errorf("typed missing path = %#v, want missing with no content", detailed)
	}
	// Same stderr-only classification as ShowFile: a bad revision is a failure,
	// not an absent file, even though the argv echoes a path.
	if out, ok, err := ShowFileLimited(t.Context(), repo, "BADREV", "small.txt", ceiling); err == nil || ok || out != "" {
		t.Errorf("bad rev = (%q, ok %v, err %v), want (\"\", false, non-nil)", out, ok, err)
	}
	// A non-positive ceiling means "no ceiling", so it must behave as ShowFile.
	if out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", oversizedPath, 0); err != nil || !ok || out != files[oversizedPath] {
		t.Errorf("unbounded call = (%d bytes, ok %v, err %v), want the whole blob", len(out), ok, err)
	}
}

// The refusal above is observable either way — buffering the whole blob and THEN
// reporting it oversized returns the same triple. What this pins is the part that
// is not observable from the return value: the bytes are never allocated.
func TestShowFileLimitedNeverMaterializesAnOversizedBlob(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const blobSize = 32 << 20
	const ceiling = 1 << 10
	// Incompressible, so `git show` really does stream 32 MiB: a run of one byte
	// would still decompress to the same size, but random content also rules out
	// any future short-circuit on a cheap object.
	blob := make([]byte, blobSize)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "huge.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "huge blob")
	blob = nil

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "huge.bin", ceiling)
	runtime.ReadMemStats(&after)

	if err != nil || ok || out != "" {
		t.Fatalf("oversized blob = (%d bytes, ok %v, err %v), want (\"\", false, nil)", len(out), ok, err)
	}
	// Generous by three orders of magnitude against the ceiling and by eight
	// times against the blob: this fails on materialization, not on allocator
	// noise.
	const budget = 4 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > budget {
		t.Errorf("reading a %d-byte blob under a %d-byte ceiling allocated %d bytes, want under %d: the blob was materialized before the ceiling was applied",
			blobSize, ceiling, allocated, budget)
	}
}

// A gitlink resolves to a commit object, not a blob. `cat-file -s` alone can
// make that object look safe because the commit itself is tiny, while `git
// show` renders the commit's entire patch. The bounded file reader must reject
// the type before invoking that renderer.
func TestShowFileLimitedRejectsGitlinkBeforeRenderingCommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const renderedPatchBytes = 16 << 20
	const ceiling = int64(4 << 10)
	large := strings.Repeat("a line large enough to render in the commit patch\n", renderedPatchBytes/48+1)
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "large.txt")
	git(t, repo, "commit", "-m", "large gitlink target")
	target := gitOutput(t, repo, "rev-parse", "HEAD")
	targetSize, err := strconv.ParseInt(gitOutput(t, repo, "cat-file", "-s", target), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if targetSize >= ceiling {
		t.Fatalf("target commit object is %d bytes, want below the %d-byte ceiling", targetSize, ceiling)
	}

	git(t, repo, "rm", "large.txt")
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000", target, "vendor/module")
	git(t, repo, "commit", "-m", "record gitlink")
	large = ""

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "vendor/module", ceiling)
	runtime.ReadMemStats(&after)
	if err != nil || ok || out != "" {
		t.Fatalf("gitlink = (%d bytes, ok %v, err %v), want (\"\", false, nil)", len(out), ok, err)
	}
	const allocationBudget = 4 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > allocationBudget {
		t.Errorf("refusing a gitlink allocated %d bytes, want under %d: git show rendered the target commit's patch", allocated, allocationBudget)
	}
	detailed, err := ReadFileLimited(t.Context(), repo, "HEAD", "vendor/module", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if detailed.Status != LimitedFileNonBlob || detailed.Content != "" || detailed.Bytes != 0 {
		t.Errorf("typed gitlink result = %#v, want non-blob with no content", detailed)
	}
}

func TestReadFileLimitedClassifiesGitlinksWithoutCeiling(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "seed.txt")
	git(t, repo, "commit", "-m", "reachable target")
	target := gitOutput(t, repo, "rev-parse", "HEAD")
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000", target, "reachable")
	git(t, repo, "commit", "-m", "reachable gitlink")

	reachable, err := ReadFileLimited(t.Context(), repo, "HEAD", "reachable", 0)
	if err != nil || reachable.Status != LimitedFileNonBlob {
		t.Fatalf("reachable unbounded gitlink = (%#v, %v), want non-blob", reachable, err)
	}
	oidLength := 40
	if gitOutput(t, repo, "rev-parse", "--show-object-format") == "sha256" {
		oidLength = 64
	}
	danglingTree := gitInputOutput(
		t,
		repo,
		fmt.Sprintf("160000 commit %s\tdangling%c", strings.Repeat("1", oidLength), byte(0)),
		"mktree", "-z", "--missing",
	)
	dangling, err := ReadFileLimited(t.Context(), repo, danglingTree, "dangling", 0)
	if err != nil || dangling.Status != LimitedFileNonBlob {
		t.Fatalf("dangling unbounded gitlink = (%#v, %v), want non-blob", dangling, err)
	}
	if content, ok, err := ShowFileLimited(t.Context(), repo, danglingTree, "dangling", 0); err != nil || ok || content != "" {
		t.Fatalf("compact dangling gitlink = (%q, %v, %v), want refused non-blob", content, ok, err)
	}
}

func TestLimitedFileReaderReusesBatchesAndRefusesByMetadata(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "seed.txt")
	git(t, repo, "commit", "-m", "gitlink target")
	target := gitOutput(t, repo, "rev-parse", "HEAD")

	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	const fileCount = 32
	for i := range fileCount {
		path := filepath.Join(repo, "src", fmt.Sprintf("file%02d.go", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("package src\n// file %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const ceiling = int64(64)
	if err := os.WriteFile(filepath.Join(repo, "large.go"), []byte(strings.Repeat("x", int(ceiling)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "update-index", "--add", "--cacheinfo", "160000", target, "vendor/module.go")
	git(t, repo, "commit", "-m", "reader fixture")

	reader := NewLimitedFileReader(t.Context(), repo, "HEAD", ceiling)
	t.Cleanup(func() { _ = reader.Close() })
	primePaths := make([]string, 0, fileCount+3)
	for i := range fileCount {
		primePaths = append(primePaths, fmt.Sprintf("src/file%02d.go", i))
	}
	primePaths = append(primePaths, "large.go", "vendor/module.go", "missing.go")
	if err := reader.Prime(primePaths); err != nil {
		t.Fatal(err)
	}
	if reader.content != nil {
		t.Fatal("metadata priming should not start the persistent content batch")
	}
	var contentCmd *exec.Cmd
	for i := range fileCount {
		path := fmt.Sprintf("src/file%02d.go", i)
		result, err := reader.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != LimitedFileContent || !strings.Contains(result.Content, fmt.Sprintf("file %d", i)) {
			t.Fatalf("%s = %#v, want its content", path, result)
		}
		if i == 0 {
			contentCmd = reader.content.cmd
		} else if reader.content.cmd != contentCmd {
			t.Fatal("limited reader started new Git processes instead of reusing its content batch")
		}
	}

	large, err := reader.ReadFile("large.go")
	if err != nil {
		t.Fatal(err)
	}
	if large.Status != LimitedFileOversize || large.Bytes != ceiling+1 || large.Content != "" {
		t.Fatalf("large blob = %#v, want metadata-only oversize refusal", large)
	}
	if _, streamed := reader.content.OversizeBlob("large.go"); streamed {
		t.Fatal("oversized blob entered the content batch and was streamed merely to advance it")
	}
	if reader.content.cmd != contentCmd {
		t.Fatal("oversize probe replaced a persistent batch process")
	}

	gitlink, err := reader.ReadFile("vendor/module.go")
	if err != nil {
		t.Fatal(err)
	}
	if gitlink.Status != LimitedFileNonBlob {
		t.Fatalf("gitlink = %#v, want non-blob", gitlink)
	}
	missing, err := reader.ReadFile("missing.go")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != LimitedFileMissing {
		t.Fatalf("missing path = %#v, want missing", missing)
	}
}

func TestLimitedFileReaderUsesObjectIDsForLineTerminators(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines or carriage returns")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	plainPath := "name.go"
	carriagePath := "name.go\r"
	newlinePath := "line\nname.go"
	plainContent := "package plain\n"
	carriageContent := "package carriage\n"
	newlineContent := "package newline\n"
	if err := os.WriteFile(filepath.Join(repo, plainPath), []byte(plainContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, carriagePath), []byte(carriageContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, newlinePath), []byte(newlineContent), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "carriage return siblings")

	reader := NewLimitedFileReader(t.Context(), repo, "HEAD", 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{plainPath, carriagePath, newlinePath}); err != nil {
		t.Fatal(err)
	}
	newlineResult, err := reader.ReadFile(newlinePath)
	if err != nil {
		t.Fatal(err)
	}
	if newlineResult.Status != LimitedFileContent || newlineResult.Content != newlineContent {
		t.Fatalf("newline path = %#v, want its own content %q", newlineResult, newlineContent)
	}
	if reader.content == nil {
		t.Fatal("newline path did not start the exact-object content batch")
	}
	contentCmd := reader.content.cmd

	carriageResult, err := reader.ReadFile(carriagePath)
	if err != nil {
		t.Fatal(err)
	}
	if carriageResult.Status != LimitedFileContent || carriageResult.Content != carriageContent {
		t.Fatalf("trailing-CR path = %#v, want its own content %q", carriageResult, carriageContent)
	}
	if reader.content.cmd != contentCmd {
		t.Fatal("line-terminator paths did not reuse the exact-object content batch")
	}

	plain, err := reader.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Status != LimitedFileContent || plain.Content != plainContent {
		t.Fatalf("plain sibling = %#v, want %q", plain, plainContent)
	}
	if reader.content.cmd != contentCmd {
		t.Fatal("plain sibling did not reuse the exact-object content batch")
	}
}

func TestLimitedFileReaderKeepsRootPathsExactFromRepoSubdirectory(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const content = "package scope\n"
	if err := os.MkdirAll(filepath.Join(repo, "scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scope", "file.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subdirectory fixture")

	reader := NewLimitedFileReader(t.Context(), filepath.Join(repo, "scope"), "HEAD", 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{"scope/file.go", "scope/missing.go"}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadFile("scope/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileContent || result.Content != content {
		t.Fatalf("subdirectory file = %#v, want root-relative content %q", result, content)
	}
	missing, err := reader.ReadFile("scope/missing.go")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != LimitedFileMissing {
		t.Fatalf("subdirectory missing path = %#v, want missing", missing)
	}

	// ReadFile without an explicit Prime must use the same tree-root coordinate,
	// rather than delegating to the repo-relative one-shot API and prepending
	// scope/ a second time.
	lazy := NewLimitedFileReader(t.Context(), filepath.Join(repo, "scope"), "HEAD", 1024)
	t.Cleanup(func() { _ = lazy.Close() })
	lazyResult, err := lazy.ReadFile("scope/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if lazyResult.Status != LimitedFileContent || lazyResult.Content != content {
		t.Fatalf("lazy subdirectory file = %#v, want root-relative content %q", lazyResult, content)
	}
}

func TestLimitedFileReaderSplitsAncestorAndDescendantPathspecs(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.MkdirAll(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "file.go"), []byte("package dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "overlapping path fixture")

	paths := []string{"dir", "dir/file.go"}
	if end := treeMetadataBatchEnd(paths, 0); end != 1 {
		t.Fatalf("first prefix-free batch ended at %d, want 1", end)
	}
	reader := NewLimitedFileReader(t.Context(), repo, "HEAD", 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime(paths); err != nil {
		t.Fatal(err)
	}
	if len(reader.primed) != 2 {
		t.Fatalf("primed entries = %#v, want exactly ancestor and descendant", reader.primed)
	}
	if got := reader.primed["dir"].result.Status; got != LimitedFileNonBlob {
		t.Fatalf("ancestor status = %v, want non-blob tree", got)
	}
	if got := reader.primed["dir/file.go"].result.Status; got != LimitedFileContent {
		t.Fatalf("descendant status = %v, want blob content", got)
	}
	result, err := reader.ReadFile("dir/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileContent || result.Content != "package dir\n" {
		t.Fatalf("descendant read = %#v, want exact content", result)
	}
}

// An exact literal pathspec does not imply one ls-tree record when a raw tree
// contains duplicate names. Keep the adversarial output proportional to the
// requested path set: the reader must reject the second record and retire Git,
// rather than buffering every duplicate before discovering the malformed tree.
func TestLimitedFileReaderStreamsAndRejectsDuplicateTreeMetadata(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	blob := gitInputOutput(t, repo, "package duplicate\n", "hash-object", "-w", "--stdin")

	const duplicateEntries = 100_000
	tree := func() string {
		var input strings.Builder
		input.Grow(duplicateEntries * (len(blob) + len("100644 blob \tfile.go\x00")))
		for range duplicateEntries {
			fmt.Fprintf(&input, "100644 blob %s\tfile.go%c", blob, byte(0))
		}
		return gitInputOutput(t, repo, input.String(), "mktree", "-z")
	}()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reader := NewLimitedFileReader(ctx, repo, tree, 1024)
	err := reader.Prime([]string{"file.go"})
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), `duplicate path "file.go"`) {
		t.Fatalf("duplicate-tree Prime error = %v, want immediate duplicate-path rejection", err)
	}
	// The old whole-output run path allocated the complete ~6.9 MiB listing,
	// then copied and split it. This generous bound permits subprocess plumbing
	// while proving the duplicate stream is stopped after its second record.
	const allocationBudget = 4 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > allocationBudget {
		t.Fatalf("duplicate metadata probe allocated %d bytes, want under %d", allocated, allocationBudget)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("duplicate metadata probe did not retire Git before its deadline: %v", err)
	}
	// A prompt Wait is not enough on Windows if the killed Git launcher leaves
	// its worker behind with the repository as its current directory. Require
	// the complete repository to be removable as soon as Prime returns.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("duplicate metadata probe left a Git process holding the repository: %v", err)
	}
}

func TestLimitedFileReaderRejectsTrailingSlashBeforeGit(t *testing.T) {
	// The directory need not be a repository: validation must happen before
	// ls-tree, because a single literal path ending in slash expands every
	// immediate child instead of returning one exact tree entry.
	reader := NewLimitedFileReader(t.Context(), t.TempDir(), "HEAD", 1024)
	err := reader.Prime([]string{"dir/"})
	if err == nil || !strings.Contains(err.Error(), `invalid Git tree path "dir/"`) {
		t.Fatalf("trailing-slash Prime error = %v, want pre-Git invalid-path rejection", err)
	}
	if _, err := reader.ReadFile("dir/"); err == nil || !strings.Contains(err.Error(), `invalid Git tree path "dir/"`) {
		t.Fatalf("trailing-slash lazy read error = %v, want pre-Git invalid-path rejection", err)
	}
	for _, path := range []string{"", ".", "./file", "dir/../file", "/file", "dir//file", "dir/", "nul\x00path"} {
		if _, err := reader.ReadFile(path); err == nil || !strings.Contains(err.Error(), "invalid Git tree path") {
			t.Fatalf("lazy invalid path %q error = %v, want pre-Git validation", path, err)
		}
		if _, err := ReadFileLimited(t.Context(), t.TempDir(), "HEAD", path, 0); err == nil || !strings.Contains(err.Error(), "invalid Git tree path") {
			t.Fatalf("one-shot invalid path %q error = %v, want pre-Git validation", path, err)
		}
		if _, _, err := ShowFileLimited(t.Context(), t.TempDir(), "HEAD", path, 0); err == nil || !strings.Contains(err.Error(), "invalid Git tree path") {
			t.Fatalf("compatibility invalid path %q error = %v, want pre-Git validation", path, err)
		}
	}
}

func TestLimitedFileReaderPrimesDeepGitTreePath(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	tree, path := nestedGitTree(t, repo, "package deep\n", 25)
	if len(path) <= literalPathOutputMaxPathBytes || len(treeMetadataLiteralPrefix)+len(path) >= literalPathspecBatchBytes {
		t.Fatalf("fixture path length = %d, want between output and batch limits", len(path))
	}

	reader := NewLimitedFileReader(t.Context(), repo, tree, 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{path}); err != nil {
		t.Fatal(err)
	}
	if _, primed := reader.primed[path]; !primed {
		t.Fatal("valid deep Git-tree path was not metadata-primed")
	}
	result, err := reader.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileContent || result.Content != "package deep\n" {
		t.Fatalf("deep Git-tree file = %#v, want exact content", result)
	}
}

func TestLimitedFileReaderPrimesPathBeyondBatchArgvBound(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	tree, path := nestedGitTree(t, repo, "package fallback\n", 80)
	if len(treeMetadataLiteralPrefix)+len(path) <= literalPathspecBatchBytes {
		t.Fatalf("fixture path length = %d, want beyond batch argv bound", len(path))
	}

	reader := NewLimitedFileReader(t.Context(), repo, tree, 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{path}); err != nil {
		t.Fatal(err)
	}
	if _, primed := reader.primed[path]; !primed {
		t.Fatal("over-bound path was not resolved by bounded component traversal")
	}
	result, err := reader.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileContent || result.Content != "package fallback\n" {
		t.Fatalf("component-resolved deep Git-tree file = %#v, want exact content", result)
	}
	if reader.content == nil {
		t.Fatal("component-resolved path did not use exact-OID content batch")
	}
}

// TestLimitedFileReaderPrimeStopsOrdinaryBatchesAtDeadline reproduces the
// trail finding on Prime: the ordinary short-path batch loop ran every batch
// to completion regardless of SetDeadline, so a candidate list long enough to
// need several literalPathspecBatchCount-sized batches could keep starting
// new Git subprocesses long after the caller's wall-clock budget elapsed.
func TestLimitedFileReaderPrimeStopsOrdinaryBatchesAtDeadline(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	// More than one literalPathspecBatchCount-sized group, so Prime must issue
	// at least two ordinary metadata batches to resolve every path.
	const total = literalPathspecBatchCount + 10
	paths := make([]string, total)
	for i := range paths {
		path := fmt.Sprintf("file%03d.go", i)
		paths[i] = path
		if err := os.WriteFile(filepath.Join(repo, path), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "many short paths")
	head := gitOutput(t, repo, "rev-parse", "HEAD")

	reader := NewLimitedFileReader(t.Context(), repo, head, 1024)
	t.Cleanup(func() { _ = reader.Close() })
	deadline := time.Unix(100, 0)
	checks := 0
	reader.now = func() time.Time {
		checks++
		if checks == 1 {
			// Let the first batch attempt proceed, so this test exercises a
			// deadline that elapses mid-Prime, not one already elapsed
			// before the first Git subprocess ever ran.
			return deadline.Add(-time.Second)
		}
		return deadline
	}
	reader.SetDeadline(deadline)

	if err := reader.Prime(paths); err != nil {
		t.Fatal(err)
	}
	if checks < 2 {
		t.Fatalf("Prime checked the deadline %d times, want at least 2 (once per batch attempt)", checks)
	}
	resolved, unaddressable := 0, 0
	for _, path := range paths {
		switch reader.primed[path].result.Status {
		case LimitedFileUnaddressable:
			unaddressable++
		default:
			resolved++
		}
	}
	if unaddressable == 0 {
		t.Fatalf("Prime resolved every path despite an elapsed deadline: %d resolved, 0 unaddressable", resolved)
	}
	if resolved == 0 {
		t.Fatal("Prime marked every path unaddressable; want the already-started first batch to still complete")
	}
}

func TestLimitedFileReaderCachesAndCapsComponentMetadata(t *testing.T) {
	const (
		depth  = 80
		leaves = 200
	)
	component := strings.Repeat("a", 200)
	prefix := strings.Repeat(component+"/", depth)
	paths := make([]string, 0, leaves)
	for index := range leaves {
		paths = append(paths, prefix+fmt.Sprintf("file%03d.go", index))
	}
	if len(paths[0])+len(treeMetadataLiteralPrefix) <= literalPathspecBatchBytes {
		t.Fatalf("fixture path length = %d, want component traversal", len(paths[0]))
	}

	reader := NewLimitedFileReader(t.Context(), "unused", "tree-0", 1024)
	lookups := 0
	reader.componentMetadataLookup = func(
		_ context.Context,
		_, treeOID string,
		components []string,
	) (map[string]primedLimitedFile, error) {
		lookups++
		if len(components) != 1 {
			t.Fatalf("component lookup paths = %#v, want one", components)
		}
		componentPath := components[0]
		treeIndex, err := strconv.Atoi(strings.TrimPrefix(treeOID, "tree-"))
		if err != nil {
			t.Fatalf("fake tree OID %q: %v", treeOID, err)
		}
		if treeIndex < depth && componentPath == component {
			return map[string]primedLimitedFile{
				componentPath: {
					result:     LimitedFileResult{Status: LimitedFileNonBlob},
					objectID:   fmt.Sprintf("tree-%d", treeIndex+1),
					objectType: "tree",
				},
			}, nil
		}
		if treeIndex == depth && strings.HasPrefix(componentPath, "file") {
			return map[string]primedLimitedFile{
				componentPath: {
					result:     LimitedFileResult{Status: LimitedFileContent, Bytes: 1},
					objectID:   "blob-" + componentPath,
					objectType: "blob",
				},
			}, nil
		}
		return map[string]primedLimitedFile{}, nil
	}

	if err := reader.Prime(paths); err != nil {
		t.Fatal(err)
	}
	if lookups != limitedFileComponentMetadataProcessLimit ||
		reader.componentMetadataProcesses != limitedFileComponentMetadataProcessLimit {
		t.Fatalf("component metadata lookups = %d/%d, want cap %d",
			lookups, reader.componentMetadataProcesses, limitedFileComponentMetadataProcessLimit)
	}
	// The shared 80-entry prefix is looked up once, leaving the rest of the
	// process allowance for distinct leaves. Without the (tree, component)
	// cache, only a few paths would resolve before hitting the same cap.
	wantContent := limitedFileComponentMetadataProcessLimit - depth
	for index, path := range paths {
		want := LimitedFileUnaddressable
		if index < wantContent {
			want = LimitedFileContent
		}
		if got := reader.primed[path].result.Status; got != want {
			t.Fatalf("path %d status = %v, want %v", index, got, want)
		}
	}
	if len(reader.componentMetadata) != limitedFileComponentMetadataProcessLimit {
		t.Fatalf("component cache entries = %d, want %d", len(reader.componentMetadata), limitedFileComponentMetadataProcessLimit)
	}
	if err := reader.Prime(paths); err != nil {
		t.Fatal(err)
	}
	if lookups != limitedFileComponentMetadataProcessLimit {
		t.Fatalf("repeat Prime launched %d component lookups, want cached %d", lookups, limitedFileComponentMetadataProcessLimit)
	}
}

func TestLimitedFileReaderStopsComponentMetadataAtDeadline(t *testing.T) {
	const depth = 80
	component := strings.Repeat("a", 200)
	prefix := strings.Repeat(component+"/", depth)
	reader := NewLimitedFileReader(t.Context(), "unused", "tree-0", 1024)
	lookups := 0
	reader.componentMetadataLookup = func(
		_ context.Context,
		_, treeOID string,
		components []string,
	) (map[string]primedLimitedFile, error) {
		lookups++
		treeIndex, err := strconv.Atoi(strings.TrimPrefix(treeOID, "tree-"))
		if err != nil {
			return nil, err
		}
		return map[string]primedLimitedFile{
			components[0]: {
				result:     LimitedFileResult{Status: LimitedFileNonBlob},
				objectID:   fmt.Sprintf("tree-%d", treeIndex+1),
				objectType: "tree",
			},
		}, nil
	}
	deadline := time.Unix(100, 0)
	reader.now = func() time.Time {
		if reader.componentMetadataProcesses < 3 {
			return deadline.Add(-time.Second)
		}
		return deadline
	}
	reader.SetDeadline(deadline)
	paths := []string{prefix + "first.go", prefix + "second.go"}
	if err := reader.Prime(paths); err != nil {
		t.Fatal(err)
	}
	if lookups != 3 || reader.componentMetadataProcesses != 3 {
		t.Fatalf("deadline component lookups = %d/%d, want 3", lookups, reader.componentMetadataProcesses)
	}
	for _, path := range paths {
		if got := reader.primed[path].result.Status; got != LimitedFileUnaddressable {
			t.Fatalf("deadline result for %q = %v, want unaddressable", path, got)
		}
	}
}

func TestLimitedFileReaderClassifiesSingleComponentBeyondArgvBound(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	blob := gitInputOutput(t, repo, "package leaf\n", "hash-object", "-w", "--stdin")
	leafTree := gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", blob, byte(0)), "mktree", "-z")
	component := strings.Repeat("x", literalPathspecBatchBytes+1)
	tree := gitInputOutput(t, repo, fmt.Sprintf("040000 tree %s\t%s%c", leafTree, component, byte(0)), "mktree", "-z")
	path := component + "/file.go"

	reader := NewLimitedFileReader(t.Context(), repo, tree, 1024)
	if err := reader.Prime([]string{path}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileUnaddressable {
		t.Fatalf("over-bound component result = %#v, want bounded unaddressable classification", result)
	}
	oneShot, err := ReadFileLimited(t.Context(), repo, tree, path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if oneShot.Status != LimitedFileUnaddressable {
		t.Fatalf("one-shot over-bound component = %#v, want bounded unaddressable classification", oneShot)
	}
	if reader.content != nil {
		t.Fatal("unaddressable component started a content batch")
	}
}

func TestLimitedFileReaderClassifiesMissingBlobObjectAsUnreadable(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	knownBlob := gitInputOutput(t, repo, "package known\n", "hash-object", "-w", "--stdin")
	missingOID := strings.Repeat("1", len(knownBlob))
	tree := gitInputOutput(
		t,
		repo,
		fmt.Sprintf("100644 blob %s\tmissing.go%c", missingOID, byte(0)),
		"mktree", "-z", "--missing",
	)

	reader := NewLimitedFileReader(t.Context(), repo, tree, 1024)
	t.Cleanup(func() { _ = reader.Close() })
	if err := reader.Prime([]string{"missing.go"}); err != nil {
		t.Fatalf("prime missing-object entry: %v", err)
	}
	result, err := reader.ReadFile("missing.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != LimitedFileUnreadable {
		t.Fatalf("missing-object result = %#v, want unreadable", result)
	}
	if reader.content != nil {
		t.Fatal("unreadable metadata started a content batch")
	}

	oneShot, err := ReadFileLimited(t.Context(), repo, tree, "missing.go", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if oneShot.Status != LimitedFileUnreadable {
		t.Fatalf("one-shot missing-object result = %#v, want unreadable", oneShot)
	}
}

func nestedGitTree(t *testing.T, repo, content string, depth int) (tree, path string) {
	t.Helper()
	blob := gitInputOutput(t, repo, content, "hash-object", "-w", "--stdin")
	tree = gitInputOutput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", blob, byte(0)), "mktree", "-z")
	path = "file.go"
	component := strings.Repeat("a", 200)
	for range depth {
		tree = gitInputOutput(t, repo, fmt.Sprintf("040000 tree %s\t%s%c", tree, component, byte(0)), "mktree", "-z")
		path = component + "/" + path
	}
	return tree, path
}

// A ceiling no blob can reach must behave as no ceiling. This once guarded an
// overflow in a maxBytes+1 limit; deciding from the blob's size removed that
// arithmetic, but the guarantee is still worth pinning, and the clock-based
// assertion still catches the failure mode this function has had twice: not a
// wrong answer, a hang. The blob is larger than a pipe buffer, which is what
// turns a stalled read into git blocking on a write nobody drains.
func TestShowFileLimitedTreatsAnUnreachableCeilingAsNoCeiling(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const blobSize = 1 << 20
	blob := make([]byte, blobSize)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "big.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "pipe-filling blob")

	type outcome struct {
		out string
		ok  bool
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		// t.Context() is cancelled when the test ends, so a hung git is reaped
		// rather than outliving the run.
		out, ok, err := ShowFileLimited(t.Context(), repo, "HEAD", "big.bin", math.MaxInt64)
		done <- outcome{out, ok, err}
	}()

	select {
	case got := <-done:
		if got.err != nil || !got.ok || len(got.out) != blobSize {
			t.Fatalf("unreachable ceiling = (%d bytes, ok %v, err %v), want (%d bytes, true, nil)", len(got.out), got.ok, got.err, blobSize)
		}
		if got.out != string(blob) {
			t.Fatal("unreachable ceiling returned different bytes than the blob")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ShowFileLimited did not return: maxBytes+1 overflowed to a negative limit, so the read EOFed at once and git blocked writing to a pipe nobody drains")
	}
}

// When the size probe gives no answer the read is unbounded, so the ceiling can
// only be enforced on the result. That path was previously untested and the doc
// comment claimed a guarantee it no longer had: the refusal must survive a
// silent probe, or a caller receives content it declared too large.
func TestShowFileLimitedRefusesOversizedBlobWhenTheProbeIsSilent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const ceiling = 1 << 10
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("over.txt", strings.Repeat("x", ceiling+1))
	write("under.txt", strings.Repeat("y", ceiling))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "probe fixture")

	silent := func(context.Context, string, string, string) (int64, blobProbeStatus) {
		return 0, blobProbeUnknown
	}

	if out, ok, err := showFileLimited(t.Context(), repo, "HEAD", "over.txt", ceiling, silent); err != nil || ok || out != "" {
		t.Errorf("oversized blob with a silent probe = (%d bytes, ok %v, err %v), want (\"\", false, nil)", len(out), ok, err)
	}
	// A silent probe must not turn into a blanket refusal either: everything at
	// or under the ceiling still has to come back.
	if out, ok, err := showFileLimited(t.Context(), repo, "HEAD", "under.txt", ceiling, silent); err != nil || !ok || len(out) != ceiling {
		t.Errorf("blob at the ceiling with a silent probe = (%d bytes, ok %v, err %v), want (%d bytes, true, nil)", len(out), ok, err, ceiling)
	}
	// And absent-vs-failed still comes from ShowFile, not from the probe.
	if out, ok, err := showFileLimited(t.Context(), repo, "HEAD", "missing.txt", ceiling, silent); err != nil || ok || out != "" {
		t.Errorf("missing path with a silent probe = (%q, ok %v, err %v), want (\"\", false, nil)", out, ok, err)
	}
	if out, ok, err := showFileLimited(t.Context(), repo, "BADREV", "under.txt", ceiling, silent); err == nil || ok || out != "" {
		t.Errorf("bad rev with a silent probe = (%q, ok %v, err %v), want (\"\", false, non-nil)", out, ok, err)
	}
}

func TestNewCmdPinsSubprocessLocaleToC(t *testing.T) {
	// ShowFile classifies file-absent by matching git's English stderr text, so
	// diagnostic subprocesses run with a pinned C locale. LC_ALL=C must come
	// after the inherited environment so it overrides LC_ALL/LANG/LC_MESSAGES.
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("LANG", "fr_FR.UTF-8")
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")
	t.Setenv("GIT_ALLOW_PROTOCOL", "https")
	dir := t.TempDir()
	cmd := newCmd(context.Background(), dir, "git", "version")
	if cmd.WaitDelay != gitCommandWaitDelay || cmd.WaitDelay == 0 {
		t.Fatalf("stable-locale command WaitDelay = %v, want %v", cmd.WaitDelay, gitCommandWaitDelay)
	}
	lcAll, lang, pwd, noReplace, noLazyFetch, allowProtocol := "", "", "", "", "", "unset"
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "LC_ALL="); ok {
			lcAll = v
		}
		if v, ok := strings.CutPrefix(kv, "LANG="); ok {
			lang = v
		}
		if v, ok := strings.CutPrefix(kv, "PWD="); ok {
			pwd = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_NO_REPLACE_OBJECTS="); ok {
			noReplace = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_NO_LAZY_FETCH="); ok {
			noLazyFetch = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_ALLOW_PROTOCOL="); ok {
			allowProtocol = v
		}
	}
	if lcAll != "C" || lang != "C" {
		t.Fatalf("effective subprocess locale LC_ALL=%q LANG=%q, want both \"C\"", lcAll, lang)
	}
	if runtime.GOOS != "windows" && filepath.Clean(pwd) != filepath.Clean(dir) {
		t.Fatalf("subprocess PWD=%q, want command directory %q", pwd, dir)
	}
	if noReplace != "1" {
		t.Fatalf("effective GIT_NO_REPLACE_OBJECTS=%q, want 1", noReplace)
	}
	if noLazyFetch != "1" {
		t.Fatalf("effective GIT_NO_LAZY_FETCH=%q, want 1: a partial clone's promisor remote must never be contacted by this no-egress provider", noLazyFetch)
	}
	if allowProtocol != "" {
		t.Fatalf("effective GIT_ALLOW_PROTOCOL=%q, want empty: GIT_NO_LAZY_FETCH is unrecognized before Git 2.45,"+
			" so every transport must independently be denied regardless of the inherited environment", allowProtocol)
	}

	grepCmd := newGitCmdWithCallerLocale(context.Background(), dir, "version")
	if grepCmd.WaitDelay != gitCommandWaitDelay || grepCmd.WaitDelay == 0 {
		t.Fatalf("caller-locale command WaitDelay = %v, want %v", grepCmd.WaitDelay, gitCommandWaitDelay)
	}
	lcAll, pwd, noReplace, noLazyFetch, allowProtocol = "", "", "", "", "unset"
	for _, kv := range grepCmd.Env {
		if v, ok := strings.CutPrefix(kv, "LC_ALL="); ok {
			lcAll = v
		}
		if v, ok := strings.CutPrefix(kv, "PWD="); ok {
			pwd = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_NO_REPLACE_OBJECTS="); ok {
			noReplace = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_NO_LAZY_FETCH="); ok {
			noLazyFetch = v
		}
		if v, ok := strings.CutPrefix(kv, "GIT_ALLOW_PROTOCOL="); ok {
			allowProtocol = v
		}
	}
	if lcAll != "fr_FR.UTF-8" {
		t.Fatalf("caller-locale git command LC_ALL=%q, want inherited locale", lcAll)
	}
	if runtime.GOOS != "windows" && filepath.Clean(pwd) != filepath.Clean(dir) {
		t.Fatalf("caller-locale git command PWD=%q, want command directory %q", pwd, dir)
	}
	if noReplace != "1" {
		t.Fatalf("caller-locale git command GIT_NO_REPLACE_OBJECTS=%q, want 1", noReplace)
	}
	if noLazyFetch != "1" {
		t.Fatalf("caller-locale git command GIT_NO_LAZY_FETCH=%q, want 1", noLazyFetch)
	}
	if allowProtocol != "" {
		t.Fatalf("caller-locale git command GIT_ALLOW_PROTOCOL=%q, want empty", allowProtocol)
	}
}

// TestGitAllowProtocolBlocksALazyFetchGitVersionIndependently pins the
// property the trail finding asked for directly: even if a caller's own
// GIT_NO_LAZY_FETCH were somehow ineffective (as it silently is on any Git
// older than 2.45, since the variable did not exist yet), a fetch attempt
// still cannot reach the network, because newCmd's GIT_ALLOW_PROTOCOL=
// denies every transport unconditionally.
func TestGitAllowProtocolBlocksALazyFetchGitVersionIndependently(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	// GIT_NO_LAZY_FETCH unset here (unlike newCmd's own environment) stands
	// in for a Git binary old enough that the variable does nothing, so a
	// pass only because of that guard would go undetected by this test.
	cmd := exec.Command("git", "-C", repo, "fetch", "file:///nonexistent-does-not-exist")
	cmd.Env = append(os.Environ(), noTransportProtocolEnv)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git fetch under GIT_ALLOW_PROTOCOL= succeeded, want every transport denied: %s", out)
	}
	if !strings.Contains(string(out), "not allowed") {
		t.Fatalf("git fetch under GIT_ALLOW_PROTOCOL= failed for an unexpected reason (want a transport-denial error): %s", out)
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

// gitOutputHonoringReplaceRefs is a test control, never a production command.
// GIT_NO_REPLACE_OBJECTS disables replacements whenever it is present, even
// when its value is "0", so remove every inherited occurrence explicitly.
func gitOutputHonoringReplaceRefs(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	env := cmd.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_NO_REPLACE_OBJECTS=") {
			filtered = append(filtered, entry)
		}
	}
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git honoring replacements %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestGrepTreePathsCaseSensitiveIncludingBinary pins the identifier-prefilter
// semantics: case-sensitive substring matching. A file containing the name
// only in a different case must not be selected, while substring occurrences
// still are (the result must stay a strict superset of an exact-token check;
// substring false positives are filtered later by the caller's token screen).
func TestGrepTreePathsCaseSensitiveIncludingBinary(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	files := map[string]string{
		"exact.py":     "value = Foo(1)\n",
		"case.py":      "value = foo(1)\n",
		"substring.py": "value = myFooBar\n",
		"dollar.js":    "value = $Foo;\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	commit := gitOutput(t, repo, "rev-parse", "HEAD")

	got, err := GrepTreePathsCaseSensitiveIncludingBinary(t.Context(), repo, commit, []string{"Foo"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"dollar.js", "exact.py", "substring.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-sensitive grep = %#v, want %#v", got, want)
	}
}

// TestOversizeBlobAtRevAnswersOnlyForOversizedBlobs pins the contract that lets
// a caller of ShowFileLimited record a file it was refused. The "found" return
// means "exists AND is over the ceiling", never merely "exists": a caller whose
// read failed for some other reason must not be able to report that file as too
// large, because it would then carry a size, hash and line count nothing
// established.
func TestOversizeBlobAtRevAnswersOnlyForOversizedBlobs(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const ceiling = 1 << 10
	over := strings.Repeat("x\n", ceiling) // 2*ceiling bytes, ceiling lines
	if err := os.WriteFile(filepath.Join(repo, "over.txt"), []byte(over), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "exact.txt"), []byte(strings.Repeat("y", ceiling)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "leaf.txt"), []byte("leaf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "size fixture")

	blob, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "over.txt", ceiling)
	if err != nil || !found {
		t.Fatalf("oversized blob = (found %v, err %v), want (true, nil)", found, err)
	}
	sum := sha256.Sum256([]byte(over))
	if blob.Bytes != int64(len(over)) || blob.Lines != ceiling || blob.Hash != hex.EncodeToString(sum[:]) {
		t.Errorf(
			"blob = {Bytes:%d Lines:%d Hash:%s}, want {Bytes:%d Lines:%d Hash:%s}",
			blob.Bytes, blob.Lines, blob.Hash, len(over), ceiling, hex.EncodeToString(sum[:]),
		)
	}

	// A blob exactly at the ceiling is not oversized: ShowFileLimited returns it,
	// so nothing here is owed a record.
	if _, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "exact.txt", ceiling); err != nil || found {
		t.Errorf("blob at the ceiling = (found %v, err %v), want (false, nil)", found, err)
	}
	// Absent is refused, not broken -- the same distinction the readers keep.
	if _, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "absent.txt", ceiling); err != nil || found {
		t.Errorf("absent path = (found %v, err %v), want (false, nil)", found, err)
	}
	// No ceiling means nothing is oversized, and no git runs.
	if _, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "over.txt", 0); err != nil || found {
		t.Errorf("disabled ceiling = (found %v, err %v), want (false, nil)", found, err)
	}
	// A tree is not a blob: git cannot cat it, and that is a failure to report,
	// not an absence -- the caller must not silently record a directory.
	if _, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "dir", ceiling); err == nil || found {
		t.Errorf("a tree path = (found %v, err %v), want (false, error)", found, err)
	}
}

// TestOversizeBlobAtRevNeverMaterializesTheBlob is the reason the function
// exists rather than callers reading the file and measuring it. The digest is
// computed while the blob streams past and is discarded, so learning a 32 MiB
// file's identity costs a bounded buffer, not 32 MiB.
func TestOversizeBlobAtRevNeverMaterializesTheBlob(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	const blobSize = 32 << 20
	const ceiling = 1 << 10
	// Incompressible, so git really does stream 32 MiB past the digest.
	blob := make([]byte, blobSize)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "huge.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "huge fixture")

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	record, found, err := OversizeBlobAtRev(t.Context(), repo, "HEAD", "huge.bin", ceiling)
	if err != nil || !found {
		t.Fatalf("OversizeBlobAtRev = (found %v, err %v), want (true, nil)", found, err)
	}
	if record.Bytes != blobSize {
		t.Fatalf("Bytes = %d, want %d", record.Bytes, blobSize)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	// Generous: the point is that the bound is a buffer, not the blob. A version
	// that held the content would allocate at least blobSize.
	if allocated > blobSize/4 {
		t.Fatalf("allocated %d bytes reading a %d-byte blob; it must never be held", allocated, blobSize)
	}
}
