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
)

func TestAnalyzeGitRange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)

def format_date(value):
    return str(value)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	if len(result.Files[0].Changes) != 2 {
		t.Fatalf("changes = %#v", result.Files[0].Changes)
	}
}

// TestAnalyzeGitRangeSetsSchemaVersion pins that every Result built by the
// diff/analyze path carries the package SchemaVersion — the field that lets a
// copy persisted into checkpoint metadata be read back knowing which schema
// it was written under.
func TestAnalyzeGitRangeSetsSchemaVersion(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q, want %q", result.SchemaVersion, SchemaVersion)
	}
}

func TestAnalyzeGitRangeAcceptsTreeObjects(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "scope/auth.py", "def validate_token(token):\n    return bool(token)\n")
	write(t, repo, "scope/use_auth.py", "def check(token):\n    return validate_token(token)\n")
	// This sibling makes a direct `baseExpression + ^{tree}` resolve the
	// wrong repository path instead of failing. Analyze must resolve the exact
	// caller expression first, then peel its immutable OID.
	write(t, repo, "scope^{tree}/auth.py", "def decoy():\n    return False\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	baseCommit := rev(t, repo, "HEAD")
	baseTree := rev(t, repo, "HEAD^{tree}")

	write(t, repo, "scope/auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "head")
	headCommit := rev(t, repo, "HEAD")
	headTree := rev(t, repo, "HEAD^{tree}")

	result, err := AnalyzeGitRange(t.Context(), repo, baseTree, headTree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Base != baseTree || result.Head != headTree {
		t.Fatalf("result labels = %q..%q, want tree labels %q..%q", result.Base, result.Head, baseTree, headTree)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "scope/auth.py" {
		t.Fatalf("tree-object diff files = %#v, want scope/auth.py", result.Files)
	}

	baseExpression := baseCommit + ":scope"
	headExpression := headCommit + ":scope"
	subtreeResult, err := AnalyzeGitRange(t.Context(), filepath.Join(repo, "scope"), baseExpression, headExpression, nil)
	if err != nil {
		t.Fatal(err)
	}
	if subtreeResult.Base != baseExpression || subtreeResult.Head != headExpression {
		t.Fatalf("result labels = %q..%q, want expressions %q..%q", subtreeResult.Base, subtreeResult.Head, baseExpression, headExpression)
	}
	if len(subtreeResult.Files) != 1 || subtreeResult.Files[0].Path != "auth.py" {
		t.Fatalf("tree-expression diff files = %#v, want auth.py", subtreeResult.Files)
	}
	foundSubtreeDependent := false
	for _, change := range subtreeResult.Files[0].Changes {
		if change.Name == "validate_token" {
			foundSubtreeDependent = true
			if change.DependentsCount != 1 {
				t.Fatalf("subdirectory subtree dependent count = %d, want 1: %#v", change.DependentsCount, change)
			}
		}
	}
	if !foundSubtreeDependent {
		t.Fatalf("subdirectory subtree diff did not report validate_token: %#v", subtreeResult.Files)
	}

	// Direct full-root tree OIDs retain the historical caller-cwd pathspec
	// semantics. Unlike commit:scope above, auth.py means scope/auth.py here.
	baseRootTree := rev(t, repo, baseCommit+"^{tree}")
	headRootTree := rev(t, repo, headCommit+"^{tree}")
	rootTreeResult, err := AnalyzeGitRange(t.Context(), filepath.Join(repo, "scope"), baseRootTree, headRootTree, []string{"auth.py"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rootTreeResult.Files) != 1 || rootTreeResult.Files[0].Path != "scope/auth.py" {
		t.Fatalf("direct root-tree diff files = %#v, want scope/auth.py", rootTreeResult.Files)
	}
}

func TestAnalyzeGitRangeReadsRootPathsFromRepoSubdirectory(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "scope/auth.py", "def validate_token(token):\n    return bool(token)\n")
	write(t, repo, "scope/use_auth.py", "def check(token):\n    return validate_token(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	base := rev(t, repo, "HEAD")
	write(t, repo, "scope/auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "head")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(t.Context(), filepath.Join(repo, "scope"), base, head, []string{"auth.py"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "scope/auth.py" {
		t.Fatalf("subdirectory diff files = %#v, want root-relative scope/auth.py", result.Files)
	}
	foundDependent := false
	for _, change := range result.Files[0].Changes {
		if change.Name == "validate_token" {
			foundDependent = true
			if change.DependentsCount != 1 {
				t.Fatalf("subdirectory dependent count = %d, want 1: %#v", change.DependentsCount, change)
			}
		}
	}
	if !foundDependent {
		t.Fatalf("subdirectory diff did not report validate_token: %#v", result.Files)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_READ" {
			t.Fatalf("root-relative changed path was misclassified as missing: %#v", warning)
		}
	}
}

func TestAnalyzeGitRangeReadsPathBeyondMetadataArgvBound(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	baseTree, path := nestedAnalyzeTree(t, repo, "package deep\n\nvar Value = 1\n", 80)
	headTree, headPath := nestedAnalyzeTree(t, repo, "package deep\n\nvar Value = 2\n", 80)
	if headPath != path || len(path) <= 15<<10 {
		t.Fatalf("deep fixture path lengths = %d/%d, want same path beyond 15 KiB", len(path), len(headPath))
	}
	result, err := AnalyzeGitRange(t.Context(), repo, baseTree, headTree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != path {
		t.Fatalf("deep-tree diff files = %#v, want exact %d-byte path", result.Files, len(path))
	}
}

func TestAnalyzeGitRangeWarnsForSingleComponentBeyondArgvBound(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	component := strings.Repeat("x", (16<<10)+1)
	makeTree := func(content string) string {
		t.Helper()
		blob := gitInput(t, repo, content, "hash-object", "-w", "--stdin")
		leaf := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", blob, byte(0)), "mktree", "-z")
		return gitInput(t, repo, fmt.Sprintf("040000 tree %s\t%s%c", leaf, component, byte(0)), "mktree", "-z")
	}
	result, err := AnalyzeGitRange(t.Context(), repo, makeTree("package p\nvar Value = 1\n"), makeTree("package p\nvar Value = 2\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("unaddressable path produced semantic changes: %#v", result.Files)
	}
	found := false
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_READ" && strings.Contains(warning.Detail, "bounded Git metadata traversal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing bounded unaddressable warning: %#v", result.Warnings)
	}
}

func TestAnalyzeGitRangeDependentsAvoidLineProtocolPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain newlines or carriage returns")
	}
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	write(t, repo, "a\nunsafe.py", "def first(token):\n    return validate_token(token)\n")
	// This path is also returned by git grep, but its trailing CR makes it an
	// unsupported parser path. It must never enter the line-framed reader.
	write(t, repo, "b.py\r", "def second(token):\n    return validate_token(token)\n")
	const oversizeUnsafePath = "c\nover.py"
	write(t, repo, oversizeUnsafePath, "validate_token\n"+strings.Repeat("#", defaultMaxParseBytes))
	write(t, repo, "z_plain.py", "def third(token):\n    return validate_token(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	base := rev(t, repo, "HEAD")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "head")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(t.Context(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundOversizeWarning := false
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_TOO_LARGE" && warning.FilePath == oversizeUnsafePath {
			foundOversizeWarning = true
		}
	}
	if !foundOversizeWarning {
		t.Fatalf("unsafe oversized dependent was not reported: %#v", result.Warnings)
	}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Name == "validate_token" && change.DependentsCount != 2 {
				t.Fatalf("line-safe dependent count = %d, want newline + ordinary candidates: %#v", change.DependentsCount, change)
			}
		}
	}
}

