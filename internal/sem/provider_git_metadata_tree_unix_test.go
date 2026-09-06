//go:build !windows

package sem

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func linkedGitMetadataTreeFixture(t *testing.T) (repo, gitDir, common string) {
	t.Helper()
	repo = t.TempDir()
	gitDir = filepath.Join(repo, "admin", "worktrees", "wt")
	common = filepath.Join(repo, "admin")
	writeFile(t, repo, ".git", "gitdir: admin/worktrees/wt\n")
	writeFile(t, gitDir, "HEAD", strings.Repeat("a", 40)+"\n")
	writeFile(t, gitDir, "commondir", "../..\n")
	for _, dir := range []string{filepath.Join(common, "objects"), filepath.Join(common, "refs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repo, gitDir, common
}

func rootGitMetadataTreeFixture(t *testing.T) (repo, gitDir string) {
	t.Helper()
	repo = t.TempDir()
	gitDir = filepath.Join(repo, ".git")
	for _, dir := range []string{filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, gitDir, "HEAD", strings.Repeat("a", 40)+"\n")
	return repo, gitDir
}

func writeGitMetadataFIFO(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireUnsafeGitMetadataPromptly(t *testing.T, repo, description string) {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- gitMetadataSafeForSubprocess(repo) }()
	select {
	case safe := <-done:
		if safe {
			t.Fatalf("%s passed the pre-subprocess metadata guard", description)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("metadata guard blocked while inspecting %s", description)
	}
}

func TestGitMetadataTreeGuardRejectsBlockingCommonShallow(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(common, "shallow"))
	requireUnsafeGitMetadataPromptly(t, repo, "common shallow FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingCommonGrafts(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(common, "info", "grafts"))
	requireUnsafeGitMetadataPromptly(t, repo, "common info/grafts FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingHEADLooseRefRedirect(t *testing.T) {
	repo, gitDir, common := linkedGitMetadataTreeFixture(t)
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	target := filepath.Join(repo, "ref-target")
	writeGitMetadataFIFO(t, filepath.Join(target, "main"))
	if err := os.Symlink(target, filepath.Join(common, "refs", "heads")); err != nil {
		t.Fatal(err)
	}
	requireUnsafeGitMetadataPromptly(t, repo, "HEAD loose-ref path redirected to a FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingNonHEADLooseRef(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(common, "refs", "tags", "release"))
	requireUnsafeGitMetadataPromptly(t, repo, "non-HEAD loose-ref FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingPerWorktreeReflog(t *testing.T) {
	repo, gitDir, _ := linkedGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(gitDir, "logs", "HEAD"))
	requireUnsafeGitMetadataPromptly(t, repo, "per-worktree logs/HEAD FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingPackDirectoryRedirect(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	target := filepath.Join(repo, "pack-target")
	pack := "pack-" + strings.Repeat("b", 40) + ".idx"
	writeGitMetadataFIFO(t, filepath.Join(target, pack))
	if err := os.Symlink(target, filepath.Join(common, "objects", "pack")); err != nil {
		t.Fatal(err)
	}
	requireUnsafeGitMetadataPromptly(t, repo, "objects/pack redirect containing a FIFO")
}

func TestGitMetadataTreeGuardRejectsBlockingSharedIndex(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(gitDir, "sharedindex."+strings.Repeat("c", 40)))
	requireUnsafeGitMetadataPromptly(t, repo, "shared index FIFO")
}

func TestGitMetadataTreeGuardAllowsSafeLocalLooseRefRedirect(t *testing.T) {
	repo, gitDir, common := linkedGitMetadataTreeFixture(t)
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	target := filepath.Join(repo, "safe-ref-target")
	writeFile(t, target, "main", strings.Repeat("d", 40)+"\n")
	if err := os.Symlink(target, filepath.Join(common, "refs", "heads")); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe same-filesystem loose-ref redirect was refused")
	}
}

func TestGitMetadataTreeGuardAllowsLinkedWorktreeLogicalHEADSymlink(t *testing.T) {
	repo, gitDir, common := linkedGitMetadataTreeFixture(t)
	if err := os.Remove(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(common, "refs", "heads"), "main", strings.Repeat("f", 40)+"\n")
	if err := os.Symlink("refs/heads/main", filepath.Join(gitDir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("linked-worktree logical HEAD symlink into common refs was refused")
	}
}

func TestGitMetadataTreeGuardAllowsCollapsedGitDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, ".git", "gitdir: .\n")
	writeFile(t, repo, "HEAD", strings.Repeat("e", 40)+"\n")
	for _, name := range []string{"objects", "refs"} {
		if err := os.Mkdir(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, repo, "src/app.go", "package src\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe gitdir collapsed onto the worktree root was refused")
	}
}

func TestGitMetadataTreeGuardAllowsGitDirectoryContainingWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".git", "gitdir: ..\n")
	writeFile(t, root, "HEAD", strings.Repeat("1", 40)+"\n")
	for _, name := range []string{"objects", "refs"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, repo, "src/app.go", "package src\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe gitdir containing the worktree was refused")
	}
}

