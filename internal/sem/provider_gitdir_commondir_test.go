package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLooksLikeGitDirRejectsCommondirGitRefusesToRead is the over-exclusion side
// of the `commondir` rule. git decides a `commondir` is PRESENT with lstat
// (file_exists()), then reads it and dies if the read fails — so a `commondir`
// that is a dangling symlink, a directory, or empty makes git refuse the whole
// git directory. Verified on git 2.54.0:
//
//	$ ln -s /nonexistent/nowhere adm/commondir
//	$ git --git-dir=adm rev-parse --git-dir
//	fatal: failed to read adm/commondir: No such file or directory
//	$ mkdir admd/commondir            # same shape, a directory
//	fatal: failed to read admd/commondir: Is a directory
//
// os.Stat cannot tell a dangling symlink from an absent file — both are ENOENT —
// so the dangling case fell through the "no commondir file" branch, resolved
// objects/ and refs/ inside the directory itself, and called an ordinary fixture
// tree a git directory. Everything under it is then dropped from every snapshot.
func TestLooksLikeGitDirRejectsCommondirGitRefusesToRead(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		write func(t *testing.T, commondir string)
	}{
		{"dangling symlink", func(t *testing.T, commondir string) {
			symlinkOrSkip(t, filepath.Join("..", "nowhere"), commondir)
		}},
		{"directory", func(t *testing.T, commondir string) {
			if err := os.Mkdir(commondir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", func(t *testing.T, commondir string) {
			if err := os.WriteFile(commondir, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeGitDirFixture(t, dir, "d")
			testCase.write(t, filepath.Join(dir, "d", "commondir"))
			if looksLikeGitDir(filepath.Join(dir, "d")) {
				t.Error("looksLikeGitDir = true, want false: git refuses to read this commondir")
			}
		})
	}
}

// TestSearchRepositoryStillIndexesSourceUnderACommondirGitRefuses is the same
// over-exclusion end to end: a fixture tree that happens to carry the three
// names plus a broken `commondir` must keep its source in the index.
func TestSearchRepositoryStillIndexesSourceUnderACommondirGitRefuses(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "testdata/parser/HEAD", "ref: refs/heads/main\n")
	writeFile(t, repo, "testdata/parser/objects/blob.go", "package objects\n")
	writeFile(t, repo, "testdata/parser/refs/head.go", "package refs\n")
	writeFile(t, repo, "testdata/parser/app.go", "package parser\n\n// LoadOriginCredential returns the origin remote credential.\nfunc LoadOriginCredential() string { return \"\" }\n")
	symlinkOrSkip(t, filepath.Join("..", "nowhere"), filepath.Join(repo, "testdata", "parser", "commondir"))
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
		got = append(got, result.FilePath)
		if result.FilePath == "testdata/parser/app.go" {
			return
		}
	}
	t.Errorf("search did not return testdata/parser/app.go; results = %v", got)
}

// TestGitDirExcluderStillExcludesAPointerTargetWhoseCommondirGitRefuses is the
// under-exclusion side, and the reason the two rules cannot share one verdict.
// A `gitdir:` pointer is the second, independent piece of evidence, and a git
// directory git REFUSES is exactly the state that makes git decline the worktree
// and the filesystem fallback run. Resolving objects/ and refs/ through a
// commondir git will not read must therefore fall back to the directory itself
// here, or the pointer target's credentialed config is indexed.
func TestGitDirExcluderStillExcludesAPointerTargetWhoseCommondirGitRefuses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		write func(t *testing.T, commondir string)
	}{
		{"dangling symlink", func(t *testing.T, commondir string) {
			symlinkOrSkip(t, filepath.Join("..", "nowhere"), commondir)
		}},
		{"directory", func(t *testing.T, commondir string) {
			if err := os.Mkdir(commondir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", func(t *testing.T, commondir string) {
			if err := os.WriteFile(commondir, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			writeFile(t, repo, "nested/.git", "gitdir: ../.dead-git\n")
			writeHeadlessGitDirFixture(t, repo, ".dead-git")
			testCase.write(t, filepath.Join(repo, ".dead-git", "commondir"))
			writeFile(t, repo, "src/app.go", "package src\n")
			excluder := newGitDirExcluder(repo)
			excluder.observeListedPaths([]string{".dead-git/config", "src/app.go"}, nil)
			if !excluder.excluded(".dead-git/config") {
				t.Error(`excluded(".dead-git/config") = false, want true`)
			}
			if excluder.excluded("src/app.go") {
				t.Error(`excluded("src/app.go") = true, want false`)
			}
		})
	}
}
