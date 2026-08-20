package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
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
	return outputPathRepoAt(t, t.TempDir())
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
	if err := writeOutputFile(t.Context(), repo, filepath.Join(repo, "out", "GRAPH_REPORT.md"), []byte("x"), 0o644, false); err == nil {
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

// outputPathRepoAt is outputPathRepo with the location chosen by the caller, for
// the tests that need the repository's own directory NAME to be part of the
// fixture (a subdirectory to point --repo at, a mixed-case root to re-spell).
func outputPathRepoAt(t *testing.T, repo string) string {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.go", "package auth\n\nfunc ValidateToken(token string) bool { return token != \"\" }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// runIndexReportWithRepoFlag is runIndexReport with --repo spelled separately
// from the repository the report path belongs to. Both findings below are about
// exactly that gap: the value of --repo is not the boundary.
func runIndexReportWithRepoFlag(t *testing.T, repoFlag, report string) error {
	t.Helper()
	return Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{PluginDataDir: t.TempDir()},
		Stdout:  &bytes.Buffer{},
	}, []string{"index", "--repo", repoFlag, "--format", "json", "--report", report})
}

// TestIndexReportRefusesSymlinkWhenRepoNamesASubdirectory is the regression for
// the first review finding. --repo may name a SUBDIRECTORY of the checkout, and
// using that subdirectory as the confinement root calls the checkout's own
// root-level files caller-owned. They are not: they came out of the same hostile
// clone. The whole checkout is on disk either way, and `git log --name-only`
// (gitutil/git.go:440, which backs co-change) reports paths across the entire
// top-level repository even when git runs from the subdirectory.
func TestIndexReportRefusesSymlinkWhenRepoNamesASubdirectory(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	write(t, repo, filepath.Join("subdir", "sub.go"), "package sub\n\nfunc Helper() {}\n")
	victim, victimContent := plantVictim(t)
	report := filepath.Join(repo, "GRAPH_REPORT.md")
	if err := os.Symlink(victim, report); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile report path at the repository root")

	err := runIndexReportWithRepoFlag(t, filepath.Join(repo, "subdir"), report)
	if err == nil {
		t.Fatal("index --repo <subdir> --report <repo-root path> followed a symlink committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	assertVictimIntact(t, victim, victimContent)
}

// caseVariantRepo builds a repository whose directory name has upper-case letters
// and returns it together with an all-lower-case spelling of the same directory.
// It skips when the spelling does not reach the same directory, which is the case
// on an ordinary Linux filesystem and is the whole reason this hazard is
// platform-shaped: macOS and Windows are case-insensitive by default.
func caseVariantRepo(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "CaseRepo")
	variant := filepath.Join(parent, "caserepo")
	outputPathRepoAt(t, repo)
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	variantInfo, err := os.Stat(variant)
	if err != nil || !os.SameFile(repoInfo, variantInfo) {
		t.Skip("filesystem is case-sensitive, so a re-spelled root is a different directory here")
	}
	return repo, variant
}

// TestOutputPathClassificationFollowsTheFilesystemsCaseRules is the regression for
// the second review finding, and it is the one that also runs on Windows, where
// the default filesystem is case-insensitive too. filepath.Rel compares bytes, and
// filepath.EvalSymlinks does not canonicalise the case of ordinary components, so
// a re-spelled root used to fall through as caller-owned.
func TestOutputPathClassificationFollowsTheFilesystemsCaseRules(t *testing.T) {
	repo, variant := caseVariantRepo(t)
	target, err := classifyOutputPath(repo, filepath.Join(variant, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !target.insideRepo() || target.rel != "GRAPH_REPORT.md" {
		t.Fatalf("a repository file spelled with different case was classified caller-owned: %#v", target)
	}
}

// TestIndexReportRefusesSymlinkThroughACaseVariantRoot drives the same finding
// through the command, so the claim is about the destroyed file and not only
// about the classifier.
func TestIndexReportRefusesSymlinkThroughACaseVariantRoot(t *testing.T) {
	requireSymlinkSupport(t)
	repo, variant := caseVariantRepo(t)
	victim, victimContent := plantVictim(t)
	if err := os.Symlink(victim, filepath.Join(repo, "GRAPH_REPORT.md")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile report path")

	err := runIndexReportWithRepoFlag(t, repo, filepath.Join(variant, "GRAPH_REPORT.md"))
	if err == nil {
		t.Fatal("index --report through a case-variant spelling of the root followed a committed symlink")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	assertVictimIntact(t, victim, victimContent)
}

// TestConfinementRootResolvesTheGitTopLevel pins the boundary itself rather than
// one escape through it: --repo pointed at a subdirectory still confines against
// the checkout. os.SameFile rather than string equality because git reports the
// on-disk path — /private/var/... for a macOS TMPDIR under /var.
func TestConfinementRootResolvesTheGitTopLevel(t *testing.T) {
	repo := outputPathRepo(t)
	write(t, repo, filepath.Join("subdir", "sub.go"), "package sub\n\nfunc Helper() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subdirectory")

	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(confinementRoot(t.Context(), filepath.Join(repo, "subdir")))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(repoInfo, rootInfo) {
		t.Fatalf("confinement root for a subdirectory is not the checkout: %v", rootInfo.Name())
	}
}

// TestConfinementRootFallsBackOutsideAGitRepository is the other half of that
// resolution, and it is a REQUIREMENT, not a defensive branch: verify runs the
// caller's test command in whatever directory they name, which need not be a git
// repository, so the fallback keeps the verb working there instead of erroring.
func TestConfinementRootFallsBackOutsideAGitRepository(t *testing.T) {
	plain := t.TempDir()
	// The premise, established independently of the code under test: the temporary
	// directory is not inside anyone's checkout. If a runner ever puts TMPDIR under
	// one, this test has nothing to say rather than something wrong.
	probe := exec.Command("git", "rev-parse", "--show-toplevel")
	probe.Dir = plain
	if out, err := probe.CombinedOutput(); err == nil {
		t.Skipf("temporary directory is inside a git repository (%s)", strings.TrimSpace(string(out)))
	}
	if got := confinementRoot(t.Context(), plain); got != plain {
		t.Fatalf("confinement root outside a git repository = %q, want %q", got, plain)
	}
}

// TestVerifyRecordBaselineOutsideAGitRepository drives that requirement through
// the verb: a non-git working directory records a baseline to a caller-owned path
// and to one beneath the directory itself.
func TestVerifyRecordBaselineOutsideAGitRepository(t *testing.T) {
	plain := t.TempDir()
	outside := filepath.Join(t.TempDir(), "base.json")
	if err := runVerifyRecord(t, plain, outside); err != nil {
		t.Fatalf("verify --record-baseline outside a git repository failed: %v", err)
	}
	inside := filepath.Join(plain, "nested", "base.json")
	if err := runVerifyRecord(t, plain, inside); err != nil {
		t.Fatalf("verify --record-baseline beneath a non-git --repo failed: %v", err)
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

// TestOutputPathClassificationFollowsALinkIntoTheRepository pins the chain the
// named walk alone would miss: a caller-owned link that points at a SUBDIRECTORY
// of the repository. Every ancestor of the named path is outside the checkout,
// but the file it lands on is repository-controlled, so a symlink committed there
// still has to be refused.
func TestOutputPathClassificationFollowsALinkIntoTheRepository(t *testing.T) {
	requireSymlinkSupport(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "into-repo")
	if err := os.Symlink(filepath.Join(repo, "sub"), link); err != nil {
		t.Fatal(err)
	}
	target, err := classifyOutputPath(repo, filepath.Join(link, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !target.insideRepo() || target.rel != filepath.Join("sub", "GRAPH_REPORT.md") {
		t.Fatalf("a link into the repository was classified caller-owned: %#v", target)
	}

	victim, victimContent := plantVictim(t)
	if err := os.Symlink(victim, filepath.Join(repo, "sub", "GRAPH_REPORT.md")); err != nil {
		t.Fatal(err)
	}
	err = writeOutputFile(t.Context(), repo, filepath.Join(link, "GRAPH_REPORT.md"), []byte("x"), 0o644, false)
	if err == nil {
		t.Fatal("write followed a symlink committed inside the repository")
	}
	assertVictimIntact(t, victim, victimContent)
}

// dotDotPath spells `<first>/../<rest...>` by CONCATENATION rather than
// filepath.Join, because Join cleans and the ".." is precisely what cleaning
// removes. Every test below needs the uncleaned spelling to survive as far as the
// code under test.
func dotDotPath(first string, rest ...string) string {
	sep := string(filepath.Separator)
	return first + sep + ".." + sep + strings.Join(rest, sep)
}

// TestOutputPathsResolveDotDotThroughSymlinksLikeTheKernel is the regression for
// the review finding on classifyOutputPath.
//
// filepath.Abs CLEANS: it collapses ".." textually, before any symlink is
// traversed. The `os.WriteFile(path, …)` this fix replaced did not — it handed the
// path to the KERNEL, where ".." steps out of the link's TARGET. With
// `link -> real/sub`, `link/../report.md` names `real/report.md` on disk and
// `report.md` beside the link after cleaning. Those are two different files, so
// classifying and writing the cleaned spelling silently moves the caller's output
// and, in the second subtest, hands a repository-controlled path back to the
// unconfined write.
func TestOutputPathsResolveDotDotThroughSymlinksLikeTheKernel(t *testing.T) {
	requireSymlinkSupport(t)

	// linkOver creates <home>/real/<sub> and a <home>/link symlink onto it, so
	// that `<home>/link/..` is `<home>/real` to the kernel and `<home>` to Clean.
	linkOver := func(t *testing.T, home, sub string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(home, "real", sub), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, "link")
		if err := os.Symlink(filepath.Join(home, "real", sub), link); err != nil {
			t.Fatal(err)
		}
		return link
	}

	// The destination half: an unconfined write must land where the caller's own
	// path resolves on disk, which is where os.WriteFile put it before this fix.
	t.Run("report lands where the path resolves on disk", func(t *testing.T) {
		repo := outputPathRepo(t)
		home := t.TempDir()
		link := linkOver(t, home, "sub")
		if err := runIndexReport(t, repo, dotDotPath(link, "report.md")); err != nil {
			t.Fatal(err)
		}
		onDisk := filepath.Join(home, "real", "report.md")
		if _, err := os.Stat(onDisk); err != nil {
			t.Errorf("report did not land where the path resolves on disk (%s): %v", onDisk, err)
		}
		if _, err := os.Stat(filepath.Join(home, "report.md")); err == nil {
			t.Error("report landed beside the link, at the path filepath.Clean invented")
		}
	})

	// `verify --record-baseline` is documented to CREATE its parents, so the ".."
	// there is followed by a directory that does not exist yet and the mkdir has to
	// agree with the write. Before this fix the two disagreed even on main, which
	// created the parent beside the link and then failed to open the file inside the
	// link's target.
	t.Run("record-baseline creates its parent where the path resolves", func(t *testing.T) {
		repo := outputPathRepo(t)
		home := t.TempDir()
		link := linkOver(t, home, "sub")
		if err := runVerifyRecord(t, repo, dotDotPath(link, "created", "base.json")); err != nil {
			t.Fatal(err)
		}
		onDisk := filepath.Join(home, "real", "created", "base.json")
		if _, err := os.Stat(onDisk); err != nil {
			t.Errorf("baseline did not land where the path resolves on disk (%s): %v", onDisk, err)
		}
		if _, err := os.Stat(filepath.Join(home, "created")); err == nil {
			t.Error("baseline created its parent beside the link, at the path filepath.Clean invented")
		}
	})

	// The confinement half, and the reason this is not merely cosmetic. A
	// caller-owned link whose ".." step lands back INSIDE the checkout names a
	// repository-controlled file. TestOutputPathClassificationFollowsALinkIntoThe
	// Repository already pins that for the plain spelling; cleaning ".." first
	// throws the same path back to the unconfined write, so a symlink committed at
	// the destination would be followed again.
	t.Run("a dot-dot step back into the repository stays confined", func(t *testing.T) {
		repo := outputPathRepo(t)
		if err := os.MkdirAll(filepath.Join(repo, "sub", "deeper"), 0o755); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		link := filepath.Join(home, "into-repo")
		if err := os.Symlink(filepath.Join(repo, "sub", "deeper"), link); err != nil {
			t.Fatal(err)
		}
		given := dotDotPath(link, "GRAPH_REPORT.md")

		target, err := classifyOutputPath(repo, given)
		if err != nil {
			t.Fatal(err)
		}
		if !target.insideRepo() || target.rel != filepath.Join("sub", "GRAPH_REPORT.md") {
			t.Fatalf("a dot-dot step back into the repository was classified caller-owned: %#v", target)
		}

		victim, victimContent := plantVictim(t)
		if err := os.Symlink(victim, filepath.Join(repo, "sub", "GRAPH_REPORT.md")); err != nil {
			t.Fatal(err)
		}
		if err := writeOutputFile(t.Context(), repo, given, []byte("x"), 0o644, false); err == nil {
			t.Error("write followed a symlink committed inside the repository")
		}
		assertVictimIntact(t, victim, victimContent)
	})
}

// TestHasDotDotMatchesWholeComponentsOnly and TestSplitFinalComponentDoesNotClean
// cover the two helpers realOutputPath is built from on EVERY platform, including
// Windows, where the symlink tests above skip. Both are spelled with the platform
// separator so that the Windows backslash is exercised there.
func TestHasDotDotMatchesWholeComponentsOnly(t *testing.T) {
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"", false},
		{"report.md", false},
		{"..", true},
		{"...", false},
		{"..report.md", false},
		{"a" + sep + ".." + sep + "b", true},
		{"a" + sep + "..", true},
		{".." + sep + "reports" + sep + "x.md", true},
		{"a" + sep + "..b" + sep + "c", false},
		{"a" + sep + "b.." + sep + "c", false},
		{"a" + sep + sep + ".." + sep + "b", true},
	} {
		if got := hasDotDot(tc.path); got != tc.want {
			t.Errorf("hasDotDot(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestSplitFinalComponentDoesNotClean(t *testing.T) {
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		path, parent, last string
	}{
		{"", "", ""},
		{sep, "", ""},
		{sep + sep, "", ""},
		{"report.md", "", "report.md"},
		{sep + "x", sep, "x"},
		{"a" + sep + ".." + sep + "b", "a" + sep + ".." + sep, "b"},
		// The trailing separator is ignored rather than treated as an empty
		// component, and the ".." SURVIVES in the parent — filepath.Dir would have
		// collapsed it, which is the whole reason this helper exists.
		{"a" + sep + ".." + sep + "b" + sep, "a" + sep + ".." + sep, "b"},
		{"a" + sep + "b" + sep + "..", "a" + sep + "b" + sep, ".."},
	} {
		parent, last := splitFinalComponent(tc.path)
		if parent != tc.parent || last != tc.last {
			t.Errorf("splitFinalComponent(%q) = (%q, %q), want (%q, %q)",
				tc.path, parent, last, tc.parent, tc.last)
		}
	}
}

// TestRealOutputPathWithoutDotDotIsPlainAbs pins the fast path: with no ".."
// component there is nothing for filepath.Clean to move across a symlink, so
// realOutputPath must not resolve anything and must agree with filepath.Abs. That
// keeps classification for the overwhelmingly common invocation byte-identical to
// what it was before ".." handling was added.
func TestRealOutputPathWithoutDotDotIsPlainAbs(t *testing.T) {
	for _, path := range []string{
		"GRAPH_REPORT.md",
		filepath.Join("reports", "x.md"),
		filepath.Join(t.TempDir(), "sub", "base.json"),
	} {
		want, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := realOutputPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("realOutputPath(%q) = %q, want filepath.Abs = %q", path, got, want)
		}
	}
}

// TestOutputPathsRefuseDotDotThroughAMissingDirectory is the regression for the
// review finding on resolveExistingPrefix.
//
// The kernel resolves ".." against the directory it FOLLOWS, so `missing/../x`
// fails at `missing` with ENOENT and never reaches `x`. resolveExistingPrefix
// peeled the not-yet-existing tail back onto the resolved prefix with
// filepath.Join, which CLEANS — so `missing/..` collapsed to nothing and the
// destination silently became `x` beside it. That turned the previous ENOENT into a
// truncating write of a DIFFERENT existing file, for both `--report` and
// `--record-baseline`.
//
// The refusal belongs to the platforms that TRAVERSE the missing component.
// Windows removes `missing\..` while normalising the path in user mode, so the
// spelling names the file beside it there and writing it is what the caller asked
// for; TestRealOutputPathFollowsThePlatformsDotDotRule pins both rules on one
// machine.
func TestOutputPathsRefuseDotDotThroughAMissingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows collapses `missing\\..` before the filesystem sees it, so this spelling names an existing file there")
	}
	// Outside the repository: the caller-owned write path. The bystander is an
	// ordinary file the caller never named.
	t.Run("report does not truncate a bystander outside the repository", func(t *testing.T) {
		repo := outputPathRepo(t)
		victim, victimContent := plantVictim(t)
		given := dotDotPath(filepath.Join(filepath.Dir(victim), "missing"), filepath.Base(victim))

		err := runIndexReport(t, repo, given)
		if err == nil {
			t.Error("write through a missing directory succeeded; the kernel would have returned ENOENT")
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error is not a not-exist error: %v", err)
		}
		assertVictimIntact(t, victim, victimContent)
	})

	// Inside the repository: the confined write path, which reaches the same
	// resolution helper. The bystander here is a TRACKED source file.
	t.Run("record-baseline does not truncate a tracked file inside the repository", func(t *testing.T) {
		repo := outputPathRepo(t)
		tracked := filepath.Join(repo, "auth.go")
		before, err := os.ReadFile(tracked)
		if err != nil {
			t.Fatal(err)
		}
		given := dotDotPath(filepath.Join(repo, "missing"), "auth.go")

		if err := runVerifyRecord(t, repo, given); err == nil {
			t.Error("write through a missing directory succeeded; the kernel would have returned ENOENT")
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error is not a not-exist error: %v", err)
		}
		assertVictimIntact(t, tracked, string(before))
	})

	// The helper itself, so the reason is pinned where it lives rather than only
	// through two commands.
	t.Run("realOutputPath reports the missing component", func(t *testing.T) {
		home := t.TempDir()
		if _, err := realOutputPath(dotDotPath(filepath.Join(home, "missing"), "x.md")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("realOutputPath through a missing directory: err = %v, want a not-exist error", err)
		}
		// A ".." after a directory that DOES exist is ordinary and must still
		// resolve — including when the leaf directories do not exist yet, which is
		// the documented `--record-baseline` case.
		if err := os.MkdirAll(filepath.Join(home, "present"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := realOutputPath(dotDotPath(filepath.Join(home, "present"), "created", "base.json"))
		if err != nil {
			t.Fatal(err)
		}
		// Against the RESOLVED home: t.TempDir sits under a symlinked /var on macOS,
		// and resolving `present` is what this helper is for.
		resolvedHome, err := filepath.EvalSymlinks(home)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(resolvedHome, "created", "base.json"); got != want {
			t.Errorf("realOutputPath = %q, want %q", got, want)
		}
	})
}

// trailingSpaceCheckout builds the same checkout in a directory whose name ends
// in a SPACE. A trailing space is an ordinary byte in a POSIX path component, so
// `git clone` into `~/work/checkout ` is a checkout like any other; Win32 strips
// trailing spaces from path components while normalising, so the name is not
// representable there and this input only exists on Unix.
func trailingSpaceCheckout(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Win32 strips a trailing space from every path component, so this directory name cannot exist there")
	}
	return outputPathRepoAt(t, filepath.Join(t.TempDir(), "checkout "))
}

// TestConfinementRootPreservesACheckoutNamedWithATrailingSpace pins the boundary
// for that checkout.
//
// `git rev-parse --show-toplevel` prints the path and a newline. Trimming ALL
// trailing whitespace takes the path's own last byte with the terminator, so the
// boundary names `…/checkout` — a directory that is not the checkout, and usually
// is not on disk at all. Every path in the real checkout then classifies as
// outside it.
func TestConfinementRootPreservesACheckoutNamedWithATrailingSpace(t *testing.T) {
	repo := trailingSpaceCheckout(t)
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	root := confinementRoot(t.Context(), repo)
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("confinement root %q is not on disk: %v", root, err)
	}
	if !os.SameFile(repoInfo, rootInfo) {
		t.Fatalf("confinement root = %q, which is not the checkout %q", root, repo)
	}
}

// TestIndexReportRefusesSymlinkInACheckoutNamedWithATrailingSpace drives the same
// defect through the verb it re-opens: with the boundary naming a different
// directory, the in-repo report path is classified caller-owned and written with
// os.WriteFile, which follows the committed symlink and truncates the victim —
// byte for byte the finding this PR exists to close.
func TestIndexReportRefusesSymlinkInACheckoutNamedWithATrailingSpace(t *testing.T) {
	requireSymlinkSupport(t)
	repo := trailingSpaceCheckout(t)
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

// TestRealOutputPathFollowsThePlatformsDotDotRule pins the ".." refusal to the
// platforms that actually traverse the missing component.
//
// A Unix kernel resolves each component in turn, so `missing/../x` fails at
// `missing` with ENOENT and never reaches `x`. Windows does not: it normalises the
// path in USER MODE before the filesystem sees it, removing `missing\..` the way
// filepath.Clean does, so the same spelling names `x` beside it and OPENS it. The
// refusal is therefore correct on Unix and a regression on Windows, where a
// `--report` or `--record-baseline` path that wrote its file before would now
// return a not-exist error.
//
// Both halves run on one machine because the platform rule is a parameter.
func TestRealOutputPathFollowsThePlatformsDotDotRule(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	given := dotDotPath(filepath.Join(home, "missing"), "x.md")

	if _, err := realOutputPathOn(given, false); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("traversing platform: err = %v, want a not-exist error", err)
	}

	got, err := realOutputPathOn(given, true)
	if err != nil {
		t.Fatalf("normalising platform: realOutputPathOn returned %v, want the collapsed path", err)
	}
	want, err := filepath.Abs(given)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("normalising platform: realOutputPathOn = %q, want %q", got, want)
	}
}

// TestTheDotDotRuleMatchesTheFilesystemUnderTheTest checks the PREMISE of the
// rule above against the machine actually running the test, rather than asserting
// it: it performs the very call this helper replaced — os.WriteFile through a
// ".." after a directory that does not exist — and requires realOutputPath to
// agree with what the filesystem did.
//
// It therefore carries no GOOS guard on purpose. On a traversing platform both
// fail; on a normalising one both succeed and name the same file. Compiling in
// the wrong rule for the host is what this catches, and it is the reason the
// Windows half of the rule does not rest on a claim: windows-latest runs it.
func TestTheDotDotRuleMatchesTheFilesystemUnderTheTest(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	given := dotDotPath(filepath.Join(home, "missing"), "probe.txt")

	writeErr := os.WriteFile(given, []byte("probe\n"), 0o600)
	resolved, resolveErr := realOutputPath(given)
	if (writeErr == nil) != (resolveErr == nil) {
		t.Fatalf("os.WriteFile(%q) = %v but realOutputPath = %v: the compiled-in \"..\" rule is not this filesystem's",
			given, writeErr, resolveErr)
	}
	if writeErr != nil {
		// Traversing platform: the write stopped at the missing component, which
		// is what the refusal preserves. Nothing landed anywhere.
		if _, err := os.Stat(filepath.Join(home, "probe.txt")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the write failed but a file appeared beside the missing directory: %v", err)
		}
		return
	}
	// Normalising platform: the write landed beside the missing component, and
	// realOutputPath must name that same file rather than refuse it.
	beside := filepath.Join(home, "probe.txt")
	if _, err := os.Stat(beside); err != nil {
		t.Fatalf("the write succeeded but %s is not there: %v", beside, err)
	}
	if resolved != beside {
		t.Fatalf("realOutputPath = %q, but the write landed at %q", resolved, beside)
	}
}

// TestIndexReportRefusesACommittedSymlinkedParentDirectory is the regression for
// the third review finding. os.Root guarantees the write cannot leave the root; it
// does NOT refuse a link that stays inside it — on Unix, Root FOLLOWS one. Checking
// only the FINAL component therefore left every parent component followed, and a
// repository that commits `reports -> .git` turned `--report reports/config` into a
// truncating write of git's own configuration, which is where core.pager and
// core.fsmonitor live.
func TestIndexReportRefusesACommittedSymlinkedParentDirectory(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	if err := os.Symlink(".git", filepath.Join(repo, "reports")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile report directory")
	victim := filepath.Join(repo, ".git", "config")
	before, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}

	err = runIndexReport(t, repo, filepath.Join(repo, "reports", "config"))
	if err == nil {
		t.Fatal("index --report followed a symlinked directory committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	after, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the repository's own git configuration was rewritten:\n%s", after)
	}
}

// TestVerifyRecordBaselineRefusesACommittedSymlinkedParentDirectory drives the same
// component rule through the other verb, which additionally CREATES its parent
// directories — so the check has to come before that creation walks the link too.
func TestVerifyRecordBaselineRefusesACommittedSymlinkedParentDirectory(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	if err := os.Symlink(".git", filepath.Join(repo, "reports")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile baseline directory")

	err := runVerifyRecord(t, repo, filepath.Join(repo, "reports", "nested", "base.json"))
	if err == nil {
		t.Fatal("verify --record-baseline followed a symlinked directory committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "nested")); !os.IsNotExist(err) {
		t.Fatalf("parent creation walked through the committed link into .git: %v", err)
	}
}

// TestOutputPathsOutsideTheRepositoryKeepWorkingThroughLinkedDirectories is the
// DO-NOT-OVER-CORRECT pin, and it passes both before and after the component rule
// above: refusing symlinked components must apply only INSIDE the repository. A
// caller-owned destination reached through a symlinked parent — /tmp on macOS, and
// the `--record-baseline /tmp/base.json` the verify help advertises — is unconfined
// by design and must still be written, parents created and all.
func TestOutputPathsOutsideTheRepositoryKeepWorkingThroughLinkedDirectories(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := runIndexReport(t, repo, filepath.Join(link, "GRAPH_REPORT.md")); err != nil {
		t.Fatalf("index --report through a caller-owned symlinked directory failed: %v", err)
	}
	if err := runVerifyRecord(t, repo, filepath.Join(link, "nested", "base.json")); err != nil {
		t.Fatalf("verify --record-baseline through a caller-owned symlinked directory failed: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(real, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(report), "# Graph report") {
		t.Fatalf("report content is wrong:\n%s", report)
	}
	baseline, err := os.ReadFile(filepath.Join(real, "nested", "base.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(baseline), `"format_version": 1`) {
		t.Fatalf("baseline is not the recorded JSON:\n%s", baseline)
	}
}

// TestIndexReportRefusesSymlinkWhenRepoIsALinkToASubdirectory is the regression for
// the fourth review finding. --repo may be an EXTERNAL symlink that points at a
// subdirectory of the checkout. Git resolves it and reports the real top level, but
// containment was walked over the alias's own lexical parents, which never reach
// that top level; the widening was refused, the boundary fell back to the alias, and
// every root-level file of the real checkout then classified as caller-owned and
// took the unconfined write.
func TestIndexReportRefusesSymlinkWhenRepoIsALinkToASubdirectory(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	write(t, repo, filepath.Join("subdir", "sub.go"), "package sub\n\nfunc Helper() {}\n")
	victim, victimContent := plantVictim(t)
	report := filepath.Join(repo, "GRAPH_REPORT.md")
	if err := os.Symlink(victim, report); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile report path at the repository root")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(repo, "subdir"), alias); err != nil {
		t.Fatal(err)
	}

	err := runIndexReportWithRepoFlag(t, alias, report)
	if err == nil {
		t.Fatal("index --repo <link to subdir> followed a symlink committed inside the repository")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("refusal does not say why: %v", err)
	}
	assertVictimIntact(t, victim, victimContent)
}

// TestConfinementRootResolvesAliasedRepositories pins the boundary that finding
// directly, rather than one escape through it: a symlink to a subdirectory still
// confines against the checkout it really lives in.
func TestConfinementRootResolvesAliasedRepositories(t *testing.T) {
	requireSymlinkSupport(t)
	repo := outputPathRepo(t)
	write(t, repo, filepath.Join("subdir", "sub.go"), "package sub\n\nfunc Helper() {}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "subdirectory")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(repo, "subdir"), alias); err != nil {
		t.Fatal(err)
	}

	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(confinementRoot(t.Context(), alias))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(repoInfo, rootInfo) {
		t.Fatal("confinement root for a symlinked subdirectory is not the checkout")
	}
}

// TestOutputPathsRefuseASymlinkTraversedByDotDot is the regression for the fifth
// review finding, and it is the THIRD route to the same file. `.git/config` holds
// core.pager and core.fsmonitor, which name programs git then runs, so truncating it
// with attacker-chosen report bytes escalates to execution.
//
// The first route was the leaf (`GRAPH_REPORT.md` committed as a link), the second a
// parent directory (`reports -> .git`, then `--report reports/config`). Both are
// caught by walking the destination's components. This one is not: a committed
// `link -> .git/objects` with `--report link/../config` has realOutputPath resolve
// the link while reading the ".." the way the kernel does, so the destination it
// hands on is the canonical `.git/config` — a plain directory and a plain file, with
// no component left for the walk to refuse. The refusal has to sit on the ROUTE, not
// only on the destination.
func TestOutputPathsRefuseASymlinkTraversedByDotDot(t *testing.T) {
	requireSymlinkSupport(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows collapses `link\\..` before the filesystem sees it, so the spelling never reaches the link's target")
	}

	// hostile builds a repository whose committed link, followed by a "..", lands on
	// the repository's own git configuration, and returns the caller's spelling of
	// that path together with the bytes the configuration must still have afterwards.
	hostile := func(t *testing.T) (repo, given, victim string, before []byte) {
		t.Helper()
		repo = outputPathRepo(t)
		if err := os.Symlink(filepath.Join(".git", "objects"), filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "hostile link traversed by a dot-dot")
		victim = filepath.Join(repo, ".git", "config")
		before, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		return repo, dotDotPath(filepath.Join(repo, "link"), "config"), victim, before
	}

	assertRefused := func(t *testing.T, err error, victim string, before []byte) {
		t.Helper()
		if err == nil {
			t.Fatal("the write was resolved through a symlink committed inside the repository")
		}
		if !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("refusal does not say why: %v", err)
		}
		after, readErr := os.ReadFile(victim)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("the repository's own git configuration was rewritten:\n%s", after)
		}
	}

	t.Run("index --report", func(t *testing.T) {
		repo, given, victim, before := hostile(t)
		assertRefused(t, runIndexReport(t, repo, given), victim, before)
		// The link is the repository's own committed entry: refusing must not have
		// replaced it with a regular file.
		info, err := os.Lstat(filepath.Join(repo, "link"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("the repository's symlink was replaced by a regular file")
		}
	})

	// The other verb creates its parents, so the refusal has to come before that
	// walk too — this spelling would otherwise mkdir inside the git directory.
	t.Run("verify --record-baseline", func(t *testing.T) {
		repo, given, victim, before := hostile(t)
		assertRefused(t, runVerifyRecord(t, repo, given), victim, before)
	})

	// The relative spelling reaches the same place through os.Getwd rather than
	// through an absolute path, which is the concatenation branch of rawAbs.
	t.Run("relative to the working directory", func(t *testing.T) {
		repo, _, victim, before := hostile(t)
		t.Chdir(repo)
		assertRefused(t, runIndexReport(t, repo, dotDotPath("link", "config")), victim, before)
	})
}

// TestOutputPathsAllowDotDotThroughARealDirectory is the DO-NOT-OVER-CORRECT pin for
// the rule above, on the inside-the-repository half. Refusing the ROUTE must refuse
// symlinks on it, not the ".." itself: a repository with no committed link there is
// an ordinary checkout, and `--report build/../GRAPH_REPORT.md` has to keep working.
// (The outside-the-repository half stays pinned by
// TestOutputPathsOutsideTheRepositoryKeepWorkingThroughLinkedDirectories, whose
// caller-owned symlinked parent must still be followed rather than refused.)
func TestOutputPathsAllowDotDotThroughARealDirectory(t *testing.T) {
	repo := outputPathRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	given := dotDotPath(filepath.Join(repo, "build"), "GRAPH_REPORT.md")
	if err := runIndexReport(t, repo, given); err != nil {
		t.Fatalf("a dot-dot through a real directory inside the repository was refused: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(repo, "GRAPH_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(report), "# Graph report") {
		t.Fatalf("report content is wrong:\n%s", report)
	}
}

// TestPathComponentsKeepsDotDot covers the splitter the route walk is built on, on
// EVERY platform including the one the symlink tests above skip. It is spelled with
// the platform separator so the Windows backslash is exercised there.
func TestPathComponentsKeepsDotDot(t *testing.T) {
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		path string
		want []string
	}{
		{sep, nil},
		{sep + "a" + sep + "b", []string{"a", "b"}},
		{sep + "a" + sep + ".." + sep + "b", []string{"a", "..", "b"}},
		{sep + "a" + sep + sep + "b" + sep, []string{"a", "b"}},
		{sep + "a" + sep + "." + sep + "b", []string{"a", ".", "b"}},
		{sep + "..a" + sep + "b..", []string{"..a", "b.."}},
	} {
		got := pathComponents(tc.path)
		if len(got) != len(tc.want) {
			t.Errorf("pathComponents(%q) = %q, want %q", tc.path, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("pathComponents(%q) = %q, want %q", tc.path, got, tc.want)
				break
			}
		}
	}
}

// TestOutputPathsKeepTheKernelDestinationThroughACallerOwnedLink is the
// DO-NOT-OVER-CORRECT pin for the rule above, on the route half.
//
// Classifying the CLEANED spelling is what routes the traversal escape into the
// refusal: a committed `link -> .git/objects` resolves `link/../config` to a path
// OUTSIDE the repository on disk, and only the cleaned spelling keeps it inside
// where refuseTraversedSymlinks can see it. But the cleaned spelling is a different
// FILE, so preferring it must be limited to the routes that need it — the ones
// whose link the repository controls.
//
// It was not. With a CALLER-OWNED `<parent>/link -> <elsewhere>/a/b`,
// `--report <parent>/link/../repo/out.md` is `<elsewhere>/a/repo/out.md` to the
// kernel — the ".." steps out of the link's TARGET — and `<parent>/repo/out.md`
// once cleaned, which is inside the scanned repository. Preferring the cleaned one
// moved the caller's report off the destination they named and truncated an
// unrelated file in the repository instead. The link is outside the repository, so
// it is caller-owned by the same ownership rule that leaves the whole
// out-of-repository write unconfined; there is nothing here to confine.
func TestOutputPathsKeepTheKernelDestinationThroughACallerOwnedLink(t *testing.T) {
	requireSymlinkSupport(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows collapses `link\\..` before the filesystem sees it, so the two spellings never differ")
	}

	// bystander is the in-repo file the CLEANED spelling names. Nothing may write
	// it: the caller never asked for it, and on a real checkout it is tracked work.
	const bystander = "the caller never named this file\n"

	// scene builds the shape once for both verbs and returns the caller's spelling,
	// the destination the kernel opens for it, and the in-repo file that must not
	// move. leaf is the output file's own name under <elsewhere>/a/.
	scene := func(t *testing.T, leaf ...string) (repo, given, kernelDest, inRepo string) {
		t.Helper()
		parent := t.TempDir()
		repo = outputPathRepoAt(t, filepath.Join(parent, "repo"))
		elsewhere := t.TempDir()
		if err := os.MkdirAll(filepath.Join(elsewhere, "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		// The kernel's destination directory, which is <link>/.. + "repo".
		if err := os.MkdirAll(filepath.Join(elsewhere, "a", "repo"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(filepath.Join(elsewhere, "a", "b"), link); err != nil {
			t.Fatal(err)
		}
		inRepo = filepath.Join(repo, filepath.Join(leaf...))
		if err := os.MkdirAll(filepath.Dir(inRepo), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(inRepo, []byte(bystander), 0o600); err != nil {
			t.Fatal(err)
		}
		given = dotDotPath(link, append([]string{"repo"}, leaf...)...)
		kernelDest = filepath.Join(append([]string{elsewhere, "a", "repo"}, leaf...)...)
		return repo, given, kernelDest, inRepo
	}

	assertLandedOnTheKernelDestination := func(t *testing.T, err error, kernelDest, inRepo, want string) {
		t.Helper()
		if err != nil {
			t.Fatalf("a caller-owned destination reached through their own link was refused: %v", err)
		}
		written, readErr := os.ReadFile(kernelDest)
		if readErr != nil {
			t.Fatalf("nothing was written to the path the kernel opens for the caller's spelling: %v", readErr)
		}
		if !strings.HasPrefix(string(written), want) {
			t.Fatalf("the destination holds the wrong bytes:\n%s", written)
		}
		untouched, readErr := os.ReadFile(inRepo)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(untouched) != bystander {
			t.Fatalf("the write was redirected onto an unrelated file inside the repository:\n%s", untouched)
		}
	}

	t.Run("index --report", func(t *testing.T) {
		repo, given, kernelDest, inRepo := scene(t, "out.md")
		assertLandedOnTheKernelDestination(t, runIndexReport(t, repo, given), kernelDest, inRepo, "# Graph report")
	})

	// The other verb creates its parents, so this also pins that the parents get
	// created under the kernel's destination rather than inside the repository.
	t.Run("verify --record-baseline", func(t *testing.T) {
		repo, given, kernelDest, inRepo := scene(t, "nested", "base.json")
		assertLandedOnTheKernelDestination(t, runVerifyRecord(t, repo, given), kernelDest, inRepo, "{")
	})
}

// TestOutputPathsRefuseASpellingThatNamesADirectory is the regression for the
// seventh review finding, and it is a bug this branch INTRODUCED rather than an
// escape it failed to close.
//
// Confining the write meant deciding the destination with filepath.Abs, which
// CLEANS. Cleaning erases the trailing separator and the final "." that say the
// caller named a DIRECTORY, so `--report out.md/` stopped being ENOTDIR against an
// existing out.md — the answer the kernel gives, and the answer the os.WriteFile
// this fix replaced returned — and became a truncating write of out.md itself. On
// an absent target the same spellings quietly created a regular file.
func TestOutputPathsRefuseASpellingThatNamesADirectory(t *testing.T) {
	const victim = "the caller named a directory, not this file\n"
	sep := string(filepath.Separator)

	for _, spelling := range []string{sep, sep + ".", sep + "..", sep + "." + sep} {
		t.Run("existing file, --report "+spelling, func(t *testing.T) {
			repo := outputPathRepo(t)
			target := filepath.Join(repo, "out.md")
			if err := os.WriteFile(target, []byte(victim), 0o600); err != nil {
				t.Fatal(err)
			}
			err := runIndexReport(t, repo, target+spelling)
			if err == nil {
				t.Fatal("a path naming a directory was accepted as an output file")
			}
			after, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("the file the caller did not name was replaced: %v", readErr)
			}
			if string(after) != victim {
				t.Fatalf("the file the caller did not name was truncated:\n%s", after)
			}
		})
	}

	// Absent target: nothing may be created at the cleaned spelling either.
	t.Run("absent target", func(t *testing.T) {
		repo := outputPathRepo(t)
		absent := filepath.Join(repo, "absent.md")
		if err := runVerifyRecord(t, repo, absent+sep); err == nil {
			t.Fatal("a path naming a directory was accepted as an output file")
		}
		if _, err := os.Lstat(absent); !os.IsNotExist(err) {
			t.Fatalf("the cleaned spelling was created anyway: %v", err)
		}
	})

	// Outside the repository takes the unconfined branch, and cleans the same way.
	t.Run("outside the repository", func(t *testing.T) {
		repo := outputPathRepo(t)
		target := filepath.Join(t.TempDir(), "out.md")
		if err := os.WriteFile(target, []byte(victim), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runIndexReport(t, repo, target+sep); err == nil {
			t.Fatal("a path naming a directory was accepted as an output file")
		}
		assertVictimIntact(t, target, victim)
	})

	// DO-NOT-OVER-CORRECT: only the spellings that NAME a directory are refused. A
	// "." or ".." in the MIDDLE of the path is an ordinary spelling of a file.
	t.Run("dot and dot-dot inside the path still write", func(t *testing.T) {
		repo := outputPathRepo(t)
		if err := os.MkdirAll(filepath.Join(repo, "build"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, given := range []string{
			dotDotPath(filepath.Join(repo, "build"), "GRAPH_REPORT.md"),
			filepath.Join(repo, "."+sep+"GRAPH_REPORT.md"),
		} {
			if err := runIndexReport(t, repo, given); err != nil {
				t.Fatalf("ordinary output path %q was refused: %v", given, err)
			}
			report, err := os.ReadFile(filepath.Join(repo, "GRAPH_REPORT.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(report), "# Graph report") {
				t.Fatalf("report content is wrong:\n%s", report)
			}
			if err := os.Remove(filepath.Join(repo, "GRAPH_REPORT.md")); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestNamesADirectoryReadsTheRawSpelling(t *testing.T) {
	sep := string(filepath.Separator)
	for _, testCase := range []struct {
		path string
		want bool
	}{
		{"out.md", false},
		{"out.md" + sep, true},
		{"out.md" + sep + ".", true},
		{"out.md" + sep + "..", true},
		{".", true},
		{"..", true},
		{sep, true},
		{"", false},
		{"..hidden", false},
		{".hidden", false},
		{"build" + sep + ".." + sep + "out.md", false},
		{"." + sep + "out.md", false},
	} {
		if got := namesADirectory(testCase.path); got != testCase.want {
			t.Errorf("namesADirectory(%q) = %v, want %v", testCase.path, got, testCase.want)
		}
	}
}

// assertSameDirectory compares two path spellings by filesystem identity, because
// the boundary is a directory and a directory has many names.
func assertSameDirectory(t *testing.T, got, want, what string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("%s %q is not on disk: %v", what, got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("%s is %q, want the checkout %q", what, got, want)
	}
}

// TestConfinementRootFindsTheCheckoutWhenGitCannotBeAsked is the regression for the
// fourth spelling of the original escape.
//
// The boundary was taken from an EXTERNAL PROGRAM, and EVERY failure of that program
// was read as the one answer "this directory is in no checkout" — which narrows the
// boundary to --repo itself and leaves the checkout's own root-level files outside
// it, caller-owned, on the unconfined write. `verify` needs no git at all, so running
// it where git is not installed is an ordinary configuration rather than a broken
// one; a checkout whose files another uid owns (`detected dubious ownership`) and a
// --repo the repository itself committed as a link out of the tree fail the same way.
//
// The premise is checked at RUNTIME, with git, before it is relied on: the fallback
// half needs a temp directory that really is in no checkout, and the discovery half
// needs git to really be unreachable.
func TestConfinementRootFindsTheCheckoutWhenGitCannotBeAsked(t *testing.T) {
	repo := outputPathRepo(t)
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()
	if _, err := gitutil.RepoRoot(t.Context(), plain); err == nil {
		t.Skip("this machine's temp directory is itself inside a checkout")
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := gitutil.RepoRoot(t.Context(), sub); err == nil {
		t.Fatal("git still ran; the premise of this test is that it cannot be asked")
	}

	assertSameDirectory(t, confinementRoot(t.Context(), sub), repo, "confinement root")
	// The other direction: where there is no checkout to find, the fallback to
	// --repo stands, so `verify --record-baseline` in a plain directory keeps
	// working exactly as it is documented to.
	if got := confinementRoot(t.Context(), plain); got != plain {
		t.Fatalf("a directory inside no checkout did not fall back to --repo: %q", got)
	}
}

// TestRecordBaselineStaysConfinedWhenRepoIsACommittedSymlinkOutOfTheCheckout drives
// the same hole through the real verb, with git present and working the whole time:
// the REPOSITORY decides that `rev-parse --show-toplevel` fails, by committing the
// subdirectory --repo names as a link to somewhere that is not a checkout.
func TestRecordBaselineStaysConfinedWhenRepoIsACommittedSymlinkOutOfTheCheckout(t *testing.T) {
	requireSymlinkSupport(t)
	parent := t.TempDir()
	outside := filepath.Join(parent, "notarepository")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := outputPathRepoAt(t, filepath.Join(parent, "checkout"))
	victim, victimContent := plantVictim(t)
	baseline := filepath.Join(repo, "GRAPH_REPORT.md")
	if err := os.Symlink(victim, baseline); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "subdir")
	if err := os.Symlink(outside, sub); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "hostile --repo and hostile report path")

	if _, err := gitutil.RepoRoot(t.Context(), sub); err == nil {
		t.Fatal("git reported a top level for a directory outside every checkout")
	}
	err := Run(t.Context(), Options{
		Version: "test-version",
		Env:     EntireEnv{RepoRoot: repo},
		Stdout:  &bytes.Buffer{},
	}, []string{"verify", "--repo", sub, "--test", "true", "--record-baseline", baseline})
	if err == nil {
		t.Fatal("verify --record-baseline followed a symlink committed inside the repository")
	}
	assertVictimIntact(t, victim, victimContent)
	info, lstatErr := os.Lstat(baseline)
	if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the repository's own entry was replaced rather than refused: %v", lstatErr)
	}
}
