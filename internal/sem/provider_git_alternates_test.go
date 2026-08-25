package sem

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func TestGitMetadataGuardAllowsLocalCQuotedAlternateObjectStore(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "alternate objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(gitDir, "objects", "info"), "alternates", `"../../alternate\040objects"`+"\n")
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("same-volume C-quoted relative alternate object store was rejected")
	}
}

func TestCommittedHEADReadsAnObjectFromAValidatedLocalAlternate(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "primary")
	alternate := filepath.Join(repo, "alternate objects")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(alternate, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, alternate, "init")
	git(t, alternate, "config", "user.name", "Entire Graph Test")
	git(t, alternate, "config", "user.email", "graph@example.com")
	writeFile(t, alternate, "main.go", "package main\n")
	git(t, alternate, "add", ".")
	git(t, alternate, "commit", "-m", "alternate object")
	wantCommit, wantTree, err := gitutil.HeadCommitAndTree(t.Context(), alternate)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".git", "objects", "info"), "alternates", `"../../alternate\040objects/.git/objects"`+"\n")
	writeFile(t, filepath.Join(repo, ".git"), "HEAD", wantCommit+"\n")

	commit, tree, err := resolveCommittedHEAD(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if commit != wantCommit || tree != wantTree {
		t.Fatalf("resolved provenance = %s/%s, want %s/%s", commit, tree, wantCommit, wantTree)
	}
}

func TestParseGitAlternatePathsMatchesGitInputRules(t *testing.T) {
	tests := []struct {
		raw  []byte
		want []string
		ok   bool
	}{
		{raw: []byte("plain/path\n# comment\n\nnext"), want: []string{"plain/path", "next"}, ok: true},
		{raw: []byte(`"path\\with\040space"`), want: []string{"path\\with space"}, ok: true},
		{raw: []byte("\"quoted\"\r\nnext\n"), want: []string{"quoted", "next"}, ok: true},
		{raw: []byte("\"line\nbreak\"\n"), want: []string{"line\nbreak"}, ok: true},
		{raw: []byte(`"unterminated`), want: []string{`"unterminated`}, ok: true},
		{raw: []byte(`""`), want: nil, ok: true},
		{raw: []byte("before\x00ignored\n"), want: []string{"before"}, ok: true},
		{raw: []byte(`"before\000ignored"`), want: []string{"before"}, ok: true},
		{raw: []byte(`"path"trailing`), want: []string{"path", "railing"}, ok: true},
	}
	for _, test := range tests {
		got, ok := parseGitAlternatePaths(test.raw, maxGitAlternateEntries)
		if !slices.Equal(got, test.want) || ok != test.ok {
			t.Errorf("parseGitAlternatePaths(%q) = (%q, %v), want (%q, %v)", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestGitAlternatesBoundsFailClosed(t *testing.T) {
	if paths, ok := parseGitAlternatePaths(bytes.Repeat([]byte{'x'}, maxGitPointerBytes+1), maxGitAlternateEntries); ok || paths != nil {
		t.Fatalf("oversized decoded path = (%q, %v), want rejection", paths, ok)
	}
	if paths, ok := parseGitAlternatePaths([]byte("one\ntwo\n"), 1); ok || paths != nil {
		t.Fatalf("over-count paths = (%q, %v), want rejection", paths, ok)
	}
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, gitDir, "HEAD", "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(gitDir, "objects", "info"), "alternates", string(bytes.Repeat([]byte{'#'}, maxGitAlternatesAggregateBytes+1)))
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("oversized aggregate alternates metadata passed the pre-subprocess guard")
	}
}

func TestGitAlternateResolvedPathAccountingExactBound(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "objects")
	validation := gitAlternatesValidation{
		resolver:          &sameVolumePathResolver{baseResolved: base},
		resolvedPathBytes: maxGitMetadataTreePathBytes - len("objects") - 1,
		seen:              make(map[string]struct{}),
	}
	if !validation.admitObjectDirectory(directory) {
		t.Fatal("alternate root exactly filling the retained-path bound was refused")
	}
	if validation.admitObjectDirectory(filepath.Join(base, "next")) {
		t.Fatal("alternate root above the retained-path bound was admitted")
	}
}
