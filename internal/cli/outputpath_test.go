package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These cover the two caller-named output files — `index --report` and
// `verify --record-baseline` — in BOTH directions, because the fix is a
// conditional rule and either half alone would be a regression:
//
//   - a path inside the scanned repository is repository-controlled, so a symlink
//     committed there must be refused rather than followed;
//   - a path outside it is caller-owned and documented (the verify help advertises
//     `--record-baseline /tmp/base.json`), so it must keep working, symlinked
//     parent directories included — /tmp is a symlink on macOS.

// symlinkRepo skips on Windows, where creating a symlink needs either Developer
// Mode or SeCreateSymbolicLinkPrivilege, so a Git checkout there materialises a
// committed symlink as a plain file containing the target's path. The input this
// test needs — a mode-120000 entry the write follows — is not representable.
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows; a committed symlink checks out as a regular file there")
	}
}

// outputPathRepo builds the smallest repository index has something to report on.
func outputPathRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func runIndexReport(t *testing.T, repo, report string) error {
	t.Helper()
	return Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo, PluginDataDir: t.TempDir()},
		Stdout:  &bytes.Buffer{},
	}, []string{"index", "--repo", repo, "--format", "json", "--report", report})
}

func runVerifyRecord(t *testing.T, repo, baseline string) error {
	t.Helper()
	return Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &bytes.Buffer{},
	}, []string{"verify", "--repo", repo, "--test", "true", "--record-baseline", baseline})
}

// plantVictim writes the file a hostile repository aims the write at, outside the
// repository, and returns its path and its exact bytes.
func plantVictim(t *testing.T) (string, string) {
	t.Helper()
	const content = "export PATH=/usr/bin\n# real user config\n"
	path := filepath.Join(t.TempDir(), "zshrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func assertVictimIntact(t *testing.T, victim, want string) {
	t.Helper()
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("the file the repository aimed at was rewritten:\n%s", got)
	}
}

// TestIndexReportRefusesRepositoryCommittedSymlink is the regression for the
// finding: a hostile clone commits the report path as a symlink, the documented
// invocation follows it, and the victim file is truncated and replaced with report
// bytes. The link itself must survive as a link, or the refusal would still have
// destroyed the repository's own entry.
func TestIndexReportRefusesRepositoryCommittedSymlink(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	victim, victimContent := plantVictim(t)
	report := filepath.Join(repo, "GRAPH_REPORT.md")
	if err := os.Symlink(victim, report); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile report path")

	err := runIndexReport(t, repo, report)
	if err == nil {
		t.Fatal("index --report followed a symlink committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	assertVictimIntact(t, victim, victimContent)
	info, err := os.Lstat(report)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the repository's symlink was replaced by a regular file")
	}
}

// TestIndexReportWritesOutsideTheRepository pins the other half: an out-of-repo
// path is caller-owned and keeps working, including through a symlinked parent
// directory, which is what /tmp is on macOS and what every /tmp example in the
// help would hit.
func TestIndexReportWritesOutsideTheRepository(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := runIndexReport(t, repo, filepath.Join(link, "GRAPH_REPORT.md")); err != nil {
		t.Fatalf("index --report to a caller-owned path failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(real, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "# Graph report") {
		t.Fatalf("report content is wrong:\n%s", content)
	}
}

// TestIndexReportWritesInsideTheRepository pins that confinement did not cost the
// ordinary in-repo report, which is the spelling the verb's own output suggests.
func TestIndexReportWritesInsideTheRepository(t *testing.T) {
	repo := outputPathRepo(t)
	report := filepath.Join(repo, "GRAPH_REPORT.md")
	if err := runIndexReport(t, repo, report); err != nil {
		t.Fatalf("index --report inside the repository failed: %v", err)
	}
	content, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "# Graph report") {
		t.Fatalf("report content is wrong:\n%s", content)
	}
}

// TestVerifyRecordBaselineRefusesRepositoryCommittedSymlink is the same finding on
// the other verb: --record-baseline writes JSON through whatever the repository
// left at the path.
func TestVerifyRecordBaselineRefusesRepositoryCommittedSymlink(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	victim, victimContent := plantVictim(t)
	baseline := filepath.Join(repo, "baseline.json")
	if err := os.Symlink(victim, baseline); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile baseline path")

	err := runVerifyRecord(t, repo, baseline)
	if err == nil {
		t.Fatal("verify --record-baseline followed a symlink committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	assertVictimIntact(t, victim, victimContent)
}

// TestVerifyRecordBaselineWritesToCallerOwnedPaths pins the documented example —
// `--record-baseline /tmp/base.json` — and the documented parent creation, on both
// sides of the boundary, since the confined path has its own MkdirAll.
func TestVerifyRecordBaselineWritesToCallerOwnedPaths(t *testing.T) {
	repo := outputPathRepo(t)
	outside := filepath.Join(t.TempDir(), "nested", "base.json")
	if err := runVerifyRecord(t, repo, outside); err != nil {
		t.Fatalf("verify --record-baseline outside the repository failed: %v", err)
	}
	inside := filepath.Join(repo, ".graph", "base.json")
	if err := runVerifyRecord(t, repo, inside); err != nil {
		t.Fatalf("verify --record-baseline inside the repository failed: %v", err)
	}
	for _, path := range []string{outside, inside} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), `"format_version": 1`) {
			t.Fatalf("baseline at %s is not the recorded JSON:\n%s", path, content)
		}
	}
}

