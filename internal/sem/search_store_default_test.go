package sem

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreindexHitSurfacesStoreRepairFailureAsWarning pins how a no-op index
// treats a sidecar it cannot build now that the sidecar is on by default: the
// index succeeds (an operator who never opted in must not see it fail on, say,
// a full disk) and the failure is a W_SEARCH_STORE_UNAVAILABLE header warning,
// never a silent drop. Break caught: the repair error failing `index` again,
// or being swallowed.
func TestPreindexHitSurfacesStoreRepairFailureAsWarning(t *testing.T) {
	t.Setenv(searchStoreEnv, "0")
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Store Default Test")
	git(t, repo, "config", "user.email", "store-default@example.invalid")
	write(t, repo, "main.go", "package sample\n\nfunc Render() {}\n")
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
	cacheDir := t.TempDir()
	options := ProviderSnapshotOptions{NoNetwork: true, Profile: ProfileFull}
	const version = "store-repair-warning-test"
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, version, options, cacheDir); err != nil {
		t.Fatal(err)
	}

	var entries []string
	if err := filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json.gz") {
			entries = append(entries, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one complete entry, got %q", entries)
	}
	// A non-empty directory where the sidecar belongs: the meta check cannot
	// open it, and the rebuild can neither remove it nor rename over it.
	sidecar := strings.TrimSuffix(entries[0], ".json.gz") + ".sqlite"
	if err := os.MkdirAll(filepath.Join(sidecar, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv(searchStoreEnv)
	snapshot, hit, err := PreindexProviderSnapshot(t.Context(), repo, version, options, cacheDir)
	if err != nil {
		t.Fatalf("a sidecar repair failure failed the index: %v", err)
	}
	if !hit {
		t.Fatal("second index was not a cache hit")
	}
	var found *ProviderWarning
	for index := range snapshot.Header.Warnings {
		if snapshot.Header.Warnings[index].Code == SearchStoreUnavailableCode {
			found = &snapshot.Header.Warnings[index]
		}
	}
	if found == nil || found.Detail == "" || found.Severity != "warning" {
		t.Fatalf("repair failure not surfaced as a %s warning: %+v", SearchStoreUnavailableCode, snapshot.Header.Warnings)
	}
}
