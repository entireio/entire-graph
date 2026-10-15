package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/agentsetup"
)

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
			seen := map[string]bool{}
			for _, line := range strings.Split(guide, "\n") {
				line = strings.TrimSpace(line)
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
				if seen[command] {
					t.Fatalf("duplicate example: %s", line)
				}
				seen[command] = true
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
