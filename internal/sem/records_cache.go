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
)

// Provider records (the streamed snapshot/symbols/edges NDJSON emitted by the
// `graph` commands) are deterministic for a given commit, tree, indexing mode,
// and set of options. Recomputing them on every call is expensive on large
// repos, so we cache the raw NDJSON bytes under that complete identity. Unlike
// the structured search cache, an opaque record stream cannot restamp commit
// provenance on a same-tree hit.
const providerRecordsCacheVersion = "provider-records-v7"

// cachedProviderRecords is the on-disk envelope for a cached record stream. The
// key alone is authoritative (sha256 over version+commit+tree+mode+profile+
// options+path-file contents), but we re-validate the discriminating fields on
// read as defense-in-depth against a stale or hand-edited cache file.
type cachedProviderRecords struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Commit          string           `json:"commit"`
	Tree            string           `json:"tree"`
	Mode            string           `json:"mode"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Records         []byte           `json:"records"`
	Summary         *SnapshotSummary `json:"summary,omitempty"`
}

// providerRecordsKey derives the cache key for a record stream. It intentionally
// folds in the caller-selected output mode (including
// snapshot:compact-ndjson-v1) and the profile so native and compact snapshots
// never collide, and it hashes the contents of
// any --ignore-file / --include-file inputs, in caller order, so an edit or
// reorder of those files misses the cache. OnlyFiles is included for
// completeness even though the record commands do not expose it. Callers must
// NOT use this for --worktree runs (the working tree can differ from HEAD) or
// for targeted --to/--from/--relation queries.
func providerRecordsKey(absRepo, repositoryKey, providerVersion, commit, tree, mode string, options ProviderSnapshotOptions) (string, error) {
	policy, err := cachePolicyForOptions(absRepo, options)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writeCacheKeyString(hash, "cache-version", providerRecordsCacheVersion)
	// The built-in credential-store deny decides which files are in this corpus at
	// all, so a build that disagrees about it must not reach this build's entries.
	// See builtinSecretRulesDigest for why nothing else in this key separates them.
	writeCacheKeyString(hash, "builtin-secret-rules", builtinSecretRulesDigest())
	writeCacheKeyString(hash, "repository-path", absRepo)
	// Repo identity PREFIXES EVERY SYMBOL ID this cache stores, so serving one repository's records
	// to another hands back IDs attributed to the wrong project. Reproduced by re-pointing a remote:
	// the warm run still reported gh/entireio/entire-graph after the checkout had become a fork.
	// searchSnapshotKey already folds this in; this key did not.
	writeCacheKeyString(hash, "repository-key", repositoryKey)
	writeCacheKeyString(hash, "provider-version", providerVersion)
	writeCacheKeyString(hash, "commit", commit)
	writeCacheKeyString(hash, "tree", tree)
	writeCacheKeyString(hash, "mode", mode)
	writeCacheKeyString(hash, "profile", string(options.Profile))
	writeCacheKeyString(hash, "max-parse-bytes", fmt.Sprintf("%d", options.MaxParseBytes))
	// Same graph-shaping argument as searchSnapshotKey, resolved for the same reason: the env var
	// behind the option has to reach the key too. This cache is the one that is ON BY DEFAULT, so
	// the hole mattered more here.
	writeCacheKeyString(hash, "max-files", fmt.Sprintf("%d", resolveMaxSourceFiles(options.MaxFiles)))
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writeCacheKeyString(hash, "only-files", "begin")
	for _, filePath := range onlyFiles {
		writeCacheKeyString(hash, "only-file", filepath.ToSlash(filepath.Clean(filePath)))
	}
	policy.writeCacheKey(hash)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ProviderRecordsCacheTransaction pins the cache key and immutable ignore
// policy used by one record-stream lookup/build/store cycle.
type ProviderRecordsCacheTransaction struct {
	enabled         bool
	entry           cacheEntry
	providerVersion string
	repositoryKey   string
	commit          string
	tree            string
	mode            string
	options         ProviderSnapshotOptions
}

// BeginProviderRecordsCache captures every external ignore input once and
// prepares the one entry used throughout a provider-record cache transaction.
// Callers must build records with Options before calling Store.
func BeginProviderRecordsCache(ctx context.Context, repo, providerVersion, commit, tree, mode, cacheDir string, options ProviderSnapshotOptions) (*ProviderRecordsCacheTransaction, error) {
	transaction := &ProviderRecordsCacheTransaction{
		providerVersion: providerVersion,
		commit:          commit,
		tree:            tree,
		mode:            mode,
		options:         options,
	}
	if cacheDir == "" || commit == "" || tree == "" || options.Worktree {
		return transaction, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	transaction.options, err = CaptureProviderCachePolicy(absRepo, options)
	if err != nil {
		return nil, err
	}
	transaction.repositoryKey = repoKey(ctx, absRepo)
	key, err := providerRecordsKey(
		absRepo,
		transaction.repositoryKey,
		providerVersion,
		commit,
		tree,
		mode,
		transaction.options,
	)
	if err != nil {
		return nil, err
	}
	transaction.entry, err = newCacheEntry(cacheDir, "records", providerRecordsCacheVersion, key)
	if err != nil {
		return nil, err
	}
	transaction.enabled = true
	return transaction, nil
}

// Options returns the policy-pinned options that must shape records stored by
// this transaction.
func (transaction *ProviderRecordsCacheTransaction) Options() ProviderSnapshotOptions {
	if transaction == nil {
		return ProviderSnapshotOptions{}
	}
	return cloneProviderSnapshotOptions(transaction.options)
}

// Load returns this transaction's cached record stream, when present.
func (transaction *ProviderRecordsCacheTransaction) Load() ([]byte, *SnapshotSummary, bool) {
	if transaction == nil || !transaction.enabled {
		return nil, nil, false
	}
	cache, err := readProviderRecords(transaction.entry)
	if err != nil || !validCachedProviderRecords(
		cache,
		transaction.providerVersion,
		transaction.commit,
		transaction.tree,
		transaction.mode,
		transaction.options,
	) {
		return nil, nil, false
	}
	return cache.Records, cache.Summary, true
}

// Store persists records built with Options only when their observed header
// still matches the immutable commit, tree, repository identity, provider, and
// profile that keyed this transaction. A moving HEAD or remote therefore cannot
// place a later snapshot's record stream under the earlier cache entry.
func (transaction *ProviderRecordsCacheTransaction) Store(records []byte, summary *SnapshotSummary, observed SnapshotHeader) error {
	if transaction == nil || !transaction.enabled {
		return nil
	}
	if observed.Tree != transaction.tree ||
		observed.Commit != transaction.commit ||
		observed.RepoKey != transaction.repositoryKey ||
		observed.Provider != ProviderName ||
		observed.ProviderVersion != transaction.providerVersion ||
		observed.Profile != string(transaction.options.Profile) {
		return fmt.Errorf(
			"provider records snapshot provenance changed while building: got commit=%q tree=%q repo=%q provider=%q version=%q profile=%q, want commit=%q tree=%q repo=%q provider=%q version=%q profile=%q",
			observed.Commit, observed.Tree, observed.RepoKey, observed.Provider, observed.ProviderVersion, observed.Profile,
			transaction.commit, transaction.tree, transaction.repositoryKey, ProviderName, transaction.providerVersion, transaction.options.Profile,
		)
	}
	cache := cachedProviderRecords{
		CacheVersion:    providerRecordsCacheVersion,
		ProviderVersion: transaction.providerVersion,
		Commit:          transaction.commit,
		Tree:            transaction.tree,
		Mode:            transaction.mode,
		Profile:         transaction.options.Profile,
		MaxParseBytes:   transaction.options.MaxParseBytes,
		Records:         records,
		Summary:         summary,
	}
	return writeProviderRecords(transaction.entry, cache)
}

// LoadProviderRecords returns the cached NDJSON record stream for repo at the
// given commit/tree/mode/options, or hit=false when there is no usable entry.
// The returned summary (when present) lets the caller reproduce the partial-parse
// warning without re-indexing. A missing/corrupt cache is a miss, not an error;
// only a key-derivation failure (an unreadable ignore/include file) errors.
func LoadProviderRecords(ctx context.Context, repo, providerVersion, commit, tree, mode, cacheDir string, options ProviderSnapshotOptions) ([]byte, *SnapshotSummary, bool, error) {
	transaction, err := BeginProviderRecordsCache(ctx, repo, providerVersion, commit, tree, mode, cacheDir, options)
	if err != nil {
		return nil, nil, false, err
	}
	records, summary, hit := transaction.Load()
	return records, summary, hit, nil
}

func validCachedProviderRecords(cache cachedProviderRecords, providerVersion, commit, tree, mode string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == providerRecordsCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Commit == commit &&
		cache.Tree == tree &&
		cache.Mode == mode &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes
}

func readProviderRecords(entry cacheEntry) (cachedProviderRecords, error) {
	file, err := entry.open()
	if err != nil {
		return cachedProviderRecords{}, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return cachedProviderRecords{}, err
	}
	defer reader.Close()
	var cache cachedProviderRecords
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&cache); err != nil {
		return cachedProviderRecords{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cachedProviderRecords{}, errors.New("provider records cache has trailing data")
	}
	return cache, nil
}

func writeProviderRecords(entry cacheEntry, cache cachedProviderRecords) error {
	path := entry.writePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".records-*.json.gz")
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
