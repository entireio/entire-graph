package sem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// initRepo makes path a git repository with a deterministic identity.
func initRepo(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
}

// writeSparseFile creates a file of the requested apparent size without writing
// its bytes, so a test can exercise an oversize file without the disk cost.
func writeSparseFile(t *testing.T, repo, path string, size int64, header string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(header); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) (string, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	buf := make([]byte, 1<<16)
	lines := 0
	var last byte
	total := 0
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			_, _ = hasher.Write(buf[:n])
			lines += strings.Count(string(buf[:n]), "\n")
			last = buf[n-1]
			total += n
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
	}
	if total > 0 && last != '\n' {
		lines++
	}
	return hex.EncodeToString(hasher.Sum(nil)), lines
}

// TestWorktreeSnapshotHonorsEveryGitExcludeSource is the regression guard for the
// working-tree listing: a vendored dependency tree excluded by a NESTED
// .gitignore (plus .git/info/exclude and core.excludesFile) was listed, parsed
// and read into memory, while --head on the same repository listed only the
// tracked source. The graph must see the same files Git does.
func TestWorktreeSnapshotHonorsEveryGitExcludeSource(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, ".gitignore", ".pytest_cache/\n")
	writeFile(t, repo, "excludes-global", "*.generated.py\n")
	git(t, repo, "config", "core.excludesFile", filepath.Join(repo, "excludes-global"))
	writeFile(t, repo, ".git/info/exclude", "secrets/\n")

	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, "backend/app.py", "def handler():\n    return True\n")
	// The nested .gitignore is the whole point: nothing at the repository root
	// mentions this tree.
	writeFile(t, repo, "backend/.gitignore", ".venv/\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first-party source")

	writeFile(t, repo, "backend/.venv/lib/python3.13/site-packages/numpy/core/numeric.py",
		"def vendored_numeric():\n    return True\n")
	writeFile(t, repo, "backend/.venv/lib/python3.13/site-packages/pkg/mod.py",
		"def vendored_mod():\n    return True\n")
	writeFile(t, repo, ".pytest_cache/cache.py", "def cached():\n    return True\n")
	writeFile(t, repo, "secrets/token.py", "def secret():\n    return True\n")
	writeFile(t, repo, "app/thing.generated.py", "def generated():\n    return True\n")
	// Untracked and excluded by nothing: Git shows it, so the graph shows it.
	writeFile(t, repo, "app/new.py", "def brand_new():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"keep_me", "handler", "brand_new"} {
		if !snapshotHasSymbol(snapshot, name) {
			t.Fatalf("worktree snapshot missing first-party symbol %q: %#v", name, snapshot.Symbols)
		}
	}
	for _, prefix := range []string{
		"backend/.venv/",
		".pytest_cache/",
		"secrets/",
	} {
		assertSnapshotOmitsPathPrefix(t, snapshot, prefix)
	}
	if snapshotHasPath(snapshot, "app/thing.generated.py") || snapshotHasSymbol(snapshot, "generated") {
		t.Fatalf("worktree snapshot included a path excluded by core.excludesFile: %#v", snapshot.Files)
	}
	for _, name := range []string{"vendored_numeric", "vendored_mod", "cached", "secret"} {
		if snapshotHasSymbol(snapshot, name) {
			t.Fatalf("worktree snapshot included excluded symbol %q", name)
		}
	}
}

// TestWorktreeSnapshotFileCeilingOnVendoredTree is the file-count ceiling: a
// vendored tree an order of magnitude larger than the source must not enlarge the
// graph at all. Under the root-.gitignore-only listing every one of these files
// was indexed.
func TestWorktreeSnapshotFileCeilingOnVendoredTree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, "backend/.gitignore", "node_env/\n")
	writeFile(t, repo, "backend/app.py", "def handler():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first-party source")

	const vendored = 300
	for i := 0; i < vendored; i++ {
		writeFile(t, repo, fmt.Sprintf("backend/node_env/pkg_%d/mod.py", i),
			fmt.Sprintf("def vendored_%d():\n    return True\n", i))
	}

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Three tracked files (.gitignore counts as one) and nothing from the vendored
	// tree: a ceiling low enough that the old behaviour (303 files) cannot pass.
	if len(snapshot.Files) > 8 {
		t.Fatalf("worktree snapshot listed %d files, want the handful of tracked files: %#v",
			len(snapshot.Files), snapshot.Files)
	}
	assertSnapshotOmitsPathPrefix(t, snapshot, "backend/node_env/")
}

