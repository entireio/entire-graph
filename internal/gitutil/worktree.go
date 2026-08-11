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
	paths, err := WorktreeDirtyPaths(ctx, repo)
	if err != nil {
		return false, err
	}
	return len(paths) == 0, nil
}

// WorktreeDirtyPaths returns every repo-relative path that makes the working
// tree differ from HEAD: modified, staged, deleted, renamed, unmerged, or
// untracked-but-not-ignored. An empty slice means the two views are equal, so
// WorktreeMatchesHEAD is exactly len(paths)==0.
//
// It exists so a caller can ask the sharper question "does anything that
// differs actually affect what I build?". WorktreeMatchesHEAD answers only
// "does anything differ at all", which disables a tree-keyed cache for a stray
// .DS_Store or a compiled binary — files no indexer reads. The caller owns that
// judgement because only it knows which paths it consumes; this function stays
// purely factual.
//
// Both sides of a rename are reported, since a rename both removes the old path
// and adds the new one.
//
// Ignored files are excluded (git omits them without --ignored), matching the
// provider's working-tree walk, which honours .gitignore.
func WorktreeDirtyPaths(ctx context.Context, repo string) ([]string, error) {
	// A repository with no commits has no HEAD tree to be equal to.
	if _, err := RevParse(ctx, repo, "HEAD"); err != nil {
		return nil, err
	}
	out, err := run(ctx, repo, "git",
		"--no-optional-locks", "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return nil, err
	}
	// -z emits NUL-terminated records "XY <path>". A rename or copy spends a
	// SECOND record on its source path, with no status prefix of its own, so it
	// must be consumed alongside the entry that introduced it rather than parsed
	// as a malformed status line.
	fields := strings.Split(out, "\x00")
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 || field[2] != ' ' {
			continue
		}
		status, path := field[:2], field[3:]
		if path != "" {
			paths = append(paths, path)
		}
		if strings.ContainsAny(status, "RC") && i+1 < len(fields) {
			i++
			if source := fields[i]; source != "" {
				paths = append(paths, source)
			}
		}
	}
	return paths, nil
}
