package gitutil

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// GrepTreePathsInFiles is GrepTreePaths restricted to the supplied repository-
// relative file names (relative to repo, even when repo is a subdirectory).
// Names are literal, not Git pathspecs. A nil or empty
// inventory searches nothing, rather than falling back to a whole-tree scan.
func GrepTreePathsInFiles(ctx context.Context, repo, treeish string, patterns, files []string) ([]string, error) {
	return grepTreePathsInFiles(ctx, repo, treeish, patterns, files, true, grepPathBatchBytes())
}

// GrepTreePathsIncludingBinaryInFiles is GrepTreePathsIncludingBinary restricted
// to literal file names, with the same empty-inventory contract as GrepTreePathsInFiles.
func GrepTreePathsIncludingBinaryInFiles(ctx context.Context, repo, treeish string, patterns, files []string) ([]string, error) {
	return grepTreePathsInFiles(ctx, repo, treeish, patterns, files, false, grepPathBatchBytes())
}

func grepPathBatchBytes() int {
	// Leave room for patterns, environment and Git's fixed arguments. Windows
	// has a smaller command-line limit and quotes/escapes arguments as UTF-16.
	if runtime.GOOS == "windows" {
		return 8 * 1024
	}
	return 64 * 1024
}

func grepTreePathsInFiles(ctx context.Context, repo, treeish string, patterns, files []string, textOnly bool, batchBytes int) ([]string, error) {
	if treeish == "" || strings.HasPrefix(treeish, "-") || strings.ContainsRune(treeish, '\x00') {
		return nil, fmt.Errorf("invalid git grep treeish %q", treeish)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 || len(patterns) == 0 {
		return []string{}, nil
	}
	// Sorting and deduplicating keeps batches and their result order stable even
	// when the inventory came from a map. Do not mutate the caller's inventory.
	seen := make(map[string]bool, len(files))
	paths := make([]string, 0, len(files))
	for _, path := range files {
		if path == "" || strings.ContainsRune(path, '\x00') {
			return nil, errors.New("git grep file name is empty or contains NUL")
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	matches := make([]string, 0)
	for start := 0; start < len(paths); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end, size := start, 0
		for end < len(paths) {
			cost := len(":(literal)") + len(paths[end]) + 1
			if end > start && size+cost > batchBytes {
				break
			}
			size += cost
			end++
		}
		batch, err := grepTreePathsWithFiles(ctx, repo, treeish, patterns, textOnly, false, paths[start:end])
		if err != nil {
			// Never return partial postings; callers can use their exhaustive fallback.
			return nil, err
		}
		matches = append(matches, batch...)
		start = end
	}
	sort.Strings(matches)
	return matches, nil
}
