package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/entireio/entire-graph/internal/filedigest"
)

type ChangedFile struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
}

type FileCochange struct {
	Left  string
	Right string
	Count int
}

// GrepMatch is one matched substring from a tracked-worktree fixed-string grep.
type GrepMatch struct {
	Path string
	Text string
}

// RepoRoot reports the top level of the checkout cwd sits in.
//
// Only git's own LINE TERMINATOR is removed, not every trailing space.
// `rev-parse --show-toplevel` prints the path followed by "\n", and a path
// component may legitimately END in a space — a trailing space is an ordinary
// byte in a POSIX name, so `git clone … "~/work/checkout "` is a checkout like
// any other. strings.TrimSpace takes that byte along with the terminator and
// returns the name of a DIFFERENT directory, which usually is not on disk at all;
// callers that use the result as a boundary (internal/cli.confinementRoot) then
// classify every path in the real checkout as outside it.
func RepoRoot(ctx context.Context, cwd string) (string, error) {
	out, err := run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// RepoPrefix reports cwd's path from the repository root, including Git's
// trailing slash when non-empty. Git is authoritative here: linked worktrees,
// submodules, and unusual path bytes must use the same coordinate system as
// tree object expressions. Only Git's final LF is removed.
func RepoPrefix(ctx context.Context, cwd string) (string, error) {
	out, err := run(ctx, cwd, "git", "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// RepoCommandRoot returns a cwd from which tree-root-relative paths keep the
// same coordinates. A worktree subdirectory is normalized to Git's top level;
// a worktree root or bare repository already has an empty prefix and is kept as
// provided. This matters when rev itself is a subtree OID: running tree commands
// from the original subdirectory would apply that prefix a second time.
func RepoCommandRoot(ctx context.Context, cwd string) (string, error) {
	prefix, err := RepoPrefix(ctx, cwd)
	if err != nil {
		return "", err
	}
	if prefix == "" {
		return cwd, nil
	}
	return RepoRoot(ctx, cwd)
}

func repoTreePath(ctx context.Context, repo, path string) (string, error) {
	prefix, err := RepoPrefix(ctx, repo)
	if err != nil {
		return "", err
	}
	return prefix + path, nil
}

func RevParse(ctx context.Context, repo, rev string) (string, error) {
	out, err := run(ctx, repo, "git", "rev-parse", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func FirstParent(ctx context.Context, repo, rev string) (string, error) {
	out, err := run(ctx, repo, "git", "rev-parse", rev+"^")
	if err != nil {
		return "", fmt.Errorf("resolve first parent for %s: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

func FindCommitWithCheckpoint(ctx context.Context, repo, checkpointID string) (string, error) {
	out, err := run(ctx, repo, "git", "log", "--all", "--format=%H", "-n", "1", "--grep=Entire-Checkpoint: "+checkpointID)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", fmt.Errorf("checkpoint %s has no associated commit in this repository", checkpointID)
	}
	return commit, nil
}

func ListFiles(ctx context.Context, repo, rev string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// ListIndexFiles lists the tracked files of the working tree's git index
// (`git ls-files -z`), relative to repo. It runs one git subprocess for the
// whole listing; callers use it to decide tracked-ness without per-path git
// calls. A non-git directory returns an error.
func ListIndexFiles(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// ListWorktreeFiles lists the working tree the way Git itself sees it: tracked
// files plus untracked files that no exclude rule covers
// (`git ls-files --cached --others --exclude-standard`). Delegating the exclude
// decision to Git is the point — it applies nested .gitignore files,
// .git/info/exclude, per-worktree excludes, and core.excludesFile (global and
// system), none of which a hand-rolled reader of the repository-root .gitignore
// can see. Paths are relative to repo and returned in Git's order with
// duplicates removed; a non-git directory returns an error so callers can fall
// back to a filesystem walk.
func ListWorktreeFiles(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitNULPaths(out), nil
}

// ListIgnoredWorktreeFiles lists the untracked working-tree files Git's exclude
// rules *do* cover (`git ls-files --others --ignored --exclude-standard`). It
// exists for one caller: an explicit include-file whose negations re-include
// paths the project gitignores. Nothing else should enumerate ignored content —
// that is the tree whose size is the reason the exclude rules exist.
func ListIgnoredWorktreeFiles(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitNULPaths(out), nil
}

func splitNULPaths(out string) []string {
	fields := strings.Split(out, "\x00")
	files := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, path := range fields {
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	return files
}

// GrepIndexMatches returns a bounded sample of matched terms per tracked
// worktree file. Fixed strings and NUL-delimited paths keep query terms and
// unusual paths from changing grep semantics.
func GrepIndexMatches(ctx context.Context, repo string, patterns []string, maxPerFile int) ([]GrepMatch, error) {
	return grepFixedStringMatches(ctx, repo, "", patterns, maxPerFile)
}

// GrepTreeMatches returns a bounded sample of matched fixed strings per file
// from an immutable Git tree. The returned paths are relative to repo and do
// not include Git's "<treeish>:" display prefix. Query strings are always
// passed as fixed-string patterns and paths are NUL-delimited, so neither can
// change grep or path parsing semantics.
func GrepTreeMatches(ctx context.Context, repo, treeish string, patterns []string, maxPerFile int) ([]GrepMatch, error) {
	if treeish == "" {
		return nil, errors.New("git grep treeish cannot be empty")
	}
	if strings.HasPrefix(treeish, "-") || strings.ContainsRune(treeish, '\x00') {
		return nil, fmt.Errorf("invalid git grep treeish %q", treeish)
	}
	return grepFixedStringMatches(ctx, repo, treeish, patterns, maxPerFile)
}

// GrepTreePaths returns every file in an immutable Git tree that contains at
// least one of the fixed-string patterns. Git emits each path once in
// NUL-delimited form, avoiding both matched-text fanout and ambiguity from
// unusual path bytes.
//
// It passes -I, so a blob Git itself classifies as binary (a NUL byte early
// in the content, or a `.gitattributes` binary/-diff marking) is silently
// excluded from the result even if it contains a matching pattern. That
// makes this preselection a strict superset of a text-only match, but NOT of
// every possible match in the tree -- callers that need every matching file
// regardless of Git's binary heuristic must use GrepTreePathsIncludingBinary.
func GrepTreePaths(ctx context.Context, repo, treeish string, patterns []string) ([]string, error) {
	if treeish == "" {
		return nil, errors.New("git grep treeish cannot be empty")
	}
	return grepTreePaths(ctx, repo, treeish, patterns, true, false)
}

// GrepTreePathsIncludingBinary behaves exactly like GrepTreePaths except it
// omits -I, so a file Git classifies as binary (an early NUL byte, or a
// `.gitattributes` binary/-diff marking) is still searched and can appear in
// the result. Use this when a caller's correctness requires a genuine strict
// superset of every file that contains a matching pattern, regardless of
// Git's binary heuristic -- e.g. a prefilter ahead of a parser that reads
// raw file content directly and does not care whether Git thinks the file is
// binary.
func GrepTreePathsIncludingBinary(ctx context.Context, repo, treeish string, patterns []string) ([]string, error) {
	if treeish == "" {
		return nil, errors.New("git grep treeish cannot be empty")
	}
	return grepTreePaths(ctx, repo, treeish, patterns, false, false)
}

// GrepTreePathsCaseSensitiveIncludingBinary behaves like
// GrepTreePathsIncludingBinary but matches case-sensitively. It exists for
// identifier prefilters: a case-sensitive substring match is still a strict
// superset of a case-sensitive whole-identifier check, while excluding files
// that only contain the pattern in a different case. Deliberately NOT -w:
// git grep's word-boundary mode leaves the multi-pattern fixed-string fast
// path and is orders of magnitude slower with hundreds of patterns (measured
// 5s vs 0.06s on a ~2.6k-file tree with 234 patterns).
func GrepTreePathsCaseSensitiveIncludingBinary(ctx context.Context, repo, treeish string, patterns []string) ([]string, error) {
	if treeish == "" {
		return nil, errors.New("git grep treeish cannot be empty")
	}
	return grepTreePaths(ctx, repo, treeish, patterns, false, true)
}

// GrepFixedStringPaths returns every file containing one exact, case-sensitive string. An empty
// treeish greps the working tree (what a worktree search indexes); a non-empty one greps that
// immutable tree.
//
// It exists for the repository-wide literal lookup in search: one needle, exact case, and the
// caller reads the matched files itself to get line numbers, so no output parsing beyond the
// NUL-delimited path list this package already does.
func GrepFixedStringPaths(ctx context.Context, repo, treeish, pattern string) ([]string, error) {
	if pattern == "" {
		return []string{}, nil
	}
	return grepTreePaths(ctx, repo, treeish, []string{pattern}, false, true)
}

func grepTreePaths(ctx context.Context, repo, treeish string, patterns []string, textOnly, caseSensitive bool) ([]string, error) {
	if strings.HasPrefix(treeish, "-") || strings.ContainsRune(treeish, '\x00') {
		return nil, fmt.Errorf("invalid git grep treeish %q", treeish)
	}
	if len(patterns) == 0 {
		return []string{}, nil
	}
	args := []string{"grep", "-z"}
	if textOnly {
		args = append(args, "-I")
	}
	if caseSensitive {
		args = append(args, "-F", "-l")
	} else {
		args = append(args, "-i", "-F", "-l")
	}
	patternCount := 0
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		args = append(args, "-e", pattern)
		patternCount++
	}
	if patternCount == 0 {
		return []string{}, nil
	}
	// An empty treeish means the working tree: Git is given no revision at all, and the paths it
	// prints then carry no `<treeish>:` display prefix.
	if treeish != "" {
		args = append(args, treeish)
	}
	args = append(args, "--")
	cmd := newGitCmdWithCallerLocale(ctx, repo, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 && stderr.Len() == 0 {
			return []string{}, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	if stderr.Len() > 0 {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "unexpected stderr output"
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	prefix := ""
	if treeish != "" {
		prefix = treeish + ":"
	}
	data := stdout.Bytes()
	paths := make([]string, 0, bytes.Count(data, []byte{0}))
	for len(data) > 0 {
		pathEnd := bytes.IndexByte(data, 0)
		if pathEnd < 0 {
			return nil, errors.New("git grep returned a non-NUL-terminated path")
		}
		displayed := string(data[:pathEnd])
		if prefix != "" && !strings.HasPrefix(displayed, prefix) {
			return nil, fmt.Errorf("git grep returned path %q without treeish prefix %q", displayed, prefix)
		}
		paths = append(paths, strings.TrimPrefix(displayed, prefix))
		data = data[pathEnd+1:]
	}
	return paths, nil
}

func grepFixedStringMatches(ctx context.Context, repo, treeish string, patterns []string, maxPerFile int) ([]GrepMatch, error) {
	if len(patterns) == 0 {
		return []GrepMatch{}, nil
	}
	if maxPerFile <= 0 {
		maxPerFile = 32
	}
	args := []string{"grep", "-z", "-I", "-i", "-F", "-o", "-m", strconv.Itoa(maxPerFile)}
	for _, pattern := range patterns {
		if pattern != "" {
			args = append(args, "-e", pattern)
		}
	}
	if len(args) == 8 {
		return []GrepMatch{}, nil
	}
	if treeish != "" {
		args = append(args, treeish)
	}
	args = append(args, "--")
	// Preserve the caller's locale here. Unlike the other git commands in this
	// package, `git grep -i` uses LC_CTYPE for non-ASCII case folding; forcing
	// the C locale would make Unicode matches disappear.
	cmd := newGitCmdWithCallerLocale(ctx, repo, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 && stderr.Len() == 0 {
			return []GrepMatch{}, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	if stderr.Len() > 0 {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "unexpected stderr output"
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}

	data := stdout.Bytes()
	matches := make([]GrepMatch, 0)
	for len(data) > 0 {
		pathEnd := bytes.IndexByte(data, 0)
		if pathEnd < 0 {
			return nil, fmt.Errorf("git grep returned malformed path metadata")
		}
		path := string(data[:pathEnd])
		if treeish != "" {
			prefix := treeish + ":"
			if !strings.HasPrefix(path, prefix) {
				return nil, fmt.Errorf("git grep returned path %q without treeish prefix %q", path, prefix)
			}
			path = strings.TrimPrefix(path, prefix)
		}
		data = data[pathEnd+1:]
		textEnd := bytes.IndexByte(data, '\n')
		if textEnd < 0 {
			textEnd = len(data)
		}
		matches = append(matches, GrepMatch{Path: path, Text: string(data[:textEnd])})
		if textEnd == len(data) {
			data = nil
		} else {
			data = data[textEnd+1:]
		}
	}
	return matches, nil
}

func ChangedFiles(ctx context.Context, repo, base, head string, paths []string) ([]ChangedFile, error) {
	args := []string{"diff", "-z", "--name-status", "--find-renames", base, head, "--"}
	args = append(args, paths...)
	out, err := run(ctx, repo, "git", args...)
	if err != nil {
		return nil, err
	}

	var files []ChangedFile
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		switch {
		case strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C"):
			if i+1 < len(fields) {
				files = append(files, ChangedFile{Status: status[:1], OldPath: fields[i], Path: fields[i+1]})
				i += 2
			}
		default:
			if i < len(fields) {
				files = append(files, ChangedFile{Status: status[:1], Path: fields[i]})
				i++
			}
		}
	}
	return files, nil
}

// FileCochanges returns repeated file pairs from the history reachable from
// revision. Callers pass an already-resolved commit so every
// history-derived relation belongs to the same immutable snapshot as its
// files and symbols.
func FileCochanges(ctx context.Context, repo, revision string, maxCommits int) ([]FileCochange, error) {
	if revision == "" {
		return nil, errors.New("git co-change revision cannot be empty")
	}
	if strings.HasPrefix(revision, "-") || strings.ContainsRune(revision, '\x00') {
		return nil, fmt.Errorf("invalid git co-change revision %q", revision)
	}
	if maxCommits <= 0 {
		maxCommits = 256
	}
	// -z makes git emit raw, NUL-terminated pathnames with no quoting at all,
	// matching the file keys produced by ListFiles (`ls-tree -z`). A plain
	// --name-only (even with core.quotePath=false) still C-quotes paths
	// containing '"', '\', tabs, or newlines, which would never match those
	// keys. The per-commit marker is emitted via --pretty=format; under -z each
	// commit's output is either the marker alone (no files, e.g. a merge) or
	// "<marker>\n<first file>" followed by NUL-separated paths.
	const marker = "--entire-graph-commit--"
	// maxFilesPerCommit bounds the O(n^2) co-change pair expansion for a single
	// commit. A commit touching more files than this is a mass change (initial
	// import, tree-wide rename/format, generated-file regeneration, large merge),
	// whose pairs are co-change noise rather than signal — and enumerating them
	// blows up memory: one 10k-file commit alone produces ~50M pair keys (multi-GB).
	// Real feature/fix commits touch a handful of related files and stay well under.
	const maxFilesPerCommit = 50
	out, err := run(ctx, repo, "git", "log", "-z", "--name-only", "--pretty=format:"+marker, "-n", strconv.Itoa(maxCommits), revision, "--")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	var commitFiles []string
	flush := func() {
		if len(commitFiles) < 2 {
			commitFiles = nil
			return
		}
		sort.Strings(commitFiles)
		uniq := commitFiles[:0]
		for _, path := range commitFiles {
			if len(uniq) == 0 || uniq[len(uniq)-1] != path {
				uniq = append(uniq, path)
			}
		}
		if len(uniq) > maxFilesPerCommit {
			commitFiles = nil
			return // mass-change commit: skip its O(n^2) noise pairs (the memory explosion source)
		}
		for i := 0; i < len(uniq); i++ {
			for j := i + 1; j < len(uniq); j++ {
				counts[uniq[i]+"\x00"+uniq[j]]++
			}
		}
		commitFiles = nil
	}
	for _, tok := range strings.Split(out, "\x00") {
		if tok == marker {
			flush()
			continue
		}
		if first, ok := strings.CutPrefix(tok, marker+"\n"); ok {
			flush()
			if first != "" {
				commitFiles = append(commitFiles, first)
			}
			continue
		}
		if tok != "" {
			commitFiles = append(commitFiles, tok)
		}
	}
	flush()

	pairs := make([]FileCochange, 0, len(counts))
	for key, count := range counts {
		if count < 2 {
			continue
		}
		left, right, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		pairs = append(pairs, FileCochange{Left: left, Right: right, Count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		if pairs[i].Left != pairs[j].Left {
			return pairs[i].Left < pairs[j].Left
		}
		return pairs[i].Right < pairs[j].Right
	})
	if len(pairs) > 1000 {
		pairs = pairs[:1000]
	}
	return pairs, nil
}

// ShowFile reads path relative to repo from a revision that represents the
// repository-root tree. When repo is a subdirectory, Git's authoritative cwd
// prefix is added before resolving the object.
func ShowFile(ctx context.Context, repo, rev, path string) (string, bool, error) {
	// Classify against git's stderr only, never the wrapped error that echoes
	// the argv (which includes rev+":"+path). Matching the full error text made
	// any real failure on a path containing a marker substring (e.g. "Path" in
	// src/PathHelper.go) look like a missing file, swallowing the error.
	// Peel the revision to a tree before resolving the path. Without the type
	// constraint, a missing full object ID or a blob object can produce the
	// same path-looking diagnostic as a genuinely absent file.
	treePath, err := repoTreePath(ctx, repo, path)
	if err != nil {
		return "", false, err
	}
	objectSpec := rev + "^{tree}:" + treePath
	out, stderr, err := runWithStderr(ctx, repo, "git", "show", objectSpec)
	if err != nil {
		if isMissingPathDiagnostic(stderr) {
			return "", false, nil
		}
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return "", false, fmt.Errorf("git show %s: %s", objectSpec, msg)
	}
	return out, true, nil
}

// ShowFileLimited is ShowFile with a size ceiling: a larger blob is always
// reported unreadable, and is normally refused before its bytes are read.
//
// The two halves of that sentence are guaranteed differently, and the difference
// is the contract. Refusing an oversized blob is UNCONDITIONAL — it holds even
// when the size probe below says nothing. Refusing it without materializing it
// holds whenever the probe answers, which is every ordinary run; when the probe
// cannot answer, the fallback read is bounded on its result rather than on its
// allocation. An earlier version of this comment promised the absolute form
// after the probe had already been made best effort, which is the sort of claim
// a caller sizing a buffer would rely on.
//
// ShowFile buffers all of git's stdout, so a caller that only wants small files
// could not express that — checking len(content) afterwards bounds the ANSWER
// once the allocation has already happened. The other bounded readers here do
// not work that way: the batch reader decides off the size in its header, and
// the on-disk reader stats before it opens. This asks Git for the object type
// and then its size first, for the same reason and with the same shape.
//
// Deciding from the size, rather than reading through an io.LimitReader and
// stopping git once the ceiling is passed, is deliberate. The reading form
// deadlocked on Windows: killing git left a grandchild holding the inherited
// stderr handle, so the copier goroutine never saw EOF and Cmd.Wait blocked in
// awaitGoroutines forever. Nothing here now depends on process-tree teardown.
// This one-shot helper pays for separate metadata probes. Callers scanning a
// range of paths should use LimitedFileReader, which batches metadata, shares
// exceptional component traversal, and sends exact blob OIDs (never repository
// paths) to its persistent content batch.
//
// The metadata probe is authoritative only when Git identifies the object. A
// blob proceeds to the size decision below; a non-blob (notably a gitlink's
// commit object) is refused before `git show` can render that object's patch.
// Anything the probe cannot establish falls through to the read, which keeps
// ShowFile's absent-vs-failed classification.
func ShowFileLimited(ctx context.Context, repo, rev, path string, maxBytes int64) (string, bool, error) {
	return showFileLimited(ctx, repo, rev, path, maxBytes, blobSizeAtRev)
}

// LimitedFileStatus classifies the outcome of ReadFileLimited without
// conflating a missing path, an oversized blob, and a non-blob tree entry.
type LimitedFileStatus uint8

const (
	LimitedFileMissing LimitedFileStatus = iota
	LimitedFileContent
	LimitedFileOversize
	LimitedFileNonBlob
	// LimitedFileUnreadable means the tree entry exists and identifies a blob,
	// but Git could not read that blob's object metadata. This is distinct from
	// LimitedFileMissing (no tree entry) and from the bounded traversal refusal
	// below; callers can report one bad object as a recoverable file-read failure.
	LimitedFileUnreadable
	// LimitedFileUnaddressable means a valid tree path could not be resolved
	// within the bounded exact-metadata traversal: a component exceeded the
	// argv ceiling, the component-process allowance was exhausted, or the
	// caller's traversal deadline elapsed. No content was read.
	LimitedFileUnaddressable
)

// LimitedFileResult is the bounded, typed result for one path at a revision.
// Content is populated only for LimitedFileContent. Bytes is the exact content
// size for LimitedFileContent and LimitedFileOversize; the latter comes from
// Git's object metadata whenever the probe succeeds, so the blob is not read.
type LimitedFileResult struct {
	Status  LimitedFileStatus
	Content string
	Bytes   int64
}

// ReadFileLimited is the typed form of ShowFileLimited. Callers that need one
// coherent snapshot across this metadata probe and other Git operations must
// pass an already-resolved immutable revision rather than a moving ref name.
// Like ShowFile, path is relative to repo and rev represents the repository
// root when repo is a subdirectory. Path must be canonical and file-like: it
// cannot be empty or NUL-bearing and cannot contain empty, ".", or ".."
// components. Invalid paths fail before Git runs.
func ReadFileLimited(ctx context.Context, repo, rev, path string, maxBytes int64) (LimitedFileResult, error) {
	return readFileLimited(ctx, repo, rev, path, maxBytes, blobSizeAtRev)
}

// showFileLimited preserves the compact historical API for callers that only
// distinguish returned content from unavailable content.
func showFileLimited(
	ctx context.Context,
	repo, rev, path string,
	maxBytes int64,
	probe func(ctx context.Context, repo, rev, path string) (int64, blobProbeStatus),
) (string, bool, error) {
	result, err := readFileLimited(ctx, repo, rev, path, maxBytes, probe)
	if err != nil {
		return "", false, err
	}
	if result.Status != LimitedFileContent {
		return "", false, nil
	}
	return result.Content, true, nil
}

// readFileLimited takes the probe as an argument so a test can exercise the
// best-effort no-answer path without relying on a transient Git failure.
func readFileLimited(
	ctx context.Context,
	repo, rev, path string,
	maxBytes int64,
	probe func(ctx context.Context, repo, rev, path string) (int64, blobProbeStatus),
) (LimitedFileResult, error) {
	// A file read must never let git-show reinterpret an empty/trailing-slash
	// path as a request to render a tree. Validate before the best-effort probe:
	// otherwise its validation error becomes "unknown" and the fallback can
	// materialize an unbounded repository-controlled directory listing.
	if err := validateLimitedFilePath(path); err != nil {
		return LimitedFileResult{}, err
	}
	// An identified non-blob is not file content. In particular, `git show` on a
	// gitlink's small commit object renders the commit's potentially enormous
	// patch, so trusting only that object's size would defeat the caller's bound.
	// Unknown outcomes — missing path, bad revision, unexpected diagnostics, or
	// an unparsable size — still fall through to ShowFile, which remains the one
	// place absent-vs-failed is decided.
	size, status := probe(ctx, repo, rev, path)
	if status == blobProbeNonBlob {
		return LimitedFileResult{Status: LimitedFileNonBlob}, nil
	}
	if status == blobProbeUnreadable {
		return LimitedFileResult{Status: LimitedFileUnreadable}, nil
	}
	if status == blobProbeUnaddressable {
		return LimitedFileResult{Status: LimitedFileUnaddressable}, nil
	}
	if status == blobProbeBlob && maxBytes > 0 && size > maxBytes {
		// Refused, not failed: a blob this caller cannot quote, exactly like a
		// missing one. No content was read to learn this.
		return LimitedFileResult{Status: LimitedFileOversize, Bytes: size}, nil
	}
	// A positively identified blob can be read with plumbing, avoiding git
	// show's worktree-path disambiguation and its commit-rendering behavior.
	// Unknown probes retain ShowFile as the historical missing-vs-error arbiter.
	var content string
	var ok bool
	var err error
	if status == blobProbeBlob {
		content, ok, err = readBlobAtRev(ctx, repo, rev, path)
	} else {
		content, ok, err = ShowFile(ctx, repo, rev, path)
	}
	if err != nil {
		return LimitedFileResult{}, err
	}
	if !ok {
		return LimitedFileResult{Status: LimitedFileMissing}, nil
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		// Only reachable when the probe gave no answer. The bytes are already
		// allocated, so this cannot restore the memory bound — but it keeps the
		// ANSWER identical either way, so a caller never receives content it
		// declared too large to accept.
		return LimitedFileResult{Status: LimitedFileOversize, Bytes: int64(len(content))}, nil
	}
	return LimitedFileResult{Status: LimitedFileContent, Content: content, Bytes: int64(len(content))}, nil
}

func validateLimitedFilePath(path string) error {
	if path == "" || strings.ContainsRune(path, 0) {
		return fmt.Errorf("invalid Git tree path %q", path)
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("invalid Git tree path %q", path)
		}
	}
	return nil
}

// IsCanonicalGitTreePath reports whether path has the file-like, slash-separated
// form accepted by the bounded object readers. Raw Git trees can contain "." or
// ".." entries that cannot be represented safely by rev:path plumbing; callers
// enumerating such trees use this predicate to turn one entry into a partial
// read failure without poisoning a shared Git process.
func IsCanonicalGitTreePath(path string) bool {
	return validateLimitedFilePath(path) == nil
}

func readBlobAtRev(ctx context.Context, repo, rev, path string) (string, bool, error) {
	treePath, err := repoTreePath(ctx, repo, path)
	if err != nil {
		return "", false, err
	}
	objectSpec := rev + "^{tree}:" + treePath
	out, stderr, err := runWithStderr(ctx, repo, "git", "cat-file", "blob", objectSpec)
	if err != nil {
		if isMissingPathDiagnostic(stderr) {
			return "", false, nil
		}
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return "", false, fmt.Errorf("git cat-file blob %s: %s", objectSpec, msg)
	}
	return out, true, nil
}

// OversizeBlobAtRev reports the size, content hash and line count of the blob at
// rev:path when that blob exceeds maxBytes — the same OversizeBlob the batch
// reader records for a blob IT refuses, learned the same way, by streaming the
// blob past a digest and discarding it. The bytes are never held.
//
// It is the companion ShowFileLimited owes its callers. The batch reader digests
// what it declines on the way past, so a caller can still record a file it could
// not parse; ShowFileLimited only refuses, so a caller taking that fallback — a
// Git path containing a newline, which `cat-file --batch` cannot carry — would
// otherwise lose the file entirely because of how it is NAMED.
//
// The second return means "this blob exists and is over maxBytes", not merely
// "found": false is also the answer for a blob at or under the ceiling, so a
// caller that refused for some other reason cannot record it as oversized. A
// missing path is (false, nil); a read that broke is (false, err).
//
// maxBytes <= 0 means no ceiling, so nothing is oversized and no git runs.
func OversizeBlobAtRev(ctx context.Context, repo, rev, path string, maxBytes int64) (OversizeBlob, bool, error) {
	if maxBytes <= 0 {
		return OversizeBlob{}, false, nil
	}
	treePath, err := repoTreePath(ctx, repo, path)
	if err != nil {
		return OversizeBlob{}, false, err
	}
	cmd := newCmd(ctx, repo, "git", "cat-file", "blob", rev+"^{tree}:"+treePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return OversizeBlob{}, false, err
	}
	if err := cmd.Start(); err != nil {
		return OversizeBlob{}, false, fmt.Errorf("git cat-file blob: %w", err)
	}
	prefix := make([]byte, 0, oversizeBlobPrefixCap)
	digest, digestErr := filedigest.Stream(io.TeeReader(stdout, prefixCaptureWriter{buf: &prefix}))
	// Drain anything the digest did not consume before waiting. git blocks on a
	// full pipe, and Wait would then block on git.
	_, _ = io.Copy(io.Discard, stdout)
	if waitErr := cmd.Wait(); waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if isMissingPathDiagnostic(msg) {
			return OversizeBlob{}, false, nil
		}
		if msg == "" {
			msg = waitErr.Error()
		}
		return OversizeBlob{}, false, fmt.Errorf("git cat-file blob %s:%s: %s", rev, path, msg)
	}
	if digestErr != nil {
		return OversizeBlob{}, false, digestErr
	}
	if digest.Bytes <= maxBytes {
		return OversizeBlob{}, false, nil
	}
	return OversizeBlob{Bytes: digest.Bytes, Hash: digest.Hash, Lines: digest.Lines, Prefix: string(prefix)}, true, nil
}

type blobProbeStatus uint8

const (
	blobProbeUnknown blobProbeStatus = iota
	blobProbeBlob
	blobProbeNonBlob
	blobProbeUnreadable
	blobProbeUnaddressable
)

// blobSizeAtRev reports the type and, for a blob, the size of rev:path without
// reading its content. It is best effort: unknown means "no answer", never
// "absent" or "broken", so a caller can only use it to refuse, not to conclude.
func blobSizeAtRev(ctx context.Context, repo, rev, path string) (int64, blobProbeStatus) {
	treePath, err := repoTreePath(ctx, repo, path)
	if err != nil {
		return 0, blobProbeUnknown
	}
	entry, found, err := treeEntryMetadata(ctx, repo, rev, treePath)
	if err != nil || !found {
		return 0, blobProbeUnknown
	}
	if entry.result.Status == LimitedFileUnaddressable {
		return 0, blobProbeUnaddressable
	}
	if entry.result.Status == LimitedFileUnreadable {
		return 0, blobProbeUnreadable
	}
	if entry.objectType != "blob" {
		return 0, blobProbeNonBlob
	}
	return entry.result.Bytes, blobProbeBlob
}

func isMissingPathDiagnostic(stderr string) bool {
	// ShowFile runs git under the C locale, so only classify Git's specific
	// missing-path diagnostics. Broad substring checks can match a bad revision
	// or an unrelated error merely because an argv value contains the phrase.
	return strings.HasPrefix(stderr, "fatal: path '") &&
		(strings.Contains(stderr, "' does not exist in '") ||
			strings.Contains(stderr, "' exists on disk, but not in '"))
}

// BatchFileReader reads blobs from one revision through one persistent
// `git cat-file --batch-command` process. Each read issues `info` first and
// issues `contents` only for a blob, so a non-blob never enters the content
// stream. All production Git commands ignore replace refs, and keeping both
// commands in one process ensures an immutable raw OID cannot change type
// between the metadata and content responses. Oversized blobs are still
// streamed for caller digests.
// Paths are relative to repo; rev represents the repository-root tree when repo
// is a subdirectory.
type BatchFileReader struct {
	rev          string
	pathPrefix   string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Reader
	stderr       *bytes.Buffer
	mu           sync.Mutex
	closed       bool
	poison       error
	maxBytes     int64
	oversize     map[string]OversizeBlob
	oversizeScan func(path string, chunk []byte)
}

// OversizeBlob describes a blob ReadFile refused to materialize because it
// exceeds the reader's cap. The hash and line count are computed while the blob
// is streamed past and discarded, so a caller can still record the file's
// identity and shape without ever holding its bytes.
type OversizeBlob struct {
	Bytes int64
	Hash  string
	Lines int
	// Prefix holds up to oversizeBlobPrefixCap leading bytes, captured for
	// free from the same mandatory streaming pass that computes Hash and
	// Lines. Git blob reads are all-or-nothing, so a refused blob otherwise
	// cannot be prefix-sniffed at all (for example, to route an
	// extensionless committed file by its shebang line) without a second,
	// unbounded read of the same object.
	Prefix string
}

// oversizeBlobPrefixCap bounds OversizeBlob.Prefix. It is comfortably above
// the largest known prefix consumer (shebang sniffing) while staying a fixed,
// small cost independent of the blob's real size.
const oversizeBlobPrefixCap = 4096

// prefixCaptureWriter retains up to oversizeBlobPrefixCap leading bytes
// written to it and discards the rest, for use as an io.TeeReader sink
// alongside a digest pass that must consume the whole stream anyway.
type prefixCaptureWriter struct {
	buf *[]byte
}

func (w prefixCaptureWriter) Write(p []byte) (int, error) {
	if room := oversizeBlobPrefixCap - len(*w.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		*w.buf = append(*w.buf, p[:room]...)
	}
	return len(p), nil
}

// SetOversizeScanner registers a callback invoked with successive chunks of an OVERSIZE blob as it
// streams past the reader and is discarded. It exists so a caller can decide whether a blob it will
// never hold was nonetheless relevant: the dependents scan needs to know whether an oversized file
// contained a changed name, because warning about a file that never was a candidate is noise. The
// bytes are BORROWED - the callback must not retain the slice. Chunks arrive in order and may split
// a token, so a caller matching multi-byte patterns must carry its own overlap.
func (r *BatchFileReader) SetOversizeScanner(scan func(path string, chunk []byte)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.oversizeScan = scan
}

// SetMaxBytes caps the blob size ReadFile will materialize. A larger blob is
// streamed past the reader into a digest and discarded: ReadFile reports it as
// unavailable and OversizeBlob then returns its size, content hash and line
// count. Without a cap one oversized blob costs its own size twice (the byte
// slice plus the string conversion), so the reader's memory is set by the
// largest object in the revision rather than by anything the caller chose. Zero
// or negative removes the cap.
func (r *BatchFileReader) SetMaxBytes(maxBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxBytes = maxBytes
}

// OversizeBlob returns what ReadFile learned about a blob it refused to
// materialize, so the caller can record the file without its content.
func (r *BatchFileReader) OversizeBlob(path string) (OversizeBlob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, ok := r.oversize[path]
	return blob, ok
}

func NewBatchFileReader(ctx context.Context, repo, rev string) (*BatchFileReader, error) {
	pathPrefix, err := RepoPrefix(ctx, repo)
	if err != nil {
		return nil, err
	}
	cmd := newCmd(ctx, repo, "git", "cat-file", "--batch-command")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("git cat-file --batch-command (requires Git 2.36 or newer): %w", err)
	}
	reader := &BatchFileReader{
		rev:        rev,
		pathPrefix: pathPrefix,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdoutPipe),
		stderr:     &stderr,
	}
	// Start is not enough to feature-detect an option: an older Git starts and
	// then exits after parsing argv. A metadata-only missing-object request
	// verifies the protocol without reading an object body. The process already
	// has raw-object semantics through GIT_NO_REPLACE_OBJECTS.
	const probeOID = "0000000000000000000000000000000000000000"
	if _, _, _, found, probeErr := reader.objectInfoLocked(probeOID); probeErr != nil || found {
		_ = stdin.Close()
		waitErr := cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			if probeErr != nil {
				msg = probeErr.Error()
			} else if waitErr != nil {
				msg = waitErr.Error()
			} else {
				msg = "unexpected response to protocol probe"
			}
		}
		return nil, fmt.Errorf("git cat-file --batch-command unavailable (Git 2.36 or newer required): %s", msg)
	}
	return reader, nil
}

// IsBatchPathSafe reports whether path can be embedded in one LF-delimited
// cat-file batch-command request without splitting or normalization. A
// trailing CR is consumed by Git's line reader; internal CR bytes are preserved.
func IsBatchPathSafe(path string) bool {
	return !strings.Contains(path, "\n") && !strings.HasSuffix(path, "\r")
}

// IsPathSafe includes the repository-cwd prefix that ReadFile prepends before
// writing its request. Git reports embedded newlines in that prefix verbatim,
// so a plain caller-relative path can still be unsafe in an unusual subdir.
func (r *BatchFileReader) IsPathSafe(path string) bool {
	return IsBatchPathSafe(r.rev + ":" + r.pathPrefix + path)
}

func (r *BatchFileReader) ReadFile(path string) (string, bool, error) {
	// Once framing failed, even a path rejected locally must not obscure the
	// session's original cause. More importantly, no later call may reach stdin.
	r.mu.Lock()
	poison := r.poison
	r.mu.Unlock()
	if poison != nil {
		return "", false, poison
	}
	// Reject path forms Git interprets relative to the worktree before they enter
	// the persistent protocol. In particular, `rev:../file` terminates cat-file
	// with an "outside repository" error and would poison every later read on the
	// shared process. Repository tree listings can contain such raw components
	// even though a filesystem checkout cannot.
	if err := validateLimitedFilePath(path); err != nil {
		return "", false, err
	}
	if !r.IsPathSafe(path) {
		return "", false, fmt.Errorf("Git path cannot be represented by cat-file batch protocol")
	}
	return r.readCheckedObjectSpec(r.rev+":"+r.pathPrefix+path, path)
}

func (r *BatchFileReader) readCheckedObjectSpec(objectSpec, oversizeKey string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	objectID, objectType, objectSize, found, err := r.objectInfoLocked(objectSpec)
	if err != nil || !found {
		return "", false, err
	}
	if objectType != "blob" {
		// Never issue contents for a non-blob. That response places the complete
		// object after its header, so declining only after the request would still
		// have to stream an arbitrarily large tree or commit merely to reach the next
		// response. The info response has no object body to drain.
		return "", false, nil
	}
	// Pin the checked object before asking for content. Besides avoiding a second
	// path-bearing request, this keeps a moving ref from changing object type
	// between the metadata and content commands.
	return r.readObjectContentsLocked(objectID, objectType, objectSize, oversizeKey)
}

// readKnownBlobObjectID is the exact-OID path for LimitedFileReader, whose
// ls-tree metadata established that objectID was a blob at metadata time. It
// still repeats the type check in this SAME batch-command process before asking
// for contents, defending against missing/corrupt objects without ever sending
// a non-blob body request. The request contains only an immutable raw OID, never
// repository path bytes.
func (r *BatchFileReader) readKnownBlobObjectID(objectID, oversizeKey string) (string, bool, error) {
	return r.readCheckedObjectSpec(objectID, oversizeKey)
}

func (r *BatchFileReader) objectInfoLocked(objectSpec string) (string, string, int64, bool, error) {
	if r.poison != nil {
		return "", "", 0, false, r.poison
	}
	if r.closed {
		return "", "", 0, false, fmt.Errorf("git cat-file batch reader is closed")
	}
	if _, err := fmt.Fprintf(r.stdin, "info %s\n", objectSpec); err != nil {
		return "", "", 0, false, r.poisonLocked(fmt.Errorf("write git cat-file batch-command info request: %w", err))
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return "", "", 0, false, r.poisonLocked(fmt.Errorf("read git cat-file batch-command info header: %w", err))
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") {
		if header != objectSpec+" missing" {
			return "", "", 0, false, r.poisonLocked(fmt.Errorf("unexpected git cat-file batch-command info missing header %q", header))
		}
		return "", "", 0, false, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", "", 0, false, r.poisonLocked(fmt.Errorf("unexpected git cat-file batch-command info header %q", header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return "", "", 0, false, r.poisonLocked(fmt.Errorf("parse git cat-file batch-command info size %q", fields[2]))
	}
	return fields[0], fields[1], size, true, nil
}

func (r *BatchFileReader) readObjectContentsLocked(objectSpec, expectedType string, expectedSize int64, oversizeKey string) (string, bool, error) {
	if r.poison != nil {
		return "", false, r.poison
	}
	if r.closed {
		return "", false, fmt.Errorf("git cat-file batch reader is closed")
	}
	if _, err := fmt.Fprintf(r.stdin, "contents %s\n", objectSpec); err != nil {
		return "", false, r.poisonLocked(fmt.Errorf("write git cat-file batch-command contents request: %w", err))
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return "", false, r.poisonLocked(fmt.Errorf("read git cat-file batch-command contents header: %w", err))
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") {
		if header != objectSpec+" missing" {
			return "", false, r.poisonLocked(fmt.Errorf("unexpected git cat-file batch-command contents missing header %q", header))
		}
		return "", false, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", false, r.poisonLocked(fmt.Errorf("unexpected git cat-file batch-command contents header %q", header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return "", false, r.poisonLocked(fmt.Errorf("parse git cat-file size %q", fields[2]))
	}
	if fields[0] != objectSpec || fields[1] != expectedType || size != expectedSize {
		// info and contents share one process, so their resolved identity, type and
		// size must agree. Do not drain an unexpected body merely to salvage the
		// protocol: that is exactly the unbounded non-blob read this gate prevents.
		return "", false, r.poisonLocked(fmt.Errorf("git cat-file batch-command metadata changed between info and contents: info=%s %s %d contents=%s %s %d",
			objectSpec, expectedType, expectedSize, fields[0], fields[1], size))
	}
	if fields[1] != "blob" {
		return "", false, r.poisonLocked(fmt.Errorf("git cat-file batch-command returned non-blob contents after blob info: %s", fields[1]))
	}
	if r.maxBytes > 0 && size > r.maxBytes {
		var src io.Reader = io.LimitReader(r.stdout, size)
		prefix := make([]byte, 0, oversizeBlobPrefixCap)
		tee := io.Writer(prefixCaptureWriter{buf: &prefix})
		if scan := r.oversizeScan; scan != nil {
			// The same single pass the digest already makes: the scanner sees the bytes on their
			// way to being discarded, so relevance costs no extra read and no retained memory.
			tee = io.MultiWriter(tee, oversizeScanWriter{path: oversizeKey, scan: scan})
		}
		src = io.TeeReader(src, tee)
		digest, err := filedigest.Stream(src)
		if err != nil {
			return "", false, r.poisonLocked(fmt.Errorf("stream oversized git blob: %w", err))
		}
		if digest.Bytes != size {
			return "", false, r.poisonLocked(fmt.Errorf("git cat-file blob body length %d, want %d", digest.Bytes, size))
		}
		trailing, err := r.stdout.ReadByte()
		if err != nil {
			return "", false, r.poisonLocked(fmt.Errorf("read git cat-file blob separator: %w", err))
		}
		if trailing != '\n' {
			return "", false, r.poisonLocked(fmt.Errorf("git cat-file blob missing trailing newline separator"))
		}
		if r.oversize == nil {
			r.oversize = map[string]OversizeBlob{}
		}
		r.oversize[oversizeKey] = OversizeBlob{Bytes: digest.Bytes, Hash: digest.Hash, Lines: digest.Lines, Prefix: string(prefix)}
		return "", false, nil
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, content); err != nil {
		return "", false, r.poisonLocked(fmt.Errorf("read git cat-file blob body: %w", err))
	}
	trailing, err := r.stdout.ReadByte()
	if err != nil {
		return "", false, r.poisonLocked(fmt.Errorf("read git cat-file blob separator: %w", err))
	}
	if trailing != '\n' {
		return "", false, r.poisonLocked(fmt.Errorf("git cat-file blob missing trailing newline separator"))
	}
	return string(content), true, nil
}

// poisonLocked retires a batch-command session whose request/response boundary
// is no longer trustworthy. It preserves the first protocol error as the
// reader's stable result; later reads never write into a dead or desynchronized
// stream, and Close reaps the killed child while returning this original cause.
// r.mu must be held.
func (r *BatchFileReader) poisonLocked(err error) error {
	if r.poison != nil {
		return r.poison
	}
	r.poison = err
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	return err
}

func (r *BatchFileReader) Close() error {
	r.mu.Lock()
	if r.closed {
		poison := r.poison
		r.mu.Unlock()
		return poison
	}
	r.closed = true
	stdin := r.stdin
	poison := r.poison
	r.mu.Unlock()
	var closeErrors []error
	if err := stdin.Close(); err != nil {
		closeErrors = append(closeErrors, err)
	}
	if err := r.cmd.Wait(); err != nil {
		if poison != nil {
			// poisonLocked deliberately killed this child. The protocol cause is
			// actionable; "signal: killed" is only the cleanup mechanism.
			return poison
		}
		msg := strings.TrimSpace(r.stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		closeErrors = append(closeErrors, fmt.Errorf("git cat-file --batch-command: %s", msg))
	}
	if poison != nil {
		return poison
	}
	return errors.Join(closeErrors...)
}

// LimitedFileReader performs repeated bounded reads from one immutable raw Git
// tree without spawning probes per file. Replace refs are ignored. Prime
// batches exact ls-tree metadata; ReadFile then asks a persistent content batch
// for the exact blob OIDs at or below the ceiling. Repository paths never enter
// that line-oriented protocol,
// so newline-bearing and trailing-carriage-return names cannot split or
// normalize a request. An unprimed path resolves the same metadata lazily.
type LimitedFileReader struct {
	ctx      context.Context
	repo     string
	rev      string
	maxBytes int64

	mu      sync.Mutex
	content *BatchFileReader
	primed  map[string]primedLimitedFile
	closed  bool

	componentMetadata          map[treeComponentMetadataKey]treeComponentMetadataResult
	componentMetadataProcesses int
	componentMetadataLookup    treeMetadataLookup
	deadline                   time.Time
	now                        func() time.Time
}

type primedLimitedFile struct {
	result     LimitedFileResult
	objectID   string
	objectType string
}

const (
	treeMetadataLiteralPrefix = ":(top,literal)"
	// ls-tree formats mode, type, object ID, and decimal size (or BAD) before
	// each path. SHA-256 object IDs need at most 64 bytes; 128 bytes leaves
	// generous room for every fixed field while putting a hard bound on a
	// malformed record before its path is retained.
	treeMetadataRecordOverheadMax = 128
)

// Normal paths use count- and argv-bounded metadata batches. Only a path too
// large for those batches enters component traversal. 256 leaves ample room
// for the existing 80-component compatibility case while bounding one
// adversarial reader (and therefore both sides of an Analyze call)
// independently of path depth/count.
const limitedFileComponentMetadataProcessLimit = 256

type treeComponentMetadataKey struct {
	treeOID   string
	component string
}

type treeComponentMetadataResult struct {
	entry primedLimitedFile
	found bool
}

type treeMetadataLookup func(
	context.Context,
	string,
	string,
	[]string,
) (map[string]primedLimitedFile, error)

// NewLimitedFileReader creates a lazy reader. No Git process starts until Prime
// or ReadFile, so an empty or extension-filtered range costs no subprocesses.
// rev must be immutable when metadata and content consistency matters.
func NewLimitedFileReader(ctx context.Context, repo, rev string, maxBytes int64) *LimitedFileReader {
	return &LimitedFileReader{
		ctx:                     ctx,
		repo:                    repo,
		rev:                     rev,
		maxBytes:                maxBytes,
		componentMetadataLookup: treeEntryMetadataBatch,
		now:                     time.Now,
	}
}

// SetDeadline bounds the component-by-component metadata traversal, and also
// stops Prime from starting further metadata batches once elapsed (checked
// between batches, not mid-batch: one already-started batch still runs to
// completion). A zero deadline restores the historical no-deadline behavior.
func (r *LimitedFileReader) SetDeadline(deadline time.Time) {
	r.mu.Lock()
	r.deadline = deadline
	r.mu.Unlock()
}

// Prime resolves exact tree-entry metadata for a bounded group of paths. Unlike
// cat-file, ls-tree identifies a gitlink from its tree mode even when the
// referenced commit object is absent from the superproject object database.
// Literal pathspec batches keep unusual names inert and bound argv size.
func (r *LimitedFileReader) Prime(paths []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("limited Git file reader is closed")
	}
	if len(paths) == 0 {
		return nil
	}
	if r.primed == nil {
		r.primed = make(map[string]primedLimitedFile, len(paths))
	}
	for start := 0; start < len(paths); {
		// An unbounded candidate list otherwise runs every batch to completion
		// before the caller's overBudget check ever sees it: SetDeadline only
		// bounded the exceptional component-by-component path above. Once the
		// deadline elapses, stop starting new metadata subprocesses and mark
		// every remaining path unaddressable instead, exactly like the
		// component traversal's own deadline refusal.
		if r.deadlineReached() {
			for _, path := range paths[start:] {
				if _, already := r.primed[path]; !already {
					r.primed[path] = unaddressableLimitedFile()
				}
			}
			return nil
		}
		// A rare over-limit full path is resolved one tree component at a time.
		// No command receives the whole path, while the final exact blob OID can
		// still use the persistent content batch.
		if len(treeMetadataLiteralPrefix)+len(paths[start]) > literalPathspecBatchBytes {
			entry, found, err := r.treeEntryMetadataByComponents(paths[start])
			if err != nil {
				return err
			}
			if !found {
				entry = primedLimitedFile{result: LimitedFileResult{Status: LimitedFileMissing}}
			}
			r.primed[paths[start]] = entry
			start++
			continue
		}
		end := treeMetadataBatchEnd(paths, start)
		entries, err := treeEntryMetadataBatch(r.ctx, r.repo, r.rev, paths[start:end])
		if err != nil {
			return err
		}
		for _, path := range paths[start:end] {
			r.primed[path] = primedLimitedFile{result: LimitedFileResult{Status: LimitedFileMissing}}
		}
		for path, entry := range entries {
			r.primed[path] = entry
		}
		start = end
	}
	return nil
}

func treeEntryMetadata(ctx context.Context, repo, rev, path string) (primedLimitedFile, bool, error) {
	reader := NewLimitedFileReader(ctx, repo, rev, 0)
	return reader.treeEntryMetadata(path)
}

func (r *LimitedFileReader) treeEntryMetadata(path string) (primedLimitedFile, bool, error) {
	if len(treeMetadataLiteralPrefix)+len(path) > literalPathspecBatchBytes {
		return r.treeEntryMetadataByComponents(path)
	}
	entries, err := treeEntryMetadataBatch(r.ctx, r.repo, r.rev, []string{path})
	if err != nil {
		return primedLimitedFile{}, false, err
	}
	entry, found := entries[path]
	return entry, found, nil
}

// treeEntryMetadataByComponents resolves a deep path without ever placing the
// full repository-controlled string in argv. Each intermediate entry must be a
// tree; its immutable OID becomes the root of the next exact one-component
// lookup. Resolved (tree OID, component) pairs are cached across paths, and
// uncached lookups stop at the reader's process allowance or deadline.
func (r *LimitedFileReader) treeEntryMetadataByComponents(path string) (primedLimitedFile, bool, error) {
	if err := validateLimitedFilePath(path); err != nil {
		return primedLimitedFile{}, false, err
	}
	components := strings.Split(path, "/")
	currentTree := r.rev
	for i, component := range components {
		if len(treeMetadataLiteralPrefix)+len(component) > literalPathspecBatchBytes {
			return unaddressableLimitedFile(), true, nil
		}
		metadata, addressable, err := r.componentEntryMetadata(currentTree, component)
		if err != nil {
			return primedLimitedFile{}, false, err
		}
		if !addressable {
			return unaddressableLimitedFile(), true, nil
		}
		if !metadata.found {
			return primedLimitedFile{}, false, nil
		}
		if i == len(components)-1 {
			return metadata.entry, true, nil
		}
		if metadata.entry.objectType != "tree" {
			return primedLimitedFile{}, false, nil
		}
		currentTree = metadata.entry.objectID
	}
	return primedLimitedFile{}, false, nil
}

func (r *LimitedFileReader) componentEntryMetadata(
	treeOID, component string,
) (treeComponentMetadataResult, bool, error) {
	key := treeComponentMetadataKey{treeOID: treeOID, component: component}
	if metadata, ok := r.componentMetadata[key]; ok {
		return metadata, true, nil
	}
	if r.componentMetadataProcesses >= limitedFileComponentMetadataProcessLimit || r.deadlineReached() {
		return treeComponentMetadataResult{}, false, nil
	}
	r.componentMetadataProcesses++
	entries, err := r.componentMetadataLookup(r.ctx, r.repo, treeOID, []string{component})
	if err != nil {
		return treeComponentMetadataResult{}, false, err
	}
	entry, found := entries[component]
	metadata := treeComponentMetadataResult{entry: entry, found: found}
	if r.componentMetadata == nil {
		r.componentMetadata = make(map[treeComponentMetadataKey]treeComponentMetadataResult)
	}
	r.componentMetadata[key] = metadata
	return metadata, true, nil
}

func (r *LimitedFileReader) deadlineReached() bool {
	if r.deadline.IsZero() {
		return false
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	return !now().Before(r.deadline)
}

func unaddressableLimitedFile() primedLimitedFile {
	return primedLimitedFile{result: LimitedFileResult{Status: LimitedFileUnaddressable}}
}

// treeMetadataBatchEnd keeps each ls-tree invocation prefix-free as well as
// count- and argv-bounded. Combining an ancestor tree path with one of its
// descendants makes ls-tree recursively expand the ancestor; splitting them
// preserves the command's one-record-per-exact-path memory bound.
func treeMetadataBatchEnd(paths []string, start int) int {
	end := start
	pathspecBytes := 0
	for end < len(paths) && end-start < literalPathspecBatchCount {
		next := paths[end]
		nextBytes := len(treeMetadataLiteralPrefix) + len(next)
		if end > start && pathspecBytes+nextBytes > literalPathspecBatchBytes {
			break
		}
		conflicts := false
		for _, batched := range paths[start:end] {
			if treePathsOverlap(batched, next) {
				conflicts = true
				break
			}
		}
		if conflicts {
			break
		}
		pathspecBytes += nextBytes
		end++
	}
	return end
}

func treePathsOverlap(left, right string) bool {
	return left == right ||
		strings.HasPrefix(left, right+"/") ||
		strings.HasPrefix(right, left+"/")
}

func treeEntryMetadataBatch(ctx context.Context, repo, rev string, paths []string) (map[string]primedLimitedFile, error) {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.ContainsRune(rev, 0) {
		return nil, fmt.Errorf("invalid treeish %q", rev)
	}
	if len(paths) > literalPathspecBatchCount {
		return nil, fmt.Errorf("git tree metadata input exceeds %d paths", literalPathspecBatchCount)
	}
	if len(paths) == 0 {
		return map[string]primedLimitedFile{}, nil
	}
	args := []string{"ls-tree", "-z", "-l", "--full-name", rev, "--"}
	known := make(map[string]struct{}, len(paths))
	pathspecBytes := 0
	expectedOutputBytes := 0
	for _, path := range paths {
		if err := validateLimitedFilePath(path); err != nil {
			return nil, err
		}
		pathspecBytes += len(treeMetadataLiteralPrefix) + len(path)
		if pathspecBytes > literalPathspecBatchBytes {
			return nil, fmt.Errorf("git tree metadata pathspecs exceed %d bytes", literalPathspecBatchBytes)
		}
		args = append(args, treeMetadataLiteralPrefix+path)
		known[path] = struct{}{}
		expectedOutputBytes += len(path) + treeMetadataRecordOverheadMax
	}
	cmd := newCmd(ctx, repo, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	launch, err := preparePathOutputCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree metadata: %w", err)
	}
	defer launch.close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree metadata pipe: %w", err)
	}
	job, err := launch.start(cmd)
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git ls-tree metadata: %s", message)
	}
	defer job.close()

	entries := make(map[string]primedLimitedFile, len(paths))
	outputCount := 0
	outputBytes := 0
	reader := bufio.NewReaderSize(stdout, literalPathspecBatchBytes+treeMetadataRecordOverheadMax)
	for {
		record, readErr := reader.ReadSlice(0)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf(
				"git ls-tree returned a metadata record longer than %d bytes",
				literalPathspecBatchBytes+treeMetadataRecordOverheadMax,
			)
		}
		if len(record) == 0 && errors.Is(readErr, io.EOF) {
			waitErr := cmd.Wait()
			if waitErr != nil {
				message := strings.TrimSpace(stderr.String())
				if message == "" {
					message = waitErr.Error()
				}
				return nil, fmt.Errorf("git ls-tree metadata: %s", message)
			}
			return entries, nil
		}
		if len(record) == 0 || record[len(record)-1] != 0 {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, errors.New("git ls-tree returned non-NUL-terminated metadata")
		}
		record = record[:len(record)-1]
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, errors.New("git ls-tree returned malformed metadata")
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 4 {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("git ls-tree returned malformed metadata header %q", record[:tab])
		}
		path := string(record[tab+1:])
		if _, ok := known[path]; !ok {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("git ls-tree returned unexpected path %q", path)
		}
		if _, duplicate := entries[path]; duplicate {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("git ls-tree returned duplicate path %q", path)
		}
		outputCount++
		outputBytes += len(record) + 1
		if outputCount > len(known) || outputCount > literalPathspecBatchCount {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("git ls-tree returned more than %d metadata records", len(known))
		}
		if outputBytes > expectedOutputBytes {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("git ls-tree returned more than %d metadata bytes", expectedOutputBytes)
		}
		if fields[1] != "blob" {
			entries[path] = primedLimitedFile{
				result:     LimitedFileResult{Status: LimitedFileNonBlob},
				objectID:   fields[2],
				objectType: fields[1],
			}
			continue
		}
		// With -l, Git uses the exact sentinel BAD when a listed blob object
		// cannot be inspected (for example, a tree references a missing object).
		// The tree entry is still authoritative and recoverable as one unreadable
		// file. Keep every other unexpected size a hard protocol error.
		if fields[3] == "BAD" {
			entries[path] = primedLimitedFile{
				result:     LimitedFileResult{Status: LimitedFileUnreadable},
				objectID:   fields[2],
				objectType: fields[1],
			}
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("parse git ls-tree blob size %q for %q", fields[3], path)
		}
		entries[path] = primedLimitedFile{
			result:     LimitedFileResult{Status: LimitedFileContent, Bytes: size},
			objectID:   fields[2],
			objectType: fields[1],
		}
		if readErr != nil {
			stopPathOutputCommand(cmd, stdout, job)
			return nil, fmt.Errorf("read git ls-tree metadata: %w", readErr)
		}
	}
}

// ReadFile returns one typed bounded result. Oversized blobs are answered from
// metadata and are never sent to the content batch.
func (r *LimitedFileReader) ReadFile(path string) (LimitedFileResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return LimitedFileResult{}, errors.New("limited Git file reader is closed")
	}
	entry, primed := r.primed[path]
	if !primed {
		var found bool
		var err error
		entry, found, err = r.treeEntryMetadata(path)
		if err != nil {
			return LimitedFileResult{}, err
		}
		if !found {
			entry = primedLimitedFile{result: LimitedFileResult{Status: LimitedFileMissing}}
		}
		if r.primed == nil {
			r.primed = make(map[string]primedLimitedFile)
		}
		r.primed[path] = entry
	}
	result := entry.result
	if result.Status != LimitedFileContent {
		return result, nil
	}
	if r.maxBytes > 0 && result.Bytes > r.maxBytes {
		result.Status = LimitedFileOversize
		return result, nil
	}
	if r.content == nil {
		content, err := NewBatchFileReader(r.ctx, r.repo, r.rev)
		if err != nil {
			return LimitedFileResult{}, err
		}
		content.SetMaxBytes(r.maxBytes)
		r.content = content
	}
	content, ok, err := r.content.readKnownBlobObjectID(entry.objectID, path)
	if err != nil {
		return LimitedFileResult{}, err
	}
	if !ok {
		if oversize, exists := r.content.OversizeBlob(path); exists {
			return LimitedFileResult{Status: LimitedFileOversize, Bytes: oversize.Bytes}, nil
		}
		// Metadata already established an exact blob entry. A missing exact OID
		// therefore means its object became unavailable, not that the tree path
		// was absent.
		return LimitedFileResult{Status: LimitedFileUnreadable}, nil
	}
	result.Content = content
	result.Bytes = int64(len(content))
	return result, nil
}

// Close stops any lazy batches that were started.
func (r *LimitedFileReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	content := r.content
	r.mu.Unlock()
	if content != nil {
		return content.Close()
	}
	return nil
}

func RemoteURLs(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return nil, err
	}
	origin := ""
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := fields[0]
		url := fields[1]
		if key == "remote.origin.url" {
			origin = url
			continue
		}
		urls = append(urls, url)
	}
	if origin != "" {
		urls = append([]string{origin}, urls...)
	}
	return urls, nil
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	stdout, stderr, err := runWithStderr(ctx, dir, name, args...)
	if err != nil {
		msg := stderr
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout, nil
}

const (
	rawGitObjectsEnv    = "GIT_NO_REPLACE_OBJECTS=1"
	noLazyFetchEnv      = "GIT_NO_LAZY_FETCH=1"
	// noTransportProtocolEnv is a second, independent no-egress guard.
	// GIT_NO_LAZY_FETCH only became effective in Git 2.45 (git/git@2c206fc);
	// this package's floor is Git 2.36 (cat-file --batch-command), so on any
	// Git between those two versions GIT_NO_LAZY_FETCH is an unrecognized,
	// silently ignored variable and a partial clone's promisor remote would
	// still be lazily fetched with no guard at all. GIT_ALLOW_PROTOCOL has
	// gated every transport since Git 2.11.1; set to empty it whitelists zero
	// protocols, so any fetch a lazy-fetch would need to make (over http,
	// https, ssh, or git) fails hard instead of being silently skipped like
	// GIT_NO_LAZY_FETCH's own guard. This tool never intentionally fetches,
	// clones, or pushes, so blocking every transport costs it nothing.
	noTransportProtocolEnv = "GIT_ALLOW_PROTOCOL="
	gitCommandWaitDelay    = time.Second
)

// newGitCmdWithCallerLocale builds the two git-grep subprocesses whose
// case-folding must retain the caller's locale. Like every other production Git
// subprocess in this package, it disables replace refs: exact tree/object IDs
// are the immutable snapshot boundary, regardless of later refs/replace edits.
// It also disables lazy fetching, for the same reason newCmd does below: this
// provider promises no-egress execution, and without this a partial clone
// would have Git silently reach out to the promisor remote for any object a
// command here touches.
func newGitCmdWithCallerLocale(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Bound Wait when Git exits but a child process inherited one of the pipes.
	// This matters especially on Windows, where killing Git does not necessarily
	// close an orphan-held stderr handle promptly.
	cmd.WaitDelay = gitCommandWaitDelay
	// Cmd.Environ observes Dir and updates PWD accordingly. Append after the
	// inherited environment so a hostile GIT_NO_REPLACE_OBJECTS=0 is overridden.
	cmd.Env = append(cmd.Environ(), rawGitObjectsEnv, noLazyFetchEnv, noTransportProtocolEnv)
	return cmd
}

// newCmd builds the exec.Cmd used by subprocesses whose diagnostics must be
// stable. It pins the subprocess locale to C (LC_ALL=C overrides LANG and any
// LC_*; LANG=C is set as a belt-and-braces default) so git's stderr messages
// are always the English ones our error classification matches — e.g.
// ShowFile's absent-file detection would otherwise break under a non-English
// git locale. It also disables replace refs for every Git command, so an exact
// tree or object ID keeps raw-object semantics across separate subprocesses.
// The git-grep paths above preserve caller locale while applying the same raw
// object rule.
//
// It also disables lazy fetching for every Git command: this provider
// advertises no-egress execution, but a partial clone with a promisor remote
// configured will otherwise have Git silently contact that remote to fill in
// any object a command here asks about (ls-tree -l's per-blob size lookup is
// exactly such a command). With lazy fetch disabled, Git reports a missing
// promised object as a per-entry failure instead of fetching it — ls-tree -l
// prints its "BAD" sentinel for a blob it cannot size, which callers already
// classify as LimitedFileUnreadable rather than treating as an error. The
// GIT_ALLOW_PROTOCOL guard alongside it closes the same gap on a Git older
// than the lazy-fetch guard itself (see noTransportProtocolEnv).
func newCmd(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Bound Wait after cancellation or process exit when a descendant retains a
	// subprocess pipe. All production Git commands are constructed here or by
	// newGitCmdWithCallerLocale, so malformed-output retirement cannot hang.
	cmd.WaitDelay = gitCommandWaitDelay
	// Cmd.Environ observes Dir and updates PWD accordingly. Starting from
	// os.Environ would leave child processes with the parent's stale PWD.
	env := cmd.Environ()
	if name == "git" {
		env = append(env, rawGitObjectsEnv, noLazyFetchEnv, noTransportProtocolEnv)
	}
	cmd.Env = append(env, "LC_ALL=C", "LANG=C")
	return cmd
}

// runWithStderr runs a command and returns its stdout and trimmed stderr
// separately, so callers can classify failures against git's own message
// without the wrapped error text (which echoes the argv, including paths).
func runWithStderr(ctx context.Context, dir, name string, args ...string) (string, string, error) {
	cmd := newCmd(ctx, dir, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), strings.TrimSpace(stderr.String()), err
}

// oversizeScanWriter adapts a chunk callback to io.Writer so it can sit in the TeeReader on the
// oversize path. Write must not retain p, and the callback is documented not to.
type oversizeScanWriter struct {
	path string
	scan func(path string, chunk []byte)
}

func (w oversizeScanWriter) Write(p []byte) (int, error) {
	w.scan(w.path, p)
	return len(p), nil
}
