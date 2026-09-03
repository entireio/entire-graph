package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func testCacheEntry(t *testing.T) cacheEntry {
	t.Helper()
	entry, err := newCacheEntry(t.TempDir(), "test-cache", "v1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestSearchSnapshotCachePreservesPrivateSymbolMetadata(t *testing.T) {
	snapshot := ProviderSnapshot{Symbols: []SymbolRecord{
		{
			ID:                  "symbol-id",
			sourceStartByte:     17,
			sourceEndByte:       43,
			parameterNames:      []string{"B", "value"},
			parameterNamesKnown: true,
			paramTypeText:       "Input, int",
			returnTypeText:      "Output",
			signatureTypesKnown: true,
		},
		{
			ID:                  "zero-parameter-symbol",
			parameterNamesKnown: true,
			signatureTypesKnown: true,
		},
	}}
	cache := newCachedSearchSnapshot("test-version", "commit", "tree", ProviderSnapshotOptions{Profile: ProfileFull}, snapshot)
	entry := testCacheEntry(t)
	if err := writeSearchSnapshot(entry, cache); err != nil {
		t.Fatal(err)
	}
	restored, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot.Symbols) != 2 {
		t.Fatalf("restored symbols = %#v", restored.Snapshot.Symbols)
	}
	symbol := restored.Snapshot.Symbols[0]
	if symbol.sourceStartByte != 17 || symbol.sourceEndByte != 43 {
		t.Fatalf("restored byte range = [%d,%d), want [17,43)", symbol.sourceStartByte, symbol.sourceEndByte)
	}
	if !reflect.DeepEqual(symbol.parameterNames, []string{"B", "value"}) {
		t.Fatalf("restored private parameter names = %#v", symbol.parameterNames)
	}
	if !symbol.parameterNamesKnown {
		t.Fatal("restored symbol lost known parameter metadata")
	}
	// Signature types must survive too: a cache hit that lost them would fall
	// back to the signature-string split and emit different type relations than
	// the cold run that produced the cache.
	if symbol.paramTypeText != "Input, int" || symbol.returnTypeText != "Output" || !symbol.signatureTypesKnown {
		t.Fatalf("restored signature types = params %q, returns %q, known %t",
			symbol.paramTypeText, symbol.returnTypeText, symbol.signatureTypesKnown)
	}
	zeroParameterSymbol := restored.Snapshot.Symbols[1]
	if !zeroParameterSymbol.signatureTypesKnown || zeroParameterSymbol.returnTypeText != "" {
		t.Fatalf("restored no-return-type metadata = known %t, returns %q",
			zeroParameterSymbol.signatureTypesKnown, zeroParameterSymbol.returnTypeText)
	}
	if !zeroParameterSymbol.parameterNamesKnown || len(zeroParameterSymbol.parameterNames) != 0 {
		t.Fatalf("restored zero-parameter metadata = known %t, names %#v", zeroParameterSymbol.parameterNamesKnown, zeroParameterSymbol.parameterNames)
	}
}

func TestSearchCacheLoadersRejectSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "target.go", "package target\nfunc Needle() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	outsideCacheDir := filepath.Join(parent, "outside")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Profile: ProfileSyntaxOnly}
	full, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", options, outsideCacheDir)
	if err != nil {
		t.Fatalf("seed complete cache: %v", err)
	}
	selectiveOptions := options
	selectiveOptions.OnlyFiles = []string{"target.go"}
	if _, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, outsideCacheDir, false); err != nil {
		t.Fatalf("seed selective cache: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside", "search"), filepath.Join(cacheDir, "search")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", options, cacheDir); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("complete cache loader followed a symlink outside the opened root")
	}

	// A populated selective entry would be an immediate hit if the primary
	// load followed the escaping symlink.
	if _, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, false); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("selective cache loader followed a symlink outside the opened root")
	}

	// Remove the selective artifact written by that best-effort miss. The full
	// entry remains outside, so this isolates the full-cache fallback read.
	selectiveKey, err := searchSnapshotKey(repo, full.Header.RepoKey, "test-version", full.Header.Tree, selectiveOptions)
	if err != nil {
		t.Fatal(err)
	}
	outsideSelective, err := newCacheEntry(outsideCacheDir, "search", searchSnapshotCacheVersion, selectiveKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(outsideSelective.root, outsideSelective.relative)); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, false); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("full-cache fallback followed a symlink outside the opened root")
	}

	// Force a build so preindex reaches its separate durability read. That READ must reject the
	// escaping symlink, because it is what decides whether outside content is treated as this
	// cache's own. The WRITE that follows must be refused for the same reason and say so: an entry
	// this reader cannot see is one preindex cannot promise, so reporting the refusal is the only
	// honest outcome. Persisting anyway is what wrote the artifact through a symlink the
	// repository could have planted.
	var persistenceReadErr error
	forced := options
	forced.ForceRebuild = true
	_, _, persistErr := preindexProviderSnapshotWithPersistenceReader(
		t.Context(), repo, "test-version", forced, cacheDir,
		func(entry cacheEntry) (cachedSearchSnapshot, error) {
			cached, err := readSearchSnapshot(entry)
			persistenceReadErr = err
			return cached, err
		},
	)
	if persistErr == nil {
		t.Fatal("preindex persisted through a symlink out of the cache directory")
	}
	if !strings.Contains(persistErr.Error(), "is a symlink") {
		t.Fatalf("preindex failure does not name the symlinked component: %v", persistErr)
	}
	if persistenceReadErr == nil {
		t.Fatal("preindex persistence read followed a symlink outside the opened root")
	}
}

