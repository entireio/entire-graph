package agentsetup

import (
	"os"
	"path/filepath"
)

// OuterRepo reports the nearest ancestor of root that is itself a git
// repository, or "" when root is the outermost one.
//
// Context resolves the guide's destination by walking up from the working
// directory to the NEAREST .git, which is right until a repository is nested
// inside another. The documented build-from-source flow produces exactly that
// shape: `git clone entire-brain` inside a project, `cd entire-brain`, install
// — and leaves the shell inside the clone, so the next `setup` writes
// AGENTS.md, CLAUDE.md and the guide into the clone instead of the project.
// The clone is usually untracked by the outer repository, so the files are
// invisible to the team the guide was meant for and nothing reports an error.
//
// Returning the outer root lets the caller name both paths and let the person
// decide, rather than silently picking the inner one.
func OuterRepo(root string) string {
	if root == "" {
		return ""
	}
	// root must be a repository ITSELF, or the caller's message is false.
	//
	// This walks UP for an ancestor .git and never checked the starting point,
	// so a plain directory inside a repository -- a monorepo subdirectory, or
	// anything under a dotfiles-tracked home -- came back with that ancestor
	// and the caller announced "<root> is a git repository inside <outer>"
	// about a path that is not a repository at all. The remedy it suggests
	// then addresses nothing.
	//
	// Checking here rather than at the call site keeps the guarantee with the
	// function that makes the claim: OuterRepo returns an outer repository
	// only when there is genuinely an inner one.
	if !isRepoRoot(root) {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	for p := filepath.Dir(abs); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return p
		} else if !os.IsNotExist(err) {
			return ""
		}
		if parent := filepath.Dir(p); parent == p {
			return ""
		}
	}
}

// isRepoRoot reports whether this path holds its own .git, by Lstat so a
// symlinked .git is seen rather than followed -- matching how the walk above
// tests each ancestor.
func isRepoRoot(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}
