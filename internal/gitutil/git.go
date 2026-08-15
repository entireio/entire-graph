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

func RepoRoot(ctx context.Context, cwd string) (string, error) {
	out, err := run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
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
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
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

func ShowFile(ctx context.Context, repo, rev, path string) (string, bool, error) {
	// Classify against git's stderr only, never the wrapped error that echoes
	// the argv (which includes rev+":"+path). Matching the full error text made
	// any real failure on a path containing a marker substring (e.g. "Path" in
	// src/PathHelper.go) look like a missing file, swallowing the error.
	// Peel the revision to a tree before resolving the path. Without the type
	// constraint, a missing full object ID or a blob object can produce the
	// same path-looking diagnostic as a genuinely absent file.
	objectSpec := rev + "^{tree}:" + path
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

// ShowFileLimited is ShowFile with a READ-SIDE ceiling: it never materializes
// more than maxBytes+1 of the blob, and reports a larger blob as unreadable.
//
// ShowFile buffers all of git's stdout before returning, so a caller that only
// wants small files could not express that: checking len(content) afterwards
// enforces the ceiling on the ANSWER while the allocation has already happened.
// Every other bounded read in this package refuses before materializing — the
// batch reader streams an oversized blob to io.Discard off its header size, and
// the on-disk reader reads through an io.LimitReader — so this closes the one
// path where the bound arrived too late.
//
// The over-limit blob is not drained: cancelling the context stops git instead,
// because the bytes were already refused and reading them to /dev/null is the
// same wait this ceiling exists to avoid.
func ShowFileLimited(ctx context.Context, repo, rev, path string, maxBytes int64) (string, bool, error) {
	if maxBytes <= 0 {
		return ShowFile(ctx, repo, rev, path)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Same object spec and the same stderr-only classification as ShowFile; see
	// there for why the revision is peeled to a tree first.
	objectSpec := rev + "^{tree}:" + path
	cmd := newCmd(ctx, repo, "git", "show", objectSpec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, fmt.Errorf("git show %s: %w", objectSpec, err)
	}
	content, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	oversized := int64(len(content)) > maxBytes
	if oversized {
		cancel()
	}
	waitErr := cmd.Wait()
	switch {
	case oversized:
		// Refused, not failed: an oversized blob is a file this caller cannot
		// quote, exactly like a missing one. waitErr here is the kill.
		return "", false, nil
	case readErr != nil:
		return "", false, fmt.Errorf("git show %s: %w", objectSpec, readErr)
	case waitErr != nil:
		message := strings.TrimSpace(stderr.String())
		if isMissingPathDiagnostic(message) {
			return "", false, nil
		}
		if message == "" {
			message = waitErr.Error()
		}
		return "", false, fmt.Errorf("git show %s: %s", objectSpec, message)
	}
	return string(content), true, nil
}

func isMissingPathDiagnostic(stderr string) bool {
	// ShowFile runs git under the C locale, so only classify Git's specific
	// missing-path diagnostics. Broad substring checks can match a bad revision
	// or an unrelated error merely because an argv value contains the phrase.
	return strings.HasPrefix(stderr, "fatal: path '") &&
		(strings.Contains(stderr, "' does not exist in '") ||
			strings.Contains(stderr, "' exists on disk, but not in '"))
}

// BatchFileReader reads blobs from one revision through a persistent
// `git cat-file --batch` process. It avoids spawning one git process per file
// while preserving HEAD-tree snapshot semantics.
type BatchFileReader struct {
	rev          string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Reader
	stderr       *bytes.Buffer
	mu           sync.Mutex
	closed       bool
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
	cmd := newCmd(ctx, repo, "git", "cat-file", "--batch")
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
		return nil, fmt.Errorf("git cat-file --batch: %w", err)
	}
	return &BatchFileReader{
		rev:    rev,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
		stderr: &stderr,
	}, nil
}

func (r *BatchFileReader) ReadFile(path string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", false, fmt.Errorf("git cat-file batch reader is closed")
	}
	if _, err := fmt.Fprintf(r.stdin, "%s:%s\n", r.rev, path); err != nil {
		return "", false, err
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		return "", false, fmt.Errorf("read git cat-file header: %w", err)
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") {
		return "", false, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", false, fmt.Errorf("unexpected git cat-file header %q", header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", false, fmt.Errorf("parse git cat-file size %q: %w", fields[2], err)
	}
	if fields[1] != "blob" {
		if _, err := io.CopyN(io.Discard, r.stdout, size+1); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	if r.maxBytes > 0 && size > r.maxBytes {
		var src io.Reader = io.LimitReader(r.stdout, size)
		if scan := r.oversizeScan; scan != nil {
			// The same single pass the digest already makes: the scanner sees the bytes on their
			// way to being discarded, so relevance costs no extra read and no retained memory.
			src = io.TeeReader(src, oversizeScanWriter{path: path, scan: scan})
		}
		digest, err := filedigest.Stream(src)
		if err != nil {
			return "", false, err
		}
		if _, err := io.CopyN(io.Discard, r.stdout, 1); err != nil {
			return "", false, err
		}
		if r.oversize == nil {
			r.oversize = map[string]OversizeBlob{}
		}
		r.oversize[path] = OversizeBlob{Bytes: digest.Bytes, Hash: digest.Hash, Lines: digest.Lines}
		return "", false, nil
	}
	content := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, content); err != nil {
		return "", false, err
	}
	trailing, err := r.stdout.ReadByte()
	if err != nil {
		return "", false, err
	}
	if trailing != '\n' {
		return "", false, fmt.Errorf("git cat-file blob missing trailing newline separator")
	}
	return string(content), true, nil
}

func (r *BatchFileReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	stdin := r.stdin
	r.mu.Unlock()
	if err := stdin.Close(); err != nil {
		return err
	}
	if err := r.cmd.Wait(); err != nil {
		msg := strings.TrimSpace(r.stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git cat-file --batch: %s", msg)
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

// newCmd builds the exec.Cmd used by subprocesses whose diagnostics must be
// stable. It pins the subprocess locale to C (LC_ALL=C overrides LANG and any
// LC_*; LANG=C is set as a belt-and-braces default) so git's stderr messages
// are always the English ones our error classification matches — e.g.
// ShowFile's absent-file detection would otherwise break under a non-English
// git locale. GrepIndexMatches intentionally bypasses this helper and keeps
// the caller's locale because git grep uses LC_CTYPE for case folding.
func newCmd(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Cmd.Environ observes Dir and updates PWD accordingly. Starting from
	// os.Environ would leave child processes with the parent's stale PWD.
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "LANG=C")
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
