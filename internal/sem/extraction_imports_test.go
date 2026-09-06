package sem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractionRawImportsFamilyRoundTripAndProfiles(t *testing.T) {
	for _, fixture := range []struct {
		path, content string
		imports       []string
	}{
		{"a.go", "package p\nimport \"fmt\"\nfunc A(){fmt.Println(1)}\n", []string{"fmt"}},
		{"a.ts", "import { other } from './other';\nexport function A(){ other(); }\n", []string{"./other"}},
		{"a.py", "from other import value\ndef run():\n    return value()\n", []string{"other", "other.value"}},
		{"empty.go", "package p\nfunc A(){}\n", []string{}},
		{"broken.go", "package p\nimport \"fmt\"\nfunc Broken( {\n", []string{"fmt"}},
	} {
		for _, profile := range []Profile{ProfileSyntaxOnly, ProfileFast, ProfileFull} {
			t.Run(fixture.path+"/"+string(profile), func(t *testing.T) {
				cache := &extractionCache{directory: t.TempDir(), repository: "imports-fixture", build: "fixture-build"}
				spec := resolveProfile(profile)
				language, _ := languageForPath(fixture.path)
				source := captureSource(fixture.path, fixture.content)
				first, hit := cache.extract(spec, language, source, 4096)
				if hit {
					t.Fatal("cold hit")
				}
				cache.flush()
				second, hit := cache.extract(spec, language, source, 4096)
				if !hit || !reflect.DeepEqual(first, second) {
					t.Fatalf("raw imports not exactly reused: hit=%t first=%+v second=%+v", hit, first, second)
				}
				if profile == ProfileSyntaxOnly {
					if first.relationFamilies != 0 || first.rawImports != nil {
						t.Fatal("syntax-only computed raw relations")
					}
					return
				}
				if first.relationFamilies != extractionRawImports || !reflect.DeepEqual(first.rawImports, fixture.imports) {
					t.Fatalf("presence/content lost: %+v", first)
				}
				if len(second.rawImports) > 0 {
					second.rawImports[0] = "mutated"
					third, _ := cache.extract(spec, language, source, 4096)
					if !reflect.DeepEqual(third.rawImports, fixture.imports) {
						t.Fatal("mutable raw imports escaped cache ownership")
					}
				}
				if cache.stats().RawImportsParsed != 1 || cache.stats().RawImportsReused < 1 {
					t.Fatal("raw family work not counted")
				}
			})
		}
	}
}

func TestExtractionRawImportsInvalidPresenceAndVersionRejected(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "imports-fixture", build: "fixture-build"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nfunc A(){}\n")
	cache.extract(spec, language, source, 4096)
	cache.flush()
	entry, key, _ := cache.entry(spec, language, source, 4096)
	record, ok := loadExtraction(entry, key, cache)
	if !ok {
		t.Fatal("missing baseline record")
	}
	for _, change := range []func(*extractionRecord){func(r *extractionRecord) { r.RelationFamilies = 2 }, func(r *extractionRecord) { r.RelationFamilies = 0; r.RawImports = []string{"invented"} }, func(r *extractionRecord) { r.Version = 2 }} {
		altered := record
		change(&altered)
		payload, _ := json.Marshal(altered)
		if err := entry.write("fixture", extractionEnvelope{Key: key, PayloadDigest: contentHash(payload), Record: altered}); err != nil {
			t.Fatal(err)
		}
		if _, ok := loadExtraction(entry, key, cache); ok {
			t.Fatal("unsupported or ambiguous family admitted")
		}
	}
}

func TestExtractionQuotaOverrides(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", "1")
	t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES", "20000")
	cache := &extractionCache{directory: t.TempDir(), repository: "quota-fixture", build: "fixture-build"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	var last capturedSource
	for i := 0; i < 5; i++ {
		last = captureSource("a.go", fmt.Sprintf("package p\nfunc A() int { return %d }\n", i))
		cache.extract(spec, language, last, 4096)
		cache.flush()
		entry, _, _ := cache.entry(spec, language, last, 4096)
		entries, err := os.ReadDir(filepath.Join(entry.root, filepath.Dir(entry.relative)))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) > 2 {
			t.Fatalf("entry quota exceeded (including lock metadata): %d", len(entries))
		}
		if i == 0 {
			t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", "2")
		}
	}
	if cache.maxEntries != 1 {
		t.Fatal("quota configuration changed within operation")
	}
	t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES", "1")
	tiny := &extractionCache{directory: t.TempDir(), repository: "tiny-fixture", build: "fixture-build"}
	tiny.extract(spec, language, last, 4096)
	tiny.flush()
	if _, hit := tiny.extract(spec, language, last, 4096); hit {
		t.Fatal("oversize entry stored despite byte quota")
	}
	for _, value := range []string{"", "0", "-1", "bad", "100001"} {
		t.Setenv("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", value)
		if got := extractionConfiguredLimit("ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES", extractionEntryLimit); got != extractionEntryLimit {
			t.Fatalf("invalid override %q admitted %d", value, got)
		}
	}
}
