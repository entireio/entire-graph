package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

// TestSetupStrictModePersistsAcrossBareReruns: `setup --strict` saves the mode
// in the guide's activation record — the job the removed init-agents command
// used to do — and a bare re-run inherits it rather than resetting to normal.
// `setup --normal` is the explicit way back.
func TestSetupStrictModePersistsAcrossBareReruns(t *testing.T) {
	t.Parallel()
	repo := gitRepoForTest(t, false)
	guidePath := filepath.Join(repo, agentsetup.Path)
	setup := func(extra ...string) string {
		t.Helper()
		var out bytes.Buffer
		args := append([]string{"setup", "--repo", repo, "--no-claude-hook"}, extra...)
		if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, args); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		guide, err := os.ReadFile(guidePath)
		if err != nil {
			t.Fatal(err)
		}
		return string(guide)
	}

	if guide := setup("--strict"); !strings.Contains(guide, `"mode":"strict"`) {
		t.Fatalf("--strict did not persist in the activation record:\n%s", guide)
	}
	if guide := setup(); !strings.Contains(guide, `"mode":"strict"`) {
		t.Fatalf("a bare re-run reset the saved strict mode:\n%s", guide)
	}
	if guide := setup("--normal"); strings.Contains(guide, `"mode":"strict"`) {
		t.Fatalf("--normal did not reset the saved mode:\n%s", guide)
	}
}

// The nested-repo warning interpolates the outer path into a SUGGESTED COMMAND.
// A path with a space -- ordinary on macOS -- would make an unquoted remedy
// silently do the wrong thing when copied, and one with $ or a backtick would
// expand rather than be passed through.
func TestNestedRepoWarningQuotesThePathInItsSuggestedCommand(t *testing.T) {
	t.Parallel()

	for name, dir := range map[string]string{
		"space":        "/Users/me/My Projects/outer",
		"dollar":       "/srv/$HOME/outer",
		"backtick":     "/srv/`id`/outer",
		"semicolon":    "/srv/a;rm -rf x/outer",
		"single quote": "/srv/it's/outer",
		"glob":         "/srv/a*/outer",
	} {
		t.Run(name, func(t *testing.T) {
			got := shellQuotedRepoDir(dir)
			if got == dir {
				t.Fatalf("%q was passed through unquoted into a copy-pasteable command", dir)
			}
			if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
				t.Errorf("want a single-quoted token, got %s", got)
			}
			// A bare ' must be carried through as '\'' or the quoting itself
			// breaks the command.
			if strings.Contains(dir, "'") && !strings.Contains(got, `'\''`) {
				t.Errorf("an embedded single quote must be escaped: %s", got)
			}
		})
	}

	// An ordinary path stays bare, so the common case reads naturally.
	if got := shellQuotedRepoDir("/srv/outer"); got != "/srv/outer" {
		t.Errorf("an ordinary path must not be quoted, got %s", got)
	}
	if got := shellQuotedRepoDir("  "); got != "." {
		t.Errorf("an empty path must fall back to the cwd, got %s", got)
	}
}

// END-TO-END: the warning setup actually prints must carry the quoted form.
// The helper being correct is not the same as the call site using it.
func TestNestedRepoWarningUsesTheQuotedForm(t *testing.T) {
	t.Parallel()
	outer := filepath.Join(t.TempDir(), "My Projects", "outer")
	inner := filepath.Join(outer, "inner")
	for _, d := range []string{filepath.Join(outer, ".git"), filepath.Join(inner, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Non-vacuity: the outer path must actually contain a space, or nothing is
	// being tested.
	if !strings.Contains(outer, " ") {
		t.Fatalf("fixture path has no space: %s", outer)
	}

	// NO --repo: the warning fires only when no explicit --repo is given
	// (agents.go), so passing it is exactly how this test would silently skip
	// itself. The repository root reaches the command through the host CLI's
	// environment instead, and the warning must print before anything else
	// refuses or writes.
	var out bytes.Buffer
	_ = Run(context.Background(), Options{Stdout: &out, Stderr: &out, Env: EntireEnv{RepoRoot: inner}}, []string{"setup"})

	got := out.String()
	if !strings.Contains(got, "git repository inside") {
		t.Fatalf("the nested-repo warning did not fire, so this guard measures nothing:\n%s", got)
	}
	if strings.Contains(got, "--repo "+outer+"\n") {
		t.Errorf("the suggested command carries the path unquoted:\n%s", got)
	}
	if !strings.Contains(got, "entire graph setup --repo '"+outer+"'") {
		t.Errorf("the suggested command must carry the quoted path:\n%s", got)
	}

	// An explicit --repo is consent to the nested target: the warning stays
	// quiet.
	var quiet bytes.Buffer
	_ = Run(context.Background(), Options{Stdout: &quiet, Stderr: &quiet}, []string{"setup", "--repo", inner})
	if strings.Contains(quiet.String(), "git repository inside") {
		t.Errorf("an explicit --repo must silence the nested-repo warning:\n%s", quiet.String())
	}
}

// A project under a git-tracked HOME (dotfiles) is not a nested clone: the
// outer repository is the home directory, and suggesting `setup --repo $HOME`
// would misfire on every project of everyone who tracks their dotfiles. The
// warning stays quiet for a home-directory outer repo and keeps firing for any
// deeper nesting.
func TestNestedRepoWarningSkipsAGitTrackedHome(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "code", "project")
	for _, d := range []string{filepath.Join(home, ".git"), filepath.Join(project, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// os.UserHomeDir reads USERPROFILE on Windows and HOME elsewhere.
	homeEnv := "HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "USERPROFILE"
	}
	t.Setenv(homeEnv, home)

	var out bytes.Buffer
	_ = Run(context.Background(), Options{Stdout: &out, Stderr: &out, Env: EntireEnv{RepoRoot: project}}, []string{"setup"})
	if strings.Contains(out.String(), "git repository inside") {
		t.Errorf("a git-tracked home must not trigger the nested-repo warning:\n%s", out.String())
	}

	// Non-vacuity, and the boundary: an outer repo BELOW home still warns.
	outer := filepath.Join(home, "work", "monorepo")
	inner := filepath.Join(outer, "clone")
	for _, d := range []string{filepath.Join(outer, ".git"), filepath.Join(inner, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var warned bytes.Buffer
	_ = Run(context.Background(), Options{Stdout: &warned, Stderr: &warned, Env: EntireEnv{RepoRoot: inner}}, []string{"setup"})
	if !strings.Contains(warned.String(), "git repository inside") {
		t.Errorf("an outer repository below home must still warn:\n%s", warned.String())
	}
}

// init-agents is removed, not aliased, and the error must say where it went:
// a caller with an older script would otherwise diagnose a typo that is not
// one.
func TestInitAgentsErrorNamesItsReplacement(t *testing.T) {
	t.Parallel()
	err := Run(context.Background(), Options{}, []string{"init-agents"})
	if err == nil || !strings.Contains(err.Error(), `"entire graph setup"`) {
		t.Fatalf("init-agents must fail with a pointer to setup, got: %v", err)
	}
}
