package sem

import (
	"fmt"
	"testing"
)

// TestTF142R16BuildManifestImportResolverStopsMidScan reproduces the finding
// at provider.go:3094 (buildManifestImportResolver, called from
// forEachRelation): nothing inside the helper polled shouldStop, including
// its path-only classification loop over the whole repository file list that
// makes no readContent call at all (readContent's own budget gating never
// even runs for that loop). A deadline that had already expired when
// forEachRelation entered still bought the complete pass over every file
// before the first gated manifest read, let alone the per-language
// (go/js/python/jvm/csharp/php/rust) passes after it.
func TestTF142R16BuildManifestImportResolverStopsMidScan(t *testing.T) {
	t.Parallel()
	const fileCount = 5000
	files := make([]FileRecord, fileCount)
	for i := range fileCount {
		files[i] = FileRecord{
			RecordType: "file",
			ID:         fmt.Sprintf("file%d", i),
			Path:       fmt.Sprintf("pkg%04d/file%d.go", i, i),
			Language:   "Go",
		}
	}
	readContent := func(path string) (string, bool) {
		if path == "go.mod" {
			return "module example.com/fixture\n", true
		}
		return "", false
	}

	// Control: unbudgeted, every .go file gets a resolved package entry keyed
	// by its directory import path.
	unbudgeted := buildManifestImportResolver(files, readContent, nil)
	if len(unbudgeted.goPackages) != fileCount {
		t.Fatalf("fixture must resolve one Go package per file: got %d, want %d", len(unbudgeted.goPackages), fileCount)
	}

	visited := 0
	stop := func() bool { visited++; return visited > 1 }
	stopped := buildManifestImportResolver(files, readContent, stop)
	if visited > budgetPollStride*2 {
		t.Fatalf("buildManifestImportResolver polled shouldStop only %d times before stopping ('never' if this is 0): "+
			"it must poll inside its own loops, not just rely on the caller's check before the call", visited)
	}
	if len(stopped.goPackages) >= len(unbudgeted.goPackages) {
		t.Fatalf("a stopped scan must not complete: %d Go packages resolved stopped vs %d unbudgeted",
			len(stopped.goPackages), len(unbudgeted.goPackages))
	}
}

// TestTF142R16BuildManifestImportResolverUnbudgetedAreUnchanged pins the
// control: a nil stop predicate must not alter the result relative to the
// pre-fix signature.
func TestTF142R16BuildManifestImportResolverUnbudgetedAreUnchanged(t *testing.T) {
	t.Parallel()
	files := []FileRecord{
		{RecordType: "file", ID: "f1", Path: "pkg/widget.go", Language: "Go"},
	}
	readContent := func(path string) (string, bool) {
		if path == "go.mod" {
			return "module example.com/widget\n", true
		}
		return "", false
	}
	resolver := buildManifestImportResolver(files, readContent, nil)
	if resolver.goModule != "example.com/widget" {
		t.Fatalf("goModule = %q, want example.com/widget", resolver.goModule)
	}
	if got := resolver.goPackages["example.com/widget/pkg"]; got != "pkg/widget.go" {
		t.Fatalf("goPackages[example.com/widget/pkg] = %q, want pkg/widget.go", got)
	}
}
