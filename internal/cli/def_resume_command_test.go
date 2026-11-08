package cli

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestDefClippedBodyHintResumesThroughPublicCommand(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable for command-tokenization check")
	}
	for _, mode := range []string{"--head", "--worktree"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			const path = "pkg/o' $((1+1)).go"
			var source strings.Builder
			source.WriteString("package example\nfunc Run() int {\n")
			for i := 0; i < 100; i++ {
				source.WriteString(" // long padding makes the body exceed the response budget\n")
			}
			source.WriteString(" return 42\n}\n")
			write(t, repo, path, source.String())
			git(t, repo, "init")
			git(t, repo, "add", ".")
			git(t, repo, "-c", "user.name=Resume Test", "-c", "user.email=resume@example.invalid", "commit", "-m", "fixture")
			write(t, repo, path, strings.ReplaceAll(source.String(), "long padding", "dirty padding"))
			run := func(args []string) string {
				t.Helper()
				var out bytes.Buffer
				if err := Run(t.Context(), Options{Version: "resume-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, append([]string{"def", "--repo", repo, "--worktree", "--no-cache"}, args...)); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			first := run([]string{"Run", mode, "--max-context-bytes", "1500"})
			_, note, ok := strings.Cut(first, "rerun with ")
			if !ok {
				t.Fatalf("fixture did not produce a resume hint: %s", first)
			}
			command, _, ok := strings.Cut(note, ", or raise")
			if !ok {
				t.Fatalf("missing hint boundary: %s", note)
			}
			// Intercept the tool name and capture the shell's argv. No installed Graph
			// binary is used: the exact decoded command goes through this build's Run.
			capture := exec.Command(shell, "-c", `entire() { printf '%s\000' "$@"; }; `+command)
			raw, err := capture.Output()
			if err != nil {
				t.Fatalf("invalid resume command %q: %v", command, err)
			}
			args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			if len(args) < 2 || args[0] != "graph" || args[1] != "def" {
				t.Fatalf("hint is not a complete def invocation: %q", command)
			}
			flags, err := parseDefFlags(args[2:])
			if err != nil || flags.File != path || flags.From <= 2 || flags.Repo != repo || flags.Worktree != (mode == "--worktree") {
				t.Fatalf("hint lost name, path, or continuation: %+v %v", flags, err)
			}
			resumed := run(args[2:])
			expectedPadding := "long padding"
			if mode == "--worktree" {
				expectedPadding = "dirty padding"
			}
			if !strings.Contains(resumed, "Run") || !strings.Contains(resumed, expectedPadding) || !strings.Contains(resumed, fmt.Sprintf("%3d→", flags.From)) {
				t.Fatalf("resume returned no body: %s", resumed)
			}
			if len(first) > 1500 {
				t.Fatalf("hint overflowed the answer: %d", len(first))
			}

		})
	}
}
