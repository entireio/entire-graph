package sem

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/entireio/entire-graph/internal/gitutil"
)

const searchSnapshotCacheVersion = "search-snapshot-v1"

type cachedSearchSnapshot struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Tree            string           `json:"tree"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Snapshot        ProviderSnapshot `json:"snapshot"`
}

func loadOrBuildSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	if disableCache || cacheDir == "" || options.Worktree {
		snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, commitErr := gitutil.RevParse(ctx, absRepo, "HEAD")
	tree, err := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")
	if commitErr != nil || commit == "" || err != nil || tree == "" {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	key, err := searchSnapshotKey(absRepo, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	if cached, err := readSearchSnapshot(path); err == nil && validCachedSearchSnapshot(cached, providerVersion, tree, options) {
		cached.Snapshot.Header.Commit = commit
		return cached.Snapshot, true, nil
	}
	// A complete committed-tree snapshot is query independent and can serve a
	// selective search without rebuilding the same tree for every query. Keep
	// the selective view so cache presence cannot change retrieval semantics.
	if len(options.OnlyFiles) > 0 {
		fullOptions := options
		fullOptions.OnlyFiles = nil
		fullKey, keyErr := searchSnapshotKey(absRepo, providerVersion, tree, fullOptions)
		if keyErr != nil {
			return ProviderSnapshot{}, false, keyErr
		}
		fullPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")
		if cached, readErr := readSearchSnapshot(fullPath); readErr == nil && validCachedSearchSnapshot(cached, providerVersion, tree, fullOptions) {
			cached.Snapshot.Header.Commit = commit
			return filterSearchSnapshot(cached.Snapshot, options.OnlyFiles), true, nil
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	cache := cachedSearchSnapshot{
		CacheVersion:    searchSnapshotCacheVersion,
		ProviderVersion: providerVersion,
		Tree:            tree,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
		Snapshot:        snapshot,
	}
	// Cache persistence is best effort. Retrieval correctness never depends on
	// a writable cache directory.
	_ = writeSearchSnapshot(path, cache)
	return snapshot, false, nil
}

// PreindexProviderSnapshot builds or loads the complete snapshot for exactly
// the repository's current HEAD tree. Unlike query-time selective indexing,
// this cache entry is query independent and can be prepared before an agent
// task begins. Worktree snapshots are deliberately rejected because dirty
// state cannot be represented by a durable tree-keyed cache safely.
func PreindexProviderSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
) (ProviderSnapshot, bool, error) {
	if options.Worktree {
		return ProviderSnapshot{}, false, errors.New("preindex requires a committed HEAD snapshot")
	}
	if cacheDir == "" {
		return ProviderSnapshot{}, false, errors.New("preindex requires a cache directory")
	}
	options.Worktree = false
	options.OnlyFiles = nil
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, err := gitutil.RevParse(ctx, absRepo, "HEAD")
	if err != nil || commit == "" {
		if err == nil {
			err = errors.New("HEAD resolved to an empty commit")
		}
		return ProviderSnapshot{}, false, fmt.Errorf("resolve committed HEAD for preindex: %w", err)
	}
	tree, err := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")
	if err != nil || tree == "" {
		if err == nil {
			err = errors.New("HEAD resolved to an empty tree")
		}
		return ProviderSnapshot{}, false, fmt.Errorf("resolve committed HEAD tree for preindex: %w", err)
	}
	snapshot, cacheHit, err := loadOrBuildSearchSnapshot(ctx, absRepo, providerVersion, options, cacheDir, false)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if snapshot.Header.Commit != commit || snapshot.Header.Tree != tree {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"preindex snapshot provenance mismatch: got commit %q tree %q, want commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	// Query-time caching is deliberately best effort, but an explicit preindex
	// command promises a durable artifact. Verify that the entry exists and, if
	// the best-effort write failed, retry while surfacing the persistence error.
	key, err := searchSnapshotKey(absRepo, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	persisted, readErr := readSearchSnapshot(path)
	if readErr != nil || !validCachedSearchSnapshot(persisted, providerVersion, tree, options) {
		cache := cachedSearchSnapshot{
			CacheVersion:    searchSnapshotCacheVersion,
			ProviderVersion: providerVersion,
			Tree:            tree,
			Profile:         options.Profile,
			MaxParseBytes:   options.MaxParseBytes,
			Snapshot:        snapshot,
		}
		if err := writeSearchSnapshot(path, cache); err != nil {
			return ProviderSnapshot{}, false, fmt.Errorf("persist preindex snapshot: %w", err)
		}
	}
	return snapshot, cacheHit, nil
}

func filterSearchSnapshot(snapshot ProviderSnapshot, onlyFiles []string) ProviderSnapshot {
	allowedFiles := make(map[string]bool, len(onlyFiles))
	for _, filePath := range onlyFiles {
		allowedFiles[filepath.ToSlash(filepath.Clean(filePath))] = true
	}
	filtered := ProviderSnapshot{Header: snapshot.Header}
	allowedIDs := make(map[string]bool)
	for _, file := range snapshot.Files {
		if allowedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			filtered.Files = append(filtered.Files, file)
			allowedIDs[file.ID] = true
		}
	}
	for _, symbol := range snapshot.Symbols {
		if allowedFiles[filepath.ToSlash(filepath.Clean(symbol.FilePath))] {
			filtered.Symbols = append(filtered.Symbols, symbol)
			allowedIDs[symbol.ID] = true
		}
	}
	for _, external := range snapshot.Externals {
		if allowedIDs[external.SourceSymbol] || (external.FilePath != "" && allowedFiles[filepath.ToSlash(filepath.Clean(external.FilePath))]) {
			filtered.Externals = append(filtered.Externals, external)
			allowedIDs[external.ID] = true
		}
	}
	for _, relation := range snapshot.Relations {
		if allowedIDs[relation.FromID] && allowedIDs[relation.ToID] {
			filtered.Relations = append(filtered.Relations, relation)
		}
	}
	filtered.Header.Warnings = filterSearchWarnings(snapshot.Header.Warnings, allowedFiles)
	filtered.Header.PartialFailures = filterSearchPartialFailures(snapshot.Header.PartialFailures, allowedFiles)
	return filtered
}

func filterSearchWarnings(warnings []ProviderWarning, allowedFiles map[string]bool) []ProviderWarning {
	filtered := make([]ProviderWarning, 0, len(warnings))
	for _, warning := range warnings {
		if warning.FilePath == "" || allowedFiles[filepath.ToSlash(filepath.Clean(warning.FilePath))] {
			filtered = append(filtered, warning)
		}
	}
	return filtered
}

func filterSearchPartialFailures(failures []PartialFailure, allowedFiles map[string]bool) []PartialFailure {
	filtered := make([]PartialFailure, 0, len(failures))
	for _, failure := range failures {
		if failure.FilePath == "" || allowedFiles[filepath.ToSlash(filepath.Clean(failure.FilePath))] {
			filtered = append(filtered, failure)
		}
	}
	return filtered
}

// LoadOrBuildProviderSnapshot reuses the tree-keyed, option-keyed compressed
// provider snapshot cache shared with search. Worktree snapshots always bypass
// the cache so dirty edits cannot be hidden by committed-tree state.
func LoadOrBuildProviderSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache)
}

func searchSnapshotKey(absRepo, providerVersion, tree string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(searchSnapshotCacheVersion)
	writePart(absRepo)
	writePart(providerVersion)
	writePart(tree)
	writePart(string(options.Profile))
	writePart(fmt.Sprintf("%d", options.MaxParseBytes))
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writePart("only-files")
	for _, filePath := range onlyFiles {
		writePart(filepath.ToSlash(filepath.Clean(filePath)))
	}
	for groupIndex, group := range [][]string{options.IgnoreFiles, options.IncludeFiles} {
		writePart(fmt.Sprintf("path-group-%d", groupIndex))
		paths := append([]string(nil), group...)
		sort.Strings(paths)
		for _, path := range paths {
			resolved := path
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(absRepo, resolved)
			}
			writePart(filepath.Clean(resolved))
			content, err := os.ReadFile(resolved)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					writePart("missing")
					continue
				}
				return "", err
			}
			_, _ = hash.Write(content)
			writePart("")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validCachedSearchSnapshot(cache cachedSearchSnapshot, providerVersion, tree string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == searchSnapshotCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.Tree == tree &&
		cache.Snapshot.Header.Provider == ProviderName &&
		cache.Snapshot.Header.Profile == string(options.Profile)
}

func readSearchSnapshot(path string) (cachedSearchSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer reader.Close()
	var cache cachedSearchSnapshot
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&cache); err != nil {
		return cachedSearchSnapshot{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cachedSearchSnapshot{}, errors.New("search snapshot cache has trailing data")
	}
	return cache, nil
}

func writeSearchSnapshot(path string, cache cachedSearchSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".snapshot-*.json.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writer := gzip.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(cache); err != nil {
		_ = writer.Close()
		_ = temporary.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
