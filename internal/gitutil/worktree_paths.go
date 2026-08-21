package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const (
	literalPathspecBatchCount = 128
	// Windows CreateProcess limits the complete command line to 32,767 UTF-16
	// code units. Go quoting can nearly double a path argument, so 15 KiB plus
	// two quote characters per path leaves over 1 KiB for Git's executable,
	// fixed flags, and a commit ID without requiring stdin pathspec support.
	literalPathspecBatchBytes        = 15 << 10
	literalPathOutputMaxPathBytes    = 4096
	nestedIgnorePathMaxBytes         = 4096
	nestedIgnoreCandidateMaxCount    = 512
	nestedIgnoreCandidateMaxAllBytes = nestedIgnoreCandidateMaxCount * nestedIgnorePathMaxBytes
)

// TreeContainsPaths returns the exact file-like members of paths present in
// treeish. That includes blobs and gitlinks, which the provider's recursive
// tree listing also carries, but not directory tree entries. Literal pathspecs
// keep metacharacters inert, and non-recursive ls-tree emits at most one record
// per requested path instead of expanding directory names. Paths are resolved
// relative to repo, matching ListFiles and the provider when --repo names a
// repository subdirectory.
func TreeContainsPaths(ctx context.Context, repo, treeish string, paths []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for start := 0; start < len(paths); {
		end := literalPathspecBatchEnd(paths, start)
		batch, err := treeBlobMembersBatch(ctx, repo, treeish, paths[start:end])
		if err != nil {
			return nil, err
		}
		for path := range batch {
			result[path] = struct{}{}
		}
		start = end
	}
	return result, nil
}

func treeBlobMembersBatch(
	ctx context.Context,
	repo, treeish string,
	paths []string,
) (map[string]struct{}, error) {
	if treeish == "" || strings.HasPrefix(treeish, "-") || strings.ContainsRune(treeish, 0) {
		return nil, fmt.Errorf("invalid treeish %q", treeish)
	}
	args := []string{"ls-tree", "-z", treeish, "--"}
	known := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		args = append(args, ":(literal)"+path)
		known[path] = struct{}{}
	}
	cmd := newCmd(ctx, repo, "git", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git ls-tree literal membership probe: %s", message)
	}
	data := stdout.Bytes()
	if len(data) > 0 && data[len(data)-1] != 0 {
		return nil, errors.New("git ls-tree returned malformed membership output")
	}
	result := make(map[string]struct{})
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, errors.New("git ls-tree returned a malformed membership record")
		}
		header := strings.Fields(string(record[:tab]))
		if len(header) != 3 {
			return nil, errors.New("git ls-tree returned a malformed membership header")
		}
		member := string(record[tab+1:])
		if _, ok := known[member]; !ok {
			return nil, fmt.Errorf("git ls-tree returned unexpected path %q", member)
		}
		if header[1] == "blob" || header[1] == "commit" {
			result[member] = struct{}{}
		}
	}
	return result, nil
}

// FirstTreeNestedIgnorePaths returns the first limit nested .gitignore paths
// in the same cwd-relative Git tree order consumed by the provider. Git
// ls-tree cannot filter this suffix server-side, so when a tree has fewer than
// limit nested ignore files the child necessarily emits every tree path. That
// output is consumed one bounded record at a time and never accumulated; the
// child is killed as soon as limit candidates are retained. Retained memory is
// separately bounded by candidate count, per-path bytes, and aggregate bytes.
func FirstTreeNestedIgnorePaths(ctx context.Context, repo, treeish string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	if err := validateNestedIgnoreLimit(limit); err != nil {
		return nil, err
	}
	args := []string{
		"ls-tree", "-r", "-z", "--name-only", treeish,
	}
	return firstNestedIgnorePaths(ctx, repo, args, limit, nil)
}

// BoundedTreeNestedIgnorePaths returns every nested .gitignore path when their
// count fits limit and reports an error on the first path beyond it. Unlike the
// First variant, reaching limit is not success: callers use this form when
// silently omitting a policy file would change the answer.
func BoundedTreeNestedIgnorePaths(ctx context.Context, repo, treeish string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	if err := validateNestedIgnoreLimit(limit); err != nil {
		return nil, err
	}
	args := []string{"ls-tree", "-r", "-z", "--name-only", treeish}
	return boundedNestedIgnorePaths(ctx, repo, args, limit, nil, nil)
}