// A symlinked family subdirectory used to be tolerated on the write side, as a supported operator
// layout: the family on a larger volume, in a shared cache, outside a container's writable layer.
// It was never any of those things. Every one of them needs a link that LEAVES the cache
// directory, and reads go through os.OpenRoot(cacheDir), which refuses exactly that — so the
// artifact was written on every run and read on none. This pins both halves: the read cannot see
// an artifact behind such a link even when one is placed there directly, and the write no longer
// pretends otherwise. Relocating the cache is what --cache-dir is for, and a symlinked cache
// directory itself still resolves on both sides.
func TestCacheWritesRefuseSymlinkedFamilyDirectories(t *testing.T) {
	parent := t.TempDir()
	cacheDir := filepath.Join(parent, "cache")
	backing := filepath.Join(parent, "backing")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "backing"), filepath.Join(cacheDir, "search")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	entry, err := newCacheEntry(cacheDir, "search", "v1", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	// Place a valid artifact where the link resolves — cache/search/v1/<key> is backing/v1/<key> —
	// by addressing that path directly, and show the reader still cannot reach it through the
	// link. That is why the write may be refused: there was never a hit to lose.
	behindLink, err := newCacheEntry(parent, "backing", "v1", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSearchSnapshot(behindLink, cachedSearchSnapshot{CacheVersion: searchSnapshotCacheVersion, Tree: "planted"}); err != nil {
		t.Fatalf("seed the link target directly: %v", err)
	}
	if _, err := readSearchSnapshot(entry); err == nil {
		t.Error("read reached an artifact through a symlink out of the cache directory")
	}

	if err := writeSearchSnapshot(entry, cachedSearchSnapshot{CacheVersion: searchSnapshotCacheVersion, Tree: "through-symlink"}); err == nil {
		t.Error("write followed a symlink out of the cache directory")
	}
}

func TestValidCachedSearchSnapshotKeysRepoAndIgnoresCommit(t *testing.T) {
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	snapshot := ProviderSnapshot{Header: SnapshotHeader{
		// Real snapshots always stamp the schema they were built under (see
		// newProviderSnapshot); the cache validator now requires it, so a
		// hand-built header has to carry it too.
		SchemaVersion:   SchemaVersion,
		RepoKey:         "github.com/example/repo",
		Commit:          "old-commit",
		Tree:            "tree",
		Provider:        ProviderName,
		ProviderVersion: "test-version",
		Profile:         string(ProfileFull),
	}}
	cache := newCachedSearchSnapshot("test-version", "old-commit", "tree", options, snapshot)
	if !validCachedSearchSnapshot(cache, snapshot.Header.RepoKey, "test-version", "tree", options) {
		t.Fatal("same-repository, same-tree cache entry must be valid")
	}
	if validCachedSearchSnapshot(cache, "github.com/example/other", "test-version", "tree", options) {
		t.Fatal("cache entry from another repository identity must not be reused")
	}
	cache.Snapshot.Header.ProviderVersion = "other-version"
	if validCachedSearchSnapshot(cache, snapshot.Header.RepoKey, "test-version", "tree", options) {
		t.Fatal("cache entry with mismatched snapshot provider version must not be reused")
	}
	cache.Snapshot.Header.ProviderVersion = "test-version"
	cache.Commit = "different-commit"
	cache.Snapshot.Header.Commit = "different-commit"
	if !validCachedSearchSnapshot(cache, snapshot.Header.RepoKey, "test-version", "tree", options) {
		t.Fatal("commit-only drift must not invalidate a tree-keyed cache entry")
	}
}

func TestValidateBuiltSearchSnapshotPinsGraphProvenanceButNotCommit(t *testing.T) {
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	want := SnapshotHeader{
		SchemaVersion:   SchemaVersion,
		Provider:        ProviderName,
		ProviderVersion: "test-version",
		RepoKey:         "github.com/example/repo",
		Commit:          "commit-built-mid-transaction",
		Tree:            "tree",
		Profile:         string(ProfileFull),
	}
	validate := func(header SnapshotHeader) error {
		return validateBuiltSearchSnapshot(
			ProviderSnapshot{Header: header}, want.RepoKey, want.ProviderVersion, want.Tree, options,
		)
	}
	if err := validate(want); err != nil {
		t.Fatalf("matching snapshot rejected: %v", err)
	}
	sameTreeDifferentCommit := want
	sameTreeDifferentCommit.Commit = "another-commit"
	if err := validate(sameTreeDifferentCommit); err != nil {
		t.Fatalf("commit-only drift rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SnapshotHeader)
	}{
		{"schema version missing", func(header *SnapshotHeader) { header.SchemaVersion = "" }},
		{"schema version foreign", func(header *SnapshotHeader) { header.SchemaVersion = "9.9" }},
		{"repository key", func(header *SnapshotHeader) { header.RepoKey = "github.com/example/other" }},
		{"tree", func(header *SnapshotHeader) { header.Tree = "other-tree" }},
		{"provider", func(header *SnapshotHeader) { header.Provider = "other-provider" }},
		{"provider version", func(header *SnapshotHeader) { header.ProviderVersion = "other-version" }},
		{"profile", func(header *SnapshotHeader) { header.Profile = string(ProfileFast) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := want
			test.mutate(&header)
			if err := validate(header); err == nil {
				t.Fatalf("mismatched %s was accepted: %#v", test.name, header)
			}
		})
	}
}

func TestSearchSnapshotMatchesSelectionPinsRepositoryIdentityAndTree(t *testing.T) {
	selection := searchFileSelection{repoKey: "github.com/example/repo", tree: "tree"}
	snapshot := ProviderSnapshot{Header: SnapshotHeader{SchemaVersion: SchemaVersion, RepoKey: selection.repoKey, Tree: selection.tree}}
	if !searchSnapshotMatchesSelection(snapshot, selection) {
		t.Fatal("matching snapshot and selection rejected")
	}
	snapshot.Header.Commit = "commit-drift-does-not-matter"
	if !searchSnapshotMatchesSelection(snapshot, selection) {
		t.Fatal("commit-only drift rejected")
	}
	snapshot.Header.RepoKey = "github.com/example/other"
	if searchSnapshotMatchesSelection(snapshot, selection) {
		t.Fatal("repository-identity drift accepted")
	}
	snapshot.Header.RepoKey = selection.repoKey
	snapshot.Header.Tree = "other-tree"
	if searchSnapshotMatchesSelection(snapshot, selection) {
		t.Fatal("tree drift accepted")
	}
}

func TestMergePartialFailuresDeduplicatesByCodeAndFile(t *testing.T) {
	base := []PartialFailure{{Code: "E_PARSE_TIMEOUT", FilePath: "src/a.ts"}}
	merged := mergePartialFailures(base, []PartialFailure{
		{Code: "E_PARSE_TIMEOUT", FilePath: "src/a.ts", Detail: "duplicate"},
		{Code: "E_PARSE_TIMEOUT", FilePath: "src/b.ts"},
	})
	if len(merged) != 2 {
		t.Fatalf("merged failures = %#v, want the duplicate dropped and the new file kept", merged)
	}
	if merged[1].FilePath != "src/b.ts" {
		t.Fatalf("merged failures = %#v, want src/b.ts appended", merged)
	}
}

func TestSelectiveFastSearchSnapshotPreservesCachedParameterShadows(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/parser.ts", `namespace B { export function parse() {} }
interface Client { parse(): void }
export function run(B: Client) { B.parse(); }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFast}, cacheDir); err != nil {
		t.Fatal(err)
	}
	selective, cacheHit, err := loadOrBuildSearchGraphSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFast, OnlyFiles: []string{"src/parser.ts"}}, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("fast selective snapshot did not derive from complete cache")
	}
	if hasRelationByLastSegment(selective.Relations, "CALLS", "run", "parse") {
		t.Fatalf("cached fast selective snapshot lost parameter shadow: %#v", relationsOfType(selective.Relations, "CALLS"))
	}
}

