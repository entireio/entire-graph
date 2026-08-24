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
	"strings"
	"sync"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// searchSnapshotCacheVersion names the on-disk cache directory and is hashed
// into every entry key, so bumping it moves new entries to a fresh directory
// and any prior-version directory can simply be deleted wholesale — cleanup is
// "remove old version dirs" instead of per-entry reachability analysis.
// (v5 isolated the tree-only key layout; v6, on main, supersedes it.)
const searchSnapshotCacheVersion = "search-snapshot-v8"

type cachedSymbolByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type cachedSearchSnapshot struct {
	CacheVersion    string  `json:"cache_version"`
	ProviderVersion string  `json:"provider_version"`
	Commit          string  `json:"commit"`
	Tree            string  `json:"tree"`
	Profile         Profile `json:"profile"`
	MaxParseBytes   int     `json:"max_parse_bytes"`
	// Worktree records which view produced this entry. Working-tree snapshots
	// carry provenance a committed-tree snapshot does not (W_WORKTREE_SNAPSHOT),
	// so the two views never share an entry even when their content is equal.
	// This became load-bearing when a clean working tree turned cacheable at all
	// (worktreeSnapshotCacheable): before that, `options.Worktree` bypassed the
	// cache outright and no working-tree entry could exist to collide.
	Worktree bool             `json:"worktree,omitempty"`
	Snapshot ProviderSnapshot `json:"snapshot"`
	// FileRecord.Lines, SymbolRecord.Local, and exact symbol byte ranges are
	// intentionally absent from the public wire format, but relation resolution
	// consumes them. Preserve those internal fields so a complete preindex can
	// derive an exact selective view without reparsing source files.
	FileLines                    map[string]int                   `json:"file_lines,omitempty"`
	LocalSymbolIDs               []string                         `json:"local_symbol_ids,omitempty"`
	SymbolByteRanges             map[string]cachedSymbolByteRange `json:"symbol_byte_ranges,omitempty"`
	SymbolParameterNames         map[string][]string              `json:"symbol_parameter_names,omitempty"`
	SymbolParameterNamesKnownIDs []string                         `json:"symbol_parameter_names_known_ids,omitempty"`
	// Signature types travel with the same reasoning as parameter names: they
	// are AST metadata the type passes consume, so a cache hit must reproduce
	// them or a cached run would fall back to the signature-string split and
	// emit different type relations than a cold one.
	SymbolSignatureTypes map[string]cachedSignatureTypes `json:"symbol_signature_types,omitempty"`
	// BodylessSymbolIDs travels for the same reason as LocalSymbolIDs: call
	// resolution reads SymbolRecord.bodyless to tell a TypeScript overload set
	// apart from two genuinely ambiguous same-name definitions, and the selective
	// derivation reruns that resolution over cached symbols. Without it a cache
	// hit would downgrade an overloaded call that a cold run resolves exactly.
	BodylessSymbolIDs []string `json:"bodyless_symbol_ids,omitempty"`
}

type cachedSignatureTypes struct {
	Params  string `json:"params,omitempty"`
	Returns string `json:"returns,omitempty"`
}

// worktreeCleanTTL bounds how stale a working-tree cleanliness verdict may be.
// One command issues several snapshot lookups (graph scope, complete preindex,
// selective build) and must not pay for a `git status` each time; a window this
// short cannot span an edit that happens between two of them, while a
// long-lived library consumer still re-checks on every new command.
const worktreeCleanTTL = 2 * time.Second

type worktreeCleanVerdict struct {
	clean     bool
	checkedAt time.Time
}

var worktreeCleanVerdicts sync.Map // absRepo -> worktreeCleanVerdict

// absOrRepo resolves repo to an absolute path, falling back to the input when
// that is impossible. It exists so a cleanliness verdict is memoized under the
// same key the cache lookups use.
func absOrRepo(repo string) string {
	if abs, err := filepath.Abs(repo); err == nil {
		return abs
	}
	return repo
}

// worktreeSnapshotCacheable reports whether these options may be served from,
// and stored in, the tree-keyed snapshot cache.
//
// Committed-tree snapshots always could. A working-tree snapshot may too, but
// only while the working tree's INDEXABLE content is identical to HEAD: then the
// tree hash names that content exactly, which is the whole premise of the cache
// key. This is what makes the relation verbs (neighbors/impact, which index the
// whole repository) warm on their second call instead of re-indexing from
// scratch — and it never hides a dirty edit, because a dirty indexable file
// fails the check and bypasses the cache exactly as before.
//
// "Indexable" is the load-bearing word. Requiring a wholly pristine tree made a
// stray untracked file that no parser ever opens — .DS_Store, a compiled binary,
// a log, an editor swap file — disable the cache for the entire repository, so
// every query re-indexed from scratch. A path that cannot contribute a symbol or
// a relation cannot make a snapshot stale either, so it is not a reason to throw
// one away.
//
// The test is extensionUnsupported, which is what the indexer uses to decide whether to PARSE a
// file — but parsing is not the only way a file reaches the graph. buildManifestImportResolver
// also reads the CONTENT of the repo-root manifests, and two of those (go.mod, setup.cfg) carry
// no supported extension while deciding how every import in the repository resolves. Forgiving
// them served a snapshot whose call edges were still resolved against the previous module path.
// They are excluded by name through isManifestImportFile, and TestManifestImportFilesCoverReads
// fails if that list falls behind the manifests actually read.
//
// Otherwise the rule is deliberately conservative in the safe direction: an extensionless path
// counts as supported, because a shebang can make it a script, and any supported path still
// bypasses the cache whatever its status.
//
// Ignored files never reach this loop: git status omits them, and the provider's walk skips them
// too, so neither side can serve what the other would have indexed. Note the two do not agree in
// general — git never ignores a TRACKED file, while the walk applies the ignore stack to every
// path — but that disagreement only ever reports a path as dirty that the walk would have
// skipped, which costs a re-index and cannot serve a stale one.
func worktreeSnapshotCacheable(ctx context.Context, absRepo string, options ProviderSnapshotOptions) bool {
	if !options.Worktree {
		return true
	}
	if cached, ok := worktreeCleanVerdicts.Load(absRepo); ok {
		verdict := cached.(worktreeCleanVerdict)
		if time.Since(verdict.checkedAt) < worktreeCleanTTL {
			return verdict.clean
		}
	}
	clean := true
	dirty, err := gitutil.WorktreeDirtyPaths(ctx, absRepo)
	if err != nil {
		clean = false
	}
	for _, path := range dirty {
		if !extensionUnsupported(path) || isManifestImportFile(path) {
			clean = false
			break
		}
	}
	worktreeCleanVerdicts.Store(absRepo, worktreeCleanVerdict{clean: clean, checkedAt: time.Now()})
	return clean
}

