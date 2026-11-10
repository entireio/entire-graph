package sem

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// coldWarmParityFixture has more source files than the shrunk default pool, so a
// cold query is selective, and query terms spread over more files than that pool
// holds, so a bounded content scan and the warm Git tree grep choose differently.
func coldWarmParityFixture(t *testing.T) string {
	t.Helper()
	return aliasGrepFixture(t, map[string]string{
		"alpha/run.go":     "package alpha\n\nimport \"fmt\"\n\n// Run frobnicates the ledger by delegating to the package helper.\nfunc Run() string {\n\tfmt.Println(\"run\")\n\treturn helper() + Format()\n}\n",
		"alpha/helper.go":  "package alpha\n\nfunc helper() string { return \"alpha\" }\n",
		"alpha/format.go":  "package alpha\n\n// Format renders the ledger entry for Run.\nfunc Format() string { return \"ledger\" }\n",
		"beta/helper.go":   "package beta\n\nfunc helper() string { return \"beta\" }\n\n// Audit checks the ledger after Run.\nfunc Audit() string { return helper() }\n",
		"value/parse.go":   "package value\n\n// readInt parses a ledger amount.\nfunc readInt(s string) int { return len(s) }\n",
		"value/limit.go":   "package value\n\n// Limit caps an integer amount.\nfunc Limit(n int) int { return n }\n",
		"value/convert.go": "package value\n\nfunc toInt(s string) int { return readInt(s) }\n",
	})
}

// strippedSearchJSON is the response as JSON without the fields that legitimately
// differ between two runs of one query: latencies, and whether the graph came from
// the cache (that difference is the point of a cold run).
func strippedSearchJSON(t *testing.T, response SearchResponse) string {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var strip func(any) any
	strip = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			out := make(map[string]any, len(typed))
			for key, field := range typed {
				if strings.HasSuffix(key, "latency_ms") || key == "index_cache_hit" || key == "generated_at" {
					continue
				}
				out[key] = strip(field)
			}
			return out
		case []any:
			for index := range typed {
				typed[index] = strip(typed[index])
			}
			return typed
		}
		return value
	}
	stripped, err := json.MarshalIndent(strip(decoded), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(stripped)
}

// TestColdCachedQueryAnswersLikeWarm pins N3: the first query on a tree (cold:
// nothing cached, so it builds and persists the complete snapshot) answers exactly
// what the same query answers once that snapshot is cached (warm). Both rank over
// a view derived from the same complete graph, so they must select the same way.
// Cold used to run a bounded content scan while warm ran a Git tree grep, which
// picked different files and made the first answer differ from every later one.
func TestColdCachedQueryAnswersLikeWarm(t *testing.T) {
	repo := coldWarmParityFixture(t)
	// "integer amount" infers the int alias, so it takes the alias grep route.
	for _, query := range []string{"Format ledger entry", "integer amount"} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()
			// The DEFAULT pool shrunk below the corpus: selective, and not an explicit
			// --max-indexed-files bound, which never builds the complete snapshot.
			options := SearchOptions{Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: 2}

			coldOptions := options
			coldOptions.CacheDir = t.TempDir()
			cold, err := SearchRepository(t.Context(), repo, "test-version", query, coldOptions)
			if err != nil {
				t.Fatal(err)
			}

			warmOptions := options
			warmOptions.CacheDir = t.TempDir()
			if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, warmOptions.CacheDir); err != nil {
				t.Fatal(err)
			}
			warm, err := SearchRepository(t.Context(), repo, "test-version", query, warmOptions)
			if err != nil {
				t.Fatal(err)
			}
			if cold.Stats.IndexCacheHit || !warm.Stats.IndexCacheHit || len(warm.Results) == 0 {
				t.Fatalf("premise lost: want a cold miss and a warm hit with results: cold=%#v warm=%#v", cold.Stats, warm.Stats)
			}
			if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, coldOptions.CacheDir); err != nil || !hit {
				t.Fatalf("premise lost: the cold query must build and persist the complete snapshot: hit=%v err=%v", hit, err)
			}
			// The bounded content scan is still what an uncached search runs. It must
			// choose differently here, or this fixture could not tell the routes apart.
			uncachedOptions := options
			uncachedOptions.DisableCache = true
			uncached, err := SearchRepository(t.Context(), repo, "test-version", query, uncachedOptions)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(uncached.Results, warm.Results) {
				t.Fatalf("fixture drift: the content scan answers like the warm grep, so cold parity proves nothing: %#v", warm.Results)
			}

			if got, want := strippedSearchJSON(t, cold), strippedSearchJSON(t, warm); got != want {
				t.Fatalf("cold answer differs from warm for %q:\ncold=%s\nwarm=%s", query, got, want)
			}
		})
	}
}

