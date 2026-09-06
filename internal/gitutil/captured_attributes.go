package gitutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CapturedAttributeReader supplies the already-observed bytes of a repository
// attribute file. A false result means that the file was absent from the
// observation; an error means that observation was incomplete and must not be
// treated as an absent policy file.
type CapturedAttributeReader func(path string) (content string, ok bool, err error)

// CapturedDiffAttribute is Git's effective value for the diff attribute. Value
// is exactly one of Git's check-attr values: unspecified, unset, set, or a
// custom diff driver name. Binary is derived from an explicit unset value or
// the original repository's diff.<driver>.binary configuration. Text records
// a forced text diff (`diff` set or a driver whose binary setting is false),
// which overrides Git's content sniff when the caller classifies captured
// bytes.
type CapturedDiffAttribute struct {
	Value  string
	Driver string
	Binary bool
	Text   bool
}

const (
	maxCapturedAttributeFiles = 1_000_000
	maxCapturedAttributeBytes = 64 << 20
)

var errCapturedAttributeOutputLimit = errors.New("Git attribute output exceeded its bound")

// CapturedDiffAttributes evaluates Git's diff attributes against a private
// worktree containing only captured .gitattributes files. Git metadata and
// repository-local configuration remain selected by repo, so custom driver
// binary settings retain normal Git semantics. trackedPaths and the callback
// use paths relative to the selected repository root. A subdirectory repo is
// rejected because its ancestor policy paths cannot be supplied in the
// callback's repo-relative coordinate system without silently changing scope.
//
// Every ancestor .gitattributes path is materialized, including an empty file
// for an unobserved/missing file. That empty placeholder is deliberate: Git
// otherwise falls back to the index when a worktree attribute file is absent,
// which would reintroduce mutable or unobserved policy into captured
// preselection. The callback is never asked for source files.
func CapturedDiffAttributes(
	ctx context.Context,
	repo string,
	trackedPaths []string,
	capturedRead CapturedAttributeReader,
) (map[string]CapturedDiffAttribute, error) {
	if capturedRead == nil {
		return nil, errors.New("captured attribute reader is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := RepoRoot(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("resolve Git repository root: %w", err)
	}
	prefix, err := RepoPrefix(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("resolve Git repository prefix: %w", err)
	}
	if prefix != "" {
		return nil, fmt.Errorf("captured Git attributes require repo root; selected subdirectory %q is unsupported", prefix)
	}

	rootPaths := make([]string, len(trackedPaths))
	attributePaths := make(map[string]struct{})
	for index, path := range trackedPaths {
		if err := validateLimitedFilePath(path); err != nil {
			return nil, err
		}
		rootPath := prefix + path
		if err := validateLimitedFilePath(rootPath); err != nil {
			return nil, fmt.Errorf("invalid Git root path %q: %w", rootPath, err)
		}
		rootPaths[index] = rootPath
		components := strings.Split(rootPath, "/")
		for depth := 0; depth < len(components); depth++ {
			attributePath := ".gitattributes"
			if depth > 0 {
				attributePath = strings.Join(components[:depth], "/") + "/.gitattributes"
			}
			attributePaths[attributePath] = struct{}{}
			if len(attributePaths) > maxCapturedAttributeFiles {
				return nil, errors.New("captured attribute file count exceeded its bound")
			}
		}
	}

	attributeNames := make([]string, 0, len(attributePaths))
	for path := range attributePaths {
		attributeNames = append(attributeNames, path)
	}
	sort.Strings(attributeNames)
	capturedAttributes := make(map[string]string, len(attributeNames))
	missingAttributes := make([]string, 0)
	var capturedBytes int64
	for _, path := range attributeNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, ok, err := capturedRead(path)
		if err != nil {
			return nil, fmt.Errorf("capture %s: %w", path, err)
		}
		if !ok {
			missingAttributes = append(missingAttributes, path)
			continue
		}
		capturedBytes += int64(len(content))
		if capturedBytes > maxCapturedAttributeBytes {
			return nil, errors.New("captured attribute bytes exceeded their bound")
		}
		capturedAttributes[path] = content
	}
	indexAttributes, err := capturedIndexAttributes(ctx, root, missingAttributes, maxCapturedAttributeBytes-capturedBytes)
	if err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "entire-graph-attributes-")
	if err != nil {
		return nil, fmt.Errorf("create captured attribute worktree: %w", err)
	}
	defer os.RemoveAll(temporary)
	temporaryRoot, err := os.OpenRoot(temporary)
	if err != nil {
		return nil, fmt.Errorf("open captured attribute worktree: %w", err)
	}
	defer temporaryRoot.Close()

	for _, path := range attributeNames {
		content, ok := capturedAttributes[path]
		if !ok {
			content = indexAttributes[path]
		}
		name := filepath.FromSlash(path)
		if directory := filepath.Dir(name); directory != "." {
			if err := temporaryRoot.MkdirAll(directory, 0o700); err != nil {
				return nil, fmt.Errorf("create captured attribute directory %s: %w", path, err)
			}
		}
		if err := temporaryRoot.WriteFile(name, []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("write captured attribute %s: %w", path, err)
		}
	}

	output, err := capturedDiffCheckAttr(ctx, root, temporary, rootPaths)
	if err != nil {
		return nil, err
	}
	drivers := make(map[string]struct{})
	for _, attribute := range output {
		if attribute.Driver != "" {
			drivers[attribute.Driver] = struct{}{}
		}
	}
	driverSettings, err := capturedDiffBinaryDrivers(ctx, root, drivers)
	if err != nil {
		return nil, err
	}
	result := make(map[string]CapturedDiffAttribute, len(trackedPaths))
	for index, path := range trackedPaths {
		attribute, ok := output[rootPaths[index]]
		if !ok {
			return nil, fmt.Errorf("Git check-attr omitted %q", rootPaths[index])
		}
		if attribute.Driver != "" {
			setting := driverSettings[attribute.Driver]
			attribute.Binary = setting.Binary
			attribute.Text = setting.Text
		}
		result[path] = attribute
	}
	return result, nil
}

