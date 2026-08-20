package sem

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitPointerNULPadding is the junk an attacker writes AFTER the NUL that ends a
// pointer's path. Git never sees it — it reads the file into a NUL-terminated
// buffer and hands the buffer on as a C string — so every one of these fixtures
// is a pointer git follows.
const gitPointerNULPadding = "\x00junkjunkjunk\n"

// TestGitCommonDirStopsAtTheFirstNUL is the `commondir` half of the byte rule
// the `.git` gitfile already had. Git ends BOTH paths at the first NUL, and
// verified on git 2.54.0 it accepts the git directory that results:
//
//	$ printf '../realcommon\0junkjunkjunk\n' > adm/commondir
//	$ git --git-dir=adm rev-parse --git-common-dir
//	<tmp>/realcommon
//	$ git --git-dir=adm rev-parse --git-dir
//	adm
//
// Keeping the NUL looked objects/ and refs/ up at a path nothing on disk is
// called, so the structural rule called a linked worktree's administrative git
// directory ordinary content and left its config indexable.
func TestGitCommonDirStopsAtTheFirstNUL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGitDirFixture(t, dir, "common")
	writeFile(t, dir, "d/HEAD", "ref: refs/heads/main\n")
	writeFile(t, dir, "d/commondir", "../common"+gitPointerNULPadding)
	if !looksLikeGitDir(filepath.Join(dir, "d")) {
		t.Error("looksLikeGitDir = false for a git directory whose commondir ends at a NUL, want true")
	}
}

// TestGitCommonDirFollowsALargeCommondirTerminatedByNUL pins the other half of
// the same defect: the size test must not run before the byte rule. Git puts NO
// size limit on `commondir` at all — a 200 KiB one whose path ends at byte 13 is
// resolved on git 2.54.0 — so a whole-file bound refused git directories git
// accepts, by the same one byte.
func TestGitCommonDirFollowsALargeCommondirTerminatedByNUL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGitDirFixture(t, dir, "common")
	writeFile(t, dir, "d/HEAD", "ref: refs/heads/main\n")
	writeFile(t, dir, "d/commondir", "../common\x00"+strings.Repeat("A", 4*maxGitPointerBytes)+"\n")
	if !looksLikeGitDir(filepath.Join(dir, "d")) {
		t.Error("looksLikeGitDir = false for a large commondir whose path ends at a NUL, want true")
	}
}

// TestSearchRepositoryNeverIndexesAGitDirWhoseCommondirHoldsANUL is the same
// leak end to end, on the shape that has no pointer to fall back on: a linked
// worktree's administrative git directory is not named `.git` and nothing names
// it, so the structural rule is the only one in reach.
func TestSearchRepositoryNeverIndexesAGitDirWhoseCommondirHoldsANUL(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeGitDirFixture(t, repo, "shared-git")
	writeFile(t, repo, "state/.wt-git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, repo, "state/.wt-git/commondir", "../../shared-git"+gitPointerNULPadding)
	writeFile(t, repo, "state/.wt-git/config", gitDirConfigWithCredential)
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, "state/.wt-git")
	assertSearchReturns(t, repo, "src/app.go")
}

// TestGitDirPointerFollowsALargePointerTerminatedByNUL pins the pointer's size
// test behind the byte rule. Git refuses a `.git` file only above 1 MiB, and the
// path ends at the first NUL, so the bytes that decide "too large" are not the
// bytes that decide the target. Verified on git 2.54.0 with a
// `--separate-git-dir` worktree:
//
//	$ { printf 'gitdir: %s/.repo-git\0' "$PWD"; head -c 65536 /dev/zero | tr '\0' A; } > .git
//	$ git rev-parse --git-dir
//	<abs>/.repo-git
//	$ { printf 'gitdir: x\0'; head -c 1048569 /dev/zero | tr '\0' A; } > .git
//	$ git rev-parse --git-dir
//	fatal: too large to be a .git file: '<abs>/.git'
func TestGitDirPointerFollowsALargePointerTerminatedByNUL(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.git", "gitdir: ../.dep-git\x00"+strings.Repeat("A", 4*maxGitPointerBytes)+"\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")
	got, ok, hidden := gitDirPointerTarget(repo, "nested")
	if !ok || hidden || got != ".dep-git" {
		t.Errorf("gitDirPointerTarget = (%q, %v, hidden %v), want (%q, true, false)", got, ok, hidden, ".dep-git")
	}
}

// TestSearchRepositoryNeverIndexesAGitDirNamedByALargePointer is that leak end
// to end. The target's HEAD is missing, which is the state that makes git refuse
// the worktree and the filesystem fallback run, so the pointer is the ONLY
// evidence naming it — and refusing the pointer on the file's size threw that
// evidence away and indexed the credentialed config.
func TestSearchRepositoryNeverIndexesAGitDirNamedByALargePointer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "nested/.git", "gitdir: ../.dep-git\x00"+strings.Repeat("A", 4*maxGitPointerBytes)+"\n")
	writeHeadlessGitDirFixture(t, repo, ".dep-git")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, ".dep-git")
	assertSearchReturns(t, repo, "src/app.go")
}