func TestSelectiveSearchSnapshotUsesCachedSymbolByteRanges(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	source := `namespace A { export function parse(value: string) {} export namespace B { export function parse(value: number) {} } } export function run() { A.B.parse(1); }`
	write(t, repo, "src/parser.ts", source)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil {
		t.Fatal(err)
	}
	selective, cacheHit, err := loadOrBuildSearchGraphSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"src/parser.ts"},
	}, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective snapshot did not derive from the complete cache")
	}
	var calls []RelationRecord
	for _, relation := range selective.Relations {
		if relation.Type == "CALLS" {
			calls = append(calls, relation)
		}
	}
	symbolsByID := make(map[string]SymbolRecord, len(selective.Symbols))
	for _, symbol := range selective.Symbols {
		symbolsByID[symbol.ID] = symbol
	}
	namespaces := jsNamespaceBySymbolID(source, selective.Symbols, jsNamespaceScopes(source))
	if len(calls) != 1 || symbolsByID[calls[0].FromID].Name != "run" || symbolsByID[calls[0].ToID].Name != "parse" || namespaces[calls[0].ToID] != "A.B" || calls[0].Resolution != "exact" {
		t.Fatalf("cached selective namespace calls = %#v, want only exact run -> A.B.parse", calls)
	}
}

func TestLateFullPreindexHitDerivesSelectiveSnapshotAcrossFileBoundary(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/caller.ts", `import { Helper, helperFunction } from "./helper";

export class Caller extends Helper {
  run(): string {
    function localFormatter(value: string): string { return value.trim(); }
    return localFormatter(helperFunction());
  }
}
`)
	write(t, repo, "src/helper.ts", `export class Helper {}
export function helperFunction(): string { return "helper"; }
`)
	write(t, repo, "src/unrelated.ts", `export function unrelated(): boolean { return true; }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	} else if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}

	selectiveOptions := ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"src/caller.ts"},
	}
	// This directly exercises the state reached when SearchRepository misses the
	// complete cache during its initial probe, then another process publishes the
	// full snapshot before the graph loader runs. The late full-cache hit must be
	// derived into the requested OnlyFiles view, never returned verbatim.
	cached, cacheHit, err := loadOrBuildSearchGraphSnapshot(
		t.Context(), repo, "test-version", selectiveOptions, cacheDir, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from complete preindex")
	}
	uncached, _, err := LoadOrBuildProviderSnapshot(
		t.Context(), repo, "test-version", selectiveOptions, cacheDir, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, uncached) {
		t.Fatalf("cached selective snapshot differs from uncached OnlyFiles build:\ncached=%#v\nuncached=%#v", cached, uncached)
	}

	assertSelectiveSnapshotAccounting(t, cached)
	if !hasExternalID(cached.Externals, "external:import:./helper") {
		t.Fatalf("cross-boundary import was not externalized: %#v", cached.Externals)
	}
	if !hasExternalID(cached.Externals, "external:type:Helper") {
		t.Fatalf("cross-boundary superclass was not externalized: %#v", cached.Externals)
	}
	for _, relation := range cached.Relations {
		if strings.Contains(relation.ToID, ":src/helper.ts:") {
			t.Fatalf("selective snapshot retained a relation to an unselected symbol: %#v", relation)
		}
	}
}

func TestFullPreindexSelectiveSnapshotFiltersFailureStats(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "selected.go", "package sample\nfunc Selected() bool { return true }\n")
	write(t, repo, "too_large.go", "package sample\n// "+strings.Repeat("oversized ", 80)+"\n")
	write(t, repo, "not_selected.go", "package sample\nfunc NotSelected() bool { return false }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	baseOptions := ProviderSnapshotOptions{Profile: ProfileFull, MaxParseBytes: 128}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", baseOptions, cacheDir); err != nil {
		t.Fatal(err)
	}
	selectiveOptions := baseOptions
	selectiveOptions.OnlyFiles = []string{"selected.go", "too_large.go"}
	cached, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from complete preindex")
	}
	uncached, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, uncached) {
		t.Fatalf("cached selective failure accounting differs from uncached build:\ncached=%#v\nuncached=%#v", cached.Header, uncached.Header)
	}
	assertSelectiveSnapshotAccounting(t, cached)
	if cached.Header.Stats.Files != 2 || cached.Header.Stats.ParsedFiles != 1 || cached.Header.Stats.PartialFailures != 1 {
		t.Fatalf("unexpected selective failure stats: %#v", cached.Header.Stats)
	}
	if len(cached.Header.PartialFailures) != 1 || cached.Header.PartialFailures[0].FilePath != "too_large.go" {
		t.Fatalf("unexpected selective failures: %#v", cached.Header.PartialFailures)
	}
}

func assertSelectiveSnapshotAccounting(t *testing.T, snapshot ProviderSnapshot) {
	t.Helper()
	if snapshot.Header.Stats.Files != len(snapshot.Files) ||
		snapshot.Header.Stats.Symbols != len(snapshot.Symbols) ||
		snapshot.Header.Stats.Relations != len(snapshot.Relations) ||
		snapshot.Header.Stats.PartialFailures != len(snapshot.Header.PartialFailures) {
		t.Fatalf("header stats do not describe selective records: stats=%#v files=%d symbols=%d relations=%d failures=%d",
			snapshot.Header.Stats,
			len(snapshot.Files),
			len(snapshot.Symbols),
			len(snapshot.Relations),
			len(snapshot.Header.PartialFailures),
		)
	}
	relationCount := 0
	for _, count := range snapshot.Header.Completeness.Relations {
		relationCount += count
	}
	if relationCount != len(snapshot.Relations) {
		t.Fatalf("relation completeness total = %d, want %d: %#v", relationCount, len(snapshot.Relations), snapshot.Header.Completeness.Relations)
	}
	fileCount, symbolCount := 0, 0
	for _, completeness := range snapshot.Header.Completeness.Languages {
		fileCount += completeness.Files
		symbolCount += completeness.Symbols
	}
	if fileCount != len(snapshot.Files) || symbolCount != len(snapshot.Symbols) {
		t.Fatalf("language completeness does not describe selective records: %#v", snapshot.Header.Completeness.Languages)
	}
}

func hasExternalID(externals []ExternalRecord, id string) bool {
	for _, external := range externals {
		if external.ID == id {
			return true
		}
	}
	return false
}

func TestPreindexProviderSnapshotServesSelectiveSearch(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for index := 0; index < 12; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf(
			"package noise\nfunc Noise%d() int { return %d }\n", index, index,
		))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the query-independent preindex request.
