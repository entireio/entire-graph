package sem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// coldBuildLockChildEnv selects the role a re-executed test binary plays.
const coldBuildLockChildEnv = "ENTIRE_GRAPH_TEST_COLD_BUILD_LOCK_CHILD"

const (
	coldBuildLockRoleQuery = "query"
	coldBuildLockRoleHold  = "hold"
)

var coldBuildLockQueryOptions = SearchOptions{Profile: ProfileFull, TopK: 10, MaxIndexedFiles: 2}

// TestColdBuildLockChild is not a test on its own: it is the body a
// re-executed test binary runs as one of several concurrent processes.
func TestColdBuildLockChild(t *testing.T) {
	role := os.Getenv(coldBuildLockChildEnv)
	if role == "" {
		t.Skip("helper process for the cold build lock tests")
	}
	repo := os.Getenv("COLD_BUILD_LOCK_REPO")
	cacheDir := os.Getenv("COLD_BUILD_LOCK_CACHE")
	switch role {
	case coldBuildLockRoleQuery:
		counter := os.Getenv("COLD_BUILD_LOCK_COUNTER")
		beforeColdCompleteBuild = func() {
			file, err := os.OpenFile(counter, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
			if err == nil {
				_, _ = fmt.Fprintf(file, "build %d\n", os.Getpid())
				_ = file.Close()
			}
			// Hold the build open long enough that every other process has
			// already missed the cache: without the lock each would build too.
			time.Sleep(time.Second)
		}
		if err := os.WriteFile(os.Getenv("COLD_BUILD_LOCK_READY"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		start := os.Getenv("COLD_BUILD_LOCK_START")
		for deadline := time.Now().Add(30 * time.Second); ; {
			if _, err := os.Stat(start); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("start barrier never opened")
			}
			time.Sleep(5 * time.Millisecond)
		}
		options := coldBuildLockQueryOptions
		options.CacheDir = cacheDir
		result, err := SearchRepository(context.Background(), repo, "test-version", "Format ledger entry", options)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("COLD-BUILD-LOCK-RESULT hit=%v results=%d\n", result.Stats.IndexCacheHit, len(result.Results))
	case coldBuildLockRoleHold:
		file, err := openCompleteBuildLockFile(cacheDir, "search", searchSnapshotCacheVersion, os.Getenv("COLD_BUILD_LOCK_KEY"))
		if err != nil {
			t.Fatal(err)
		}
		if err := tryLockBuildLockFile(file); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("COLD_BUILD_LOCK_READY"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		// Hold the lock until the parent kills this process: the kernel, not
		// this code, is what must release it.
		time.Sleep(10 * time.Minute)
	default:
		t.Fatalf("unknown role %q", role)
	}
}

func coldBuildLockChild(t *testing.T, role string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestColdBuildLockChild$", "-test.v=true", "-test.count=1")
	cmd.Env = append(append(os.Environ(), coldBuildLockChildEnv+"="+role), env...)
	return cmd
}

func waitForFiles(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for _, path := range paths {
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper process never signalled %s", path)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TestConcurrentColdQueriesBuildCompleteSnapshotOnce pins the fix for parallel
// agents: several PROCESSES running a cold query against one uncached tree at
// the same moment produce exactly one complete build. The rest wait on the
// build lock and are then served the published snapshot as a cache hit.
func TestConcurrentColdQueriesBuildCompleteSnapshotOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns helper processes")
	}
	repo := selectiveFilterFixture(t)
	cacheDir := t.TempDir()
	signals := t.TempDir()
	counter := filepath.Join(signals, "builds")
	start := filepath.Join(signals, "start")

	const processes = 3
	type child struct {
		cmd    *exec.Cmd
		output *bytes.Buffer
	}
	var children []child
	var ready []string
	for index := range processes {
		readyFile := filepath.Join(signals, fmt.Sprintf("ready-%d", index))
		ready = append(ready, readyFile)
		cmd := coldBuildLockChild(t, coldBuildLockRoleQuery,
			"COLD_BUILD_LOCK_REPO="+repo,
			"COLD_BUILD_LOCK_CACHE="+cacheDir,
			"COLD_BUILD_LOCK_COUNTER="+counter,
			"COLD_BUILD_LOCK_READY="+readyFile,
			"COLD_BUILD_LOCK_START="+start,
		)
		output := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{cmd: cmd, output: output})
	}
	t.Cleanup(func() {
		for _, child := range children {
			_ = child.cmd.Process.Kill()
			_ = child.cmd.Wait()
		}
	})
	waitForFiles(t, ready...)
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	hits, misses := 0, 0
	for _, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Fatalf("helper query failed: %v\n%s", err, child.output)
		}
		out := child.output.String()
		switch {
		case strings.Contains(out, "COLD-BUILD-LOCK-RESULT hit=true"):
			hits++
		case strings.Contains(out, "COLD-BUILD-LOCK-RESULT hit=false"):
			misses++
		default:
			t.Fatalf("helper query reported no result:\n%s", out)
		}
		if strings.Contains(out, "results=0") {
			t.Fatalf("helper query returned no results:\n%s", out)
		}
	}
	builds, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(builds), "build "); count != 1 || misses != 1 || hits != processes-1 {
		t.Fatalf("%d concurrent cold queries ran %d complete builds (misses=%d hits=%d), want exactly 1 build:\n%s",
			processes, count, misses, hits, builds)
	}
}