// InvalidateWorktreeCleanVerdicts drops the memoized cleanliness verdicts. A
// one-shot command never needs it (the memo cannot outlive the process), but a
// long-lived embedder that has just written to a working tree can call it to
// force the next query to re-check rather than wait out the TTL.
func InvalidateWorktreeCleanVerdicts() {
	worktreeCleanVerdicts.Range(func(key, _ any) bool {
		worktreeCleanVerdicts.Delete(key)
		return true
	})
}

// loadOrBuildSearchGraphSnapshot preserves the exact candidate-file scope even
// when a complete committed-tree snapshot is published concurrently. The
// shared loader derives an OnlyFiles view from that full snapshot and reports a
// cache hit, so cache timing cannot change the graph search receives.
//
// It used to consult loadCachedCompleteSearchSnapshot here first. That shortcut is
// gone: the shared loader now performs the same derivation itself, under the same
// worktree-cleanliness gate, so a second entry point could only reintroduce the
// divergence between a warm and a cold result set that confinement removed.
func loadOrBuildSearchGraphSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache, nil)
}

func loadCachedCompleteSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
) (ProviderSnapshot, bool, error) {
	if cacheDir == "" {
		return ProviderSnapshot{}, false, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if !worktreeSnapshotCacheable(ctx, absRepo, options) {
		return ProviderSnapshot{}, false, nil
	}
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)
	if headErr != nil {
		return ProviderSnapshot{}, false, nil
	}
	repositoryKey := repoKey(ctx, absRepo)
	fullOptions := options
	fullOptions.OnlyFiles = nil
	if fullOptions.Profile == "" {
		fullOptions.Profile = ProfileFull
	}
	fullKey, keyErr := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, fullOptions)
	if keyErr != nil {
		return ProviderSnapshot{}, false, keyErr
	}
	fullEntry, entryErr := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, fullKey)
	if entryErr != nil {
		return ProviderSnapshot{}, false, entryErr
	}
	cached, readErr := readSearchSnapshot(fullEntry)
	if readErr != nil || !validCachedSearchSnapshot(cached, repositoryKey, providerVersion, tree, fullOptions) {
		return ProviderSnapshot{}, false, nil
	}
	// The cache key is tree-only: a hit may have been built for a different
	// commit that shares this tree. The parsed graph is exactly correct, but
	// commit provenance must reflect the HEAD we are serving right now.
	cached = restampCachedSearchSnapshotCommit(cached, commit)
	return cached.Snapshot, true, nil
}

