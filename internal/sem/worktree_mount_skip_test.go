package sem

import (
	"slices"
	"strings"
	"testing"
)

// The filesystem fallback cannot see mounts itself: it prunes exactly the mount
// points the preflight handed it, before reading anything inside them. A
// `.git` pointer inside a pruned mount point is never read, so the git
// directory it names elsewhere in the tree must still be excluded through the
// same fail-closed promotion an unreadable directory triggers.
func TestWalkWorktreeFilesPrunesSkippedMountPointsUnread(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "keep.go", "package keep\nfunc Kept() {}\n")
	writeFile(t, repo, "mnt/inside.go", "package mnt\nfunc BehindTheMount() {}\n")
	writeFile(t, repo, "mnt/.git", "gitdir: ../.dep-git\n")
	// A HEAD-less git directory: only the pointer, or the promotion that
	// stands in for an unread pointer, identifies it.
	writeFile(t, repo, ".dep-git/config", "[remote \"origin\"]\n\turl = https://token@example.invalid/repo\n")
	writeFile(t, repo, ".dep-git/objects/.keep", "")
	writeFile(t, repo, ".dep-git/refs/.keep", "")

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	warnings, err := visitWalkWorktreeFiles(t.Context(), repo, ignores, nil, nil, false, []string{"mnt"}, func(rel string) bool {
		paths = append(paths, rel)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsWorktreePath(paths, "keep.go") {
		t.Fatalf("paths = %q, want keep.go beside the skipped mount point", paths)
	}
	for _, rel := range paths {
		if rel == "mnt" || strings.HasPrefix(rel, "mnt/") {
			t.Fatalf("paths = %q: the walk entered the skipped mount point", paths)
		}
		if strings.HasPrefix(rel, ".dep-git/") {
			t.Fatalf("paths = %q: a git directory a skipped mount point could name was indexed", paths)
		}
	}
	if !hasProviderWarningDetail(warnings, "W_WALK_MOUNT_SKIPPED", "mnt") {
		t.Fatalf("warnings = %+v, want W_WALK_MOUNT_SKIPPED naming mnt", warnings)
	}
}

func hasProviderWarningDetail(warnings []ProviderWarning, code, detail string) bool {
	for _, warning := range warnings {
		if warning.Code == code && strings.Contains(warning.Detail, detail) {
			return true
		}
	}
	return false
}

// Readers are asked for paths by name as well as for listed ones (import
// resolution, a whole-tree Git grep), so the source refuses every path at or
// below a skipped mount point, including aliases a folding filesystem would
// resolve into it.
func TestWorktreeSourceRefusesReadsBelowSkippedMountPoints(t *testing.T) {
	t.Parallel()
	var asked []string
	opened := refuseWorktreeMountReads(openedSource{
		read: func(rel string) (string, bool) {
			asked = append(asked, rel)
			return "content", true
		},
		readPrefix: func(rel string, _ int) (string, bool) {
			asked = append(asked, rel)
			return "content", true
		},
	}, []string{".claude/.cc-writes", "mnt"})
	for _, rel := range []string{
		"mnt", "mnt/inside.go", "MNT/inside.go", "a/../mnt/inside.go", "./mnt/inside.go",
		".claude/.cc-writes/x.go", ".Claude/.CC-writes/x.go",
	} {
		if _, ok := opened.read(rel); ok {
			t.Errorf("read(%q) succeeded below a skipped mount point", rel)
		}
		if _, ok := opened.readPrefix(rel, 16); ok {
			t.Errorf("readPrefix(%q) succeeded below a skipped mount point", rel)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("underlying reader was asked for %q", asked)
	}
	for _, rel := range []string{"keep.go", "mnt2/x.go", "mntx", ".claude/settings.json", "a/mnt/x.go"} {
		if _, ok := opened.read(rel); !ok {
			t.Errorf("read(%q) refused outside every skipped mount point", rel)
		}
		if _, ok := opened.readPrefix(rel, 16); !ok {
			t.Errorf("readPrefix(%q) refused outside every skipped mount point", rel)
		}
	}
	if len(opened.worktreeMounts) != 2 {
		t.Fatalf("worktreeMounts = %q, want both skipped mount points kept for search", opened.worktreeMounts)
	}

	unchanged := refuseWorktreeMountReads(openedSource{}, nil)
	if unchanged.read != nil || unchanged.readPrefix != nil || unchanged.worktreeMounts != nil {
		t.Fatal("a source with no skipped mount points was changed")
	}
}

// An ignored directory is pruned without a walk frame of its own, but the
// repo-ignore disclosure still walks it to count what the rule removed. That
// accounting walk must stop at a skipped mount point inside it too.
func TestPrunedExclusionAccountingStopsAtSkippedMountPoints(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, graphIgnoreFileName, "hidden/\n")
	writeFile(t, repo, "keep.go", "package keep\nfunc Kept() {}\n")
	writeFile(t, repo, "hidden/counted.go", "package hidden\nfunc Counted() {}\n")
	writeFile(t, repo, "hidden/mnt/.gitignore", "!*.go\n")
	writeFile(t, repo, "hidden/mnt/behind.go", "package mnt\nfunc BehindTheMount() {}\n")

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &repoIgnoreLedger{}
	if _, err := visitWalkWorktreeFiles(t.Context(), repo, ignores, func(string) bool { return false }, ledger, false,
		[]string{"hidden/mnt"}, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	report := ledger.report()
	if report == nil {
		t.Fatal("the pruned tree was not disclosed at all")
	}
	for _, exclusion := range report.Sample {
		if strings.HasPrefix(exclusion.Path, "hidden/mnt/") {
			t.Fatalf("sample = %+v: the accounting walk entered the skipped mount point", report.Sample)
		}
	}
	if report.Files != 1 || !report.CountIncomplete {
		t.Fatalf("files = %d, count incomplete = %v, want 1 counted file and a lower-bound count", report.Files, report.CountIncomplete)
	}
	if !slices.Contains(report.Unreadable, "hidden/mnt") {
		t.Fatalf("unreadable = %q, want the skipped mount point named", report.Unreadable)
	}
}

// The pointer sweep over a pruned tree reads each child directory's `.git`
// before it opens the child, so a skipped mount point inside an ignored
// directory has to be dropped there, not left to the guarded open.
func TestPrunedTreePointerSweepDoesNotObserveSkippedMountPoints(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "hidden/plain/keep.txt", "")
	writeFile(t, repo, "hidden/mnt/.git", "gitdir: ../../.dep-git\n")
	gitDirs := newGitDirExcluder(t.Context(), repo)
	gitDirs.skippedMounts = map[string]struct{}{"hidden/mnt": {}}
	gitDirs.observePrunedSubtree("hidden")
	if slices.Contains(gitDirs.observedDirs, "hidden/mnt") {
		t.Fatalf("observed = %q: the sweep read inside a skipped mount point", gitDirs.observedDirs)
	}
	if !slices.Contains(gitDirs.observedDirs, "hidden/plain") {
		t.Fatalf("observed = %q, want the ordinary sibling observed", gitDirs.observedDirs)
	}
	if gitDirs.hiddenEvidence == 0 || !slices.Contains(gitDirs.skippedMountDirs, "hidden/mnt") {
		t.Fatalf("hidden evidence = %d, skipped = %q: the unread mount point must count and be disclosed",
			gitDirs.hiddenEvidence, gitDirs.skippedMountDirs)
	}
}

// When the root turns out to be the git directory, its git-owned entries are
// excluded by name after one lookup each, except a skipped mount point, which
// is never looked up: an Lstat of one can activate an NFS, FUSE or autofs
// filesystem. Its name is excluded without that lookup, case-folded like every
// mount rejection. Nothing is on disk at `hooks` here, so only the name match
// can exclude it; a lookup would have found nothing and left it out.
func TestGitDirRootEntriesAreNotLookedUpAtSkippedMountPoints(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "config", "[core]\n")
	gitDirs := newGitDirExcluder(t.Context(), repo)
	gitDirs.inSkippedMount = skippedMountMatcher([]string{"HOOKS"})
	gitDirs.addTarget(gitDirRootTarget)
	if !gitDirs.excluded("hooks/pre-commit") {
		t.Fatal("hooks/pre-commit is indexable: a skipped mount point at a git-owned root name was not excluded by name")
	}
	if !gitDirs.excluded("config") {
		t.Fatal("config is indexable: an existing git-owned root entry was not excluded")
	}
	if gitDirs.excluded("logs/HEAD") || gitDirs.excluded("src/app.go") {
		t.Fatal("an absent git-owned name or ordinary source was excluded")
	}
}

// The excluder observes the root as it is built, and a root `.git` pointer
// naming the root itself takes the root for a git directory right there. The
// skipped mount points are in place before that first observation, so its
// probes of git-owned names skip them as well.
func TestGitDirRootObservationSkipsMountPointsFromTheStart(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, ".git", "gitdir: .\n")
	writeFile(t, repo, "objects/.keep", "")
	writeFile(t, repo, "refs/.keep", "")
	gitDirs := newGitDirExcluderSkippingMounts(t.Context(), repo, []string{"hooks"})
	if !gitDirs.gitDirRoot {
		t.Fatal("the root pointer did not take the root for a git directory")
	}
	if !gitDirs.excluded("hooks/pre-commit") {
		t.Fatal("hooks/pre-commit is indexable: the root observation ran before the skipped mount points were installed")
	}
	if _, skipped := gitDirs.skippedMounts["hooks"]; !skipped {
		t.Fatal("the walk's pruning set is missing the skipped mount point")
	}
}
