package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoConfig(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// The refusal is a safety decision, but a reader cannot act on "unsafe or
// unreadable repository metadata" -- it never says which of a dozen conditions
// tripped. Each recognised cause must name itself.
func TestGitMetadataRefusalNamesItsCause(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ config, want string }{
		"promisor remote": {
			"[remote \"https://example.invalid/x.git\"]\n\tpromisor = true\n\tpartialclonefilter = blob:none\n",
			"promisor remote",
		},
		"partial clone extension": {
			"[extensions]\n\tpartialclone = origin\n",
			"partial-clone extension",
		},
		"include": {
			"[include]\n\tpath = ../other\n",
			"include or includeIf",
		},
		"unparseable": {
			"this is not a git config\n",
			"could not be parsed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := describeGitMetadataRefusal(writeRepoConfig(t, tc.config))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("cause not named.\n got: %q\nwant substring: %q", got, tc.want)
			}
		})
	}
}

// The message must stay a refusal, and must still carry the original wording so
// anything matching on it keeps working.
func TestGitMetadataRefusalErrorKeepsBaseWording(t *testing.T) {
	t.Parallel()

	repo := writeRepoConfig(t, "[remote \"https://example.invalid/x.git\"]\n\tpromisor = true\n")
	err := gitMetadataRefusalError(repo)
	if err == nil {
		t.Fatal("expected a refusal error")
	}
	if !strings.Contains(err.Error(), "refuse Git subprocesses for unsafe or unreadable repository metadata") {
		t.Fatalf("base wording lost: %v", err)
	}
	if !strings.Contains(err.Error(), "promisor remote") {
		t.Fatalf("cause missing: %v", err)
	}
}

// Diagnosis must never widen what is accepted: an unrecognised cause still
// refuses, with the original wording and no invented explanation.
func TestGitMetadataRefusalStaysGenericWhenCauseUnknown(t *testing.T) {
	t.Parallel()

	repo := writeRepoConfig(t, "[core]\n\trepositoryformatversion = 0\n")
	if got := describeGitMetadataRefusal(repo); got != "" {
		t.Fatalf("invented a cause for an ordinary config: %q", got)
	}
	err := gitMetadataRefusalError(repo)
	if err == nil || !strings.Contains(err.Error(), "refuse Git subprocesses") {
		t.Fatalf("must still refuse: %v", err)
	}
}

// core.worktree being present is not the refusal. The predicate refuses only
// when the path it names leaves the repository's filesystem, so a valid one
// must not be reported as the cause of somebody else's refusal.
func TestGitMetadataRefusalDoesNotBlameAValidCoreWorktree(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[core]\n\tworktree = " + repo + "\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := describeGitMetadataRefusal(repo); strings.Contains(got, "core.worktree") {
		t.Fatalf("blamed a core.worktree that does not leave the filesystem: %q", got)
	}
}

// Discovery here is narrower than the predicate's: it does not follow `.git`
// gitfiles, ancestor discovery, or bare layouts. Where it cannot be sure it is
// describing the same metadata it must stay silent, not attribute the refusal to
// the common directory -- a bare repository has no `<repo>/.git` at all, and
// saying its common directory is unresolvable would be plainly wrong when the
// real cause is sitting in its config.
func TestGitMetadataRefusalStaysSilentOnABareRepository(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real cause lives here, and it is not the common directory.
	config := "[remote \"https://example.invalid/x.git\"]\n\tpromisor = true\n"
	if err := os.WriteFile(filepath.Join(repo, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := describeGitMetadataRefusal(repo); got != "" {
		t.Fatalf("claimed a cause for a layout it cannot attribute: %q", got)
	}
}
