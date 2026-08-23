//go:build linux

package sem

import "testing"

func TestGitWorktreePreflightRefusesMountBeforeReadingChildren(t *testing.T) {
	anchor, resolvedRoot, err := newPathTraversalAnchor("/", "/")
	if err != nil {
		t.Fatal(err)
	}
	root, err := newSweepDirectoryRoot(resolvedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var budget gitWorktreePreflightBudget
	if !budget.admitDirectory("proc") {
		t.Fatal("failed to admit the synthetic preflight seed")
	}
	err = gitWorktreeSafeBeforeListingFromDirectories(
		t.Context(), root, anchor, &budget, []string{"proc"},
	)
	if err == nil {
		t.Fatal("worktree preflight crossed the /proc mount boundary")
	}
}
