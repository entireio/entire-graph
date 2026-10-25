package sem

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// Provider records (the streamed snapshot/symbols/edges NDJSON emitted by the
// `graph` commands) are deterministic for a given commit, tree, indexing mode,
// and set of options. Recomputing them on every call is expensive on large
// repos, so we cache the raw NDJSON bytes under that complete identity. Unlike
// the structured search cache, an opaque record stream cannot restamp commit
// provenance on a same-tree hit. v8 additionally binds the shared nested-ignore
// resource policy and surfaced refusals; v9 retires entries whose DATA_FLOWS
// records carry a single evidence entry per edge rather than every flow; v10
// retires entries written before truncated records counted what they dropped.
// v11 retires streams carrying the old diagnostic-count completeness status.
// v12 retires HANDLES_ROUTE streams produced before scoped Go receiver and
// middleware analysis; unchanged consumer trees must not replay those edges.
// v13 retires route records from the old sixteen-round parameter-taint cutoff.
// v14 retires warning text that incorrectly described every command family as worktree-default.
// v15 retires malformed-route literal guesses and undisclosed signature-budget loss.
// v16 retires checkout-path keys and carries checkout roots plus relative scopes
// for safe cross-worktree replay, and binds captured co-change history without
// reusing older route-producer entries.
// v17 retires routes omitted by closure-local writes and address escapes.
// v18 retires bare routes from wrapped mounts and unbounded route masks.
// v19 retires entries before unresolved route placement and Go 1.22 method patterns.
// v20 moves the record bytes out of the JSON envelope into a `.stream.gz`
// sidecar so a producer spools them to disk as they arrive; the envelope keeps
// the sidecar's digest. Entries written by earlier builds embed the records
// inline and are unreadable at this version (and vice versa).
// v21 retires streams carrying DATA_FLOWS edges matched by another language's
// expression patterns (a Python conditional pattern on a Go body and the
// like); language-routed flow extraction no longer produces them.
// v22 retires streams carrying kind=tool symbols (and their HANDLES_TOOL edges)
// fabricated from body text; tool boundaries now come from identifiers only.
// v23 follows v22 and retires entries written before a failed
// co-change history read was disclosed. Such an entry is keyed exactly as this
// build keys the same failure (the history-unavailable scope and no pairs; the
// reason is not, and need not be, in the key), so without the bump this build
// replays its summary, which lacks W_COCHANGE_HISTORY_UNAVAILABLE: the silent
// drop the warning exists to end.
const providerRecordsCacheVersion = "provider-records-v23-" + IdentityRevision

