package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

// The preview is the installer's render path. Keep the version-controlled
// activation artifact in sync so existing and newly initialized agents agree.
func TestCheckedInGuideMatchesInstallerPreview(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(repo, ".entire", "agent-guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(t.Context(), Options{Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, []string{"agent-guide", "--repo", repo, "--normal"}); err != nil {
		t.Fatal(err)
	}
	// Git may check documentation out with CRLF on Windows; compare its text.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatal("checked-in guide differs from the installer preview; regenerate it with agent-guide")
	}
}

// Execute the complete shipped examples, rather than accepting their flags one
// at a time. Placeholder substitution is the only adaptation besides pointing
// --repo at the isolated committed fixture.
func TestShippedGraphGuideCommandsExecuteAgainstCommittedSource(t *testing.T) {
	repo := t.TempDir()
	write(t, repo, "main.go", "package example\nfunc Needle(value int) int { return value + 1 }\nfunc Caller() int { return Needle(1) }\n")
	git(t, repo, "init")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=Guide Test", "-c", "user.email=guide@example.invalid", "commit", "-m", "fixture")
	for name, guide := range map[string]string{"graph": agentsetup.GraphGuide, "combined": agentsetup.CombinedGuide} {
		t.Run(name, func(t *testing.T) {
			seen := map[string]string{}
			for _, line := range strings.Split(guide, "\n") {
				line = strings.TrimSpace(line)
				// The combined guide ships the query inside an ordered sequence
				// ("2. entire graph query ..."); the command is the rest of the line.
				line = strings.TrimPrefix(strings.TrimPrefix(line, "1. "), "2. ")
				if !strings.HasPrefix(line, "entire graph ") {
					continue
				}
				line = strings.ReplaceAll(line, `"<task>"`, "Needle")
				line = strings.ReplaceAll(line, "<Name>", "Needle")
				args := strings.Fields(strings.TrimPrefix(line, "entire graph "))
				for i := 1; i < len(args); i++ {
					if args[i-1] == "--repo" {
						args[i] = repo
					}
				}
				command := args[0]
				if command != "query" && command != "def" && command != "neighbors" && command != "impact" {
					t.Fatalf("unexpected shipped example: %s", line)
				}
				// The combined guide shows the query line twice on purpose (as
				// sequence step 2 and again on the every-question list). A repeat
				// must be the SAME line; two different spellings of one verb would
				// leave an agent guessing which to follow.
				if previous, ok := seen[command]; ok {
					if previous != line {
						t.Fatalf("conflicting examples for %s: %q vs %q", command, previous, line)
					}
					continue
				}
				seen[command] = line
				var out bytes.Buffer
				if err := Run(t.Context(), Options{Version: "guide-test", Env: EntireEnv{RepoRoot: repo}, Stdout: &out}, args); err != nil {
					t.Fatalf("%s: %v", line, err)
				}
				if out.Len() == 0 || out.Len() > 4096 || !strings.Contains(out.String(), "Needle") {
					t.Fatalf("%s: unusable or oversized answer (%d bytes): %s", line, out.Len(), out.String())
				}
			}
			if len(seen) != 4 {
				t.Fatalf("executed %d examples, want all four", len(seen))
			}
		})
	}
}
