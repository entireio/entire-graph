package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// walkHidingTree builds a NON-GIT directory — the listing mode `walkWorktreeFiles`
// serves, because `git ls-files` cannot enumerate it — whose real implementation
// sits in a subdirectory that one `.graphignore` line prunes.
func walkHidingTree(t *testing.T, rule string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "hidden/auth.go", `package hidden

// ValidateToken checks the bearer token presented on a request.
func ValidateToken(token string) bool { return len(token) == 64 }
`)
	write(t, root, "visible/auth_stub.go", `package visible

// ValidateTokenStub is the permissive stand-in.
func ValidateTokenStub(token string) bool { return token != "" }
`)
	write(t, root, graphIgnoreFileName, rule)
	return root
}

// TestSearchDisclosesWalkFallbackDirectoryPrune is the directory-prune blind spot.
//
// A rule naming a FILE is disclosed by the walk fallback, but a rule naming a
// DIRECTORY makes WalkDir return SkipDir before any child is ever tested, so an
// entire source tree left the corpus with repo_ignored == nil. It FAILS AT
// RUNTIME on the current head: the subtree disappears and the payload discloses
// nothing.
func TestSearchDisclosesWalkFallbackDirectoryPrune(t *testing.T) {
	t.Parallel()
	root := walkHidingTree(t, "hidden/\n")
	response, err := SearchRepository(t.Context(), root, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: the prune works, so there is something to disclose.
	for _, result := range response.Results {
		if result.FilePath == "hidden/auth.go" {
			t.Fatalf("fixture is wrong: %s did not prune hidden/", graphIgnoreFileName)
		}
	}
	if response.RepoIgnored == nil {
		t.Fatalf("a %s directory rule pruned a whole source tree and the response disclosed nothing", graphIgnoreFileName)
	}
	if response.RepoIgnored.Files != 1 {
		t.Errorf("RepoIgnored.Files = %d, want 1 (the pruned tree holds one source file)", response.RepoIgnored.Files)
	}
	if response.Stats.RepoIgnoredFiles != response.RepoIgnored.Files {
		t.Errorf("Stats.RepoIgnoredFiles = %d, want %d", response.Stats.RepoIgnoredFiles, response.RepoIgnored.Files)
	}
	if len(response.RepoIgnored.Sample) != 1 || response.RepoIgnored.Sample[0].Path != "hidden/auth.go" {
		t.Errorf("Sample = %+v, want hidden/auth.go — the actionable half is the path", response.RepoIgnored.Sample)
	}
}

// TestWalkFallbackKeepsGitAppliedDirectoryPrunesQuiet is the kind-(b) guard on the
// fix above: the ordinary build-output directory every .gitignore excludes must
// not start printing paths. Only a prune Git would not have made itself is worth
// a reader's attention.
func TestWalkFallbackKeepsGitAppliedDirectoryPrunesQuiet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "build/generated.go", "package build\n\nfunc Generated() {}\n")
	write(t, root, "app/main.go", "package app\n\nfunc Main() {}\n")
	write(t, root, ".gitignore", "build/\n")
	response, err := SearchRepository(t.Context(), root, "test", "generated", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored != nil {
		t.Fatalf("a .gitignore directory prune Git applies itself must stay quiet, got %+v", response.RepoIgnored)
	}
}

