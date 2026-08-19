package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrunedDescendantsKeepTheDirectoryRulesAttribution pins the attribution a
// directory prune gives its descendants, and it pins it to Git's own answer
// rather than to an intuition about specificity.
//
// A review round proposed re-deciding every descendant of a pruned directory and
// dropping the ones whose "winning" rule is a caller's own --ignore-file, on the
// theory that a caller-controlled exclusion was being mislabelled as
// repository-controlled. Git 2.54.0 says otherwise. With `hidden/` in the LOWEST
// precedence source and an explicit `!hidden/private.go` in the HIGHEST:
//
//	$ git check-ignore -v hidden/private.go
//	.git/info/exclude:1:hidden/	hidden/private.go
//
// An excluded parent directory is dispositive: no more specific rule from any
// source re-includes a path underneath it, and Git attributes the exclusion to
// the directory rule. So does this walk. Re-deciding per descendant would make
// the disclosure disagree with the tool every reader checks it against, and
// would drop paths the repository's own rule really did remove.
//
// The security claim underneath it does not survive either, and this test pins
// that too: the caller's label and pattern never reach the report, and the
// caller's extra rule changes nothing about which paths are named — every
// descendant of the pruned directory is disclosed with or without it.
func TestPrunedDescendantsKeepTheDirectoryRulesAttribution(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	write(t, repo, "hidden/private.go", "package hidden\n\nfunc Secret() {}\n")
	write(t, repo, "hidden/other.go", "package hidden\n\nfunc Other() {}\n")
	write(t, repo, graphIgnoreFileName, "hidden/\n")

	callerIgnore := filepath.Join(t.TempDir(), "caller-ignore")
	if err := os.WriteFile(callerIgnore, []byte("hidden/private.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := func(t *testing.T, ignoreFiles, includeFiles []string) *RepoIgnoreReport {
		t.Helper()
		ignores, err := loadWorktreeIgnoreMatcher(repo, ignoreFiles, includeFiles)
		if err != nil {
			t.Fatal(err)
		}
		ledger := &repoIgnoreLedger{}
		paths, err := walkWorktreeFiles(repo, ignores, func(string) bool { return false }, ledger)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if strings.HasPrefix(path, "hidden/") && len(includeFiles) == 0 {
				t.Fatalf("fixture is wrong: %q survived the prune", path)
			}
		}
		return ledger.report()
	}

	bare := report(t, nil, nil)
	if bare == nil {
		t.Fatal("a pruned source tree left the corpus with no disclosure at all")
	}
	withCaller := report(t, []string{callerIgnore}, nil)
	if withCaller == nil {
		t.Fatal("the caller's own extra exclusion silenced the repository's disclosure entirely")
	}

	// Same paths, same count, same attribution — the caller's rule is redundant
	// with a prune that already removed the whole tree.
	if withCaller.Files != bare.Files || withCaller.Files != 2 {
		t.Fatalf("files = %d with the caller rule and %d without, want 2 both ways: the prune "+
			"removed both descendants either way", withCaller.Files, bare.Files)
	}
	for _, exclusion := range withCaller.Sample {
		if exclusion.Source != graphIgnoreFileName || exclusion.Rule != "hidden" {
			t.Fatalf("%s attributed to %s:%q, want %s:%q — Git attributes a descendant of an "+
				"excluded directory to the directory rule",
				exclusion.Path, exclusion.Source, exclusion.Rule, graphIgnoreFileName, "hidden")
		}
		// Nothing the caller wrote may ride out on a repository-controlled report.
		if strings.Contains(exclusion.Source, "caller-ignore") || exclusion.Rule == "hidden/private.go" {
			t.Fatalf("the caller's own exclusion leaked into the disclosure: %+v", exclusion)
		}
	}
	for _, source := range withCaller.Sources {
		if source.File != graphIgnoreFileName {
			t.Fatalf("report credits %q, want only %s", source.File, graphIgnoreFileName)
		}
	}
}

// TestCallerIncludeFileKeepsAPrunedDescendantOutOfTheDisclosure is the other
// direction, and it is the one that would actually be a bug: a path the caller
// pulled BACK into the corpus must not be reported as removed from it.
//
// This is where the caller/repository distinction really lives. An --include-file
// is the one thing that stops the prune (walkWorktreeFiles consults
// MayIncludeDescendant before SkipDir), so the descendant is indexed and must not
// appear in a report about what the repository hid.
func TestCallerIncludeFileKeepsAPrunedDescendantOutOfTheDisclosure(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	write(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	write(t, repo, "hidden/private.go", "package hidden\n\nfunc Secret() {}\n")
	write(t, repo, "hidden/other.go", "package hidden\n\nfunc Other() {}\n")
	write(t, repo, graphIgnoreFileName, "hidden/\n")

	callerInclude := filepath.Join(t.TempDir(), "caller-include")
	if err := os.WriteFile(callerInclude, []byte("hidden/private.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ignores, err := loadWorktreeIgnoreMatcher(repo, nil, []string{callerInclude})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &repoIgnoreLedger{}
	paths, err := walkWorktreeFiles(repo, ignores, func(string) bool { return false }, ledger)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, path := range paths {
		if path == "hidden/private.go" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("the caller's --include-file did not re-include the path, so the disclosure " +
			"question below is not the one this test means to ask")
	}
	report := ledger.report()
	if report == nil {
		t.Fatal("the sibling the prune really did remove was not disclosed")
	}
	if report.Files != 1 {
		t.Fatalf("files = %d, want 1: only hidden/other.go left the corpus", report.Files)
	}
	for _, exclusion := range report.Sample {
		if exclusion.Path == "hidden/private.go" {
			t.Fatalf("a path that IS in the corpus was reported as excluded from it: %+v", exclusion)
		}
	}
}