// coldBuildLockKey runs one cold query in a throwaway cache directory and
// returns the complete-snapshot key its build lock was named after. The key
// does not depend on the cache directory, so it names the same lock in any.
func coldBuildLockKey(t *testing.T, repo string) string {
	t.Helper()
	probe := t.TempDir()
	options := coldBuildLockQueryOptions
	options.CacheDir = probe
	if _, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options); err != nil {
		t.Fatal(err)
	}
	locks, err := filepath.Glob(filepath.Join(probe, "search", searchSnapshotCacheVersion, "*.build.lock"))
	if err != nil || len(locks) != 1 {
		t.Fatalf("want exactly one build lock beside the complete entry, got %v (err %v)", locks, err)
	}
	key := strings.TrimSuffix(filepath.Base(locks[0]), ".build.lock")
	if _, err := os.Stat(filepath.Join(probe, "search", searchSnapshotCacheVersion, key+".json.gz")); err != nil {
		t.Fatalf("the build lock is not named after the complete entry it guards: %v", err)
	}
	return key
}

func holdColdBuildLock(t *testing.T, cacheDir, key string) {
	t.Helper()
	file, err := openCompleteBuildLockFile(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := tryLockBuildLockFile(file); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unlockBuildLockFile(file)
		_ = file.Close()
	})
}

func requireCompleteSnapshotPersisted(t *testing.T, repo, cacheDir string) {
	t.Helper()
	if _, hit, err := loadCachedCompleteSearchSnapshot(t.Context(), repo, "test-version", ProviderSnapshotOptions{Profile: ProfileFull}, cacheDir); err != nil || !hit {
		t.Fatalf("the cold query did not persist the complete snapshot: hit=%v err=%v", hit, err)
	}
}

// TestColdBuildLockWaitTimesOutAndBuilds pins the bound on waiting: a holder
// that never finishes costs a waiter completeBuildLockWait and then a build of
// its own, never a hung or failed query.
func TestColdBuildLockWaitTimesOutAndBuilds(t *testing.T) {
	repo := selectiveFilterFixture(t)
	key := coldBuildLockKey(t, repo)
	cacheDir := t.TempDir()
	holdColdBuildLock(t, cacheDir, key)

	previous := completeBuildLockWait
	completeBuildLockWait = 300 * time.Millisecond
	t.Cleanup(func() { completeBuildLockWait = previous })

	options := coldBuildLockQueryOptions
	options.CacheDir = cacheDir
	began := time.Now()
	result, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options)
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited < completeBuildLockWait {
		t.Fatalf("query returned after %v without waiting for the held lock", waited)
	}
	if result.Stats.IndexCacheHit || len(result.Results) == 0 {
		t.Fatalf("a timed-out waiter must build and answer itself: %#v", result.Stats)
	}
	requireCompleteSnapshotPersisted(t, repo, cacheDir)
}

// TestColdBuildLockLeftByKilledHolderIsFree pins crash recovery: the lock is an
// OS file lock, so a holder killed mid-build leaves the lock FILE behind but
// not the lock, and the next query takes it at once instead of waiting it out.
func TestColdBuildLockLeftByKilledHolderIsFree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a helper process")
	}
	repo := selectiveFilterFixture(t)
	key := coldBuildLockKey(t, repo)
	cacheDir := t.TempDir()
	ready := filepath.Join(t.TempDir(), "locked")
	holder := coldBuildLockChild(t, coldBuildLockRoleHold,
		"COLD_BUILD_LOCK_CACHE="+cacheDir,
		"COLD_BUILD_LOCK_KEY="+key,
		"COLD_BUILD_LOCK_READY="+ready,
	)
	var output bytes.Buffer
	holder.Stdout, holder.Stderr = &output, &output
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	waitForFiles(t, ready)

	// Premise: the helper really holds the lock.
	probe, err := openCompleteBuildLockFile(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := tryLockBuildLockFile(probe); !errors.Is(err, errBuildLockBusy) {
		_ = probe.Close()
		t.Fatalf("premise lost: the helper's lock was not held (%v)\n%s", err, output.String())
	}
	_ = probe.Close()

	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	lockPath := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".build.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("premise lost: the killed holder's lock file is gone: %v", err)
	}

	previous := completeBuildLockWait
	completeBuildLockWait = 30 * time.Second
	t.Cleanup(func() { completeBuildLockWait = previous })
	options := coldBuildLockQueryOptions
	options.CacheDir = cacheDir
	began := time.Now()
	result, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options)
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(began); waited >= 10*time.Second {
		t.Fatalf("a lock left by a killed holder was waited on for %v", waited)
	}
	if result.Stats.IndexCacheHit || len(result.Results) == 0 {
		t.Fatalf("premise lost: %#v", result.Stats)
	}
	requireCompleteSnapshotPersisted(t, repo, cacheDir)
}

