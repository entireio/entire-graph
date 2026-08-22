//go:build windows

package sem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitDirPointerTargetRejectsAUNCTargetWithoutTouchingTheNetwork
// reproduces the trail finding on gitDirPointerTarget's out-of-repo fallback:
// a `.git` pointer naming a UNC share reached filepath.EvalSymlinks on that
// target directly, which resolves it by talking to the named server over
// SMB — attacker-controlled network access, with ambient credentials, for a
// target that is provably outside the repository before any resolution is
// needed at all (a UNC share is never on the same volume as a local
// repository root). The fix rejects a different volume BEFORE EvalSymlinks
// runs, so this must return immediately instead of hanging or dialing a
// nonexistent host.
func TestGitDirPointerTargetRejectsAUNCTargetWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, ".git", `gitdir: \\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var target string
	var ok, hidden bool
	go func() {
		target, ok, hidden = gitDirPointerTarget(repo, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirPointerTarget did not return within 5s: a UNC target must be rejected by volume" +
			" before EvalSymlinks attempts to resolve it over the network")
	}
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false): a UNC"+
			" target is never inside the repository", target, ok, hidden)
	}
}

// TestGitCommonDirRejectsAUNCTargetWithoutTouchingTheNetwork reproduces the
// sibling trail finding on gitCommonDir: unlike the `.git` pointer, this
// result feeds straight into hasObjectsAndRefs's os.Stat with no containment
// check downstream, so a `commondir` naming a UNC share made
// looksLikeGitDir/hasGitDirStructure open an SMB connection to a server the
// scanned repository's own committed content names. The fix rejects a
// different volume before that Stat ever runs, so this must return quickly
// with "not a git directory" rather than hanging or dialing a nonexistent
// host.
func TestGitCommonDirRejectsAUNCTargetWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "HEAD", "ref: refs/heads/main\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, "commondir", `\\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var target string
	var ok, looksLikeGit bool
	go func() {
		target, ok = gitCommonDir(repo)
		looksLikeGit = looksLikeGitDir(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitCommonDir/looksLikeGitDir did not return within 5s: a UNC commondir target must be" +
			" rejected by volume before os.Stat attempts to resolve it over the network")
	}
	if ok {
		t.Errorf("gitCommonDir(repo) = (%q, true), want (_, false): a UNC target is never on the repository's own volume", target)
	}
	if looksLikeGit {
		t.Error("looksLikeGitDir(repo) = true, want false: a refused commondir must not be treated as a git directory")
	}
}

// TestHasObjectsAndRefsRejectsAUNCSymlinkWithoutTouchingTheNetwork reproduces
// the trail finding on hasObjectsAndRefs: unlike the `.git` pointer and
// `commondir` files, an `objects` or `refs` ENTRY that is itself a symlink to
// a UNC share reached os.Stat directly with no volume check at all, and this
// probe runs over every directory the sweep observes — not only ones a
// `.git` pointer already named. A hang or a dial to the (nonexistent, per
// RFC 5737) host would be the missing guard, not a fluke.
func TestHasObjectsAndRefsRejectsAUNCSymlinkWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "refs/marker.txt", "refs\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	if err := os.Symlink(`\\203.0.113.1\share\repo\objects`, filepath.Join(repo, "objects")); err != nil {
		t.Fatalf("create objects symlink: %v", err)
	}

	done := make(chan struct{})
	var got bool
	go func() {
		got = hasObjectsAndRefs(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hasObjectsAndRefs did not return within 5s: a UNC symlink target must be rejected by" +
			" volume before os.Stat attempts to resolve it over the network")
	}
	if got {
		t.Error("hasObjectsAndRefs(repo) = true, want false: an objects/ symlink to a UNC share is never on the repository's own volume")
	}
}

