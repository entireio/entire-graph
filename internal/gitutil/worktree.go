package gitutil

import (
	"context"
	"strings"
)

// WorktreeMatchesHEAD reports whether the working tree's indexable content is
// byte-identical to the committed HEAD tree.
//
// It is the precondition that lets a caller reuse a tree-keyed index cache for
// a working-tree query: when nothing is modified, staged, deleted or untracked,
// every file a working-tree walk can see is exactly the file HEAD records, so a
// snapshot built from one is a snapshot of the other.
//
// The check is one `git status --porcelain --untracked-files=all` and is
// deliberately conservative:
//
//   - it reports FALSE on any output at all — modified, staged, deleted,
//     renamed, unmerged, untracked, or a dirty submodule;
//   - `--untracked-files=all` is required, not optional: a working-tree walk
//     indexes untracked-but-not-ignored files, so "no tracked modifications"
//     alone would not make the two views equal;
//   - files git ignores are excluded from BOTH sides (the provider's
//     working-tree walk honours .gitignore), so an ignored build artifact
//     changing cannot invalidate the equality. If the ignore rules themselves
//     change so that a file stops being ignored, that file becomes untracked
//     and this check turns false, which is the safe direction;
//   - any error (not a git repository, git missing) reports FALSE with the
//     error, never a hopeful TRUE.
//
// `--no-optional-locks` keeps this read-only probe from refreshing the index
// and so from contending with a concurrent git command in the same repo.
func WorktreeMatchesHEAD(ctx context.Context, repo string) (bool, error) {
	// A repository with no commits has no HEAD tree to be equal to.
	if _, err := RevParse(ctx, repo, "HEAD"); err != nil {
		return false, err
	}
	out, err := run(ctx, repo, "git",
		"--no-optional-locks", "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return false, err
	}
	return strings.Trim(out, "\x00\n\r \t") == "", nil
}
