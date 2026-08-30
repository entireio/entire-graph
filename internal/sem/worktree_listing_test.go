package sem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// TestWorktreeSnapshotHonorsSafeGitExcludeSources is the regression guard for
// the working-tree listing: vendored trees excluded by nested .gitignore and
// .git/info/exclude must stay out, while a configuration-derived external
// core.excludesFile is neutralized at the no-egress subprocess boundary.
func TestWorktreeSnapshotHonorsSafeGitExcludeSources(t *testing.T) {
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

	for _, name := range []string{"keep_me", "handler", "brand_new", "generated"} {
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
	if !snapshotHasPath(snapshot, "app/thing.generated.py") {
		t.Fatalf("worktree snapshot honored configuration-derived core.excludesFile: %#v", snapshot.Files)
	}
	for _, name := range []string{"vendored_numeric", "vendored_mod", "cached", "secret"} {
		if snapshotHasSymbol(snapshot, name) {
			t.Fatalf("worktree snapshot included excluded symbol %q", name)
		}
	}
}

func TestWorktreeSnapshotIgnoresInheritedRepositoryOverrides(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "pkg/.gitignore", "secret.go\n")
	writeFile(t, repo, "pkg/keep.go", "package pkg\nfunc Keep() {}\n")
	writeFile(t, repo, "pkg/secret.go", "package pkg\nfunc AmbientOverrideSecret() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "requested repository")

	other := t.TempDir()
	initRepo(t, other)
	writeFile(t, other, "pkg/secret.go", "package pkg\nfunc OtherRepositorySecret() {}\n")
	git(t, other, "add", ".")
	git(t, other, "commit", "-m", "ambient override repository")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "Keep") {
		t.Fatalf("requested repository's tracked source is missing: %#v", snapshot.Files)
	}
	if snapshotHasPath(snapshot, "pkg/secret.go") || snapshotHasSymbol(snapshot, "AmbientOverrideSecret") {
		t.Fatalf("inherited Git repository overrides changed the requested corpus: %#v", snapshot.Files)
	}
}

func TestWorktreeSnapshotWarnsAndRetainsTrackedAmbiguousDirectoryWhenGitFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the selective Git wrapper is a POSIX shell script")
	}
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "build/keep.go", "package build\nfunc KeepOnFallback() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "tracked ambiguous directory")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "git")
	script := "#!/bin/sh\ncase \"$*\" in *\"ls-files\"*) exit 41;; esac\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PATH", binDir)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(snapshot, "KeepOnFallback") {
		t.Fatalf("fallback treated a potentially tracked ambiguous directory as untracked: %#v", snapshot.Files)
	}
	warned := false
	for _, warning := range snapshot.Header.Warnings {
		warned = warned || warning.Code == "W_GIT_WORKTREE_FALLBACK"
	}
	if !warned {
		t.Fatalf("warnings = %+v, want W_GIT_WORKTREE_FALLBACK", snapshot.Header.Warnings)
	}
}

// TestWorktreeIncludeFileReopensGitIgnoredTree covers the include-file escape
// hatch in a real git repository: because the listing now comes from Git, a path
// Git excludes has to be asked for explicitly, and an include file's negation is
// the one thing that may ask.
func TestWorktreeIncludeFileReopensGitIgnoredTree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, ".gitignore", "cache/\ncache/skip.py\n")
	writeFile(t, repo, ".graphinclude", "cache/\n")
	writeFile(t, repo, "app/keep.py", "def keep_me():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first-party source")
	writeFile(t, repo, "cache/include.py", "def include_me():\n    return True\n")
	writeFile(t, repo, "cache/skip.py", "def skip_me():\n    return True\n")

	withInclude, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree:     true,
		IncludeFiles: []string{".graphinclude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotHasSymbol(withInclude, "include_me") {
		t.Fatalf("include file did not reopen the git-ignored tree: %#v", withInclude.Files)
	}
	// The include file reopened the directory; a rule naming one file inside it
	// still wins.
	if snapshotHasSymbol(withInclude, "skip_me") {
		t.Fatalf("include file overrode a rule naming one file: %#v", withInclude.Files)
	}

	without, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
		Worktree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshotHasSymbol(without, "include_me") {
		t.Fatalf("git-ignored tree entered the listing without an include file: %#v", without.Files)
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

// TestSnapshotHonorsNestedGitignoreReinclusion is the regression guard for the
// vendored-directory heuristic's escape hatch. A project that keeps one of its own
// packages inside a vendored-looking tree says so in the ignore file beside that
// tree, which is where Git expects it — and the heuristic read only the
// repository-root .gitignore, so it saw no re-inclusion and silently dropped
// TRACKED first-party source from both `--head` and the working tree. Moving the
// identical negation to the root kept the same file, which is what made the
// root-only read the cause rather than the vendored name.
//
// Both directions matter: the re-included package must come back, and the rest of
// the vendored tree must stay out, or the fix is just a disabled heuristic.
func TestSnapshotHonorsNestedGitignoreReinclusion(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "app.py", "def app_entrypoint():\n    return True\n")
	writeFile(t, repo, "vendor/.gitignore", "*\n!.gitignore\n!mypkg/\n!mypkg/**\n")
	writeFile(t, repo, "vendor/mypkg/lib.py", "def vendored_first_party():\n    return True\n")
	writeFile(t, repo, "vendor/other/dep.py", "def real_dependency():\n    return True\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "keep one package inside vendor/")

	for _, worktree := range []bool{false, true} {
		mode := "head"
		if worktree {
			mode = "worktree"
		}
		t.Run(mode, func(t *testing.T) {
			snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{
				Worktree: worktree,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"app_entrypoint", "vendored_first_party"} {
				if !snapshotHasSymbol(snapshot, name) {
					t.Fatalf("%s snapshot dropped %q re-included by a nested .gitignore: %#v",
						mode, name, snapshot.Files)
				}
			}
			if snapshotHasSymbol(snapshot, "real_dependency") {
				t.Fatalf("%s snapshot included the vendored tree the project did not re-include: %#v",
					mode, snapshot.Files)
			}
		})
	}
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

func TestFilesystemWalkPropagatesNestedIgnoreLimitErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "nested/keep.go", "package sample\n")
	writeFile(t, repo, "nested/.gitignore", "#"+strings.Repeat("x", maxIgnoreRuleBytes))
	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = walkWorktreeFiles(t.Context(), repo, ignores, func(string) bool { return false }, nil)
	if err == nil || !strings.Contains(err.Error(), "nested ignore file") ||
		!strings.Contains(err.Error(), "rule line exceeds") {
		t.Fatalf("nested ignore limit error = %v, want propagated rule-line failure", err)
	}
}

func TestFilesystemWalkRejectsUnreadableNestedIgnoreInReadableDirectory(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "nested/.gitignore", "*.secret\n")
	writeFile(t, repo, "nested/keep.go", "package nested\n")
	unreadableFileOrSkip(t, filepath.Join(repo, "nested", ".gitignore"))
	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = walkWorktreeFiles(t.Context(), repo, ignores, func(string) bool { return false }, nil)
	if err == nil || !strings.Contains(err.Error(), "nested/.gitignore") {
		t.Fatalf("unreadable nested policy error = %v, want a hard error naming nested/.gitignore", err)
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