func TestResolveDiffTreesReusesResolutionForSameLabel(t *testing.T) {
	var revisions []string
	resolve := func(_ context.Context, _, revision string) (string, error) {
		revisions = append(revisions, revision)
		switch revision {
		case "moving":
			return "moving-object", nil
		case "moving-object^{tree}":
			return "old-tree", nil
		default:
			return "new-tree", nil
		}
	}
	base, head, rootRelative, err := resolveDiffTrees(t.Context(), "repo", "moving", "moving", resolve)
	if err != nil {
		t.Fatal(err)
	}
	// The moving label is still resolved exactly once; the two follow-ups both
	// address the immutable object it resolved to, never the label again.
	if len(revisions) != 3 ||
		revisions[0] != "moving" ||
		revisions[1] != "moving-object^{tree}" ||
		revisions[2] != "moving-object^{commit}" {
		t.Fatalf("same ref resolutions = %#v, want exact label then immutable tree and commit peels", revisions)
	}
	if !rootRelative {
		t.Fatal("a commit-ish label must report repository-root relative names")
	}
	if base != "old-tree" || head != "old-tree" {
		t.Fatalf("same ref pinned to %q..%q, want old-tree..old-tree", base, head)
	}
}

// TestResolveDiffTreesRejectsTreePathLabels pins that resolving to a commit is
// not on its own evidence that a label named one. A gitlink resolves to the
// submodule's commit, and a range over its tree is named relative to the
// SUBMODULE's root, so this repository's exclusion rules do not describe those
// names. The probe happens to fail today only because a submodule's objects are
// usually absent from the superproject; an absorbed or fetched submodule puts
// them there, and this must not depend on that.
func TestResolveDiffTreesRejectsTreePathLabels(t *testing.T) {
	// Mimics a superproject that DOES hold the submodule's objects: every peel
	// resolves cleanly, so only the label's shape can rule it out.
	resolve := func(_ context.Context, _, revision string) (string, error) {
		return "gitlink-commit", nil
	}
	_, _, rootRelative, err := resolveDiffTrees(t.Context(), "repo", "HEAD:sub", "HEAD:sub", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if rootRelative {
		t.Fatal("a gitlink label must not be treated as repository-root relative")
	}

	// The commit-message search is the one colon form that names a commit and
	// reaches into no tree.
	_, _, rootRelative, err = resolveDiffTrees(t.Context(), "repo", ":/fix the bug", ":/fix the bug", resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !rootRelative {
		t.Fatal(":/text names a commit, so its names are repository-root relative")
	}
}

// TestLabelSelectsTreePath pins which colons are the <rev>:<path> separator and
// which are data. Rejecting a colon that is not that separator does not lose
// data, but it silently disables every exclusion rule for the range, so a
// reflog date selector must not be mistaken for a subtree expression.
func TestLabelSelectsTreePath(t *testing.T) {
	tests := []struct {
		label string
		want  bool
	}{
		{"HEAD", false},
		{"main", false},
		{"HEAD~1", false},
		{"refs/tags/v1.0.0", false},
		{"HEAD@{2}", false},
		// A reflog date selector: commit-ish, and full of colons.
		{"HEAD@{2026-08-27 12:34:56 +0000}", false},
		{"main@{yesterday 09:00:00}", false},
		// The commit-message searches name a commit; colons in them are text.
		{":/fix: the bug", false},
		{"HEAD^{/release: fix}", false},
		{"main^{/colon: here}~2", false},
		// The ordinary peels carry no colon but must stay revision-shaped.
		{"HEAD^{tree}", false},
		{"HEAD^{commit}", false},
		// These do reach into a tree.
		{"HEAD:sub", true},
		{"HEAD~1:sub/dir", true},
		{":0:conflicted.go", true},
		{"HEAD@{2}:sub", true},
		{"HEAD^{/release fix}:sub", true},
	}
	for _, test := range tests {
		if got := labelSelectsTreePath(test.label); got != test.want {
			t.Errorf("labelSelectsTreePath(%q) = %v, want %v", test.label, got, test.want)
		}
	}
}

func TestAnalyzeGitRangeSurfacesModuleScopeChange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	// Only module-scope code changes: a trailing top-level comment is added
	// while validate_token stays byte-for-byte identical. Without module-scope
	// attribution this diff would collapse to an empty (null) files list.
	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)


# module-level configuration note
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "module-scope edit")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	file := result.Files[0]
	if file.Path != "auth.py" {
		t.Fatalf("path = %q", file.Path)
	}
	if len(file.Changes) != 1 {
		t.Fatalf("changes = %#v", file.Changes)
	}
	change := file.Changes[0]
	if change.Kind != moduleKind {
		t.Fatalf("kind = %q, want %q", change.Kind, moduleKind)
	}
	if change.Type != "body_changed" {
		t.Fatalf("type = %q, want body_changed", change.Type)
	}
	if change.Name != "auth.py" {
		t.Fatalf("name = %q, want auth.py", change.Name)
	}
}