// TestDisclosureSkipsPathsTheSnapshotWouldNotRead is the phantom-exclusion
// finding. `git ls-files` lists index entries, including a file whose deletion is
// not staged, and the snapshot never reads one of those. Attributing it to the
// repository's ignore rules claims a file was hidden that was not there to hide.
//
// FAILS AT RUNTIME on the current head: the ledger is written before the
// eligibility check, so the deleted path is reported as removed by .graphignore.
func TestDisclosureSkipsPathsTheSnapshotWouldNotRead(t *testing.T) {
	t.Parallel()
	repo := hidingRepo(t, graphIgnoreFileName)
	// The ignored file leaves the working tree; Git still lists the index entry.
	if err := os.Remove(filepath.Join(repo, "internal", "auth", "auth.go")); err != nil {
		t.Fatal(err)
	}
	response, err := SearchRepository(t.Context(), repo, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored != nil {
		t.Fatalf("no file was removed from the corpus by an ignore rule — auth.go was already gone from the"+
			" working tree — yet the response disclosed %+v", response.RepoIgnored)
	}
	if response.Stats.RepoIgnoredFiles != 0 {
		t.Errorf("Stats.RepoIgnoredFiles = %d, want 0", response.Stats.RepoIgnoredFiles)
	}
}

// TestRepoIgnoreDisclosureComesFirst pins the placement, not a preference: every
// renderer that caps the diagnostics it prints takes them from the head of the
// list, so a disclosure appended behind three unrelated warnings loses the path
// it exists to name.
func TestRepoIgnoreDisclosureComesFirst(t *testing.T) {
	t.Parallel()
	existing := []ProviderWarning{{Code: "W_ONE"}, {Code: "W_TWO"}, {Code: "W_THREE"}}
	got := withRepoIgnoreDisclosure(existing, &RepoIgnoreReport{
		Files:   1,
		Sources: []RepoIgnoreSource{{File: graphIgnoreFileName, Files: 1}},
		Sample:  []RepoExclusion{{Path: "internal/auth/auth.go", Source: graphIgnoreFileName, Rule: "internal/auth/auth.go"}},
	})
	if len(got) != 4 {
		t.Fatalf("warnings = %d, want 4", len(got))
	}
	if got[0].Code != repoIgnoreDisclosureCode {
		t.Fatalf("warnings[0] = %q, want the disclosure first so a capped renderer still prints it", got[0].Code)
	}
	if got[0].FilePath != "internal/auth/auth.go" {
		t.Errorf("FilePath = %q, want the excluded path", got[0].FilePath)
	}
	for i, code := range []string{"W_ONE", "W_TWO", "W_THREE"} {
		if got[i+1].Code != code {
			t.Errorf("warnings[%d] = %q, want %q — the existing order must survive", i+1, got[i+1].Code, code)
		}
	}
	if existing[0].Code != "W_ONE" {
		t.Errorf("the caller's slice was mutated: %+v", existing)
	}
}

// TestWalkPruneDisclosureOmitsWhatTheScanDropsAnyway is the over-disclosure half
// of the prune accounting. The outer walk never puts a vendored tree or a
// lockfile in the corpus, so a `.graphignore` rule over their parent did not
// remove them from it — naming them as removed by the repository's rules blames a
// committed line for content no line hid, and buries the one path that matters
// (here `hidden/auth.go`, which the ten-path sample could not even reach).
//
// The other direction is asserted in the same test: ordinary source nested deeper
// inside the pruned tree, behind a nested .gitignore with no opinion about it,
// must still be counted and named. Fixing over-disclosure by disclosing less of
// the real thing would be the worse regression.
//
// FAILS AT RUNTIME on the current head: Files = 5 and the sample leads with
// node_modules.
func TestWalkPruneDisclosureOmitsWhatTheScanDropsAnyway(t *testing.T) {
	t.Parallel()
	root := walkHidingTree(t, "hidden/\n")
	write(t, root, "hidden/pkg/deep/service.go", `package deep

// ValidateTokenDeep is first-party source nested inside the pruned tree.
func ValidateTokenDeep(token string) bool { return token != "" }
`)
	write(t, root, "hidden/node_modules/dep/index.js", "module.exports = function () { return 1; };\n")
	write(t, root, "hidden/vendor/lib/lib.go", "package lib\n\nfunc Lib() {}\n")
	write(t, root, "hidden/yarn.lock", "# lockfile\n")
	response, err := SearchRepository(t.Context(), root, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored == nil {
		t.Fatalf("a %s directory rule pruned a source tree and the response disclosed nothing", graphIgnoreFileName)
	}
	disclosed := make(map[string]bool, len(response.RepoIgnored.Sample))
	for _, exclusion := range response.RepoIgnored.Sample {
		disclosed[exclusion.Path] = true
	}
	for _, path := range []string{"hidden/auth.go", "hidden/pkg/deep/service.go"} {
		if !disclosed[path] {
			t.Errorf("%s left the corpus with the prune and was not named; sample = %+v", path, response.RepoIgnored.Sample)
		}
	}
	for _, path := range []string{
		"hidden/node_modules/dep/index.js",
		"hidden/vendor/lib/lib.go",
		"hidden/yarn.lock",
	} {
		if disclosed[path] {
			t.Errorf("%s is dropped by the scan itself, so no ignore rule removed it from the corpus", path)
		}
	}
	if response.RepoIgnored.Files != 2 {
		t.Errorf("RepoIgnored.Files = %d, want 2 (the two source files the rule actually removed)", response.RepoIgnored.Files)
	}
	if response.Stats.RepoIgnoredFiles != response.RepoIgnored.Files {
		t.Errorf("Stats.RepoIgnoredFiles = %d, want %d", response.Stats.RepoIgnoredFiles, response.RepoIgnored.Files)
	}
}

// TestWalkPruneDisclosureDoesNotEnterGitExcludedSubtrees proves the accounting
// walk stops where the outer walk stops instead of crawling a Git-excluded tree
// to filter it file by file.
//
// The unreadable directory is the probe: it can only be reached by descending
// into `hidden/generated/`, which `.gitignore` excludes and the outer walk prunes
// wholesale. Reaching it also corrupted the disclosure — the count came back
// `count_incomplete` over a subtree that was never part of the count, i.e. the
// report understated itself about content it had no business counting.
//
// FAILS AT RUNTIME on the current head: CountIncomplete is true and Unreadable
// names hidden/generated/deep.
func TestWalkPruneDisclosureDoesNotEnterGitExcludedSubtrees(t *testing.T) {
	t.Parallel()
	if !unreadableDirectoriesHold(t) {
		t.Skip("this user/filesystem can read a 0o000 directory, so the probe cannot be built")
	}
	root := walkHidingTree(t, "hidden/\n")
	write(t, root, ".gitignore", "generated/\n")
	write(t, root, "hidden/generated/deep/built.go", "package deep\n\nfunc Built() {}\n")
	unreadable := filepath.Join(root, "hidden", "generated", "deep")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	response, err := SearchRepository(t.Context(), root, "test", "bearer token validation", SearchOptions{
		Worktree: true,
		Profile:  ProfileSyntaxOnly,
		TopK:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RepoIgnored == nil {
		t.Fatalf("a %s directory rule pruned a source tree and the response disclosed nothing", graphIgnoreFileName)
	}
	if response.RepoIgnored.CountIncomplete || len(response.RepoIgnored.Unreadable) != 0 {
		t.Errorf("the accounting walk descended into a Git-excluded tree: CountIncomplete = %t, Unreadable = %v",
			response.RepoIgnored.CountIncomplete, response.RepoIgnored.Unreadable)
	}
	if response.RepoIgnored.Files != 1 {
		t.Errorf("RepoIgnored.Files = %d, want 1 (hidden/auth.go; the generated tree is Git's own exclusion)",
			response.RepoIgnored.Files)
	}
}
