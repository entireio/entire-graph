package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise real revisions: accepting a path must still analyze the intended
// checkout, including linked worktrees whose .git is a file rather than a directory.
func TestRangeCommandsCheckoutScope(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "Test")
	write(t, repo, "sub/main.go", "package example\nfunc RangeScope() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	write(t, repo, "sub/main.go", "package example\nfunc RangeScope() int { return 2 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "change\n\nEntire-Checkpoint: abcdef123456")

	check := func(t *testing.T, root string) {
		t.Helper()
		for _, command := range []string{"diff", "analyze", "commit", "checkpoint"} {
			t.Run(command, func(t *testing.T) {
				for _, source := range []string{"explicit", "environment"} {
					t.Run(source, func(t *testing.T) {
						for _, subdir := range []bool{false, true} {
							selected := root
							if subdir {
								selected = filepath.Join(root, "sub")
							}
							var out bytes.Buffer
							opts := Options{Stdout: &out, Stderr: io.Discard}
							args := []string{command, "--json"}
							if source == "explicit" {
								args = append(args, "--repo", selected)
							} else {
								opts.Env.RepoRoot = selected
							}
							if command == "checkpoint" {
								args = append(args, "abcdef123456")
							}
							err := Run(t.Context(), opts, args)
							if subdir {
								if err == nil || !strings.Contains(err.Error(), "checkout root") || out.Len() != 0 {
									t.Fatalf("subdirectory: error = %v, output = %q; want refusal without output", err, out.String())
								}
							} else if err != nil || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "RangeScope") {
								t.Fatalf("checkout root: error = %v, output = %q; want analyzed RangeScope change", err, out.String())
							}
						}
					})
				}
			})
		}
	}
	t.Run("ordinary", func(t *testing.T) { check(t, repo) })
	t.Run("linked worktree", func(t *testing.T) {
		linked := filepath.Join(t.TempDir(), "linked checkout")
		git(t, repo, "worktree", "add", "--detach", linked, "HEAD")
		check(t, linked)
	})
	t.Run("symlink", func(t *testing.T) {
		requireSymlinkSupport(t)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(repo, alias); err != nil {
			t.Fatal(err)
		}
		check(t, alias)
	})
}

func TestRangeCommandsRejectSubdirectoryRepo(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	subdir := filepath.Join(repo, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := requireRangeRepoRoot(t.Context(), repo, "diff"); err != nil {
		t.Fatalf("checkout root rejected: %v", err)
	}
	for _, command := range []string{"diff", "analyze", "commit", "checkpoint"} {
		t.Run(command, func(t *testing.T) {
			args := []string{command, "--repo", subdir}
			if command == "checkpoint" {
				args = append(args, "checkpoint-id")
			}
			err := Run(t.Context(), Options{Stdout: io.Discard, Stderr: io.Discard}, args)
			if err == nil || !strings.Contains(err.Error(), "checkout root") {
				t.Fatalf("%s with a subdirectory returned %v, want checkout-root refusal", command, err)
			}
		})
	}
}