func TestAnalyzeGitRangeReportsProgress(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head := rev(t, repo, "HEAD")

	var phases []string
	_, err := AnalyzeGitRangeWithOptions(t.Context(), repo, base, head, nil, AnalyzeOptions{
		Progress: func(event AnalyzeProgressEvent) {
			phases = append(phases, event.Phase)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"discover", "parse", "reconcile", "dependents", "complete"} {
		if !containsPhase(phases, want) {
			t.Fatalf("progress phases %v missing %q", phases, want)
		}
	}
}

func TestAnalyzeGitRangePinsSymbolicRefsBeforeDiscoveryAndReads(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	baseCommit := rev(t, repo, "HEAD")
	git(t, repo, "branch", "base-view", baseCommit)

	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first head")
	firstHead := rev(t, repo, "HEAD")
	git(t, repo, "branch", "moving-head", firstHead)

	write(t, repo, "auth.py", "def validate_token(token, audience=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "advanced head")
	advancedHead := rev(t, repo, "HEAD")

	advanced := false
	result, err := AnalyzeGitRangeWithOptions(t.Context(), repo, "base-view", "moving-head", nil, AnalyzeOptions{
		Progress: func(event AnalyzeProgressEvent) {
			// The parse boundary is emitted after ChangedFiles. Moving the ref here
			// deterministically exercises every content and dependent read that
			// follows discovery.
			if event.Phase == "parse" && event.FilesDone == 0 && !advanced {
				git(t, repo, "update-ref", "refs/heads/moving-head", advancedHead, firstHead)
				advanced = true
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Fatal("test did not advance the symbolic head after discovery")
	}
	if result.Base != "base-view" || result.Head != "moving-head" {
		t.Fatalf("result labels = %q..%q, want caller labels base-view..moving-head", result.Base, result.Head)
	}

	var signatureChange *EntityChange
	for fileIndex := range result.Files {
		for changeIndex := range result.Files[fileIndex].Changes {
			change := &result.Files[fileIndex].Changes[changeIndex]
			if change.Name == "validate_token" && change.Type == "signature_changed" {
				signatureChange = change
			}
		}
	}
	if signatureChange == nil {
		t.Fatalf("changes = %#v, want validate_token signature change", result.Files)
	}
	if !strings.Contains(signatureChange.NewSignature, "issuer") || strings.Contains(signatureChange.NewSignature, "audience") {
		t.Fatalf("new signature = %q, want initially pinned head containing issuer, not advanced head containing audience", signatureChange.NewSignature)
	}
}

func containsPhase(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAnalyzeGitRangeReconcilesCrossFileMove(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "util.py", `def transform(value):
    return value * 2


def keep(value):
    return value
`)
	write(t, repo, "helpers.py", `def helper(value):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	// Move transform from util.py to helpers.py with an identical body.
	write(t, repo, "util.py", `def keep(value):
    return value
`)
	write(t, repo, "helpers.py", `def helper(value):
    return value


def transform(value):
    return value * 2
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "move transform")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	var moved *EntityChange
	for fi := range result.Files {
		for ci := range result.Files[fi].Changes {
			change := &result.Files[fi].Changes[ci]
			if change.Type == "moved" {
				moved = change
			}
			if change.Type == "removed" && change.Name == "transform" {
				t.Fatalf("transform reported as removed instead of moved: %#v", result.Files)
			}
			if change.Type == "added" && change.Name == "transform" {
				t.Fatalf("transform reported as added instead of moved: %#v", result.Files)
			}
		}
	}
	if moved == nil {
		t.Fatalf("no moved change in %#v", result.Files)
	}
	if moved.Name != "transform" || moved.Reconciliation != "MOVED" {
		t.Fatalf("moved change = %#v", moved)
	}
	if moved.OldPath != "util.py" || moved.NewPath != "helpers.py" {
		t.Fatalf("moved paths = %q -> %q", moved.OldPath, moved.NewPath)
	}
	if moved.Similarity < moveThreshold {
		t.Fatalf("moved similarity = %v", moved.Similarity)
	}
}

func TestAnalyzeGitRangeDependentCounts(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	write(t, repo, "use_auth.py", `def check(token):
    return validate_token(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Name == "validate_token" && change.DependentsCount != 1 {
				t.Fatalf("dependents = %d, want 1 in %#v", change.DependentsCount, change)
			}
		}
	}
}

func TestAnalyzeGitRangeExpandedLanguageSignatureChange(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "User.java", `class User {
    boolean validate(String token) { return true; }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "User.java", `class User {
    boolean validate(String token, String issuer) { return true; }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "semantic change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range result.Files {
		if file.Path != "User.java" || file.Language != "Java" {
			continue
		}
		for _, change := range file.Changes {
			if change.Type == "signature_changed" && change.Kind == "method" && change.Name == "User.validate" {
				return
			}
		}
	}
	t.Fatalf("missing Java method signature change in %#v", result.Files)
}

func TestAnalyzeGitRangeMatchesSurvivingOverloadByFingerprint(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "C.java", `class C {
    int F(int value) {
        return 1;
    }

    int F(String value) {
        return 2;
    }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial overloads")
	base := rev(t, repo, "HEAD")

	write(t, repo, "C.java", `class C {
    int F(Object value) {
        return 2;
    }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "remove and edit overloads")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}

	var removedInt, editedString bool
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if change.Kind != "method" || change.Name != "C.F" {
				continue
			}
			switch change.Type {
			case "removed":
				if strings.Contains(change.OldSignature, "int") {
					removedInt = true
				}
			case "signature_changed":
				if strings.Contains(change.OldSignature, "String") && strings.Contains(change.NewSignature, "Object") {
					editedString = true
				}
			case "added":
				t.Fatalf("surviving overload reported as added: %#v", change)
			}
		}
	}
	if !removedInt || !editedString {
		t.Fatalf("missing method changes removedInt=%v editedString=%v in %#v", removedInt, editedString, result.Files)
	}
}

func TestAnalyzeGitRangeIncludesGitHubWorkflowYAML(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, ".github/workflows/ci.yml", `name: CI
on:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, ".github/workflows/ci.yml", `name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test -race ./...
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "update workflow")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	file := result.Files[0]
	if file.Path != ".github/workflows/ci.yml" {
		t.Fatalf("path = %q", file.Path)
	}
	if file.Language != "YAML" {
		t.Fatalf("language = %q", file.Language)
	}

	var sawJob bool
	var sawTrigger bool
	for _, change := range file.Changes {
		if change.Type == "body_changed" && change.Kind == "job" && change.Name == "jobs.test" {
			sawJob = true
		}
		if change.Type == "body_changed" && change.Kind == "section" && change.Name == "on" {
			sawTrigger = true
		}
	}
	if !sawJob || !sawTrigger {
		t.Fatalf("workflow changes missing job=%v trigger=%v in %#v", sawJob, sawTrigger, file.Changes)
	}
}

// Regression for the jdx/mise report: a changed file with no parser support
// (a PowerShell test script there) silently disappeared from the result, so an
// empty diff was indistinguishable from "not analyzed". It must surface as a
// machine-readable skipped marker.
func TestAnalyzeGitRangeMarksUnsupportedChangedFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", `def validate_token(token):
    return bool(token)
`)
	write(t, repo, "shim.Tests.ps1", `Describe "shim" {
    It "runs" { $true | Should -BeTrue }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "auth.py", `def validate_token(token, *, issuer=None):
    return bool(token)
`)
	write(t, repo, "shim.Tests.ps1", `Describe "shim" {
    It "runs" { $false | Should -BeFalse }
}
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change both")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "auth.py" {
		t.Fatalf("files = %#v", result.Files)
	}
	var marker *ProviderWarning
	for i, w := range result.Warnings {
		if w.Code == "W_UNSUPPORTED_FILE" {
			marker = &result.Warnings[i]
		}
	}
	if marker == nil {
		t.Fatalf("missing W_UNSUPPORTED_FILE warning: %#v", result.Warnings)
	}
	if marker.FilePath != "shim.Tests.ps1" || marker.Severity != "info" {
		t.Fatalf("unexpected marker %#v", marker)
	}
}

func TestAnalyzeGitRangeMarksChangedGitlinkAsUnsupported(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")

	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	oidLength := 40
	if runGit("rev-parse", "--show-object-format") == "sha256" {
		oidLength = 64
	}
	makeGitlinkTree := func(digit string) string {
		t.Helper()
		cmd := exec.Command("git", "mktree", "-z", "--missing")
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader("160000 commit " + strings.Repeat(digit, oidLength) + "\tmodule.go\x00")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git mktree: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	baseTree := makeGitlinkTree("1")
	headTree := makeGitlinkTree("2")
	base := runGit("commit-tree", baseTree, "-m", "record first gitlink")
	head := runGit("commit-tree", headTree, "-p", base, "-m", "advance gitlink")

	result, err := AnalyzeGitRange(t.Context(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("gitlink produced semantic file changes: %#v", result.Files)
	}

	var unsupported *ProviderWarning
	for i := range result.Warnings {
		warning := &result.Warnings[i]
		if warning.Code == "E_FILE_TOO_LARGE" {
			t.Fatalf("gitlink was misclassified as oversized: %#v", warning)
		}
		if warning.Code == "E_FILE_READ" {
			t.Fatalf("gitlink was misclassified as a missing blob: %#v", warning)
		}
		if warning.Code == "W_UNSUPPORTED_FILE" && warning.FilePath == "module.go" {
			unsupported = warning
		}
	}
	if unsupported == nil {
		t.Fatalf("missing gitlink W_UNSUPPORTED_FILE warning: %#v", result.Warnings)
	}
	if unsupported.Severity != "info" || unsupported.Detail != "base and head versions are non-blob Git tree entries" {
		t.Fatalf("unexpected gitlink warning: %#v", unsupported)
	}
	if !strings.Contains(unsupported.EffectOnCompleteness, "has no blob content") {
		t.Fatalf("gitlink warning has inaccurate effect: %#v", unsupported)
	}
}

func TestAnalyzeGitRangeReportsUnreadableBlobObject(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	baseBlob := gitInput(t, repo, "package p\nvar Value = 1\n", "hash-object", "-w", "--stdin")
	missingOID := strings.Repeat("1", len(baseBlob))
	baseTree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", baseBlob, byte(0)), "mktree", "-z")
	headTree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", missingOID, byte(0)), "mktree", "-z", "--missing")

	result, err := AnalyzeGitRange(t.Context(), repo, baseTree, headTree, nil)
	if err != nil {
		t.Fatalf("analyze listed unreadable blob: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("unreadable blob produced semantic changes: %#v", result.Files)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_READ" && warning.FilePath == "file.go" &&
			warning.Detail == "head version references an unreadable Git blob object" {
			return
		}
	}
	t.Fatalf("unreadable blob had no accurate E_FILE_READ warning: %#v", result.Warnings)
}

func TestAnalyzeGitRangeRecoversFromNoncanonicalTreePath(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	baseBlob := gitInput(t, repo, "package p\nvar Value = 1\n", "hash-object", "-w", "--stdin")
	headBlob := gitInput(t, repo, "package p\nvar Value = 2\n", "hash-object", "-w", "--stdin")
	baseSubtree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", baseBlob, byte(0)), "mktree", "-z")
	headSubtree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", headBlob, byte(0)), "mktree", "-z")
	baseTree := gitInput(t, repo, fmt.Sprintf("040000 tree %s\t..%c", baseSubtree, byte(0)), "mktree", "-z")
	headTree := gitInput(t, repo, fmt.Sprintf("040000 tree %s\t..%c", headSubtree, byte(0)), "mktree", "-z")

	result, err := AnalyzeGitRange(t.Context(), repo, baseTree, headTree, nil)
	if err != nil {
		t.Fatalf("analyze noncanonical raw tree path: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("noncanonical path produced semantic changes: %#v", result.Files)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_READ" && warning.Severity == "error" &&
			warning.FilePath == "../file.go" &&
			warning.Detail == "base and head paths are not canonical Git tree paths" {
			return
		}
	}
	t.Fatalf("noncanonical path had no recoverable E_FILE_READ warning: %#v", result.Warnings)
}

func TestAnalyzeGitRangeIgnoresTreeReplacementMovedAfterDiscovery(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	baseBlob := gitInput(t, repo, "package p\nfunc Target() int { return 1 }\n", "hash-object", "-w", "--stdin")
	headBlob := gitInput(t, repo, "package p\nfunc Target(value int) int { return value }\n", "hash-object", "-w", "--stdin")
	baseTree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", baseBlob, byte(0)), "mktree", "-z")
	headTree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", headBlob, byte(0)), "mktree", "-z")
	// Preserve a hostile inherited assignment; production must append its
	// canonical raw-object value after it. The control command below removes the
	// variable entirely so Git demonstrably honors the moved replacement.
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")

	replacementMoved := false
	controlTree := ""
	result, err := AnalyzeGitRangeWithOptions(t.Context(), repo, baseTree, headTree, nil, AnalyzeOptions{
		Progress: func(event AnalyzeProgressEvent) {
			if replacementMoved || event.Phase != "parse" || event.Path != "" {
				return
			}
			replacementMoved = true
			// ChangedFiles already discovered file.go from the raw trees. Replace the
			// head tree with the base now; any later Git process that honors this ref
			// sees identical content and silently loses the signature change.
			git(t, repo, "update-ref", "refs/replace/"+headTree, baseTree)
			controlTree = gitOutputHonoringReplaceRefs(t, repo, "cat-file", "-p", headTree)
		},
	})
	if err != nil {
		t.Fatalf("analyze across moving tree replacement: %v", err)
	}
	if !replacementMoved {
		t.Fatal("fixture did not move the tree replacement after discovery")
	}
	if !strings.Contains(controlTree, baseBlob) || strings.Contains(controlTree, headBlob) {
		t.Fatalf("control Git did not observe moved tree replacement: %q", controlTree)
	}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			if file.Path == "file.go" && change.Type == "signature_changed" && change.Name == "Target" {
				return
			}
		}
	}
	t.Fatalf("raw pinned trees lost Target signature change after replacement moved: %#v", result)
}

func TestAnalyzeGitRangeKeepsShebangRoutableChangedFiles(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "bin/tool", `#!/usr/bin/env python3

def run(value):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "bin/tool", `#!/usr/bin/env python3

def run(value, strict=False):
    return value
`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change signature")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "bin/tool" || result.Files[0].Language != "Python" {
		t.Fatalf("shebang-routable file was not analyzed: %#v", result)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "W_UNSUPPORTED_FILE" {
			t.Fatalf("shebang-routable file marked unsupported: %#v", warning)
		}
	}
}

// TestAnalyzeGitRangeAddedOrDeletedUnsupportedFileReportsNothing covers the
// shape that an earlier version of the mixed-support rule got wrong.
//
// The rule only means something when BOTH sides exist and exactly one parses:
// then the file crossed the boundary. An added or deleted unsupported file has
// only one side at all, so a guard written as "both sides unsupported" never
// fired for it, and the empty comparison fell through to a module addition or
// removal for a path no snapshot indexes. An absent side is not an unsupported
// side, and nothing here is in the graph either way.
func TestAnalyzeGitRangeAddedOrDeletedUnsupportedFileReportsNothing(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "anchor.go", "package anchor\n\nfunc Anchor() int { return 0 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "pic.png", "nothing parses this\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add an unsupported file")
	added := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, added, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("adding an unindexed file produced a record: %#v", result.Files)
	}

	if err := os.Remove(filepath.Join(repo, "pic.png")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "delete it again")

	result, err = AnalyzeGitRange(context.Background(), repo, added, rev(t, repo, "HEAD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("deleting an unindexed file produced a record: %#v", result.Files)
	}
}

// TestAnalyzeGitRangeRenameBetweenUnindexedPathsReportsNothing is the guard on
// the other side of the mixed-support rule.
//
// When exactly one side has a parser the file entered or left the index and the
// removals or additions are real. When NEITHER side does, the file is in no
// snapshot at either end of the range: there is nothing to retire and nothing to
// learn, and emitting any record would invent one for a path the graph has never
// held — the phantom class the index policy work exists to prevent.
func TestAnalyzeGitRangeRenameBetweenUnindexedPathsReportsNothing(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "note.png", "nothing parses this\n")
	write(t, repo, "anchor.go", "package anchor\n\nfunc Anchor() int { return 0 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	git(t, repo, "mv", "note.png", "moved.png")
	git(t, repo, "commit", "-m", "rename between two paths the graph never indexes")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("files = %#v, want nothing: neither path is in any snapshot", result.Files)
	}
}

func TestAnalyzeGitRangeMarksMixedSupportRenames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fromPath   string
		toPath     string
		warnPath   string
		wantType   string
		wantStatus string
		wantPath   string
	}{
		{name: "supported to unsupported", fromPath: "sample.go", toPath: "sample.ps1", warnPath: "sample.ps1", wantType: "removed", wantStatus: "D", wantPath: "sample.go"},
		{name: "unsupported to supported", fromPath: "sample.ps1", toPath: "sample.go", warnPath: "sample.ps1", wantType: "added", wantStatus: "A", wantPath: "sample.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Test")
			git(t, repo, "config", "user.email", "graph@example.com")
			write(t, repo, tc.fromPath, "package sample\n\nfunc Run() {}\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "initial")
			base := rev(t, repo, "HEAD")

			if err := os.Rename(filepath.Join(repo, tc.fromPath), filepath.Join(repo, tc.toPath)); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "-A")
			git(t, repo, "commit", "-m", "rename across parser boundary")
			head := rev(t, repo, "HEAD")

			result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
			if err != nil {
				t.Fatal(err)
			}
			// A side with no parser holds nothing in the graph, so this rename is
			// the file leaving or entering the index and the honest delta is the
			// removals or additions a snapshot of each side would show. This used
			// to assert no record at all, to avoid a "one-sided phantom": but the
			// removals are not phantom, because .ps1 is genuinely absent from
			// every snapshot, and suppressing them left a consumer unable to
			// retire the old compound-v1 IDs while the marker named only one path.
			// A failed PARSE is still suppressed, and for the opposite reason:
			// there the absence is this provider's blind spot, not a fact.
			if len(result.Files) != 1 {
				t.Fatalf("mixed-support rename = %#v, want the indexed side reported", result.Files)
			}
			reported := result.Files[0]
			// Restated as what happened to the index, so the path and language
			// belong to the side the graph holds. Reporting `sample.ps1` as a Go
			// rename would present an unindexed file as a parsed one.
			if reported.Path != tc.wantPath || reported.Status != tc.wantStatus || reported.OldPath != "" {
				t.Fatalf("file = %#v, want %s of %q with no rename provenance", reported, tc.wantStatus, tc.wantPath)
			}
			if reported.Language != "Go" {
				t.Fatalf("language = %q, want the indexed side's language", reported.Language)
			}
			changes := reported.Changes
			if len(changes) != 1 || changes[0].Type != tc.wantType || changes[0].Name != "Run" {
				t.Fatalf("changes = %#v, want a single %q for Run", changes, tc.wantType)
			}
			var marker *ProviderWarning
			for i, warning := range result.Warnings {
				if warning.Code == "W_UNSUPPORTED_FILE" {
					marker = &result.Warnings[i]
					break
				}
			}
			if marker == nil || marker.FilePath != tc.warnPath || !strings.Contains(marker.EffectOnCompleteness, "no parser") {
				t.Fatalf("missing mixed-support marker: %#v", result.Warnings)
			}
		})
	}
}

func TestAnalyzeCheckpointResolvesAssociatedCommit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")

	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "agent update\n\nEntire-Checkpoint: abc123def456")

	result, err := AnalyzeCheckpoint(context.Background(), repo, "abc123def456")
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint != "abc123def456" {
		t.Fatalf("checkpoint = %q", result.Checkpoint)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v", result.Files)
	}
	if result.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q, want %q", result.SchemaVersion, SchemaVersion)
	}
}

func TestCompareEntitiesDisambiguatesSameNameOverloads(t *testing.T) {
	// Issue #35 repro and regression guard for the phase-2 positional
	// fallback: two same-name, same-Kind overloads (reachable in C#/Java)
	// must not collide in the diff keys. Editing the FIRST overload's
	// signature (F(int) -> F(long)) must be reported as signature_changed,
	// and the untouched SECOND overload must not become a spurious
	// remove/add. A naive pure-signature key would regress this to
	// removed+added because the rename reconciler's signature similarity
	// (~0.33) is below renameThreshold.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(long)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one signature change for the first overload", changes)
	}
	c := changes[0]
	if c.Type != "signature_changed" || c.Kind != "method" || c.Name != "C.F" {
		t.Fatalf("change = %#v, want signature_changed for method C.F", c)
	}
	if c.OldSignature != "F(int)" || c.NewSignature != "F(long)" {
		t.Fatalf("change signatures = %q -> %q, want F(int) -> F(long)", c.OldSignature, c.NewSignature)
	}
}

func TestCompareEntitiesDetectsSecondOverloadEdit(t *testing.T) {
	// Control: editing only the SECOND overload is reported, and the first is
	// left untouched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(object)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one signature change for the second overload", changes)
	}
	c := changes[0]
	if c.Type != "signature_changed" || c.OldSignature != "F(string)" || c.NewSignature != "F(object)" {
		t.Fatalf("change = %#v, want signature_changed F(string) -> F(object)", c)
	}
}

