package sem

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A failed co-change history read used to drop every FILE_CHANGES_WITH edge with no trace, so a
// repository whose history could not be read looked exactly like one whose files never changed
// together. The read now discloses itself as W_COCHANGE_HISTORY_UNAVAILABLE.

const stubCochangeFailure = "fatal: unable to read 1adc93b33d50b734c22d565d205429259283af45 (stub)"

// stubFailingCochangeGit puts a git on PATH that fails only the co-change read (`log --name-only`)
// and hands every other invocation to the real git, so HEAD resolution and file listing still work.
// The stub drains stdin before exiting so a caller's write never races the exit.
func stubFailingCochangeGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Git stub")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = --name-only ]; then\ncat >/dev/null\n" +
		"echo " + quote(stubCochangeFailure) + " >&2\nexit 128\nfi\ndone\nexec " + quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func cochangeRelationCount(snapshot ProviderSnapshot) int {
	count := 0
	for _, relation := range snapshot.Relations {
		if relation.Type == "FILE_CHANGES_WITH" {
			count++
		}
	}
	return count
}

// cochangeWarnings returns every W_COCHANGE_HISTORY_UNAVAILABLE warning in warnings.
func cochangeWarnings(warnings []ProviderWarning) []ProviderWarning {
	var found []ProviderWarning
	for _, warning := range warnings {
		if warning.Code == CochangeHistoryUnavailableCode {
			found = append(found, warning)
		}
	}
	return found
}

func TestSnapshotWarnsWhenCochangeHistoryCannotBeRead(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	options := ProviderSnapshotOptions{Profile: ProfileFull}

	control, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "history-test", options)
	if err != nil {
		t.Fatal(err)
	}
	if cochangeRelationCount(control) == 0 || len(cochangeWarnings(control.Header.Warnings)) != 0 {
		t.Fatalf("control: readable history must yield co-change edges and no warning, got %d edges, warnings %#v",
			cochangeRelationCount(control), control.Header.Warnings)
	}

	stubFailingCochangeGit(t)
	failed, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "history-test", options)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Header.Commit == "" {
		t.Fatal("premise: the stub must leave HEAD resolution working, or no history read is attempted")
	}
	if got := cochangeRelationCount(failed); got != 0 {
		t.Fatalf("premise: the failed history read must yield no co-change edges, got %d", got)
	}
	warnings := cochangeWarnings(failed.Header.Warnings)
	if len(warnings) != 1 {
		t.Fatalf("a failed co-change read must be disclosed exactly once, got %#v", failed.Header.Warnings)
	}
	if warnings[0].Severity != "warning" || !strings.Contains(warnings[0].Detail, stubCochangeFailure) ||
		!strings.Contains(warnings[0].EffectOnCompleteness, "FILE_CHANGES_WITH") {
		t.Fatalf("warning must name the effect and carry Git's reason: %#v", warnings[0])
	}
}

// The index command builds through a records-cache transaction, which reads history once up front.
// Its failure must reach the snapshot with Git's own reason, not a generic one.
func TestRecordsCacheTransactionDisclosesCochangeHistoryFailure(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	commit, tree, err := resolveCommittedHEAD(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	stubFailingCochangeGit(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	transaction, err := BeginProviderRecordsCache(t.Context(), repo, "history-test", commit, tree, "snapshot", t.TempDir(), options)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "history-test", transaction.Options())
	if err != nil {
		t.Fatal(err)
	}
	warnings := cochangeWarnings(snapshot.Header.Warnings)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Detail, stubCochangeFailure) {
		t.Fatalf("transaction-pinned history failure must be disclosed with Git's reason, got %#v", snapshot.Header.Warnings)
	}
}