func TestGitMetadataTreeGuardAllowsDisabledFSMonitorSocket(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	listener, err := net.Listen("unix", filepath.Join(gitDir, "fsmonitor--daemon.ipc"))
	if err != nil {
		t.Skipf("create Unix fsmonitor socket: %v", err)
	}
	defer listener.Close()
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("Git's fsmonitor daemon socket was refused even though provider subprocesses disable core.fsmonitor")
	}
}

func TestGitMetadataTreeGuardRejectsOtherSocket(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	listener, err := net.Listen("unix", filepath.Join(gitDir, "attacker.sock"))
	if err != nil {
		t.Skipf("create Unix metadata socket: %v", err)
	}
	defer listener.Close()
	requireUnsafeGitMetadataPromptly(t, repo, "non-fsmonitor Unix socket")
}

func TestGitMetadataTreeGuardRejectsFSMonitorNamedSocketUnderRefs(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	socket := filepath.Join(gitDir, "refs", "heads", "fsmonitor--daemon.ipc")
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("create Unix metadata socket: %v", err)
	}
	defer listener.Close()
	requireUnsafeGitMetadataPromptly(t, repo, "fsmonitor-named socket below refs")
}

func TestGitMetadataTreeGuardAllowsOtherLinkedWorktreeFSMonitorSocket(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	other := filepath.Join(repo, "other-worktree-admin")
	writeFile(t, other, "HEAD", strings.Repeat("9", 40)+"\n")
	if err := os.MkdirAll(filepath.Join(common, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(common, "worktrees", "other")); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(other, "fsmonitor--daemon.ipc"))
	if err != nil {
		t.Skipf("create linked-worktree Unix fsmonitor socket: %v", err)
	}
	defer listener.Close()
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("another linked worktree's disabled fsmonitor daemon socket was refused")
	}
}

