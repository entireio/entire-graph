package sem

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Reproduces the finding: RenderRepoIgnoreDisclosure and its JSON twin echo
// the deciding rule's pattern TEXT into the response. Ignore files were opened
// via os.Stat, which follows symlinks, so a repository-controlled .gitignore
// (or .graphignore, or a nested .gitignore) that is ITSELF a symlink pointing
// outside the repo let a crafted matching path exfiltrate up to ~200 bytes of
// an arbitrary local file — e.g. a sibling .env — into the disclosed rule
// text. The fix: gate every ignore-file load on Lstat, so a symlinked ignore
// file hits the SAME "not a regular file" hard failure a non-regular file
// (e.g. a directory named .gitignore) already produced — it is never opened,
// so its target's content is never read into a rule.
func TestSymlinkedGitignoreIsRejectedNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows CI")
	}
	t.Parallel()

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.env")
	if err := os.WriteFile(secretPath, []byte("SECRET_TOKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitignore := filepath.Join(repo, ".gitignore")
	if err := os.Symlink(secretPath, gitignore); err != nil {
		t.Fatal(err)
	}

	_, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err == nil {
		t.Fatal("a symlinked .gitignore must be rejected as not-a-regular-file, not silently followed")
	}
	if strings.Contains(err.Error(), "SECRET_TOKEN") {
		t.Fatalf("the rejection error must not itself carry the symlink target's content: %v", err)
	}
}

// TestSymlinkedNestedGitignoreIsSkippedNotFollowed pins the same fix at the
// nested .gitignore loader (enterCharged), which used its own os.Stat gate
// separate from loadOptional/loadRequired.
func TestSymlinkedNestedGitignoreIsSkippedNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows CI")
	}
	t.Parallel()

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.env")
	if err := os.WriteFile(secretPath, []byte("SECRET_TOKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(repo, "sub", ".gitignore")); err != nil {
		t.Fatal(err)
	}

	stack := &nestedIgnoreStack{repo: repo}
	if ok := stack.enterCharged(nil, "sub"); !ok {
		t.Fatal("a symlinked nested .gitignore must be treated as absent (continue descending), not as an unreadable-path stop")
	}
	for _, level := range stack.levels {
		for _, rule := range level.matcher.rules {
			if rule.pattern == "SECRET_TOKEN" {
				t.Fatalf("nested matcher picked up a rule sourced from the symlink target's content: %q", rule.pattern)
			}
		}
	}
}

// TestSymlinkedExplicitIgnoreFileIsRejected pins loadRequired's half of the
// fix: an operator-supplied --ignore-file that turns out to be a symlink is
// refused (as "not a regular file") the same way a directory would be, rather
// than transparently reading through it.
func TestSymlinkedExplicitIgnoreFileIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows CI")
	}
	t.Parallel()

	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.env")
	if err := os.WriteFile(secretPath, []byte("SECRET_TOKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	linked := filepath.Join(repo, "explicit-ignore")
	if err := os.Symlink(secretPath, linked); err != nil {
		t.Fatal(err)
	}

	var matcher ignoreMatcher
	err := matcher.loadRequired(linked, false, callerIgnoreOrigin("explicit-ignore"))
	if err == nil {
		t.Fatal("a symlinked --ignore-file must be rejected, not silently followed")
	}
}