// loadOrBuildSearchSnapshot is the single search-snapshot cache pipeline: it
// resolves HEAD and the repository key once, serves a valid per-query cache
// entry first, otherwise derives a selective view from a complete
// committed-tree snapshot (the optional preloadedFull already in memory, then
// the on-disk complete entry) and persists it, and finally falls back to a
// fresh build. Derivation failures are soft so an optional cache can never
// break retrieval.
func loadOrBuildSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
	preloadedFull *ProviderSnapshot,
) (ProviderSnapshot, bool, error) {
	if options.Profile == "" {
		options.Profile = ProfileFull
	}
	if disableCache || cacheDir == "" {
		snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if !worktreeSnapshotCacheable(ctx, absRepo, options) {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)
	if headErr != nil {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	repositoryKey := repoKey(ctx, absRepo)
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if cached, err := readSearchSnapshot(entry); err == nil && validCachedSearchSnapshot(cached, repositoryKey, providerVersion, tree, options) {
		// See loadCachedCompleteSearchSnapshot: tree-only keying means this hit
		// may belong to a different commit that shares the tree. Re-stamp before
		// handing it back so no caller ever reports a stale commit.
		cached = restampCachedSearchSnapshotCommit(cached, commit)
		return cached.Snapshot, true, nil
	}
	// A complete committed-tree snapshot is query independent and can serve a
	// selective search without rebuilding the same tree for every query. Keep
	// the selective view so cache presence cannot change retrieval semantics.
	if len(options.OnlyFiles) > 0 {
		deriveFromFull := func(full ProviderSnapshot) (ProviderSnapshot, bool) {
			selective, deriveErr := selectiveSearchSnapshotFromFull(ctx, absRepo, providerVersion, options, full)
			if deriveErr != nil {
				// Provenance or internal-metadata mismatches make this complete
				// snapshot unsuitable for derivation. Fall through instead of
				// letting an optional cache break retrieval.
				return ProviderSnapshot{}, false
			}
			// Persisting the exact selective view makes repeated identical queries
			// a direct cache hit. As with ordinary search caching, this is best effort.
			_ = writeSearchSnapshot(entry, newCachedSearchSnapshot(providerVersion, commit, tree, options, selective))
			return selective, true
		}
		if preloadedFull != nil {
			if selective, ok := deriveFromFull(*preloadedFull); ok {
				return selective, true, nil
			}
		}
		fullOptions := options
		fullOptions.OnlyFiles = nil
		fullKey, keyErr := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, fullOptions)
		if keyErr != nil {
			return ProviderSnapshot{}, false, keyErr
		}
		fullEntry, entryErr := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, fullKey)
		if entryErr != nil {
			return ProviderSnapshot{}, false, entryErr
		}
		if cached, readErr := readSearchSnapshot(fullEntry); readErr == nil && validCachedSearchSnapshot(cached, repositoryKey, providerVersion, tree, fullOptions) {
			cached = restampCachedSearchSnapshotCommit(cached, commit)
			if selective, ok := deriveFromFull(cached.Snapshot); ok {
				return selective, true, nil
			}
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if snapshot.Header.Tree != tree {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"HEAD changed while building search snapshot: got commit %q tree %q, started at commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	// The tree we started at is still what got built even if a same-tree
	// commit (e.g. an empty commit) landed concurrently. Re-stamp so the
	// returned snapshot reports the commit this call is serving, not whatever
	// HEAD happened to be mid-build.
	snapshot.Header.Commit = commit
	cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
	// Cache persistence is best effort. Retrieval correctness never depends on
	// a writable cache directory.
	_ = writeSearchSnapshot(entry, cache)
	return snapshot, false, nil
}

// loadOrDeriveSelectiveSearchSnapshot serves a selective query from an
// already-loaded complete snapshot through the shared cache pipeline: a valid
// cached selective entry wins, a miss derives the exact selective view from
// the in-memory complete snapshot and persists it so the next identical query
// is a direct cache hit, and a derivation failure (for example a HEAD move
// since the complete snapshot was read) falls back to the ordinary selective
// load/build instead of failing the search.
func loadOrDeriveSelectiveSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
	full ProviderSnapshot,
) (ProviderSnapshot, bool, error) {
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache, &full)
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
	return preindexProviderSnapshotWithPersistenceReader(
		ctx, repo, providerVersion, options, cacheDir, readSearchSnapshot,
	)
}

func preindexProviderSnapshotWithPersistenceReader(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	readPersisted func(cacheEntry) (cachedSearchSnapshot, error),
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
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return ProviderSnapshot{}, false, fmt.Errorf("resolve committed HEAD for preindex: %w", err)
	}
	repositoryKey := repoKey(ctx, absRepo)
	var snapshot ProviderSnapshot
	var cacheHit bool
	if options.ForceRebuild {
		// --force: rebuild from HEAD regardless of any cached entry. The fresh
		// snapshot overwrites that entry below so later queries serve it.
		snapshot, err = BuildProviderSnapshotWithOptions(ctx, absRepo, providerVersion, options)
	} else {
		snapshot, cacheHit, err = loadOrBuildSearchSnapshot(ctx, absRepo, providerVersion, options, cacheDir, false, nil)
	}
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if snapshot.Header.Tree != tree {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"preindex snapshot provenance mismatch: got tree %q (commit %q), want tree %q (commit %q); only tree identity is checked",
			snapshot.Header.Tree, snapshot.Header.Commit, tree, commit,
		)
	}
	if cacheHit {
		// A hit is returned only after the persisted entry has been fully decoded
		// and validated, so reading the same artifact again cannot strengthen the
		// durability guarantee.
		return snapshot, true, nil
	}
	if options.ForceRebuild {
		// Match loadOrBuildSearchSnapshot's re-stamp: report the commit this call
		// serves, not whatever HEAD happened to be mid-build on a same-tree race.
		snapshot.Header.Commit = commit
	}
	// Query-time caching is deliberately best effort, but an explicit preindex
	// command promises a durable artifact. Verify that the entry exists and, if
	// the best-effort write failed (or --force asked for a rewrite), persist while
	// surfacing any persistence error.
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	persisted, readErr := readPersisted(entry)
	if options.ForceRebuild || readErr != nil || !validCachedSearchSnapshot(persisted, repositoryKey, providerVersion, tree, options) {
		cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
		if err := writeSearchSnapshot(entry, cache); err != nil {
			return ProviderSnapshot{}, false, fmt.Errorf("persist preindex snapshot: %w", err)
		}
	}
	return snapshot, cacheHit, nil
}