func capturedDiffCheckAttr(ctx context.Context, repo, worktree string, paths []string) (map[string]CapturedDiffAttribute, error) {
	if len(paths) == 0 {
		return map[string]CapturedDiffAttribute{}, nil
	}
	cmd := newCmd(ctx, repo, "git", "--work-tree="+worktree, "check-attr", "--stdin", "-z", "diff")
	var input bytes.Buffer
	for _, path := range paths {
		input.WriteString(path)
		input.WriteByte(0)
	}
	cmd.Stdin = &input
	var stdout boundedBuffer
	stdout.limit = maxCapturedAttributeBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, gitAttributeCommandError("check-attr", err, stderr.String())
	}
	if stdout.exceeded {
		return nil, errCapturedAttributeOutputLimit
	}
	records := bytes.Split(stdout.Bytes(), []byte{0})
	if len(records) == 0 || len(records[len(records)-1]) != 0 {
		return nil, errors.New("Git check-attr returned malformed NUL output")
	}
	records = records[:len(records)-1]
	if len(records)%3 != 0 {
		return nil, errors.New("Git check-attr returned incomplete NUL output")
	}
	result := make(map[string]CapturedDiffAttribute, len(paths))
	for index := 0; index < len(records); index += 3 {
		path := string(records[index])
		if string(records[index+1]) != "diff" {
			return nil, fmt.Errorf("Git check-attr returned unexpected attribute %q", records[index+1])
		}
		value := string(records[index+2])
		attribute := CapturedDiffAttribute{Value: value}
		switch value {
		case "unspecified":
		case "set":
			attribute.Text = true
		case "unset":
			attribute.Binary = true
		default:
			attribute.Driver = value
		}
		result[path] = attribute
	}
	return result, nil
}

const literalPathspecPrefix = ":(literal)"