// TestColdBuildLockWaitHonoursCancellation pins that a waiter stops when its
// query is cancelled instead of sitting out the whole wait.
func TestColdBuildLockWaitHonoursCancellation(t *testing.T) {
	repo := selectiveFilterFixture(t)
	key := coldBuildLockKey(t, repo)
	cacheDir := t.TempDir()
	holdColdBuildLock(t, cacheDir, key)

	previous := completeBuildLockWait
	completeBuildLockWait = 30 * time.Second
	t.Cleanup(func() { completeBuildLockWait = previous })

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	options := coldBuildLockQueryOptions
	options.CacheDir = cacheDir
	began := time.Now()
	_, err := SearchRepository(ctx, repo, "test-version", "Format ledger entry", options)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a cancelled waiter returned %v, want the context's error", err)
	}
	if waited := time.Since(began); waited >= 10*time.Second {
		t.Fatalf("a cancelled waiter kept waiting for %v", waited)
	}
}

// TestColdBuildLockUnusableFallsBackToBuilding pins that a lock which cannot
// even be opened (here a directory squats on its name) is skipped: the query
// builds exactly as it did before the lock existed.
func TestColdBuildLockUnusableFallsBackToBuilding(t *testing.T) {
	repo := selectiveFilterFixture(t)
	key := coldBuildLockKey(t, repo)
	cacheDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".build.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := coldBuildLockQueryOptions
	options.CacheDir = cacheDir
	result, err := SearchRepository(t.Context(), repo, "test-version", "Format ledger entry", options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.IndexCacheHit || len(result.Results) == 0 {
		t.Fatalf("premise lost: %#v", result.Stats)
	}
	requireCompleteSnapshotPersisted(t, repo, cacheDir)
}

// TestCompleteQueryWaitsOnColdBuildLockThenHits pins the complete-query path
// (def, impact, neighbors): it builds the same complete entry a concurrent cold
// search does, so it takes the same lock, and a snapshot published while it
// waited is served as a hit instead of being built a second time.
func TestCompleteQueryWaitsOnColdBuildLockThenHits(t *testing.T) {
	repo := selectiveFilterFixture(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	probe := t.TempDir()
	if _, hit, err := LoadOrBuildProviderSnapshot(t.Context(), repo, "test-version", options, probe, false); err != nil || hit {
		t.Fatalf("premise lost: probe build hit=%v err=%v", hit, err)
	}
	entries, err := filepath.Glob(filepath.Join(probe, "search", searchSnapshotCacheVersion, "*.build.lock"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("want exactly one build lock beside the complete entry, got %v (err %v)", entries, err)
	}
	key := strings.TrimSuffix(filepath.Base(entries[0]), ".build.lock")
	published, err := os.ReadFile(filepath.Join(probe, "search", searchSnapshotCacheVersion, key+".json.gz"))
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	file, err := openCompleteBuildLockFile(cacheDir, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := tryLockBuildLockFile(file); err != nil {
		t.Fatal(err)
	}
	previous := completeBuildLockWait
	completeBuildLockWait = 30 * time.Second
	t.Cleanup(func() { completeBuildLockWait = previous })
	builds := 0
	beforeColdCompleteBuild = func() { builds++ }
	t.Cleanup(func() { beforeColdCompleteBuild = nil })

	type outcome struct {
		hit bool
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, hit, err := LoadOrBuildProviderSnapshot(context.Background(), repo, "test-version", options, cacheDir, false)
		done <- outcome{hit: hit, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("the complete query did not wait for the held build lock: %#v", got)
	case <-time.After(300 * time.Millisecond):
	}
	// The holder publishes, then releases.
	if err := os.WriteFile(filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz"), published, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unlockBuildLockFile(file); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.hit || builds != 0 {
			t.Fatalf("a waiter rebuilt what the holder published: hit=%v builds=%d err=%v", got.hit, builds, got.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the complete query never resumed after the lock was released")
	}
}
