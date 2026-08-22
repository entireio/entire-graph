package sem

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestGitDirRootEntryNamesCoversEveryNameGitWrites is the lock on
// gitDirRootEntryNames, and it is an ORACLE differential rather than a reading
// of git's source: it drives the real git binary through the operations that
// leave state in a git directory, then reports every top-level entry git created
// that the list does not cover.
//
// The list is consulted in exactly one situation — a `.git` pointer names the
// repository ROOT, so the git directory and the worktree are one directory and
// only a name can separate them — and a name it lacks is a piece of the git
// directory handed to the index. Hard-coding names from one reading of git's
// path.c is what let `reftable` through: on git 2.54.0, a repository initialized
// with `--ref-format=reftable` keeps its whole ref store under `reftable/`, and
// the list, written against the loose-ref layout, had never heard of the name.
//
// The battery is deliberately made of ordinary porcelain, so it keeps answering
// for whatever git the runner has, and every step is optional: a git too old for
// one of them skips that step rather than the test.
func TestGitDirRootEntryNamesCoversEveryNameGitWrites(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary required to ask git what it writes into a git directory")
	}

	covered := map[string]struct{}{}
	for _, name := range gitDirRootEntryNames {
		covered[name] = struct{}{}
	}

	found := map[string]string{}
	for _, scenario := range gitStateScenarios() {
		for _, gitDir := range scenario.run(t) {
			for _, name := range topLevelNames(t, gitDir) {
				if _, ok := covered[name]; ok {
					continue
				}
				if _, ok := found[name]; !ok {
					found[name] = scenario.name
				}
			}
		}
	}

	missing := make([]string, 0, len(found))
	for name := range found {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("git wrote %q into a git directory (%s) and gitDirRootEntryNames does not cover it: "+
			"where a `.git` pointer names the repository root, that entry is indexed as worktree content",
			name, found[name])
	}
}

// gitStateScenario is one battery step: a name for the failure message and a
// body that builds a repository and returns the git directories to inspect.
type gitStateScenario struct {
	name string
	run  func(t *testing.T) []string
}

func gitStateScenarios() []gitStateScenario {
	return []gitStateScenario{
		{"merge conflict with rerere", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("merge", "topic")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"cherry-pick conflict", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("cherry-pick", "topic")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"revert conflict", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("revert", "--no-edit", "HEAD~1")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"rebase conflict", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("rebase", "topic")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"am conflict", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			patches := t.TempDir()
			if !git.try("format-patch", "-1", "topic", "-o", patches) {
				return nil
			}
			entries, err := os.ReadDir(patches)
			if err != nil || len(entries) == 0 {
				return nil
			}
			git.try("am", filepath.Join(patches, entries[0].Name()))
			return []string{filepath.Join(repo, ".git")}
		}},
		{"merge with autostash", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			writeFile(t, repo, "f.txt", "dirty\n")
			git.try("merge", "--autostash", "topic")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"editor buffers", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			// Both leave their buffer in the git directory. The tag is REFUSED for
			// an empty message, which is the point: the buffer outlives the command.
			git.try("tag", "-a", "v1")
			git.try("branch", "--edit-description")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"bisect in progress", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("bisect", "start")
			git.try("bisect", "bad")
			git.try("bisect", "good", "HEAD~1")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"bisect without a checkout", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("bisect", "start", "--no-checkout")
			git.try("bisect", "bad")
			git.try("bisect", "good", "HEAD~1")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"notes merge conflict", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("notes", "add", "-m", "ours", "HEAD")
			git.try("notes", "--ref", "theirs", "add", "-m", "theirs", "HEAD")
			git.try("notes", "merge", "-s", "manual", "theirs")
			return []string{filepath.Join(repo, ".git")}
		}},
		{"reftable ref store", func(t *testing.T) []string {
			repo := t.TempDir()
			git := gitIn(t, repo)
			if !git.try("init", "--ref-format=reftable", ".") {
				return nil
			}
			commitOnce(t, repo, git)
			return []string{filepath.Join(repo, ".git")}
		}},
		{"fetch, stash, tag, gc and a linked worktree", func(t *testing.T) []string {
			repo, git := divergedRepo(t)
			git.try("fetch", ".", "HEAD")
			git.try("stash", "push", "--include-untracked")
			git.try("tag", "-a", "v1", "-m", "release")
			git.try("gc")
			linked := filepath.Join(t.TempDir(), "linked")
			git.try("worktree", "add", linked, "-b", "wt")
			return []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", "wt")}
		}},
		{"shallow clone", func(t *testing.T) []string {
			source, _ := divergedRepo(t)
			clone := filepath.Join(t.TempDir(), "clone")
			git := gitIn(t, t.TempDir())
			if !git.try("clone", "--depth", "1", "--no-local", "file://"+filepath.ToSlash(source), clone) {
				return nil
			}
			return []string{filepath.Join(clone, ".git")}
		}},
	}
}

