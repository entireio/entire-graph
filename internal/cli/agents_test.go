package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAgentPointerBlock = agentPointerBegin + "\n" +
	"This repo has the entire-graph code graph installed. Before exploring code with\n" +
	"grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance\n" +
	"for using graph retrieval, focused source inspection, and verification.\n" +
	"@.entire/graph-agent.md\n" +
	agentPointerEnd + "\n"

const testInheritedAgentPointerBlock = agentPointerBegin + "\n" +
	"Entire Graph's agent instructions are inherited from AGENTS.md, which this file\n" +
	"already imports; the guide itself is .entire/graph-agent.md.\n" +
	agentPointerEnd + "\n"

func TestAgentGuidePrintsDoctrine(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"agent-guide"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SEARCH FIRST",
		"entire graph search",
		"--profile full",
		"VERIFY before stopping",
		"never trade resolution for fewer turns",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("agent-guide output missing %q:\n%s", want, out.String())
		}
	}
	for _, withdrawn := range []string{"54.9%", "57.7%", "roughly in half", "make the minimal edit, and STOP"} {
		if strings.Contains(out.String(), withdrawn) {
			t.Fatalf("agent-guide output retained withdrawn guidance %q:\n%s", withdrawn, out.String())
		}
	}
}

func TestInitAgentsInstallsAndIsIdempotent(t *testing.T) {
	repo := t.TempDir()
	// pre-existing AGENTS.md must be preserved, not clobbered
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# my project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	run := func() {
		t.Helper()
		if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
			t.Fatal(err)
		}
	}
	run()

	guide, err := os.ReadFile(filepath.Join(repo, ".entire", "graph-agent.md"))
	if err != nil {
		t.Fatalf("guide not written: %v", err)
	}
	if !strings.Contains(string(guide), "SEARCH FIRST") {
		t.Fatalf("guide content wrong:\n%s", guide)
	}

	agents, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "# my project rules") {
		t.Fatalf("existing AGENTS.md content clobbered:\n%s", agents)
	}
	if !strings.Contains(string(agents), agentPointerBegin) || !strings.Contains(string(agents), ".entire/graph-agent.md") {
		t.Fatalf("pointer block missing from AGENTS.md:\n%s", agents)
	}
	if !strings.Contains(string(agents), "resolution-first guidance") ||
		strings.Contains(string(agents), "roughly in half") {
		t.Fatalf("pointer block retained withdrawn doctrine:\n%s", agents)
	}

	claude, err := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	if !strings.Contains(string(claude), "@.entire/graph-agent.md") {
		t.Fatalf("CLAUDE.md missing import line:\n%s", claude)
	}

	// Idempotence includes the complete bytes, not only the marker count.
	run()
	agents2, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if !bytes.Equal(agents, agents2) {
		t.Fatalf("AGENTS.md changed on idempotent rerun:\nbefore:\n%s\nafter:\n%s", agents, agents2)
	}
	if got := strings.Count(string(agents2), agentPointerBegin); got != 1 {
		t.Fatalf("pointer block duplicated (%d occurrences):\n%s", got, agents2)
	}
	claude2, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if !bytes.Equal(claude, claude2) {
		t.Fatalf("CLAUDE.md changed on idempotent rerun:\nbefore:\n%s\nafter:\n%s", claude, claude2)
	}
	if got := strings.Count(string(claude2), agentPointerBegin); got != 1 {
		t.Fatalf("CLAUDE.md block duplicated (%d occurrences):\n%s", got, claude2)
	}
}

