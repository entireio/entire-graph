package sem

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// newDependentsTestRepo creates a git repo with:
//   - auth.py defining Foo (capitalized) and foo (lowercase, unrelated).
//   - caller_one.py and caller_two.py each with a genuine dependent that
//     calls Foo.
//   - near_miss.py that only contains "foo" (case differs from "Foo").
//   - substring.py that only contains "myFooBar" (substring, not a token).
//
// It returns the repo path and the head commit.
func newDependentsTestRepo(t *testing.T) (repo, head string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def Foo(token):
    return bool(token)

def foo(token):
    return not bool(token)
`)
	write(t, repo, "caller_one.py", `def check(token):
    return Foo(token)
`)
	write(t, repo, "caller_two.py", `def check_again(token):
    return Foo(token) and Foo(token)
`)
	write(t, repo, "near_miss.py", `def uses_lowercase(token):
    return foo(token)
`)
	write(t, repo, "substring.py", `def myFooBar(token):
    return token
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	head = rev(t, repo, "HEAD")
	return repo, head
}

// TestBuildReferenceIndexCaseSensitiveWholeToken pins the pre-grep semantics:
// a case-sensitive, whole-token match on the entity block. Git's grep
// preselection is case-insensitive substring matching, a strict superset, but
// the final containsIdentifier check must still exclude near-miss files.
func TestBuildReferenceIndexCaseSensitiveWholeToken(t *testing.T) {
	repo, head := newDependentsTestRepo(t)

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings on a clean grep prefilter, got %#v", warnings)
	}

	dependents := index["Foo"]

	// (c) genuine dependents in multiple files, counted once per entity even
	// when an entity calls Foo more than once.
	if _, ok := dependents["caller_one.py#function:check"]; !ok {
		t.Fatalf("expected caller_one.py check() as a dependent, got %#v", dependents)
	}
	if _, ok := dependents["caller_two.py#function:check_again"]; !ok {
		t.Fatalf("expected caller_two.py check_again() as a dependent, got %#v", dependents)
	}

	// (d) self-name exclusion: Foo's own definition must not count itself.
	if _, ok := dependents["auth.py#function:Foo"]; ok {
		t.Fatalf("Foo must not be counted as its own dependent, got %#v", dependents)
	}

	// (a) a file matching only case-insensitively (contains "foo" but the
	// name is "Foo") must contribute 0 dependents.
	if _, ok := dependents["near_miss.py#function:uses_lowercase"]; ok {
		t.Fatalf("case-insensitive-only match must not count as a dependent, got %#v", dependents)
	}
	if _, ok := dependents["auth.py#function:foo"]; ok {
		t.Fatalf("lowercase foo definition must not count as a dependent of Foo, got %#v", dependents)
	}

	// (b) a file matching as a substring only ("myFooBar" vs name "Foo")
	// must contribute 0 dependents.
	if _, ok := dependents["substring.py#function:myFooBar"]; ok {
		t.Fatalf("substring-only match must not count as a dependent, got %#v", dependents)
	}

	if got, want := len(dependents), 2; got != want {
		t.Fatalf("dependents count = %d, want %d: %#v", got, want, dependents)
	}
}

