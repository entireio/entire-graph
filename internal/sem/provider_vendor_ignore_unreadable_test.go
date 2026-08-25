package sem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRevParseOutput runs `git rev-parse` in repo and returns the trimmed
// stdout, failing the test on error.
func gitRevParseOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"rev-parse"}, args...)...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// deleteLooseObject removes a committed blob's own loose object file from
// repo's object store, without touching the tree entry that names it. Git
// still knows a blob belongs there — `git ls-tree`/`git rev-parse` keep
// answering from the tree and commit objects, which are untouched — but any
// read of the blob's OWN content now fails exactly as it would for a
// promised-but-missing object in a partial clone whose lazy fetch this
// provider's GIT_ALLOW_PROTOCOL= guard refuses to allow: a real error, never
// git's "path does not exist" diagnostic.
func deleteLooseObject(t *testing.T, repo, hash string) {
	t.Helper()
	// Loose objects can be packed instead (e.g. after a clone that runs its
	// own gc), so this repacks loose first to guarantee a loose file exists
	// to delete; --unpacked with no other args is a no-op if already loose.
	objPath := filepath.Join(repo, ".git", "objects", hash[:2], hash[2:])
	if _, err := os.Stat(objPath); err != nil {
		t.Fatalf("blob %s is not a loose object at %s (%v); repacking may be required for this git version", hash, objPath, err)
	}
	if err := os.Remove(objPath); err != nil {
		t.Fatalf("remove loose object %s: %v", objPath, err)
	}
}

// A missing committed ignore blob must stop the snapshot with an actionable
// error. Continuing with incomplete rules would make both inclusion and
// vendored-directory decisions depend on silently missing policy.
func TestSnapshotReportsNestedVendorGitignoreUnreadable(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/.gitignore", "*\n!pkg/\n")
	writeFile(t, repo, "vendor/pkg/keep.go", "package pkg\n")
	writeFile(t, repo, "main.go", "package main\n")
	// vendor/ is a name the vendored-directory heuristic treats as vendored by
	// default; these files must be force-added despite vendor/.gitignore's own
	// `*` rule, the same way a project that checks in an allowlisted vendor
	// subtree has to.
	git(t, repo, "add", "-f", "-A")
	git(t, repo, "commit", "-m", "vendor with a re-included subpackage")

	hash := gitRevParseOutput(t, repo, "HEAD:vendor/.gitignore")
	deleteLooseObject(t, repo, hash)

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err == nil || !strings.Contains(err.Error(), `vendor/.gitignore`) ||
		!strings.Contains(err.Error(), "Git blob object is unavailable or unreadable") {
		t.Fatalf("unreadable nested ignore error = %v, want path and actionable blob diagnosis", err)
	}
}

func TestUnreadableNestedVendorGitignoreStopsBeforeUnrelatedFiltering(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/.gitignore", "*\n!pkg/\n")
	writeFile(t, repo, "vendor/pkg/keep.go", "package pkg\n")
	writeFile(t, repo, "node_modules/thirdparty.js", "module.exports = {};\n")
	writeFile(t, repo, "main.go", "package main\n")
	git(t, repo, "add", "-f", "-A")
	git(t, repo, "commit", "-m", "a vendored subtree with a re-included subpackage, plus an unrelated vendored dir")

	hash := gitRevParseOutput(t, repo, "HEAD:vendor/.gitignore")
	deleteLooseObject(t, repo, hash)

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err == nil || !strings.Contains(err.Error(), `vendor/.gitignore`) {
		t.Fatalf("unreadable nested ignore error = %v, want the policy path", err)
	}
}

func TestUnreadableDeepNestedVendorGitignoreIsReported(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/pkg/.gitignore", "*\n!keep.go\n")
	writeFile(t, repo, "vendor/pkg/keep.go", "package pkg\n")
	writeFile(t, repo, "main.go", "package main\n")
	git(t, repo, "add", "-f", "-A")
	git(t, repo, "commit", "-m", "nested vendor rules with a re-included file")

	hash := gitRevParseOutput(t, repo, "HEAD:vendor/pkg/.gitignore")
	deleteLooseObject(t, repo, hash)

	_, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err == nil || !strings.Contains(err.Error(), `vendor/pkg/.gitignore`) {
		t.Fatalf("unreadable deep nested ignore error = %v, want the scoped policy path", err)
	}
}

func TestNestedIgnorePathsRejectEveryInputBeyondBound(t *testing.T) {
	paths := []string{"main.go", ".gitignore"}
	for i := 0; i < maxNestedIgnoreFiles+1; i++ {
		paths = append(paths, fmt.Sprintf("dir-%04d/.gitignore", i))
	}

	if _, err := nestedIgnorePathsFromListing(paths); err == nil ||
		!strings.Contains(err.Error(), fmt.Sprintf("more than %d nested ignore files", maxNestedIgnoreFiles)) {
		t.Fatalf("nested ignore overflow error = %v, want explicit resource-bound refusal", err)
	}
}
