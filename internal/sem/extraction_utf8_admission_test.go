package sem

import (
	"reflect"
	"testing"
)

// Independent raw-import fixture: JSON would silently replace the invalid byte.
func TestExtractionInvalidUTF8BypassesPersistence(t *testing.T) {
	cache := &extractionCache{directory: t.TempDir(), repository: "fixture/repo", build: "fixture-build"}
	spec := resolveProfile(ProfileFull)
	language, _ := languageForPath("a.go")
	source := captureSource("a.go", "package p\nimport \"fixture/\xff\"\nfunc A() {}\n")
	first, hit := cache.extract(spec, language, source, 1024)
	if hit {
		t.Fatal("cold extraction hit")
	}
	if len(first.rawImports) == 0 {
		t.Fatal("fixture did not retain raw import")
	}
	record := recordExtraction(first.entities, first.language, first.status)
	record.RawImports = first.rawImports
	if validateExtractionRecord(record) == nil {
		t.Fatal("fixture no longer contains lossy JSON input")
	}
	cache.flush()
	second, hit := cache.extract(spec, language, source, 1024)
	if hit || !reflect.DeepEqual(first, second) {
		t.Fatal("lossy extraction was persisted or fresh result changed")
	}
	if cache.cacheWriteBytes.Load() != 0 {
		t.Fatal("invalid UTF-8 extraction was written")
	}
}
