//go:build windows

package sem

import (
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