func TestInitAgentsMigratesClaudeToAgentsInheritance(t *testing.T) {
	repo := t.TempDir()
	claudePath := filepath.Join(repo, "CLAUDE.md")
	legacy := "# Claude-only rules\n\n@AGENTS.md\n\n" + testAgentPointerBlock + "\nKeep this footer.\n"
	if err := os.WriteFile(claudePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	runInitAgentsForTest(t, repo)
	claude := readFileForTest(t, claudePath)
	if !strings.Contains(claude, "# Claude-only rules\n") || !strings.Contains(claude, "\nKeep this footer.\n") {
		t.Fatalf("unmanaged CLAUDE.md content was not preserved:\n%s", claude)
	}
	if !strings.Contains(claude, "@AGENTS.md") {
		t.Fatalf("user-owned AGENTS.md import was removed:\n%s", claude)
	}
	if !strings.Contains(claude, testInheritedAgentPointerBlock) {
		t.Fatalf("legacy direct block was not migrated to inheritance notice:\n%s", claude)
	}
	if strings.Contains(claude, "@.entire/graph-agent.md") {
		t.Fatalf("CLAUDE.md retained a duplicate direct guide import:\n%s", claude)
	}
	agents := readFileForTest(t, filepath.Join(repo, "AGENTS.md"))
	if !strings.Contains(agents, testAgentPointerBlock) {
		t.Fatalf("AGENTS.md lost the canonical direct guide pointer:\n%s", agents)
	}

	runInitAgentsForTest(t, repo)
	if rerun := readFileForTest(t, claudePath); rerun != claude {
		t.Fatalf("migrated CLAUDE.md changed on rerun:\nbefore:\n%s\nafter:\n%s", claude, rerun)
	}
}

func TestInitAgentsRestoresClaudeDirectPointerWhenAgentsImportIsRemoved(t *testing.T) {
	repo := t.TempDir()
	claudePath := filepath.Join(repo, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Claude rules\n@AGENTS.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	if got := readFileForTest(t, claudePath); !strings.Contains(got, testInheritedAgentPointerBlock) {
		t.Fatalf("CLAUDE.md did not initially inherit through AGENTS.md:\n%s", got)
	}

	withoutImport := strings.ReplaceAll(readFileForTest(t, claudePath), "@AGENTS.md\n", "")
	if err := os.WriteFile(claudePath, []byte(withoutImport), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	got := readFileForTest(t, claudePath)
	if !strings.Contains(got, "# Claude rules\n") {
		t.Fatalf("unmanaged content was lost while restoring direct pointer:\n%s", got)
	}
	if !strings.Contains(got, testAgentPointerBlock) || strings.Contains(got, testInheritedAgentPointerBlock) {
		t.Fatalf("CLAUDE.md direct guide pointer was not restored:\n%s", got)
	}

	withImportAgain := "@./AGENTS.md\n" + got
	if err := os.WriteFile(claudePath, []byte(withImportAgain), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	got = readFileForTest(t, claudePath)
	if !strings.Contains(got, testInheritedAgentPointerBlock) || strings.Contains(got, "@.entire/graph-agent.md") {
		t.Fatalf("CLAUDE.md did not migrate back to inheritance:\n%s", got)
	}
}

func TestInitAgentsRecognizesLiveAgentsImportPathVariants(t *testing.T) {
	tests := []struct {
		name    string
		content func(string) string
	}{
		{name: "bare relative", content: func(string) string { return "@AGENTS.md\n" }},
		{name: "dot relative", content: func(string) string { return "@./AGENTS.md\n" }},
		{name: "cleaned relative", content: func(string) string { return "@subdir/../AGENTS.md\n" }},
		{name: "absolute", content: func(repo string) string { return "@" + filepath.Join(repo, "AGENTS.md") + "\n" }},
		{name: "after closed comment", content: func(string) string { return "<!-- context -->\n@AGENTS.md\n" }},
		// A `-->` with no opener is prose, not comment structure, and must not hide the import.
		{name: "prose arrow after import", content: func(string) string { return "@AGENTS.md\n\nFlow: a --> b\n" }},
		{name: "fenced arrow before import", content: func(string) string {
			return "```text\nlocate --> entire graph search\n```\n@AGENTS.md\n"
		}},
		{name: "fenced comment markers", content: func(string) string {
			return "```md\n<!-- entire-graph:end -->\n```\n@AGENTS.md\n"
		}},
		{name: "fenced unterminated comment", content: func(string) string {
			return "```md\n<!-- example\n```\n@AGENTS.md\n"
		}},
		{name: "stray end marker before import", content: func(string) string {
			return agentPointerEnd + "\n@AGENTS.md\n"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(tt.content(repo)), 0o644); err != nil {
				t.Fatal(err)
			}
			runInitAgentsForTest(t, repo)
			got := readFileForTest(t, filepath.Join(repo, "CLAUDE.md"))
			if !strings.Contains(got, testInheritedAgentPointerBlock) {
				t.Fatalf("live import variant did not select inheritance block:\n%s", got)
			}
		})
	}
}

func TestInitAgentsIgnoresNonLiveAndExcludedAgentsMentions(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "inline code", content: "Use `@AGENTS.md` as an example.\n"},
		{name: "backtick fence", content: "```md\n@AGENTS.md\n```\n"},
		{name: "tilde fence", content: "~~~md\n@AGENTS.md\n~~~\n"},
		{name: "html comment", content: "<!-- @AGENTS.md -->\n"},
		{name: "managed region", content: agentPointerBegin + "\n@AGENTS.md\n" + agentPointerEnd + "\n"},
		{name: "word prefix", content: "owner@AGENTS.md\n"},
		{name: "filename suffix", content: "@AGENTS.md.backup\n"},
		{name: "path suffix", content: "@AGENTS.md/example\n"},
		{name: "space-indented code", content: "Example:\n\n    @AGENTS.md\n"},
		{name: "tab-indented code", content: "Example:\n\n\t@AGENTS.md\n"},
		{name: "short fence cannot close", content: "````md\nexample\n```\n@AGENTS.md\n"},
		{name: "annotated fence cannot close", content: "```md\nexample\n```not-a-close\n@AGENTS.md\n"},
		{name: "unclosed code span hides later import", content: "`unfinished example\n@AGENTS.md\n"},
		{name: "unclosed fence is ambiguous", content: "@AGENTS.md\n```md\nunfinished\n"},
		{name: "unclosed comment is ambiguous", content: "@AGENTS.md\n<!-- unfinished\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			runInitAgentsForTest(t, repo)
			got := readFileForTest(t, filepath.Join(repo, "CLAUDE.md"))
			if strings.Contains(got, testInheritedAgentPointerBlock) {
				t.Fatalf("excluded or ambiguous mention selected inheritance block:\n%s", got)
			}
			if !strings.Contains(got, "@.entire/graph-agent.md") {
				t.Fatalf("safe direct guide pointer is missing:\n%s", got)
			}
		})
	}
}