// TestOutputPathClassificationHandlesSymlinkedRoots pins the two ways a purely
// lexical or purely resolved comparison gets the boundary wrong.
func TestOutputPathClassificationHandlesSymlinkedRoots(t *testing.T) {
	requireSymlinkSupport(t)

	// Resolved-only failure: a repository reached through a symlinked root — the
	// shape of a macOS TMPDIR under /var, or a repo under /tmp — would have its own
	// files resolve outside itself and lose confinement.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target, err := classifyOutputPath(real, filepath.Join(link, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !target.insideRepo() || target.rel != "GRAPH_REPORT.md" {
		t.Fatalf("a repository's own file through a symlinked root was classified outside it: %#v", target)
	}

	// Lexical-only failure: a committed symlinked DIRECTORY resolves outside the
	// repository, and treating that as caller-owned is the escape being closed.
	repo := t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(repo, "out")); err != nil {
		t.Fatal(err)
	}
	target, err = classifyOutputPath(repo, filepath.Join(repo, "out", "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !target.insideRepo() {
		t.Fatalf("a path under a committed symlinked directory was classified caller-owned: %#v", target)
	}
	// And the confined write refuses it rather than escaping through the directory.
	if err := writeOutputFile(repo, filepath.Join(repo, "out", "GRAPH_REPORT.md"), []byte("x"), 0o644, false); err == nil {
		t.Fatal("write escaped the repository through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(escape, "GRAPH_REPORT.md")); !os.IsNotExist(err) {
		t.Fatalf("write landed outside the repository: %v", err)
	}
}

// TestIndexReportEscapesNewlinesInTrackedFilenames covers the escalation on top of
// the symlink write: the report prints tracked filenames verbatim, and Git allows
// any byte but NUL and '/' in a pathname, so a repository that names a file with an
// embedded newline chooses a whole LINE of the file this tool writes.
func TestIndexReportEscapesNewlinesInTrackedFilenames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 forbids control characters in filenames, so this input cannot be checked out there")
	}
	repo := outputPathRepo(t)
	write(t, repo, "a\nexport EVIL=owned\nb.go", "package auth\n\nfunc Evil() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile filename")

	report := filepath.Join(t.TempDir(), "GRAPH_REPORT.md")
	if err := runIndexReport(t, repo, report); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "export EVIL=owned" {
			t.Fatalf("a filename's newline became a line of the report:\n%s", content)
		}
	}
	// Escaped, not dropped: the reader still has to be able to see which file it was.
	if !strings.Contains(string(content), `export EVIL=owned`) {
		t.Fatalf("the hostile filename was dropped instead of escaped:\n%s", content)
	}
}