func NeedleTarget() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	preindexed, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit the cache")
	}
	if len(preindexed.Files) != 13 {
		t.Fatalf("preindex files = %d, want 13", len(preindexed.Files))
	}

	options := SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 1,
		CacheDir:        cacheDir,
	}
	selectiveProviderOptions := ProviderSnapshotOptions{
		Profile:   ProfileSyntaxOnly,
		OnlyFiles: []string{"target/needle.go"},
	}
	selectiveKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Tree, selectiveProviderOptions)
	if err != nil {
		t.Fatal(err)
	}
	selectivePath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, selectiveKey+".json.gz")
	cached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.Stats.IndexCacheHit {
		t.Fatal("selective search did not reuse the complete preindex cache")
	}
	if cached.Stats.FilesIndexed != 1 {
		t.Fatalf("selective search indexed %d files, want 1", cached.Stats.FilesIndexed)
	}
	if cached.Stats.SymbolsConsidered != 1 {
		t.Fatalf("search considered %d symbols, want the one symbol in the selective cached graph", cached.Stats.SymbolsConsidered)
	}
	if cached.Stats.QueryFilesRead == 0 || cached.Stats.QueryBytesRead == 0 || cached.Stats.QueryFilesRead >= len(preindexed.Files) {
		t.Fatalf("query content reads were not bounded to candidate scope: %#v", cached.Stats)
	}
	if _, statErr := os.Stat(selectivePath); statErr != nil {
		t.Fatalf("warm search did not persist the per-query selective graph cache at %s: %v", selectivePath, statErr)
	}
	if len(cached.Results) == 0 || cached.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("preindexed search lost target: %#v", cached.Results)
	}

	options.DisableCache = true
	uncached, err := SearchRepository(t.Context(), repo, "test-version", "NeedleTarget preindex request", options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached.Results, uncached.Results) {
		t.Fatalf("full-cache selective view changed retrieval: cached=%#v uncached=%#v", cached.Results, uncached.Results)
	}

	_, secondHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !secondHit {
		t.Fatal("second preindex did not reuse the complete cache")
	}
}

func TestFullPreindexSearchMatchesColdSelectiveGraphExpansion(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "target/entry.go", `package target

// NeedlePolicy handles frobnication for a request.
func NeedlePolicy() { helper() }
`)
	write(t, repo, "target/helper.go", `package target

func helper() {}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	const query = "NeedlePolicy frobnication"
	baseOptions := SearchOptions{
		Profile:         ProfileFull,
		TopK:            10,
		MaxIndexedFiles: 1,
	}
	coldOptions := baseOptions
	coldOptions.DisableCache = true
	cold, err := SearchRepository(t.Context(), repo, "test-version", query, coldOptions)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.FilesIndexed != 1 {
		t.Fatalf("cold search indexed %d files, want one", cold.Stats.FilesIndexed)
	}

	cacheDir := t.TempDir()
	preindexed, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first full preindex unexpectedly hit cache")
	}
	warmOptions := baseOptions
	warmOptions.CacheDir = cacheDir
	warm, err := SearchRepository(t.Context(), repo, "test-version", query, warmOptions)
	if err != nil {
		t.Fatal(err)
	}
	if !warm.Stats.IndexCacheHit || warm.Stats.FilesIndexed != 1 {
		t.Fatalf("warm selective search did not reuse the full preindex correctly: %#v", warm.Stats)
	}
	if warm.Stats.SymbolsConsidered >= len(preindexed.Symbols) || warm.Stats.SymbolsConsidered != cold.Stats.SymbolsConsidered {
		t.Fatalf("warm search used a different graph scope: warm=%#v cold=%#v preindex_symbols=%d", warm.Stats, cold.Stats, len(preindexed.Symbols))
	}
	if !reflect.DeepEqual(warm.Results, cold.Results) {
		t.Fatalf("full preindex changed selective graph expansion:\nwarm=%#v\ncold=%#v", warm.Results, cold.Results)
	}
	for _, result := range warm.Results {
		if result.SymbolName == "helper" {
			t.Fatalf("warm selective search expanded across the unselected file boundary: %#v", warm.Results)
		}
	}
}

func TestWarmSelectiveSearchPersistsAndReusesPerQueryCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	for index := 0; index < 12; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%02d.go", index), fmt.Sprintf(
			"package noise\nfunc Noise%d() int { return %d }\n", index, index,
		))
	}
	write(t, repo, "target/needle.go", `package target

// NeedleTarget handles the repeated warm selective query.
func NeedleTarget() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	preindexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	selectiveOptions := ProviderSnapshotOptions{
		Profile:   ProfileSyntaxOnly,
		OnlyFiles: []string{"target/needle.go"},
	}
	selectiveKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Tree, selectiveOptions)
	if err != nil {
		t.Fatal(err)
	}
	selectivePath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, selectiveKey+".json.gz")

	options := SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 1,
		CacheDir:        cacheDir,
	}
	const query = "NeedleTarget warm selective query"
	first, err := SearchRepository(t.Context(), repo, "test-version", query, options)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Stats.IndexCacheHit || first.Stats.FilesIndexed != 1 {
		t.Fatalf("first warm search did not reuse the complete preindex: %#v", first.Stats)
	}
	if len(first.Results) == 0 || first.Results[0].SymbolName != "NeedleTarget" {
		t.Fatalf("first warm search lost target: %#v", first.Results)
	}
	persisted, statErr := os.Stat(selectivePath)
	if statErr != nil {
		t.Fatalf("first warm search did not persist the per-query selective entry at %s: %v", selectivePath, statErr)
	}

	second, err := SearchRepository(t.Context(), repo, "test-version", query, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Stats.IndexCacheHit {
		t.Fatalf("second warm search missed the selective cache: %#v", second.Stats)
	}
	if !reflect.DeepEqual(second.Results, first.Results) {
		t.Fatalf("selective cache entry changed retrieval:\nfirst=%#v\nsecond=%#v", first.Results, second.Results)
	}
	reused, statErr := os.Stat(selectivePath)
	if statErr != nil {
		t.Fatalf("second warm search lost the selective entry: %v", statErr)
	}
	if !reused.ModTime().Equal(persisted.ModTime()) {
		t.Fatalf("second warm search re-derived and rewrote the selective entry: first=%v second=%v", persisted.ModTime(), reused.ModTime())
	}

	// With the complete preindex entry gone, re-derivation is impossible: only
	// the persisted per-query entry can keep the third identical search a hit.
	fullKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Tree, ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, fullKey+".json.gz")); err != nil {
		t.Fatal(err)
	}
	third, err := SearchRepository(t.Context(), repo, "test-version", query, options)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Stats.IndexCacheHit {
		t.Fatalf("persisted selective entry was not reused after preindex removal: %#v", third.Stats)
	}
	if !reflect.DeepEqual(third.Results, first.Results) {
		t.Fatalf("persisted selective entry changed retrieval:\nfirst=%#v\nthird=%#v", first.Results, third.Results)
	}
}