// TestColdNoHitQueryBuildsNothing pins that selecting the warm way on a cold query
// keeps selection BEFORE the build: a query nothing in the tree matches returns
// without parsing a file or persisting a snapshot, whether the corpus is larger
// than the default pool or fits inside it.
func TestColdNoHitQueryBuildsNothing(t *testing.T) {
	repo := coldWarmParityFixture(t)
	builds := 0
	previous := beforeColdCompleteBuild
	beforeColdCompleteBuild = func() { builds++ }
	t.Cleanup(func() { beforeColdCompleteBuild = previous })

	for _, pool := range []int{2, 64} {
		cacheDir := t.TempDir()
		response, err := SearchRepository(t.Context(), repo, "test-version", "quokka zanzibar", SearchOptions{
			Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: pool, CacheDir: cacheDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 || response.Stats.IndexCacheHit {
			t.Fatalf("pool %d: premise lost: want a cold query with no results: %#v", pool, response.Stats)
		}
		if response.Stats.PreselectionBackend != "git-tree-grep" {
			t.Fatalf("pool %d: the cold query took the %q route, want the warm git-tree-grep route", pool, response.Stats.PreselectionBackend)
		}
		if builds != 0 || response.Stats.FilesIndexed != 0 {
			t.Fatalf("pool %d: a cold query with no hits built a snapshot (%d build(s), %d files indexed)", pool, builds, response.Stats.FilesIndexed)
		}
		if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil || hit {
			t.Fatalf("pool %d: a cold query with no hits persisted the complete snapshot: hit=%v err=%v", pool, hit, err)
		}
	}
}

// TestColdQueryOverCompleteBoundKeepsContentScan pins the other side of the switch:
// above coldCompleteSnapshotMaxFiles no complete build follows a cold query, so it
// keeps the bounded content scan. The warm grep keeps every matching file, which a
// selective build of a large repository must not be handed.
func TestColdQueryOverCompleteBoundKeepsContentScan(t *testing.T) {
	previous := coldCompleteSnapshotMaxFiles
	coldCompleteSnapshotMaxFiles = 3
	t.Cleanup(func() { coldCompleteSnapshotMaxFiles = previous })

	repo := coldWarmParityFixture(t)
	cold, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: 2, CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.IndexCacheHit || len(cold.Results) == 0 {
		t.Fatalf("premise lost: %#v", cold.Stats)
	}
	if cold.Stats.PreselectionBackend != "go-content" || cold.Stats.FilesIndexed > 2 {
		t.Fatalf("a cold query over the complete-build bound left the bounded content scan: backend=%q files_indexed=%d",
			cold.Stats.PreselectionBackend, cold.Stats.FilesIndexed)
	}
}

// TestColdIndexAllFilesQueryKeepsWholeCorpus pins that --index-all-files is not
// narrowed by the switch: it never ran the content scan, so a cold query still
// ranks over every file of the complete snapshot it builds.
func TestColdIndexAllFilesQueryKeepsWholeCorpus(t *testing.T) {
	t.Parallel()
	repo := coldWarmParityFixture(t)
	cold, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", SearchOptions{
		Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: 2, IndexAllFiles: true, CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cold.Stats.IndexCacheHit || len(cold.Results) == 0 {
		t.Fatalf("premise lost: %#v", cold.Stats)
	}
	if cold.Stats.PreselectionBackend != "inventory" || cold.Stats.FilesIndexed != cold.Stats.FilesScanned {
		t.Fatalf("a cold --index-all-files query was narrowed: backend=%q files_indexed=%d of %d",
			cold.Stats.PreselectionBackend, cold.Stats.FilesIndexed, cold.Stats.FilesScanned)
	}
}
