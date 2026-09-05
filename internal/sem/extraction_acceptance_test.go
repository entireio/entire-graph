package sem

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// Independently written declarations exercise private overload, scope, parameter,
// and malformed-input state. Baseline is this provider on the identical bytes.
func TestExtractionReuseLanguageAndFailureEquivalence(t *testing.T) {
	fixtures := map[string]string{
		"a.go":      "package p\nfunc A(x int) int { f := func(y int) int {return y}; return f(x) }\n",
		"a.ts":      "export function f(x: string): string;\nexport function f(x: number): number;\nexport function f(x: any): any { return x; }\nclass B { call() { return f(1); } }\n",
		"a.py":      "def outer(x):\n    def inner(y):\n        return y\n    return inner(x)\n",
		"a.cpp":     "extern \"C\" int f(int x);\nint f(int x) { return x; }\n",
		"broken.go": "package p\nfunc broken( {\n",
	}
	repo, cache := t.TempDir(), t.TempDir()
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
		options := ProviderSnapshotOptions{Worktree: true, Profile: profile, ExtractionCacheDir: cache}
		baseline, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
		if err != nil {
			t.Fatal(err)
		}
		options.ExtractionReuse = true
		for run := 0; run < 2; run++ {
			got, err := BuildProviderSnapshotWithOptions(context.Background(), repo, "fixture", options)
			if err != nil {
				t.Fatal(err)
			}
			assertCaptureProvenance(t, got)
			got.Header.OperationInputs = nil // default omits this validated additive capture field
			got.Header.Stats.Extraction = nil
			if !reflect.DeepEqual(baseline, got) {
				t.Fatalf("profile %s run %d drift", profile, run)
			}
		}
	}
}

func TestExtractionConcurrentSameKeyAndNoFollow(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "fixture/repo", build: "fixture-build"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nfunc A() {}\n")
	expected := extractCapturedSource(spec, language, source)
	expected.relationFamilies = extractionRawImports // Go full-profile empty imports are computed, not absent.
	expected.rawImports = []string{}                 // The existing Go scanner preserves a computed empty slice.
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, _ := cache.extract(spec, language, source, 1024)
			if !reflect.DeepEqual(expected, got) {
				t.Error("concurrent drift")
			}
		}()
	}
	wait.Wait()
	entry, key, err := cache.entry(spec, language, source, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadExtraction(entry, key, cache); !ok {
		t.Fatal("no completed entry after concurrent publication")
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(entry.root, entry.relative)
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadExtraction(entry, key, cache); ok {
		t.Fatal("followed redirected entry")
	}
}

func TestExtractionDeterministicFailureAdmission(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "fixture/repo", build: "fixture-build"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nfunc broken( {\n")
	first, hit := cache.extract(spec, language, source, 1024)
	if hit || !first.status.DeterministicSyntaxError {
		t.Fatalf("invalid cold error status: %#v", first.status)
	}
	second, hit := cache.extract(spec, language, source, 1024)
	if !hit || !reflect.DeepEqual(first, second) {
		t.Fatal("deterministic failure not retained exactly")
	}
	for _, status := range []ParseStatus{{ParseError: true, Code: "E_PARSE_ERROR"}, {ParseError: true, Code: "E_PARSE_TIMEOUT"}, {Partial: true}, {DepthExceeded: true}, {ParseError: true, DeterministicSyntaxError: true, Code: "E_PARSE_TIMEOUT"}} {
		if cacheableExtractionStatus(status) {
			t.Fatalf("transient or invalid status admitted: %#v", status)
		}
	}
}
