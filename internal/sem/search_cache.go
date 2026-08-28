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
)

// searchSnapshotCacheVersion names the on-disk cache directory and is hashed
// into every entry key, so bumping it moves new entries to a fresh directory
// and any prior-version directory can simply be deleted wholesale — cleanup is
// "remove old version dirs" instead of per-entry reachability analysis.
// v12 retires every entry produced before the final immutable-policy,
// unconditional-worktree-bypass transaction checks, and shared nested-ignore
// resource policy were complete.
const searchSnapshotCacheVersion = "search-snapshot-v12"

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
	// Worktree is retained for decoding and validating older in-memory fixtures.
	// Persistent worktree caching is disabled because Git listing inputs and
	// working-tree contents cannot be pinned to one atomic cache transaction.
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

// worktreeSnapshotCacheable deliberately rejects every working-tree snapshot.
// A cleanliness comparison cannot pin Git's listing, info/exclude, nested
// ignore files, and filesystem content to one atomic view; A→B→A changes can
// therefore evade any pre/post recheck and poison a persistent entry.
func worktreeSnapshotCacheable(_ context.Context, _ string, options ProviderSnapshotOptions) bool {
	return !options.Worktree
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
	capturedOptions, captureErr := ensureProviderCachePolicy(absRepo, options)
	if captureErr != nil {
		// Cache policy is stricter than sequential matcher construction because
		// it retains every input at once. An optional cache must not make an
		// otherwise valid query fail; let the caller continue to a cold build.
		return ProviderSnapshot{}, false, nil
	}
	options = capturedOptions
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
	capturedOptions, captureErr := ensureProviderCachePolicy(absRepo, options)
	if captureErr != nil {
		// Capture can reject an aggregate input set that the sequential matcher
		// can safely consume. Preserve cache/no-cache behavior parity by taking
		// the existing uncached build path; ordinary input errors still surface
		// from that build.
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	options = capturedOptions
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
			if validateBuiltSearchSnapshot(selective, repositoryKey, providerVersion, tree, options) != nil {
				// The derivation reopens the repository to enumerate the selective
				// source. If identity or HEAD moved since this transaction was keyed,
				// discard the result and take the ordinary build path below.
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
	if err := validateBuiltSearchSnapshot(snapshot, repositoryKey, providerVersion, tree, options); err != nil {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"search snapshot provenance changed while building from commit %q tree %q: %w",
			commit, tree, err,
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
	options, err = CaptureProviderCachePolicy(absRepo, options)
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
	if err := validateBuiltSearchSnapshot(snapshot, repositoryKey, providerVersion, tree, options); err != nil {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"preindex snapshot provenance mismatch for commit %q tree %q: %w",
			commit, tree, err,
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

// validateBuiltSearchSnapshot closes the transaction between cache keying and
// snapshot construction. Git tree identity alone is enough for source bytes,
// but repository identity participates in stable symbol IDs, while provider
// version and profile select the shape of the graph. A concurrent config or
// option change must therefore fail before the snapshot is returned or stored.
// Commit is deliberately excluded: different commits with the same tree have
// identical graph content and are re-stamped to the commit captured by the
// caller after this validation succeeds.
func validateBuiltSearchSnapshot(
	snapshot ProviderSnapshot,
	repositoryKey, providerVersion, tree string,
	options ProviderSnapshotOptions,
) error {
	header := snapshot.Header
	if header.Tree != tree ||
		header.RepoKey != repositoryKey ||
		header.Provider != ProviderName ||
		header.ProviderVersion != providerVersion ||
		header.Profile != string(options.Profile) {
		return fmt.Errorf(
			"got repo %q tree %q provider %q version %q profile %q; want repo %q tree %q provider %q version %q profile %q",
			header.RepoKey, header.Tree, header.Provider, header.ProviderVersion, header.Profile,
			repositoryKey, tree, ProviderName, providerVersion, options.Profile,
		)
	}
	return nil
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

// selectiveSearchSnapshotFromFull derives the same graph that a fresh
// OnlyFiles build would produce. It reuses cached parse output, but deliberately
// reruns relation resolution against only the selected symbols: simply dropping
// cross-boundary edges from a complete graph is wrong because an OnlyFiles build
// externalizes those targets and records different resolution metadata.
func selectiveSearchSnapshotFromFull(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	full ProviderSnapshot,
) (ProviderSnapshot, error) {
	sc, err := prepareSource(ctx, repo, options)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	if sc.close != nil {
		defer sc.close()
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
	for _, file := range full.Files {
		if allowedFiles[filepath.ToSlash(filepath.Clean(file.Path))] {
			selective.Files = append(selective.Files, file)
		}
	}
	for _, symbol := range full.Symbols {
		if allowedFiles[filepath.ToSlash(filepath.Clean(symbol.FilePath))] {
			selective.Symbols = append(selective.Symbols, symbol)
		}
	}

	recordsByFile := make(map[string][]SymbolRecord)
	structuralByFile := make(map[string][]structuralSymbol)
	for _, symbol := range selective.Symbols {
		recordsByFile[symbol.FilePath] = append(recordsByFile[symbol.FilePath], symbol)
	}
	if spec.name == ProfileSyntaxOnly {
		for filePath, symbols := range recordsByFile {
			structuralByFile[filePath] = compactStructuralSymbols(symbols)
		}
	} else {
		for filePath, symbols := range recordsByFile {
			recordsByFile[filePath] = retainedSymbolsForProfile(symbols, spec)
		}
	}
	precomputedImports := make(map[string][]string)
	if spec.name != ProfileSyntaxOnly {
		for _, file := range selective.Files {
			if !skipFastProfilePerSymbolScan(spec, file.Language) {
				continue
			}
			if content, ok := sc.read(file.Path); ok {
				precomputedImports[file.Path] = importsFor(file.Path, content)
			}
		}
	}

	seenRelations := make(map[uint64]struct{})
	externalsByID := make(map[string]ExternalRecord)
	relationsByType := make(map[string]int)
	var symbolsByID map[string]SymbolRecord
	var filesByID map[string]FileRecord
	if spec.includeEvidence {
		symbolsByID, filesByID = recordIndexes(selective.Files, recordsByFile)
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
				mergeExternalRecord(externalsByID, externalRecordFor(relation, id, symbolsByID, filesByID))
			}
		}
		relationsByType[relation.Type]++
		selective.Relations = append(selective.Relations, relation)
	}
	var relationFailures []PartialFailure
	if spec.name == ProfileSyntaxOnly {
		emitStructuralRelationsCompact(sc.key, selective.Files, structuralByFile, emitRelation)
	} else {
		forEachRelation(sc.key, selective.Files, recordsByFile, sc.read, precomputedImports, spec, func() bool {
			return ctx.Err() != nil
		}, emitRelation, func(failure PartialFailure) {
			relationFailures = append(relationFailures, failure)
		})
		if spec.emits("FILE_CHANGES_WITH") {
			for _, relation := range fileChangesWithRelations(ctx, sc.absRepo, sc.commit, sc.key, selective.Files) {
				if ctx.Err() != nil {
					break
				}
				emitRelation(relation)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return ProviderSnapshot{}, err
	}

	externalIDs := make([]string, 0, len(externalsByID))
	for id := range externalsByID {
		externalIDs = append(externalIDs, id)
	}
	sort.Strings(externalIDs)
	for _, id := range externalIDs {
		selective.Externals = append(selective.Externals, externalsByID[id])
	}
	sort.Slice(selective.Relations, func(i, j int) bool {
		left := selective.Relations[i].Type + selective.Relations[i].FromID + selective.Relations[i].ToID
		right := selective.Relations[j].Type + selective.Relations[j].FromID + selective.Relations[j].ToID
		return left < right
	})

	warnings := sc.warnings
	if warnings == nil {
		warnings = []ProviderWarning{}
	}
	failures := filterSearchPartialFailures(full.Header.PartialFailures, allowedFiles)
	failures = mergePartialFailures(failures, relationFailures)
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
		CompletenessLevel: completenessLevel(completenessFailureCount(failures), len(selective.Files), parsedFiles, len(selective.Symbols)),
	}
	selective.Header.Completeness = CompletenessReport{
		Languages: completenessLanguages,
		Relations: relationsByType,
	}
	return selective, nil
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
	if options.Worktree {
		return "", errors.New("working-tree snapshots cannot have persistent cache keys")
	}
	policy, err := cachePolicyForOptions(absRepo, options)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writeCacheKeyString(hash, "cache-version", searchSnapshotCacheVersion)
	writeCacheKeyString(hash, "repository-path", absRepo)
	writeCacheKeyString(hash, "repository-key", repositoryKey)
	writeCacheKeyString(hash, "provider-version", providerVersion)
	writeCacheKeyString(hash, "tree", tree)
	writeCacheKeyString(hash, "profile", string(options.Profile))
	writeCacheKeyString(hash, "max-parse-bytes", fmt.Sprintf("%d", options.MaxParseBytes))
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
	writeCacheKeyString(hash, "max-files", fmt.Sprintf("%d", resolveMaxSourceFiles(options.MaxFiles)))
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writeCacheKeyString(hash, "only-files", "begin")
	for _, filePath := range onlyFiles {
		writeCacheKeyString(hash, "only-file", filepath.ToSlash(filepath.Clean(filePath)))
	}
	// The repo-root .graphignore is applied implicitly, so it must key the entry
	// exactly as an explicit --ignore-file does. Without it, editing
	// .graphignore against an unchanged tree hits the old entry and the new
	// rules silently do nothing.
	// The built-in credential-store deny is applied implicitly too, after the
	// repository's own exclude files, so it keys the entry for the same reason
	// .graphignore does: it decides which files the snapshot may contain, and
	// therefore which files the ranked payload, the snippets and the context blocks
	// can quote. See builtinSecretRulesDigest.
	writeCacheKeyString(hash, "builtin-secret-rules", builtinSecretRulesDigest())
	policy.writeCacheKey(hash)
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
		// The stored header records the schema its records were serialized under, and
		// nothing else here separates two schemas: searchSnapshotCacheVersion tracks
		// the caching machinery, and providerVersion is the constant "dev" for every
		// local build and "v0.0.0-ci" for every non-tag CI build. Without this clause
		// a binary at schema N serves a snapshot built at schema N-1 as its own.
		cache.Snapshot.Header.SchemaVersion == SchemaVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.RepoKey == repositoryKey &&
		// Both identity gates remain required for decoding older entries: RepoKey
		// separates two checkouts that share a tree hash, while Worktree prevents a
		// retired working-tree entry from ever serving a committed-tree request.
		cache.Worktree == options.Worktree &&
		cache.Snapshot.Header.Tree == tree &&
		cache.Snapshot.Header.Provider == ProviderName &&
		cache.Snapshot.Header.ProviderVersion == providerVersion &&
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

func writeSearchSnapshot(entry cacheEntry, cache cachedSearchSnapshot) error {
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