// TestHasObjectsAndRefsAcceptsARelativeSymlinkOnTheSameVolume reproduces the
// trail finding on hasObjectsAndRefs' volume comparison: os.Readlink returns
// a RELATIVE target exactly as the link stores it, which carries no volume of
// its own (filepath.VolumeName is "" for every relative path), while repo is
// an absolute `C:\...` path. Comparing that raw target against repo's volume
// rejected every relative objects/refs symlink outright -- a layout git
// itself accepts -- misclassifying a real git directory as ordinary content
// and leaving its config indexable. The fix resolves a relative target
// against the entry's own parent (the same directory gitJoinRelative already
// resolves a `.git`/`commondir` pointer's own relative target against)
// before comparing volumes.
func TestHasObjectsAndRefsAcceptsARelativeSymlinkOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "real-objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "refs/marker.txt", "refs\n")
	if err := os.Symlink(`real-objects`, filepath.Join(repo, "objects")); err != nil {
		t.Fatalf("create objects symlink: %v", err)
	}

	if !hasObjectsAndRefs(repo) {
		t.Error("hasObjectsAndRefs(repo) = false, want true: a relative objects/ symlink resolved" +
			" against its own parent directory must be accepted, not rejected on an empty-vs-drive" +
			" volume mismatch")
	}
}

// TestGitDirPointerTargetRejectsAUNCGitfileSymlinkWithoutTouchingTheNetwork
// reproduces the trail finding on gitDirPointerTarget's `.git`-itself check:
// unlike the pointer's TEXT content (already guarded above), the `.git`
// FILE was read with a bare os.Stat, which follows a symlink as part of the
// same syscall that reports its result. A `.git` that is itself a symlink to
// a UNC path would dial SMB with ambient credentials before this function
// ever got a chance to look at, let alone reject, the target. The fix reads
// the link and checks its volume first, exactly as hasObjectsAndRefs already
// does for an objects/refs entry, so this must return quickly instead of
// hanging or dialing a nonexistent host.
func TestGitDirPointerTargetRejectsAUNCGitfileSymlinkWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	if err := os.Symlink(`\\203.0.113.1\share\repo\.git`, filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("create .git symlink: %v", err)
	}

	done := make(chan struct{})
	var target string
	var ok, hidden bool
	go func() {
		target, ok, hidden = gitDirPointerTarget(repo, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirPointerTarget did not return within 5s: a .git symlink to a UNC target must be" +
			" rejected by volume before os.Stat attempts to resolve it over the network")
	}
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false): a UNC"+
			" .git symlink target is never on the repository's own volume", target, ok, hidden)
	}
}

// TestGitDirPointerTargetFollowsAGitfileSymlinkOnTheSameVolume pins the other
// half: a `.git` symlink to an ordinary same-volume gitfile must still be
// followed and parsed, matching git's own read_gitfile_gently() fidelity
// (S_ISREG of the stat RESULT, not of the link itself) -- the volume guard
// must not turn into a blanket refusal of every symlinked `.git`.
func TestGitDirPointerTargetFollowsAGitfileSymlinkOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".real-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "elsewhere-gitfile", "gitdir: .real-git\n")
	if err := os.Symlink(filepath.Join(repo, "elsewhere-gitfile"), filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("create .git symlink: %v", err)
	}

	target, ok, hidden := gitDirPointerTarget(repo, "")
	if !ok || hidden || target != ".real-git" {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (\".real-git\", true, false)",
			target, ok, hidden)
	}
}

// TestGitInfoExcludePathRejectsAUNCCommonDirWithoutTouchingTheNetwork
// reproduces the sibling trail finding on gitInfoExcludePath: unlike
// gitCommonDir in provider.go, this independent commondir reader had no
// volume guard at all, so a linked worktree's `.git` pointer plus a
// `commondir` naming a UNC share made the CALLER's os.Stat(info/exclude)
// dial an SMB share — reachable through the same NUL-aware pointer parser
// readGitDirPointer already has to defend the `.git` file itself against.
func TestGitInfoExcludePathRejectsAUNCCommonDirWithoutTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".realgit")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, ".git", "gitdir: .realgit\n")
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737): reserved for documentation, so
	// this address is never routable and a hang here would be the missing
	// guard, not a fluke of a real host answering.
	writeFile(t, repo, ".realgit/commondir", `\\203.0.113.1\share\repo`+"\n")

	done := make(chan struct{})
	var got string
	go func() {
		got = gitInfoExcludePath(repo)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gitInfoExcludePath did not return within 5s: a UNC commondir target must be rejected by" +
			" volume before any caller's os.Stat attempts to resolve it over the network")
	}
	if got != "" {
		t.Errorf("gitInfoExcludePath(repo) = %q, want \"\": a UNC commondir target is never on the repository's own volume", got)
	}
}