// cachedProviderRecords is the on-disk envelope for a cached record stream. The
// key alone is authoritative (sha256 over version+commit+tree+mode+profile+
// options+path-file contents), but we re-validate the discriminating fields on
// read as defense-in-depth against a stale or hand-edited cache file.
type cachedProviderRecords struct {
	CacheVersion string `json:"cache_version"`
	// SchemaVersion is the sem.SchemaVersion the stored record stream was
	// serialized under. It is the only thing in this envelope that distinguishes
	// two wire shapes; see validCachedProviderRecords. Entries written before this
	// field existed decode as "" and are correctly rejected as a different schema.
	SchemaVersion   string  `json:"schema_version"`
	ProviderVersion string  `json:"provider_version"`
	Commit          string  `json:"commit"`
	Tree            string  `json:"tree"`
	Mode            string  `json:"mode"`
	Profile         Profile `json:"profile"`
	MaxParseBytes   int     `json:"max_parse_bytes"`
	// RepoRoot is the checkout the stream was built in, whose path the header
	// line's repo_root carries. The key is path-free, so a hit from another
	// worktree or clone at the same commit restamps that one field
	// (restampProviderRecordsRepoRoot) and misses when it cannot.
	RepoRoot string `json:"repo_root"`
	// RepoScope is cacheCheckoutScope of RepoRoot. A hit from another checkout
	// restamps repo_root only when that checkout has the same scope.
	RepoScope string `json:"repo_scope,omitempty"`
	// RecordsDigest is the SHA-256 (hex) of the `.stream.gz` sidecar's stored
	// bytes. The record stream lives there, not in this envelope, so the
	// envelope must say which stream it vouches for: the two files are renamed
	// into place separately, and a reader that finds a sidecar this digest does
	// not match treats the entry as a miss rather than serving bytes nothing
	// validated.
	RecordsDigest string           `json:"records_digest"`
	Summary       *SnapshotSummary `json:"summary,omitempty"`
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
// providerRecordsKey addresses an entry for the schema THIS build serializes
// under. The schema is a key input, not only a read-time check: without it two
// builds at different schema versions address the same artifact, so each one's
// store overwrites the other's and neither ever gets a warm cache. Validating
// the schema after opening a schema-independent entry catches the wrong answer
// but cannot stop the mutual eviction that produced it.
func providerRecordsKey(absRepo, repositoryKey, providerVersion, commit, tree, mode string, options ProviderSnapshotOptions, contexts ...context.Context) (string, error) {
	return providerRecordsKeyForSchema(SchemaVersion, absRepo, repositoryKey, providerVersion, commit, tree, mode, options, contexts...)
}

// providerRecordsKeyForSchema takes the schema version explicitly so a test can
// address the entry a build at another schema would have written. Production
// reaches it only through providerRecordsKey, which supplies SchemaVersion.
func providerRecordsKeyForSchema(schemaVersion, absRepo, repositoryKey, providerVersion, commit, tree, mode string, options ProviderSnapshotOptions, contexts ...context.Context) (string, error) {
	return providerRecordsKeyForVersions(providerRecordsCacheVersion, schemaVersion, absRepo, repositoryKey, providerVersion, commit, tree, mode, options, contexts...)
}

// providerRecordsKeyForVersions also takes the cache version explicitly, so a test
// can address the entry an earlier build wrote for the same inputs.
func providerRecordsKeyForVersions(cacheVersion, schemaVersion, absRepo, repositoryKey, providerVersion, commit, tree, mode string, options ProviderSnapshotOptions, contexts ...context.Context) (string, error) {
	policy, err := cachePolicyForOptions(absRepo, options)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writeCacheKeyString(hash, "cache-version", cacheVersion)
	// The record bytes are replayed VERBATIM, so the schema that shaped them is
	// part of the entry's ADDRESS, not just of its contents.
	writeCacheKeyString(hash, "schema-version", schemaVersion)
	// The built-in credential-store deny decides which files are in this corpus at
	// all, so a build that disagrees about it must not reach this build's entries.
	// See builtinSecretRulesDigest for why nothing else in this key separates them.
	writeCacheKeyString(hash, "builtin-secret-rules", builtinSecretRulesDigest())
	// No checkout path: the stream is a function of the commit and the keyed
	// options, so worktrees and clones at one commit share the entry, and Load
	// restamps the header's repo_root for the checkout it serves.
	//
	// Repo identity PREFIXES EVERY SYMBOL ID this cache stores, so serving one repository's records
	// to another hands back IDs attributed to the wrong project. Reproduced by re-pointing a remote:
	// the warm run still reported gh/entireio/entire-graph after the checkout had become a fork.
	// searchSnapshotKey already folds this in; this key did not.
	// Unlike the search cache, the full key is hashed even for local/<basename>:
	// these bytes are replayed verbatim, so an ID namespace cannot be rewritten.
	writeCacheKeyString(hash, "repository-key", repositoryKey)
	// Path-free is not scope-free: a `--repo <subdir>` stream lists only that
	// subtree, with subdir-relative paths, at the same commit and tree as the
	// root's. See the "repository-scope" term in searchSnapshotKeyForSchema.
	writeCacheKeyString(hash, "repository-scope", cacheCheckoutScope(absRepo, contexts...))
	writeCacheKeyString(hash, "provider-version", providerVersion)
	writeCacheKeyString(hash, "commit", commit)
	writeCacheKeyString(hash, "tree", tree)
	if resolveProfile(options.Profile).emits("FILE_CHANGES_WITH") {
		captured := options.historyCochanges
		if captured == nil {
			ctx := context.Background()
			if len(contexts) > 0 && contexts[0] != nil {
				ctx = contexts[0]
			}
			captured = &capturedFileCochanges{unavailable: true}
			if gitMetadataSafeForSubprocessContext(ctx, absRepo) {
				if pairs, err := gitutil.FileCochanges(ctx, absRepo, commit, 256); err == nil {
					captured = &capturedFileCochanges{pairs: pairs}
				}
			}
		}
		if captured.unavailable {
			writeCacheKeyString(hash, "history-unavailable-scope", filepath.Clean(absRepo))
		}
		writeCacheKeyString(hash, "history-cochanges", "begin")
		for _, pair := range captured.pairs {
			writeCacheKeyString(hash, "history-left", pair.Left)
			writeCacheKeyString(hash, "history-right", pair.Right)
			writeCacheKeyString(hash, "history-count", fmt.Sprint(pair.Count))
		}
	}

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
	ctx             context.Context
	enabled         bool
	entry           cacheEntry
	streamEntry     cacheEntry
	absRepo         string
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
		ctx:             ctx,
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
	transaction.absRepo = absRepo
	// Preserve the caller's validation scope. A transaction may outlive setup,
	// so it must not mint a safety receipt that later Load/Store calls reuse.
	safe := gitMetadataSafeForSubprocessContext(ctx, absRepo)
	transaction.options, err = CaptureProviderCachePolicy(absRepo, options)
	if err != nil {
		return nil, err
	}
	transaction.repositoryKey = repoKey(ctx, absRepo)
	if resolveProfile(options.Profile).emits("FILE_CHANGES_WITH") {
		captured := &capturedFileCochanges{
			unavailable: true,
			reason:      "git metadata is not safe for Git subprocesses, so history was not read",
		}
		if safe {
			pairs, historyErr := gitutil.FileCochanges(ctx, absRepo, commit, 256)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if historyErr == nil {
				captured = &capturedFileCochanges{pairs: pairs}
			} else {
				captured.reason = historyErr.Error()
			}
		}
		transaction.options.historyCochanges = captured
	}

	key, err := providerRecordsKey(
		absRepo,
		transaction.repositoryKey,
		providerVersion,
		commit,
		tree,
		mode,
		transaction.options,
		ctx,
	)
	if err != nil {
		return nil, err
	}
	transaction.entry, err = newCacheEntry(cacheDir, "records", providerRecordsCacheVersion, key)
	if err != nil {
		return nil, err
	}
	transaction.streamEntry, err = newCacheStreamEntry(cacheDir, "records", providerRecordsCacheVersion, key)
	if err != nil {
		return nil, err
	}
	transaction.enabled = true
	return transaction, nil
}

// NewSpool returns the writer a producer tees its record stream into while
// building, so the bytes land on disk as they arrive instead of accumulating
// in memory. A disabled transaction returns a nil spool: the caller skips the
// tee and Store stays a no-op, the same silence the disabled path always had.
// Give the finished spool to Store; Abandon it on any other exit.
func (transaction *ProviderRecordsCacheTransaction) NewSpool() (*ProviderRecordsSpool, error) {
	if transaction == nil || !transaction.enabled {
		return nil, nil
	}
	return newProviderRecordsSpool(transaction.streamEntry)
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
	// Restamp only between checkouts of the same scope (see cachedProviderRecords.RepoScope);
	// the same root is the same checkout, so the scope lookup is paid only on a cross-checkout hit.
	if cache.RepoRoot != transaction.absRepo && cache.RepoScope != cacheCheckoutScope(transaction.absRepo, transaction.ctx) {
		return nil, nil, false
	}
	stored, err := readProviderRecordsStream(transaction.streamEntry, cache.RecordsDigest)
	if err != nil {
		return nil, nil, false
	}
	records, ok := restampProviderRecordsRepoRoot(stored, cache.RepoRoot, transaction.absRepo)
	if !ok {
		return nil, nil, false
	}
	return records, cache.Summary, true
}

// restampProviderRecordsRepoRoot rewrites the header line's repo_root from the
// checkout a stream was built in to the one replaying it, the only absolute path
// a stream carries. Every other record is repository-relative.
//
// The stream is opaque bytes, so the rewrite is literal: the header is the first
// line in both the native and the compact encoding, and the field is located by
// re-encoding the recorded root exactly as the record encoders did (no HTML
// escaping). Anything short of exactly one match in that line is a miss rather
// than a guess, which also covers a root whose encoding the terminal-safety
// wrapper altered.
func restampProviderRecordsRepoRoot(records []byte, from, to string) ([]byte, bool) {
	if from == to {
		return records, true
	}
	if from == "" {
		return nil, false
	}
	end := bytes.IndexByte(records, '\n')
	if end < 0 {
		return nil, false
	}
	oldField, newField := providerRecordsRepoRootField(from), providerRecordsRepoRootField(to)
	if oldField == nil || newField == nil || bytes.Count(records[:end], oldField) != 1 {
		return nil, false
	}
	header := bytes.Replace(records[:end], oldField, newField, 1)
	restamped := make([]byte, 0, len(header)+len(records)-end)
	restamped = append(restamped, header...)
	return append(restamped, records[end:]...), true
}

func providerRecordsRepoRootField(root string) []byte {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return nil
	}
	return append([]byte(`"repo_root":`), bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))...)
}

