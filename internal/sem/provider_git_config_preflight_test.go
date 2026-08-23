package sem

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitConfigPreflightFixture(t *testing.T) (repo, gitDir string) {
	t.Helper()
	repo = t.TempDir()
	gitDir = filepath.Join(repo, ".git")
	for _, dir := range []string{filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, gitDir
}

func TestGitMetadataGuardRejectsLocalConfigIncludeSections(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "ordinary include",
			content: "[include]\n\tpath = ../outside\n",
		},
		{
			name:    "case whitespace and CRLF",
			content: " \t[InClUdE]\r\n\t PaTh\t = ../outside\r\n",
		},
		{
			name:    "conditional include",
			content: "[IncludeIf \"gitdir:~/src/**\"]\npath = ~/outside\n",
		},
		{
			// Git's old dotted section spelling cannot express every modern
			// includeIf condition, and most such spellings do not currently
			// produce the exact include.path key. Reject the whole include
			// namespace anyway: accepting an obsolete spelling would make this
			// preflight depend on parser-version trivia in the Git executable.
			name:    "deprecated dotted section",
			content: "[INCLUDE.path]\nvalue = ../outside\n",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo, gitDir := gitConfigPreflightFixture(t)
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if gitMetadataSafeForSubprocess(repo) {
				t.Fatalf("repo-local config include passed metadata preflight:\n%s", tt.content)
			}
		})
	}
}

func TestGitMetadataGuardDoesNotTreatCommentOrValueAsIncludeSection(t *testing.T) {
	repo, gitDir := gitConfigPreflightFixture(t)
	content := "[core]\n\tnotes = \"literal [include] text\"\n# [include]\n; [includeIf \"gitdir:**\"]\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("commented and quoted include spellings were treated as active config sections")
	}
}

func TestGitMetadataGuardRejectsPromisorConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "partial clone extension",
			content: "[extensions]\npartialClone = origin\n",
		},
		{
			name:    "quoted remote promisor",
			content: "[remote \"origin\"]\npromisor = true\n",
		},
		{
			name:    "case-insensitive implicit promisor",
			content: " [ReMoTe \"origin\"]\r\n\tProMiSoR\r\n",
		},
		{
			name:    "deprecated dotted remote promisor",
			content: "[remote.origin]\npromisor = yes\n",
		},
		{
			name:    "partial clone filter alone",
			content: "[remote \"origin\"]\npartialCloneFilter = blob:none\n",
		},
		{
			name:    "empty partial clone filter still registers remote",
			content: "[remote \"origin\"]\npartialCloneFilter =\n",
		},
		{
			name:    "later false does not unregister promisor",
			content: "[remote \"origin\"]\npromisor = true\npromisor = false\n",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo, gitDir := gitConfigPreflightFixture(t)
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if gitMetadataSafeForSubprocess(repo) {
				t.Fatalf("promisor configuration passed metadata preflight:\n%s", tt.content)
			}
		})
	}
}

func TestGitMetadataGuardAllowsNonPromisorRemoteConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "ordinary remote",
			content: "[remote \"origin\"]\nurl = https://example.invalid/repo.git\nfetch = +refs/heads/*:refs/remotes/origin/*\n",
		},
		{
			name:    "explicitly non-promisor remote",
			content: "[remote \"origin\"]\npromisor = false\nurl = https://example.invalid/repo.git\n",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo, gitDir := gitConfigPreflightFixture(t)
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if !gitMetadataSafeForSubprocess(repo) {
				t.Fatalf("non-promisor remote configuration was refused:\n%s", tt.content)
			}
		})
	}
}

func TestGitMetadataGuardChecksOnlyActiveWorktreeConfig(t *testing.T) {
	const planted = "[include]\npath = ../outside\n"

	t.Run("disabled", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if !gitMetadataSafeForSubprocess(repo) {
			t.Fatal("inactive config.worktree include was treated as repository config")
		}
	})

	t.Run("enabled with continued boolean", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = tr\\\nue\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if gitMetadataSafeForSubprocess(repo) {
			t.Fatal("active config.worktree include passed metadata preflight")
		}
	})
}

func TestGitMetadataGuardChecksPromisorInOnlyActiveWorktreeConfig(t *testing.T) {
	const planted = "[remote \"origin\"]\npromisor = true\nurl = https://example.invalid/repo.git\n"

	t.Run("disabled", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if !gitMetadataSafeForSubprocess(repo) {
			t.Fatal("inactive config.worktree promisor was treated as repository config")
		}
	})

	t.Run("enabled", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if gitMetadataSafeForSubprocess(repo) {
			t.Fatal("active config.worktree promisor passed metadata preflight")
		}
	})
}

func TestGitMetadataGuardChecksPartialCloneExtensionInOnlyActiveWorktreeConfig(t *testing.T) {
	const planted = "[extensions]\npartialClone = origin\n"

	t.Run("disabled", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if !gitMetadataSafeForSubprocess(repo) {
			t.Fatal("inactive config.worktree partial-clone extension was treated as repository config")
		}
	})

	t.Run("enabled", func(t *testing.T) {
		repo, gitDir := gitConfigPreflightFixture(t)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[extensions]\nworktreeConfig = true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte(planted), 0o644); err != nil {
			t.Fatal(err)
		}
		if gitMetadataSafeForSubprocess(repo) {
			t.Fatal("active config.worktree partial-clone extension passed metadata preflight")
		}
	})
}

func TestGitMetadataGuardBoundsActiveLocalConfigRead(t *testing.T) {
	repo, gitDir := gitConfigPreflightFixture(t)
	content := bytes.Repeat([]byte{'#'}, maxGitFileBytes+1)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if gitMetadataSafeForSubprocess(repo) {
		t.Fatal("oversized active repository config passed bounded preflight")
	}
}

func TestGitLocalConfigPreflightRefusesFileGrownAfterOpenedStat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("[core]\nfilemode = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	info, err := opened.Stat()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("[include]\npath = ../outside\n"); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitLocalConfigPreflightFromOpened(opened, info); ok {
		t.Fatal("config grown after the opened handle's Stat was parsed as its admitted prefix")
	}
}

func TestGitMetadataGuardAllowsSafeCoreWorktree(t *testing.T) {
	repo, gitDir := gitConfigPreflightFixture(t)
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\nworktree = ..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitMetadataSafeForSubprocess(repo) {
		t.Fatal("same-filesystem core.worktree was refused")
	}
}