func TestCompareEntitiesSingleEntityUnchangedBehavior(t *testing.T) {
	// Regression: a lone non-overloaded entity keeps its pre-ordinal behavior.
	before := []Entity{
		{Kind: "function", Name: "validate", Signature: "validate(token)", StartLine: 1},
	}
	after := []Entity{
		{Kind: "function", Name: "validate", Signature: "validate(token, issuer)", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one change", changes)
	}
	if changes[0].Type != "signature_changed" || changes[0].Name != "validate" {
		t.Fatalf("change = %#v, want signature_changed for validate", changes[0])
	}
}

func TestCompareEntitiesAddedOverloadReportedAsAdded(t *testing.T) {
	// Adding a third overload (before has 2, after has 3, appended in file
	// order) must surface exactly one `added` for the new overload; the two
	// pre-existing overloads pair by exact signature and produce no churn.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(removed) != 0 {
		t.Fatalf("unexpected removed: %#v", removed)
	}
	if len(added) != 1 {
		t.Fatalf("added = %#v, want exactly one added overload", added)
	}
	if added[0].Signature != "F(bool)" {
		t.Fatalf("added signature = %q, want F(bool)", added[0].Signature)
	}
}

func TestCompareEntitiesRemovedOverloadReported(t *testing.T) {
	// Removing an overload (before has 3, after has 2, the last in file order
	// dropped) must surface exactly one `removed` for the dropped overload and
	// must not misattribute the removal to a surviving overload.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(added) != 0 {
		t.Fatalf("unexpected added: %#v", added)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %#v, want exactly one removed overload", removed)
	}
	if removed[0].Signature != "F(bool)" {
		t.Fatalf("removed signature = %q, want F(bool)", removed[0].Signature)
	}
}

