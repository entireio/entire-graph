package sem

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func healthSourceFixture(total int) ([]FileRecord, ProviderStats) {
	files := make([]FileRecord, total)
	for i := range files {
		files[i] = FileRecord{Path: fmt.Sprintf("source%d.py", i), Language: "Python"}
	}
	return files, ProviderStats{Files: total, ParsedFiles: total, Symbols: total}
}

func TestGraphHealthThresholdAndDuplicates(t *testing.T) {
	for _, tc := range []struct {
		total, flagged int
		status         string
	}{
		{100, 0, "ok"}, {21, 1, "ok"}, {20, 1, "degraded"}, {19, 1, "degraded"},
		{100, 25, "degraded"}, {100, 26, "unsafe"}, {0, 0, "ok"},
	} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.flagged, tc.total), func(t *testing.T) {
			files, stats := healthSourceFixture(tc.total)
			var failures []PartialFailure
			for i := 0; i < tc.flagged; i++ {
				// Both phases, duplicate records and different codes still count once.
				for _, code := range []string{"E_PARSE_ERROR", "E_PARSE_ERROR", "E_PARSE_DEPTH_EXCEEDED"} {
					failures = append(failures, PartialFailure{FilePath: files[i].Path, Code: code, Detail: "at line 2 column 3"})
				}
			}
			h := calculateGraphHealth(files, failures, stats)
			if h.SourceFiles != tc.total || h.FlaggedFiles != tc.flagged || h.Status != tc.status || h.ThresholdPercentage != 5 {
				t.Fatalf("health = %+v", h)
			}
			wantPercent := 0.0
			if tc.total != 0 {
				wantPercent = 100 * float64(tc.flagged) / float64(tc.total)
			}
			if h.FlaggedPercentage != wantPercent {
				t.Fatalf("percentage = %v, want %v", h.FlaggedPercentage, wantPercent)
			}
			if tc.total > 0 && h.Languages["Python"] != h.HealthCounts {
				t.Fatalf("language totals differ: %+v", h)
			}
			if len(failures) != tc.flagged*3 {
				t.Fatal("diagnostics were removed")
			}
		})
	}
}

func TestGraphHealthSourceEligibility(t *testing.T) {
	for _, path := range []string{"readme.md", "page.rst", "data.json", "data.json5", "data.yaml", "data.xml", "project.toml", "settings.ini", "file.plist", "main.tf", "main.tfvars", "main.bicep", "Makefile", "Dockerfile", "CMakeLists.txt", "package.csproj", "requirements.txt", "data.csv", "unknown.txt"} {
		if _, eligible := sourceFileLanguage(path, ""); eligible {
			t.Errorf("non-source classified as source: %s", path)
		}
	}
	for _, path := range []string{"a.go", "a.py", "a.js", "a.tsx", "a.rs", "a.cpp", "a.h", "a.sh", "a.vue", "a.svelte", "a.html", "a.css", "a.proto", "a.sql", "a.nim", "a.f90"} {
		if _, eligible := sourceFileLanguage(path, ""); !eligible {
			t.Errorf("source excluded: %s", path)
		}
	}
	if language, eligible := sourceFileLanguage("bin/run", "Bash"); !eligible || language != "Bash" {
		t.Fatal("shebang-routed source excluded")
	}
	files, stats := healthSourceFixture(20)
	failures := []PartialFailure{{FilePath: "source0.py", Code: "E_PARSE_ERROR"}}
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("doc%d.yaml", i)
		files = append(files, FileRecord{Path: path, Language: "YAML"})
		failures = append(failures, PartialFailure{FilePath: path, Code: "E_PARSE_ERROR"})
	}
	stats.Files += 100
	stats.ParsedFiles += 100
	h := calculateGraphHealth(files, failures, stats)
	if h.SourceFiles != 20 || h.FlaggedFiles != 1 || h.FlaggedPercentage != 5 || h.Status != "degraded" {
		t.Fatalf("non-source diluted health: %+v", h)
	}
}

func TestGraphHealthSkipsOmissionsAndSafeguards(t *testing.T) {
	files, stats := healthSourceFixture(20)
	failures := []PartialFailure{{FilePath: "source0.py", Code: "E_MINIFIED"}, {FilePath: "source0.py", Code: "E_FILE_TOO_LARGE"}}
	h := calculateGraphHealth(files, failures, stats)
	if h.Status != "ok" || h.FlaggedFiles != 0 || h.IntentionalSkippedFiles != 1 || h.SourceFiles != 20 {
		t.Fatalf("skip accounting: %+v", h)
	}
	// An omitted source belongs to both the denominator and numerator.
	h = calculateGraphHealth(files[:19], []PartialFailure{{FilePath: "missing.py", Code: "E_FILE_READ"}}, stats)
	if h.SourceFiles != 20 || h.FlaggedFiles != 1 || h.FlaggedPercentage != 5 {
		t.Fatalf("omitted file accounting: %+v", h)
	}
	h = calculateGraphHealth(nil, []PartialFailure{{FilePath: "missing.f90", Code: "E_UNSUPPORTED_LANGUAGE"}}, ProviderStats{})
	if h.Status != "unsafe" || h.SourceFiles != 1 || h.FlaggedFiles != 1 {
		t.Fatalf("unsupported source safeguard: %+v", h)
	}
	h = calculateGraphHealth(nil, []PartialFailure{{FilePath: "bin/run", Language: "Bash", Code: "E_FILE_READ"}}, ProviderStats{})
	if h.SourceFiles != 1 || h.FlaggedFiles != 1 || h.Status != "unsafe" {
		t.Fatalf("omitted shebang source: %+v", h)
	}
	for _, tc := range []struct {
		parsed, symbols int
		status          string
	}{{9, 20, "unsafe"}, {10, 20, "degraded"}, {20, 0, "degraded"}} {
		stats.ParsedFiles, stats.Symbols = tc.parsed, tc.symbols
		if h := calculateGraphHealth(files, nil, stats); h.Status != tc.status {
			t.Fatalf("lost safeguard: %+v", h)
		}
	}
	h = calculateGraphHealth([]FileRecord{{Path: "config.yaml", Language: "YAML"}}, nil, ProviderStats{Files: 1, ParsedFiles: 1})
	if h.SourceFiles != 0 || h.FlaggedPercentage != 0 || h.Status != "degraded" {
		t.Fatalf("zero denominator lost empty-graph guard: %+v", h)
	}
}

