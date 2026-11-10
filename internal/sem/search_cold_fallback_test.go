package sem

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The warm Git tree grep keeps every matching file, so a cold query may use it only
// when a complete snapshot serves the view. These tests take away the complete
// snapshot in each way it can go missing after the query has selected, and require
// the bounded content-scan selection a cold query made before the warm route existed.

const coldFallbackPool = 2

func coldFallbackOptions(cacheDir string) SearchOptions {
	return SearchOptions{Profile: ProfileFull, TopK: 10, defaultIndexedFilesForTest: coldFallbackPool, CacheDir: cacheDir}
}

func requireBoundedColdSelection(t *testing.T, response SearchResponse) {
	t.Helper()
	if response.Stats.IndexCacheHit || len(response.Results) == 0 {
		t.Fatalf("premise lost: want a cold query with results: %#v", response.Stats)
	}
	if response.Stats.PreselectionBackend != "go-content" || response.Stats.FilesIndexed == 0 ||
		response.Stats.FilesIndexed > coldFallbackPool {
		t.Fatalf("a cold query no complete snapshot could serve left the bounded selection: backend=%q files_indexed=%d (pool %d)",
			response.Stats.PreselectionBackend, response.Stats.FilesIndexed, coldFallbackPool)
	}
}

// coldAllMatchFixture: every file contains the query term, so the warm grep selects
// the whole corpus and the view is the complete snapshot itself (OnlyFiles nil).
func coldAllMatchFixture(t *testing.T) string {
	t.Helper()
	return aliasGrepFixture(t, map[string]string{
		"a/one.go":   "package a\n\n// One posts the ledger.\nfunc One() int { return 1 }\n",
		"a/two.go":   "package a\n\n// Two audits the ledger.\nfunc Two() int { return 2 }\n",
		"b/three.go": "package b\n\n// Three closes the ledger.\nfunc Three() int { return 3 }\n",
		"b/four.go":  "package b\n\n// Four opens the ledger.\nfunc Four() int { return 4 }\n",
		"c/five.go":  "package c\n\n// Five reads the ledger.\nfunc Five() int { return 5 }\n",
	})
}

// coldFallbackViews are the two shapes a warm selection takes: a selective view
// derived from the complete snapshot, and the complete snapshot itself.
var coldFallbackViews = []struct {
	name    string
	fixture func(*testing.T) string
	query   string
}{
	{"selective view", coldWarmParityFixture, "Format ledger entry"},
	{"complete view", coldAllMatchFixture, "ledger"},
}