func TestCompareEntitiesTrueDuplicatesEditOne(t *testing.T) {
	// Two entities with identical Kind:Name:Signature on both sides (true
	// duplicates). Editing the body of one must surface exactly one
	// body_changed and no spurious remove/add for the untouched duplicate.
	// This works because true duplicates are paired by occurrence index in
	// file order, so the Nth duplicate on each side pairs with the Nth on
	// the other (a plain signature key would collide for true duplicates).
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 {
		t.Fatalf("unexpected remove/add: removed=%#v added=%#v", removed, added)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one body change", changes)
	}
	if changes[0].Type != "body_changed" || changes[0].Kind != "method" || changes[0].Name != "C.F" {
		t.Fatalf("change = %#v, want body_changed for method C.F", changes[0])
	}
}

func TestCompareEntitiesDuplicateInsertionPreservesExistingBodies(t *testing.T) {
	// A new exact-signature duplicate inserted before two existing duplicates
	// must not shift occurrence-index pairing and manufacture body changes for
	// both survivors. Stable body hashes identify the existing entities; only
	// the genuinely new h0 entity should remain unmatched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h0", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 {
		t.Fatalf("duplicate insertion caused survivor churn: changes=%#v removed=%#v", changes, removed)
	}
	if len(added) != 1 || added[0].BodyHash != "h0" || added[0].StartLine != 1 {
		t.Fatalf("added = %#v, want only the new h0 duplicate", added)
	}
}

func TestCompareEntitiesRepeatedDuplicateInsertionPreservesContentClass(t *testing.T) {
	// Repeated equal hashes are an equivalence class, not an ambiguity that
	// should disable matching. Pair the two existing h1 entities as a multiset
	// and leave only the distinct h0 insertion unmatched.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h0", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 1 || added[0].BodyHash != "h0" {
		t.Fatalf("unexpected duplicate multiset diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestCompareEntitiesDuplicateReorderUsesBodyBeforeFingerprint(t *testing.T) {
	// Fingerprints can legitimately collide for exact-signature duplicates
	// whose bodies normalize alike. An exact body hash is stronger evidence in
	// this phase and must keep a pure reorder inert.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", Fingerprint: "shared", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h2", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F()", BodyHash: "h1", Fingerprint: "shared", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 0 {
		t.Fatalf("duplicate reorder must be inert: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestCompareEntitiesExactSignatureOutranksSharedFingerprint(t *testing.T) {
	// Cross-signature fingerprint continuity is useful only after exact
	// signatures are anchored. Shared fingerprints must not turn removal of
	// F(int) into a phantom F(int)->F(string) signature edit.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", Fingerprint: "shared", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "shared", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "shared", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(added) != 0 {
		t.Fatalf("exact-signature survivor caused churn: changes=%#v added=%#v", changes, added)
	}
	if len(removed) != 1 || removed[0].Signature != "F(int)" {
		t.Fatalf("removed = %#v, want only F(int)", removed)
	}
}

func TestCompareEntitiesExactSignaturesOutrankSwappedBodies(t *testing.T) {
	// Bodies may be copied or swapped between overloads while both signatures
	// survive. Cross-signature fingerprint evidence must not steal those exact
	// signatures and turn two body edits into two phantom signature edits.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "int-body", Fingerprint: "int-body", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "string-body", Fingerprint: "string-body", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "string-body", Fingerprint: "string-body", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "int-body", Fingerprint: "int-body", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 || len(changes) != 2 {
		t.Fatalf("unexpected swapped-body diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
	for _, change := range changes {
		if change.Type != "body_changed" {
			t.Fatalf("change = %#v, want body_changed", change)
		}
	}
}

func TestCompareEntitiesRemovalAndSignatureEditMatchByFingerprint(t *testing.T) {
	// When one overload is removed while another changes signature, positional
	// leftover pairing chooses the wrong survivor. The unchanged fingerprint
	// anchors F(string) across its F(object) signature edit, leaving F(int) as
	// the actual removal even though the edited entity moved to the first line.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "int-body", Fingerprint: "int-fingerprint", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "old-body", Fingerprint: "survivor", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(object)", BodyHash: "new-body", Fingerprint: "survivor", StartLine: 1},
	}

	changes, removed, added := compareEntities(before, after)
	if len(added) != 0 {
		t.Fatalf("unexpected added overloads: %#v", added)
	}
	if len(removed) != 1 || removed[0].Signature != "F(int)" {
		t.Fatalf("removed = %#v, want only F(int)", removed)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %#v, want one survivor signature change", changes)
	}
	change := changes[0]
	if change.Type != "signature_changed" || change.OldSignature != "F(string)" || change.NewSignature != "F(object)" {
		t.Fatalf("change = %#v, want signature_changed F(string) -> F(object)", change)
	}
	if change.BeforeStartLine != 5 || change.AfterStartLine != 1 {
		t.Fatalf("change lines = %d -> %d, want 5 -> 1", change.BeforeStartLine, change.AfterStartLine)
	}
}

func TestCompareEntitiesReorderedSignatureEditsMatchUniqueFingerprints(t *testing.T) {
	// Two survivors may both change signature and reorder. Unique fingerprints
	// must produce a one-to-one match independent of the new file order.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "old-a", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "old-b", Fingerprint: "b", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(object)", BodyHash: "new-b", Fingerprint: "b", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(long)", BodyHash: "new-a", Fingerprint: "a", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(removed) != 0 || len(added) != 0 || len(changes) != 2 {
		t.Fatalf("unexpected reordered edit diff: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
	paired := map[string]string{}
	for _, change := range changes {
		if change.Type != "signature_changed" {
			t.Fatalf("change = %#v, want signature_changed", change)
		}
		paired[change.OldSignature] = change.NewSignature
	}
	if paired["F(int)"] != "F(long)" || paired["F(string)"] != "F(object)" {
		t.Fatalf("signature pairs = %#v, want int->long and string->object", paired)
	}
}

func TestCompareEntitiesSameLineOverloadChangesSortDeterministically(t *testing.T) {
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", Fingerprint: "b", StartLine: 1},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(long)", Fingerprint: "a", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(object)", Fingerprint: "b", StartLine: 1},
	}

	for i := 0; i < 100; i++ {
		changes := Compare(before, after)
		if len(changes) != 2 || changes[0].OldSignature != "F(int)" || changes[1].OldSignature != "F(string)" {
			t.Fatalf("iteration %d changes = %#v, want deterministic old-signature order", i, changes)
		}
	}
}

func TestCompareEntitiesRemoveFirstOverloadNoCascade(t *testing.T) {
	// Removing the FIRST of three same-name overloads must report exactly one
	// `removed F(int)`. Pure positional-ordinal keying used to cascade here:
	// signature_changed F(int)->F(string), signature_changed
	// F(string)->F(bool), removed F(bool) -- all phantom. Two-phase matching
	// pairs the surviving overloads by exact signature first, leaving only
	// the truly removed one.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on surviving overloads: %#v", changes)
	}
	if len(added) != 0 {
		t.Fatalf("unexpected added: %#v", added)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %#v, want exactly one removed overload", removed)
	}
	if removed[0].Signature != "F(int)" {
		t.Fatalf("removed signature = %q, want F(int)", removed[0].Signature)
	}
}