// A cached entry built where history was unreadable carries the warning. Served where the history
// refresh succeeds, the edges are back and the warning must go; and a selective view derived from
// an entry that still lacks the edges must say so, as a build of the view would.
func TestSearchCacheCochangeWarningTracksTheHistoryRefresh(t *testing.T) {
	repo := t.TempDir()
	sharedCacheFixture(t, repo)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	cache := t.TempDir()
	originalPath := os.Getenv("PATH")

	stubFailingCochangeGit(t)
	built, _, err := PreindexProviderSnapshot(t.Context(), repo, "history-test", options, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(cochangeWarnings(built.Header.Warnings)) != 1 {
		t.Fatalf("premise: the entry is built with the warning, got %#v", built.Header.Warnings)
	}

	derived, err := selectiveSearchSnapshotFromFull(t.Context(), repo, "history-test",
		ProviderSnapshotOptions{Profile: ProfileFull, OnlyFiles: []string{"ledger/ledger.go", "ledger/audit.go"}}, built)
	if err != nil {
		t.Fatal(err)
	}
	if len(cochangeWarnings(derived.Header.Warnings)) != 1 {
		t.Fatalf("a view derived from an entry without co-change edges must disclose it, got %#v", derived.Header.Warnings)
	}

	t.Setenv("PATH", originalPath)
	commit, tree := built.Header.Commit, built.Header.Tree
	key, err := searchSnapshotKey(repo, built.Header.RepoKey, "history-test", tree, options, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newCacheEntry(cache, "search", searchSnapshotCacheVersion, key)
	if err != nil {
		t.Fatal(err)
	}
	served, hit := loadCachedSearchSnapshot(entry, repo, commit, built.Header.RepoKey, "history-test", tree, options, t.Context())
	if !hit {
		t.Fatal("premise: the entry written under the failing history must be stored and served")
	}
	if cochangeRelationCount(served.Snapshot) == 0 {
		t.Fatal("premise: the refresh must restore the co-change edges")
	}
	if warnings := cochangeWarnings(served.Snapshot.Header.Warnings); len(warnings) != 0 {
		t.Fatalf("refreshed history still reported unavailable: %#v", warnings)
	}
}

// The real failure, without a stub: history whose rename detection needs a blob the object store
// does not have (what a blob:none partial clone of a history with inexact renames looks like). HEAD
// itself is readable, so the snapshot builds; only the co-change read fails.
func TestSnapshotWarnsWhenHistoryNeedsAMissingBlob(t *testing.T) {
	repo, oldBlob := missingBlobHistoryRepo(t)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "history-test", ProviderSnapshotOptions{Profile: ProfileFull})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Header.Commit == "" {
		t.Fatalf("premise: HEAD must stay readable, warnings %#v", snapshot.Header.Warnings)
	}
	warnings := cochangeWarnings(snapshot.Header.Warnings)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Detail, oldBlob) {
		t.Fatalf("history that needs a missing blob must be disclosed, naming it; got %#v", snapshot.Header.Warnings)
	}
}