func newCachedSearchSnapshot(providerVersion, commit, tree string, options ProviderSnapshotOptions, snapshot ProviderSnapshot) cachedSearchSnapshot {
	cache := cachedSearchSnapshot{
		CacheVersion:    searchSnapshotCacheVersion,
		ProviderVersion: providerVersion,
		Commit:          commit,
		Tree:            tree,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
		Worktree:        options.Worktree,
		Snapshot:        snapshot,
	}
	for _, file := range snapshot.Files {
		if file.Lines == 0 {
			continue
		}
		if cache.FileLines == nil {
			cache.FileLines = make(map[string]int)
		}
		cache.FileLines[file.ID] = file.Lines
	}
	for _, symbol := range snapshot.Symbols {
		if symbol.Local {
			cache.LocalSymbolIDs = append(cache.LocalSymbolIDs, symbol.ID)
		}
		if symbol.bodyless {
			cache.BodylessSymbolIDs = append(cache.BodylessSymbolIDs, symbol.ID)
		}
		if symbol.sourceEndByte > symbol.sourceStartByte {
			if cache.SymbolByteRanges == nil {
				cache.SymbolByteRanges = make(map[string]cachedSymbolByteRange)
			}
			cache.SymbolByteRanges[symbol.ID] = cachedSymbolByteRange{
				Start: symbol.sourceStartByte,
				End:   symbol.sourceEndByte,
			}
		}
		if symbol.parameterNamesKnown {
			cache.SymbolParameterNamesKnownIDs = append(cache.SymbolParameterNamesKnownIDs, symbol.ID)
		}
		if symbol.signatureTypesKnown {
			if cache.SymbolSignatureTypes == nil {
				cache.SymbolSignatureTypes = make(map[string]cachedSignatureTypes)
			}
			cache.SymbolSignatureTypes[symbol.ID] = cachedSignatureTypes{
				Params:  symbol.paramTypeText,
				Returns: symbol.returnTypeText,
			}
		}
		if len(symbol.parameterNames) > 0 {
			if cache.SymbolParameterNames == nil {
				cache.SymbolParameterNames = make(map[string][]string)
			}
			cache.SymbolParameterNames[symbol.ID] = append([]string(nil), symbol.parameterNames...)
		}
	}
	return cache
}

func restoreCachedSearchInternals(cache *cachedSearchSnapshot) {
	for index := range cache.Snapshot.Files {
		cache.Snapshot.Files[index].Lines = cache.FileLines[cache.Snapshot.Files[index].ID]
	}
	localIDs := make(map[string]bool, len(cache.LocalSymbolIDs))
	for _, id := range cache.LocalSymbolIDs {
		localIDs[id] = true
	}
	bodylessIDs := make(map[string]bool, len(cache.BodylessSymbolIDs))
	for _, id := range cache.BodylessSymbolIDs {
		bodylessIDs[id] = true
	}
	parameterNamesKnownIDs := make(map[string]bool, len(cache.SymbolParameterNamesKnownIDs))
	for _, id := range cache.SymbolParameterNamesKnownIDs {
		parameterNamesKnownIDs[id] = true
	}
	for index := range cache.Snapshot.Symbols {
		symbol := &cache.Snapshot.Symbols[index]
		symbol.Local = localIDs[symbol.ID]
		symbol.bodyless = bodylessIDs[symbol.ID]
		if sourceRange, ok := cache.SymbolByteRanges[symbol.ID]; ok && sourceRange.End > sourceRange.Start {
			symbol.sourceStartByte = sourceRange.Start
			symbol.sourceEndByte = sourceRange.End
		}
		symbol.parameterNames = append([]string(nil), cache.SymbolParameterNames[symbol.ID]...)
		symbol.parameterNamesKnown = parameterNamesKnownIDs[symbol.ID]
		if types, ok := cache.SymbolSignatureTypes[symbol.ID]; ok {
			symbol.paramTypeText = types.Params
			symbol.returnTypeText = types.Returns
			symbol.signatureTypesKnown = true
		}
	}
}

// budgetPollStride is how often the cheap per-record loops in the selective
// derivation poll the wall-clock budget. The loops are linear in the size of the
// COMPLETE cached snapshot, so they must be interruptible, but their bodies are
// far cheaper than a context read; polling every stride keeps the unbudgeted
// path's cost unchanged while bounding the residual to one stride.
const budgetPollStride = 1024

// selectiveSearchSnapshotFromFull derives the same graph that a fresh
// OnlyFiles build would produce. It reuses cached parse output, but deliberately
// reruns relation resolution against only the selected symbols: simply dropping
// cross-boundary edges from a complete graph is wrong because an OnlyFiles build
// externalizes those targets and records different resolution metadata.
// filterFilesAndSymbolsForBudget filters fullFiles and fullSymbols down to
// the files allowedFiles admits, in lockstep, stopping as soon as stop
// reports true.
//
// full.Files and full.Symbols are filtered together, file by file, rather
// than in two independent index-based passes: the streaming builder emits
// one FileRecord followed immediately by every SymbolRecord for that file
// (see the reducer in BuildProviderSnapshotWithOptions), so full.Symbols is
// a run-length grouping in the same file order as full.Files. Filtering them
// separately let expiry land between a file's FileRecord and the tail of
// its SymbolRecords -- or mid-run within the Symbols pass alone -- leaving
// selective.Files claim a file was retained while selective.Symbols held
// none or only some of its symbols. The cold (non-cached) path never
// produces that state: a file it cannot finish in budget is dropped whole.
// This walks both slices in lockstep so a file crossing the boundary is
// dropped whole here too, keeping a "retained" file's symbol set complete
// by construction rather than by the budget's luck.
func filterFilesAndSymbolsForBudget(fullFiles []FileRecord, fullSymbols []SymbolRecord, allowedFiles map[string]bool, stop func() bool) (files []FileRecord, symbols []SymbolRecord) {
	symIndex := 0
filesAndSymbols:
	for fileIndex, file := range fullFiles {
		if fileIndex%budgetPollStride == 0 && stop() {
			break
		}
		runStart := symIndex
		// Polled by SYMBOL, not just by file: the outer poll above only fires
		// once per budgetPollStride FILES, so one pathological file holding
		// far more than that many symbols (a huge generated or vendored
		// source) let this inner run advance through all of them before the
		// outer loop ever got another chance to check. A stop here breaks
		// the OUTER loop too (not just this inner one), because the file
		// this run belongs to has not reached its allowedFiles decision yet
		// and must be dropped whole -- exactly like a stop at the outer poll
		// above already drops the CURRENT file whole -- rather than retained
		// with a truncated, budget-luck-sized symbol slice.
		for symIndex < len(fullSymbols) && fullSymbols[symIndex].FilePath == file.Path {
			if (symIndex-runStart)%budgetPollStride == 0 && stop() {
				break filesAndSymbols
			}
			symIndex++
		}
		if allowedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			files = append(files, file)
			symbols = append(symbols, fullSymbols[runStart:symIndex]...)
		}
	}
	return files, symbols
}

