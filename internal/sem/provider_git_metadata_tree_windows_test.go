//go:build windows

package sem

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const windowsMetadataTestUNC = `\\203.0.113.1\share\git-metadata`

// windowsRawMetadataSymlinkOrSkip calls CreateSymbolicLinkW directly so a
// nonexistent UNC target is recorded in the reparse point without first being
// probed over SMB. os.Symlink may inspect the target to infer its type when the
// target does not exist, which would make setup perform the network access the
// test is meant to prevent.
func windowsRawMetadataSymlinkOrSkip(t *testing.T, target, link string, directory bool) {
	t.Helper()
	linkUTF16, err := syscall.UTF16PtrFromString(link)
	if err != nil {
		t.Fatalf("encode symlink path %q: %v", link, err)
	}
	targetUTF16, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		t.Fatalf("encode symlink target %q: %v", target, err)
	}

	flags := uint32(0)
	if directory {
		flags = syscall.SYMBOLIC_LINK_FLAG_DIRECTORY
	}
	// Windows 10 developer mode permits unprivileged symlink creation with
	// flag 0x2. Older systems reject that flag, so retry with the traditional
	// privileged call before deciding the test must be skipped.
	const allowUnprivilegedCreate = uint32(0x2)
	createErr := syscall.CreateSymbolicLink(linkUTF16, targetUTF16, flags|allowUnprivilegedCreate)
	if errors.Is(createErr, syscall.Errno(87)) || errors.Is(createErr, syscall.ERROR_PRIVILEGE_NOT_HELD) { // ERROR_INVALID_PARAMETER
		createErr = syscall.CreateSymbolicLink(linkUTF16, targetUTF16, flags)
	}
	if createErr == nil {
		return
	}
	if errors.Is(createErr, syscall.ERROR_PRIVILEGE_NOT_HELD) {
		t.Skipf("symlink creation requires unavailable Windows privilege: %v", createErr)
	}
	t.Fatalf("create raw Windows symlink %q -> %q: %v", link, target, createErr)
}

func newWindowsGitMetadataTreeFixture(t *testing.T) (repo, gitDir string) {
	t.Helper()
	repo = t.TempDir()
	gitDir = filepath.Join(repo, ".git")
	for _, dir := range []string{
		filepath.Join(gitDir, "objects"),
		filepath.Join(gitDir, "refs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe metadata-tree fixture was refused before the redirect was installed")
	}
	return repo, gitDir
}

func assertWindowsGitMetadataRejectedPromptly(t *testing.T, repo, label string) {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- gitMetadataSafeForSubprocess(repo) }()
	select {
	case safe := <-done:
		if safe {
			t.Fatalf("UNC redirect at %s passed the pre-subprocess metadata guard", label)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("UNC redirect at %s was followed instead of being rejected before network access", label)
	}
}

func TestGitMetadataTreeRejectsCommonShallowUNCRedirectWithoutDialing(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	common := filepath.Join(root, "common.git")
	gitDir := filepath.Join(common, "worktrees", "checkout")
	for _, dir := range []string{
		repo,
		filepath.Join(common, "objects"),
		filepath.Join(common, "refs"),
		gitDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, repo, ".git", "gitdir: ../common.git/worktrees/checkout\n")
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	writeFile(t, gitDir, "commondir", "../..\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe linked-worktree fixture was refused before the shallow redirect was installed")
	}

	windowsRawMetadataSymlinkOrSkip(t, windowsMetadataTestUNC+`\shallow`, filepath.Join(common, "shallow"), false)
	assertWindowsGitMetadataRejectedPromptly(t, repo, "common/shallow")
}

func TestGitMetadataTreeRejectsNestedUNCRedirectsWithoutDialing(t *testing.T) {
	const sharedIndexName = "sharedindex.0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name      string
		relative  string
		directory bool
	}{
		{name: "refs heads directory", relative: filepath.Join("refs", "heads"), directory: true},
		{name: "objects pack directory", relative: filepath.Join("objects", "pack"), directory: true},
		{name: "HEAD reflog", relative: filepath.Join("logs", "HEAD")},
		{name: "split index", relative: sharedIndexName},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repo, gitDir := newWindowsGitMetadataTreeFixture(t)
			link := filepath.Join(gitDir, testCase.relative)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			target := windowsMetadataTestUNC + `\` + filepath.Base(testCase.relative)
			windowsRawMetadataSymlinkOrSkip(t, target, link, testCase.directory)
			assertWindowsGitMetadataRejectedPromptly(t, repo, filepath.ToSlash(testCase.relative))
		})
	}
}

func TestGitMetadataTreeRejectsMixedCaseEscapingReftableManifest(t *testing.T) {
	repo, gitDir := newWindowsGitMetadataTreeFixture(t)
	writeFile(t, filepath.Join(gitDir, "REFTABLE"), "tables.list", "../outside.ref\n")
	assertWindowsGitMetadataRejectedPromptly(t, repo, "mixed-case reftable/tables.list")
}

func TestGitMetadataTreeRejectsTerminalAlternateUNCWithoutDialing(t *testing.T) {
	repo, gitDir := newWindowsGitMetadataTreeFixture(t)
	stores := make([]string, maxGitAlternateDepth+1)
	for index := range stores {
		stores[index] = filepath.Join(repo, "alternate", string(rune('a'+index)), "objects")
		if err := os.MkdirAll(filepath.Join(stores[index], "info"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(gitDir, "objects", "info"), "alternates", filepath.ToSlash(stores[0])+"\n")
	for index := 0; index+1 < len(stores); index++ {
		writeFile(t, filepath.Join(stores[index], "info"), "alternates", filepath.ToSlash(stores[index+1])+"\n")
	}
	writeFile(t, filepath.Join(stores[len(stores)-1], "info"), "alternates", windowsMetadataTestUNC+`\terminal`+"\n")
	assertWindowsGitMetadataRejectedPromptly(t, repo, "terminal-depth objects/info/alternates")
}