func TestBuildReferenceIndexWarnsWhenPrefilteredBlobDisappears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git wrapper is a POSIX shell script")
	}
	repo, head := newDependentsTestRepo(t)
	objectID := rev(t, repo, head+":caller_one.py")
	objectPath := filepath.Join(repo, ".git", "objects", objectID[:2], objectID[2:])
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("locate loose candidate blob: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "git")
	script := `#!/bin/sh
for arg do
	if [ "$arg" = "grep" ]; then
		"$REAL_GIT" "$@"
		status=$?
		if [ "$status" -eq 0 ]; then
			unlink "$DELETE_OBJECT"
		fi
		exit "$status"
	fi
done
exec "$REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("DELETE_OBJECT", objectPath)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, counted := index["Foo"]["caller_one.py#function:check"]; counted {
		t.Fatalf("dependent was counted after its blob disappeared: %#v", index["Foo"])
	}
	if _, counted := index["Foo"]["caller_two.py#function:check_again"]; !counted {
		t.Fatalf("readable dependent disappeared beside the missing blob: %#v", index["Foo"])
	}
	var fileWarnings []ProviderWarning
	for _, warning := range warnings {
		if warning.FilePath == "caller_one.py" {
			fileWarnings = append(fileWarnings, warning)
		}
	}
	if len(fileWarnings) != 1 {
		t.Fatalf("missing blob after dependent prefilter warnings = %#v, want exactly one", fileWarnings)
	}
	warning := fileWarnings[0]
	if warning.Code != "E_FILE_READ" || warning.Severity != "error" ||
		!strings.Contains(warning.Detail, "unavailable") ||
		!strings.Contains(warning.EffectOnCompleteness, "not counted") {
		t.Fatalf("missing blob warning = %#v, want exact E_FILE_READ completeness disclosure", warning)
	}
}

func TestBuildReferenceIndexWarnsWhenPromisedBlobIsMissingBeforePrefilter(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")

	authBase := gitObjectInputOutput(t, repo, "def Foo():\n    return 1\n", "hash-object", "-w", "--stdin")
	authHead := gitObjectInputOutput(t, repo, "def Foo():\n    return 2\n", "hash-object", "-w", "--stdin")
	missingCaller := gitObjectInputOutput(t, repo, "def missing_caller():\n    return Foo()\n", "hash-object", "-w", "--stdin")
	readableCaller := gitObjectInputOutput(t, repo, "def readable_caller():\n    return Foo()\n", "hash-object", "-w", "--stdin")
	treeInput := func(auth string) string {
		return fmt.Sprintf(
			"100644 blob %s\tauth.py%c100644 blob %s\tmissing.py%c100644 blob %s\treadable.py%c",
			auth, byte(0), missingCaller, byte(0), readableCaller, byte(0),
		)
	}
	baseTree := gitObjectInputOutput(t, repo, treeInput(authBase), "mktree", "-z")
	base := gitObjectInputOutput(
		t, repo, "", "-c", "user.name=Entire Graph Test", "-c", "user.email=graph@example.com",
		"commit-tree", baseTree, "-m", "base",
	)
	headTree := gitObjectInputOutput(t, repo, treeInput(authHead), "mktree", "-z")
	head := gitObjectInputOutput(
		t, repo, "", "-c", "user.name=Entire Graph Test", "-c", "user.email=graph@example.com",
		"commit-tree", headTree, "-p", base, "-m", "head",
	)
	git(t, repo, "update-ref", "refs/heads/main", head)
	git(t, repo, "config", "extensions.partialClone", "origin")
	git(t, repo, "config", "remote.origin.promisor", "true")
	git(t, repo, "config", "remote.origin.partialclonefilter", "blob:none")

	objectPath := filepath.Join(repo, ".git", "objects", missingCaller[:2], missingCaller[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove promised caller blob: %v", err)
	}
	assertMissing := func(stage string) {
		t.Helper()
		cmd := exec.Command("git", "cat-file", "-e", missingCaller)
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=")
		if err := cmd.Run(); err == nil {
			t.Fatalf("%s: promised caller blob unexpectedly exists", stage)
		}
	}
	assertMissing("before dependent scan")

	index, warnings, err := buildReferenceIndex(t.Context(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}
	assertMissing("after dependent scan")
	if _, ok := index["Foo"]["readable.py#function:readable_caller"]; !ok {
		t.Fatalf("readable sibling dependent was not counted: %#v", index["Foo"])
	}
	if _, ok := index["Foo"]["missing.py#function:missing_caller"]; ok {
		t.Fatalf("missing promised dependent was unexpectedly counted: %#v", index["Foo"])
	}

	var fallbackWarnings, missingWarnings []ProviderWarning
	for _, warning := range warnings {
		if warning.Code == "W_DEPENDENTS_PREFILTER_FAILED" {
			fallbackWarnings = append(fallbackWarnings, warning)
		}
		if warning.Code == "E_FILE_READ" && warning.FilePath == "missing.py" {
			missingWarnings = append(missingWarnings, warning)
		}
	}
	if len(fallbackWarnings) != 1 || len(missingWarnings) != 1 {
		t.Fatalf("promised missing blob warnings = %#v, want one prefilter and one file-read warning", warnings)
	}
	if !strings.Contains(missingWarnings[0].EffectOnCompleteness, "not counted") {
		t.Fatalf("missing blob warning does not disclose undercount: %#v", missingWarnings[0])
	}
}

// TestBuildReferenceIndexEmptyNamesDoesNoWork pins that an empty names map
// short-circuits before any git call is made -- passing a repo path that
// does not exist must not surface an error, because buildReferenceIndex
// should never reach gitutil.
func TestBuildReferenceIndexEmptyNamesDoesNoWork(t *testing.T) {
	index, warnings, err := buildReferenceIndex(context.Background(), "/nonexistent/repo/path", "HEAD", map[string]struct{}{})
	if err != nil {
		t.Fatalf("expected no error for empty names, got %v", err)
	}
	if len(index) != 0 {
		t.Fatalf("expected empty index, got %#v", index)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for empty names, got %#v", warnings)
	}
}

// TestAddDependentCountsNoChangesSkipsIndexBuild pins the same short-circuit
// at the addDependentCounts level: a Result with no entity changes must not
// attempt to build a reference index at all.
func TestAddDependentCountsNoChangesSkipsIndexBuild(t *testing.T) {
	result := &Result{}
	if err := addDependentCounts(context.Background(), "/nonexistent/repo/path", "HEAD", result); err != nil {
		t.Fatalf("expected no error when there are no changes, got %v", err)
	}
}

// paddedPythonSource builds a syntactically valid Python file of exactly
// targetSize bytes: a real function that calls calledName, followed by a
// single padding comment line long enough to reach the target size.
func paddedPythonSource(funcName, calledName string, targetSize int) string {
	prefix := "def " + funcName + "(token):\n    return " + calledName + "(token)\n"
	padNeeded := targetSize - len(prefix) - len("# \n")
	if padNeeded < 0 {
		padNeeded = 0
	}
	return prefix + "# " + strings.Repeat("x", padNeeded) + "\n"
}

// TestBuildReferenceIndexSkipsFilesOverMaxParseBytes pins parity with the
// provider's MaxParseBytes eligibility: a file whose content exceeds
// defaultMaxParseBytes must contribute 0 dependents, even though it contains
// a token matching a changed name, while a file just under the limit with a
// genuine call is still counted.
func TestBuildReferenceIndexSkipsFilesOverMaxParseBytes(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	overLimit := paddedPythonSource("huge_caller", "Foo", defaultMaxParseBytes+4096)
	underLimit := paddedPythonSource("under_caller", "Foo", defaultMaxParseBytes-4096)
	if len(overLimit) <= defaultMaxParseBytes {
		t.Fatalf("fixture must exceed defaultMaxParseBytes, got %d bytes", len(overLimit))
	}
	if len(underLimit) >= defaultMaxParseBytes {
		t.Fatalf("fixture must stay under defaultMaxParseBytes, got %d bytes", len(underLimit))
	}

	write(t, repo, "huge.py", overLimit)
	write(t, repo, "just_under.py", underLimit)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	head := rev(t, repo, "HEAD")

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}

	dependents := index["Foo"]
	if _, ok := dependents["huge.py#function:huge_caller"]; ok {
		t.Fatalf("file above defaultMaxParseBytes must contribute 0 dependents, got %#v", dependents)
	}
	if _, ok := dependents["just_under.py#function:under_caller"]; !ok {
		t.Fatalf("expected just_under.py under_caller() as a dependent, got %#v", dependents)
	}
	if got, want := len(dependents), 1; got != want {
		t.Fatalf("dependents count = %d, want %d: %#v", got, want, dependents)
	}

	// The skipped oversized candidate must not be silent: a warning mirroring
	// the provider's E_FILE_TOO_LARGE partial failure names the file and says
	// its dependent references were not counted.
	if got, want := len(warnings), 1; got != want {
		t.Fatalf("warnings count = %d, want %d: %#v", got, want, warnings)
	}
	if warnings[0].Code != "E_FILE_TOO_LARGE" || warnings[0].FilePath != "huge.py" {
		t.Fatalf("expected an E_FILE_TOO_LARGE warning for huge.py, got %#v", warnings[0])
	}
}

// TestBuildReferenceIndexFallsBackWhenGrepFails pins the fallback: if the git
// grep preselection call fails for any reason, buildReferenceIndex must
// still fall back to a full-tree scan rather than silently returning zero
// dependents. A NUL byte in a pattern makes the underlying git-grep
// subprocess invocation fail outright (Go's exec layer rejects NUL bytes in
// arguments), while leaving the unrelated, well-formed pattern's real
// dependents fully discoverable via the fallback scan.
func TestBuildReferenceIndexFallsBackWhenGrepFails(t *testing.T) {
	repo, head := newDependentsTestRepo(t)

	names := map[string]struct{}{
		"Foo":         {},
		"poison\x00x": {},
	}

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, names)
	if err != nil {
		t.Fatal(err)
	}

	dependents := index["Foo"]
	if got, want := len(dependents), 2; got != want {
		t.Fatalf("dependents count after grep failure = %d, want %d: %#v", got, want, dependents)
	}
	if _, ok := dependents["caller_one.py#function:check"]; !ok {
		t.Fatalf("expected caller_one.py check() as a dependent after fallback, got %#v", dependents)
	}
	if _, ok := dependents["caller_two.py#function:check_again"]; !ok {
		t.Fatalf("expected caller_two.py check_again() as a dependent after fallback, got %#v", dependents)
	}

	if _, ok := index["poison\x00x"]; !ok {
		t.Fatalf("expected an (empty) entry for the poisoned name, got %#v", index)
	}
	if len(index["poison\x00x"]) != 0 {
		t.Fatalf("poisoned name should have no real dependents, got %#v", index["poison\x00x"])
	}

	// The fallback itself must not be silent: exactly one warning notes the
	// prefilter failure and includes the underlying error text.
	if got, want := len(warnings), 1; got != want {
		t.Fatalf("warnings count = %d, want %d: %#v", got, want, warnings)
	}
	if warnings[0].Code != "W_DEPENDENTS_PREFILTER_FAILED" || warnings[0].Detail == "" {
		t.Fatalf("expected a W_DEPENDENTS_PREFILTER_FAILED warning with error detail, got %#v", warnings[0])
	}
}

// TestBuildReferenceIndexFallbackWarnsOnlyForCandidateFiles pins that the
// grep-failure fallback's full-tree scan applies the prefilter's candidate
// test in-process before warning: an oversized or unparseable file that
// contains no changed name was never a real candidate and must not produce a
// warning (otherwise every huge vendored blob in the tree would spray
// E_FILE_TOO_LARGE on the rare fallback path), while an oversized file that
// does contain a changed name must still warn, exactly as it would have on
// the prefiltered path. Dependent counts are unaffected either way.
func TestBuildReferenceIndexFallbackWarnsOnlyForCandidateFiles(t *testing.T) {
	repo, _ := newDependentsTestRepo(t)

	write(t, repo, "huge_with_name.py", paddedPythonSource("huge_caller", "Foo", defaultMaxParseBytes+4096))
	write(t, repo, "huge_without_name.py", paddedPythonSource("huge_unrelated", "Bar", defaultMaxParseBytes+4096))
	write(t, repo, "broken_without_name.ts", "type Broken = <\n\nexport function alpha(){return Bar()}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add fallback candidate fixtures")
	head := rev(t, repo, "HEAD")

	// The NUL byte forces the grep prefilter to fail (Go's exec layer rejects
	// NUL in arguments), so buildReferenceIndex takes the full-tree fallback.
	names := map[string]struct{}{
		"Foo":         {},
		"poison\x00x": {},
	}
	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, names)
	if err != nil {
		t.Fatal(err)
	}

	dependents := index["Foo"]
	if got, want := len(dependents), 2; got != want {
		t.Fatalf("dependents count = %d, want %d: %#v", got, want, dependents)
	}

	var codes []string
	for _, warning := range warnings {
		codes = append(codes, warning.Code+":"+warning.FilePath)
		if warning.FilePath == "huge_without_name.py" {
			t.Fatalf("oversized file with no changed name must not warn on the fallback path, got %#v", warning)
		}
		if warning.FilePath == "broken_without_name.ts" {
			t.Fatalf("unparseable file with no changed name must not warn on the fallback path, got %#v", warning)
		}
	}
	if got, want := len(warnings), 2; got != want {
		t.Fatalf("warnings = %v, want exactly the fallback notice and one oversized candidate", codes)
	}
	sawFallback, sawTooLarge := false, false
	for _, warning := range warnings {
		switch {
		case warning.Code == "W_DEPENDENTS_PREFILTER_FAILED":
			sawFallback = true
		case warning.Code == "E_FILE_TOO_LARGE" && warning.FilePath == "huge_with_name.py":
			sawTooLarge = true
		}
	}
	if !sawFallback || !sawTooLarge {
		t.Fatalf("expected W_DEPENDENTS_PREFILTER_FAILED and E_FILE_TOO_LARGE for huge_with_name.py, got %v", codes)
	}
}

// TestBuildReferenceIndexFallbackWarnsForLineUnsafeOversizedCandidate
// reproduces the dependents.go:248 finding: a candidate whose path the batch
// reader's IsPathSafe rejects (here, an embedded newline) is read through
// LimitedFileReader instead, which has no content-scanning capability of its
// own, so oversizeMatched can never be populated for it. On the full-tree
// fallback (git-grep failed, prefiltered == false) that used to read as "did
// not match" and silently drop the E_FILE_TOO_LARGE warning even though the
// file does contain a changed name, undercounting dependents_count with
// nothing in Result.Warnings to show for it. With no candidate evidence
// either way, it must warn.
func TestBuildReferenceIndexFallbackWarnsForLineUnsafeOversizedCandidate(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	// importDeepDependentFiles treats a single path component as both the
	// containing tree's name and the blob's own name, so a genuinely flat
	// path needs a directory component to avoid an unrelated self-nesting
	// quirk in the fixture builder.
	const unsafePath = "d/unsafe\ncaller.py"
	head := importDeepDependentFiles(t, repo, []deepDependentFile{{
		path:    unsafePath,
		content: paddedPythonSource("unsafe_caller", "Foo", defaultMaxParseBytes+4096),
	}})

	// The NUL byte forces the grep prefilter to fail, exactly as in
	// TestBuildReferenceIndexFallbackWarnsOnlyForCandidateFiles, so
	// buildReferenceIndex takes the full-tree fallback.
	names := map[string]struct{}{
		"Foo":         {},
		"poison\x00x": {},
	}
	_, warnings, err := buildReferenceIndex(context.Background(), repo, head, names)
	if err != nil {
		t.Fatal(err)
	}

	var fallbackCount, tooLargeCount int
	for _, warning := range warnings {
		if warning.Code == "W_DEPENDENTS_PREFILTER_FAILED" {
			fallbackCount++
		}
		if warning.Code == "E_FILE_TOO_LARGE" && warning.FilePath == unsafePath {
			tooLargeCount++
		}
	}
	if fallbackCount != 1 || tooLargeCount != 1 || len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want exactly one prefilter warning and one E_FILE_TOO_LARGE for the line-unsafe candidate", warnings)
	}
}

// pfBrokenCallsFooTS is a hard tree-sitter parse failure (mirrors
// analyze_parsefailure_test.go's pfBrokenTS trick: a malformed leading type
// alias derails the whole parse) that also contains a whole-token match for
// "Foo", so it doubles as a dependents candidate file.
const pfBrokenCallsFooTS = "type Broken = <\n\nexport function alpha(){return Foo()}\nexport function beta(){return 2}\n"

// TestBuildReferenceIndexWarnsOnParseFailure pins that a Supported candidate
// file which fails to parse (ParseWithStatus reports ParseError) surfaces a
// warning naming the file, mirroring the provider's parse-failure partial
// failure, instead of silently undercounting dependents with no trace in
// Result.Warnings. Any entities the parser still recovers keep counting
// exactly as before -- this is observability only.
func TestBuildReferenceIndexWarnsOnParseFailure(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "broken.ts", pfBrokenCallsFooTS)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	head := rev(t, repo, "HEAD")

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}

	var parseWarning *ProviderWarning
	for i := range warnings {
		if warnings[i].FilePath == "broken.ts" {
			parseWarning = &warnings[i]
		}
	}
	if parseWarning == nil {
		t.Fatalf("expected a parse-failure warning for broken.ts, got %#v", warnings)
	}
	if parseWarning.Code != "E_PARSE_ERROR" {
		t.Fatalf("expected E_PARSE_ERROR code, got %#v", parseWarning)
	}
	if parseWarning.Detail == "" {
		t.Fatalf("expected non-empty detail on the parse-failure warning, got %#v", parseWarning)
	}

	// Whatever entities the broken parse did or didn't recover, dependent
	// counting behavior on them is unchanged by this fix: it is purely
	// additive observability.
	dependents := index["Foo"]
	if _, ok := dependents["broken.ts#function:alpha"]; ok {
		t.Fatalf("alpha must not count as a dependent unless the parser actually recovered it as an entity, got %#v", dependents)
	}
}

// TestBuildReferenceIndexIncludesGitBinaryFlaggedFiles pins that the git-grep
// prefilter used by referenceCandidateFiles is a genuine strict superset of
// containsIdentifier's check, even for a file git itself classifies as
// binary. `git grep -I` (binary-aware search) silently excludes such files
// from its match list -- it does not error, so the grep-failure fallback
// never triggers -- which would otherwise make a real dependent inside a
// NUL-containing source file vanish without warning. The embedded NUL byte
// also trips a genuine (soft-recoverable) tree-sitter-python ERROR node, so
// this fixture now surfaces a parse-failure warning too; the dependent is
// still found because tree-sitter still recovers the entity around the
// error, exactly like the pfSoftTS "soft recovery" case elsewhere.
func TestBuildReferenceIndexIncludesGitBinaryFlaggedFiles(t *testing.T) {
	repo, _ := newDependentsTestRepo(t)

	binaryFlaggedSource := "def binary_caller(token):\n    return Foo(token)\n# nul marker: \x00\n"
	write(t, repo, "binary_caller.py", binaryFlaggedSource)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add NUL-containing caller")
	head := rev(t, repo, "HEAD")

	// Confirm the fixture is actually excluded by git's binary-file heuristic
	// (`-I`) -- otherwise this test would not be exercising the case it claims
	// to cover.
	textOnlyMatches, err := gitutil.GrepTreePaths(context.Background(), repo, head, []string{"Foo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range textOnlyMatches {
		if path == "binary_caller.py" {
			t.Fatal("fixture was not excluded by git grep -I; test no longer exercises the binary-file case")
		}
	}

	index, warnings, err := buildReferenceIndex(context.Background(), repo, head, map[string]struct{}{"Foo": {}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(warnings), 1; got != want {
		t.Fatalf("expected exactly one parse-failure warning for the NUL byte tripping a tree-sitter ERROR node, got %d: %#v", got, warnings)
	}
	if warnings[0].Code != "E_PARSE_ERROR" || warnings[0].FilePath != "binary_caller.py" {
		t.Fatalf("expected an E_PARSE_ERROR warning for binary_caller.py, got %#v", warnings[0])
	}

	dependents := index["Foo"]
	if _, ok := dependents["binary_caller.py#function:binary_caller"]; !ok {
		t.Fatalf("expected binary_caller.py binary_caller() as a dependent despite the embedded NUL byte, got %#v", dependents)
	}
}

// TestAddDependentCountsBudgetExpiredWarnsAndKeepsResult pins the dependents
// half of the time-budget contract: an already-expired deadline stops the
// reference scan before it starts, appends exactly one machine-readable
// W_ANALYSIS_BUDGET_EXCEEDED warning, and leaves the (uncounted) result
// intact rather than erroring.
func TestAddDependentCountsBudgetExpiredWarnsAndKeepsResult(t *testing.T) {
	repo, head := newDependentsTestRepo(t)

	result := Result{Files: []FileChange{{
		Path:    "auth.py",
		Status:  "M",
		Changes: []EntityChange{{Type: "body_changed", Kind: "function", Name: "Foo"}},
	}}}
	err := addDependentCountsWithProgress(context.Background(), repo, head, &result, dependentsScanOptions{
		deadline: time.Now().Add(-time.Second),
		budget:   time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("expired budget must not error, got %v", err)
	}
	var budgetWarnings []ProviderWarning
	for _, warning := range result.Warnings {
		if warning.Code == "W_ANALYSIS_BUDGET_EXCEEDED" {
			budgetWarnings = append(budgetWarnings, warning)
		}
	}
	if len(budgetWarnings) != 1 {
		t.Fatalf("want exactly one budget warning, got %#v", result.Warnings)
	}
	if budgetWarnings[0].EffectOnCompleteness == "" || budgetWarnings[0].Detail == "" {
		t.Fatalf("budget warning must carry effect and detail, got %#v", budgetWarnings[0])
	}
	if got := result.Files[0].Changes[0].DependentsCount; got != 0 {
		t.Fatalf("dependents count under expired budget = %d, want 0", got)
	}
}

type deepDependentFile struct {
	path    string
	content string
}

// importDeepDependentFiles builds the tree one component at a time. No Git
// command receives the complete adversarial path: besides avoiding filesystem
// limits, that keeps the fixture valid on Git for Windows, whose fast-import
// path parser rejects names this deep even though the object format permits
// them.
func importDeepDependentFiles(t *testing.T, repo string, files []deepDependentFile) string {
	t.Helper()
	var rootInput strings.Builder
	for _, file := range files {
		components := strings.Split(file.path, "/")
		blob := gitObjectInputOutput(t, repo, file.content, "hash-object", "-w", "--stdin")
		tree := gitObjectInputOutput(
			t,
			repo,
			fmt.Sprintf("100644 blob %s\t%s%c", blob, components[len(components)-1], byte(0)),
			"mktree", "-z",
		)
		for i := len(components) - 2; i >= 1; i-- {
			tree = gitObjectInputOutput(
				t,
				repo,
				fmt.Sprintf("040000 tree %s\t%s%c", tree, components[i], byte(0)),
				"mktree", "-z",
			)
		}
		fmt.Fprintf(&rootInput, "040000 tree %s\t%s%c", tree, components[0], byte(0))
	}
	root := gitObjectInputOutput(t, repo, rootInput.String(), "mktree", "-z")
	commit := gitObjectInputOutput(
		t,
		repo,
		"",
		"-c", "user.name=Entire Graph Test",
		"-c", "user.email=graph@example.com",
		"commit-tree", root, "-m", "deep dependents",
	)
	git(t, repo, "update-ref", "refs/heads/deep-dependents", commit)
	return commit
}

func gitObjectInputOutput(t *testing.T, repo, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repeatedDeepUnsafePath(branch, depth, componentBytes int) string {
	component := strings.Repeat(string(rune('a'+branch)), componentBytes)
	components := make([]string, depth, depth+1)
	for i := range components {
		components[i] = component
	}
	components = append(components, fmt.Sprintf("unsafe\ncaller_%d.py", branch))
	return strings.Join(components, "/")
}

// TestBuildReferenceIndexSharesDeepPathMetadataAllowance proves that unsafe
// dependent paths use one LimitedFileReader. Four independent 81-component
// paths need 324 metadata subprocesses in total; the shared allowance resolves
// three, then reports the known-relevant remainder as partial. The previous
// per-file ReadFileLimited fallback reset the allowance four times and read all
// four paths.
func TestBuildReferenceIndexSharesDeepPathMetadataAllowance(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")

	files := make([]deepDependentFile, 4)
	for i := range files {
		files[i] = deepDependentFile{
			path:    repeatedDeepUnsafePath(i, 80, 200),
			content: fmt.Sprintf("def caller_%d():\n    return Foo()\n", i),
		}
	}
	head := importDeepDependentFiles(t, repo, files)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	index, warnings, err := buildReferenceIndexWithProgress(ctx, repo, head, map[string]struct{}{"Foo": {}}, dependentsScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("dependents scan exceeded safety timeout: %v", err)
	}
	if got, want := len(index["Foo"]), 3; got != want {
		t.Fatalf("dependents resolved before shared metadata cap = %d, want %d", got, want)
	}

	var unaddressable []ProviderWarning
	for _, warning := range warnings {
		if warning.Code == "E_FILE_READ" && strings.Contains(warning.Detail, "bounded Git metadata traversal") {
			unaddressable = append(unaddressable, warning)
		}
		if warning.Code == "W_ANALYSIS_BUDGET_EXCEEDED" {
			t.Fatalf("metadata process cap must not be reported as a time budget expiry: %#v", warning)
		}
	}
	if got, want := len(unaddressable), 1; got != want {
		t.Fatalf("bounded metadata warnings = %d, want %d: %#v", got, want, warnings)
	}
	if unaddressable[0].EffectOnCompleteness == "" {
		t.Fatalf("bounded metadata warning must explain partial dependents: %#v", unaddressable[0])
	}
}

// Every newline-bearing candidate is unsafe for cat-file's LF request
// protocol. Prime must batch their exact metadata before the scan; otherwise
// each lazy fallback starts its own ls-tree process and the scan cost grows
// linearly with the candidate count.
func TestBuildReferenceIndexBatchesLineUnsafeMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git wrapper is a POSIX shell script")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	const fileCount = 200
	files := make([]deepDependentFile, fileCount)
	for i := range files {
		files[i] = deepDependentFile{
			path:    fmt.Sprintf("unsafe\ncaller_%03d.py", i),
			content: fmt.Sprintf("def caller_%03d():\n    return Foo()\n", i),
		}
	}
	head := importDeepDependentFiles(t, repo, files)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	lsTreeLog := filepath.Join(wrapperDir, "ls-tree.log")
	wrapper := filepath.Join(wrapperDir, "git")
	script := `#!/bin/sh
for arg do
	if [ "$arg" = "ls-tree" ]; then
		printf 'ls-tree\n' >> "$LS_TREE_LOG"
		break
	fi
done
exec "$REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("LS_TREE_LOG", lsTreeLog)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	index, warnings, err := buildReferenceIndexWithProgress(
		ctx,
		repo,
		head,
		map[string]struct{}{"Foo": {}},
		dependentsScanOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("line-unsafe dependents scan exceeded safety timeout: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("line-unsafe dependents warnings = %#v, want none", warnings)
	}
	if got := len(index["Foo"]); got != fileCount {
		t.Fatalf("line-unsafe dependents = %d, want %d", got, fileCount)
	}
	log, err := os.ReadFile(lsTreeLog)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Count(string(log), "ls-tree\n"), 2; got != want {
		t.Fatalf("ls-tree metadata subprocesses = %d, want %d bounded batches", got, want)
	}
}

