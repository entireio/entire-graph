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
// No symlink is involved, so this runs on every platform, Windows included.
func TestOutputPathsRefuseDotDotThroughAMissingDirectory(t *testing.T) {
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