// TestWorktreeWalkFallbackHonorsNestedGitignore covers the non-git fallback: a
// directory Git cannot enumerate is still filtered by the nested ignore files the
// project wrote.
func TestWorktreeWalkFallbackHonorsNestedGitignore(t *testing.T) {
	// The expectations below are exactly what `git ls-files --others
	// --exclude-standard` reports for this tree: a negation cannot re-include a
	// path inside an excluded directory, but it can re-include a path excluded by
	// a pattern.
	repo := t.TempDir()
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, "backend/.gitignore", "cache/\n!cache/keep_this.py\ngenerated_*.py\n!generated_keep.py\n")
	writeFile(t, repo, "backend/app.py", "def handler():\n    return True\n")
	writeFile(t, repo, "backend/cache/drop.py", "def dropped():\n    return True\n")
	writeFile(t, repo, "backend/cache/keep_this.py", "def not_reincludable():\n    return True\n")
	writeFile(t, repo, "backend/generated_drop.py", "def generated_dropped():\n    return True\n")
	writeFile(t, repo, "backend/generated_keep.py", "def kept_by_negation():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep_me", "handler", "kept_by_negation"} {
		if !snapshotHasSymbol(snapshot, name) {
			t.Fatalf("fallback walk dropped %q: %#v", name, snapshot.Files)
		}
	}
	for _, name := range []string{"dropped", "not_reincludable", "generated_dropped"} {
		if snapshotHasSymbol(snapshot, name) {
			t.Fatalf("fallback walk ignored a nested .gitignore rule for %q: %#v", name, snapshot.Files)
		}
	}
}