// Store persists the spooled records only when their observed header still
// matches the immutable schema, commit, tree, repository identity, provider,
// and profile that keyed this transaction. A moving HEAD, remote, or
// serializer therefore cannot place a different snapshot's record stream under
// the earlier cache entry.
//
// Store consumes the spool on every path, success or failure; callers must not
// reuse or Abandon it afterwards (Abandon stays safe, just redundant). The
// sidecar is renamed into place before the envelope is written: a crash
// between the two leaves a sidecar the surviving envelope's digest does not
// match, which reads as a miss, never as the wrong bytes.
func (transaction *ProviderRecordsCacheTransaction) Store(spool *ProviderRecordsSpool, summary *SnapshotSummary, observed SnapshotHeader) error {
	if transaction == nil || !transaction.enabled || spool == nil {
		spool.Abandon()
		return nil
	}
	if SnapshotTruncated(summary) {
		// A truncated stream is not this tree's answer. The key is derived from
		// the tree and the graph-shaping options only, so persisting a
		// truncation would hand it to every later caller as the complete index.
		// Not an error: persistence here is best effort by contract, and the
		// caller already has the (correctly self-describing) records in hand.
		return nil
	}
	if observed.SchemaVersion != SchemaVersion ||
		observed.Tree != transaction.tree ||
		observed.Commit != transaction.commit ||
		observed.RepoKey != transaction.repositoryKey ||
		observed.Provider != ProviderName ||
		observed.ProviderVersion != transaction.providerVersion ||
		observed.Profile != string(transaction.options.Profile) ||
		(observed.RepoRoot != "" && observed.RepoRoot != transaction.absRepo) {
		spool.Abandon()
		return fmt.Errorf(
			"provider records snapshot provenance changed while building: got schema=%q commit=%q tree=%q repo=%q provider=%q version=%q profile=%q root=%q, want schema=%q commit=%q tree=%q repo=%q provider=%q version=%q profile=%q root=%q",
			observed.SchemaVersion, observed.Commit, observed.Tree, observed.RepoKey, observed.Provider, observed.ProviderVersion, observed.Profile, observed.RepoRoot,
			SchemaVersion, transaction.commit, transaction.tree, transaction.repositoryKey, ProviderName, transaction.providerVersion, transaction.options.Profile, transaction.absRepo,
		)
	}
	digest, err := spool.finish()
	if err != nil {
		return err
	}
	if err := spool.install(transaction.streamEntry); err != nil {
		return err
	}
	cache := cachedProviderRecords{
		CacheVersion:    providerRecordsCacheVersion,
		SchemaVersion:   SchemaVersion,
		ProviderVersion: transaction.providerVersion,
		Commit:          transaction.commit,
		Tree:            transaction.tree,
		Mode:            transaction.mode,
		Profile:         transaction.options.Profile,
		MaxParseBytes:   transaction.options.MaxParseBytes,
		RepoRoot:        transaction.absRepo,
		RepoScope:       cacheCheckoutScope(transaction.absRepo, transaction.ctx),
		RecordsDigest:   digest.String(),
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
		// The record bytes are replayed VERBATIM, so the schema they were serialized
		// under is the schema the caller receives. Nothing else here separates two
		// schemas: providerRecordsCacheVersion tracks the caching machinery, and
		// provider-version is the constant "dev" for every local build and the
		// constant "v0.0.0-ci" for every non-tag CI build. Without this clause a
		// binary at schema N serves entries written at schema N-1 as its own output —
		// reproduced by renaming one wire field and diffing warm against cold.
		cache.SchemaVersion == SchemaVersion &&
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
	if err := decodeCacheValue(reader, &cache); err != nil {
		return cachedProviderRecords{}, err
	}
	return cache, nil
}

func writeProviderRecords(entry cacheEntry, cache cachedProviderRecords) error {
	return entry.write("records", cache)
}