func TestWarmSelectiveDerivationFailureFallsBackToFreshBuild(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "target/entry.go", `package target

// NeedlePolicy handles frobnication for a request.
func NeedlePolicy() { helper() }
`)
	write(t, repo, "target/helper.go", `package target

func helper() {}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	full, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("preindexed complete snapshot did not load")
	}

	// This directly exercises the state reached when SearchRepository probes the
	// complete cache successfully and HEAD's tree then changes before derivation:
	// the stale in-memory snapshot must not fail the search. A same-tree empty
	// commit remains a valid tree-keyed hit and therefore is not a mismatch.
	write(t, repo, "unrelated.go", "package target\nfunc Unrelated() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change tree after preindex probe")
	selectiveOptions := ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"target/entry.go"},
	}
	if _, deriveErr := selectiveSearchSnapshotFromFull(t.Context(), repo, "test-version", selectiveOptions, full); deriveErr == nil {
		t.Fatal("stale complete snapshot no longer fails derivation; fixture does not simulate a provenance mismatch")
	}
	snapshot, cacheHit, err := loadOrDeriveSelectiveSearchSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, false, full)
	if err != nil {
		t.Fatalf("warm derivation failure was not treated as soft: %v", err)
	}
	if cacheHit {
		t.Fatal("stale-preindex fallback unexpectedly reported a cache hit")
	}
	if snapshot.Header.Commit == full.Header.Commit {
		t.Fatalf("fallback did not rebuild at the advanced HEAD: %#v", snapshot.Header)
	}
	fresh, _, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", selectiveOptions, cacheDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, fresh) {
		t.Fatalf("fallback snapshot differs from uncached OnlyFiles build:\nfallback=%#v\nfresh=%#v", snapshot, fresh)
	}
}

func TestIndexAllFilesSearchWritesCanonicalFullSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	_, err := SearchRepository(t.Context(), repo, "test-version", "validate token", SearchOptions{
		Profile:       ProfileSyntaxOnly,
		IndexAllFiles: true,
		CacheDir:      cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("index-all-files search did not populate the canonical full-snapshot cache")
	}
}

func TestWarmNoHitSearchPreservesCachedGraphHealth(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "healthy.go", "package sample\nfunc Healthy() bool { return true }\n")
	write(t, repo, "oversized.go", "package sample\n// "+strings.Repeat("oversized ", 80)+"\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	const maxParseBytes = 128
	preindexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(preindexed.Header.PartialFailures) != 1 {
		t.Fatalf("preindex failures = %#v", preindexed.Header.PartialFailures)
	}

	response, err := SearchRepository(t.Context(), repo, "test-version", "definitely absent retrieval phrase", SearchOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes, CacheDir: cacheDir, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 || !response.Stats.IndexCacheHit {
		t.Fatalf("warm no-hit response = %#v", response)
	}
	if !reflect.DeepEqual(response.PartialFailures, preindexed.Header.PartialFailures) ||
		!reflect.DeepEqual(response.Completeness, preindexed.Header.Completeness) {
		t.Fatalf("no-hit search lost cached graph health: response=%#v preindex=%#v", response, preindexed.Header)
	}
	if response.Stats.QueryFilesRead != 0 || response.Stats.QueryBytesRead != 0 {
		t.Fatalf("no-hit search read repository content: %#v", response.Stats)
	}
	if response.Stats.PreselectionBackend != "git-tree-grep" || response.Stats.PreselectionPasses != 1 ||
		response.Stats.PreselectionFilesExamined != response.Stats.FilesScanned {
		t.Fatalf("no-hit Git full-tree work was hidden by zero blob hydration: %#v", response.Stats)
	}
}

func TestWarmCommittedSearchMatchesExhaustiveResultsWithoutFullContentRescan(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	// Git 2.55 runs the geometric maintenance strategy in a detached process
	// after this repository's larger commit. Keep the fixture deterministic:
	// the production metadata preflight must continue to fail closed while a
	// separate process is actively rewriting the Git administrative tree.
	git(t, repo, "config", "maintenance.auto", "false")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/policy.ts", "export const durableCacheRefreshPolicy = 'eager'\n")
	write(t, repo, "src/consumer.ts", "export function applyRefreshPolicy() { install(durableCacheRefreshPolicy) }\n")
	write(t, repo, "tests/policy.test.ts", "test('refresh policy', () => expect(durableCacheRefreshPolicy).toBeTruthy())\n")
	write(t, repo, "docs/policy.md", "# Durable cache refresh policy\n")
	for index := 0; index < 80; index++ {
		write(t, repo, fmt.Sprintf("noise/file_%03d.ts", index), fmt.Sprintf(
			"export function unrelated%d() { return %d }\n", index, index,
		))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	query := "durable cache refresh policy consumer"
	warm, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
		Profile: ProfileFull, TopK: 10, IndexAllFiles: true, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	exhaustive, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
		Worktree: true, Profile: ProfileFull, TopK: 10, IndexAllFiles: true, DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := func(results []SearchResult) []string {
		out := make([]string, len(results))
		for index, result := range results {
			out[index] = fmt.Sprintf("%s:%d:%d:%s", result.FilePath, result.StartLine, result.EndLine, result.SymbolID)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(identity(warm.Results), identity(exhaustive.Results)) {
		t.Fatalf("optimized results differ from exhaustive retrieval:\nwarm=%#v\nexhaustive=%#v", warm.Results, exhaustive.Results)
	}
	if len(warm.Results) == 0 || warm.Results[0].FilePath != "src/policy.ts" {
		t.Fatalf("query-aware artifact prior did not rank implementation first: %#v", warm.Results)
	}
	if !warm.Stats.IndexCacheHit || warm.Stats.FilesContentRead != 0 {
		t.Fatalf("warm committed search did not use the canonical cache/tree grep: %#v", warm.Stats)
	}
	if warm.Stats.QueryFilesRead >= exhaustive.Stats.QueryFilesRead || warm.Stats.QueryFilesRead > 4 {
		t.Fatalf("warm query reads were not bounded: warm=%#v exhaustive=%#v", warm.Stats, exhaustive.Stats)
	}
	if warm.Stats.UsageFilesRead != 0 || warm.Stats.UsageBytesRead != 0 {
		t.Fatalf("identifier usage cache hits were double-counted as physical reads: %#v", warm.Stats)
	}
	if warm.Stats.UsagePreselectionBackend != "git-tree-grep" || warm.Stats.UsagePreselectionPasses != 1 ||
		warm.Stats.UsagePreselectionFilesExamined != warm.Stats.FilesScanned {
		t.Fatalf("identifier-usage Git scan was not represented honestly: %#v", warm.Stats)
	}
}

func TestWarmCommittedSearchKeepsLexicalMatchesFromPartialFailureFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "large.go", "package sample\n// HiddenLargeNeedle "+strings.Repeat("oversized payload ", 40)+"\n")
	write(t, repo, "healthy.go", "package sample\nfunc Healthy() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	const maxParseBytes = 128
	preindexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull, MaxParseBytes: maxParseBytes,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(preindexed.Header.PartialFailures) != 1 || preindexed.Header.PartialFailures[0].FilePath != "large.go" {
		t.Fatalf("preindex did not record the oversized file: %#v", preindexed.Header.PartialFailures)
	}
	options := SearchOptions{Profile: ProfileFull, MaxParseBytes: maxParseBytes, TopK: 5}
	warmOptions := options
	warmOptions.CacheDir = cacheDir
	warm, err := SearchRepository(t.Context(), repo, "test-version", "HiddenLargeNeedle", warmOptions)
	if err != nil {
		t.Fatal(err)
	}
	exhaustiveOptions := options
	exhaustiveOptions.Worktree = true
	exhaustiveOptions.IndexAllFiles = true
	exhaustiveOptions.DisableCache = true
	exhaustive, err := SearchRepository(t.Context(), repo, "test-version", "HiddenLargeNeedle", exhaustiveOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(warm.Results) == 0 || warm.Results[0].FilePath != "large.go" ||
		len(exhaustive.Results) == 0 || exhaustive.Results[0].FilePath != "large.go" {
		t.Fatalf("partial-failure lexical result was dropped: warm=%#v exhaustive=%#v", warm.Results, exhaustive.Results)
	}
	if warm.Results[0].Snippet != exhaustive.Results[0].Snippet {
		t.Fatalf("optimized partial-failure result differs from exhaustive retrieval: warm=%#v exhaustive=%#v", warm.Results[0], exhaustive.Results[0])
	}
}

func TestCommittedGitPreselectionMatchesExhaustiveUnicodeLowering(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "unicode.go", "package sample\n// KernelNeedle is deliberately spelled with Unicode Kelvin sign.\nfunc KernelNeedle() {}\n// İssueNeedle uses Turkish dotted capital I.\nfunc İssueNeedle() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "unicode case-fold fixture")
	if !strings.Contains(strings.ToLower("KernelNeedle"), "kernelneedle") {
		t.Fatal("Go Unicode lowering no longer maps Kelvin sign to ASCII k")
	}
	if !strings.Contains(strings.ToLower("İssueNeedle"), "issueneedle") {
		t.Fatal("Go Unicode lowering no longer maps dotted capital I to ASCII i")
	}

	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"kernelneedle", "issueneedle"} {
		warm, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
			Profile: ProfileFull, TopK: 5, CacheDir: cacheDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		exhaustive, err := SearchRepository(t.Context(), repo, "test-version", query, SearchOptions{
			Worktree: true, Profile: ProfileFull, TopK: 5, IndexAllFiles: true, DisableCache: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(exhaustive.Results) == 0 || exhaustive.Results[0].FilePath != "unicode.go" {
			t.Fatalf("exhaustive Unicode fixture did not match %q: %#v", query, exhaustive.Results)
		}
		if !reflect.DeepEqual(warm.Results, exhaustive.Results) {
			t.Fatalf("committed Git preselection changed Unicode-fold retrieval for %q:\nwarm=%#v\nexhaustive=%#v", query, warm.Results, exhaustive.Results)
		}
	}
}

func TestPreindexProviderSnapshotRejectsWorktreeAndMissingCache(t *testing.T) {
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{Worktree: true}, t.TempDir()); err == nil {
		t.Fatal("expected worktree preindex to fail")
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), t.TempDir(), "test", ProviderSnapshotOptions{}, ""); err == nil {
		t.Fatal("expected preindex without a cache directory to fail")
	}
}

func TestPreindexWarmCacheHitDoesNotRevalidatePersistedSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\nfunc ValidateToken() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileSyntaxOnly}
	cold, cacheHit, err := PreindexProviderSnapshot(
		t.Context(), repo, "test-version", options, cacheDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("cold preindex unexpectedly hit cache")
	}

	persistenceReads := 0
	warm, cacheHit, err := preindexProviderSnapshotWithPersistenceReader(
		t.Context(), repo, "test-version", options, cacheDir,
		func(entry cacheEntry) (cachedSearchSnapshot, error) {
			persistenceReads++
			return readSearchSnapshot(entry)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("warm preindex did not hit cache")
	}
	if !reflect.DeepEqual(warm, cold) {
		t.Fatalf("warm cache hit changed snapshot:\nwarm=%#v\ncold=%#v", warm, cold)
	}
	if persistenceReads != 0 {
		t.Fatalf("warm cache hit redundantly revalidated persisted snapshot %d time(s)", persistenceReads)
	}
}

func TestPreindexProviderSnapshotSurfacesPersistenceFailure(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cacheDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err == nil {
		t.Fatal("expected an unwritable cache path to fail preindex")
	}
}

func TestPreindexProviderSnapshotReusesSameTreeAcrossCommits(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	first := make(map[Profile]ProviderSnapshot)
	for _, profile := range []Profile{ProfileFast, ProfileFull} {
		snapshot, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile: profile,
		}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if cacheHit {
			t.Fatalf("first %s preindex unexpectedly hit cache", profile)
		}
		first[profile] = snapshot
	}
	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")
	if newHead == first[ProfileFull].Header.Commit {
		t.Fatal("test setup did not advance HEAD to a new commit")
	}
	for _, profile := range []Profile{ProfileFast, ProfileFull} {
		second, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile: profile,
		}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if !cacheHit {
			t.Fatalf("same-tree but different-commit %s snapshot did not reuse cache", profile)
		}
		if second.Header.Tree != first[profile].Header.Tree {
			t.Fatalf("%s snapshot tree changed across an empty commit: first=%s second=%s",
				profile, first[profile].Header.Tree, second.Header.Tree,
			)
		}
		// The parsed graph is reused from the old commit's cache entry, but the
		// commit we report must be the one we are actually serving right now,
		// not the stale commit recorded when the cache entry was built.
		if second.Header.Commit != newHead {
			t.Fatalf("%s snapshot commit was not re-stamped to serving HEAD: got %s, want %s",
				profile, second.Header.Commit, newHead,
			)
		}
	}
}

func TestSearchReusesSameTreeCacheAcrossCommitsAndReportsCurrentHEAD(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	options := SearchOptions{Profile: ProfileFull, TopK: 5, CacheDir: cacheDir}
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir); err != nil {
		t.Fatal(err)
	}

	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")

	response, err := SearchRepository(t.Context(), repo, "test-version", "ValidateToken", options)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Stats.IndexCacheHit {
		t.Fatal("search after an empty commit did not reuse the same-tree cache")
	}
	if response.Commit != newHead {
		t.Fatalf("search response commit = %q, want current HEAD %q", response.Commit, newHead)
	}
	if len(response.Results) == 0 || response.Results[0].SymbolName != "ValidateToken" {
		t.Fatalf("search lost target after cache reuse across commits: %#v", response.Results)
	}
}

func TestSearchSnapshotCacheDoesNotReuseRepoKeyAfterRemoteChanges(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "remote", "add", "origin", "https://github.com/acme/legacy.git")
	write(t, repo, "target/needle.go", `package target

// IdentitySensitiveTarget finds the repository identity cache regression.
func IdentitySensitiveTarget() bool { return true }
`)
	write(t, repo, "noise/unrelated.go", "package noise\nfunc Unrelated() bool { return false }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	preindexed, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}
	if preindexed.Header.RepoKey != "gh/acme/legacy" {
		t.Fatalf("initial repo key = %q, want gh/acme/legacy", preindexed.Header.RepoKey)
	}
	commit, tree := preindexed.Header.Commit, preindexed.Header.Tree

	git(t, repo, "remote", "set-url", "origin", "https://github.com/acme/renamed.git")
	currentCommit, currentTree, err := resolveCommittedHEAD(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if currentCommit != commit || currentTree != tree {
		t.Fatalf("remote-only change altered HEAD provenance: got %s/%s, want %s/%s", currentCommit, currentTree, commit, tree)
	}

	if _, staleHit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err != nil {
		t.Fatal(err)
	} else if staleHit {
		t.Fatal("full preindex with the old repository key was reported as usable")
	}

	assertNewRepoKeyResult := func(name string, response SearchResponse) {
		t.Helper()
		if response.Stats.IndexCacheHit {
			t.Fatalf("%s search reported the old repository-key cache as a hit: %#v", name, response.Stats)
		}
		for _, result := range response.Results {
			if result.SymbolName != "IdentitySensitiveTarget" {
				continue
			}
			if !strings.HasPrefix(result.SymbolID, "gh/acme/renamed:") {
				t.Fatalf("%s search returned stale symbol ID %q", name, result.SymbolID)
			}
			return
		}
		t.Fatalf("%s search lost IdentitySensitiveTarget: %#v", name, response.Results)
	}

	selective, err := SearchRepository(t.Context(), repo, "test-version", "IdentitySensitiveTarget repository identity", SearchOptions{
		Profile: ProfileSyntaxOnly, TopK: 5, MaxIndexedFiles: 1, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNewRepoKeyResult("selective", selective)

	full, err := SearchRepository(t.Context(), repo, "test-version", "IdentitySensitiveTarget repository identity", SearchOptions{
		Profile: ProfileSyntaxOnly, TopK: 5, IndexAllFiles: true, CacheDir: cacheDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNewRepoKeyResult("full", full)
}

func TestSearchRejectsPreindexWhenRepoKeyChangesAfterCacheLoad(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "remote", "add", "origin", "https://github.com/acme/legacy.git")
	write(t, repo, "target.go", `package target

func IdentityRaceTarget() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileSyntaxOnly,
	}, cacheDir); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("first preindex unexpectedly hit cache")
	}

	mutated := false
	response, err := SearchRepository(t.Context(), repo, "test-version", "IdentityRaceTarget", SearchOptions{
		Profile:       ProfileSyntaxOnly,
		TopK:          5,
		IndexAllFiles: true,
		CacheDir:      cacheDir,
		afterPreindexLoad: func() {
			mutated = true
			git(t, repo, "remote", "set-url", "origin", "https://github.com/acme/renamed.git")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !mutated {
		t.Fatal("test did not mutate repository identity after loading the complete preindex")
	}
	if response.Stats.IndexCacheHit {
		t.Fatalf("search reported the old-identity preindex as a hit: %#v", response.Stats)
	}
	for _, result := range response.Results {
		if result.SymbolName != "IdentityRaceTarget" {
			continue
		}
		if !strings.HasPrefix(result.SymbolID, "gh/acme/renamed:") {
			t.Fatalf("search returned stale repository identity in symbol ID %q", result.SymbolID)
		}
		return
	}
	t.Fatalf("search lost IdentityRaceTarget: %#v", response.Results)
}

func TestSearchSnapshotCacheKeyPreservesIgnoreFileOrder(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".ignore-target", "target.go\n")
	write(t, repo, ".reinclude-target", "!target.go\n")
	write(t, repo, "target.go", "package target\nfunc Target() bool { return true }\n")
	write(t, repo, "control.go", "package target\nfunc Control() bool { return true }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	includeTarget := ProviderSnapshotOptions{
		Profile:     ProfileSyntaxOnly,
		IgnoreFiles: []string{".ignore-target", ".reinclude-target"},
	}
	first, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", includeTarget, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first ordered-ignore snapshot unexpectedly hit cache")
	}
	if !snapshotHasSymbol(first, "Target") {
		t.Fatalf("later re-inclusion rule did not restore target: %#v", first.Symbols)
	}

	ignoreTarget := includeTarget
	ignoreTarget.IgnoreFiles = []string{".reinclude-target", ".ignore-target"}
	includeKey, err := searchSnapshotKey(repo, first.Header.RepoKey, "test-version", first.Header.Tree, includeTarget)
	if err != nil {
		t.Fatal(err)
	}
	ignoreKey, err := searchSnapshotKey(repo, first.Header.RepoKey, "test-version", first.Header.Tree, ignoreTarget)
	if err != nil {
		t.Fatal(err)
	}
	if includeKey == ignoreKey {
		t.Fatalf("reversed order-sensitive ignore files produced the same cache key %q", includeKey)
	}

	second, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", ignoreTarget, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("reversed ignore-file order reused the incompatible cached snapshot")
	}
	if snapshotHasSymbol(second, "Target") {
		t.Fatalf("later ignore rule did not exclude target: %#v", second.Symbols)
	}
	if !snapshotHasSymbol(second, "Control") {
		t.Fatalf("reversed-rule snapshot lost control symbol: %#v", second.Symbols)
	}
}