// missingBlobHistoryRepo commits history with an inexact rename, then removes the
// pre-rename blob from the object store. HEAD stays readable; `git log
// --name-only` fails with "unable to read <oid>". It returns the repository and
// that blob's id.
func missingBlobHistoryRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "core.autocrlf", "false")
	body := "package cc\n\nfunc A() int { return 1 }\n" + strings.Repeat("// filler line for rename similarity\n", 40)
	write(t, repo, "a.go", body)
	write(t, repo, "b.go", "package cc\n\nfunc B() int { return A() }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "initial")
	oldBlob := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD:a.go"))
	// An inexact rename: the content changes too, so Git must read the old blob to pair the paths.
	git(t, repo, "mv", "a.go", "renamed.go")
	write(t, repo, "renamed.go", body+"// changed\n")
	write(t, repo, "b.go", "package cc\n\nfunc B() int { return A() + 0 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "rename")
	write(t, repo, "b.go", "package cc\n\nfunc B() int { return A() + 1 }\n")
	git(t, repo, "commit", "-q", "-am", "edit")

	// Repack everything except the old blob, then drop the loose objects.
	var keep []string
	for _, line := range strings.Split(gitOutput(t, repo, "rev-list", "--objects", "--all"), "\n") {
		if oid, _, _ := strings.Cut(line, " "); oid != "" && oid != oldBlob {
			keep = append(keep, oid)
		}
	}
	gitInput(t, repo, strings.Join(keep, "\n")+"\n", "pack-objects", "-q", filepath.Join(".git", "objects", "pack", "pack"))
	objects := filepath.Join(repo, ".git", "objects")
	entries, err := os.ReadDir(objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) == 2 {
			if err := os.RemoveAll(filepath.Join(objects, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := exec.Command("git", "-C", repo, "cat-file", "-e", oldBlob).Run(); err == nil {
		t.Fatal("premise: the old blob must be missing")
	}
	return repo, oldBlob
}

// recordsCacheVersionsBeforeCochangeWarning are the records-cache versions that
// builds without W_COCHANGE_HISTORY_UNAVAILABLE wrote under: v21 on main before
// it, and v22 from #374, which lands first.
var recordsCacheVersionsBeforeCochangeWarning = []string{
	"provider-records-v21-" + IdentityRevision,
	"provider-records-v22-" + IdentityRevision,
}

// An older build keys a failed history read exactly as this one does (the
// unavailable scope and no pairs; the reason is not in the key), so its entry,
// whose summary has no warning, sits at this build's address unless the cache
// version moved. Replaying it would bring back the silent drop.
func TestRecordsCacheDoesNotReplayEntriesWrittenBeforeTheCochangeWarning(t *testing.T) {
	repo, _ := missingBlobHistoryRepo(t)
	commit, tree, err := resolveCommittedHEAD(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	const version, mode = "dev", "snapshot"

	// What an older build produced for this tree: the same records, and a summary
	// without the warning.
	stale := func(transaction *ProviderRecordsCacheTransaction) ([]byte, SnapshotSummary, SnapshotHeader) {
		t.Helper()
		var stream bytes.Buffer
		var summary SnapshotSummary
		encoder := json.NewEncoder(&stream)
		if err := StreamSnapshot(t.Context(), repo, version, transaction.Options(), func(record any) error {
			if typed, ok := record.(SnapshotSummary); ok {
				summary = typed
				return nil
			}
			return encoder.Encode(record)
		}); err != nil {
			t.Fatal(err)
		}
		if len(cochangeWarnings(summary.Warnings)) != 1 {
			t.Fatalf("premise: this build discloses the failed history read, got %#v", summary.Warnings)
		}
		summary.Warnings = withoutWarningCode(summary.Warnings, CochangeHistoryUnavailableCode)
		header := SnapshotHeader{
			SchemaVersion: SchemaVersion, Provider: ProviderName, ProviderVersion: version,
			RepoKey: transaction.repositoryKey, Commit: commit, Tree: tree, Profile: string(transaction.options.Profile),
		}
		return stream.Bytes(), summary, header
	}

	// Control: at this build's own address, that entry WOULD replay silently. The
	// cache version is the only thing keeping an older build's entry out.
	controlDir := t.TempDir()
	control, err := BeginProviderRecordsCache(t.Context(), repo, version, commit, tree, mode, controlDir, options)
	if err != nil {
		t.Fatal(err)
	}
	records, summary, header := stale(control)
	spool, err := spoolRecords(control, records)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Store(spool, &summary, header); err != nil {
		t.Fatal(err)
	}
	if _, replayed, hit := control.Load(); !hit || len(cochangeWarnings(replayed.Warnings)) != 0 {
		t.Fatalf("control: a warning-less entry at the current address must replay (hit=%v)", hit)
	}

	cacheDir := t.TempDir()
	current, err := BeginProviderRecordsCache(t.Context(), repo, version, commit, tree, mode, cacheDir, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, previous := range recordsCacheVersionsBeforeCochangeWarning {
		key, err := providerRecordsKeyForVersions(previous, SchemaVersion, current.absRepo, current.repositoryKey,
			version, commit, tree, mode, current.options, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		older := *current
		if older.entry, err = newCacheEntry(cacheDir, "records", previous, key); err != nil {
			t.Fatal(err)
		}
		if older.streamEntry, err = newCacheStreamEntry(cacheDir, "records", previous, key); err != nil {
			t.Fatal(err)
		}
		spool, err := spoolRecords(&older, records)
		if err != nil {
			t.Fatal(err)
		}
		if err := older.Store(spool, &summary, header); err != nil {
			t.Fatal(err)
		}
		rewriteCachedJSON(t, filepath.Join(older.entry.root, older.entry.relative), func(object map[string]any) {
			object["cache_version"] = previous
		})
	}
	if _, replayed, hit := current.Load(); hit {
		t.Fatalf("replayed an entry written before the co-change warning existed; warnings %#v", replayed.Warnings)
	}
	// On the miss the build runs, and the warning appears.
	var warnings []ProviderWarning
	if err := StreamSnapshot(t.Context(), repo, version, current.Options(), func(record any) error {
		if typed, ok := record.(SnapshotSummary); ok {
			warnings = typed.Warnings
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(cochangeWarnings(warnings)) != 1 {
		t.Fatalf("the rebuild after the miss must disclose the failed history read, got %#v", warnings)
	}
}

func gitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// searchCacheVersionsBeforeCochangeWarning are the search-cache versions that
// builds without W_COCHANGE_HISTORY_UNAVAILABLE wrote under: v26 on main before
// it, and v27 from #374, which lands first.
var searchCacheVersionsBeforeCochangeWarning = []string{
	"search-snapshot-v26-" + IdentityRevision,
	"search-snapshot-v27-" + IdentityRevision,
}

// `graph store` and `graph context` serve the persisted search entry and its
// sqlite sidecar without building, so unlike a query they never re-read history.
// An entry an older build persisted for a tree whose history is unreadable has no
// co-change warning in its header; this build must not hand it to them.
func TestStoreAndContextDoNotServeEntriesWrittenBeforeTheCochangeWarning(t *testing.T) {
	t.Setenv(searchStoreEnv, "1")
	repo, _ := missingBlobHistoryRepo(t)
	options := ProviderSnapshotOptions{Profile: ProfileFull}
	const version = "dev"

	// staleEntry is what an older build persisted: this tree's entry with no
	// co-change warning in its header, under cacheVersion.
	staleEntry := func(cacheDir string) (cachedSearchSnapshot, cacheEntry) {
		t.Helper()
		built, _, err := PreindexProviderSnapshot(t.Context(), repo, version, options, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cochangeWarnings(built.Header.Warnings)) != 1 {
			t.Fatalf("premise: this build discloses the failed history read, got %#v", built.Header.Warnings)
		}
		key, err := searchSnapshotKey(repo, built.Header.RepoKey, version, built.Header.Tree, options, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		entry, err := newCacheEntry(cacheDir, "search", searchSnapshotCacheVersion, key)
		if err != nil {
			t.Fatal(err)
		}
		cached, err := readSearchSnapshot(entry)
		if err != nil {
			t.Fatal(err)
		}
		cached.Snapshot.Header.Warnings = withoutWarningCode(cached.Snapshot.Header.Warnings, CochangeHistoryUnavailableCode)
		return cached, entry
	}
	writeEntryAndStore := func(entry cacheEntry, cached cachedSearchSnapshot) {
		t.Helper()
		if err := writeSearchSnapshot(entry, cached); err != nil {
			t.Fatal(err)
		}
		digest, err := entry.digest()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeSearchStoreSidecar(t.Context(), entry, digest.String(), &cached); err != nil {
			t.Fatal(err)
		}
	}
	headerCochangeWarnings := func(header json.RawMessage) int {
		t.Helper()
		var decoded SnapshotHeader
		if err := json.Unmarshal(header, &decoded); err != nil {
			t.Fatal(err)
		}
		return len(cochangeWarnings(decoded.Warnings))
	}

	// Control: at this build's own address, a warning-less entry IS served by
	// store and context. The cache version is the only thing keeping an older
	// build's entry out.
	controlDir := t.TempDir()
	stale, current := staleEntry(controlDir)
	writeEntryAndStore(current, stale)
	described, err := DescribeSearchStore(t.Context(), repo, version, controlDir, options)
	if err != nil || headerCochangeWarnings(described.Header) != 0 {
		t.Fatalf("control: a warning-less entry at the current address must be served (err=%v)", err)
	}

	cacheDir := t.TempDir()
	stale, current = staleEntry(cacheDir)
	// Only the older builds' entries remain: what a cache looks like on the first
	// run of this build.
	for _, path := range []string{current.relative, searchStoreEntryFor(current).relative} {
		if err := os.Remove(filepath.Join(current.root, path)); err != nil {
			t.Fatal(err)
		}
	}
	for _, previous := range searchCacheVersionsBeforeCochangeWarning {
		key, err := searchSnapshotKeyForVersions(previous, SchemaVersion, repo, stale.Snapshot.Header.RepoKey,
			version, stale.Snapshot.Header.Tree, options, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		older, err := newCacheEntry(cacheDir, "search", previous, key)
		if err != nil {
			t.Fatal(err)
		}
		previousCached := stale
		previousCached.CacheVersion = previous
		writeEntryAndStore(older, previousCached)
	}
	if served, err := DescribeSearchStore(t.Context(), repo, version, cacheDir, options); !errors.Is(err, ErrSearchStoreMissing) {
		t.Fatalf("graph store served an entry written before the co-change warning existed (err=%v, co-change warnings=%d)",
			err, headerCochangeWarnings(served.Header))
	}
	if _, err := LoadSearchStoreContext(t.Context(), repo, version, cacheDir, "B", options, 5, 5, 5); !errors.Is(err, ErrSearchStoreMissing) {
		t.Fatalf("graph context served an entry written before the co-change warning existed (err=%v)", err)
	}
	// The next build persists a fresh entry, and the store then carries the warning.
	if _, _, err := PreindexProviderSnapshot(t.Context(), repo, version, options, cacheDir); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := DescribeSearchStore(t.Context(), repo, version, cacheDir, options)
	if err != nil || headerCochangeWarnings(rebuilt.Header) != 1 {
		t.Fatalf("the rebuilt store must disclose the failed history read (err=%v)", err)
	}
}