// TestGitDirExcluderExcludesGitFilesWhenAPointerNamesTheRepositoryRoot covers
// the third spelling of the same mistake: `gitdir: .` is a pointer git follows,
// to the repository root. Verified on git 2.54.0, with the git directory's own
// files at the top of the worktree:
//
//	$ printf 'gitdir: .\n' > .git
//	$ git rev-parse --git-dir
//	<repo>
//	$ git ls-files --cached --others --exclude-standard --directory
//	HEAD
//	app.go
//	config
//	objects/
//	refs/
//
// containedRel called the root "outside the repository", so no target was
// recorded at all and `config` was ranked with its credential. Both directions
// are in one test on purpose: the git directory's own entries go, and the
// caller's source stays — excluding the root itself would return nothing.
func TestGitDirExcluderExcludesGitFilesWhenAPointerNamesTheRepositoryRoot(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		pointer string
		dir     string
	}{
		{"gitdir: . at the root", "gitdir: .\n", ""},
		{"gitdir: .. from a subdirectory", "gitdir: ..\n", "sub"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeRootGitDirFixture(t, repo)
			writeFile(t, repo, filepath.Join(testCase.dir, ".git"), testCase.pointer)
			writeFile(t, repo, "src/app.go", "package src\n")

			excluder := newGitDirExcluder(t.Context(), repo)
			excluder.observeListedPaths([]string{"config", "HEAD", "hooks/post-commit.go", "src/app.go"}, nil)
			if testCase.dir != "" {
				excluder.observe(testCase.dir)
			}
			for _, gitOwned := range []string{"config", "HEAD", "hooks/post-commit.go", "objects", "refs"} {
				if !excluder.excluded(gitOwned) {
					t.Errorf("excluded(%q) = false, want true: the pointer names the root, so this is the git directory's own file", gitOwned)
				}
			}
			if excluder.excluded("src/app.go") {
				t.Error(`excluded("src/app.go") = true, want false: the caller's source is not the git directory`)
			}
		})
	}
}

// TestSearchRepositoryNeverIndexesTheRootGitDirNamedByADotPointer is that leak
// end to end, and its narrowing guard: the credential goes, the source stays.
func TestSearchRepositoryNeverIndexesTheRootGitDirNamedByADotPointer(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeRootGitDirFixture(t, repo)
	writeFile(t, repo, ".git", "gitdir: .\n")
	writeFile(t, repo, "src/app.go", "package src\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")

	assertNoGitDirLeak(t, repo, "config", "hooks")
	assertSearchReturns(t, repo, "src/app.go")
}

// TestSearchRepositoryStillIndexesGitNamedFilesInAnOrdinaryRepository is the
// widening guard on the same rule, and the reason it is spelled as "a pointer
// named the ROOT" rather than "these names are git's". A project that ships a
// `config` file, a `hooks/` directory and an `info/` directory at its top level
// is ordinary source, and nothing there says the root is a git directory.
func TestSearchRepositoryStillIndexesGitNamedFilesInAnOrdinaryRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "config", "# origin remote credential loader defaults\n")
	writeFile(t, repo, "hooks/post-commit.go", "package hooks\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	writeFile(t, repo, "info/notes.go", "package info\n\n// OriginRemoteCredential documents the origin remote credential loader.\nconst OriginRemoteCredential = \"\"\n")
	for _, name := range []string{"config", "hooks/post-commit.go", "info/notes.go"} {
		assertSearchReturns(t, repo, name)
	}
}

// writeRootGitDirFixture makes the repository root itself a git directory: the
// three names is_git_directory() asks for, plus the config whose remote URL
// holds the credential.
func writeRootGitDirFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, repo, "HEAD", "ref: refs/heads/main\n")
	writeFile(t, repo, "config", gitDirConfigWithCredential)
	writeFile(t, repo, "hooks/post-commit.go", gitDirHookSource)
	for _, sub := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(repo, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// assertSearchReturns is the widening half of every fixture above: a narrowing
// that also deletes the caller's source has traded one bug for another.
func assertSearchReturns(t *testing.T, repo, want string) {
	t.Helper()
	response, err := SearchRepository(t.Context(), repo, "test", "origin remote credential loader", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, result := range response.Results {
		if result.FilePath == want {
			return
		}
		got = append(got, result.FilePath)
	}
	t.Errorf("search did not return %q; results = %v", want, got)
}

// TestGitPointerFilesAreReadThroughOneReader is the structural half of this
// round's fix, and the one that survives the next reader somebody adds.
//
// Three separate readers of the same two files disagreed about git's byte rule
// at once — the excluder's gitfile parser had the NUL rule, its `commondir`
// reader did not, and ignore.go's info/exclude resolver had neither it nor the
// concatenating join — because each one opened the file itself. There is one
// reader now, readGitPointerFile, and this test fails if a function grows its
// own: naming `.git`, `gitdir: ` or `commondir` AND reading a file directly is
// the shape of a bypass.
func TestGitPointerFilesAreReadThroughOneReader(t *testing.T) {
	t.Parallel()
	const sink = "readGitPointerFile"
	pointerLiterals := []string{`".git"`, `"commondir"`, `"gitdir: "`}
	rawReaders := map[string]struct{}{"os.ReadFile": {}, "os.Open": {}}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			function, isFunction := decl.(*ast.FuncDecl)
			if !isFunction || function.Body == nil || function.Name.Name == sink {
				continue
			}
			var namesAPointerFile bool
			var readsDirectly string
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.BasicLit:
					for _, literal := range pointerLiterals {
						if typed.Value == literal {
							namesAPointerFile = true
						}
					}
				case *ast.CallExpr:
					selector, isSelector := typed.Fun.(*ast.SelectorExpr)
					if !isSelector {
						return true
					}
					pkg, isIdent := selector.X.(*ast.Ident)
					if !isIdent {
						return true
					}
					if _, raw := rawReaders[pkg.Name+"."+selector.Sel.Name]; raw {
						readsDirectly = pkg.Name + "." + selector.Sel.Name
					}
				}
				return true
			})
			if namesAPointerFile && readsDirectly != "" {
				t.Errorf("%s: %s names a git pointer file and reads it with %s; read it through %s so git's byte rule applies once",
					name, function.Name.Name, readsDirectly, sink)
			}
		}
	}
}