func selectiveSearchSnapshotFromFull(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	full ProviderSnapshot,
) (ProviderSnapshot, error) {
	// Source preparation runs on the caller's context and OUTSIDE the budget
	// clock, exactly as the streaming build does (see StreamSnapshot), so both
	// paths bound the same phases and MaxDuration keeps its single documented
	// meaning: a parse-and-relation ceiling.
	sc, err := prepareSource(ctx, repo, options)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	if sc.close != nil {
		defer sc.close()
	}
	// The ceiling has to be derived HERE, not only inside
	// BuildProviderSnapshotWithOptions. On a cold cache the caller falls
	// through to that build and is bounded; on a warm complete snapshot this
	// derivation re-runs the entire relation phase instead. Observing only the
	// caller's context therefore made the SAME call with the SAME options
	// bounded or unbounded purely as a function of cache state -- measured on a
	// 500-function nested fixture, a 1 ns budget still built 385,137 relations
	// in 78 s once the complete snapshot was cached.
	workCtx := ctx
	var budgetDeadline time.Time
	if options.MaxDuration > 0 {
		var cancelBudget context.CancelFunc
		budgetDeadline = options.now().Add(options.MaxDuration)
		workCtx, cancelBudget = context.WithDeadline(ctx, budgetDeadline)
		defer cancelBudget()
	}
	// The budget is observed against the CLOCK, not only against the context's
	// timer. ctx.Err() flips one timer granularity after the deadline actually
	// passed -- microseconds on Linux and macOS, up to a system tick (~15.6 ms)
	// on Windows -- and the preprocessing below is short enough to finish
	// inside that window, which is exactly how an already-expired budget still
	// filtered the whole cached inventory on the Windows runner. See budgetGate.
	gate := newBudgetGate(workCtx, budgetDeadline, options.now)
	budgetHit := false
	// classifyStop splits a stop reason the way the streaming build does:
	// expiry of an OPT-IN MaxDuration is a truncation to report, anything else
	// (a cancellation, or a deadline that came from the CALLER's own context)
	// is returned. A caller that never asked for truncation must not receive a
	// silently incomplete snapshot with a nil error.
	classifyStop := func(err error) error {
		if err == nil {
			return nil
		}
		if options.MaxDuration > 0 && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			budgetHit = true
			return nil
		}
		return err
	}
	// One stop predicate for the whole derivation, defined HERE rather than at
	// the relation phase: everything below this point -- filtering the complete
	// cached inventory (linear in the FULL snapshot, not the selection) and the
	// per-file importsFor precompute (a content scan per selected file) -- is
	// budgeted work that used to run unchecked, so a selective request could
	// overshoot MaxDuration before the relation phase ever created a predicate.
	shouldStop := func() bool { return budgetHit || gate.expired() }
	// stopNow marks the derivation truncated the same way an expired relation
	// phase does, so the E_ANALYSIS_BUDGET_EXCEEDED marker (and with it the
	// "unsafe" completeness level and the cache writer's refusal) is reached by
	// this path too. A caller cancellation or a caller-owned deadline still has
	// to surface as an error, which classifyStop decides at the sink below.
	stopNow := func() bool {
		if !shouldStop() {
			return false
		}
		if options.MaxDuration > 0 && errors.Is(gate.err(), context.DeadlineExceeded) && ctx.Err() == nil {
			budgetHit = true
		}
		return true
	}
	// Tree (not commit) determines whether the cached full snapshot is a valid
	// derivation source: two different commits sharing a tree parse identically.
	if sc.tree != full.Header.Tree || sc.key != full.Header.RepoKey {
		return ProviderSnapshot{}, fmt.Errorf(
			"cached full snapshot provenance mismatch: got repo %q tree %q, want repo %q tree %q; commit is not part of the check",
			full.Header.RepoKey, full.Header.Tree, sc.key, sc.tree,
		)
	}

	spec := resolveProfile(options.Profile)
	selective := ProviderSnapshot{Header: leanHeader(sc, providerVersion, spec)}
	allowedFiles := make(map[string]bool, len(sc.paths))
	for _, filePath := range sc.paths {
		allowedFiles[filepath.ToSlash(filepath.Clean(filePath))] = true
	}
	selective.Files, selective.Symbols = filterFilesAndSymbolsForBudget(full.Files, full.Symbols, allowedFiles, stopNow)

	if stopNow() {
		if err := classifyStop(gate.err()); err != nil {
			return ProviderSnapshot{}, err
		}
		finalizeSelectiveOrdering(&selective, nil, nil, budgetHit)
		failures := filterSearchPartialFailures(full.Header.PartialFailures, retainedFileSet(selective.Files))
		if budgetHit {
			failures = append(failures, analysisBudgetFailure(options.MaxDuration))
		}
		selective.Header.PartialFailures = failures
		selective.Header.Stats = ProviderStats{
			Files:             len(selective.Files),
			ParsedFiles:       len(selective.Files),
			Symbols:           len(selective.Symbols),
			Relations:         len(selective.Relations),
			PartialFailures:   len(failures),
			CompletenessLevel: completenessLevel(failures, len(selective.Files), len(selective.Files), len(selective.Symbols)),
		}
		return selective, nil
	}

	// recordsByFile only feeds relation resolution below (never the reported
	// selective.Symbols, which is already complete by this point), so
	// stopping it early merely narrows how many of the RETAINED files get a
	// relation pass -- the same truncation the relation phase itself is
	// already allowed to apply per file. Without polling here, a large
	// cached symbol inventory made this grouping and per-file transform pass
	// linear in the number of budgeted symbols exactly like the relation
	// phase, but with no stop check of its own.
	recordsByFile := make(map[string][]SymbolRecord)
	structuralByFile := make(map[string][]structuralSymbol)
	for i, symbol := range selective.Symbols {
		if i%budgetPollStride == 0 && stopNow() {
			break
		}
		recordsByFile[symbol.FilePath] = append(recordsByFile[symbol.FilePath], symbol)
	}
	if spec.name == ProfileSyntaxOnly {
		i := 0
		for filePath, symbols := range recordsByFile {
			if i%budgetPollStride == 0 && stopNow() {
				break
			}
			structuralByFile[filePath] = compactStructuralSymbols(symbols)
			i++
		}
	} else {
		i := 0
		for filePath, symbols := range recordsByFile {
			if i%budgetPollStride == 0 && stopNow() {
				break
			}
			recordsByFile[filePath] = retainedSymbolsForProfile(symbols, spec)
			i++
		}
	}
	// Every content read below this point goes through the budgeted reader, so
	// a producer that scans file content per symbol cannot keep scanning after
	// the budget is gone even though it builds its whole slice before returning.
	budgetedRead := gate.reader(sc.read)
	precomputedImports := make(map[string][]string)
	if spec.name != ProfileSyntaxOnly {
		for _, file := range selective.Files {
			// importsFor reads and scans the whole file, so this is real
			// per-file work and is polled per file, not per stride.
			if stopNow() {
				break
			}
			if !skipFastProfilePerSymbolScan(spec, file.Language) {
				continue
			}
			if content, ok := budgetedRead(file.Path); ok {
				precomputedImports[file.Path] = importsFor(file.Path, content)
			}
		}
	}

	seenRelations := make(map[uint64]struct{})
	externalsByID := make(map[string]ExternalRecord)
	// externalOrder is the first-seen order of every external ID, tracked
	// alongside externalsByID (not derived from it) so a budget-truncated
	// derivation can emit externals in a deterministic order without paying
	// for a sort: see finalizeSelectiveOrdering.
	var externalOrder []string
	relationsByType := make(map[string]int)
	var symbolsByID map[string]SymbolRecord
	var filesByID map[string]FileRecord
	if spec.includeEvidence {
		symbolsByID, filesByID = recordIndexes(selective.Files, recordsByFile, stopNow)
	}
	emitRelation := func(relation RelationRecord) {
		if !spec.emits(relation.Type) {
			return
		}
		if spec.callResolution == "shallow" && !shallowRelationRetained(relation.Type, relation.Resolution) {
			return
		}
		if !spec.includeEvidence {
			relation.Evidence = nil
		}
		if relation.WarningCodes == nil {
			relation.WarningCodes = []string{}
		}
		key := relationDedupKey(relation)
		if _, seen := seenRelations[key]; seen {
			return
		}
		seenRelations[key] = struct{}{}
		for _, id := range []string{relation.FromID, relation.ToID} {
			if strings.HasPrefix(id, "external:") {
				if _, exists := externalsByID[id]; !exists {
					externalOrder = append(externalOrder, id)
				}
				mergeExternalRecord(externalsByID, externalRecordFor(relation, id, symbolsByID, filesByID))
			}
		}
		relationsByType[relation.Type]++
		selective.Relations = append(selective.Relations, relation)
	}
	var relationFailures []PartialFailure
	if spec.name == ProfileSyntaxOnly {
		emitStructuralRelationsCompact(sc.key, selective.Files, structuralByFile, shouldStop, emitRelation)
	} else {
		forEachRelation(workCtx, sc.key, selective.Files, recordsByFile, budgetedRead, precomputedImports, spec, shouldStop, emitRelation, func(failure PartialFailure) {
			relationFailures = append(relationFailures, failure)
		})
		// fileChangesWithRelations runs a `git log` subprocess and returns a
		// fully materialised slice, so the budget is checked before entering it
		// and the subprocess itself runs under workCtx.
		if spec.emits("FILE_CHANGES_WITH") && !shouldStop() {
			for _, relation := range fileChangesWithRelations(workCtx, sc.absRepo, sc.commit, sc.key, selective.Files) {
				if shouldStop() {
					break
				}
				emitRelation(relation)
			}
		}
	}
	// shouldStop deliberately returns no reason, so classify the stop once here:
	// budget expiry marks the derived snapshot truncated, a caller cancellation
	// or a caller deadline is returned.
	//
	// Classify off the GATE, not off workCtx directly. shouldStop is
	// level-triggered against the clock, so between the deadline passing and
	// the context timer firing it stops the derivation while workCtx.Err() is
	// still nil. Reading the raw context here would leave budgetHit false, and
	// a snapshot that is missing everything after the deadline would carry no
	// E_ANALYSIS_BUDGET_EXCEEDED marker -- which is exactly what makes it
	// cacheable, and every later unbudgeted query would be served the gap as
	// the complete index.
	if err := classifyStop(gate.err()); err != nil {
		return ProviderSnapshot{}, err
	}

	finalizeSelectiveOrdering(&selective, externalsByID, externalOrder, budgetHit)

	warnings := sc.warnings
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	failures := filterSearchPartialFailures(full.Header.PartialFailures, retainedFileSet(selective.Files))
	failures = mergePartialFailures(failures, relationFailures)
	if budgetHit {
		// The marker both tells the caller the view is partial and makes the
		// view uncacheable: writeSearchSnapshot refuses any snapshot carrying
		// it, so a truncated derivation can never be served to a later
		// unbudgeted query as the complete index.
		failures = append(failures, analysisBudgetFailure(options.MaxDuration))
	}
	languageSet := make(map[string]struct{})
	completenessLanguages := make(map[string]LanguageCompleteness)
	for _, file := range selective.Files {
		languageSet[file.Language] = struct{}{}
		completeness := completenessLanguages[file.Language]
		completeness.Files++
		completenessLanguages[file.Language] = completeness
	}
	for _, symbol := range selective.Symbols {
		completeness := completenessLanguages[symbol.Language]
		completeness.Symbols++
		completenessLanguages[symbol.Language] = completeness
	}
	unparsedFiles := make(map[string]bool)
	for _, failure := range failures {
		if failure.Code == "E_FILE_TOO_LARGE" || failure.Code == "E_MINIFIED" {
			unparsedFiles[filepath.ToSlash(filepath.Clean(failure.FilePath))] = true
		}
	}
	parsedFiles := 0
	for _, file := range selective.Files {
		if !unparsedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			parsedFiles++
		}
	}
	selective.Header.Languages = sortedKeys(languageSet)
	selective.Header.LanguageTiers = languageTiers(languageSet)
	selective.Header.Warnings = warnings
	selective.Header.PartialFailures = failures
	selective.Header.Stats = ProviderStats{
		Files:             len(selective.Files),
		ParsedFiles:       parsedFiles,
		Symbols:           len(selective.Symbols),
		Relations:         len(selective.Relations),
		PartialFailures:   len(failures),
		CompletenessLevel: completenessLevel(failures, len(selective.Files), parsedFiles, len(selective.Symbols)),
	}
	selective.Header.Completeness = CompletenessReport{
		Languages: completenessLanguages,
		Relations: relationsByType,
	}
	return selective, nil
}

