package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitx(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil && args[0] != "merge" {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestChangedFilesParsesEveryTwoTreeDiffShape pins the contract of the --raw
// parser against the output git can actually produce.
//
// ChangedFiles moved from `--name-status` to `-z --raw` so it could carry tree
// entry MODES, which is what lets a symlink be classified without reading it.
// The new parser is stricter than the one it replaced: it returns an error on
// metadata it does not recognise, where the old one skipped the record. That is
// a change to a shared helper, so the question is not "is strict better" but
// "can git hand it something it would now reject".
//
// This walks the shapes a two-tree `git diff` emits: rename (two pathnames in
// one record), add, delete, mode change, pathnames containing a space and a
// double quote (which -z emits unquoted, and `--name-status` without -z would
// C-quote), a MERGE commit as head (which must still be a plain raw diff and
// never the `::`-prefixed combined form that would break the parser), an empty
// diff, and a pathspec matching nothing.
//
// The strictness is therefore fail-closed on input git cannot produce here,
// which for a semantic diff is the right direction: a silently partial file list
// is a wrong answer, not a degraded one.
func TestChangedFilesParsesEveryTwoTreeDiffShape(t *testing.T) {
	repo := t.TempDir()
	gitx(t, repo, "init", "-q", "-b", "main")
	w := func(name, body string) {
		p := filepath.Join(repo, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("a.go", "package a\n")
	w("with space.go", "package b\n")
	// The discriminating shape for this parser is a pathname `--name-status`
	// C-QUOTES while `-z --raw` emits verbatim. A double quote is one such name
	// and was used here first, but Windows cannot hold it at all — the file
	// cannot be created, nor can any later checkout of a tree containing it,
	// which took the branch/merge half of this walk down with it. Guarding it
	// behind GOOS kept the suite green and gave the case up on the one platform
	// whose path rules differ most.
	//
	// A NON-ASCII name has the identical property and every platform accepts it,
	// because core.quotePath defaults to true:
	//
	//	git diff --name-status  ->  D  "\346\227\245\346\234\254.go"   (C-quoted)
	//	git diff -z --raw       ->  D  日本.go                          (verbatim)
	//
	// It is deliberately CJK rather than an accented Latin name. macOS and
	// Windows normalize filenames differently (NFD vs NFC), so a decomposable
	// character like the e-acute in "café" can come back from the filesystem
	// with different bytes than were written, and this test compares the
	// pathname the parser returns. CJK ideographs have no canonical
	// decomposition, so the two normal forms are identical and the comparison is
	// stable everywhere.
	quoted := "日本.go"
	w(quoted, "package c\n")
	gitx(t, repo, "add", "-A")
	gitx(t, repo, "commit", "-qm", "base")
	base := gitx(t, repo, "rev-parse", "HEAD")[:40]

	// rename + mode change + delete + add on a branch
	gitx(t, repo, "mv", "a.go", "renamed.go")
	w("exec.sh", "#!/bin/sh\n")
	gitx(t, repo, "add", "-A")
	gitx(t, repo, "update-index", "--chmod=+x", "exec.sh")
	_ = os.Remove(filepath.Join(repo, quoted))
	gitx(t, repo, "add", "-A")
	gitx(t, repo, "commit", "-qm", "churn")
	head := gitx(t, repo, "rev-parse", "HEAD")[:40]

	files, err := ChangedFiles(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatalf("ChangedFiles on a rename/mode/delete/add/odd-name diff errored: %v", err)
	}
	for _, f := range files {
		t.Logf("  %s old=%q new=%q mode %s->%s", f.Status, f.OldPath, f.Path, f.OldMode, f.NewMode)
	}

	// A MERGE commit as head: the two-tree diff must still be a plain raw diff,
	// never a combined (::) one, which is the shape that would break the parser.
	gitx(t, repo, "checkout", "-qb", "side", base)
	w("side.go", "package s\n")
	gitx(t, repo, "add", "-A")
	gitx(t, repo, "commit", "-qm", "side")
	gitx(t, repo, "checkout", "-q", "main")
	gitx(t, repo, "merge", "--no-edit", "-q", "side")
	merge := gitx(t, repo, "rev-parse", "HEAD")[:40]
	mfiles, err := ChangedFiles(context.Background(), repo, base, merge, nil)
	if err != nil {
		t.Fatalf("ChangedFiles with a MERGE commit as head errored: %v", err)
	}
	t.Logf("merge-head diff: %d files", len(mfiles))

	// Empty diff (identical trees) must not error.
	if _, err := ChangedFiles(context.Background(), repo, base, base, nil); err != nil {
		t.Fatalf("ChangedFiles on an empty diff errored: %v", err)
	}
	// A pathspec matching nothing must not error.
	if _, err := ChangedFiles(context.Background(), repo, base, head, []string{"nope/"}); err != nil {
		t.Fatalf("ChangedFiles with a non-matching pathspec errored: %v", err)
	}
}
