package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSafeStatThroughSymlinksFollowsAMultiHopChainOnTheSameVolume pins the
// ordinary case safeStatThroughSymlinks exists to keep working: a chain of
// several symlinks, every hop on the same volume, must still resolve to its
// terminal target -- the multi-hop volume guard must not turn into a blanket
// refusal of any chain longer than one link.
func TestSafeStatThroughSymlinksFollowsAMultiHopChainOnTheSameVolume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	real := filepath.Join(repo, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	hopA := filepath.Join(repo, "hop-a")
	hopB := filepath.Join(repo, "hop-b")
	hopC := filepath.Join(repo, "hop-c")
	// entry -> hop-c -> hop-b -> hop-a -> real (three intermediate hops).
	if err := os.Symlink(real, hopA); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(hopA, hopB); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(hopB, hopC); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	info, err := safeStatThroughSymlinks(repo, hopC)
	if err != nil {
		t.Fatalf("safeStatThroughSymlinks(repo, hopC) error = %v, want nil: a same-volume chain must resolve", err)
	}
	if !info.IsDir() {
		t.Error("safeStatThroughSymlinks(repo, hopC).IsDir() = false, want true: the chain terminates at a directory")
	}
}

// TestSafeStatThroughSymlinksRefusesAnImplausiblyDeepChain pins the hop
// ceiling: a chain longer than maxSymlinkChainHops is refused rather than
// followed forever. Each Lstat/Readlink call in the chain succeeds on its
// own regardless of how many hops precede it -- unlike a kernel-resolved
// os.Stat, there is no ELOOP to rely on -- so the function's own ceiling is
// the only thing bounding this loop's work.
func TestSafeStatThroughSymlinksRefusesAnImplausiblyDeepChain(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	real := filepath.Join(repo, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := real
	var entry string
	for i := range maxSymlinkChainHops + 5 {
		hop := filepath.Join(repo, "hop"+string(rune('a'+i%26))+string(rune('0'+i/26)))
		if err := os.Symlink(prev, hop); err != nil {
			t.Skipf("cannot create symlinks here: %v", err)
		}
		prev = hop
		entry = hop
	}

	if _, err := safeStatThroughSymlinks(repo, entry); err == nil {
		t.Error("safeStatThroughSymlinks did not refuse a chain deeper than maxSymlinkChainHops")
	}
}

// TestGitDirPointerTargetResolvesSymlinksBeforeAcceptingLexicalContainment
// reproduces the trail finding: a `.git` pointer naming a target that LOOKS
// lexically inside the repository (`admin-link`) used to be accepted on that
// string comparison alone. If admin-link is itself a symlink or junction to
// an external or network-backed directory, that let hasGitDirStructure probe
// commondir/objects/refs THROUGH the link — reading outside the repository —
// before any containment check that resolves symlinks ever ran.
func TestGitDirPointerTargetResolvesSymlinksBeforeAcceptingLexicalContainment(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "admin-link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	writeFile(t, repo, ".git", "gitdir: admin-link\n")

	got, ok, hidden := gitDirPointerTarget(repo, "")
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false):"+
			" a lexically in-repo target that is actually a symlink to OUTSIDE the repository must not"+
			" be reported as contained", got, ok, hidden)
	}
}

// TestGitDirPointerTargetFollowsASymlinkThatStaysInsideTheRepository is the
// widening direction: a symlink target is not rejected just for BEING a
// symlink — only for resolving outside the repository. One that resolves to
// another directory inside the same repository must still be reported,
// exactly as an ordinary (non-symlink) relative target would be.
func TestGitDirPointerTargetFollowsASymlinkThatStaysInsideTheRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "real-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "real-git"), filepath.Join(repo, "admin-link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	writeFile(t, repo, ".git", "gitdir: admin-link\n")

	got, ok, hidden := gitDirPointerTarget(repo, "")
	if !ok || hidden || got != "real-git" {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (\"real-git\", true, false):"+
			" a symlink target that resolves INSIDE the repository must still be followed", got, ok, hidden)
	}
}

// A nonexistent target has no git-directory structure to preserve, and its
// unchecked lexical spelling must not escape this resolver: a concurrent writer
// could replace a dangling in-repository link before the structure probe uses
// that spelling.
func TestGitDirPointerTargetRejectsANonexistentTargetWithoutReturningUncheckedSpelling(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, ".git", "gitdir: .repo-git\n")

	got, ok, hidden := gitDirPointerTarget(repo, "")
	if ok || hidden {
		t.Errorf("gitDirPointerTarget(repo, \"\") = (%q, ok=%v, hidden=%v), want (_, false, false)", got, ok, hidden)
	}
}

// TestGitDirExcluderPromotesRootStructureOnlyWhileEvidenceIsHidden reproduces
// the trail finding on promoteUnverifiedGitDirs: the repository root was
// never a promotion candidate at all, even though a READ pointer naming the
// root already narrows to root's own git-owned entries via recordTarget's
// "." case (recordGitDirRootEntries) rather than excluding the whole
// worktree. A hidden pointer that would have named the root — root itself
// holding objects/ and refs/ at its top level — fell through the one gap
// promotion left, and root's own config stayed indexable.
func TestGitDirExcluderPromotesRootStructureOnlyWhileEvidenceIsHidden(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(map[bool]string{false: "nothing hidden", true: "evidence hidden"}[hidden], func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, repo, "objects/fixture.txt", "objects\n")
			writeFile(t, repo, "refs/fixture.txt", "refs\n")
			writeFile(t, repo, "config", "[core]\n\tbare = true\n")
			writeFile(t, repo, "src/app.go", "package src\n")

			excluder := newGitDirExcluder(t.Context(), repo)
			excluder.observeListedPaths([]string{"config", "src/app.go"}, nil)
			if hidden {
				// observeListedPaths already ran the promotion and returned
				// early with nothing hidden, leaving promotedUnverified
				// unset, so this is the first and only pass.
				excluder.hiddenEvidence++
				excluder.promoteUnverifiedGitDirs()
			}
			if got := excluder.excluded("config"); got != hidden {
				t.Errorf("excluded(config) = %v, want %v", got, hidden)
			}
			if excluder.excluded("src/app.go") {
				t.Error("excluded(src/app.go) = true, want false: only the git-directory shape is given up, not the whole worktree")
			}
		})
	}
}