func TestInitAgentsRecognizesImportWithUnrelatedInlineCode(t *testing.T) {
	repo := t.TempDir()
	content := "@AGENTS.md\nRun `go test ./...` before committing.\n"
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	got := readFileForTest(t, filepath.Join(repo, "CLAUDE.md"))
	if !strings.Contains(got, testInheritedAgentPointerBlock) {
		t.Fatalf("unrelated inline code hid the live AGENTS.md import:\n%s", got)
	}
}

func TestInitAgentsInheritanceNoticeStaysReadable(t *testing.T) {
	repo := t.TempDir()
	claudePath := filepath.Join(repo, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("@AGENTS.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	got := readFileForTest(t, claudePath)
	// A reader that does not resolve Claude's @-import syntax must still learn where the
	// instructions are, so the notice cannot be an HTML comment.
	for _, want := range []string{"inherited from AGENTS.md", ".entire/graph-agent.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("inheritance notice is missing readable pointer %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "inherited from AGENTS.md") && strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			t.Fatalf("inheritance notice is hidden inside an HTML comment:\n%s", got)
		}
	}
}

func TestInitAgentsReplacesBlockDespiteStrayEndMarker(t *testing.T) {
	repo := t.TempDir()
	claudePath := filepath.Join(repo, "CLAUDE.md")
	// An end marker with no block above it must not send every rerun down the append path.
	if err := os.WriteFile(claudePath, []byte("# rules\n"+agentPointerEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runInitAgentsForTest(t, repo)
	first := readFileForTest(t, claudePath)
	runInitAgentsForTest(t, repo)
	runInitAgentsForTest(t, repo)
	got := readFileForTest(t, claudePath)
	if got != first {
		t.Fatalf("reruns were not byte-idempotent:\nafter first:\n%s\nafter third:\n%s", first, got)
	}
	if count := strings.Count(got, agentPointerBegin); count != 1 {
		t.Fatalf("managed block written %d times:\n%s", count, got)
	}
	if !strings.Contains(got, "# rules\n") {
		t.Fatalf("unmanaged content was lost:\n%s", got)
	}
}

func TestInitAgentsWritesSameFileOnlyOnce(t *testing.T) {
	tests := []struct {
		name          string
		createAliases func(t *testing.T, repo string)
	}{
		{
			name: "CLAUDE symlinks to AGENTS",
			createAliases: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Shared rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("AGENTS.md", filepath.Join(repo, "CLAUDE.md")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "AGENTS symlinks to CLAUDE",
			createAliases: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Shared rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("CLAUDE.md", filepath.Join(repo, "AGENTS.md")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			tt.createAliases(t, repo)
			out := runInitAgentsForTest(t, repo)
			// Both configured files were updated, even though one write covered both.
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				if !strings.Contains(out, "updated "+filepath.Join(repo, name)+"\n") {
					t.Fatalf("init-agents did not report %s as updated:\n%s", name, out)
				}
			}

			agents := readFileForTest(t, filepath.Join(repo, "AGENTS.md"))
			claude := readFileForTest(t, filepath.Join(repo, "CLAUDE.md"))
			if agents != claude {
				t.Fatalf("same-file aliases diverged:\nAGENTS.md:\n%s\nCLAUDE.md:\n%s", agents, claude)
			}
			if !strings.Contains(agents, "# Shared rules\n") || !strings.Contains(agents, testAgentPointerBlock) {
				t.Fatalf("shared file did not retain content and direct pointer:\n%s", agents)
			}
			if got := strings.Count(agents, agentPointerBegin); got != 1 {
				t.Fatalf("same file was updated with %d managed blocks:\n%s", got, agents)
			}

			before := agents
			runInitAgentsForTest(t, repo)
			if after := readFileForTest(t, filepath.Join(repo, "AGENTS.md")); after != before {
				t.Fatalf("same-file rerun was not byte-idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestInitAgentsSurfacesClaudeReadErrors(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
	if err == nil || !strings.Contains(err.Error(), "CLAUDE.md") {
		t.Fatalf("init-agents error = %v, want CLAUDE.md read/stat error", err)
	}
}

func runInitAgentsForTest(t *testing.T, repo string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
