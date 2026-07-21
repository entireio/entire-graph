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

const searchSnapshotCacheVersion = "search-snapshot-v5"

type cachedSymbolByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type cachedSearchSnapshot struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Commit          string           `json:"commit"`
	Tree            string           `json:"tree"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Snapshot        ProviderSnapshot `json:"snapshot"`
	// FileRecord.Lines, SymbolRecord.Local, and exact symbol byte ranges are
	// intentionally absent from the public wire format, but relation resolution
	// consumes them. Preserve those internal fields so a complete preindex can
	// derive an exact selective view without reparsing source files.
	FileLines            map[string]int                   `json:"file_lines,omitempty"`
	LocalSymbolIDs       []string                         `json:"local_symbol_ids,omitempty"`
	SymbolByteRanges     map[string]cachedSymbolByteRange `json:"symbol_byte_ranges,omitempty"`
	SymbolParameterNames map[string][]string              `json:"symbol_parameter_names,omitempty"`
}

// loadOrBuildSearchGraphSnapshot preserves the exact candidate-file scope even
// when a complete committed-tree snapshot is published concurrently. The
// shared loader derives an OnlyFiles view from that full snapshot and reports a
// cache hit, so cache timing cannot change the graph search receives.
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
	if cacheDir == "" || options.Worktree {
		return ProviderSnapshot{}, false, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
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
	fullKey, keyErr := searchSnapshotKey(absRepo, repositoryKey, providerVersion, commit, tree, fullOptions)
	if keyErr != nil {
		return ProviderSnapshot{}, false, keyErr
	}
	fullPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")
	cached, readErr := readSearchSnapshot(fullPath)
	if readErr != nil || !validCachedSearchSnapshot(cached, repositoryKey, providerVersion, commit, tree, fullOptions) {
		return ProviderSnapshot{}, false, nil
	}
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
	if disableCache || cacheDir == "" || options.Worktree {
		snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	commit, tree, headErr := resolveCommittedHEAD(ctx, absRepo)
	if headErr != nil {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	repositoryKey := repoKey(ctx, absRepo)
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, commit, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	if cached, err := readSearchSnapshot(path); err == nil && validCachedSearchSnapshot(cached, repositoryKey, providerVersion, commit, tree, options) {
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
			_ = writeSearchSnapshot(path, newCachedSearchSnapshot(providerVersion, commit, tree, options, selective))
			return selective, true
		}
		if preloadedFull != nil {
			if selective, ok := deriveFromFull(*preloadedFull); ok {
				return selective, true, nil
			}
		}
		fullOptions := options
		fullOptions.OnlyFiles = nil
		fullKey, keyErr := searchSnapshotKey(absRepo, repositoryKey, providerVersion, commit, tree, fullOptions)
		if keyErr != nil {
			return ProviderSnapshot{}, false, keyErr
		}
		fullPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")
		if cached, readErr := readSearchSnapshot(fullPath); readErr == nil && validCachedSearchSnapshot(cached, repositoryKey, providerVersion, commit, tree, fullOptions) {
			if selective, ok := deriveFromFull(cached.Snapshot); ok {
				return selective, true, nil
			}
		}
	}
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if searchSnapshotProvenanceChanged(snapshot.Header, commit, tree) {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"HEAD changed while building search snapshot: got commit %q tree %q, started at commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
	// Cache persistence is best effort. Retrieval correctness never depends on
	// a writable cache directory.
	_ = writeSearchSnapshot(path, cache)
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
// state cannot be represented by a durable commit-keyed cache safely.
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
	commit, tree, err := resolveCommittedHEAD(ctx, absRepo)
	if err != nil {
		return ProviderSnapshot{}, false, fmt.Errorf("resolve committed HEAD for preindex: %w", err)
	}
	repositoryKey := repoKey(ctx, absRepo)
	snapshot, cacheHit, err := loadOrBuildSearchSnapshot(ctx, absRepo, providerVersion, options, cacheDir, false, nil)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	if searchSnapshotProvenanceChanged(snapshot.Header, commit, tree) {
		return ProviderSnapshot{}, false, fmt.Errorf(
			"preindex snapshot provenance mismatch: got commit %q tree %q, want commit %q tree %q",
			snapshot.Header.Commit, snapshot.Header.Tree, commit, tree,
		)
	}
	// Query-time caching is deliberately best effort, but an explicit preindex
	// command promises a durable artifact. Verify that the entry exists and, if
	// the best-effort write failed, retry while surfacing the persistence error.
	key, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, commit, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	persisted, readErr := readSearchSnapshot(path)
	if readErr != nil || !validCachedSearchSnapshot(persisted, repositoryKey, providerVersion, commit, tree, options) {
		cache := newCachedSearchSnapshot(providerVersion, commit, tree, options, snapshot)
		if err := writeSearchSnapshot(path, cache); err != nil {
			return ProviderSnapshot{}, false, fmt.Errorf("persist preindex snapshot: %w", err)
		}
	}
	return snapshot, cacheHit, nil
}

// searchSnapshotProvenanceChanged reports whether a freshly built snapshot's
// provenance no longer matches the HEAD resolved before the build. Only
// commit and tree participate: the repository key is derived from `git
// remote` at read time and silently falls back to local/<basename> on a
// transient git failure, so comparing two independently resolved keys would
// abort valid builds whose tree provenance is intact. Repository-identity
// changes are still honored on later reads by validCachedSearchSnapshot,
// which keys cache validity on the header's RepoKey.
func searchSnapshotProvenanceChanged(header SnapshotHeader, commit, tree string) bool {
	return header.Commit != commit || header.Tree != tree
}