// finalizeSelectiveOrdering populates selective.Externals from externalsByID
// (ordered by orderedExternalIDs) and sorts selective.Relations for stable
// output -- but only on the complete (budgetHit == false) path.
//
// Sorting selective.Relations is itself O(n log n) work over whatever the
// relation phase accumulated before the gate tripped -- on a 500-function
// nested fixture, that phase alone produced 385,137 relations, and the
// comparator reallocates a concatenated key on every comparison on top of
// that. The caller already turned budget expiry into a truncation rather
// than an error by the time this runs, so a true budgetHit skips the
// (expensive) relations sort entirely instead of letting the advertised
// parse/relation ceiling get blown by its own finalization step.
// selective.Relations is already in a deterministic order without it -- it
// is built by a plain append during the relation phase, never through a
// map, so a truncated snapshot's relations come out in the same order for
// the same tree and budget every time; skipping the sort trades
// "alphabetical" for "scan order", not determinism for nondeterminism.
func finalizeSelectiveOrdering(selective *ProviderSnapshot, externalsByID map[string]ExternalRecord, externalOrder []string, budgetHit bool) {
	for _, id := range orderedExternalIDs(externalsByID, externalOrder, budgetHit) {
		selective.Externals = append(selective.Externals, externalsByID[id])
	}
	if !budgetHit {
		sort.Slice(selective.Relations, func(i, j int) bool {
			left := selective.Relations[i].Type + selective.Relations[i].FromID + selective.Relations[i].ToID
			right := selective.Relations[j].Type + selective.Relations[j].FromID + selective.Relations[j].ToID
			return left < right
		})
	}
}