// capturedIndexAttributes records the explicit index fallback that Git uses
// when a worktree .gitattributes file is absent. It is resolved before the
// temporary worktree is populated, so a mutable working-tree policy is never
// consulted and the fallback remains an immutable stage-0 index observation.
func capturedIndexAttributes(ctx context.Context, repo string, paths []string, byteLimit int64) (map[string]string, error) {
	if byteLimit < 0 {
		return nil, errors.New("captured index attribute byte budget is negative")
	}
	objects := make(map[string]string)
	for start := 0; start < len(paths); {
		end := start
		argumentBytes := 0
		for end < len(paths) && end-start < literalPathspecBatchCount {
			nextBytes := len(literalPathspecPrefix) + len(paths[end])
			if nextBytes > literalPathspecBatchBytes {
				return nil, fmt.Errorf("captured attribute path exceeds Git argv bound: %q", paths[end])
			}
			if end > start && argumentBytes+nextBytes > literalPathspecBatchBytes {
				break
			}
			argumentBytes += nextBytes
			end++
		}
		args := []string{"ls-files", "--stage", "-z", "--"}
		for _, path := range paths[start:end] {
			args = append(args, literalPathspecPrefix+path)
		}
		cmd := newCmd(ctx, repo, "git", args...)
		var stdout boundedBuffer
		stdout.limit = maxCapturedAttributeBytes
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, gitAttributeCommandError("ls-files captured attributes", err, stderr.String())
		}
		if stdout.exceeded {
			return nil, errCapturedAttributeOutputLimit
		}
		records := bytes.Split(stdout.Bytes(), []byte{0})
		if len(records) == 0 || len(records[len(records)-1]) != 0 {
			return nil, errors.New("Git ls-files returned malformed captured-attribute output")
		}
		for _, record := range records[:len(records)-1] {
			tab := bytes.IndexByte(record, '\t')
			if tab < 0 {
				return nil, errors.New("Git ls-files returned malformed captured-attribute record")
			}
			fields := strings.Fields(string(record[:tab]))
			if len(fields) != 3 {
				return nil, errors.New("Git ls-files returned malformed captured-attribute header")
			}
			path := string(record[tab+1:])
			if fields[2] != "0" {
				continue
			}
			// Git does not apply a symlink or gitlink as a .gitattributes
			// policy file. Keep only ordinary index files, including executable
			// regular files, and let the empty placeholder represent all other
			// modes.
			if fields[0] != "100644" && fields[0] != "100755" {
				continue
			}
			if err := validateLimitedFilePath(path); err != nil {
				return nil, fmt.Errorf("Git ls-files returned unsafe captured attribute path %q: %w", path, err)
			}
			if !validGitObjectID(fields[1]) {
				return nil, fmt.Errorf("Git ls-files returned invalid captured attribute object ID %q", fields[1])
			}
			objects[path] = fields[1]
		}
		start = end
	}
	result := make(map[string]string, len(objects))
	var bytesRead int64
	objectPaths := make([]string, 0, len(objects))
	for path := range objects {
		objectPaths = append(objectPaths, path)
	}
	sort.Strings(objectPaths)
	for _, path := range objectPaths {
		objectID := objects[path]
		content, err := capturedIndexBlob(ctx, repo, objectID, byteLimit-bytesRead)
		if err != nil {
			return nil, fmt.Errorf("read indexed attribute %s: %w", path, err)
		}
		bytesRead += int64(len(content))
		if bytesRead > byteLimit {
			return nil, errors.New("indexed attribute bytes exceeded their bound")
		}
		result[path] = content
	}
	return result, nil
}

func capturedIndexBlob(ctx context.Context, repo, objectID string, maxBytes int64) (string, error) {
	if !validGitObjectID(objectID) {
		return "", fmt.Errorf("invalid indexed attribute object ID %q", objectID)
	}
	if maxBytes < 0 {
		return "", errors.New("indexed attribute byte budget is negative")
	}
	cmd := newCmd(ctx, repo, "git", "cat-file", "blob", objectID)
	var stdout boundedBuffer
	stdout.limit = maxBytes
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", gitAttributeCommandError("cat-file indexed attribute", err, stderr.String())
	}
	if stdout.exceeded {
		return "", errCapturedAttributeOutputLimit
	}
	return stdout.String(), nil
}

func validGitObjectID(objectID string) bool {
	if len(objectID) != 40 && len(objectID) != 64 {
		return false
	}
	for _, character := range objectID {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

type capturedDiffDriverSetting struct {
	Binary bool
	Text   bool
}

func capturedDiffBinaryDrivers(ctx context.Context, repo string, drivers map[string]struct{}) (map[string]capturedDiffDriverSetting, error) {
	result := make(map[string]capturedDiffDriverSetting)
	// Query each configured driver only after check-attr identifies it. This
	// keeps config output bounded by the captured paths instead of importing an
	// unbounded repository configuration stream into the operation.
	driverNames := make([]string, 0, len(drivers))
	for driver := range drivers {
		driverNames = append(driverNames, driver)
	}
	sort.Strings(driverNames)
	for _, driver := range driverNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd := newCmd(ctx, repo, "git", "config", "--bool", "--get", "--", "diff."+driver+".binary")
		var stdout boundedBuffer
		stdout.limit = 1024
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			var exitError *exec.ExitError
			if errors.As(err, &exitError) && exitError.ExitCode() == 1 && strings.TrimSpace(stderr.String()) == "" {
				continue
			}
			return nil, gitAttributeCommandError("config diff driver", err, stderr.String())
		}
		if stdout.exceeded {
			return nil, errCapturedAttributeOutputLimit
		}
		value := strings.TrimSpace(stdout.String())
		switch value {
		case "true":
			result[driver] = capturedDiffDriverSetting{Binary: true}
		case "false":
			result[driver] = capturedDiffDriverSetting{Text: true}
		default:
			return nil, fmt.Errorf("Git config diff.%s.binary returned invalid boolean %q", driver, value)
		}
	}
	return result, nil
}

func gitAttributeCommandError(operation string, err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("Git %s: %s", operation, message)
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if int64(buffer.buffer.Len()+len(data)) > buffer.limit {
		buffer.exceeded = true
		return 0, errCapturedAttributeOutputLimit
	}
	return buffer.buffer.Write(data)
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