func newCachedSearchSnapshot(providerVersion, commit, tree string, options ProviderSnapshotOptions, snapshot ProviderSnapshot) cachedSearchSnapshot {
	cache := cachedSearchSnapshot{
		CacheVersion:    searchSnapshotCacheVersion,
		ProviderVersion: providerVersion,
		Commit:          commit,
		Tree:            tree,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
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
		if symbol.sourceEndByte > symbol.sourceStartByte {
			if cache.SymbolByteRanges == nil {
				cache.SymbolByteRanges = make(map[string]cachedSymbolByteRange)
			}
			cache.SymbolByteRanges[symbol.ID] = cachedSymbolByteRange{
				Start: symbol.sourceStartByte,
				End:   symbol.sourceEndByte,
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
	for index := range cache.Snapshot.Symbols {
		symbol := &cache.Snapshot.Symbols[index]
		symbol.Local = localIDs[symbol.ID]
		if sourceRange, ok := cache.SymbolByteRanges[symbol.ID]; ok && sourceRange.End > sourceRange.Start {
			symbol.sourceStartByte = sourceRange.Start
			symbol.sourceEndByte = sourceRange.End
		}
		symbol.parameterNames = append([]string(nil), cache.SymbolParameterNames[symbol.ID]...)
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
	if sc.commit != full.Header.Commit || sc.tree != full.Header.Tree || sc.key != full.Header.RepoKey {
		return ProviderSnapshot{}, fmt.Errorf(
			"cached full snapshot provenance mismatch: got repo %q commit %q tree %q, want repo %q commit %q tree %q",
			full.Header.RepoKey, full.Header.Commit, full.Header.Tree, sc.key, sc.commit, sc.tree,
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
		if relation.Type == "CALLS" && spec.callResolution == "shallow" && relation.Resolution != "exact" {
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
		CompletenessLevel: completenessLevel(len(failures), len(selective.Files)),
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

// LoadOrBuildProviderSnapshot reuses the commit-keyed, option-keyed compressed
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

func searchSnapshotKey(absRepo, repositoryKey, providerVersion, commit, tree string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(searchSnapshotCacheVersion)
	writePart(absRepo)
	writePart(repositoryKey)
	writePart(providerVersion)
	writePart(commit)
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
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validCachedSearchSnapshot(cache cachedSearchSnapshot, repositoryKey, providerVersion, commit, tree string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == searchSnapshotCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Commit == commit &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.RepoKey == repositoryKey &&
		cache.Snapshot.Header.Commit == commit &&
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
	restoreCachedSearchInternals(&cache)
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