// TestColdFallbackUnknownGenerationKeepsBoundedSelection: an unreadable cache
// generation marker rules out the cold-complete build, which only the loader sees.
func TestColdFallbackUnknownGenerationKeepsBoundedSelection(t *testing.T) {
	t.Parallel()
	repo := coldWarmParityFixture(t)
	cacheDir := t.TempDir()
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	completeOptions, err := CaptureProviderCachePolicy(absRepo, ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	_, tree, err := resolveCommittedHEAD(t.Context(), absRepo)
	if err != nil {
		t.Fatal(err)
	}
	completeKey, err := searchSnapshotKey(absRepo, repoKey(t.Context(), absRepo), "test-version", tree, completeOptions, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	marker := cacheGenerationMarkerPath(t, cacheDir, completeKey)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("not a gzip stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCacheGeneration(cacheDir, "search", searchSnapshotCacheVersion, completeKey); err == nil {
		t.Fatal("premise lost: the corrupt marker must make the generation unreadable")
	}

	response, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", coldFallbackOptions(cacheDir))
	if err != nil {
		t.Fatal(err)
	}
	requireBoundedColdSelection(t, response)
}

// TestColdFallbackUncacheablePolicyKeepsBoundedSelection: a policy whose inputs
// exceed the capture budget is searched uncached, so no complete build follows.
func TestColdFallbackUncacheablePolicyKeepsBoundedSelection(t *testing.T) {
	t.Parallel()
	repo := coldWarmParityFixture(t)
	comments := filepath.Join(t.TempDir(), "comments.ignore")
	if err := os.WriteFile(comments, bytes.Repeat([]byte("#\n"), maxIgnoreFileBytes/2), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoreFiles := []string{comments, comments, comments, comments, comments}
	if _, err := CaptureProviderCachePolicy(repo, ProviderSnapshotOptions{IgnoreFiles: ignoreFiles}); err == nil {
		t.Fatal("premise lost: the policy must exceed the capture budget")
	}
	options := coldFallbackOptions(t.TempDir())
	options.IgnoreFiles = ignoreFiles
	response, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options)
	if err != nil {
		t.Fatal(err)
	}
	requireBoundedColdSelection(t, response)
}

// TestColdFallbackInvalidCompleteBuildKeepsBoundedSelection: HEAD moves while the
// complete snapshot is being built, so the build does not validate for the tree
// the query selected from and is not published.
func TestColdFallbackInvalidCompleteBuildKeepsBoundedSelection(t *testing.T) {
	for _, view := range coldFallbackViews {
		t.Run(view.name, func(t *testing.T) {
			repo := view.fixture(t)
			builds := 0
			previous := beforeColdCompleteBuild
			beforeColdCompleteBuild = func() {
				builds++
				if builds == 1 {
					write(t, repo, "z/moved.go", "package z\n\nfunc Moved() int { return 0 }\n")
					git(t, repo, "add", ".")
					git(t, repo, "commit", "-m", "move HEAD during the complete build")
				}
			}
			t.Cleanup(func() { beforeColdCompleteBuild = previous })

			response, err := SearchRepository(t.Context(), repo, "test-version", view.query, coldFallbackOptions(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			if builds != 1 {
				t.Fatalf("premise lost: want exactly the one complete build that HEAD moved under, got %d", builds)
			}
			requireBoundedColdSelection(t, response)
		})
	}
}

// TestColdFallbackFailedCompleteBuildKeepsBoundedSelection: the complete build
// fails outright. While it runs the repository cannot be read; the next attempt's
// preindex lookup makes it readable again.
func TestColdFallbackFailedCompleteBuildKeepsBoundedSelection(t *testing.T) {
	probe := t.TempDir()
	if err := os.Chmod(probe, 0); err != nil {
		t.Fatal(err)
	}
	_, readErr := os.ReadDir(probe)
	if err := os.Chmod(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if readErr == nil {
		t.Skip("directory permissions are not enforced here (Windows, or running as root)")
	}
	for _, view := range coldFallbackViews {
		t.Run(view.name, func(t *testing.T) {
			repo := view.fixture(t)
			t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })
			builds := 0
			previous := beforeColdCompleteBuild
			beforeColdCompleteBuild = func() {
				builds++
				if err := os.Chmod(repo, 0); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { beforeColdCompleteBuild = previous })
			options := coldFallbackOptions(t.TempDir())
			options.afterPreindexLoad = func() {
				if err := os.Chmod(repo, 0o755); err != nil {
					t.Error(err)
				}
			}

			response, err := SearchRepository(t.Context(), repo, "test-version", view.query, options)
			if err != nil {
				t.Fatal(err)
			}
			if builds != 1 {
				t.Fatalf("premise lost: want exactly the one complete build that failed, got %d", builds)
			}
			requireBoundedColdSelection(t, response)
		})
	}
}

// The refusal is the loader's, so it must not leak to a caller that never asked
// for the warm route: a selection without required builds as it always did.
func TestColdCompleteRefusalIsOnlyForRequiredSelections(t *testing.T) {
	t.Parallel()
	repo := coldWarmParityFixture(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull, OnlyFiles: []string{"alpha/format.go"}}
	if _, _, err := loadOrBuildSearchSnapshot(t.Context(), repo, "test-version", options, "", true, nil); err != nil {
		t.Fatalf("an uncached selective build without required was refused: %v", err)
	}
	options.coldComplete.required = true
	if _, _, err := loadOrBuildSearchSnapshot(t.Context(), repo, "test-version", options, "", true, nil); !errors.Is(err, errColdCompleteUnavailable) {
		t.Fatalf("an uncached build of a required selection was not refused: %v", err)
	}
}

// The complete-view fallback cases above rely on the warm grep selecting every
// file of coldAllMatchFixture, which makes the view the complete snapshot itself.
func TestColdAllMatchQueryIsTheCompleteView(t *testing.T) {
	t.Parallel()
	response, err := SearchRepository(t.Context(), coldAllMatchFixture(t), "test-version", "ledger", coldFallbackOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if response.Stats.PreselectionBackend != "git-tree-grep" || response.Stats.FilesIndexed != response.Stats.FilesScanned ||
		response.Stats.FilesIndexed <= coldFallbackPool {
		t.Fatalf("fixture drift: want the warm grep to select the whole corpus: backend=%q files_indexed=%d of %d",
			response.Stats.PreselectionBackend, response.Stats.FilesIndexed, response.Stats.FilesScanned)
	}
}