func TestCompareEntitiesMidListInsertOnlyAdded(t *testing.T) {
	// Inserting an overload in the MIDDLE of the list must surface exactly
	// one `added F(string)` and leave the surrounding overloads untouched
	// (no phantom signature_changed on the shifted tail).
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", StartLine: 5},
		{Kind: "method", Name: "C.F", Signature: "F(bool)", StartLine: 9},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 {
		t.Fatalf("unexpected changes on stable overloads: %#v", changes)
	}
	if len(removed) != 0 {
		t.Fatalf("unexpected removed: %#v", removed)
	}
	if len(added) != 1 {
		t.Fatalf("added = %#v, want exactly one added overload", added)
	}
	if added[0].Signature != "F(string)" {
		t.Fatalf("added signature = %q, want F(string)", added[0].Signature)
	}
}

func TestCompareEntitiesReorderedOverloadsNoChanges(t *testing.T) {
	// Purely reordering same-name overloads (identical signatures and bodies,
	// only file positions swapped) must produce no events at all: exact
	// signature pairing matches each overload to itself regardless of
	// position.
	before := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "hi", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "hs", StartLine: 5},
	}
	after := []Entity{
		{Kind: "method", Name: "C.F", Signature: "F(string)", BodyHash: "hs", StartLine: 1},
		{Kind: "method", Name: "C.F", Signature: "F(int)", BodyHash: "hi", StartLine: 5},
	}

	changes, removed, added := compareEntities(before, after)
	if len(changes) != 0 || len(removed) != 0 || len(added) != 0 {
		t.Fatalf("reorder must be inert: changes=%#v removed=%#v added=%#v", changes, removed, added)
	}
}

func TestAnalyzeGitRangeSurfacesParseFailures(t *testing.T) {
	const validTS = "export function alpha() {\n    return 1\n}\n\nexport function beta() {\n    return 2\n}\n"
	// Parses to zero entities with ParseStatus.ParseError == true.
	const brokenTS = "type Broken = <\n\nexport function alpha(){return 1}\nexport function beta(){return 2}\n"
	// Valid TypeScript with no top-level entities (a genuinely emptied file).
	const emptiedTS = "// all symbols removed\n"

	changesByType := func(result Result, changeType string) []EntityChange {
		var out []EntityChange
		for _, file := range result.Files {
			for _, change := range file.Changes {
				if change.Type == changeType {
					out = append(out, change)
				}
			}
		}
		return out
	}
	parseWarning := func(result Result, path string) *ProviderWarning {
		for i := range result.Warnings {
			w := &result.Warnings[i]
			if w.FilePath == path && (w.Code == "E_PARSE_ERROR" || w.Code == "E_PARSE_TIMEOUT") {
				return w
			}
		}
		return nil
	}

	t.Run("head unparseable surfaces warning without phantom removals", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", validTS, brokenTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		w := parseWarning(result, "svc.ts")
		if w == nil {
			t.Fatalf("expected parse-failure warning for svc.ts, got %#v", result.Warnings)
		}
		if w.Code != "E_PARSE_ERROR" || w.Severity != "warning" || w.EffectOnCompleteness == "" {
			t.Fatalf("unexpected warning shape: %#v", w)
		}
		for _, c := range changesByType(result, "removed") {
			if c.Name == "alpha" || c.Name == "beta" {
				t.Fatalf("phantom removed change for %q: %#v", c.Name, result.Files)
			}
		}
	})

	t.Run("base unparseable surfaces warning without phantom additions", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", brokenTS, validTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if parseWarning(result, "svc.ts") == nil {
			t.Fatalf("expected parse-failure warning for svc.ts, got %#v", result.Warnings)
		}
		for _, c := range changesByType(result, "added") {
			if c.Name == "alpha" || c.Name == "beta" {
				t.Fatalf("phantom added change for %q: %#v", c.Name, result.Files)
			}
		}
	})

	t.Run("genuinely emptied file still reports real removals with no warning", func(t *testing.T) {
		repo, base, head := buildParseFailureRepo(t, "svc.ts", validTS, emptiedTS)
		result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
		if err != nil {
			t.Fatal(err)
		}
		if w := parseWarning(result, "svc.ts"); w != nil {
			t.Fatalf("did not expect a parse-failure warning for a validly emptied file: %#v", w)
		}
		removed := map[string]bool{}
		for _, c := range changesByType(result, "removed") {
			removed[c.Name] = true
		}
		if !removed["alpha"] || !removed["beta"] {
			t.Fatalf("expected real removed changes for alpha and beta, got %#v", result.Files)
		}
	})
}

func buildParseFailureRepo(t *testing.T, file, baseContent, headContent string) (repo, base, head string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, file, baseContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base = rev(t, repo, "HEAD")
	write(t, repo, file, headContent)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head = rev(t, repo, "HEAD")
	return repo, base, head
}