// gitRunner runs git in one directory. try reports whether the command
// succeeded, and a failure is never fatal: a conflict is an expected non-zero
// exit, and an operation this git does not support is a step to skip.
type gitRunner struct {
	t   *testing.T
	dir string
}

func gitIn(t *testing.T, dir string) gitRunner { return gitRunner{t: t, dir: dir} }

func (g gitRunner) try(args ...string) bool {
	g.t.Helper()
	command := exec.Command("git", args...)
	command.Dir = g.dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+filepath.Join(g.dir, "absent-global-config"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(g.dir, "absent-system-config"),
	)
	return command.Run() == nil
}

// divergedRepo builds a repository whose branch `topic` and current branch
// changed the same line, so every conflicting operation below actually
// conflicts. It returns the worktree and a runner rooted there.
func divergedRepo(t *testing.T) (string, gitRunner) {
	t.Helper()
	repo := t.TempDir()
	// Git makes loose objects read-only, which stops RemoveAll on Windows, so
	// the tree is made writable before t.TempDir removes it. Cleanups run
	// last-registered-first, so this runs ahead of that removal.
	t.Cleanup(func() { makeWritable(repo) })
	git := gitIn(t, repo)
	if !git.try("init", ".") {
		t.Skip("git init unavailable here")
	}
	git.try("config", "rerere.enabled", "true")
	git.try("config", "gc.auto", "0")
	commitOnce(t, repo, git)
	git.try("checkout", "-b", "topic")
	writeFile(t, repo, "f.txt", "topic\n")
	git.try("commit", "-am", "topic")
	git.try("checkout", "-")
	writeFile(t, repo, "f.txt", "main\n")
	git.try("commit", "-am", "main")
	return repo, git
}

func commitOnce(t *testing.T, repo string, git gitRunner) {
	t.Helper()
	t.Cleanup(func() { makeWritable(repo) })
	writeFile(t, repo, "f.txt", "base\n")
	git.try("add", "-A")
	git.try("commit", "-m", "base")
}

func makeWritable(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chmod(path, info.Mode()|0o200)
		return nil
	})
}

// topLevelNames lists one git directory's own entries, skipping the lock files
// a concurrently-running git leaves behind, which are transient rather than
// content.
func topLevelNames(t *testing.T, gitDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

// TestGitDirExcluderExcludesASharedIndexFileAtARootGitDir reproduces the trail
// finding on gitDirRootEntryNames: `git update-index --split-index` writes
// `sharedindex.<hash>` beside `index` with a fresh content-addressed hash each
// time, so no fixed name in that list can ever cover it. It has to be found by
// listing the git directory's own entries and matching the `sharedindex.`
// prefix, the same way gitDirRootEntryPrefixes documents.
func TestGitDirExcluderExcludesASharedIndexFileAtARootGitDir(t *testing.T) {
	t.Parallel()
	t.Run("at a root git directory it goes", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		writeRootGitDirFixture(t, repo)
		writeFile(t, repo, ".git", "gitdir: .\n")
		// A real hash would do, but any name with the prefix is git-owned index
		// state regardless of what the hash algorithm or length happens to be.
		writeFile(t, repo, "sharedindex.deadbeefcafef00d", "\n")
		writeFile(t, repo, "src/app.go", "package src\n")

		excluder := newGitDirExcluder(t.Context(), repo)
		if !excluder.excluded("sharedindex.deadbeefcafef00d") {
			t.Error(`excluded("sharedindex.deadbeefcafef00d") = false, want true: the pointer names the root, so this is the git directory's own split-index state`)
		}
		if excluder.excluded("src/app.go") {
			t.Error(`excluded("src/app.go") = true, want false`)
		}
	})
	t.Run("in an ordinary repository it stays", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		writeFile(t, repo, "sharedindex.go", "package main\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

		assertSearchReturns(t, repo, "sharedindex.go")
	})
}