// FirstWorktreeNestedIgnorePaths returns the first limit nested .gitignore
// paths in provider order: tracked/unignored paths first, then ignored paths
// admitted by includeIgnored. Both Git streams are filtered to .gitignore and
// stopped once the bounded result is complete.
func FirstWorktreeNestedIgnorePaths(
	ctx context.Context,
	repo string,
	limit int,
	includeIgnored func(string) bool,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	if err := validateNestedIgnoreLimit(limit); err != nil {
		return nil, err
	}
	eligibleArgs := []string{
		"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--",
		":(glob)**/.gitignore",
	}
	paths, err := firstNestedIgnorePaths(ctx, repo, eligibleArgs, limit, nil)
	if err != nil || len(paths) >= limit || includeIgnored == nil {
		return paths, err
	}
	ignoredArgs := []string{
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--",
		":(glob)**/.gitignore",
	}
	ignored, err := firstNestedIgnorePaths(ctx, repo, ignoredArgs, limit-len(paths), includeIgnored)
	if err != nil {
		return nil, err
	}
	return append(paths, ignored...), nil
}

// BoundedWorktreeNestedIgnorePaths is the policy-complete counterpart to
// FirstWorktreeNestedIgnorePaths: it scans until EOF or one admitted path beyond
// limit, including the optional ignored stream, so a cap cannot become a silent
// skip.
func BoundedWorktreeNestedIgnorePaths(
	ctx context.Context,
	repo string,
	limit int,
	includeIgnored func(string) bool,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	if err := validateNestedIgnoreLimit(limit); err != nil {
		return nil, err
	}
	eligibleArgs := []string{
		"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--",
		":(glob)**/.gitignore",
	}
	paths, err := boundedNestedIgnorePaths(ctx, repo, eligibleArgs, limit, nil, nil)
	if err != nil || includeIgnored == nil {
		return paths, err
	}
	ignoredArgs := []string{
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--",
		":(glob)**/.gitignore",
	}
	return boundedNestedIgnorePaths(ctx, repo, ignoredArgs, limit, includeIgnored, paths)
}

// VisitWorktreePaths streams Git's provider candidate listing one NUL-safe path
// at a time. When ignored is false it matches ListWorktreeFiles; when true it
// matches ListIgnoredWorktreeFiles. Returning false stops the subprocess
// successfully. The reader bounds each record and never buffers complete Git
// output, so callers can enforce their own retained-count and aggregate limits.
func VisitWorktreePaths(
	ctx context.Context,
	repo string,
	ignored bool,
	visit func(string) bool,
) error {
	if visit == nil {
		return errors.New("git worktree path visitor is nil")
	}
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if ignored {
		args = append(args, "--ignored")
	} else {
		args = append(args, "--cached")
	}
	return visitBoundedNULPaths(newCmd(ctx, repo, "git", args...), visit)
}

func firstNestedIgnorePaths(
	ctx context.Context,
	repo string,
	args []string,
	limit int,
	include func(string) bool,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	if err := validateNestedIgnoreLimit(limit); err != nil {
		return nil, err
	}
	paths := make([]string, 0, limit)
	retainedBytes := 0
	retainedLimitExceeded := false
	cmd := newCmd(ctx, repo, "git", args...)
	err := visitBoundedNULPaths(cmd, func(path string) bool {
		if !strings.Contains(path, "/") || !strings.HasSuffix(path, "/.gitignore") {
			return true
		}
		if include != nil && !include(path) {
			return true
		}
		if len(paths) >= nestedIgnoreCandidateMaxCount ||
			len(path) > nestedIgnoreCandidateMaxAllBytes-retainedBytes {
			retainedLimitExceeded = true
			return false
		}
		paths = append(paths, path)
		retainedBytes += len(path)
		return len(paths) < limit
	})
	if err != nil {
		return nil, err
	}
	if retainedLimitExceeded {
		return nil, fmt.Errorf(
			"git nested-ignore candidates exceed %d paths or %d aggregate bytes",
			nestedIgnoreCandidateMaxCount,
			nestedIgnoreCandidateMaxAllBytes,
		)
	}
	return paths, nil
}