func TestGraphHealthSnapshotIndexAndSelectiveConsistency(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	var paths []string
	for i := 0; i < 21; i++ {
		path := fmt.Sprintf("source%d.py", i)
		paths = append(paths, path)
		content := "def retained():\n    return 1\n"
		if i == 0 {
			content += "def broken(:\n"
		}
		writeFile(t, repo, path, content)
	}
	writeFile(t, repo, "notes.md", "# documentation\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "health fixture")
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", opts)
	if err != nil {
		t.Fatal(err)
	}
	h := snapshot.Header.Completeness.Health
	if h.Status != "ok" || h.SourceFiles != 21 || h.FlaggedFiles != 1 || len(snapshot.Header.PartialFailures) == 0 {
		t.Fatalf("snapshot: %+v", h)
	}
	indexed, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", opts, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(indexed.Header.Completeness, snapshot.Header.Completeness) {
		t.Fatal("index differs from snapshot")
	}
	opts.OnlyFiles = paths
	derived, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "test", opts, indexed)
	if err != nil {
		t.Fatal(err)
	}
	cold, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cold.Header.Completeness.Health, derived.Header.Completeness.Health) || !reflect.DeepEqual(h, derived.Header.Completeness.Health) {
		t.Fatalf("health differs across query surfaces: cold=%+v derived=%+v", cold.Header.Completeness.Health, derived.Header.Completeness.Health)
	}
	if !reflect.DeepEqual(cold.Header.PartialFailures, derived.Header.PartialFailures) || cold.Header.Stats.CompletenessLevel != h.Status {
		t.Fatal("query lost diagnostics/status")
	}
}

func TestGraphHealthRejectsObsoleteCachedStatus(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "source.py", "def retained():\n    return 1\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "cache fixture")
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	snapshot, _, err := PreindexProviderSnapshot(t.Context(), repo, "test", opts, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := searchSnapshotKey(repo, snapshot.Header.RepoKey, "test", snapshot.Header.Tree, opts)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readSearchSnapshot(entry)
	if err != nil {
		t.Fatal(err)
	}
	stored.CacheVersion = "search-snapshot-v15-" + IdentityRevision
	stored.Snapshot.Header.Stats.CompletenessLevel = "unsafe"
	stored.Snapshot.Header.Completeness.Health = GraphHealth{}
	if err := writeSearchSnapshot(entry, stored); err != nil {
		t.Fatal(err)
	}
	fresh, hit, err := PreindexProviderSnapshot(t.Context(), repo, "test", opts, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if hit || fresh.Header.Completeness.Health.Status != "ok" || fresh.Header.Stats.CompletenessLevel != "ok" {
		t.Fatal("obsolete cached status survived")
	}
	// The opaque stream cache must reject the old calculation too.
	transaction, err := BeginProviderRecordsCache(t.Context(), repo, "test", snapshot.Header.Commit, snapshot.Header.Tree, "snapshot", cacheDir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Store([]byte("obsolete status\n"), nil, snapshot.Header); err != nil {
		t.Fatal(err)
	}
	records, err := readProviderRecords(transaction.entry)
	if err != nil {
		t.Fatal(err)
	}
	records.CacheVersion = "provider-records-v10-" + IdentityRevision
	if err := writeProviderRecords(transaction.entry, records); err != nil {
		t.Fatal(err)
	}
	if _, _, hit := transaction.Load(); hit {
		t.Fatal("obsolete streamed status survived")
	}
}

func TestGraphHealthReadFailurePreservesShebangRouting(t *testing.T) {
	sc := sourceContext{
		readPrefix: func(string, int) (string, bool) { return "#!/bin/bash\n", true },
		read:       func(string) (string, bool) { return "", false },
	}
	result := processProviderFile(t.Context(), newBudgetGate(t.Context(), time.Time{}, time.Now), sc, resolveProfile(ProfileFull), 1024, 0, "bin/run")
	if result.file != nil || len(result.failures) != 1 || result.failures[0].Language != "Bash" {
		t.Fatalf("read failure lost resolved source language: %+v", result)
	}
	h := calculateGraphHealth(nil, result.failures, ProviderStats{})
	if h.SourceFiles != 1 || h.FlaggedFiles != 1 || h.Status != "unsafe" {
		t.Fatalf("source omitted from health: %+v", h)
	}
}
