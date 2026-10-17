package sem

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckoutScopeLookupHonorsCallerCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git stub")
	}
	repo := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(repo); err == nil {
		repo = canonical
	}
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, output)
	}
	validated, err := WithGitMetadataValidationForSetup(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(bin); err == nil {
		bin = canonical
	}
	marker := filepath.Join(bin, "called")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fmt.Sprintf("#!/bin/sh\nprintf reached > '%s'\nexec sleep 5\n", strings.ReplaceAll(marker, "'", "'\"'\"'"))), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(validated)
	defer cancel()
	canceledAt := cancelScopeAfterGitStarts(marker, cancel)
	scope := cacheCheckoutScope(repo, ctx)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Git prefix lookup was not exercised")
	}
	if elapsed := time.Since(<-canceledAt); elapsed > time.Second {
		t.Fatalf("caller cancellation ignored: %v", elapsed)
	}
	if scope != "path:"+filepath.Clean(repo) {
		t.Fatalf("failed lookup must fall back to private scope, got %q", scope)
	}
}

func TestRecordsCacheLoadHonorsCallerCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git stub")
	}
	first, second := scopeWorktrees(t)
	cacheDir := t.TempDir()
	header := storeScopeRecords(t, first, cacheDir)
	validated, err := WithGitMetadataValidationForSetup(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(validated)
	defer cancel()
	replay, err := BeginProviderRecordsCache(ctx, second, "test-v1", header.Commit, header.Tree, "snapshot", cacheDir, ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit := replay.Load(); !hit {
		t.Fatal("control: cross-checkout entry must be a hit")
	}
	bin := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(bin); err == nil {
		bin = canonical
	}
	marker := filepath.Join(bin, "called")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fmt.Sprintf("#!/bin/sh\nprintf reached > '%s'\nexec sleep 5\n", strings.ReplaceAll(marker, "'", "'\"'\"'"))), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	canceledAt := cancelScopeAfterGitStarts(marker, cancel)
	if _, _, hit := replay.Load(); hit {
		t.Fatal("canceled scope lookup must not share the entry")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cross-checkout Git lookup was not exercised")
	}
	if elapsed := time.Since(<-canceledAt); elapsed > time.Second {
		t.Fatalf("record cache load ignored caller cancellation: %v", elapsed)
	}
}

func TestRecordsCacheLoadRevalidatesChangedGitMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git stub")
	}
	first, second := scopeWorktrees(t)
	cache := t.TempDir()
	header := storeScopeRecords(t, first, cache)
	replay, err := BeginProviderRecordsCache(t.Context(), second, "test-v1", header.Commit, header.Tree, "snapshot", cache, ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit := replay.Load(); !hit {
		t.Fatal("control: cross-checkout cache must be available")
	}
	git(t, second, "config", "remote.origin.promisor", "true")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "prefix-called")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	stub := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = --show-prefix ]; then\nprintf reached > " + quote(marker) + "\nexit 0\nfi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, hit := replay.Load(); hit {
		t.Error("metadata refused after setup still supplied a cross-checkout hit")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("scope Git subprocess ran after metadata became unsafe")
	}
}

// Start cancellation only after the stub is running: process startup and metadata
// validation must not consume a tiny deadline before the operation under test.
func cancelScopeAfterGitStarts(marker string, cancel context.CancelFunc) <-chan time.Time {
	result := make(chan time.Time, 1)
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(marker); err == nil {
				result <- time.Now()
				cancel()
				return
			}
			select {
			case <-timer.C:
				result <- time.Time{}
				cancel()
				return
			case <-ticker.C:
			}
		}
	}()
	return result
}