// TestWorktreeSnapshotNeverMaterializesOversizeFile is the memory ceiling. The
// file is tracked, so no exclude rule can save the reader: the cap must. Before
// the cap this file cost its size twice (os.ReadFile plus the string conversion)
// on every read, and preselection read every file in the tree with eight
// goroutines — which is how one repository reached tens of gigabytes of RSS.
func TestWorktreeSnapshotNeverMaterializesOversizeFile(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	const oversize = 48 << 20 // 12x the 4 MiB parse cap
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeSparseFile(t, repo, "app/generated_huge.py", oversize, "def generated_huge():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "track an oversize generated file")

	wantHash, wantLines := fileSHA256(t, filepath.Join(repo, "app/generated_huge.py"))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	// One full materialization of this file would allocate at least 48 MiB, and
	// the pre-fix path did it twice per read. A ceiling of 32 MiB cannot be met
	// by any implementation that holds the file even once.
	const ceiling = 32 << 20
	if allocated > ceiling {
		t.Fatalf("snapshot allocated %d bytes for a %d-byte oversize file, want under %d",
			allocated, oversize, ceiling)
	}

	var record *FileRecord
	for i := range snapshot.Files {
		if snapshot.Files[i].Path == "app/generated_huge.py" {
			record = &snapshot.Files[i]
		}
	}
	if record == nil {
		t.Fatalf("oversize file has no file record: %#v", snapshot.Files)
	}
	if record.Bytes != oversize {
		t.Fatalf("oversize file record bytes = %d, want %d", record.Bytes, oversize)
	}
	if record.Blob != wantHash {
		t.Fatalf("oversize file record blob = %q, want the streamed content hash %q", record.Blob, wantHash)
	}
	if record.Lines != wantLines {
		t.Fatalf("oversize file record lines = %d, want %d", record.Lines, wantLines)
	}
	if snapshotHasSymbol(snapshot, "generated_huge") {
		t.Fatalf("oversize file was parsed for symbols: %#v", snapshot.Symbols)
	}
	found := false
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.FilePath == "app/generated_huge.py" && failure.Code == "E_FILE_TOO_LARGE" {
			found = true
			if !strings.Contains(failure.Detail, "never held in memory") {
				t.Fatalf("E_FILE_TOO_LARGE detail does not report the refused read: %q", failure.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("oversize file has no E_FILE_TOO_LARGE partial failure: %#v", snapshot.Header.PartialFailures)
	}
}

// TestHeadSnapshotNeverMaterializesOversizeBlob is the same ceiling for the
// committed-tree reader, which pulls content through `git cat-file --batch`.
func TestHeadSnapshotNeverMaterializesOversizeBlob(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	const oversize = 48 << 20
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeSparseFile(t, repo, "app/generated_huge.py", oversize, "def generated_huge():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "track an oversize generated file")

	wantHash, wantLines := fileSHA256(t, filepath.Join(repo, "app/generated_huge.py"))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	// Materializing the blob once costs 48 MiB, and the string conversion costs it
	// again; no implementation that holds it can come in under this ceiling.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("committed-tree snapshot allocated %d bytes for a %d-byte blob, want under %d",
			allocated, oversize, 32<<20)
	}
	var record *FileRecord
	for i := range snapshot.Files {
		if snapshot.Files[i].Path == "app/generated_huge.py" {
			record = &snapshot.Files[i]
		}
	}
	if record == nil {
		t.Fatalf("oversize blob has no file record: %#v", snapshot.Files)
	}
	if record.Bytes != oversize || record.Blob != wantHash || record.Lines != wantLines {
		t.Fatalf("oversize blob record = %#v, want bytes=%d blob=%s lines=%d",
			*record, oversize, wantHash, wantLines)
	}
	if snapshotHasSymbol(snapshot, "generated_huge") {
		t.Fatalf("oversize blob was parsed for symbols: %#v", snapshot.Symbols)
	}
}

// TestSourceListingFileLimitIsReportedNotSilent guards the listing ceiling: it
// truncates deterministically and says so, because a partial graph that claims to
// be complete is worse than a loud one.
func TestSourceListingFileLimitIsReportedNotSilent(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	for i := 0; i < 12; i++ {
		writeFile(t, repo, fmt.Sprintf("app/mod_%02d.py", i), fmt.Sprintf("def fn_%02d():\n    return True\n", i))
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "twelve modules")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
		MaxFiles: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 4 {
		t.Fatalf("listing cap produced %d files, want 4: %#v", len(snapshot.Files), snapshot.Files)
	}
	found := false
	for _, warning := range snapshot.Header.Warnings {
		if warning.Code == "W_FILE_LIMIT" {
			found = true
			if !strings.Contains(warning.Detail, "12 files") || !strings.Contains(warning.Detail, maxSourceFilesEnv) {
				t.Fatalf("W_FILE_LIMIT detail must name the count and the override: %q", warning.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("truncated listing emitted no W_FILE_LIMIT warning: %#v", snapshot.Header.Warnings)
	}
}

// TestWorktreeListingSkipsInstalledDependencyDirNames covers the defence in depth
// for a tree whose exclude rules are missing entirely (an untracked clone, a
// stray checkout): these directory names never denote first-party source.
func TestWorktreeListingSkipsInstalledDependencyDirNames(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, ".venv/lib/python3.13/site-packages/pkg/mod.py", "def vendored():\n    return True\n")
	writeFile(t, repo, "backend/.tox/py311/lib/mod.py", "def tox_vendored():\n    return True\n")
	writeFile(t, repo, ".entire/metadata/session-1.py", "def transcript():\n    return True\n")

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "keep_me") {
		t.Fatalf("listing dropped first-party source: %#v", snapshot.Symbols)
	}
	for _, name := range []string{"vendored", "tox_vendored", "transcript"} {
		if snapshotHasSymbol(snapshot, name) {
			t.Fatalf("listing included dependency/state tree symbol %q: %#v", name, snapshot.Files)
		}
	}
}

// TestTrackedToolCacheFixturesAreKept is the other side of that heuristic: a
// project that COMMITS such a tree as test data means it. Terraform commits
// `.terraform/modules` fixtures, and dropping them would silently shrink the
// graph of a first-party test corpus (and diverge from what --head sees).
func TestTrackedToolCacheFixturesAreKept(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	writeFile(t, repo, "testdata/example/.terraform/modules/child/mod.py",
		"def committed_fixture():\n    return True\n")
	git(t, repo, "add", "-f", ".")
	git(t, repo, "commit", "-m", "commit a tool-cache-shaped fixture")

	// Untracked, same directory name: still skipped.
	writeFile(t, repo, "scratch/.terraform/modules/other/mod.py",
		"def untracked_cache():\n    return True\n")

	for _, worktree := range []bool{true, false} {
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
			Worktree: worktree,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !snapshotHasSymbol(snapshot, "committed_fixture") {
			t.Fatalf("worktree=%v dropped a tracked tool-cache fixture: %#v", worktree, snapshot.Files)
		}
		if worktree && snapshotHasSymbol(snapshot, "untracked_cache") {
			t.Fatalf("worktree listing kept an untracked tool-cache tree: %#v", snapshot.Files)
		}
	}
}
