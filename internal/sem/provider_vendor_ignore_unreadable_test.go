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

// TestSnapshotDoesNotSilentlyDropPathsWhenANestedVendorGitignoreIsUnreadable
// reproduces the trail finding on headVendorIgnoreRules: this provider's
// GIT_ALLOW_PROTOCOL= no-egress guard (added to close the pre-2.45
// GIT_NO_LAZY_FETCH gap) means a partial clone can no longer lazily fetch a
// promised-but-missing blob, so `git show` for it fails with a REAL error
// rather than "path does not exist". headVendorIgnoreRules used to treat that
// error exactly like an absent file, silently discarding the negation
// `vendor/.gitignore` (`*` + `!pkg/`) declares — and with it, the first-party
// path `vendor/pkg/keep.go` it re-includes, with no warning at all.
//
// A loose object deleted after the commit reproduces the same failure mode
// ShowFile sees (a real git error, never the missing-path diagnostic)
// without needing a real partial clone or network denial.
func TestSnapshotDoesNotSilentlyDropPathsWhenANestedVendorGitignoreIsUnreadable(t *testing.T) {
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

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var sawKeep bool
	for _, file := range snapshot.Files {
		if file.Path == "vendor/pkg/keep.go" {
			sawKeep = true
		}
	}
	if !sawKeep {
		t.Errorf("vendor/pkg/keep.go missing from snapshot files (%d files); an unreadable nested"+
			" .gitignore must not silently drop the first-party path its negation re-includes",
			len(snapshot.Files))
	}

	var sawWarning bool
	for _, warning := range snapshot.Header.Warnings {
		if warning.Code == "W_VENDOR_IGNORE_UNREADABLE" {
			sawWarning = true
			if warning.FilePath != "vendor/.gitignore" {
				t.Errorf("W_VENDOR_IGNORE_UNREADABLE warning FilePath = %q, want %q", warning.FilePath, "vendor/.gitignore")
			}
		}
	}
	if !sawWarning {
		t.Error("no W_VENDOR_IGNORE_UNREADABLE warning: an unreadable nested .gitignore must be disclosed, not silent")
	}
}

// TestNestedVendorGitignoreStillExcludesUnrelatedVendoredPathsWhenAnotherIsUnreadable
// is the narrowing half: an unreadable .gitignore must fail open only for the
// specific subtree its own rules govern, not for every vendored directory in
// the repository, or the heuristic loses all value the instant any one blob
// is unreadable. node_modules/ here has no .gitignore of its own at all, so
// nothing about vendor/.gitignore being unreadable can legitimately affect it.
func TestNestedVendorGitignoreStillExcludesUnrelatedVendoredPathsWhenAnotherIsUnreadable(t *testing.T) {
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

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	filesByPath := map[string]struct{}{}
	for _, file := range snapshot.Files {
		filesByPath[file.Path] = struct{}{}
	}
	if _, ok := filesByPath["vendor/pkg/keep.go"]; !ok {
		t.Error("vendor/pkg/keep.go missing: its own unreadable .gitignore must fail open for its subtree")
	}
	if _, ok := filesByPath["node_modules/thirdparty.js"]; ok {
		t.Error("node_modules/thirdparty.js present: an unrelated vendored directory with no .gitignore" +
			" of its own must still be excluded when only vendor/.gitignore is unreadable")
	}
}

func TestUnreadableNestedVendorGitignoreKeepsItsVendoredAncestor(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "vendor/pkg/.gitignore", "*\n!keep.go\n")
	writeFile(t, repo, "vendor/pkg/keep.go", "package pkg\n")
	writeFile(t, repo, "main.go", "package main\n")
	git(t, repo, "add", "-f", "-A")
	git(t, repo, "commit", "-m", "nested vendor rules with a re-included file")

	hash := gitRevParseOutput(t, repo, "HEAD:vendor/pkg/.gitignore")
	deleteLooseObject(t, repo, hash)

	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version", ProviderSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range snapshot.Files {
		if file.Path == "vendor/pkg/keep.go" {
			return
		}
	}
	t.Error("vendor/pkg/keep.go missing: an unreadable nested .gitignore must also keep a vendored ancestor that traversal must cross")
}

func TestBoundedNestedIgnorePathsCapsEveryAttemptedInput(t *testing.T) {
	paths := []string{"main.go", ".gitignore"}
	for i := 0; i < maxNestedIgnoreFiles+1; i++ {
		paths = append(paths, fmt.Sprintf("dir-%04d/.gitignore", i))
	}

	got, truncated := boundedNestedIgnorePaths(paths)
	if !truncated {
		t.Fatal("boundedNestedIgnorePaths did not disclose an input beyond the limit")
	}
	if len(got) != maxNestedIgnoreFiles {
		t.Fatalf("boundedNestedIgnorePaths returned %d inputs, want bounded %d", len(got), maxNestedIgnoreFiles)
	}
	if last := got[len(got)-1]; last != fmt.Sprintf("dir-%04d/.gitignore", maxNestedIgnoreFiles-1) {
		t.Fatalf("last bounded nested ignore path = %q, want stable input prefix", last)
	}

	rules := newNestedIgnoreRules(ignoreMatcher{})
	rules.incomplete = true
	if !rules.ReincludesDescendant("vendor") {
		t.Fatal("incomplete nested ignore inputs did not fail open for an arbitrary vendored subtree")
	}
}