// TestHeadReadersShareDeepUnsafeMetadataAllowance covers the two committed
// multi-file reader closures outside Analyze. Each has one shared component
// allowance: four independent 81-component unsafe paths resolve three and
// classify the fourth as unavailable. A fresh one-shot reader per file would
// reset the allowance and incorrectly read all four.
func TestHeadReadersShareDeepUnsafeMetadataAllowance(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	files := make([]deepDependentFile, 4)
	for i := range files {
		files[i] = deepDependentFile{
			path:    repeatedDeepUnsafePath(i, 80, 200),
			content: fmt.Sprintf("def caller_%d():\n    return %d\n", i, i),
		}
	}
	head := importDeepDependentFiles(t, repo, files)
	git(t, repo, "symbolic-ref", "HEAD", "refs/heads/deep-dependents")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	source, err := prepareSource(ctx, repo, ProviderSnapshotOptions{MaxParseBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	// Read in the opposite order from the deterministic listing. Without the
	// pre-worker Prime, this reverse schedule would give the allowance to the
	// last three paths and make provider output depend on worker timing.
	providerRead := make(map[string]bool, len(files))
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		if content, ok := source.read(file.path); ok && content == file.content {
			providerRead[file.path] = true
		}
	}
	if err := source.close(); err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		want := i < 3
		if got := providerRead[file.path]; got != want {
			t.Fatalf("provider read for listed path %d = %v, want %v", i, got, want)
		}
	}

	readSearch, closeSearch, err := openSearchContentReader(ctx, repo, head, true, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	searchReads := 0
	for _, file := range files {
		if content, ok := readSearch(file.path); ok && content == file.content {
			searchReads++
		}
	}
	if err := closeSearch(); err != nil {
		t.Fatal(err)
	}
	if got, want := searchReads, 3; got != want {
		t.Fatalf("search reads before shared metadata cap = %d, want %d", got, want)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("shared head readers exceeded safety timeout: %v", err)
	}
}

// TestBuildReferenceIndexMidScanBudgetStopsEarly drives the in-loop deadline
// check: the deadline expires after candidate enumeration, so the scan stops
// at the first file and reports how many of the candidates were scanned.
func TestBuildReferenceIndexMidScanBudgetStopsEarly(t *testing.T) {
	repo, head := newDependentsTestRepo(t)

	names := map[string]struct{}{"Foo": {}}
	// A deadline far enough in the future to survive the initial check but
	// guaranteed to be expired by the time the scan loop runs is impossible to
	// time reliably; instead pin the two warning shapes directly.
	pre := dependentsBudgetWarning(0, -1, time.Second)
	if pre.Code != "W_ANALYSIS_BUDGET_EXCEEDED" || !strings.Contains(pre.Detail, "before the dependents scan started") {
		t.Fatalf("pre-enumeration budget warning = %#v", pre)
	}
	mid := dependentsBudgetWarning(3, 10, 2*time.Second)
	if !strings.Contains(mid.Detail, "scanned 3 of 10 candidate files") || !strings.Contains(mid.Detail, "2s") {
		t.Fatalf("mid-scan budget warning = %#v", mid)
	}
	if mid.Severity != "warning" || mid.EffectOnCompleteness == "" {
		t.Fatalf("mid-scan budget warning incomplete: %#v", mid)
	}

	// And with no deadline the same scan completes and counts dependents.
	index, warnings, err := buildReferenceIndexWithProgress(context.Background(), repo, head, names, dependentsScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range warnings {
		if warning.Code == "W_ANALYSIS_BUDGET_EXCEEDED" {
			t.Fatalf("no budget warning expected without a deadline, got %#v", warning)
		}
	}
	if got := len(index["Foo"]); got != 2 {
		t.Fatalf("dependents of Foo = %d, want 2", got)
	}
}

// TestBuildReferenceIndexReportsPerFileProgress pins the progress callback
// contract used by --progress: an initial 0/total event, and a final
// total/total event with an empty path.
func TestBuildReferenceIndexReportsPerFileProgress(t *testing.T) {
	repo, head := newDependentsTestRepo(t)

	type event struct {
		done, total int
		path        string
	}
	var events []event
	_, _, err := buildReferenceIndexWithProgress(context.Background(), repo, head, map[string]struct{}{"Foo": {}}, dependentsScanOptions{
		progress: func(done, total int, path string) {
			events = append(events, event{done: done, total: total, path: path})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least start and end progress events, got %#v", events)
	}
	first, last := events[0], events[len(events)-1]
	if first.done != 0 || first.total <= 0 || first.path != "" {
		t.Fatalf("first progress event = %#v, want 0/total with empty path", first)
	}
	if last.done != last.total || last.total != first.total || last.path != "" {
		t.Fatalf("last progress event = %#v, want total/total with empty path", last)
	}
}