func TestSearchCacheTransactionPinsPolicyAcrossCompleteAndSelectiveSnapshots(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".search-policy", "denied.go\n")
	write(t, repo, "denied.go", `package sample

func PolicyTransactionNeedle() bool { return true }
`)
	write(t, repo, "control.go", `package sample

// Control documents the cache transaction fallback.
func Control() bool { return true }
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	providerOptions := ProviderSnapshotOptions{
		Profile:     ProfileSyntaxOnly,
		IgnoreFiles: []string{".search-policy"},
	}
	preindexed, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", providerOptions, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("first policy-A preindex unexpectedly hit cache")
	}
	if snapshotHasSymbol(preindexed, "PolicyTransactionNeedle") {
		t.Fatal("policy-A preindex retained the denied symbol")
	}

	policyMutated := false
	searchOptions := SearchOptions{
		Profile:         ProfileSyntaxOnly,
		TopK:            5,
		MaxIndexedFiles: 1,
		CacheDir:        cacheDir,
		IgnoreFiles:     []string{".search-policy"},
		afterCachePolicyCapture: func() {
			policyMutated = true
			write(t, repo, ".search-policy", "# policy B includes every source file\n")
		},
	}
	policyAResponse, err := SearchRepository(
		t.Context(), repo, "test-version", "PolicyTransactionNeedle cache transaction", searchOptions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !policyMutated {
		t.Fatal("test did not mutate the policy after transaction capture")
	}
	if !policyAResponse.Stats.IndexCacheHit {
		t.Fatal("policy-A search did not load the preindexed complete snapshot")
	}
	if policyAResponse.Stats.FilesIndexed == 0 {
		t.Fatalf("policy-A search skipped selective derivation: %#v", policyAResponse.Stats)
	}
	if len(policyAResponse.Results) == 0 || policyAResponse.Results[0].FilePath != "control.go" {
		t.Fatalf("policy-A search did not derive the matching control file: %#v", policyAResponse.Results)
	}
	for _, result := range policyAResponse.Results {
		if result.FilePath == "denied.go" {
			t.Fatalf("policy-A transaction admitted a file enabled only by policy B: %#v", policyAResponse.Results)
		}
	}

	// The first transaction must not derive the missing policy-A symbol from
	// its complete snapshot and persist that omission under policy B's selective
	// key. Two B searches prove both the cold build and its direct cache replay.
	searchOptions.afterCachePolicyCapture = nil
	for attempt := 0; attempt < 2; attempt++ {
		response, searchErr := SearchRepository(
			t.Context(), repo, "test-version", "PolicyTransactionNeedle cache transaction", searchOptions,
		)
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		found := false
		for _, result := range response.Results {
			if result.SymbolName == "PolicyTransactionNeedle" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("policy-B search %d replayed a policy-A omission: %#v", attempt+1, response.Results)
		}
		if attempt == 0 && response.Stats.IndexCacheHit {
			t.Fatal("first policy-B search unexpectedly hit a poisoned selective entry")
		}
		if attempt == 1 && !response.Stats.IndexCacheHit {
			t.Fatal("second policy-B search did not reuse its correct selective entry")
		}
	}
}

// TestOnlyFilesDerivationReStampsCommitAfterSameTreeCommit pins the re-stamp
// on the OnlyFiles-derivation branch of loadOrBuildSearchSnapshot: a selective
// snapshot derived from a complete same-tree cache entry built at an older
// commit must report the commit it is actually serving right now, not the
// stale commit recorded when the complete entry was written.
func TestOnlyFilesDerivationReStampsCommitAfterSameTreeCommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "selected.go", "package sample\nfunc Selected() bool { return true }\n")
	write(t, repo, "other.go", "package sample\nfunc Other() bool { return false }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	full, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile: ProfileFull,
	}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cacheHit {
		t.Fatal("first preindex unexpectedly hit cache")
	}

	git(t, repo, "commit", "--allow-empty", "-m", "same tree")
	newHead := rev(t, repo, "HEAD")
	if newHead == full.Header.Commit {
		t.Fatal("test setup did not advance HEAD to a new commit")
	}

	selective, cacheHit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Profile:   ProfileFull,
		OnlyFiles: []string{"selected.go"},
	}, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("selective build did not derive from the complete same-tree preindex")
	}
	if selective.Header.Tree != full.Header.Tree {
		t.Fatalf("derived selective snapshot tree changed across an empty commit: got %s, want %s",
			selective.Header.Tree, full.Header.Tree,
		)
	}
	if selective.Header.Commit != newHead {
		t.Fatalf("derived selective snapshot commit was not re-stamped to serving HEAD: got %s, want %s",
			selective.Header.Commit, newHead,
		)
	}
}

// TestSearchSnapshotKeyIncludesGraphIgnore pins that the implicitly-loaded
// .graphignore keys the cache entry, exactly as an explicit --ignore-file does.
// Without it, editing .graphignore against an unchanged tree hit the previous
// entry and the new rules silently did nothing — verified before the fix by an
// index run that reported a cache hit and the full file count while
// .graphignore excluded one of the files.
func TestSearchSnapshotKeyIncludesGraphIgnore(t *testing.T) {
	repo := t.TempDir()
	options := ProviderSnapshotOptions{Profile: ProfileFull}

	absent, err := searchSnapshotKey(repo, "repo", "v", "tree", options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	present, err := searchSnapshotKey(repo, "repo", "v", "tree", options)
	if err != nil {
		t.Fatal(err)
	}
	if absent == present {
		t.Fatal("adding .graphignore must change the cache key")
	}
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := searchSnapshotKey(repo, "repo", "v", "tree", options)
	if err != nil {
		t.Fatal(err)
	}
	if edited == present {
		t.Fatal("editing .graphignore must change the cache key")
	}
}

func TestPreindexForceRebuildsDespiteValidCache(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "a.go", "package a\n\nfunc A() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	opts := func() ProviderSnapshotOptions { return ProviderSnapshotOptions{Profile: ProfileFast} }

	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "v", opts(), cacheDir); err != nil {
		t.Fatal(err)
	} else if hit {
		t.Fatal("first preindex unexpectedly hit cache")
	}
	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "v", opts(), cacheDir); err != nil {
		t.Fatal(err)
	} else if !hit {
		t.Fatal("second preindex (no force) should hit the warmed cache")
	}

	forced := opts()
	forced.ForceRebuild = true
	snapshot, hit, err := PreindexProviderSnapshot(t.Context(), repo, "v", forced, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("forced preindex must not report a cache hit")
	}
	if snapshot.Header.Stats.Symbols == 0 {
		t.Fatal("forced rebuild produced an empty snapshot")
	}

	if _, hit, err := PreindexProviderSnapshot(t.Context(), repo, "v", opts(), cacheDir); err != nil {
		t.Fatal(err)
	} else if !hit {
		t.Fatal("ordinary preindex after --force should hit the refreshed entry")
	}
}

// SymbolRecord.bodyless is private, so it does not survive the cached snapshot's
// wire format — but the selective derivation RERUNS call resolution over those
// cached symbols, and resolution reads bodyless to tell a TypeScript overload
// set apart from genuine ambiguity. Without the sidecar the derived graph
// downgrades an overloaded call to name_only, which the fast profile then drops:
// the same query would answer "1 caller" cold and "no callers" warm.
func TestSelectiveFastSearchSnapshotPreservesCachedBodylessDeclarations(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "src/renderList.ts", `export function renderList(source: number, fn: (i: number) => any): any[]
export function renderList(source: string, fn: (v: string) => any): any[]
export function renderList(source: any, fn: (...a: any[]) => any): any[] {
  return []
}
`)
	write(t, repo, "src/caller.ts", `import { renderList } from './renderList'

export function useList(items: string[]): any[] {
  return renderList(items, (v) => v)
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	cacheDir := t.TempDir()
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFast}, cacheDir); err != nil {
		t.Fatal(err)
	}
	selective, cacheHit, err := loadOrBuildSearchGraphSnapshot(t.Context(), repo, "test-version",
		ProviderSnapshotOptions{Profile: ProfileFast, OnlyFiles: []string{"src/renderList.ts", "src/caller.ts"}}, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cacheHit {
		t.Fatal("fast selective snapshot did not derive from complete cache")
	}
	calls := runCallsFrom(selective, "useList")
	if len(calls) != 1 || calls[0].Resolution != "import_resolved" {
		t.Fatalf("cached fast selective snapshot lost the overload collapse: %#v",
			relationsOfType(selective.Relations, "CALLS"))
	}
}