// The relation-phase failures recorded during selective derivation are merged
// via mergePartialFailures (provider.go), skipping records the (filtered)
// full-build failures already carry for the same file and code.
func filterSearchPartialFailures(failures []PartialFailure, allowedFiles map[string]bool) []PartialFailure {
	filtered := make([]PartialFailure, 0, len(failures))
	for _, failure := range failures {
		if failure.FilePath == "" || allowedFiles[filepath.ToSlash(filepath.Clean(failure.FilePath))] {
			filtered = append(filtered, failure)
		}
	}
	return filtered
}

func retainedFileSet(files []FileRecord) map[string]bool {
	retained := make(map[string]bool, len(files))
	for _, file := range files {
		retained[filepath.ToSlash(filepath.Clean(file.Path))] = true
	}
	return retained
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
	return loadOrBuildSearchSnapshot(ctx, repo, providerVersion, options, cacheDir, disableCache, nil)
}

// searchSnapshotKey is deliberately tree-only, not commit-keyed: parsing is a
// pure function of tree content, so any commit whose tree matches an existing
// entry can reuse it (e.g. --allow-empty commits, amends, rebases that don't
// touch content). Commit is provenance metadata carried on the cached value
// and re-stamped to the serving HEAD on load; it never influences the key.
// This is scoped to the parsed graph itself: a full-profile snapshot also
// embeds FILE_CHANGES_WITH co-change relations derived by walking recent git
// history (see fileChangesWithRelations), so a same-tree hit after a rebase
// can serve co-change edges computed against the prior history. That is
// accepted because those edges are heuristic and confidence-scored, not
// exact facts about the tree.
func searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(searchSnapshotCacheVersion)
	writePart(absRepo)
	writePart(repositoryKey)
	writePart(providerVersion)
	writePart(tree)
	writePart(string(options.Profile))
	writePart(fmt.Sprintf("%d", options.MaxParseBytes))
	// The resolved file cap SHAPES THE GRAPH: a run capped at N files produces a snapshot missing
	// everything past N, and without the cap in the key that truncated snapshot is served to a later
	// uncapped caller. Measured on this repo: a cap-5 build wrote 28 symbols, and the next uncapped
	// search was served those 28 instead of rebuilding to 5740 — 99.5% of the graph silently absent.
	// One capped ingest therefore poisons every later query on the same tree.
	//
	// RESOLVED, not raw: the cap that shaped the build is resolveMaxSourceFiles(options.MaxFiles),
	// which falls back to ENTIRE_GRAPH_MAX_FILES and then to the default. Hashing options.MaxFiles
	// left the env var out of the key entirely, so an ENTIRE_GRAPH_MAX_FILES=1 index and an uncapped
	// search both keyed on max-files=0 and shared an entry — the exact poisoning this term exists to
	// prevent, just reached by the other half of the same input.
	//
	// It cuts both ways, which is why the term has to be the resolved value rather than a lower
	// bound: an entry built with a HIGHER cap also survives a later LOWERED one, handing back more
	// of the tree than the caller asked to see. Neither direction announces itself.
	writePart(fmt.Sprintf("max-files=%d", resolveMaxSourceFiles(options.MaxFiles)))
	// Working-tree entries live in their own key space. The marker is only
	// written for them so committed-tree keys — and every cache already on disk
	// built under them — stay byte-identical.
	if options.Worktree {
		writePart("worktree")
	}
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writePart("only-files")
	for _, filePath := range onlyFiles {
		writePart(filepath.ToSlash(filepath.Clean(filePath)))
	}
	for groupIndex, group := range [][]string{options.IgnoreFiles, options.IncludeFiles} {
		writePart(fmt.Sprintf("path-group-%d", groupIndex))
		// Preserve caller order: ignore matching is last-rule-wins, including
		// across repeatable ignore/include files within each group.
		for _, path := range group {
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
	// The repo-root .graphignore is applied implicitly, so it must key the entry
	// exactly as an explicit --ignore-file does. Without it, editing
	// .graphignore against an unchanged tree hits the old entry and the new
	// rules silently do nothing.
	writePart("graphignore")
	graphIgnore := filepath.Join(absRepo, graphIgnoreFileName)
	switch content, err := os.ReadFile(graphIgnore); {
	case err == nil:
		_, _ = hash.Write(content)
		writePart("")
	case errors.Is(err, os.ErrNotExist):
		writePart("missing")
	default:
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// validCachedSearchSnapshot deliberately does not compare commit: the cache is
// tree-keyed, so an entry built at a different commit sharing this tree is a
// valid hit. Callers that serve a cached snapshot re-stamp the commit to the
// serving HEAD via restampCachedSearchSnapshotCommit before returning it;
// other call sites (e.g. PreindexProviderSnapshot's persisted-entry check)
// use this function only as a persistence check and never hand the cached
// value back to a caller, so they have no re-stamping to do.
func validCachedSearchSnapshot(cache cachedSearchSnapshot, repositoryKey, providerVersion, tree string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == searchSnapshotCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.RepoKey == repositoryKey &&
		// Both identity gates are required and neither implies the other: RepoKey
		// separates two checkouts that share a tree hash, Worktree separates the two
		// VIEWS of one checkout. A clean working tree is now cacheable
		// (worktreeSnapshotCacheable), so a working-tree entry and a committed-tree
		// entry can have the same repo, tree and profile while carrying different
		// provenance (W_WORKTREE_SNAPSHOT) — without this they would collide.
		cache.Worktree == options.Worktree &&
		cache.Snapshot.Header.Tree == tree &&
		cache.Snapshot.Header.Provider == ProviderName &&
		cache.Snapshot.Header.Profile == string(options.Profile)
}

// restampCachedSearchSnapshotCommit rewrites a loaded cache entry's commit
// provenance to the commit we are actually serving. Tree determines the
// parsed graph, so a same-tree cache hit from a different (empty, amended,
// rebased) commit is exactly correct content-wise; commit is provenance
// metadata layered on top and must reflect the serving HEAD, never the
// possibly-stale commit recorded when the entry was built. This is not just
// provenance cosmetics: query time also reads Header.Commit back out as the
// git treeish for content reads (see openSearchContentReader in search.go),
// so serving a stale commit here could point those reads at a dangling or
// wrong revision.
func restampCachedSearchSnapshotCommit(cache cachedSearchSnapshot, commit string) cachedSearchSnapshot {
	cache.Commit = commit
	cache.Snapshot.Header.Commit = commit
	return cache
}

func readSearchSnapshot(entry cacheEntry) (cachedSearchSnapshot, error) {
	file, err := entry.open()
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
	restoreCachedSearchInternals(&cache)
	return cache, nil
}

// ErrTruncatedSnapshotNotCacheable is returned when a caller tries to persist a
// snapshot that stopped at its wall-clock ceiling. The search cache is keyed by
// tree and by the options that SHAPE the graph -- never by the budget, which
// only truncates it -- so a stored truncation would be served to every later
// query on this tree, including queries that passed no budget, as though it were
// the complete index. Missing symbols would then read as confident negatives.
// The check lives in the writer rather than at the call sites so that a new call
// site cannot reintroduce the hole.
var ErrTruncatedSnapshotNotCacheable = errors.New("refusing to cache a snapshot truncated by its wall-clock budget (E_ANALYSIS_BUDGET_EXCEEDED)")

func writeSearchSnapshot(entry cacheEntry, cache cachedSearchSnapshot) error {
	if partialFailuresTruncated(cache.Snapshot.Header.PartialFailures) {
		return ErrTruncatedSnapshotNotCacheable
	}
	path := entry.writePath()
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
