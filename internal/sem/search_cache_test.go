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

func TestSearchSnapshotCachePreservesSymbolByteRanges(t *testing.T) {
	snapshot := ProviderSnapshot{Symbols: []SymbolRecord{{
		ID:              "symbol-id",
		sourceStartByte: 17,
		sourceEndByte:   43,
		parameterNames:  []string{"B", "value"},
	}}}
	cache := newCachedSearchSnapshot("test-version", "commit", "tree", ProviderSnapshotOptions{Profile: ProfileFull}, snapshot)
	path := filepath.Join(t.TempDir(), "snapshot.json.gz")
	if err := writeSearchSnapshot(path, cache); err != nil {
		t.Fatal(err)
	}
	restored, err := readSearchSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot.Symbols) != 1 {
		t.Fatalf("restored symbols = %#v", restored.Snapshot.Symbols)
	}
	symbol := restored.Snapshot.Symbols[0]
	if symbol.sourceStartByte != 17 || symbol.sourceEndByte != 43 {
		t.Fatalf("restored byte range = [%d,%d), want [17,43)", symbol.sourceStartByte, symbol.sourceEndByte)
	}
	if !reflect.DeepEqual(symbol.parameterNames, []string{"B", "value"}) {
		t.Fatalf("restored private parameter names = %#v", symbol.parameterNames)
	}
}

func TestSearchSnapshotProvenanceIgnoresRepoKeyDrift(t *testing.T) {
	header := SnapshotHeader{RepoKey: "local/fallback-after-transient-remote-failure", Commit: "commit", Tree: "tree"}
	if searchSnapshotProvenanceChanged(header, "commit", "tree") {
		t.Fatal("repo-key drift alone must not abort a build whose commit/tree provenance is intact")
	}
	if !searchSnapshotProvenanceChanged(header, "other-commit", "tree") {
		t.Fatal("commit change must still be detected")
	}
	if !searchSnapshotProvenanceChanged(header, "commit", "other-tree") {
		t.Fatal("tree change must still be detected")
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
	selectiveKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Commit, preindexed.Header.Tree, selectiveProviderOptions)
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
	selectiveKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Commit, preindexed.Header.Tree, selectiveOptions)
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
	fullKey, err := searchSnapshotKey(repo, preindexed.Header.RepoKey, "test-version", preindexed.Header.Commit, preindexed.Header.Tree, ProviderSnapshotOptions{
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
	// complete cache successfully and HEAD then advances before derivation: the
	// stale in-memory snapshot must not fail the search.
	git(t, repo, "commit", "--allow-empty", "-m", "advance HEAD after preindex probe")
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

func TestPreindexProviderSnapshotDoesNotReuseSameTreeAcrossCommits(t *testing.T) {
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
	for _, profile := range []Profile{ProfileFast, ProfileFull} {
		second, cacheHit, err := PreindexProviderSnapshot(t.Context(), repo, "test", ProviderSnapshotOptions{
			Profile: profile,
		}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if cacheHit {
			t.Fatalf("same-tree but different-commit %s snapshot reused cache", profile)
		}
		if second.Header.Tree != first[profile].Header.Tree || second.Header.Commit == first[profile].Header.Commit {
			t.Fatalf("%s snapshot provenance mismatch: first=%s/%s second=%s/%s",
				profile,
				first[profile].Header.Commit, first[profile].Header.Tree,
				second.Header.Commit, second.Header.Tree,
			)
		}
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
	includeKey, err := searchSnapshotKey(repo, first.Header.RepoKey, "test-version", first.Header.Commit, first.Header.Tree, includeTarget)
	if err != nil {
		t.Fatal(err)
	}
	ignoreKey, err := searchSnapshotKey(repo, first.Header.RepoKey, "test-version", first.Header.Commit, first.Header.Tree, ignoreTarget)
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
