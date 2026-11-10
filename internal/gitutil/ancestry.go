package gitutil

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CommitTree is one commit and the root tree it records.
type CommitTree struct {
	Commit string
	Tree   string
}

// FirstParentCommitTrees lists commit and then up to count-1 of its
// first-parent ancestors, newest first, each with its root tree. Fewer entries
// come back when history ends first (a root commit or a shallow boundary); an
// ancestor the history cannot reach is simply not listed.
func FirstParentCommitTrees(ctx context.Context, repo, commit string, count int) ([]CommitTree, error) {
	if !isObjectName(commit) {
		return nil, fmt.Errorf("first-parent walk needs a full commit object name, got %q", commit)
	}
	if count <= 0 {
		return nil, nil
	}
	out, err := run(ctx, repo, "git", "log", "--first-parent", "--no-show-signature", "--no-notes",
		"--no-decorate", "--format=%H%x00%T", "-n", strconv.Itoa(count), "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	var chain []CommitTree
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		hash, tree, ok := strings.Cut(line, "\x00")
		if !ok || !isObjectName(hash) || !isObjectName(tree) {
			return nil, fmt.Errorf("git log returned malformed commit/tree metadata %q", line)
		}
		chain = append(chain, CommitTree{Commit: hash, Tree: tree})
	}
	if len(chain) == 0 || chain[0].Commit != commit {
		return nil, errors.New("git log did not start the first-parent walk at the requested commit")
	}
	if len(chain) > count {
		return nil, fmt.Errorf("git log returned %d commits, more than the %d requested", len(chain), count)
	}
	return chain, nil
}

// TreeChangedPaths lists every path whose entry differs between two trees,
// sorted and deduplicated, relative to prefix (the checkout's --show-prefix;
// "" at the top level) and limited to paths under it.
//
// The list is complete or the call fails; it is never a sample. Rename
// detection is off, so a rename is reported as both of its paths (the deleted
// source and the added destination) rather than collapsed into one pair whose
// detection depends on similarity heuristics, and a deletion is its old path.
// Comparing two trees by name reads tree objects only, never blob content, and
// the guarded environment forbids lazy fetching, so a partial clone missing a
// tree fails here instead of fetching it or answering with a shorter list.
// Gitlinks (submodule entries) are listed like any other changed entry.
func TreeChangedPaths(ctx context.Context, repo, fromTree, toTree, prefix string) ([]string, error) {
	if !isObjectName(fromTree) || !isObjectName(toTree) {
		return nil, fmt.Errorf("tree comparison needs full tree object names, got %q and %q", fromTree, toTree)
	}
	args := []string{"diff-tree", "-r", "-z", "--name-only", "--no-renames", "--ignore-submodules=none"}
	if prefix != "" {
		if strings.HasPrefix(prefix, "-") || !strings.HasSuffix(prefix, "/") || strings.ContainsRune(prefix, '\x00') {
			return nil, fmt.Errorf("invalid checkout prefix %q", prefix)
		}
		args = append(args, "--relative="+prefix)
	} else {
		args = append(args, "--no-relative")
	}
	args = append(args, "--end-of-options", fromTree, toTree)
	out, err := run(ctx, repo, "git", args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range strings.Split(out, "\x00") {
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	return unique, nil
}

// isObjectName accepts a full SHA-1 or SHA-256 object name in lowercase hex,
// which is all Git prints for %H/%T. Anything else (an abbreviation, a ref
// name, an option-shaped string) is rejected before it reaches argv.
func isObjectName(name string) bool {
	if len(name) != 40 && len(name) != 64 {
		return false
	}
	for _, r := range name {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
