package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// grepExcludePathspecBytes bounds the command-line bytes one GrepTreePatternFiles
// call spends on exclusion pathspecs. The patterns travel on stdin, so the
// pathspecs are the only argument that grows with the tree. Windows caps a whole
// command line at 32,767 UTF-16 units; only ASCII paths are excluded (one unit
// per byte), and grepExcludePathspecCost charges each argument its worst-case
// quoting, so 16 KiB leaves the fixed arguments ample room.
const grepExcludePathspecBytes = 16 << 10

const grepExcludePathspecMagic = ":(exclude,literal)"

// TreeBlob is one blob entry of a Git tree listing.
type TreeBlob struct {
	Path string
	Size int64
}

// ListTreeBlobs lists every blob in rev's tree beneath repo with its size in
// bytes, paths relative to repo exactly as `git grep --no-full-name` prints
// them. Symlinks are blobs and are listed; submodules and trees are not.
func ListTreeBlobs(ctx context.Context, repo, rev string) ([]TreeBlob, error) {
	if err := validateTreeish(rev); err != nil {
		return nil, err
	}
	out, err := run(ctx, repo, "git", "ls-tree", "-r", "-l", "-z", "--end-of-options", rev)
	if err != nil {
		return nil, err
	}
	var blobs []TreeBlob
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		// "<mode> SP <type> SP <object> SP+ <size> TAB <path>". -z keeps the path
		// raw, and a TAB cannot appear in the fields before it.
		meta, path, found := strings.Cut(entry, "\t")
		if !found || path == "" {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 || fields[1] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("git ls-tree returned blob size %q for %q", fields[3], path)
		}
		blobs = append(blobs, TreeBlob{Path: path, Size: size})
	}
	return blobs, nil
}

// GrepTreePatternFiles visits each file of an immutable tree that has a line
// matching one of the caller-built POSIX expressions (`git grep -l`), with Git's
// treeish prefix stripped. Git stops reading a file at its first match under
// -l but not under `-m 1`, so asking for paths rather than first lines is what
// keeps a huge single-line blob from being scanned to its end.
//
// exclude names paths the caller would discard anyway. They become exclusion
// pathspecs, in the order given, while they fit grepExcludePathspecBytes; a path
// past the budget, or one that is not plain ASCII, is still searched. Exclusion
// therefore only saves work and never decides membership: callers keep filtering
// what visit receives.
func GrepTreePatternFiles(ctx context.Context, repo, treeish string, patterns, exclude []string, visit func(path string) error) error {
	if treeish == "" || strings.HasPrefix(treeish, "-") || strings.ContainsRune(treeish, '\x00') {
		return fmt.Errorf("invalid git grep treeish %q", treeish)
	}
	prefix := treeish + ":"
	return runGrepPatterns(ctx, repo, treeish, patterns, []string{"-l"}, grepExcludePathspecs(exclude), func(reader *bufio.Reader) error {
		for {
			displayed, err := reader.ReadString(0)
			if err == io.EOF && displayed == "" {
				return nil
			}
			if err != nil {
				return fmt.Errorf("git grep returned a non-NUL-terminated path: %w", err)
			}
			displayed = strings.TrimSuffix(displayed, "\x00")
			if !strings.HasPrefix(displayed, prefix) {
				return fmt.Errorf("git grep returned path %q without treeish prefix %q", displayed, prefix)
			}
			if err := visit(strings.TrimPrefix(displayed, prefix)); err != nil {
				return err
			}
		}
	})
}

// grepExcludePathspecs renders exclusion pathspecs for paths, in order, until
// the next one would overflow grepExcludePathspecBytes; a path too long to fit
// is skipped and smaller ones after it are still admitted.
//
// Only printable ASCII paths are excluded. macOS Git precomposes Unicode in
// pathspec arguments (core.precomposeUnicode, on by default there), so excluding
// a decomposed name would exclude its precomposed twin instead, which can be a
// path the caller wants searched. Leaving a path in only costs scan time.
func grepExcludePathspecs(paths []string) []string {
	var pathspecs []string
	used := 0
	for _, path := range paths {
		if path == "" || !printableASCII(path) {
			continue
		}
		cost := grepExcludePathspecCost(path)
		if used+cost > grepExcludePathspecBytes {
			continue
		}
		used += cost
		pathspecs = append(pathspecs, grepExcludePathspecMagic+path)
	}
	return pathspecs
}

// grepExcludePathspecCost is an upper bound on the command-line bytes one
// exclusion argument takes: its text, a separator, a pair of quotes, and an
// escape for every quote or backslash Windows argument quoting may add.
func grepExcludePathspecCost(path string) int {
	return len(grepExcludePathspecMagic) + len(path) + 3 + strings.Count(path, `"`) + strings.Count(path, `\`)
}

func printableASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] < 0x20 || text[index] > 0x7e {
			return false
		}
	}
	return true
}

// ScanBlobLinesAtRev streams the blob at rev:path through visit one line at a
// time, with no size cap, and stops at the first line visit accepts. It reports
// whether one did. Lines are split on '\n' alone and passed without it, exactly
// as `git grep` delimits the lines it matches.
//
// It exists to validate a grep hit in a blob too large for the bounded readers:
// only the current line is held, so memory follows the longest line, as it does
// for the matched line `git grep` itself prints, rather than the whole blob.
func ScanBlobLinesAtRev(ctx context.Context, repo, rev, path string, visit func(line string) bool) (bool, error) {
	if err := validateTreeish(rev); err != nil {
		return false, err
	}
	if err := validateLimitedFilePath(path); err != nil {
		return false, err
	}
	treePath, err := repoTreePath(ctx, repo, path)
	if err != nil {
		return false, err
	}
	objectSpec := rev + "^{tree}:" + treePath
	cmd := newCmd(ctx, repo, "git", "cat-file", "blob", objectSpec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("git cat-file blob: %w", err)
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	found := false
	var readErr error
	for {
		line, err := reader.ReadString('\n')
		if line != "" && visit(strings.TrimSuffix(line, "\n")) {
			found = true
			break
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	if found {
		// The answer is known; the rest of the blob is not needed.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return true, nil
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return false, readErr
	}
	if waitErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return false, fmt.Errorf("git cat-file blob %s: %s", objectSpec, msg)
	}
	return false, nil
}
