package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// headKeyRepo is a checkout HeadKeyWithoutGit accepts, laid out by hand without git: .git/HEAD
// naming refs/heads/main, that ref's loose file, a one-section config, and the refs and objects
// directories. mutate, when not nil, changes it before the key is read.
func headKeyRepo(t *testing.T, mutate func(t *testing.T, repo string)) string {
	t.Helper()
	repo := t.TempDir()
	for _, dir := range []string{".git/objects", ".git/refs/heads"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeHeadKeyFile(t, repo, ".git/HEAD", "ref: refs/heads/main\n")
	writeHeadKeyFile(t, repo, ".git/refs/heads/main", strings.Repeat("a", 40)+"\n")
	writeHeadKeyFile(t, repo, ".git/config", "[core]\n\tbare = false\n")
	if mutate != nil {
		mutate(t, repo)
	}
	return repo
}

func writeHeadKeyFile(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeHeadKeyPath(t *testing.T, repo, name string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(repo, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

// TestHeadKeyWithoutGitRefusesOnlyWhatNeedsGit catches a key kept for a layout git interprets
// differently, and a warm path lost to a word in a value: an [include] or [includeIf] section and
// a worktreeConfig or refStorage key refuse the key, as do a .git that is a file, a commondir, a
// missing HEAD, refs or objects, a symbolic loose ref, a HEAD that is neither a ref nor an id and
// a config with a second name; a branch, a remote URL or a comment that only mentions "include" does not.
func TestHeadKeyWithoutGitRefusesOnlyWhatNeedsGit(t *testing.T) {
	t.Parallel()
	config := func(content string) func(t *testing.T, repo string) {
		return func(t *testing.T, repo string) { writeHeadKeyFile(t, repo, ".git/config", content) }
	}
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, repo string)
		ok     bool
	}{
		{name: "plain", ok: true},
		{name: "branch named include", mutate: config("[branch \"feature/include-paths\"]\n\tremote = origin\n"), ok: true},
		{name: "remote url naming include", mutate: config("[remote \"origin\"]\n\turl = https://example.com/include.git\n"), ok: true},
		{name: "commented include", mutate: config("# [include]\n; path = other\n[core]\n\tbare = false\n"), ok: true},
		{name: "include section", mutate: config("[include]\n\tpath = other\n"), ok: false},
		{name: "include section in capitals", mutate: config("[Include]\n\tpath = other\n"), ok: false},
		{name: "includeIf section", mutate: config("[includeIf \"gitdir:~/work/\"]\n\tpath = other\n"), ok: false},
		{name: "include after another header on the line", mutate: config("[core] [include]\n\tpath = other\n"), ok: false},
		{name: "includeIf after another header on the line", mutate: config("[extensions] [includeIf \"gitdir:x\"]\n\tpath = y\n"), ok: false},
		{name: "include section after a byte order mark", mutate: config("\xef\xbb\xbf[include]\n\tpath = other\n"), ok: false},
		{name: "worktreeConfig key", mutate: config("[extensions]\n\tworktreeConfig = true\n"), ok: false},
		{name: "refStorage key after the header", mutate: config("[extensions] refStorage = reftable\n"), ok: false},
		{name: "git file", mutate: func(t *testing.T, repo string) {
			removeHeadKeyPath(t, repo, ".git")
			writeHeadKeyFile(t, repo, ".git", "gitdir: ../elsewhere/.git/worktrees/x\n")
		}, ok: false},
		{name: "commondir", mutate: func(t *testing.T, repo string) { writeHeadKeyFile(t, repo, ".git/commondir", "../..\n") }, ok: false},
		{name: "no HEAD", mutate: func(t *testing.T, repo string) { removeHeadKeyPath(t, repo, ".git/HEAD") }, ok: false},
		{name: "no refs directory", mutate: func(t *testing.T, repo string) { removeHeadKeyPath(t, repo, ".git/refs") }, ok: false},
		{name: "no objects directory", mutate: func(t *testing.T, repo string) { removeHeadKeyPath(t, repo, ".git/objects") }, ok: false},
		{name: "HEAD neither a ref nor an id", mutate: func(t *testing.T, repo string) { writeHeadKeyFile(t, repo, ".git/HEAD", "garbage\n") }, ok: false},
		{name: "symbolic loose ref", mutate: func(t *testing.T, repo string) {
			writeHeadKeyFile(t, repo, ".git/refs/heads/main", "ref: refs/heads/other\n")
		}, ok: false},
		{name: "config with a second name", mutate: func(t *testing.T, repo string) {
			if err := os.Link(filepath.Join(repo, ".git", "config"), filepath.Join(repo, "config-alias")); err != nil {
				t.Skipf("no hard links here: %v", err)
			}
		}, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := headKeyRepo(t, tc.mutate)
			if _, _, ok := HeadKeyWithoutGit(repo); ok != tc.ok {
				t.Fatalf("HeadKeyWithoutGit ok = %v, want %v", ok, tc.ok)
			}
		})
	}
}

// TestHeadKeyWithoutGitFollowsPackedRefs catches a key that misses a packed-only ref moving: with
// no loose file the key covers the ref's packed-refs line and the `^` peeled line under it, CRLF
// line ends included, and HEAD naming a ref in neither place is unborn.
func TestHeadKeyWithoutGitFollowsPackedRefs(t *testing.T) {
	t.Parallel()
	const header = "# pack-refs with: peeled fully-peeled sorted \n"
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	key := func(t *testing.T, packedRefs string) (string, bool) {
		t.Helper()
		repo := headKeyRepo(t, func(t *testing.T, repo string) {
			removeHeadKeyPath(t, repo, ".git/refs/heads/main")
			if packedRefs != "" {
				writeHeadKeyFile(t, repo, ".git/packed-refs", packedRefs)
			}
		})
		key, unborn, ok := HeadKeyWithoutGit(repo)
		if !ok {
			t.Fatalf("packed-refs %q: the key was refused", packedRefs)
		}
		return key, unborn
	}
	base, unborn := key(t, header+a+" refs/heads/main\n")
	if unborn {
		t.Fatal("a ref with a packed-refs line is unborn")
	}
	if moved, _ := key(t, header+b+" refs/heads/main\n"); moved == base {
		t.Error("the key did not change when the packed-only ref moved")
	}
	if peeled, _ := key(t, header+a+" refs/heads/main\n^"+b+"\n"); peeled == base {
		t.Error("the key did not change with the peeled line under the ref")
	}
	if _, unborn := key(t, strings.ReplaceAll(header+a+" refs/heads/main\n", "\n", "\r\n")); unborn {
		t.Error("a CRLF packed-refs line was not found")
	}
	if _, unborn := key(t, header+a+" refs/heads/other\n"); !unborn {
		t.Error("a ref named by no packed-refs line is not unborn")
	}
	if _, unborn := key(t, ""); !unborn {
		t.Error("a ref with neither a loose file nor packed-refs is not unborn")
	}
}