func TestGitMetadataTreeGuardValidatesRedirectedLinkedWorktreeReftable(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	other := filepath.Join(repo, "other-worktree-admin")
	writeFile(t, filepath.Join(other, "reftable"), "tables.list", "../outside.ref\n")
	if err := os.MkdirAll(filepath.Join(common, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(common, "worktrees", "other")); err != nil {
		t.Fatal(err)
	}
	requireUnsafeGitMetadataPromptly(t, repo, "redirected linked-worktree reftable manifest")
}

func TestGitMetadataTreeGuardValidatesCaseFoldedWorktreesReftable(t *testing.T) {
	repo := t.TempDir()
	common := filepath.Join(repo, "admin")
	gitDir := filepath.Join(common, "WORKTREES", "wt")
	writeFile(t, repo, ".git", "gitdir: admin/worktrees/wt\n")
	writeFile(t, gitDir, "HEAD", strings.Repeat("7", 40)+"\n")
	writeFile(t, gitDir, "commondir", "../..\n")
	for _, dir := range []string{filepath.Join(common, "objects"), filepath.Join(common, "refs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(common, "WORKTREES", "other", "reftable"), "tables.list", "../outside.ref\n")
	if !foldsCase(common, "WORKTREES") {
		t.Skip("filesystem is case-sensitive: WORKTREES is not Git's worktrees directory")
	}
	requireUnsafeGitMetadataPromptly(t, repo, "case-folded linked-worktree reftable manifest")
}

func TestGitMetadataTreeGuardIgnoresUnrelatedUppercaseReftableDirectory(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	writeFile(t, filepath.Join(gitDir, "REFTABLE"), "tables.list", "../ordinary-source\n")
	if foldsCase(gitDir, "REFTABLE") {
		t.Skip("filesystem folds REFTABLE onto Git's real reftable directory")
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("case-sensitive filesystem treated unrelated REFTABLE directory as Git's reftable store")
	}
}

func TestGitMetadataTreeGuardRejectsFinalLooseRefRedirectToFIFO(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	target := filepath.Join(repo, "fifo-target")
	writeGitMetadataFIFO(t, target)
	link := filepath.Join(common, "refs", "heads", "main")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	requireUnsafeGitMetadataPromptly(t, repo, "loose ref whose final redirect target is a FIFO")
}

func TestGitMetadataTreeGuardBoundsLocalRedirectCycle(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	target := filepath.Join(repo, "safe-cycle")
	writeFile(t, target, "main", strings.Repeat("8", 40)+"\n")
	if err := os.Symlink(filepath.Join(common, "refs"), filepath.Join(target, "back")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(common, "refs", "heads")); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe local directory redirect cycle was refused instead of being deduplicated")
	}
}

func TestGitMetadataTreeValidationExactBounds(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "entry")
	validation := gitMetadataTreeValidation{
		resolver:  &sameVolumePathResolver{baseResolved: base},
		entries:   maxGitMetadataTreeEntries - 1,
		pathBytes: maxGitMetadataTreePathBytes - len("entry") - 1,
	}
	if !validation.admit(path) {
		t.Fatal("entry exactly filling both metadata-tree bounds was refused")
	}
	if validation.admit(path) {
		t.Fatal("entry above the metadata-tree entry bound was admitted")
	}

	validation = gitMetadataTreeValidation{
		resolver:  &sameVolumePathResolver{baseResolved: base},
		pathBytes: maxGitMetadataTreePathBytes - len("entry"),
	}
	if validation.admit(path) {
		t.Fatal("entry one byte above the metadata-tree path bound was admitted")
	}
}

func TestGitMetadataValidationSetupAlwaysRefreshesAndStaysRepoBound(t *testing.T) {
	safeRepo, safeGitDir := rootGitMetadataTreeFixture(t)
	oldContext, err := WithGitMetadataValidationForSetup(context.Background(), safeRepo)
	if err != nil {
		t.Fatal(err)
	}
	writeGitMetadataFIFO(t, filepath.Join(safeGitDir, "shallow"))
	refreshed, safe := newGitMetadataValidation(oldContext, safeRepo)
	if safe || gitMetadataSafeForSubprocessContext(refreshed, safeRepo) {
		t.Fatal("fresh setup validation reused a stale safe receipt after metadata became unsafe")
	}

	otherRepo, otherGitDir := rootGitMetadataTreeFixture(t)
	writeGitMetadataFIFO(t, filepath.Join(otherGitDir, "shallow"))
	if gitMetadataSafeForSubprocessContext(oldContext, otherRepo) {
		t.Fatal("repository-bound validation receipt was reused for a different unsafe repository")
	}
}

func TestGitMetadataTreeGuardRejectsEscapingReftableManifest(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	writeFile(t, filepath.Join(common, "reftable"), "tables.list", "../outside.ref\n")
	requireUnsafeGitMetadataPromptly(t, repo, "escaping reftable manifest")
}

func TestGitMetadataTreeGuardAllowsLocalReftableManifest(t *testing.T) {
	repo, _, common := linkedGitMetadataTreeFixture(t)
	const table = "0x000000000001-0x000000000002-deadbeef.ref"
	writeFile(t, filepath.Join(common, "reftable"), "tables.list", table+"\n")
	writeFile(t, filepath.Join(common, "reftable"), table, "table bytes\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("safe local reftable manifest was refused")
	}
}

func TestGitMetadataTreeGuardIgnoresUnrelatedNestedReftableDirectory(t *testing.T) {
	repo, gitDir := rootGitMetadataTreeFixture(t)
	writeFile(t, filepath.Join(gitDir, "hooks", "reftable"), "tables.list", "../hook-state\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("nested non-store directory named reftable was parsed as a Git reftable stack")
	}
}

func TestGitMetadataTreeGuardIgnoresAlternateBeyondGitDepthLimit(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "checkout")
	gitDir := filepath.Join(repo, ".git")
	primaryObjects := filepath.Join(gitDir, "objects")
	for _, dir := range []string{primaryObjects, filepath.Join(gitDir, "refs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, gitDir, "HEAD", strings.Repeat("2", 40)+"\n")

	objectStores := []string{primaryObjects}
	for index := 1; index <= maxGitAlternateDepth+2; index++ {
		store := filepath.Join(root, "objects-"+strconv.Itoa(index))
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		objectStores = append(objectStores, store)
	}
	for index := 0; index < len(objectStores)-1; index++ {
		writeFile(t, objectStores[index], filepath.Join("info", "alternates"), filepath.ToSlash(objectStores[index+1])+"\n")
	}
	writeGitMetadataFIFO(t, filepath.Join(objectStores[len(objectStores)-1], "trap"))

	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("metadata guard followed and swept an alternate beyond Git's recursion limit")
	}
}