// TestSearchRepositoryNeverIndexesTheReftableRefStoreAtARootGitDir is the leak
// the list above was missing, end to end and with its narrowing guard.
//
// The shape is the one recordGitDirRootEntries exists for — a `.git` pointer
// naming the repository ROOT, so git directory and worktree are one directory —
// and the repository uses the reftable ref store. Verified on git 2.54.0:
//
//	$ git init --ref-format=reftable rt && cp -R rt/.git/. root/ && cd root
//	$ printf 'gitdir: .\n' > .git
//	$ git rev-parse --git-dir
//	<root>
//	$ git ls-files --cached --others --exclude-standard --directory
//	HEAD
//	config
//	description
//	hooks/
//	info/
//	objects/
//	refs/
//	reftable/
//	src/
//
// `reftable/` holds what `refs/`, `packed-refs` and `logs/` hold in the loose
// layout — the ref records and the reflogs — and all three of those names were
// excluded while this one was indexed.
func TestSearchRepositoryNeverIndexesTheReftableRefStoreAtARootGitDir(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeRootGitDirFixture(t, repo)
	writeFile(t, repo, ".git", "gitdir: .\n")
	// One plant per newly covered name, each holding the text a search for the
	// credential ranks, so the assertion below is about the exclusion and not
	// about whether a particular encoding survives the indexer's binary test.
	// `TAG_EDITMSG` and `EDIT_DESCRIPTION` are plain prose git wrote from an
	// editor buffer; `reftable/` is the ref store; `MERGE_RR` and `AUTO_MERGE`
	// are conflict state.
	for _, name := range []string{
		"reftable/tables.list", "TAG_EDITMSG", "EDIT_DESCRIPTION", "MERGE_RR", "AUTO_MERGE",
	} {
		writeFile(t, repo, name, gitDirConfigWithCredential)
	}
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, "reftable", "TAG_EDITMSG", "EDIT_DESCRIPTION", "MERGE_RR", "AUTO_MERGE")
	assertSearchReturns(t, repo, "src/app.go")
}

// TestGitDirExcluderExcludesTheReftableRefStoreOnlyAtARootGitDir is the
// widening guard on the same name. `reftable` is excluded because a pointer said
// the root is a git directory, not because of what it is called: a project that
// ships a `reftable/` package keeps it.
func TestGitDirExcluderExcludesTheReftableRefStoreOnlyAtARootGitDir(t *testing.T) {
	t.Parallel()
	t.Run("at a root git directory it goes", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		writeRootGitDirFixture(t, repo)
		writeFile(t, repo, ".git", "gitdir: .\n")
		writeFile(t, repo, "reftable/tables.list", "\n")
		writeFile(t, repo, "src/app.go", "package src\n")

		excluder := newGitDirExcluder(t.Context(), repo)
		if !excluder.excluded("reftable/tables.list") {
			t.Error(`excluded("reftable/tables.list") = false, want true: the pointer names the root, so this is the git directory's own ref store`)
		}
		if excluder.excluded("src/app.go") {
			t.Error(`excluded("src/app.go") = true, want false`)
		}
	})
	t.Run("in an ordinary repository it stays", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		writeFile(t, repo, "reftable/reader.go", "package reftable\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

		assertSearchReturns(t, repo, "reftable/reader.go")
	})
}