func write(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitInput(t *testing.T, repo, input string, args ...string) string {
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

func gitOutputHonoringReplaceRefs(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	env := cmd.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_NO_REPLACE_OBJECTS=") {
			filtered = append(filtered, entry)
		}
	}
	cmd.Env = filtered
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git honoring replacements %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func nestedAnalyzeTree(t *testing.T, repo, content string, depth int) (tree, path string) {
	t.Helper()
	blob := gitInput(t, repo, content, "hash-object", "-w", "--stdin")
	tree = gitInput(t, repo, fmt.Sprintf("100644 blob %s\tfile.go%c", blob, byte(0)), "mktree", "-z")
	path = "file.go"
	component := strings.Repeat("a", 200)
	for range depth {
		tree = gitInput(t, repo, fmt.Sprintf("040000 tree %s\t%s%c", tree, component, byte(0)), "mktree", "-z")
		path = component + "/" + path
	}
	return tree, path
}

func importDeepAnalyzeHistory(t *testing.T, repo, path, baseContent, headContent string) (base, head string) {
	t.Helper()
	var input strings.Builder
	fmt.Fprintf(&input, "blob\nmark :1\ndata %d\n%s\n", len(baseContent), baseContent)
	fmt.Fprintf(&input, "commit refs/heads/deep\nmark :2\ncommitter Test <test@example.com> 1 +0000\ndata 4\nbase\nM 100644 :1 %s\n\n", path)
	fmt.Fprintf(&input, "blob\nmark :3\ndata %d\n%s\n", len(headContent), headContent)
	fmt.Fprintf(&input, "commit refs/heads/deep\nmark :4\ncommitter Test <test@example.com> 2 +0000\ndata 4\nhead\nfrom :2\nM 100644 :3 %s\n\n", path)
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(input.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import deep history: %v\n%s", err, out)
	}
	return rev(t, repo, "refs/heads/deep^"), rev(t, repo, "refs/heads/deep")
}

func rev(t *testing.T, repo, value string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", value)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v\n%s", value, err, out)
	}
	return string(out[:len(out)-1])
}

// TestAnalyzeGitRangeBudgetExceededEmitsPartialResult pins the time-budget
// contract: when MaxDuration runs out, the analysis returns cleanly (no
// error) and enumerates every skipped changed file with a machine-readable
// W_ANALYSIS_BUDGET_EXCEEDED warning instead of producing nothing.
func TestAnalyzeGitRangeBudgetExceededEmitsPartialResult(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	write(t, repo, "other.py", "def helper(value):\n    return value\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	write(t, repo, "other.py", "def helper(value):\n    return value + 1\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRangeWithOptions(t.Context(), repo, base, head, nil, AnalyzeOptions{
		MaxDuration: time.Nanosecond, // expires before the first changed file
	})
	if err != nil {
		t.Fatalf("budget exhaustion must not error, got %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("no file should have been analyzed under an expired budget, got %#v", result.Files)
	}
	skipped := map[string]bool{}
	for _, warning := range result.Warnings {
		if warning.Code != "W_ANALYSIS_BUDGET_EXCEEDED" {
			t.Fatalf("unexpected warning %#v", warning)
		}
		if warning.Severity != "warning" {
			t.Fatalf("severity = %q, want warning", warning.Severity)
		}
		if warning.EffectOnCompleteness == "" || warning.Detail == "" {
			t.Fatalf("budget warning must carry effect and detail, got %#v", warning)
		}
		skipped[warning.FilePath] = true
	}
	for _, want := range []string{"auth.py", "other.py"} {
		if !skipped[want] {
			t.Fatalf("skipped files %v missing %q", skipped, want)
		}
	}
}

func TestAnalyzeGitRangeDeepPathMetadataHonorsBudget(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	components := make([]string, 800)
	for index := range components {
		components[index] = fmt.Sprintf("component-%010d", index)
	}
	path := strings.Join(components, "/") + "/file.go"
	if len(path) != 16807 {
		t.Fatalf("deep path length = %d, want reviewer fixture length 16807", len(path))
	}
	base, head := importDeepAnalyzeHistory(
		t,
		repo,
		path,
		"package deep\nvar Value = 1\n",
		"package deep\nvar Value = 2\n",
	)

	// Use a deterministically exhausted tiny budget for the integration-level
	// warning contract. The injected-clock LimitedFileReader test separately
	// proves that an in-progress component walk stops between subprocesses; this
	// test must not assume how quickly a particular host can launch 256 Git
	// processes before the fixed process allowance wins the race.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := AnalyzeGitRangeWithOptions(ctx, repo, base, head, nil, AnalyzeOptions{
		MaxDuration: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("deep-path budget exhaustion must return a partial result: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("budget-exhausted deep path produced changes: %#v", result.Files)
	}
	foundBudget := false
	for _, warning := range result.Warnings {
		if warning.Code == "E_FILE_READ" {
			t.Fatalf("deadline-limited traversal was misreported as a file read failure: %#v", warning)
		}
		if warning.Code == "W_ANALYSIS_BUDGET_EXCEEDED" && warning.FilePath == path {
			foundBudget = true
		}
	}
	if !foundBudget {
		t.Fatalf("deep-path result lacks per-file budget warning: %#v", result.Warnings)
	}
}

// TestAnalyzeGitRangeNoBudgetKeepsFullResult pins that MaxDuration == 0 keeps
// the historical unbounded behavior: full result, no budget warnings.
func TestAnalyzeGitRangeNoBudgetKeepsFullResult(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "auth.py", "def validate_token(token):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")
	write(t, repo, "auth.py", "def validate_token(token, issuer=None):\n    return bool(token)\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRangeWithOptions(t.Context(), repo, base, head, nil, AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v, want the changed file analyzed", result.Files)
	}
	for _, warning := range result.Warnings {
		if warning.Code == "W_ANALYSIS_BUDGET_EXCEEDED" {
			t.Fatalf("no budget warning expected without MaxDuration, got %#v", warning)
		}
	}
}

// TestAnalyzeGitRangeHonorsGraphIgnore pins the documented contract that a
// repo-root .graphignore is honored by every graph command. The snapshot and
// search family already applied it; the diff family did not, so a tracked but
// vendored or generated tree that the graph never indexes still produced entity
// changes — symbols no snapshot of the repository contains.
func TestAnalyzeGitRangeHonorsGraphIgnore(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".graphignore", "vendored/\n")
	write(t, repo, "keep/keep.go", "package keep\n\nfunc Keep() int { return 1 }\n")
	write(t, repo, "vendored/gen.go", "package vendored\n\nfunc Gen() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "keep/keep.go", "package keep\n\nfunc Keep() int { return 2 }\n")
	write(t, repo, "vendored/gen.go", "package vendored\n\nfunc Gen() int { return 2 }\n\nfunc Extra() int { return 3 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "touch both trees")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "keep/keep.go" {
		t.Fatalf("ignored tree reported in the diff: %#v", result.Files)
	}

	// The same repository's snapshot must agree: whatever the diff reports has
	// to be something the graph would index.
	snapshot, err := BuildProviderSnapshot(context.Background(), repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range snapshot.Symbols {
		if strings.HasPrefix(symbol.FilePath, "vendored/") {
			t.Fatalf("snapshot indexed an ignored file, fixture is wrong: %#v", symbol)
		}
	}
}

// TestAnalyzeGitRangeHonorsGraphIgnoreForDeletions covers the base side: a
// deletion has no head path, so the base path is what decides.
func TestAnalyzeGitRangeHonorsGraphIgnoreForDeletions(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, ".graphignore", "vendored/\n")
	write(t, repo, "keep/keep.go", "package keep\n\nfunc Keep() int { return 1 }\n")
	write(t, repo, "vendored/gen.go", "package vendored\n\nfunc Gen() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	if err := os.Remove(filepath.Join(repo, "vendored/gen.go")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "drop the vendored tree")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("deleting an ignored file produced a delta: %#v", result.Files)
	}
}

// TestAnalyzeGitRangeWithoutGraphIgnoreIsUnchanged guards against the filter
// swallowing ordinary files when no ignore rule exists.
func TestAnalyzeGitRangeWithoutGraphIgnoreIsUnchanged(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "vendored/gen.go", "package vendored\n\nfunc Gen() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "vendored/gen.go", "package vendored\n\nfunc Gen() int { return 2 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "edit")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "vendored/gen.go" {
		t.Fatalf("file was dropped without an ignore rule: %#v", result.Files)
	}
}

