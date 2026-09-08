package sem

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// cacheGenerationMarkerPath is where the generation marker of a complete entry
// lives. Tests ask for it here rather than spelling the layout, for the same
// reason selectiveSearchEntryPath exists.
func cacheGenerationMarkerPath(t *testing.T, cacheDir, completeKey string) string {
	t.Helper()
	entry, err := newCacheGenerationEntry(cacheDir, "search", searchSnapshotCacheVersion, completeKey)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(entry.root, entry.relative)
}

// generationMarkerFixture is a repository whose complete entry is preindexed and
// whose selective entry was derived BEFORE any generation existed, so the
// derived artifact carries the legacy empty generation.
type generationMarkerFixture struct {
	repo        string
	cacheDir    string
	complete    func() ProviderSnapshotOptions
	selective   func() ProviderSnapshotOptions
	completeKey string
	derivedPath string
	markerPath  string
}

func newGenerationMarkerFixture(t *testing.T) generationMarkerFixture {
	t.Helper()
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
	if _, _, err := loadOrBuildSearchSnapshot(t.Context(), repo, "v", selective(), cacheDir, false, nil); err != nil {
		t.Fatal(err)
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
	fixture := generationMarkerFixture{
		repo:        repo,
		cacheDir:    cacheDir,
		complete:    complete,
		selective:   selective,
		completeKey: completeKey,
		derivedPath: selectiveSearchEntryPath(t, cacheDir, repo, repositoryKey, "v", tree, selective()),
		markerPath:  cacheGenerationMarkerPath(t, cacheDir, completeKey),
	}
	if _, err := os.Stat(fixture.derivedPath); err != nil {
		t.Fatalf("a selective query made before any generation existed should persist its own entry: %v", err)
	}
	if got := readCachedSnapshotFile(t, fixture.derivedPath).DerivedFrom; got != "" {
		t.Fatalf("a derivation made before any generation existed should record the legacy empty generation, got %q", got)
	}
	return fixture
}

// A generation marker that cannot be read is not the same state as a marker that
// was never written, and the difference decides whether `--force` held. The empty
// generation is exactly what every pre-generation derivation recorded, so
// answering it for an unreadable marker revalidates the artifact the rebuild
// retired — the marker is then a comment rather than a control, and the racy
// behavior it was added to fix is back with nothing in the output to say so.
//
// Each case breaks the marker a different way the reader must survive: a
// component that redirects out of the cache root (open refuses), bytes that are
// not a gzip stream (the decompressor refuses), and a directory planted at the
// marker's name (the read refuses). None of them is fs.ErrNotExist.
func TestSelectiveCacheFailsClosedOnUnreadableGenerationMarker(t *testing.T) {
	t.Parallel()
	corruptions := map[string]func(t *testing.T, markerPath string){
		"escaping symlink": func(t *testing.T, markerPath string) {
			outside := filepath.Join(t.TempDir(), "elsewhere.json.gz")
			if err := os.WriteFile(outside, []byte("elsewhere"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(markerPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, markerPath); err != nil {
				t.Fatal(err)
			}
		},
		"not a gzip stream": func(t *testing.T, markerPath string) {
			if err := os.WriteFile(markerPath, []byte("this is not a gzip stream"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"directory at the marker name": func(t *testing.T, markerPath string) {
			if err := os.Remove(markerPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(markerPath, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newGenerationMarkerFixture(t)

			// What an in-flight pre-force deriver is holding: a view of the
			// outgoing snapshot stamped with the generation it read before the
			// rebuild started, which is the legacy empty one.
			inFlight := readCachedSnapshotFile(t, fixture.derivedPath)
			for index := range inFlight.Snapshot.Symbols {
				inFlight.Snapshot.Symbols[index].Name = "DerivedFromTheOutgoingSnapshot"
			}

			forced := fixture.complete()
			forced.ForceRebuild = true
			if _, _, err := PreindexProviderSnapshot(t.Context(), fixture.repo, "v", forced, fixture.cacheDir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(fixture.markerPath); err != nil {
				t.Fatalf("--force should have minted a generation marker: %v", err)
			}

			// The late write lands after the rebuild invalidated and removed.
			if err := os.MkdirAll(filepath.Dir(fixture.derivedPath), 0o700); err != nil {
				t.Fatal(err)
			}
			writeCachedSnapshotFile(t, fixture.derivedPath, inFlight)
			corrupt(t, fixture.markerPath)

			served, _, err := loadOrBuildSearchSnapshot(t.Context(), fixture.repo, "v", fixture.selective(), fixture.cacheDir, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if snapshotHasSymbolNamed(served, "DerivedFromTheOutgoingSnapshot") {
				t.Fatal("an unreadable generation marker was read as the legacy empty generation and revalidated a pre-force derivation")
			}
			if !snapshotHasSymbolNamed(served, "Alpha") {
				t.Fatalf("selective search lost the real symbol; got %d symbols", len(served.Symbols))
			}
			if snapshotHasSymbolNamed(served, "Beta") {
				t.Fatal("selective search returned a file outside OnlyFiles")
			}
			// Nothing may be persisted under a generation this process could not
			// read: a fresh view stamped with the legacy empty generation would be
			// served the moment the marker breaks again.
			if got := readCachedSnapshotFile(t, fixture.derivedPath); !snapshotHasSymbolNamed(got.Snapshot, "DerivedFromTheOutgoingSnapshot") {
				t.Fatal("a selective entry was persisted while the generation marker was unreadable")
			}
		})
	}
}

// The other direction: a marker that genuinely does not exist yet is the
// pre-generation state, and every artifact derived before the first rebuild
// recorded the empty generation for it. Failing closed on unreadable markers must
// not turn that ordinary state into a permanent selective cache miss.
func TestSelectiveCacheServesLegacyGenerationWhenMarkerAbsent(t *testing.T) {
	t.Parallel()
	fixture := newGenerationMarkerFixture(t)
	if _, err := os.Lstat(fixture.markerPath); !os.IsNotExist(err) {
		t.Fatalf("no --force has run, so no generation marker should exist yet: %v", err)
	}

	served, hit, err := loadOrBuildSearchSnapshot(t.Context(), fixture.repo, "v", fixture.selective(), fixture.cacheDir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("a derivation recorded before any generation existed must still be served from its own entry")
	}
	if !snapshotHasSymbolNamed(served, "Alpha") {
		t.Fatalf("selective search lost the real symbol; got %d symbols", len(served.Symbols))
	}
	if snapshotHasSymbolNamed(served, "Beta") {
		t.Fatal("selective search returned a file outside OnlyFiles")
	}
}

// writeGenerationMarkerBody installs exact bytes as the marker's gzip payload,
// which is how a marker reaches a state bumpCacheGeneration cannot mint.
func writeGenerationMarkerBody(t *testing.T, markerPath, body string) {
	t.Helper()
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(markerPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// A marker that decodes cleanly but carries no generation is the one corruption
// the reader cannot detect by failing to read it: the decode succeeds, and the
// value it yields is the legacy empty generation every pre-`--force` derivation
// stamped itself with. The marker's EXISTENCE is what says a rebuild happened,
// so a present marker holding that value contradicts itself, and answering it
// serves exactly the artifact the rebuild retired — the same silent staleness an
// unreadable marker would cause, reached through a decode that never errors.
//
// Both spellings must fail closed: an object with no generation member, and one
// that spells the member out as empty. Neither is a state bumpCacheGeneration
// can produce, so failing closed here retires no marker this program writes.
func TestSelectiveCacheFailsClosedOnEmptyGenerationMarker(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"no generation member":    "{}\n",
		"empty generation member": `{"generation":""}` + "\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newGenerationMarkerFixture(t)

			// What an in-flight pre-force deriver is holding: a view of the
			// outgoing snapshot stamped with the generation it read before the
			// rebuild started, which is the legacy empty one.
			inFlight := readCachedSnapshotFile(t, fixture.derivedPath)
			for index := range inFlight.Snapshot.Symbols {
				inFlight.Snapshot.Symbols[index].Name = "DerivedFromTheOutgoingSnapshot"
			}

			forced := fixture.complete()
			forced.ForceRebuild = true
			if _, _, err := PreindexProviderSnapshot(t.Context(), fixture.repo, "v", forced, fixture.cacheDir); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(fixture.markerPath); err != nil {
				t.Fatalf("--force should have minted a generation marker: %v", err)
			}

			// The late write lands after the rebuild invalidated and removed.
			if err := os.MkdirAll(filepath.Dir(fixture.derivedPath), 0o700); err != nil {
				t.Fatal(err)
			}
			writeCachedSnapshotFile(t, fixture.derivedPath, inFlight)
			writeGenerationMarkerBody(t, fixture.markerPath, body)

			if generation, err := readCacheGeneration(fixture.cacheDir, "search", searchSnapshotCacheVersion, fixture.completeKey); err == nil {
				t.Fatalf("a marker carrying no generation was accepted, returning %q", generation)
			}

			served, _, err := loadOrBuildSearchSnapshot(t.Context(), fixture.repo, "v", fixture.selective(), fixture.cacheDir, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if snapshotHasSymbolNamed(served, "DerivedFromTheOutgoingSnapshot") {
				t.Fatal("a marker recording no generation was read as the legacy empty generation and revalidated a pre-force derivation")
			}
			if !snapshotHasSymbolNamed(served, "Alpha") {
				t.Fatalf("selective search lost the real symbol; got %d symbols", len(served.Symbols))
			}
			if snapshotHasSymbolNamed(served, "Beta") {
				t.Fatal("selective search returned a file outside OnlyFiles")
			}
			// Nothing may be persisted under a generation this process refused:
			// a fresh view stamped with the legacy empty generation would be
			// served the moment the marker is emptied again.
			if got := readCachedSnapshotFile(t, fixture.derivedPath); !snapshotHasSymbolNamed(got.Snapshot, "DerivedFromTheOutgoingSnapshot") {
				t.Fatal("a selective entry was persisted while the generation marker recorded no generation")
			}
		})
	}
}

// A complete snapshot handed in from an earlier call was read BEFORE the
// selective loader reads the generation, which runs the generation's ordering
// backwards. If a forced rebuild publishes and bumps in between, a view derived
// from that OUTGOING snapshot gets stamped with the INCOMING generation and
// validates from then on - strictly worse than the race the marker closed,
// because the invalidating removal has already happened, so nothing later
// discards it. The preloaded snapshot must therefore carry the generation it
// was read under and be discarded once that generation is no longer current.
func TestPreloadedCompleteSnapshotIsBoundToItsGeneration(t *testing.T) {
	t.Parallel()
	fixture := newGenerationMarkerFixture(t)

	// T0: what a search reads into memory before any rebuild begins, together
	// with the generation current at that moment.
	binding, hit, err := loadCachedCompleteSearchSnapshotBinding(t.Context(), fixture.repo, "v", fixture.complete(), fixture.cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("the preindexed complete snapshot did not load")
	}
	if !binding.generationKnown || binding.generation != "" {
		t.Fatalf("no --force has run, so the preload should carry the legacy empty generation; got %q known=%v", binding.generation, binding.generationKnown)
	}
	for index := range binding.snapshot.Symbols {
		binding.snapshot.Symbols[index].Name = "DerivedFromTheOutgoingSnapshot"
	}

	// T1: the rebuild publishes, bumps the generation, and removes the views
	// derived from the snapshot the preload is still holding.
	forced := fixture.complete()
	forced.ForceRebuild = true
	if _, _, err := PreindexProviderSnapshot(t.Context(), fixture.repo, "v", forced, fixture.cacheDir); err != nil {
		t.Fatal(err)
	}
	current, err := readCacheGeneration(fixture.cacheDir, "search", searchSnapshotCacheVersion, fixture.completeKey)
	if err != nil {
		t.Fatal(err)
	}
	if current == binding.generation {
		t.Fatal("--force did not move the generation; the fixture no longer poses the question")
	}

	// T2: the selective query arrives holding the retired snapshot.
	served, _, err := loadOrDeriveSelectiveSearchSnapshot(t.Context(), fixture.repo, "v", fixture.selective(), fixture.cacheDir, false, binding)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotHasSymbolNamed(served, "DerivedFromTheOutgoingSnapshot") {
		t.Fatal("a preloaded snapshot read under a retired generation was derived from anyway")
	}
	if !snapshotHasSymbolNamed(served, "Alpha") {
		t.Fatalf("selective search lost the real symbol; got %d symbols", len(served.Symbols))
	}
	if snapshotHasSymbolNamed(served, "Beta") {
		t.Fatal("selective search returned a file outside OnlyFiles")
	}

	// The damage the ordering causes is durable, so the check that matters is
	// what an ORDINARY later query - holding no preload at all - is served.
	later, _, err := loadOrBuildSearchSnapshot(t.Context(), fixture.repo, "v", fixture.selective(), fixture.cacheDir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotHasSymbolNamed(later, "DerivedFromTheOutgoingSnapshot") {
		t.Fatal("a view of the retired snapshot was persisted under the incoming generation and is now permanently valid")
	}
	if persisted := readCachedSnapshotFile(t, fixture.derivedPath); persisted.DerivedFrom != current {
		t.Fatalf("the persisted selective entry should carry the current generation %q, got %q", current, persisted.DerivedFrom)
	}
}
