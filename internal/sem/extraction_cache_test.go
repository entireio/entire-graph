package sem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractionReuseSnapshotFreshResolution(t *testing.T) {
	repo, dir := t.TempDir(), t.TempDir()
	files := map[string]string{"go.mod": "module fixture.local/capture\n\ngo 1.24\n", "a.go": "package p\nfunc Alpha() int { return 1 }\n", "b.go": "package p\nfunc Beta() int { return Alpha() }\n", "c.go": "package p\nfunc Gamma() int { return Beta() }\n"}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, ExtractionCacheDir: dir}
	build := func(options ProviderSnapshotOptions) ProviderSnapshot {
		t.Helper()
		got, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := build(options)
	if got := first.Header.Stats.Extraction; got == nil || got.FilesParsed != 3 || got.FilesReused != 0 {
		t.Fatalf("cold %#v", got)
	}
	second := build(options)
	if got := second.Header.Stats.Extraction; got == nil || got.FilesParsed != 0 || got.FilesReused != 3 {
		t.Fatalf("warm %#v", got)
	}
	baselineOptions := options
	baselineOptions.ExtractionReuse = false
	baseline := build(baselineOptions)
	equal := func(a, b ProviderSnapshot) {
		t.Helper()
		if a.Header.OperationInputs != nil {
			assertCaptureProvenance(t, a)
		}
		if b.Header.OperationInputs != nil {
			assertCaptureProvenance(t, b)
		}
		if a.Header.OperationInputs != nil && b.Header.OperationInputs != nil && !reflect.DeepEqual(a.Header.OperationInputs, b.Header.OperationInputs) {
			t.Fatal("same captured inputs changed identity")
		}
		a.Header.OperationInputs = nil
		b.Header.OperationInputs = nil
		a.Header.Stats.Extraction = nil
		b.Header.Stats.Extraction = nil
		if !reflect.DeepEqual(a, b) {
			t.Fatal("semantic snapshot changed with extraction reuse")
		}
	}
	equal(first, second)
	equal(second, baseline)
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package p\nfunc Renamed() int { return 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edited := build(options)
	if got := edited.Header.Stats.Extraction; got.FilesParsed != 1 || got.FilesReused != 2 {
		t.Fatalf("edited %#v", got)
	}
	equal(edited, build(baselineOptions))
}

func TestExtractionReuseKeyIsolationAndCorruption(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "fixture/repo", build: "fixture-build-a"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nfunc A(){}\n")
	first, hit := cache.extract(spec, language, source, 1024)
	if hit {
		t.Fatal("cold hit")
	}
	second, hit := cache.extract(spec, language, source, 1024)
	if !hit || !reflect.DeepEqual(first, second) {
		t.Fatal("missing exact reuse")
	}
	for _, change := range []func(*extractionCache, *capturedSource, *int){
		func(c *extractionCache, s *capturedSource, l *int) { c.build = "fixture-build-b" },
		func(c *extractionCache, s *capturedSource, l *int) { s.path = "b.go" },
		func(c *extractionCache, s *capturedSource, l *int) { *l = 2048 },
		func(c *extractionCache, s *capturedSource, l *int) { c.repository = "fixture/other" },
	} {
		other := &extractionCache{directory: cache.directory, repository: cache.repository, build: cache.build}
		changed := source
		limit := 1024
		change(other, &changed, &limit)
		if _, hit := other.extract(spec, language, changed, limit); hit {
			t.Fatal("changed identity hit")
		}
	}
	entry, _, _ := cache.entry(spec, language, source, 1024)
	if err := os.WriteFile(filepath.Join(entry.root, entry.relative), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, hit := cache.extract(spec, language, source, 1024)
	if hit || !reflect.DeepEqual(first, recovered) {
		t.Fatal("corruption was not a transparent miss")
	}
}

func TestExtractionReuseSearchCaptureMutation(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "a.go")
	before := "package p\nfunc Alpha() string { return \"before\" }\n"
	after := "package p\nfunc Alpha() string { return \"after!\" }\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.go"), []byte("package p\nfunc Beta() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := SearchOptions{Worktree: true, ExtractionReuse: true, CacheDir: t.TempDir(), MaxIndexedFiles: 1}
	options.afterSourceSelection = func() {
		if err := os.WriteFile(path, []byte(after), 0600); err != nil {
			t.Fatal(err)
		}
	}
	captured, err := SearchRepository(context.Background(), repo, "fixture", "Alpha", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Results) == 0 {
		t.Fatal("missing result")
	}
	bytes, _ := json.Marshal(captured.Results)
	if !strings.Contains(string(bytes), "before") || strings.Contains(string(bytes), "after!") {
		t.Fatalf("mixed source: %s", bytes)
	}
	options.afterSourceSelection = nil
	fresh, err := SearchRepository(context.Background(), repo, "fixture", "Alpha", options)
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ = json.Marshal(fresh.Results)
	if !strings.Contains(string(bytes), "after!") {
		t.Fatalf("stale fresh operation: %s", bytes)
	}
	if fresh.Stats.Extraction == nil || fresh.Stats.Extraction.FilesParsed != 1 {
		t.Fatalf("wrong parse telemetry: %#v", fresh.Stats.Extraction)
	}
}

func TestExtractionReuseManifestDeleteIgnore(t *testing.T) {
	repo, cache := t.TempDir(), t.TempDir()
	for path, body := range map[string]string{"go.mod": "module fixture.local/first\n\ngo 1.24\n", "a.go": "package p\nfunc Alpha() int { return Beta() }\n", "b.go": "package p\nfunc Beta() int { return 1 }\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := ProviderSnapshotOptions{Worktree: true, ExtractionReuse: true, ExtractionCacheDir: cache}
	check := func(parsed, reused int64) {
		t.Helper()
		got, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
		if err != nil {
			t.Fatal(err)
		}
		if stats := got.Header.Stats.Extraction; stats.FilesParsed != parsed || stats.FilesReused != reused {
			t.Fatalf("stats %#v", stats)
		}
		baselineOptions := options
		baselineOptions.ExtractionReuse = false
		want, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", baselineOptions)
		if err != nil {
			t.Fatal(err)
		}
		assertCaptureProvenance(t, got)
		got.Header.OperationInputs = nil
		got.Header.Stats.Extraction = nil
		if !reflect.DeepEqual(got, want) {
			t.Fatal("manifest/delete/ignore did not recompute fresh graph")
		}
	}
	check(2, 0)
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module fixture.local/second\n\ngo 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	check(0, 2)
	if err := os.Remove(filepath.Join(repo, "b.go")); err != nil {
		t.Fatal(err)
	}
	check(0, 1)
	if err := os.WriteFile(filepath.Join(repo, ".graphignore"), []byte("a.go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	check(0, 0)
}
