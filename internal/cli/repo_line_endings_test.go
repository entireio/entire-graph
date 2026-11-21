package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryFileThatMustBeLFIsPinnedToLF guards the parts of the gate that a
// stock Windows clone breaks.
//
// core.autocrlf=true is the Git for Windows default, so a contributor who
// clones this repository there gets CRLF in every text file unless
// .gitattributes says otherwise. Nothing a pull request is checked on can see
// the result: every CI runner, the Windows ones included, checks out LF.
// Measured on a `git clone -c core.autocrlf=true` of 3f632e40, `gofmt -s -l .`
// listed all 872 Go files and `sh scripts/entire-graph-statusline_test.sh`
// exited 2 at its second line.
//
// Each class is asserted over every file in it rather than over the paths
// someone remembered, because the failure mode is the file added later. The
// script class is found by reading bytes rather than by matching names for the
// same reason: `*.sh` would miss an executable with no extension, which is a
// shape the sibling repository that shares internal/agentsetup already has for
// its task runner.
func TestEveryFileThatMustBeLFIsPinnedToLF(t *testing.T) {
	t.Parallel()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := strings.TrimSpace(string(root))

	t.Run("Go source", func(t *testing.T) {
		t.Parallel()
		files := trackedFiles(t, dir, "*.go")
		if len(files) == 0 {
			t.Fatal("non-vacuity: no Go file is tracked, so this proves nothing")
		}
		assertPinnedToLF(t, dir, files,
			"gofmt always writes LF, so `gofmt -s -l .` reports every CRLF file as unformatted on content nobody touched",
			"*.go")
	})

	t.Run("scripts with a shebang", func(t *testing.T) {
		t.Parallel()
		var scripts []string
		for _, name := range trackedFiles(t, dir, "") {
			if startsWithShebang(filepath.Join(dir, name)) {
				scripts = append(scripts, name)
			}
		}
		if len(scripts) == 0 {
			t.Fatal("non-vacuity: no tracked file begins with #!, so this proves nothing")
		}
		assertPinnedToLF(t, dir, scripts,
			"the CR on a `#!` line becomes part of the interpreter's name: a CRLF `#!/usr/bin/env python3` file exits 127 with `env: python3\\r: No such file or directory`, and `sh scripts/entire-graph-statusline_test.sh` exits 2",
			"the matching extension, or a path rule")
	})
}

func trackedFiles(t *testing.T, dir, pattern string) []string {
	t.Helper()
	args := []string{"-C", dir, "ls-files"}
	if pattern != "" {
		args = append(args, pattern)
	}
	listed, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git ls-files %s: %v", pattern, err)
	}
	return strings.Fields(string(listed))
}

// startsWithShebang reads the two bytes the kernel reads. A path that cannot be
// opened is not a script this checkout runs.
func startsWithShebang(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var head [2]byte
	n, err := file.Read(head[:])
	return err == nil && n == 2 && head[0] == '#' && head[1] == '!'
}

func assertPinnedToLF(t *testing.T, dir string, files []string, breaks, remedy string) {
	t.Helper()
	// check-attr takes its paths as arguments, and this repository has hundreds
	// of them, so they go on stdin rather than against the argument limit as the
	// list grows.
	command := exec.Command("git", "-C", dir, "check-attr", "--stdin", "eol")
	command.Stdin = strings.NewReader(strings.Join(files, "\n"))
	out, err := command.Output()
	if err != nil {
		t.Fatalf("git check-attr: %v", err)
	}
	var unpinned []string
	for _, line := range strings.Split(string(out), "\n") {
		// "<path>: eol: <value>"
		if line == "" || strings.HasSuffix(line, ": eol: lf") {
			continue
		}
		unpinned = append(unpinned, line)
	}
	if len(unpinned) == 0 {
		t.Logf("checked %d file(s)", len(files))
		return
	}
	shown := unpinned
	if len(shown) > 10 {
		shown = shown[:10]
	}
	t.Fatalf("%d of %d file(s) are not pinned to LF, so a default Windows clone (core.autocrlf=true) checks them out with CRLF, where %s:\n  %s\n\npin them in .gitattributes with `%s text eol=lf`",
		len(unpinned), len(files), breaks, strings.Join(shown, "\n  "), remedy)
}