func boundedNestedIgnorePaths(
	ctx context.Context,
	repo string,
	args []string,
	limit int,
	include func(string) bool,
	paths []string,
) ([]string, error) {
	if len(paths) > limit || len(paths) > nestedIgnoreCandidateMaxCount {
		return nil, fmt.Errorf("git nested-ignore candidates exceed %d paths", limit)
	}
	retainedBytes := 0
	// An unmerged index emits the same pathname once per stage. Keep Git's
	// first-seen byte order, but charge each exact candidate only once across
	// both the eligible and optional ignored streams.
	seen := make(map[string]struct{}, len(paths))
	for _, retained := range paths {
		seen[retained] = struct{}{}
		retainedBytes += len(retained)
	}
	if retainedBytes > nestedIgnoreCandidateMaxAllBytes {
		return nil, fmt.Errorf(
			"git nested-ignore candidates exceed %d aggregate bytes",
			nestedIgnoreCandidateMaxAllBytes,
		)
	}
	exceeded := false
	cmd := newCmd(ctx, repo, "git", args...)
	err := visitBoundedNULPaths(cmd, func(candidate string) bool {
		if !strings.Contains(candidate, "/") || !strings.HasSuffix(candidate, "/.gitignore") {
			return true
		}
		if include != nil && !include(candidate) {
			return true
		}
		if _, duplicate := seen[candidate]; duplicate {
			return true
		}
		if len(paths) >= limit || len(paths) >= nestedIgnoreCandidateMaxCount ||
			len(candidate) > nestedIgnoreCandidateMaxAllBytes-retainedBytes {
			exceeded = true
			return false
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
		retainedBytes += len(candidate)
		return true
	})
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, fmt.Errorf(
			"git nested-ignore candidates exceed %d paths or %d aggregate bytes",
			limit,
			nestedIgnoreCandidateMaxAllBytes,
		)
	}
	return paths, nil
}

func validateNestedIgnoreLimit(limit int) error {
	if limit > nestedIgnoreCandidateMaxCount {
		return fmt.Errorf("git nested-ignore limit %d exceeds maximum %d", limit, nestedIgnoreCandidateMaxCount)
	}
	return nil
}

func visitBoundedNULPaths(cmd *exec.Cmd, visit func(string) bool) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stdout, nestedIgnorePathMaxBytes+1)
	for {
		record, readErr := reader.ReadSlice(0)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("git returned a path longer than %d bytes", nestedIgnorePathMaxBytes)
		}
		if len(record) > 0 {
			if record[len(record)-1] != 0 {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return errors.New("git returned a non-NUL-terminated path")
			}
			if !visit(string(record[:len(record)-1])) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return nil
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			stopPathOutputCommand(cmd)
			return readErr
		}
		waitErr := cmd.Wait()
		if waitErr != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = waitErr.Error()
			}
			return errors.New(message)
		}
		return nil
	}
}

// ClassifyWorktreePaths applies the same two Git listings used by the provider
// to a bounded exact path set. eligible contains tracked files and untracked
// files admitted by Git's effective excludes; ignored contains ignored
// untracked files that an explicit sem include rule may re-admit. A path in
// neither set (including Git-internal paths such as .git/config) is ineligible.
func ClassifyWorktreePaths(
	ctx context.Context,
	repo string,
	paths []string,
) (eligible, ignored map[string]struct{}, err error) {
	eligible, err = listLiteralWorktreePaths(ctx, repo, paths, false)
	if err != nil {
		return nil, nil, err
	}
	ignored, err = listLiteralWorktreePaths(ctx, repo, paths, true)
	if err != nil {
		return nil, nil, err
	}
	return eligible, ignored, nil
}

func listLiteralWorktreePaths(
	ctx context.Context,
	repo string,
	paths []string,
	ignored bool,
) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for start := 0; start < len(paths); {
		end := literalPathspecBatchEnd(paths, start)
		batch, err := listLiteralWorktreePathBatch(ctx, repo, paths[start:end], ignored)
		if err != nil {
			return nil, err
		}
		for path := range batch {
			result[path] = struct{}{}
		}
		start = end
	}
	return result, nil
}

func literalPathspecBatchEnd(paths []string, start int) int {
	end := start
	bytes := 0
	for end < len(paths) && end-start < literalPathspecBatchCount {
		pathspecBytes := len(paths[end]) + len(":(literal)")
		if end > start && bytes+pathspecBytes > literalPathspecBatchBytes {
			break
		}
		bytes += pathspecBytes
		end++
	}
	return end
}

