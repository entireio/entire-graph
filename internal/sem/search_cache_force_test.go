package sem

import (
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// selectiveCacheArtifacts lists every persisted search artifact except the
// complete entry's own file. It walks the cache instead of rebuilding the path
// so the test pins the BEHAVIOR of `--force`, not the layout the fix happens to
// use to get there.
func selectiveCacheArtifacts(t *testing.T, cacheDir, completeKey string) []string {
	t.Helper()
	root := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion)
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json.gz") {
			return nil
		}
		if entry.Name() == completeKey+".json.gz" {
			return nil
		}
		found = append(found, path)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return found
}

func readCachedSnapshotFile(t *testing.T, path string) cachedSearchSnapshot {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var cache cachedSearchSnapshot
	if err := json.NewDecoder(reader).Decode(&cache); err != nil {
		t.Fatal(err)
	}
	return cache
}

func writeCachedSnapshotFile(t *testing.T, path string, cache cachedSearchSnapshot) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if err := json.NewEncoder(writer).Encode(cache); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// selectiveSearchEntryPath is where a selective snapshot is persisted: beneath a
// directory named by the complete entry it was derived from, which is what makes
// the dependent set nameable when that entry is rebuilt. Tests that assert
// persistence ask for the path here rather than spelling it, so the layout lives
// in one place.
func selectiveSearchEntryPath(t *testing.T, cacheDir, absRepo, repositoryKey, providerVersion, tree string, options ProviderSnapshotOptions) string {
	t.Helper()
	completeOptions := options
	completeOptions.OnlyFiles = nil
	completeKey, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, completeOptions)
	if err != nil {
		t.Fatal(err)
	}
	selectiveKey, err := searchSnapshotKey(absRepo, repositoryKey, providerVersion, tree, options)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newDerivedCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, completeKey, selectiveKey)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(entry.root, entry.relative)
}

func snapshotHasSymbolNamed(snapshot ProviderSnapshot, name string) bool {
	for _, symbol := range snapshot.Symbols {
		if symbol.Name == name {
			return true
		}
	}
	return false
}

// `index --force` exists for exactly one situation: a persisted entry is wrong
// and the operator wants it gone. Refreshing the complete entry did not achieve
// that, because a selective search reads its OWN entry before the complete one
// is ever consulted — so the query that motivated the rebuild kept being
// answered from the artifact the rebuild was supposed to replace, for as long as
// HEAD did not move, with nothing in the output to say so.
//
// The two states that must not share an answer here are "before --force" and
// "after --force". The corrupt entry is installed directly, which is the only
// way to reach the state --force is for: a correct build never writes one.
func TestPreindexForceInvalidatesDerivedSelectiveSnapshots(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "a.go", "package a\n\nfunc Alpha() int { return 1 }\n")
	write(t, repo, "b.go", "package a\n\nfunc Beta() int { return 2 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	cacheDir := t.TempDir()
	complete := func() ProviderSnapshotOptions { return ProviderSnapshotOptions{Profile: ProfileFull} }
	selective := func() ProviderSnapshotOptions {
		options := complete()
		options.OnlyFiles = []string{"a.go"}
		return options
	}

	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "v", complete(), cacheDir); err != nil {
		t.Fatal(err)
	}
	// Warm the selective entry through the ordinary query path.
	if _, _, err := loadOrBuildSearchSnapshot(t.Context(), repo, "v", selective(), cacheDir, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := loadOrBuildSearchSnapshot(t.Context(), repo, "v", selective(), cacheDir, false, nil); err != nil {
		t.Fatal(err)
	} else if !hit {
		t.Fatal("second selective query should be served from its own persisted entry")
	}

	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	repositoryKey := repoKey(t.Context(), absRepo)
	_, tree, err := resolveCommittedHEAD(t.Context(), absRepo)
	if err != nil {
		t.Fatal(err)
	}
	completeKey, err := searchSnapshotKey(absRepo, repositoryKey, "v", tree, complete())
	if err != nil {
		t.Fatal(err)
	}

	artifacts := selectiveCacheArtifacts(t, cacheDir, completeKey)
	if len(artifacts) != 1 {
		t.Fatalf("expected exactly one persisted selective artifact, got %v", artifacts)
	}
	corrupted := readCachedSnapshotFile(t, artifacts[0])
	for index := range corrupted.Snapshot.Symbols {
		corrupted.Snapshot.Symbols[index].Name = "CorruptedByAPreviousBuild"
	}
	writeCachedSnapshotFile(t, artifacts[0], corrupted)

	// The corrupt entry really is what the query is answered from; without this
	// the rest of the test could pass for the wrong reason.
	served, hit, err := loadOrBuildSearchSnapshot(t.Context(), repo, "v", selective(), cacheDir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hit || !snapshotHasSymbolNamed(served, "CorruptedByAPreviousBuild") {
		t.Fatalf("corrupt selective entry was not being served (hit=%v); the test cannot show --force clearing it", hit)
	}

	forced := complete()
	forced.ForceRebuild = true
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, "v", forced, cacheDir); err != nil {
		t.Fatal(err)
	}

	served, _, err = loadOrBuildSearchSnapshot(t.Context(), repo, "v", selective(), cacheDir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotHasSymbolNamed(served, "CorruptedByAPreviousBuild") {
		t.Fatal("selective search still served the corrupt entry after index --force rebuilt the snapshot it derives from")
	}
	if !snapshotHasSymbolNamed(served, "Alpha") {
		t.Fatalf("selective search after --force lost the real symbol; got %d symbols", len(served.Symbols))
	}
	// The selective scope is still a scope: --force must not widen it.
	if snapshotHasSymbolNamed(served, "Beta") {
		t.Fatal("selective search after --force returned a file outside OnlyFiles")
	}
}