// TestAnalyzeGitRangeHonorsBuiltinSecretRules covers the other half of the same
// matcher. The provider's built-in secret rules already keep committed
// credential files out of the snapshot; the diff reported them as entity
// changes, naming paths the rest of the provider refuses to index.
func TestAnalyzeGitRangeHonorsBuiltinSecretRules(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "app.go", "package app\n\nfunc Run() int { return 1 }\n")
	write(t, repo, ".env", "API_KEY=first\n")
	git(t, repo, "add", "-A", "-f")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	write(t, repo, "app.go", "package app\n\nfunc Run() int { return 2 }\n")
	write(t, repo, ".env", "API_KEY=second\n")
	git(t, repo, "add", "-A", "-f")
	git(t, repo, "commit", "-m", "rotate")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range result.Files {
		if file.Path == ".env" {
			t.Fatalf("committed credential file reported in the diff: %#v", file)
		}
	}
	if len(result.Files) != 1 || result.Files[0].Path != "app.go" {
		t.Fatalf("files = %#v, want only app.go", result.Files)
	}
}

// TestAnalyzeGitRangePureRenameIsReported pins the contract that a file Git
// classified as a rename is never absent from the diff. A 100%-similarity
// rename has no content change to report, but the file path is a component of
// every compound-v1 symbol ID, so the rename re-identifies every entity in the
// file. Dropping the file made a pure rename indistinguishable from an empty
// diff for any consumer that keys on the output.
func TestAnalyzeGitRangePureRenameIsReported(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "old/sample.go", "package sample\n\nfunc Run() int { return 1 }\n\ntype W struct{ N int }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	if err := os.MkdirAll(filepath.Join(repo, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "mv", "old/sample.go", "new/sample.go")
	git(t, repo, "commit", "-m", "pure rename")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("pure rename produced %d files, want 1: %#v", len(result.Files), result.Files)
	}
	file := result.Files[0]
	if file.Path != "new/sample.go" || file.OldPath != "old/sample.go" {
		t.Fatalf("rename metadata = %q -> %q", file.OldPath, file.Path)
	}
	if file.Status != "R" {
		t.Fatalf("status = %q, want R", file.Status)
	}
	if file.Language != "Go" {
		t.Fatalf("language = %q, want Go", file.Language)
	}
	if len(file.Changes) != 1 {
		t.Fatalf("changes = %#v, want exactly one path-scope change", file.Changes)
	}
	change := file.Changes[0]
	if change.Type != "moved" || change.Kind != moduleKind {
		t.Fatalf("change type/kind = %q/%q, want moved/%s", change.Type, change.Kind, moduleKind)
	}
	if change.OldPath != "old/sample.go" || change.NewPath != "new/sample.go" {
		t.Fatalf("change paths = %q -> %q", change.OldPath, change.NewPath)
	}
	if change.Name != "new/sample.go" {
		t.Fatalf("change name = %q, want the new path", change.Name)
	}
	if change.Reconciliation != "MOVED" {
		t.Fatalf("reconciliation = %q, want MOVED", change.Reconciliation)
	}
}

// TestAnalyzeGitRangeUnchangedFileStaysAbsent guards the other side of the same
// contract: reporting a rename must not start reporting files nothing touched.
//
// The range deliberately CONTAINS a pure rename, so the path-scope fallback is
// actually exercised while sample.go sits still. An earlier version of this test
// changed only an unrelated file, which never put anything through the new
// branch at all and passed identically without the fix.
func TestAnalyzeGitRangeUnchangedFileStaysAbsent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "sample.go", "package sample\n\nfunc Run() int { return 1 }\n")
	write(t, repo, "moved.go", "package sample\n\nfunc Moved() int { return 2 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	git(t, repo, "mv", "moved.go", "renamed.go")
	git(t, repo, "commit", "-m", "rename one file, touch nothing else")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The rename is reported...
	if len(result.Files) != 1 || result.Files[0].Path != "renamed.go" {
		t.Fatalf("files = %#v, want only the renamed file", result.Files)
	}
	// ...and the file that did not move is not.
	for _, file := range result.Files {
		if file.Path == "sample.go" || file.OldPath == "sample.go" {
			t.Fatalf("untouched file appeared in the diff: %#v", file)
		}
	}
}

// TestAnalyzeGitRangeModeOnlyChangeStaysAbsent covers a file that reaches the
// path-scope fallback with identical content and an unchanged path: Git reports
// a mode-only change as a modification, and it must not be reported as a move.
//
// The mode is set through the index rather than the filesystem. os.Chmod does
// nothing on Windows, where core.fileMode is false, so a filesystem chmod made
// this test skip on the one platform it could not be checked on — and it is the
// only guard on this branch, so a skip there was a hole rather than a gap.
// `git update-index --chmod` writes the index entry directly and behaves the
// same everywhere.
func TestAnalyzeGitRangeModeOnlyChangeStaysAbsent(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "sample.go", "package sample\n\nfunc Run() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	git(t, repo, "update-index", "--chmod=+x", "sample.go")
	git(t, repo, "commit", "-m", "mode only")
	head := rev(t, repo, "HEAD")

	// The fixture is only meaningful if Git really reported the file with both
	// sides pointing at the same blob.
	if before, after := blobAt(t, repo, base, "sample.go"), blobAt(t, repo, head, "sample.go"); before != after {
		t.Fatalf("fixture is wrong: content changed (%s -> %s), this is not a mode-only change", before, after)
	}

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("mode-only change produced a delta: %#v", result.Files)
	}
}

// blobAt returns the blob OID a path resolves to at a revision, so a fixture can
// assert it really did leave the content alone.
func blobAt(t *testing.T, repo, revision, path string) string {
	t.Helper()
	return rev(t, repo, revision+":"+path)
}

// TestAnalyzeGitRangeCaseOnlyRenameIsReported pins the one rename whose handling
// genuinely differs by platform. Correcting a file's capitalization changes the
// path, so it re-identifies every symbol in the file exactly as any other rename
// does — but Linux sees two distinct names while macOS and Windows fold them onto
// one, and Go's string comparison in the same-path guard is case-sensitive
// regardless. Making that guard case-insensitive to "fix" Windows would silently
// drop a real re-identification, so the behaviour is pinned here.
//
// The rename is staged through the index rather than the filesystem, so the
// fixture cannot depend on whether the host folds case: no file is ever renamed
// on disk, and the test needs no platform guard to run everywhere.
func TestAnalyzeGitRangeCaseOnlyRenameIsReported(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "Sample.go", "package sample\n\nfunc Run() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	blob := blobAt(t, repo, base, "Sample.go")
	git(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+blob+",sample.go")
	git(t, repo, "update-index", "--force-remove", "Sample.go")
	git(t, repo, "commit", "-m", "correct the capitalization")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v, want one Sample.go -> sample.go rename", result.Files)
	}
	file := result.Files[0]
	if file.Path != "sample.go" || file.OldPath != "Sample.go" {
		t.Fatalf("files = %#v, want one Sample.go -> sample.go rename", result.Files)
	}
	if len(file.Changes) != 1 || file.Changes[0].Type != "moved" || file.Changes[0].Kind != moduleKind {
		t.Fatalf("changes = %#v, want one module moved", file.Changes)
	}
	if file.Changes[0].OldPath != "Sample.go" || file.Changes[0].NewPath != "sample.go" {
		t.Fatalf("change paths = %q -> %q, want the capitalization preserved on both sides",
			file.Changes[0].OldPath, file.Changes[0].NewPath)
	}
}

// TestAnalyzeGitRangeRenameAcrossLanguagesReportsTheHeadLanguage pins the label
// on a rename that crosses extensions. The graph indexes the head path with the
// head parser's language, so reporting the base one contradicts every snapshot
// of that tree.
func TestAnalyzeGitRangeRenameAcrossLanguagesReportsTheHeadLanguage(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	write(t, repo, "mod.js", "export function run() {\n  return 1;\n}\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	base := rev(t, repo, "HEAD")

	git(t, repo, "mv", "mod.js", "mod.ts")
	git(t, repo, "commit", "-m", "js to ts, byte-identical")
	head := rev(t, repo, "HEAD")

	result, err := AnalyzeGitRange(context.Background(), repo, base, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %#v, want one", result.Files)
	}
	if got := result.Files[0].Language; got != "TypeScript" {
		t.Fatalf("language = %q, want TypeScript: the head path is what the graph indexes", got)
	}
}