func runBoundedPathOutput(
	cmd *exec.Cmd,
	known map[string]struct{},
) (map[string]struct{}, error) {
	// A literal pathspec naming a worktree file can still match descendants of
	// an index directory at the same path. Bound both the admitted input and
	// the streamed output, and stop Git at the first non-exact record.
	if len(known) > literalPathspecBatchCount {
		return nil, fmt.Errorf(
			"git literal path output input exceeds %d paths",
			literalPathspecBatchCount,
		)
	}
	expectedOutputBytes := 0
	for path := range known {
		if len(path) > literalPathOutputMaxPathBytes {
			return nil, fmt.Errorf(
				"git literal path output input path exceeds %d bytes",
				literalPathOutputMaxPathBytes,
			)
		}
		recordBytes := len(path) + 1
		if recordBytes > literalPathspecBatchBytes-expectedOutputBytes {
			return nil, fmt.Errorf(
				"git literal path output input exceeds %d aggregate bytes",
				literalPathspecBatchBytes,
			)
		}
		expectedOutputBytes += recordBytes
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}

	result := make(map[string]struct{}, len(known))
	outputCount := 0
	outputBytes := 0
	reader := bufio.NewReaderSize(stdout, literalPathOutputMaxPathBytes+1)
	for {
		record, readErr := reader.ReadSlice(0)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			stopPathOutputCommand(cmd)
			return nil, fmt.Errorf(
				"git returned a path longer than %d bytes",
				literalPathOutputMaxPathBytes,
			)
		}
		if len(record) > 0 {
			if record[len(record)-1] != 0 {
				stopPathOutputCommand(cmd)
				return nil, errors.New("git returned a non-NUL-terminated path")
			}
			if len(record) == 1 {
				stopPathOutputCommand(cmd)
				return nil, errors.New("git returned an empty path")
			}
			path := string(record[:len(record)-1])
			if _, ok := known[path]; !ok {
				stopPathOutputCommand(cmd)
				return nil, fmt.Errorf("git returned unexpected path %q", path)
			}
			if _, duplicate := result[path]; duplicate {
				stopPathOutputCommand(cmd)
				return nil, fmt.Errorf("git returned duplicate path %q", path)
			}
			outputCount++
			outputBytes += len(record)
			if outputCount > len(known) || outputCount > literalPathspecBatchCount {
				stopPathOutputCommand(cmd)
				return nil, fmt.Errorf(
					"git returned more than %d literal paths",
					len(known),
				)
			}
			if outputBytes > expectedOutputBytes || outputBytes > literalPathspecBatchBytes {
				stopPathOutputCommand(cmd)
				return nil, fmt.Errorf(
					"git returned more than %d aggregate path bytes",
					expectedOutputBytes,
				)
			}
			result[path] = struct{}{}
		}
		if readErr == nil {
			continue
		}
		waitErr := cmd.Wait()
		if !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if waitErr != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = waitErr.Error()
			}
			return nil, errors.New(message)
		}
		return result, nil
	}
}

func stopPathOutputCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func listLiteralWorktreePathBatch(
	ctx context.Context,
	repo string,
	paths []string,
	ignored bool,
) (map[string]struct{}, error) {
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if ignored {
		args = append(args, "--ignored")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	known := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		args = append(args, ":(literal)"+path)
		known[path] = struct{}{}
	}

	result, err := runBoundedPathOutput(newCmd(ctx, repo, "git", args...), known)
	if err != nil {
		return nil, fmt.Errorf("git ls-files literal worktree probe: %w", err)
	}
	return result, nil
}

// IndexHasFilesUnder reports whether rel names an index directory prefix with
// at least one tracked descendant. An exact indexed file named rel does not
// count: the provider's trackedDirSet marks directories that contain tracked
// files, not D/F-conflicting index files with the same spelling. The explicit
// literal pathspec is required: replay provenance is persisted input and
// characters such as '*', '[', and ':' must never broaden the probe. The first
// output byte proves a descendant match, at which point the process is stopped;
// a large tracked subtree is therefore neither enumerated to completion nor
// materialized in memory.
func IndexHasFilesUnder(ctx context.Context, repo, rel string) (bool, error) {
	dir := strings.TrimRight(rel, "/")
	if dir == "" {
		return false, nil
	}
	args := []string{
		"ls-files", "-z", "--cached", "--error-unmatch", "--",
		":(literal)" + dir + "/",
	}
	cmd := newCmd(ctx, repo, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("git ls-files literal index probe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("git ls-files literal index probe: %w", err)
	}
	var first [1]byte
	n, readErr := stdout.Read(first[:])
	if n > 0 {
		// No later output can change an existence answer from true to false.
		// Stopping here keeps the probe proportional to one match even when
		// rel contains a very large tracked subtree.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return true, nil
	}
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, fmt.Errorf("git ls-files literal index probe: %w", readErr)
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) && exitError.ExitCode() == 1 {
			return false, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return false, fmt.Errorf("git ls-files literal index probe: %s", message)
	}
	return false, nil
}
