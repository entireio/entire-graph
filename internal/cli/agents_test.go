package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	"<!-- Entire Graph instructions are inherited through AGENTS.md. -->\n" +
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

func TestInitAgentsWritesSameFileOnlyOnce(t *testing.T) {
	tests := []struct {
		name          string
		createAliases func(t *testing.T, repo string)
	}{
		{
			name: "CLAUDE symlinks to AGENTS",
			createAliases: func(t *testing.T, repo string) {
				t.Helper()
				skipIfSymlinksUnrepresentable(t)
				if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Shared rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				symlinkForTest(t, "AGENTS.md", filepath.Join(repo, "CLAUDE.md"))
			},
		},
		{
			name: "AGENTS symlinks to CLAUDE",
			createAliases: func(t *testing.T, repo string) {
				t.Helper()
				skipIfSymlinksUnrepresentable(t)
				if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Shared rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				symlinkForTest(t, "CLAUDE.md", filepath.Join(repo, "AGENTS.md"))
			},
		},
		{
			name: "CLAUDE hard-links to AGENTS",
			createAliases: func(t *testing.T, repo string) {
				t.Helper()
				agentsPath := filepath.Join(repo, "AGENTS.md")
				if err := os.WriteFile(agentsPath, []byte("# Shared rules\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(agentsPath, filepath.Join(repo, "CLAUDE.md")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			tt.createAliases(t, repo)
			runInitAgentsForTest(t, repo)

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

func TestInitAgentsValidatePointerMarkersRequiresOneOrderedPair(t *testing.T) {
	valid := []string{
		"# No managed markers\n",
		agentPointerBegin + agentPointerEnd,
		"prefix\n" + agentPointerBegin + "\nmanaged\n" + agentPointerEnd + "\nsuffix\n",
	}
	for i, content := range valid {
		if _, _, err := validatePointerMarkers("VALID.md", []byte(content)); err != nil {
			t.Errorf("valid case %d rejected: %v", i, err)
		}
	}

	invalid := map[string]string{
		"missing begin":     agentPointerEnd,
		"missing end":       agentPointerBegin,
		"reversed":          agentPointerEnd + "\n" + agentPointerBegin,
		"duplicate begin":   agentPointerBegin + "\n" + agentPointerBegin + "\n" + agentPointerEnd,
		"duplicate end":     agentPointerBegin + "\n" + agentPointerEnd + "\n" + agentPointerEnd,
		"nested":            agentPointerBegin + "\n" + agentPointerBegin + "\n" + agentPointerEnd + "\n" + agentPointerEnd,
		"multiple blocks":   agentPointerBegin + agentPointerEnd + agentPointerBegin + agentPointerEnd,
		"marker in example": "example: `" + agentPointerBegin + "`",
	}
	for name, content := range invalid {
		t.Run(name, func(t *testing.T) {
			_, _, err := validatePointerMarkers("BROKEN.md", []byte(content))
			if err == nil {
				t.Fatal("malformed markers were accepted")
			}
			for _, want := range []string{"BROKEN.md", "back up", "preserve user-owned text", "rerun init-agents"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing actionable text %q", err, want)
				}
			}
		})
	}
}

func TestInitAgentsRejectsMalformedMarkersWithoutWrites(t *testing.T) {
	tests := []struct {
		name      string
		malformed string
	}{
		{name: "AGENTS.md", malformed: agentPointerBegin + "\nunterminated\n"},
		{name: "CLAUDE.md", malformed: agentPointerEnd + "\n" + agentPointerBegin + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			guidePath := filepath.Join(repo, ".entire", "graph-agent.md")
			if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{
				guidePath:                        []byte("sentinel guide\n"),
				filepath.Join(repo, "AGENTS.md"): []byte("# Agent sentinel\n"),
				filepath.Join(repo, "CLAUDE.md"): []byte("# Claude sentinel\n"),
			}
			files[filepath.Join(repo, tt.name)] = []byte(tt.malformed)
			for path, content := range files {
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents accepted malformed markers")
			}
			for _, want := range []string{tt.name, "malformed", "back up", "rerun init-agents"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err, want)
				}
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before validation completed: %q", stdout.String())
			}
			for path, want := range files {
				if got := readFileForTest(t, path); got != string(want) {
					t.Fatalf("%s changed after validation error:\nwant: %q\n got: %q", path, want, got)
				}
			}
		})
	}
}

func TestInitAgentsMalformedMarkersDoNotCreateMissingOutputs(t *testing.T) {
	for _, malformedName := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(malformedName, func(t *testing.T) {
			repo := t.TempDir()
			malformedPath := filepath.Join(repo, malformedName)
			malformed := []byte(agentPointerBegin + "\nmissing end\n")
			if err := os.WriteFile(malformedPath, malformed, 0o644); err != nil {
				t.Fatal(err)
			}
			counterpartName := "CLAUDE.md"
			if malformedName == "CLAUDE.md" {
				counterpartName = "AGENTS.md"
			}

			var stdout bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &bytes.Buffer{}}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents accepted malformed markers")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before validation completed: %q", stdout.String())
			}
			if got := readFileForTest(t, malformedPath); got != string(malformed) {
				t.Fatalf("malformed source changed: %q", got)
			}
			for _, path := range []string{
				filepath.Join(repo, ".entire", "graph-agent.md"),
				filepath.Join(repo, counterpartName),
			} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("%s was created despite validation error (stat error %v)", path, statErr)
				}
			}
		})
	}
}

func TestInitAgentsRejectsNonRegularInstructionFileWithoutWrites(t *testing.T) {
	for _, invalidName := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(invalidName, func(t *testing.T) {
			repo := t.TempDir()
			guidePath := filepath.Join(repo, ".entire", "graph-agent.md")
			if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
				t.Fatal(err)
			}
			guide := []byte("sentinel guide\n")
			if err := os.WriteFile(guidePath, guide, 0o644); err != nil {
				t.Fatal(err)
			}
			counterpartName := "CLAUDE.md"
			if invalidName == "CLAUDE.md" {
				counterpartName = "AGENTS.md"
			}
			counterpartPath := filepath.Join(repo, counterpartName)
			counterpart := []byte("# Counterpart sentinel\n")
			if err := os.WriteFile(counterpartPath, counterpart, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(repo, invalidName), 0o755); err != nil {
				t.Fatal(err)
			}

			var stdout bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &bytes.Buffer{}}, []string{"init-agents", "--repo", repo})
			// "directory", not the permission-bit form: the message has to say what
			// is in the way for it to be actionable.
			if err == nil || !strings.Contains(err.Error(), invalidName) ||
				!strings.Contains(err.Error(), "regular file") || !strings.Contains(err.Error(), "found directory") {
				t.Fatalf("init-agents error = %v, want %s regular-file error", err, invalidName)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before inspection completed: %q", stdout.String())
			}
			if got := readFileForTest(t, guidePath); got != string(guide) {
				t.Fatalf("guide changed after inspection error: %q", got)
			}
			if got := readFileForTest(t, counterpartPath); got != string(counterpart) {
				t.Fatalf("counterpart changed after inspection error: %q", got)
			}
		})
	}
}

func TestInitAgentsCreatesDanglingSharedAliasTargetOnce(t *testing.T) {
	tests := []struct {
		name   string
		link   string
		target string
	}{
		{name: "CLAUDE symlinks to AGENTS", link: "CLAUDE.md", target: "AGENTS.md"},
		{name: "AGENTS symlinks to CLAUDE", link: "AGENTS.md", target: "CLAUDE.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipIfSymlinksUnrepresentable(t)
			repo := t.TempDir()
			linkPath := filepath.Join(repo, tt.link)
			symlinkForTest(t, tt.target, linkPath)

			runInitAgentsForTest(t, repo)
			agents := readFileForTest(t, filepath.Join(repo, "AGENTS.md"))
			claude := readFileForTest(t, filepath.Join(repo, "CLAUDE.md"))
			if agents != claude || strings.Count(agents, agentPointerBegin) != 1 {
				t.Fatalf("shared target was not created exactly once:\nAGENTS.md:\n%s\nCLAUDE.md:\n%s", agents, claude)
			}
			if info, err := os.Lstat(linkPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("instruction alias was not preserved: info=%v error=%v", info, err)
			}

			before := agents
			runInitAgentsForTest(t, repo)
			if after := readFileForTest(t, filepath.Join(repo, "AGENTS.md")); after != before {
				t.Fatalf("dangling-alias rerun was not byte-idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// skipIfSymlinksUnrepresentable marks tests that need runtime symlink support. Once this probe
// succeeds, fixture creation uses symlinkForTest and any later failure is a real test failure. Do
// not skip Windows wholesale, because Developer Mode and CI can provide symlinks there and its
// os.Root/path semantics need the same coverage.
func skipIfSymlinksUnrepresentable(t *testing.T) {
	t.Helper()
	probeDir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(probeDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestInitAgentsRefusesInstructionAliasEscapingRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	for _, aliasName := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(aliasName, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			victimPath := filepath.Join(base, "victim.md")
			victim := []byte("# outside the repository\n")
			if err := os.WriteFile(victimPath, victim, 0o644); err != nil {
				t.Fatal(err)
			}
			symlinkForTest(t, filepath.Join("..", "victim.md"), filepath.Join(repo, aliasName))

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatalf("init-agents followed %s out of the repository", aliasName)
			}
			if !strings.Contains(err.Error(), aliasName) {
				t.Fatalf("error %q does not name the offending alias %s", err, aliasName)
			}
			if !strings.Contains(err.Error(), "leaves the repository") {
				t.Fatalf("a real escape lost its containment wording: %v", err)
			}
			if got := readFileForTest(t, victimPath); got != string(victim) {
				t.Fatalf("a file outside the repository was written:\nwant: %q\n got: %q", victim, got)
			}
			if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the guide was written despite the containment failure (stat error %v)", statErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before containment was established: %q", stdout.String())
			}
		})
	}
}

// TestInitAgentsFollowsInstructionAliasInsideRepository pins the other half of the containment
// rule. Refusing symlinks outright, or confining writes with a root that rejects every symlinked
// component, would also break this shape, which is legitimate: the link never leaves the project.
func TestInitAgentsFollowsInstructionAliasInsideRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	shared := filepath.Join(repo, "docs", "shared.md")
	if err := os.Mkdir(filepath.Dir(shared), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, []byte("# Shared rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(repo, "AGENTS.md")
	symlinkForTest(t, filepath.Join("docs", "shared.md"), aliasPath)

	runInitAgentsForTest(t, repo)

	got := readFileForTest(t, shared)
	if !strings.Contains(got, "# Shared rules\n") || !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("in-repo alias target did not receive the managed block:\n%s", got)
	}
	if info, err := os.Lstat(aliasPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("in-repo alias was replaced instead of followed: info=%v error=%v", info, err)
	}

	before := got
	runInitAgentsForTest(t, repo)
	if after := readFileForTest(t, shared); after != before {
		t.Fatalf("in-repo alias rerun was not byte-idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestInitAgentsRefusesGuideAliasEscapingRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name string
		// plant installs the escaping alias and returns the path outside the repository
		// that the unconfined write reaches.
		plant func(t *testing.T, base, repo string) string
	}{
		{
			name: "guide directory alias",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				outside := filepath.Join(base, "outside")
				if err := os.Mkdir(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				victimPath := filepath.Join(outside, "graph-agent.md")
				if err := os.WriteFile(victimPath, []byte("# outside the repository\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				symlinkForTest(t, filepath.Join("..", "outside"), filepath.Join(repo, ".entire"))
				return victimPath
			},
		},
		{
			name: "guide file alias",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				victimPath := filepath.Join(base, "victim.md")
				if err := os.WriteFile(victimPath, []byte("# outside the repository\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(repo, ".entire"), 0o755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(repo, ".entire", "graph-agent.md")
				symlinkForTest(t, filepath.Join("..", "..", "victim.md"), link)
				return victimPath
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			victimPath := tt.plant(t, base, repo)
			victim := readFileForTest(t, victimPath)

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents followed the guide alias out of the repository")
			}
			if !strings.Contains(err.Error(), "leaves the repository") {
				t.Fatalf("a real escape lost its containment wording: %v", err)
			}
			if got := readFileForTest(t, victimPath); got != victim {
				t.Fatalf("a file outside the repository was written:\nwant: %q\n got: %q", victim, got)
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				if _, statErr := os.Lstat(filepath.Join(repo, name)); !os.IsNotExist(statErr) {
					t.Fatalf("%s was written despite the containment failure (stat error %v)", name, statErr)
				}
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before containment was established: %q", stdout.String())
			}
		})
	}
}

// skipIfDirectoryPermissionsUnenforceable guards the fixtures that make a directory unreadable
// in order to produce a genuine operational stat failure. os.Chmod on Windows only toggles the
// read-only bit and cannot withdraw traverse rights from a directory, so mode 0 there leaves it
// perfectly readable and the fixture would assert nothing; root ignores the bits for the same
// reason.
func skipIfDirectoryPermissionsUnenforceable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod cannot withdraw directory traverse permission on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not constrain root")
	}
}

// TestInitAgentsReportsUnreadableTargetAsOperationalFailure pins the half of the containment
// preflight that is not about security. os.Root answers "I could not resolve this" for an escape
// and for an ordinary I/O failure alike, and its escape sentinel is unexported, so a preflight
// that reads every non-ENOENT stat error as an escape tells whoever hit a plain EACCES that their
// repository contains a link leading out of the project. There is no link in this repository at
// all, and the cause the reader actually needs — the mode of .entire — must survive in the error.
func TestInitAgentsReportsUnreadableTargetAsOperationalFailure(t *testing.T) {
	skipIfDirectoryPermissionsUnenforceable(t)
	repo := t.TempDir()
	guideDir := filepath.Join(repo, ".entire")
	if err := os.Mkdir(guideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(guideDir, 0o000); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's cleanup cannot remove a directory it is not allowed to traverse.
	t.Cleanup(func() { _ = os.Chmod(guideDir, 0o755) })

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents reported success for a write target it could not inspect")
	}
	if strings.Contains(err.Error(), "leaves the repository") {
		t.Fatalf("an unreadable directory was reported as a repository escape: %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error dropped the operating system cause: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(repo, ".entire", "graph-agent.md")) {
		t.Fatalf("error does not name the target that could not be inspected: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before the write targets were established: %q", stdout.String())
	}
}

// TestWriteContainedFileRefusesEscapeWithoutPreflight pins the property that lets
// ensureContainedInRepo hand back an unclassifiable operational failure as itself: containment is
// enforced by the write, not by the preflight stat. Called with no preflight at all, on exactly
// the aliases the security tests plant, the confined write still refuses to leave the repository
// and creates nothing outside it.
func TestWriteContainedFileRefusesEscapeWithoutPreflight(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	victimPath := filepath.Join(base, "victim.md")
	victim := []byte("# outside the repository\n")
	if err := os.WriteFile(victimPath, victim, 0o644); err != nil {
		t.Fatal(err)
	}
	absentPath := filepath.Join(base, "absent.md")
	symlinkForTest(t, filepath.Join("..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
	symlinkForTest(t, filepath.Join("..", "absent.md"), filepath.Join(repo, "CLAUDE.md"))

	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	for _, name := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join("..", "victim.md")} {
		if err := writeContainedFile(root, name, []byte("overwritten"), 0o644); err == nil {
			t.Fatalf("writeContainedFile(%q) followed the alias out of the repository", name)
		}
	}
	if got := readFileForTest(t, victimPath); got != string(victim) {
		t.Fatalf("a file outside the repository was written:\nwant: %q\n got: %q", victim, got)
	}
	if _, err := os.Lstat(absentPath); !os.IsNotExist(err) {
		t.Fatalf("a file outside the repository was created (stat error %v)", err)
	}
}

func runInitAgentsForTest(t *testing.T, repo string) {
	t.Helper()
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestInitAgentsFollowsAbsoluteAliasInsideRepository pins the half of the containment rule that
// os.Root cannot express on its own. os.Root resolves each component with openat relative to the
// opened directory, so a symlink whose target is spelled as an absolute path has no in-root
// starting point and is refused as an escape — even when that absolute path names a file inside
// the very repository being installed into. That alias is legitimate, worked before init-agents
// was confined, and is the AGENTS.md/CLAUDE.md sharing docs/trust-and-security.md permits.
// Confinement must judge where a link LANDS, not how it is spelled.
func TestInitAgentsFollowsAbsoluteAliasInsideRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name string
		// plant installs the absolute in-repository alias and returns the path inside the
		// repository the install has to reach through it.
		plant func(t *testing.T, repo string) string
	}{
		{
			name: "AGENTS.md alias",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "docs", "shared.md")
				mkdirAllForTest(t, filepath.Dir(shared))
				writeFileForTest(t, shared, "# Shared rules\n")
				symlinkForTest(t, shared, filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name: "CLAUDE.md alias",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "docs", "claude.md")
				mkdirAllForTest(t, filepath.Dir(shared))
				writeFileForTest(t, shared, "# Claude rules\n")
				symlinkForTest(t, shared, filepath.Join(repo, "CLAUDE.md"))
				return shared
			},
		},
		{
			name: "guide directory alias",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				real := filepath.Join(repo, "tooling")
				mkdirAllForTest(t, real)
				symlinkForTest(t, real, filepath.Join(repo, ".entire"))
				return filepath.Join(real, "graph-agent.md")
			},
		},
		{
			name: "guide file alias",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				guide := filepath.Join(repo, "tooling", "guide.md")
				mkdirAllForTest(t, filepath.Dir(guide))
				writeFileForTest(t, guide, "")
				mkdirAllForTest(t, filepath.Join(repo, ".entire"))
				symlinkForTest(t, guide, filepath.Join(repo, ".entire", "graph-agent.md"))
				return guide
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			landing := tt.plant(t, repo)

			runInitAgentsForTest(t, repo)

			if got := readFileForTest(t, landing); !strings.Contains(got, "entire-graph") {
				t.Fatalf("absolute in-repository alias target %s never received the install:\n%s", landing, got)
			}
		})
	}
}

// TestInitAgentsFollowsAliasChainReachingAnAbsoluteHop pins that confinement judges where the
// WHOLE chain lands. An absolute in-repository hop reached through a relative one is still an
// in-repository alias, and stopping the rewrite at the relative hop would hand os.Root the
// absolute link it cannot resolve and turn a working project into a refusal.
func TestInitAgentsFollowsAliasChainReachingAnAbsoluteHop(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name string
		// plant installs the chain and returns the in-repository path it lands on.
		plant func(t *testing.T, repo string) string
	}{
		{
			name: "relative hop then absolute hop",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "docs", "shared.md")
				mkdirAllForTest(t, filepath.Dir(shared))
				writeFileForTest(t, shared, "# Shared rules\n")
				symlinkForTest(t, shared, filepath.Join(repo, "relay.md"))
				symlinkForTest(t, "relay.md", filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name: "absolute hop then relative hop",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "docs", "shared.md")
				mkdirAllForTest(t, filepath.Dir(shared))
				writeFileForTest(t, shared, "# Shared rules\n")
				symlinkForTest(t, "shared.md", filepath.Join(repo, "docs", "relay.md"))
				symlinkForTest(t, filepath.Join(repo, "docs", "relay.md"), filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name: "absolute hop reached through a symlinked directory",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "real", "nested", "shared.md")
				mkdirAllForTest(t, filepath.Dir(shared))
				writeFileForTest(t, shared, "# Shared rules\n")
				symlinkForTest(t, filepath.Join("real", "nested"), filepath.Join(repo, "docs"))
				symlinkForTest(t, shared, filepath.Join(repo, "docs", "alias.md"))
				symlinkForTest(t, filepath.Join("docs", "alias.md"), filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name: "absolute hop then a parent-relative hop",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "shared.md")
				writeFileForTest(t, shared, "# Shared rules\n")
				mkdirAllForTest(t, filepath.Join(repo, "docs"))
				symlinkForTest(t, filepath.Join("..", "shared.md"), filepath.Join(repo, "docs", "up.md"))
				symlinkForTest(t, filepath.Join(repo, "docs", "up.md"), filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			landing := tt.plant(t, repo)

			runInitAgentsForTest(t, repo)

			if got := readFileForTest(t, landing); !strings.Contains(got, testAgentPointerBlock) {
				t.Fatalf("chain landing %s did not receive the managed block:\n%s", landing, got)
			}
		})
	}
}

// TestInitAgentsFollowsGuideDirectoryAliasToRepositoryRoot pins the alias whose absolute target is
// the project directory itself. That resolves to "." — inside the repository, not an escape — and
// the components after it still have to be appended, so the guide lands at the repository root
// exactly as it did before init-agents was confined.
func TestInitAgentsFollowsGuideDirectoryAliasToRepositoryRoot(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	symlinkForTest(t, repo, filepath.Join(repo, ".entire"))

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, filepath.Join(repo, "graph-agent.md")); !strings.Contains(got, "entire-graph") {
		t.Fatalf("guide did not land at the repository root through the alias:\n%s", got)
	}
	if got := readFileForTest(t, filepath.Join(repo, "AGENTS.md")); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("AGENTS.md did not receive the managed block:\n%s", got)
	}
}

func TestInitAgentsCreatesDanglingGuideDirectoryAliasParents(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	baseTarget := filepath.Join("tooling", "generated", "entire")
	for _, tt := range []struct {
		name   string
		target string
	}{
		{name: "plain target", target: baseTarget},
		{name: "trailing separator", target: rawJoin(baseTarget, "")},
		{name: "terminal dot", target: rawJoin(baseTarget, ".")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			// Windows records whether a symlink targets a file or directory when
			// the link is created. Establish the directory type first, then remove
			// the target so every platform exercises the same dangling directory
			// alias that MkdirAll is expected to recreate.
			mkdirAllForTest(t, filepath.Join(repo, baseTarget))
			symlinkForTest(t, tt.target, filepath.Join(repo, ".entire"))
			if err := os.RemoveAll(filepath.Join(repo, "tooling")); err != nil {
				t.Fatal(err)
			}

			runInitAgentsForTest(t, repo)

			guide := filepath.Join(repo, baseTarget, "graph-agent.md")
			if got := readFileForTest(t, guide); !strings.Contains(got, "entire-graph") {
				t.Fatalf("guide was not created through the dangling directory alias:\n%s", got)
			}
		})
	}
}

func TestInitAgentsAcceptsParentCreatedByPlannedMkdirAll(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	mkdirAllForTest(t, filepath.Join(repo, "nested", "activation"))
	symlinkForTest(t, filepath.Join("nested", "activation"), filepath.Join(repo, ".entire"))
	if err := os.RemoveAll(filepath.Join(repo, "nested")); err != nil {
		t.Fatal(err)
	}
	symlinkForTest(t, filepath.Join("nested", "shared.md"), filepath.Join(repo, "AGENTS.md"))

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, filepath.Join(repo, "nested", "shared.md")); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("instruction target whose parent was created by MkdirAll was not updated:\n%s", got)
	}
}

// TestInitAgentsFollowsAbsoluteAliasThroughSymlinkedRoot covers the same alias when --repo names
// the project through a symlinked parent, so the absolute link target and the opened root are two
// different spellings of one directory. Comparing them as strings alone reads the alias as an
// escape; on macOS the same mismatch arises for free between /tmp and /private/tmp.
func TestInitAgentsFollowsAbsoluteAliasThroughSymlinkedRoot(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	mkdirAllForTest(t, filepath.Join(real, "docs"))
	shared := filepath.Join(real, "docs", "shared.md")
	writeFileForTest(t, shared, "# Shared rules\n")
	symlinkForTest(t, shared, filepath.Join(real, "AGENTS.md"))
	alias := filepath.Join(base, "alias")
	symlinkForTest(t, real, alias)

	runInitAgentsForTest(t, alias)

	if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("alias target did not receive the managed block through a symlinked root:\n%s", got)
	}
}

func TestInitAgentsFollowsPOSIXAbsoluteAliasWithDoubleLeadingSlash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX double-leading-slash spelling")
	}
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	shared := filepath.Join(repo, "shared.md")
	writeFileForTest(t, shared, "# Shared rules\n")
	target := string(filepath.Separator) + shared
	symlinkForTest(t, target, filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Skipf("the host does not resolve the double-leading-slash spelling: %v", err)
	}

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("double-leading-slash alias did not receive the managed block:\n%s", got)
	}
}

// TestInitAgentsFollowsAbsoluteAliasThroughDirectoryAlias verifies containment by where the
// target lands, even when the absolute spelling reaches a repository subdirectory through an
// alias outside the repository. No textual ancestor of that spelling is the project root.
func TestInitAgentsFollowsAbsoluteAliasThroughDirectoryAlias(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	for _, existing := range []bool{true, false} {
		name := "missing target"
		if existing {
			name = "existing target"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			shared := filepath.Join(repo, "docs", "shared.md")
			mkdirAllForTest(t, filepath.Dir(shared))
			if existing {
				writeFileForTest(t, shared, "# Shared rules\n")
			}
			docsAlias := filepath.Join(base, "docs-alias")
			symlinkForTest(t, filepath.Join(repo, "docs"), docsAlias)
			symlinkForTest(t, filepath.Join(docsAlias, "shared.md"), filepath.Join(repo, "AGENTS.md"))

			runInitAgentsForTest(t, repo)

			if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
				t.Fatalf("absolute alias through a directory alias did not receive the managed block:\n%s", got)
			}
		})
	}
}

func TestInitAgentsRefusesUntraversableSuffixAfterDirectoryAlias(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	skipIfDirectoryPermissionsUnenforceable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	docs := filepath.Join(repo, "docs")
	closed := filepath.Join(docs, "closed")
	mkdirAllForTest(t, closed)
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	victim := filepath.Join(docs, "victim.md")
	writeFileForTest(t, victim, "# an unrelated repository file\n")
	docsAlias := filepath.Join(base, "docs-alias")
	symlinkForTest(t, docs, docsAlias)
	symlinkForTest(t, rawJoin(docsAlias, "closed", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
	before := readFileForTest(t, victim)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents collapsed an untraversable suffix after a directory alias")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("an unrelated repository file was rewritten:\nwant: %q\n got: %q", before, got)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

// TestInitAgentsPreservesTerminalSeparatorAfterAbsoluteAliasMapping covers a directory
// requirement carried entirely by the suffix that remains after an external alias is mapped
// into the repository. Dropping that suffix turns a path the kernel rejects into permission to
// rewrite the regular file at which the external alias lands.
func TestInitAgentsPreservesTerminalSeparatorAfterAbsoluteAliasMapping(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	victim := filepath.Join(repo, ".git", "config")
	mkdirAllForTest(t, filepath.Dir(victim))
	before := "[core]\n\tbare = false\n"
	writeFileForTest(t, victim, before)
	fileAlias := filepath.Join(base, "config-alias")
	symlinkForTest(t, victim, fileAlias)
	symlinkForTest(t, rawJoin(fileAlias, ""), filepath.Join(repo, "AGENTS.md"))
	if err := kernelReachesRawPath(t, repo, "AGENTS.md"); err == nil {
		target, readErr := os.Readlink(filepath.Join(repo, "AGENTS.md"))
		t.Skipf("the host resolves the trailing-separator alias %q (readlink error %v)", target, readErr)
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents dropped a trailing separator while mapping an absolute alias")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("the mapped regular file was rewritten:\nwant: %q\n got: %q", before, got)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

// TestInitAgentsRefusesAbsoluteAliasLeavingRepository is the other side of the rewrite above: an
// absolute target must still be refused when it lands outside, including when it lands on a
// second link that leaves. Nothing outside the repository may be touched, and no partial install
// may be left behind.
func TestInitAgentsRefusesAbsoluteAliasLeavingRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name string
		// plant installs the hostile alias and returns the outside path it aims at.
		plant func(t *testing.T, base, repo string) string
	}{
		{
			name: "absolute target outside",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				victim := filepath.Join(base, "victim.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				symlinkForTest(t, victim, filepath.Join(repo, "AGENTS.md"))
				return victim
			},
		},
		{
			name: "absolute target inside relaying out",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				victim := filepath.Join(base, "victim.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				relay := filepath.Join(repo, "relay.md")
				symlinkForTest(t, victim, relay)
				symlinkForTest(t, relay, filepath.Join(repo, "AGENTS.md"))
				return victim
			},
		},
		{
			name: "absolute hop then a parent-relative hop out",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				victim := filepath.Join(base, "victim.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				mkdirAllForTest(t, filepath.Join(repo, "a"))
				symlinkForTest(t, filepath.Join("..", "..", "victim.md"), filepath.Join(repo, "a", "b.md"))
				symlinkForTest(t, filepath.Join(repo, "a", "b.md"), filepath.Join(repo, "AGENTS.md"))
				return victim
			},
		},
		{
			name: "absolute target spelled through the repository parent",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				victim := filepath.Join(base, "victim.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				symlinkForTest(t, filepath.Join(repo, "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
				return victim
			},
		},
		{
			name: "absolute guide-directory target outside",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				outside := filepath.Join(base, "outside")
				mkdirAllForTest(t, outside)
				victim := filepath.Join(outside, "keep.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				symlinkForTest(t, outside, filepath.Join(repo, ".entire"))
				return victim
			},
		},
		{
			name: "absolute target inside crossing an escaping directory",
			plant: func(t *testing.T, base, repo string) string {
				t.Helper()
				outside := filepath.Join(base, "outside")
				mkdirAllForTest(t, outside)
				victim := filepath.Join(outside, "victim.md")
				writeFileForTest(t, victim, "# outside the repository\n")
				symlinkForTest(t, outside, filepath.Join(repo, "docs"))
				symlinkForTest(t, filepath.Join(repo, "docs", "victim.md"), filepath.Join(repo, "AGENTS.md"))
				return victim
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			mkdirAllForTest(t, repo)
			victim := tt.plant(t, base, repo)
			before := readFileForTest(t, victim)

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents followed an absolute alias out of the repository")
			}
			if !strings.Contains(err.Error(), "leaves the repository") {
				t.Fatalf("a real escape lost its containment wording: %v", err)
			}
			if got := readFileForTest(t, victim); got != before {
				t.Fatalf("a file outside the repository was written:\nwant: %q\n got: %q", before, got)
			}
			if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the guide was written despite the containment failure (stat error %v)", statErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before containment was established: %q", stdout.String())
			}
		})
	}
}

func TestInitAgentsRefusesWindowsRootRelativeAlias(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows root-relative path semantics")
	}
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	victim := filepath.Join(repo, ".git", "config")
	mkdirAllForTest(t, filepath.Dir(victim))
	writeFileForTest(t, victim, "[core]\n\tbare = false\n")
	target := string(filepath.Separator) + filepath.Join(".git", "config")
	symlinkForTest(t, target, filepath.Join(repo, "AGENTS.md"))
	before := readFileForTest(t, victim)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents treated a drive-rooted alias as repository-relative")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("the repository git config was rewritten:\nwant: %q\n got: %q", before, got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before containment was established: %q", stdout.String())
	}
}

func TestInitAgentsUsesWindowsLexicalTraversalWithAlternateSeparators(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows accepts slash as an alternate path separator")
	}
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	shared := filepath.Join(repo, "shared.md")
	writeFileForTest(t, shared, "# Shared rules\n")
	symlinkForTest(t, "missing/../shared.md", filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatalf("Windows did not apply its documented lexical path cleaning: %v", err)
	}

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("the lexically resolved Windows alias was not updated:\n%s", got)
	}
}

func TestInitAgentsPreservesWindowsSymlinkType(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows symlinks encode whether their target is a file or directory")
	}
	skipIfSymlinksUnrepresentable(t)
	for _, targetExists := range []bool{true, false} {
		name := "dangling directory link"
		if targetExists {
			name = "directory link whose target became a file"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			shared := filepath.Join(repo, "shared")
			mkdirAllForTest(t, shared)
			symlinkForTest(t, shared, filepath.Join(repo, "AGENTS.md"))
			if err := os.Remove(shared); err != nil {
				t.Fatal(err)
			}
			before := ""
			if targetExists {
				before = "# unrelated file\n"
				writeFileForTest(t, shared, before)
			}
			if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
				t.Fatal("test setup did not preserve the directory-link type mismatch")
			}

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents ignored the Windows symlink target type")
			}
			if targetExists {
				if got := readFileForTest(t, shared); got != before {
					t.Fatalf("the mismatched directory link rewrote a regular file:\nwant: %q\n got: %q", before, got)
				}
			} else if _, statErr := os.Lstat(shared); !os.IsNotExist(statErr) {
				t.Fatalf("the dangling directory link target was created as a file (stat error %v)", statErr)
			}
			if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the guide was written despite the type mismatch (stat error %v)", statErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before link type was validated: %q", stdout.String())
			}
		})
	}
}

func TestInitAgentsDoesNotCleanWindowsExtendedPathTraversal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows extended-path semantics")
	}
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	victim := filepath.Join(repo, "victim.md")
	before := "# unrelated file\n"
	writeFileForTest(t, victim, before)
	target := `\\?\` + rawJoin(repo, "missing", "..", "victim.md")
	symlinkForTest(t, target, filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
		t.Skip("the host expanded dot components in an extended path")
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents cleaned a dot component in an extended path")
	}
	if strings.Contains(err.Error(), "leaves the repository") || !strings.Contains(err.Error(), "cannot resolve") {
		t.Fatalf("unsupported extended spelling was misclassified: %v", err)
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("the extended path rewrote an unrelated file:\nwant: %q\n got: %q", before, got)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before containment was established: %q", stdout.String())
	}
}

func TestInitAgentsFollowsWindowsRootRelativeAliasInsideRepository(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows root-relative path semantics")
	}
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	shared := filepath.Join(repo, "docs", "shared.md")
	mkdirAllForTest(t, filepath.Dir(shared))
	writeFileForTest(t, shared, "# Shared rules\n")
	target := strings.TrimPrefix(shared, filepath.VolumeName(shared))
	if len(target) == 0 || !os.IsPathSeparator(target[0]) {
		t.Fatalf("test target is not drive-rooted: %q", target)
	}
	symlinkForTest(t, target, filepath.Join(repo, "AGENTS.md"))

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("drive-rooted in-repository alias did not receive the managed block:\n%s", got)
	}
}

func TestUNCVolumeClassificationDoesNotRejectLocalDevicePaths(t *testing.T) {
	tests := []struct {
		name   string
		volume string
		want   bool
	}{
		{name: "ordinary UNC share", volume: `\\server\share`, want: true},
		{name: "extended UNC marker", volume: `\\?\UNC`, want: true},
		{name: "device UNC share", volume: `\\.\UNC\server\share`, want: true},
		{name: "NT UNC marker", volume: `\??\UNC`, want: true},
		{name: "unknown global device", volume: `\\?\GLOBALROOT`, want: true},
		{name: "extended local drive", volume: `\\?\C:`, want: false},
		{name: "local volume GUID", volume: `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}`, want: false},
		{name: "local device drive", volume: `\\.\C:`, want: false},
		{name: "drive letter", volume: `C:`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUNCVolume(tt.volume); got != tt.want {
				t.Fatalf("isUNCVolume(%q) = %t, want %t", tt.volume, got, tt.want)
			}
		})
	}
}

// TestInitAgentsReportsAbsoluteAliasLoopAsItself keeps the classification honest for the one
// pathological shape the absolute-alias rewrite can hit that os.Root never sees: two in-repository
// links pointing at each other by absolute path. That is a symlink loop, not an escape, and
// naming it one would send the reader hunting a link that leaves when none does.
func TestInitAgentsReportsAbsoluteAliasLoopAsItself(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	symlinkForTest(t, filepath.Join(repo, "loop.md"), filepath.Join(repo, "AGENTS.md"))
	symlinkForTest(t, filepath.Join(repo, "AGENTS.md"), filepath.Join(repo, "loop.md"))

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents accepted a symlink loop")
	}
	if !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("error %q does not name the offending alias", err)
	}
	if strings.Contains(err.Error(), "leaves the repository") {
		t.Fatalf("a symlink loop was misreported as a repository escape: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the failure (stat error %v)", statErr)
	}
}

// TestInitAgentsHonorsTheHostSymlinkLimit ensures the custom absolute-alias resolver never
// dereferences more links than the host filesystem would. Darwin rejects the thirty-second
// traversal, so treating MAXSYMLINKS as the number accepted turns its ELOOP into a write.
func TestInitAgentsHonorsTheHostSymlinkLimit(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	victim := filepath.Join(repo, ".git", "config")
	mkdirAllForTest(t, filepath.Dir(victim))
	writeFileForTest(t, victim, "[core]\n\tbare = false\n")
	next := filepath.Join(".git", "config")
	for i := 30; i >= 0; i-- {
		linkName := fmt.Sprintf("hop-%02d", i)
		symlinkForTest(t, next, filepath.Join(repo, linkName))
		next = linkName
	}
	symlinkForTest(t, next, filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
		t.Skip("the host filesystem resolves 32 symlink hops")
	}
	before := readFileForTest(t, victim)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents resolved more symlink hops than the host filesystem")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("a path rejected by the host rewrote the git config:\nwant: %q\n got: %q", before, got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

// TestInitAgentsHonorsTheHostLimitAcrossAnAbsolutePrefix ensures an external directory-alias
// prefix and the remaining in-repository chain share one traversal budget. Resolving the prefix
// in a separate syscall must not reset the count the kernel carries through the original path.
func TestInitAgentsHonorsTheHostLimitAcrossAnAbsolutePrefix(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	docs := filepath.Join(repo, "docs")
	mkdirAllForTest(t, docs)
	victim := filepath.Join(repo, ".git", "config")
	mkdirAllForTest(t, filepath.Dir(victim))
	before := "[core]\n\tbare = false\n"
	writeFileForTest(t, victim, before)

	next := filepath.Join("..", ".git", "config")
	for i := 16; i >= 0; i-- {
		name := fmt.Sprintf("inside-%02d", i)
		symlinkForTest(t, next, filepath.Join(docs, name))
		next = name
	}

	externalTarget := docs
	for i := 16; i >= 0; i-- {
		name := fmt.Sprintf("outside-%02d", i)
		link := filepath.Join(base, name)
		symlinkForTest(t, externalTarget, link)
		externalTarget = name
	}
	target := filepath.Join(base, externalTarget, "inside-00")
	symlinkForTest(t, target, filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
		t.Skip("the host resolves the combined 35-link chain")
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents reset the host link budget after mapping an absolute prefix")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("a path rejected by the host rewrote the git config:\nwant: %q\n got: %q", before, got)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

// TestInitAgentsCountsLinksInEveryAbsoluteTargetPrefix pins Darwin's exact boundary. The
// spelling /tmp follows a link to /private/tmp on the default filesystem, so sixteen absolute
// link targets consume thirty-two traversals even though the repository chain itself contains
// only sixteen links.
func TestInitAgentsCountsLinksInEveryAbsoluteTargetPrefix(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin /tmp prefix and MAXSYMLINKS boundary")
	}
	skipIfSymlinksUnrepresentable(t)
	base, err := os.MkdirTemp("/tmp", "entire-graph-absolute-links-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	repo := filepath.Join(base, "repo")
	victim := filepath.Join(repo, ".git", "config")
	mkdirAllForTest(t, filepath.Dir(victim))
	before := "[core]\n\tbare = false\n"
	writeFileForTest(t, victim, before)

	next := victim
	for i := 14; i >= 0; i-- {
		link := filepath.Join(repo, fmt.Sprintf("absolute-%02d", i))
		symlinkForTest(t, next, link)
		next = link
	}
	symlinkForTest(t, next, filepath.Join(repo, "AGENTS.md"))
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
		t.Skip("this /tmp spelling does not consume a link traversal on the host filesystem")
	}

	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents ignored links consumed by each absolute target prefix")
	}
	if got := readFileForTest(t, victim); got != before {
		t.Fatalf("a path rejected by Darwin rewrote the git config:\nwant: %q\n got: %q", before, got)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

func TestInitAgentsHonorsWindowsReparseLimits(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows reparse-point limits")
	}
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name          string
		relays        int
		absoluteFirst bool
	}{
		{name: "64 relative targets", relays: 63},
		{name: "32 hops with a fully qualified target", relays: 31, absoluteFirst: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			victim := filepath.Join(repo, ".git", "config")
			mkdirAllForTest(t, filepath.Dir(victim))
			writeFileForTest(t, victim, "[core]\n\tbare = false\n")
			next := filepath.Join(".git", "config")
			for i := tt.relays - 1; i >= 0; i-- {
				linkName := fmt.Sprintf("hop-%02d", i)
				symlinkForTest(t, next, filepath.Join(repo, linkName))
				next = linkName
			}
			firstTarget := next
			if tt.absoluteFirst {
				firstTarget = filepath.Join(repo, next)
			}
			symlinkForTest(t, firstTarget, filepath.Join(repo, "AGENTS.md"))
			if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err == nil {
				t.Skip("the host resolved a chain beyond its documented reparse-point limit")
			}
			before := readFileForTest(t, victim)

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents resolved more reparse points than Windows permits")
			}
			if got := readFileForTest(t, victim); got != before {
				t.Fatalf("a path rejected by Windows rewrote the git config:\nwant: %q\n got: %q", before, got)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
			}
		})
	}
}

func mkdirAllForTest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFileForTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlinkForTest(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestInstructionReadsAreConfinedToRepository drives the read helpers directly, which is the only
// way to observe this without racing the process against itself. ensureContainedInRepo is a
// preflight and is explicitly not atomic with what follows, so if the reads resolve absolute paths
// on their own, a link swapped to an outside file after preflight and restored before the write
// gets that file's contents read in — and then written into the repository under the managed
// block. Confinement has to hold at the read, not only at the preflight and the write.
func TestInstructionReadsAreConfinedToRepository(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	mkdirAllForTest(t, repo)
	secret := "# PRIVATE NOTES OUTSIDE THE REPO\n"
	writeFileForTest(t, filepath.Join(base, "victim.md"), secret)
	symlinkForTest(t, filepath.Join("..", "victim.md"), filepath.Join(repo, "AGENTS.md"))

	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(repo, "AGENTS.md")

	if _, err := inspectInstructionFile(root, "AGENTS.md", path); err == nil {
		t.Error("inspectInstructionFile followed a link out of the repository")
	}
	content, _, _, err := readAndValidateInstructionFile(root, "AGENTS.md", path)
	if err == nil {
		t.Errorf("readAndValidateInstructionFile read outside the repository: %q", content)
	}
	if strings.Contains(string(content), secret) {
		t.Errorf("content from outside the repository was returned: %q", content)
	}
}

// TestInitAgentsFollowsAbsoluteAliasDifferingOnlyInCase pins containment on a case-insensitive
// volume, the default on macOS and Windows. There an alias and --repo can name one directory with
// different casing, so the kernel resolves both to the same file while a lexical comparison of the
// two paths disagrees. Containment is about which file the link reaches, so it has to be decided by
// filesystem identity, not by matching path text.
func TestInitAgentsFollowsAbsoluteAliasDifferingOnlyInCase(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	base := t.TempDir()
	repo := filepath.Join(base, "Repo")
	mkdirAllForTest(t, filepath.Join(repo, "docs"))
	skipIfCaseSensitive(t, repo)
	shared := filepath.Join(repo, "docs", "shared.md")
	writeFileForTest(t, shared, "# Shared rules\n")
	// The same path, spelled with the repository directory in a different case.
	symlinkForTest(t, filepath.Join(base, "repo", "docs", "shared.md"), filepath.Join(repo, "AGENTS.md"))

	runInitAgentsForTest(t, repo)

	if got := readFileForTest(t, shared); !strings.Contains(got, testAgentPointerBlock) {
		t.Fatalf("alias differing only in case did not receive the managed block:\n%s", got)
	}
}

// skipIfCaseSensitive reports whether dir lives on a case-insensitive volume, which is a property
// of the filesystem and not of the OS: macOS and Windows default to case-insensitive, Linux CI runs
// case-sensitive, and either can be mounted the other way.
func skipIfCaseSensitive(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "casEprobe")
	writeFileForTest(t, probe, "")
	defer func() {
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := os.Stat(filepath.Join(dir, "CASEPROBE")); err != nil {
		t.Skip("case-sensitive filesystem: one directory cannot be named in two cases here")
	}
}

// filesystemAliasesNamesForTest asks the actual test filesystem whether two spellings identify
// one directory entry. This covers rules that path-string helpers cannot model, including APFS
// Unicode normalization and Win32 trailing-dot/case aliases.
func filesystemAliasesNamesForTest(t *testing.T, dir, first, second string) bool {
	t.Helper()
	firstPath := filepath.Join(dir, first)
	writeFileForTest(t, firstPath, "")
	defer func() {
		if err := os.Remove(firstPath); err != nil {
			t.Fatal(err)
		}
	}()
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(filepath.Join(dir, second))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(firstInfo, secondInfo)
}

func TestInitAgentsRefusesAliasWithTerminalDirectoryRequirementBeforeWriting(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := []struct {
		name     string
		target   string
		existing bool
	}{
		{name: "existing file with trailing separator", target: rawJoin("victim.md", ""), existing: true},
		{name: "existing file with terminal dot", target: rawJoin("victim.md", "."), existing: true},
		{name: "missing target with trailing separator", target: rawJoin("missing", "")},
		{name: "missing target with terminal dot", target: rawJoin("missing", ".")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			victim := filepath.Join(repo, "victim.md")
			before := ""
			if tt.existing {
				before = "# an unrelated repository file\n"
				writeFileForTest(t, victim, before)
			}
			symlinkForTest(t, tt.target, filepath.Join(repo, "AGENTS.md"))

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatal("init-agents accepted a file alias whose spelling requires a directory")
			}
			if !strings.Contains(err.Error(), "cannot resolve") {
				t.Fatalf("the refusal did not explain that the target cannot be resolved: %v", err)
			}
			if tt.existing {
				if got := readFileForTest(t, victim); got != before {
					t.Fatalf("an unrelated repository file was rewritten:\nwant: %q\n got: %q", before, got)
				}
			} else if _, statErr := os.Lstat(filepath.Join(repo, "missing")); !os.IsNotExist(statErr) {
				t.Fatalf("the missing target was created (stat error %v)", statErr)
			}
			if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the guide was written despite the preflight failure (stat error %v)", statErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
			}
		})
	}
}

func TestInitAgentsRefusesMissingAliasParentBeforeWriting(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	repo := t.TempDir()
	target := filepath.Join("missing", "subdir", "shared.md")
	symlinkForTest(t, target, filepath.Join(repo, "CLAUDE.md"))

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatal("init-agents accepted a dangling alias whose parent cannot be created by OpenFile")
	}
	if !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("the refusal did not identify the missing parent directory: %v", err)
	}
	for _, name := range []string{
		filepath.Join(".entire", "graph-agent.md"),
		"AGENTS.md",
		filepath.Join("missing", "subdir", "shared.md"),
	} {
		if _, statErr := os.Lstat(filepath.Join(repo, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s was written despite the preflight failure (stat error %v)", name, statErr)
		}
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
	}
}

func TestInitAgentsRefusesManagedTargetCollisionsBeforeWriting(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	t.Run("guide symlinks to AGENTS", func(t *testing.T) {
		repo := t.TempDir()
		guideDir := filepath.Join(repo, ".entire")
		mkdirAllForTest(t, guideDir)
		symlinkForTest(t, filepath.Join("..", "AGENTS.md"), filepath.Join(guideDir, "graph-agent.md"))

		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "same managed file") {
			t.Fatalf("init-agents did not reject the guide/instruction collision: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "AGENTS.md")); !os.IsNotExist(statErr) {
			t.Fatalf("AGENTS.md was written despite the collision (stat error %v)", statErr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout was written before topology validation: %q", stdout.String())
		}
	})

	t.Run("guide hard-links to AGENTS", func(t *testing.T) {
		repo := t.TempDir()
		guideDir := filepath.Join(repo, ".entire")
		mkdirAllForTest(t, guideDir)
		agents := filepath.Join(repo, "AGENTS.md")
		before := "# Shared rules\n"
		writeFileForTest(t, agents, before)
		if err := os.Link(agents, filepath.Join(guideDir, "graph-agent.md")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}

		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "same managed file") {
			t.Fatalf("init-agents did not reject the hard-linked collision: %v", err)
		}
		if got := readFileForTest(t, agents); got != before {
			t.Fatalf("hard-linked instructions changed despite the collision:\nwant: %q\n got: %q", before, got)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout was written before topology validation: %q", stdout.String())
		}
	})

	t.Run("instruction is planned directory ancestor", func(t *testing.T) {
		repo := t.TempDir()
		mkdirAllForTest(t, filepath.Join(repo, "nested", "activation"))
		symlinkForTest(t, filepath.Join("nested", "activation"), filepath.Join(repo, ".entire"))
		if err := os.RemoveAll(filepath.Join(repo, "nested")); err != nil {
			t.Fatal(err)
		}
		symlinkForTest(t, "nested", filepath.Join(repo, "AGENTS.md"))

		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "created as a directory") {
			t.Fatalf("init-agents did not reject the directory/file collision: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "nested")); !os.IsNotExist(statErr) {
			t.Fatalf("planned directory was created despite the collision (stat error %v)", statErr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout was written before topology validation: %q", stdout.String())
		}
	})

	t.Run("filesystem-normalized missing names", func(t *testing.T) {
		repo := t.TempDir()
		if !filesystemAliasesNamesForTest(t, repo, "probe-é", "probe-e\u0301") {
			t.Skip("filesystem keeps canonical Unicode normalizations distinct")
		}
		guideDir := filepath.Join(repo, ".entire")
		mkdirAllForTest(t, guideDir)
		guideTarget := "é.md"
		instructionTarget := "e\u0301.md"
		symlinkForTest(t, filepath.Join("..", guideTarget), filepath.Join(guideDir, "graph-agent.md"))
		symlinkForTest(t, instructionTarget, filepath.Join(repo, "AGENTS.md"))

		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "same managed file") {
			t.Fatalf("init-agents did not reject filesystem-equivalent Unicode names: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, guideTarget)); !os.IsNotExist(statErr) {
			t.Fatalf("the colliding target remained after rollback (stat error %v)", statErr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout was written before topology validation: %q", stdout.String())
		}
	})

	t.Run("Windows trailing-dot missing names", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Win32 name-equivalence rule")
		}
		repo := t.TempDir()
		guideDir := filepath.Join(repo, ".entire")
		mkdirAllForTest(t, guideDir)
		symlinkForTest(t, filepath.Join("..", "shared.md."), filepath.Join(guideDir, "graph-agent.md"))
		symlinkForTest(t, "shared.md", filepath.Join(repo, "AGENTS.md"))

		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
		if err == nil || !strings.Contains(err.Error(), "same managed file") {
			t.Fatalf("init-agents did not reject Win32-equivalent trailing-dot names: %v", err)
		}
		if _, statErr := os.Lstat(filepath.Join(repo, "shared.md")); !os.IsNotExist(statErr) {
			t.Fatalf("the colliding target remained after rollback (stat error %v)", statErr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout was written before topology validation: %q", stdout.String())
		}
	})
}

func TestInitAgentsAllowsCaseDistinctMissingManagedTargets(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	t.Run("guide and instruction files", func(t *testing.T) {
		repo := t.TempDir()
		if filesystemAliasesNamesForTest(t, repo, "CaseProbe", "caseprobe") {
			t.Skip("filesystem aliases names that differ only in case")
		}
		guideDir := filepath.Join(repo, ".entire")
		mkdirAllForTest(t, guideDir)
		symlinkForTest(t, filepath.Join("..", "Shared.md"), filepath.Join(guideDir, "graph-agent.md"))
		symlinkForTest(t, "shared.md", filepath.Join(repo, "AGENTS.md"))

		runInitAgentsForTest(t, repo)

		if got := readFileForTest(t, filepath.Join(repo, "Shared.md")); !strings.Contains(got, "# entire-graph") {
			t.Fatalf("case-distinct guide target did not receive the guide:\n%s", got)
		}
		if got := readFileForTest(t, filepath.Join(repo, "shared.md")); !strings.Contains(got, testAgentPointerBlock) {
			t.Fatalf("case-distinct instruction target did not receive the pointer:\n%s", got)
		}
	})

	t.Run("instruction and planned directory", func(t *testing.T) {
		repo := t.TempDir()
		if filesystemAliasesNamesForTest(t, repo, "CaseProbe", "caseprobe") {
			t.Skip("filesystem aliases names that differ only in case")
		}
		mkdirAllForTest(t, filepath.Join(repo, "nested", "activation"))
		symlinkForTest(t, filepath.Join("nested", "activation"), filepath.Join(repo, ".entire"))
		if err := os.RemoveAll(filepath.Join(repo, "nested")); err != nil {
			t.Fatal(err)
		}
		symlinkForTest(t, "NESTED", filepath.Join(repo, "AGENTS.md"))

		runInitAgentsForTest(t, repo)

		if got := readFileForTest(t, filepath.Join(repo, "nested", "activation", "graph-agent.md")); !strings.Contains(got, "# entire-graph") {
			t.Fatalf("guide was not written through the planned directory alias:\n%s", got)
		}
		if got := readFileForTest(t, filepath.Join(repo, "NESTED")); !strings.Contains(got, testAgentPointerBlock) {
			t.Fatalf("case-distinct instruction target did not receive the pointer:\n%s", got)
		}
	})
}

// aliasPlant installs one alias spelling into a repository. target names the install path whose
// resolution it subverts, and plant returns the in-repository file that spelling reaches only
// after ".." is collapsed as text — the file init-agents must not touch.
type aliasPlant struct {
	name   string
	target string
	plant  func(t *testing.T, repo string) string
	// refusedEvenIfTheKernelCanReachIt marks a landing init-agents refuses on purpose whatever
	// the kernel says, because it is inside the git directory. The oracle below otherwise
	// requires agreement with the kernel, and that requirement is exactly what this exception
	// exists to bound: on a platform whose path rules make one of these spellings reachable —
	// Windows, which collapses ".." lexically before opening anything — "agree with the
	// kernel" means "install the managed block into .git/config", which is the bug, not the
	// contract.
	refusedEvenIfTheKernelCanReachIt bool
}

// untraversableAliasPlants are the in-repository alias spellings whose landing exists only after
// ".." is collapsed as text. filepath.Abs, filepath.Join and filepath.Clean all collapse ".."
// LEXICALLY, before a single component is opened — Join collapses these very targets if used to
// build them, which is why rawJoin exists — so each spelling names a file the kernel never
// reaches: it stops at the component before the ".." with ENOENT, ENOTDIR or EACCES.
var untraversableAliasPlants = []aliasPlant{
	{
		name:   "component that does not exist",
		target: "AGENTS.md",
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "victim.md")
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			symlinkForTest(t, rawJoin("missing", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:   "absolute target with a component that does not exist",
		target: "AGENTS.md",
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "victim.md")
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			symlinkForTest(t, rawJoin(repo, "missing", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:   "component that is a regular file",
		target: "AGENTS.md",
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "victim.md")
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			writeFileForTest(t, filepath.Join(repo, "blocker"), "not a directory\n")
			symlinkForTest(t, rawJoin("blocker", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:   "component that is a link to a regular file",
		target: "AGENTS.md",
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "victim.md")
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			writeFileForTest(t, filepath.Join(repo, "leaf.md"), "not a directory\n")
			symlinkForTest(t, "leaf.md", filepath.Join(repo, "blocker"))
			symlinkForTest(t, rawJoin("blocker", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:   "component that cannot be searched",
		target: "AGENTS.md",
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "victim.md")
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			closed := filepath.Join(repo, "closed")
			mkdirAllForTest(t, closed)
			if err := os.Chmod(closed, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
			symlinkForTest(t, rawJoin("closed", "..", "victim.md"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:                             "landing inside the git directory",
		target:                           "AGENTS.md",
		refusedEvenIfTheKernelCanReachIt: true,
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, ".git", "config")
			mkdirAllForTest(t, filepath.Dir(victim))
			writeFileForTest(t, victim, "[core]\n\tbare = false\n")
			symlinkForTest(t, rawJoin("missing", "..", ".git", "config"), filepath.Join(repo, "AGENTS.md"))
			return victim
		},
	},
	{
		name:   "guide directory alias",
		target: filepath.Join(".entire", "graph-agent.md"),
		plant: func(t *testing.T, repo string) string {
			t.Helper()
			victim := filepath.Join(repo, "tooling", "graph-agent.md")
			mkdirAllForTest(t, filepath.Dir(victim))
			writeFileForTest(t, victim, "# an unrelated repository file\n")
			symlinkForTest(t, rawJoin("missing", "..", "tooling"), filepath.Join(repo, ".entire"))
			return victim
		},
	},
}

// TestInitAgentsRefusesAliasCollapsingUntraversableComponent is the hole this fix closed.
// Collapsing ".." is only containment when the kernel could have taken that step; collapsing it as
// text turns a link the filesystem refuses into a write to an unrelated file inside the
// repository — including one under .git. Before the resolve walk asked the kernel, every spelling
// below installed the managed block into the file named after the collapse.
func TestInitAgentsRefusesAliasCollapsingUntraversableComponent(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	for _, tt := range untraversableAliasPlants {
		t.Run(tt.name, func(t *testing.T) {
			skipIfKernelResolvesPlant(t, tt)
			repo := t.TempDir()
			victim := tt.plant(t, repo)
			before := readFileForTest(t, victim)

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})

			if err == nil {
				t.Fatal("init-agents wrote through a \"..\" the kernel could not have taken")
			}
			if got := readFileForTest(t, victim); got != before {
				t.Fatalf("an unrelated repository file was rewritten:\nwant: %q\n got: %q", before, got)
			}
			if !strings.Contains(err.Error(), "cannot resolve") {
				t.Fatalf("the refusal did not say the path could not be resolved: %v", err)
			}
			if strings.Contains(err.Error(), "leaves the repository") {
				// The link stays inside. Calling it an escape sends the reader hunting a
				// link that leaves when the cause is a path that does not resolve.
				t.Fatalf("a broken path was reported as a repository escape: %v", err)
			}
			if _, statErr := os.Lstat(filepath.Join(repo, ".entire", "graph-agent.md")); !os.IsNotExist(statErr) {
				t.Fatalf("the guide was written despite the failure (stat error %v)", statErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before resolution was established: %q", stdout.String())
			}
		})
	}
}

// TestInitAgentsAliasResolutionMatchesTheKernel is the oracle differential behind the fix. Rather
// than restate the kernel's path rules — restating them is how the collapse came to be written —
// it asks the filesystem what each spelling does and requires init-agents to agree: install
// exactly when the kernel can open that path, refuse when it cannot, and, when both succeed, land
// on the file the kernel's own resolution reaches. The single exception is a landing inside the
// git directory, which is refused whatever the kernel answers; see
// refusedEvenIfTheKernelCanReachIt. Every alias here stays inside the repository,
// which is what makes the two comparable; an alias that leaves is refused by design and is covered
// by TestInitAgentsRefusesAbsoluteAliasLeavingRepository.
func TestInitAgentsAliasResolutionMatchesTheKernel(t *testing.T) {
	skipIfSymlinksUnrepresentable(t)
	tests := append([]aliasPlant{
		{
			name:   "no alias at all",
			target: "AGENTS.md",
			plant:  func(t *testing.T, repo string) string { return filepath.Join(repo, "AGENTS.md") },
		},
		{
			name:   "legal traversal over a real directory",
			target: "AGENTS.md",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "shared.md")
				writeFileForTest(t, shared, "# Shared rules\n")
				mkdirAllForTest(t, filepath.Join(repo, "sub"))
				symlinkForTest(t, rawJoin("sub", "..", "shared.md"), filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name:   "legal traversal over a directory reached through a link",
			target: "AGENTS.md",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				shared := filepath.Join(repo, "docs", "shared.md")
				mkdirAllForTest(t, filepath.Join(repo, "docs", "nested"))
				writeFileForTest(t, shared, "# Shared rules\n")
				symlinkForTest(t, filepath.Join("docs", "nested"), filepath.Join(repo, "hop"))
				symlinkForTest(t, rawJoin("hop", "..", "shared.md"), filepath.Join(repo, "AGENTS.md"))
				return shared
			},
		},
		{
			name:   "dangling alias the install is meant to create",
			target: "AGENTS.md",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				symlinkForTest(t, "shared.md", filepath.Join(repo, "AGENTS.md"))
				return filepath.Join(repo, "shared.md")
			},
		},
	}, untraversableAliasPlants...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oracleRepo := t.TempDir()
			tt.plant(t, oracleRepo)
			kernelErr := kernelReachesRawPath(t, oracleRepo, tt.target)

			repo := t.TempDir()
			tt.plant(t, repo)
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})

			if tt.refusedEvenIfTheKernelCanReachIt {
				// The one deliberate divergence. Reachability is the kernel's to answer and
				// it is not the question here: a landing in the git directory is refused
				// whether the kernel could take the path or not.
				if err == nil {
					t.Fatalf("init-agents installed into the git directory; the kernel reaching it (%v) does not make it a legal landing", kernelErr)
				}
				if !strings.Contains(err.Error(), "git directory") && !strings.Contains(err.Error(), "cannot resolve") {
					t.Fatalf("refusal named neither the git directory nor an unresolvable path: %v", err)
				}
				return
			}
			if (kernelErr == nil) != (err == nil) {
				t.Fatalf("init-agents disagreed with the filesystem about %s:\nkernel: %v\n  init: %v", tt.target, kernelErr, err)
			}
			if err != nil {
				return
			}
			// Read back through the same spelling, so the kernel — not this test's idea of
			// where the alias points — decides which file is inspected.
			got, readErr := os.ReadFile(rawJoin(repo, tt.target))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(got), "entire-graph") {
				t.Fatalf("the install did not land where the kernel resolves %s:\n%s", tt.target, got)
			}
		})
	}
}

// skipIfKernelResolvesPlant keeps the refusal test honest about privileges rather than about the
// OS: a process that can search a mode-000 directory — root, or a filesystem without POSIX
// permissions — genuinely reaches the target, so refusing it would be the bug. The probe runs on
// its own copy of the plant because it creates the file it opens.
func skipIfKernelResolvesPlant(t *testing.T, plant aliasPlant) {
	t.Helper()
	probeRepo := t.TempDir()
	plant.plant(t, probeRepo)
	if err := kernelReachesRawPath(t, probeRepo, plant.target); err == nil {
		t.Skipf("this environment resolves %s, so there is nothing for init-agents to refuse", plant.target)
	}
}

// rawJoin joins path elements without filepath.Join, whose Clean collapses ".." lexically. Using
// Join to build these paths would silently rewrite them into the legitimate spelling and test
// nothing — the same substitution the code under test used to make.
func rawJoin(elements ...string) string {
	return strings.Join(elements, string(filepath.Separator))
}

// kernelReachesRawPath asks the filesystem, not this package, whether name can be opened for
// writing inside dir the way init-agents opens it — creating it when absent. Its answer is the
// oracle: ENOENT, ENOTDIR or EACCES here is the kernel refusing the walk, and nil is the kernel
// resolving it.
func kernelReachesRawPath(t *testing.T, dir, name string) error {
	t.Helper()
	file, err := os.OpenFile(rawJoin(dir, name), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return nil
}

// TestInitAgentsRefusesOversizeInstructionFileWithoutWrites pins the read bound on the only
// repository-authored files init-agents reads. Their size is chosen by the repository, so an
// unbounded read let a clone drive the command's memory to a multiple of the file it shipped.
// The refusal has to land before anything is created or modified, and it has to be a refusal
// rather than a truncation: a truncated instruction file would be written back over the user's
// own text.
func TestInitAgentsRefusesOversizeInstructionFileWithoutWrites(t *testing.T) {
	for _, oversizeName := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(oversizeName, func(t *testing.T) {
			repo := t.TempDir()
			counterpartName := "CLAUDE.md"
			if oversizeName == "CLAUDE.md" {
				counterpartName = "AGENTS.md"
			}
			oversize := bytes.Repeat([]byte("a"), maxInstructionFileBytes+1)
			oversizePath := filepath.Join(repo, oversizeName)
			if err := os.WriteFile(oversizePath, oversize, 0o644); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
			if err == nil {
				t.Fatalf("init-agents accepted a %d-byte %s", len(oversize), oversizeName)
			}
			for _, want := range []string{oversizeName, "larger than", "rerun init-agents"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err, want)
				}
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout was written before the size check completed: %q", stdout.String())
			}
			if got := readFileForTest(t, oversizePath); got != string(oversize) {
				t.Fatalf("%s was rewritten despite the refusal (%d bytes read back)", oversizeName, len(got))
			}
			for _, path := range []string{
				filepath.Join(repo, ".entire", "graph-agent.md"),
				filepath.Join(repo, counterpartName),
			} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("%s was created despite the refusal (stat error %v)", path, statErr)
				}
			}
		})
	}
}

// managedBlockOverheadForTest measures how many bytes init-agents adds to an
// unmanaged instruction file, by running it on a one-byte file and diffing. It is
// measured rather than restated so the boundary tests below cannot drift from the
// block the command actually renders.
func managedBlockOverheadForTest(t *testing.T) int {
	t.Helper()
	repo := t.TempDir()
	agentsPath := filepath.Join(repo, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatalf("probe run: %v", err)
	}
	return len(readFileForTest(t, agentsPath)) - 1
}

// TestInitAgentsAcceptsTheLargestFileThatStillFitsOnceTheBlockIsAdded keeps the bound
// off-by-one-safe from the side that matters. The limit governs what is WRITTEN, so the
// largest accepted source is the one whose rendered form lands exactly on it, and that
// rendered form must survive a second run — the round trip is the whole point of bounding
// the write rather than only the read.
func TestInitAgentsAcceptsTheLargestFileThatStillFitsOnceTheBlockIsAdded(t *testing.T) {
	overhead := managedBlockOverheadForTest(t)
	repo := t.TempDir()
	atLimit := bytes.Repeat([]byte("a"), maxInstructionFileBytes-overhead)
	agentsPath := filepath.Join(repo, "AGENTS.md")
	if err := os.WriteFile(agentsPath, atLimit, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatalf("init-agents refused a file that renders to exactly the %d-byte limit: %v", maxInstructionFileBytes, err)
	}
	got := readFileForTest(t, agentsPath)
	if !strings.HasPrefix(got, string(atLimit)) {
		t.Fatal("AGENTS.md lost its original content")
	}
	if !strings.Contains(got, agentPointerBegin) {
		t.Fatal("AGENTS.md did not receive the managed pointer block")
	}
	if len(got) != maxInstructionFileBytes {
		t.Fatalf("rendered AGENTS.md is %d bytes, want exactly the %d-byte limit", len(got), maxInstructionFileBytes)
	}

	stdout.Reset()
	stderr.Reset()
	if err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatalf("init-agents could not read back the %d-byte file it wrote: %v", len(got), err)
	}
	if again := readFileForTest(t, agentsPath); again != got {
		t.Fatalf("the second run rewrote the file (%d bytes, was %d)", len(again), len(got))
	}
}

// TestInitAgentsRefusesWhenTheManagedBlockWouldCrossTheReadLimit pins the invariant the
// read bound alone does not give: init-agents must never leave behind an instruction file
// it will refuse to read. A source sitting exactly on the limit passes the read and is then
// APPENDED to, so the written result lands past it — and the next run reports the user's
// file as too large to rewrite, over bytes this command added. Refuse it up front, with the
// tree untouched, instead.
func TestInitAgentsRefusesWhenTheManagedBlockWouldCrossTheReadLimit(t *testing.T) {
	repo := t.TempDir()
	atLimit := bytes.Repeat([]byte("a"), maxInstructionFileBytes)
	agentsPath := filepath.Join(repo, "AGENTS.md")
	if err := os.WriteFile(agentsPath, atLimit, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, Stderr: &stderr}, []string{"init-agents", "--repo", repo})
	if err == nil {
		t.Fatalf("init-agents wrote a file past the %d-byte limit it will read back", maxInstructionFileBytes)
	}
	for _, want := range []string{"AGENTS.md", "read back", "rerun init-agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if got := readFileForTest(t, agentsPath); got != string(atLimit) {
		t.Fatalf("AGENTS.md was rewritten despite the refusal (%d bytes read back)", len(got))
	}
	for _, path := range []string{
		filepath.Join(repo, ".entire", "graph-agent.md"),
		filepath.Join(repo, "CLAUDE.md"),
	} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("%s was created despite the refusal (stat error %v)", path, statErr)
		}
	}
}

// TestInitAgentsRefusesManagedTargetInsideGitDirectory pins the landing that containment alone
// cannot refuse. os.Root keeps the write inside the project root, and `.git` is inside the project
// root, so a committed `CLAUDE.md -> .git/config` used to be followed: the managed block landed in
// git's own config and git then refused to operate on the repository at all.
//
// Each case is a link that STAYS inside the repository, so none of them is an escape and none is
// caught by the escape refusal above.
func TestInitAgentsRefusesManagedTargetInsideGitDirectory(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		managed string
		victim  string
		// linkSpelling overrides the symlink target text. It is set only where the point of
		// the case is that the spelling and the landing differ.
		linkSpelling string
	}{
		{name: "claude to git config", managed: "CLAUDE.md", victim: filepath.Join(".git", "config")},
		{name: "agents to git hook", managed: "AGENTS.md", victim: filepath.Join(".git", "hooks", "pre-commit")},
		{name: "guide to git config", managed: filepath.Join(".entire", "graph-agent.md"), victim: filepath.Join(".git", "config")},
		{name: "nested checkout git config", managed: "AGENTS.md", victim: filepath.Join("vendor", "dep", ".git", "config")},
		// A symlink target is text the repository chose, and macOS and Windows resolve it
		// case-insensitively: an exact ".git" comparison is a bypass on both.
		{name: "uppercase git spelling", managed: "CLAUDE.md", victim: filepath.Join(".git", "config"), linkSpelling: ".GIT/config"},
		{name: "mixed case git spelling", managed: "CLAUDE.md", victim: filepath.Join(".git", "config"), linkSpelling: ".Git/config"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()

			victimPath := filepath.Join(repo, testCase.victim)
			if err := os.MkdirAll(filepath.Dir(victimPath), 0o755); err != nil {
				t.Fatal(err)
			}
			const victimContent = "[core]\n\tbare = false\n"
			if err := os.WriteFile(victimPath, []byte(victimContent), 0o644); err != nil {
				t.Fatal(err)
			}

			managedPath := filepath.Join(repo, testCase.managed)
			if err := os.MkdirAll(filepath.Dir(managedPath), 0o755); err != nil {
				t.Fatal(err)
			}
			relativeVictim, err := filepath.Rel(filepath.Dir(managedPath), victimPath)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.linkSpelling != "" {
				if !caseInsensitiveFilesystem(t, repo) {
					t.Skip("filesystem is case-sensitive, so this spelling names nothing")
				}
				relativeVictim = filepath.FromSlash(testCase.linkSpelling)
			}
			if err := os.Symlink(relativeVictim, managedPath); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			var out bytes.Buffer
			runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
			requireGitDirRefusal(t, runErr, out.String())

			after, err := os.ReadFile(victimPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != victimContent {
				t.Fatalf("%s was written through:\n%s", testCase.victim, after)
			}

			// The refusal is a preflight for ALL managed targets, so nothing is installed.
			for _, unwritten := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".entire", "graph-agent.md")} {
				if unwritten == testCase.managed {
					continue
				}
				if _, err := os.Lstat(filepath.Join(repo, unwritten)); !os.IsNotExist(err) {
					t.Fatalf("partial install: %s exists after the refusal (%v)", unwritten, err)
				}
			}
		})
	}
}

// TestInitAgentsRefusesIndirectRoutesIntoGitDirectory pins the refusal to the RESOLVED landing
// rather than to the link's spelling. Both shapes below reach `.git/config` without naming it as a
// plain relative path, so a check that read the symlink text would miss them: one is spelled as an
// absolute path that re-enters the repository, the other reaches it in two hops through a
// symlinked directory.
func TestInitAgentsRefusesIndirectRoutesIntoGitDirectory(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		build func(t *testing.T, repo string)
	}{
		{
			name: "absolute target re-entering the repository",
			build: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(repo, ".git", "config"), filepath.Join(repo, "CLAUDE.md")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "two hops through a symlinked directory",
			build: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join("..", ".git"), filepath.Join(repo, "sub", "glink")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				if err := os.Symlink(filepath.Join("sub", "glink", "config"), filepath.Join(repo, "AGENTS.md")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			gitDir := filepath.Join(repo, ".git")
			if err := os.MkdirAll(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			const config = "[core]\n\tbare = false\n"
			configPath := filepath.Join(gitDir, "config")
			if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			testCase.build(t, repo)

			var out bytes.Buffer
			err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
			requireGitDirRefusal(t, err, out.String())
			after, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != config {
				t.Fatalf("git config was written through:\n%s", after)
			}
		})
	}
}

// TestInitAgentsRefusesGuideDirectoryAliasedToGitDirectory covers the directory target, which
// resolves through resolveContainedDirectoryName rather than the file resolver: `.entire -> .git`
// would otherwise have MkdirAll accept the git directory as the guide's home and drop
// graph-agent.md inside it.
func TestInitAgentsRefusesGuideDirectoryAliasedToGitDirectory(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(repo, ".entire")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
	requireGitDirRefusal(t, err, out.String())
	if _, statErr := os.Lstat(filepath.Join(gitDir, "graph-agent.md")); !os.IsNotExist(statErr) {
		t.Fatalf("guide written inside the git directory (%v)", statErr)
	}
}

// TestInitAgentsStillFollowsInRepositoryAliases guards the other side of the refusal above. The
// git-directory landing is the only one being added; docs/agents.md documents in-repository
// symlinks as supported, and both shapes it names must keep working.
func TestInitAgentsStillFollowsInRepositoryAliases(t *testing.T) {
	t.Parallel()

	t.Run("shared instruction file", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		if err := os.Symlink("AGENTS.md", filepath.Join(repo, "CLAUDE.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
			t.Fatalf("documented alias refused: %v\n%s", err, out.String())
		}
		agents, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(agents), agentPointerBegin) != 1 {
			t.Fatalf("shared instruction file did not receive exactly one block:\n%s", agents)
		}
	})

	t.Run("link to an ordinary in-repository file", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(repo, "docs", "guide.md")
		if err := os.WriteFile(target, []byte("# house rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("docs", "guide.md"), filepath.Join(repo, "AGENTS.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var out bytes.Buffer
		if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
			t.Fatalf("documented alias refused: %v\n%s", err, out.String())
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "# house rules") || !strings.Contains(string(got), agentPointerBegin) {
			t.Fatalf("alias target not updated in place:\n%s", got)
		}
	})
}

// caseInsensitiveFilesystem asks the filesystem under dir rather than assuming from GOOS: a
// case-sensitive volume on macOS and a case-insensitive one on Linux are both ordinary.
func caseInsensitiveFilesystem(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, ".case-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(probe) }()
	_, err := os.Stat(filepath.Join(dir, ".CASE-PROBE"))
	return err == nil
}

// requireGitDirRefusal asserts the refusal without over-fitting its wording to one platform. The
// landing is what matters; the ROUTE to it can fail first for a reason that is not this rule.
// Windows decides a symlink's type when it is created and cannot traverse a file link as a
// directory, so a multi-hop or directory-valued spelling can be refused as unresolvable there and
// as a git-directory landing everywhere else. Both are refusals of the same write.
func requireGitDirRefusal(t *testing.T, err error, out string) {
	t.Helper()
	if err == nil {
		t.Fatalf("init-agents wrote into the git directory; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "git directory") && !strings.Contains(err.Error(), "cannot resolve") {
		t.Fatalf("refusal named neither the git directory nor an unresolvable path: %v", err)
	}
}

// TestInitAgentsRefusesManagedTargetHardLinkedIntoGitDirectory covers the route
// the git-directory refusal cannot see.
//
// Every guard around it reasons about a PATH — its components, what it resolves
// to, whether a component is a symlink. A hard link has no path to reason about:
// `ln .git/config CLAUDE.md` gives git's config a second name in the working
// tree, so it resolves to "CLAUDE.md", carries no `.git` component, and Lstat
// reports an ordinary regular file. PathLandsInGitDir is structurally unable to
// refuse it.
//
// Measured on this branch before the guard: `init-agents` exited 0 (nil error)
// and `.git/config` was rewritten with the managed block appended — the exact
// corruption this PR exists to prevent, reached by a route it did not cover.
func TestInitAgentsRefusesManagedTargetHardLinkedIntoGitDirectory(t *testing.T) {
	t.Parallel()

	for _, managed := range []string{"CLAUDE.md", "AGENTS.md"} {
		t.Run(managed, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()

			victimPath := filepath.Join(repo, ".git", "config")
			if err := os.MkdirAll(filepath.Dir(victimPath), 0o755); err != nil {
				t.Fatal(err)
			}
			const victimContent = "[core]\n\tbare = false\n"
			if err := os.WriteFile(victimPath, []byte(victimContent), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(victimPath, filepath.Join(repo, managed)); err != nil {
				t.Skipf("hard links unavailable on this filesystem: %v", err)
			}

			var out bytes.Buffer
			runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
			if runErr == nil {
				t.Fatalf("init-agents succeeded while writing through a hard link into the git directory:\n%s", out.String())
			}
			if !errors.Is(runErr, errSharedInodeManagedTarget) {
				t.Fatalf("want a hard-link refusal, got %v\n%s", runErr, out.String())
			}

			after, err := os.ReadFile(victimPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != victimContent {
				t.Fatalf(".git/config was written through a hard link:\n%s", after)
			}
		})
	}
}

// TestInitAgentsRefusesAHardLinkedTargetBeforeWritingAnything pins WHEN the hard-link refusal
// lands, not just that it lands.
//
// The guard is enforced on the write, because an open handle is the only thing that can be asked
// about an inode — but the first write that reaches it is the one that creates or overwrites the
// guide. Measured on this branch before the preflight: `AGENTS.md` hard-linked to `.git/config`
// printed "wrote .entire/graph-agent.md", left that file behind, and — where the user already had
// a guide of their own — replaced its contents with the managed guide, all while exiting non-zero.
// The error path does not call rollback, and rollback could not have restored an overwritten guide
// in any case, so the only place this can be refused without damage is before the first write.
//
// The second case is also the one docs/agents.md used to promise worked. An inode's other names
// cannot be enumerated from a handle, so a name that is not a managed target is refused whatever it
// is — the docs now say so, and this pins the behavior they describe.
func TestInitAgentsRefusesAHardLinkedTargetBeforeWritingAnything(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		// plant returns the path AGENTS.md is hard-linked to.
		plant func(t *testing.T, repo string) string
	}{
		{
			name: "second name in the git directory",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				victim := filepath.Join(repo, ".git", "config")
				mkdirAllForTest(t, filepath.Dir(victim))
				writeFileForTest(t, victim, gitAdministrativeConfig)
				return victim
			},
		},
		{
			name: "second name is an ordinary file outside the project",
			plant: func(t *testing.T, repo string) string {
				t.Helper()
				outside := filepath.Join(t.TempDir(), "shared-AGENTS.md")
				writeFileForTest(t, outside, "# shared house rules\n")
				return outside
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			for _, guide := range []string{"missing", "already the user's own"} {
				t.Run(guide, func(t *testing.T) {
					t.Parallel()
					repo := t.TempDir()

					victim := testCase.plant(t, repo)
					if err := os.Link(victim, filepath.Join(repo, "AGENTS.md")); err != nil {
						t.Skipf("hard links unavailable on this filesystem: %v", err)
					}

					guidePath := filepath.Join(repo, ".entire", "graph-agent.md")
					const ownGuide = "# my own graph notes\n"
					if guide != "missing" {
						mkdirAllForTest(t, filepath.Dir(guidePath))
						writeFileForTest(t, guidePath, ownGuide)
					}

					var out bytes.Buffer
					runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
					if runErr == nil {
						t.Fatalf("init-agents wrote through an unaccounted hard link:\n%s", out.String())
					}
					if !errors.Is(runErr, errSharedInodeManagedTarget) {
						t.Fatalf("want a hard-link refusal, got %v\n%s", runErr, out.String())
					}
					if out.Len() != 0 {
						t.Fatalf("the refusal reported an install it then abandoned:\n%s", out.String())
					}

					if guide == "missing" {
						if _, err := os.Lstat(guidePath); !os.IsNotExist(err) {
							t.Fatalf("partial install: the guide exists after the refusal (%v)", err)
						}
					} else if got := readFileForTest(t, guidePath); got != ownGuide {
						t.Fatalf("the user's own guide was replaced despite the refusal:\n%s", got)
					}
					if _, err := os.Lstat(filepath.Join(repo, "CLAUDE.md")); !os.IsNotExist(err) {
						t.Fatalf("partial install: CLAUDE.md exists after the refusal (%v)", err)
					}
					if got := readFileForTest(t, victim); strings.Contains(got, agentPointerBegin) {
						t.Fatalf("the shared inode was written through:\n%s", got)
					}
				})
			}
		})
	}
}

// TestInitAgentsWritesThroughADirectorySpelledLikeTheGitDirectory keeps the case-folded `.git`
// refusal from outliving the reason it folds.
//
// The fold exists because macOS and Windows resolve a repository-chosen link target
// case-insensitively, so `CLAUDE.md -> .GIT/config` opens `.git/config` there and an exact
// comparison would be a bypass — TestInitAgentsRefusesManagedTargetInsideGitDirectory pins that,
// and skips where the spelling names nothing. On a case-sensitive filesystem `.GIT` is an ordinary
// directory git has never used, and the alias below is a contained markdown landing like any other.
// Measured before the filesystem check, on a case-sensitive volume: init-agents refused it as
// "inside the repository's git directory", which it is not.
func TestInitAgentsWritesThroughADirectorySpelledLikeTheGitDirectory(t *testing.T) {
	t.Parallel()
	skipIfSymlinksUnrepresentable(t)

	repo := t.TempDir()
	if caseInsensitiveFilesystem(t, repo) {
		t.Skip("filesystem folds case, so `.GIT` IS the git directory here")
	}
	plantGitAdministrativeDirectory(t, filepath.Join(repo, ".git"))
	mkdirAllForTest(t, filepath.Join(repo, ".GIT"))
	const ownRules = "# team rules\n"
	writeFileForTest(t, filepath.Join(repo, ".GIT", "rules.md"), ownRules)
	symlinkForTest(t, filepath.Join(".GIT", "rules.md"), filepath.Join(repo, "AGENTS.md"))

	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatalf("init-agents refused an ordinary directory named .GIT: %v\n%s", err, out.String())
	}
	got := readFileForTest(t, filepath.Join(repo, ".GIT", "rules.md"))
	if !strings.Contains(got, ownRules) || !strings.Contains(got, agentPointerBegin) {
		t.Fatalf("the alias target was not updated in place:\n%s", got)
	}
	// The real git directory is beside it and must still be untouched.
	if config := readFileForTest(t, filepath.Join(repo, ".git", "config")); config != gitAdministrativeConfig {
		t.Fatalf("the git directory was written through:\n%s", config)
	}
}

// TestInitAgentsWritesAnOrdinaryInstructionFile keeps the hard-link guard from
// becoming a blanket refusal. A guard that rejected every managed target would
// pass the test above and break the command, so the ordinary path is asserted
// beside it.
func TestInitAgentsWritesAnOrdinaryInstructionFile(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# house rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo}); err != nil {
		t.Fatalf("init-agents on an ordinary file: %v\n%s", err, out.String())
	}
	body, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "house rules") {
		t.Fatalf("the user's own text must survive:\n%s", body)
	}
}

// plantGitAdministrativeDirectory builds a git administrative directory at dir that is NOT named
// `.git`, so the name rule cannot see it. It is written by hand rather than by shelling out to
// `git init --separate-git-dir`, so the test states exactly which structure the refusal keys on and
// needs no git binary; the layout is the one `git init` produces and the one git's own
// is_git_directory() recognises.
func plantGitAdministrativeDirectory(t *testing.T, dir string) {
	t.Helper()
	mkdirAllForTest(t, filepath.Join(dir, "objects"))
	mkdirAllForTest(t, filepath.Join(dir, "refs", "heads"))
	mkdirAllForTest(t, filepath.Join(dir, "hooks"))
	writeFileForTest(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
	writeFileForTest(t, filepath.Join(dir, "config"), gitAdministrativeConfig)
}

const gitAdministrativeConfig = "[core]\n\trepositoryformatversion = 0\n\tbare = false\n"

// TestInitAgentsRefusesGitDirectoryNotNamedDotGit pins the git-directory refusal to the directory
// git actually uses rather than to the name `.git`.
//
// sem.PathLandsInGitDir judges a repo-relative STRING, so it refuses a landing that SPELLS a `.git`
// component and nothing else. Git does not require that spelling: `git init --separate-git-dir=admin`
// leaves a `.git` POINTER FILE and puts the real administrative directory at `admin/`, and a
// repository initialised under GIT_DIR has no `.git` entry at all. Measured on this branch before
// the fix, in a `--separate-git-dir` repository: `CLAUDE.md -> admin/config` exited 0 with the
// managed block appended to git's real config, after which every git command failed with "fatal:
// bad config line 9".
//
// The `.entire` case is the one no filename rule can catch. Its landing is `graph-agent.md`, an
// ordinary markdown name that the instruction-file allowlist admits — it is refused only because of
// WHERE it lands, which is inside the real ref store.
func TestInitAgentsRefusesGitDirectoryNotNamedDotGit(t *testing.T) {
	t.Parallel()
	skipIfSymlinksUnrepresentable(t)

	for _, testCase := range []struct {
		name string
		// build plants the repository and returns the file that must not be written.
		build func(t *testing.T, repo string) string
	}{
		{
			name: "gitlink pointer to an in-tree administrative directory",
			build: func(t *testing.T, repo string) string {
				t.Helper()
				plantGitAdministrativeDirectory(t, filepath.Join(repo, "admin"))
				writeFileForTest(t, filepath.Join(repo, ".git"), "gitdir: admin\n")
				symlinkForTest(t, filepath.Join("admin", "config"), filepath.Join(repo, "CLAUDE.md"))
				return filepath.Join(repo, "admin", "config")
			},
		},
		{
			name: "administrative directory with no gitlink at all",
			build: func(t *testing.T, repo string) string {
				t.Helper()
				plantGitAdministrativeDirectory(t, filepath.Join(repo, "admin"))
				hook := filepath.Join(repo, "admin", "hooks", "pre-commit")
				writeFileForTest(t, hook, "#!/bin/sh\nexit 0\n")
				symlinkForTest(t, filepath.Join("admin", "hooks", "pre-commit"), filepath.Join(repo, "AGENTS.md"))
				return hook
			},
		},
		{
			name: "guide directory alias into the real ref store",
			build: func(t *testing.T, repo string) string {
				t.Helper()
				plantGitAdministrativeDirectory(t, filepath.Join(repo, "admin"))
				writeFileForTest(t, filepath.Join(repo, ".git"), "gitdir: admin\n")
				symlinkForTest(t, filepath.Join("admin", "refs", "heads"), filepath.Join(repo, ".entire"))
				return filepath.Join(repo, "admin", "refs", "heads", "graph-agent.md")
			},
		},
		{
			name: "administrative directory with no object or ref store",
			build: func(t *testing.T, repo string) string {
				t.Helper()
				// A linked worktree's administrative directory keeps commondir and gitdir
				// in place of its own object and ref stores, so a test that asked only for
				// those two would not recognise it.
				worktreeAdmin := filepath.Join(repo, "wt")
				mkdirAllForTest(t, worktreeAdmin)
				writeFileForTest(t, filepath.Join(worktreeAdmin, "HEAD"), "ref: refs/heads/main\n")
				writeFileForTest(t, filepath.Join(worktreeAdmin, "commondir"), "../..\n")
				victim := filepath.Join(worktreeAdmin, "gitdir")
				writeFileForTest(t, victim, "/elsewhere/.git\n")
				writeFileForTest(t, filepath.Join(repo, ".git"), "gitdir: wt\n")
				symlinkForTest(t, filepath.Join("wt", "gitdir"), filepath.Join(repo, "CLAUDE.md"))
				return victim
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			victim := testCase.build(t, repo)
			before, existed := "", false
			if content, err := os.ReadFile(victim); err == nil {
				before, existed = string(content), true
			}

			var out bytes.Buffer
			runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
			requireGitDirRefusal(t, runErr, out.String())

			if existed {
				if got := readFileForTest(t, victim); got != before {
					t.Fatalf("%s was written through:\nwant: %q\n got: %q", victim, before, got)
				}
			} else if _, statErr := os.Lstat(victim); !os.IsNotExist(statErr) {
				t.Fatalf("%s was created inside the git directory (stat error %v)", victim, statErr)
			}
			for _, unwritten := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".entire", "graph-agent.md")} {
				path := filepath.Join(repo, unwritten)
				info, statErr := os.Lstat(path)
				if os.IsNotExist(statErr) {
					continue
				}
				if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
					// The planted alias itself, untouched.
					continue
				}
				t.Fatalf("partial install: %s exists after the refusal (%v)", unwritten, statErr)
			}
		})
	}
}

// TestInitAgentsRefusesLandingThatIsNotAnInstructionFile pins the write to the kind of file
// init-agents exists to write.
//
// The git-directory refusals name one destination each, which is a denylist, and a denylist of
// destinations cannot be finished. Measured on this branch before the fix, each of these exited 0
// with the managed block appended to the victim's real build, CI, environment or package file. The
// rule that replaces the list is the one thing that IS bounded: docs/agents.md offers the alias so
// AGENTS.md and CLAUDE.md may share one INSTRUCTION FILE, so a landing that is not an instruction
// file is not a use of the feature.
func TestInitAgentsRefusesLandingThatIsNotAnInstructionFile(t *testing.T) {
	t.Parallel()
	skipIfSymlinksUnrepresentable(t)

	for _, testCase := range []struct {
		name    string
		managed string
		victim  string
		content string
	}{
		{name: "workflow", managed: "CLAUDE.md", victim: filepath.Join(".github", "workflows", "ci.yml"), content: "name: ci\non: [push]\n"},
		{name: "makefile", managed: "CLAUDE.md", victim: "Makefile", content: "all:\n\techo hi\n"},
		{name: "direnv", managed: "CLAUDE.md", victim: ".envrc", content: "export FOO=bar\n"},
		{name: "package manifest", managed: "AGENTS.md", victim: "package.json", content: "{\"scripts\":{\"build\":\"tsc\"}}\n"},
		{name: "shell profile", managed: "AGENTS.md", victim: ".bashrc", content: "export PATH=$PATH:/bin\n"},
		{name: "guide alias", managed: filepath.Join(".entire", "graph-agent.md"), victim: "Makefile", content: "all:\n\techo hi\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			victimPath := filepath.Join(repo, testCase.victim)
			mkdirAllForTest(t, filepath.Dir(victimPath))
			writeFileForTest(t, victimPath, testCase.content)

			managedPath := filepath.Join(repo, testCase.managed)
			mkdirAllForTest(t, filepath.Dir(managedPath))
			relativeVictim, err := filepath.Rel(filepath.Dir(managedPath), victimPath)
			if err != nil {
				t.Fatal(err)
			}
			symlinkForTest(t, relativeVictim, managedPath)

			var out bytes.Buffer
			runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
			if runErr == nil {
				t.Fatalf("init-agents wrote its managed block into %s; output:\n%s", testCase.victim, out.String())
			}
			if !strings.Contains(runErr.Error(), "agent-instruction file") {
				t.Fatalf("the refusal did not name the instruction-file rule: %v", runErr)
			}
			// The refusal comes from the resolver, so it carries no syscall errno and is
			// indistinguishable from os.Root's own escape sentinel unless the classifier
			// names it. Every link here stays inside the repository.
			if strings.Contains(runErr.Error(), "leaves the repository") {
				t.Fatalf("a link that never left the root was reported as a repository escape: %v", runErr)
			}
			if got := readFileForTest(t, victimPath); got != testCase.content {
				t.Fatalf("%s was written through:\nwant: %q\n got: %q", testCase.victim, testCase.content, got)
			}
			for _, unwritten := range []string{"AGENTS.md", "CLAUDE.md", filepath.Join(".entire", "graph-agent.md")} {
				if unwritten == testCase.managed {
					continue
				}
				if _, statErr := os.Lstat(filepath.Join(repo, unwritten)); !os.IsNotExist(statErr) {
					t.Fatalf("partial install: %s exists after the refusal (%v)", unwritten, statErr)
				}
			}
		})
	}
}

// TestInitAgentsAllowsInstructionFileLandings bounds the refusal above from the other side. Each of
// these is a landing the documented alias is FOR, and each must keep working: a markdown file the
// repository already had, an agent rules file that carries no markdown extension, and — twice — a
// landing this command itself created, because a rule that admitted a target on the first run and
// refused it on the second would be worse than no rule.
func TestInitAgentsAllowsInstructionFileLandings(t *testing.T) {
	t.Parallel()
	skipIfSymlinksUnrepresentable(t)

	t.Run("existing markdown target", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		shared := filepath.Join(repo, "docs", "shared.md")
		mkdirAllForTest(t, filepath.Dir(shared))
		writeFileForTest(t, shared, "# Shared rules\n")
		symlinkForTest(t, filepath.Join("docs", "shared.md"), filepath.Join(repo, "AGENTS.md"))
		symlinkForTest(t, "AGENTS.md", filepath.Join(repo, "CLAUDE.md"))

		runInitAgentsForTest(t, repo)

		got := readFileForTest(t, shared)
		if !strings.Contains(got, "# Shared rules") || !strings.Contains(got, testAgentPointerBlock) {
			t.Fatalf("the shared instruction file lost content or the pointer:\n%s", got)
		}
	})

	t.Run("rules file without a markdown extension", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		rules := filepath.Join(repo, ".cursorrules")
		writeFileForTest(t, rules, "# Cursor rules\n")
		symlinkForTest(t, ".cursorrules", filepath.Join(repo, "CLAUDE.md"))

		runInitAgentsForTest(t, repo)

		got := readFileForTest(t, rules)
		if !strings.Contains(got, "# Cursor rules") || !strings.Contains(got, testAgentPointerBlock) {
			t.Fatalf("the rules file lost content or the pointer:\n%s", got)
		}
	})

	t.Run("landing this command created is writable again", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		// No markdown extension and not on any list: admitted the first time because it
		// does not exist, and the second time because it now carries this command's block.
		symlinkForTest(t, "NOTES", filepath.Join(repo, "AGENTS.md"))

		runInitAgentsForTest(t, repo)
		runInitAgentsForTest(t, repo)

		got := readFileForTest(t, filepath.Join(repo, "NOTES"))
		if strings.Count(got, agentPointerBegin) != 1 {
			t.Fatalf("the second run did not update the block in place:\n%s", got)
		}
	})

	t.Run("guide landing this command created is writable again", func(t *testing.T) {
		t.Parallel()
		repo := t.TempDir()
		mkdirAllForTest(t, filepath.Join(repo, ".entire"))
		symlinkForTest(t, filepath.Join("..", "GUIDE"), filepath.Join(repo, ".entire", "graph-agent.md"))

		runInitAgentsForTest(t, repo)
		runInitAgentsForTest(t, repo)

		if got := readFileForTest(t, filepath.Join(repo, "GUIDE")); got != agentGuide {
			t.Fatalf("the guide landing did not hold the guide after two runs:\n%s", got)
		}
	})
}

// TestAgentGuideHeadingIdentifiesTheGuide guards the derivation the re-run case above depends on. A
// heading that went empty, or that stopped appearing in the guide, would make
// landingCarriesManagedContent either admit every file or admit none.
func TestAgentGuideHeadingIdentifiesTheGuide(t *testing.T) {
	t.Parallel()
	if len(agentGuideHeading) < len("# entire-graph") {
		t.Fatalf("the guide heading is too short to identify anything: %q", agentGuideHeading)
	}
	if !strings.Contains(agentGuide, agentGuideHeading) {
		t.Fatalf("the guide no longer contains its own heading %q", agentGuideHeading)
	}
	if strings.Contains("all:\n\techo hi\n", agentGuideHeading) {
		t.Fatalf("the guide heading matches unrelated file content: %q", agentGuideHeading)
	}
}

// TestInitAgentsRefusesHardLinkWhoseCountASymlinkAliasInflates covers the way
// the hard-link guard could be talked out of firing.
//
// The guard asks whether every one of an inode's names is a managed instruction
// file, and answers by counting managed names that resolve to the same inode.
// Only real directory entries contribute to an inode's LINK COUNT, so a symlink
// alias — the documented way to share one instruction file — resolves to the
// inode without being one of its names, and counting it inflates the managed
// tally without inflating the link count.
//
// Measured before the fix: CLAUDE.md hard-linked to .git/config (2 names) plus
// AGENTS.md symlinked to CLAUDE.md counted 2 managed names against 2 links, so
// `init-agents` exited 0 and rewrote git's config — the exact corruption the
// guard exists to prevent, reached by inflating its own evidence.
func TestInitAgentsRefusesHardLinkWhoseCountASymlinkAliasInflates(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()

	victimPath := filepath.Join(repo, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(victimPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const victimContent = "[core]\n\tbare = false\n"
	if err := os.WriteFile(victimPath, []byte(victimContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victimPath, filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Skipf("hard links unavailable on this filesystem: %v", err)
	}
	if err := os.Symlink("CLAUDE.md", filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	runErr := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"init-agents", "--repo", repo})
	if runErr == nil {
		t.Fatalf("init-agents succeeded while writing through a hard link into the git directory:\n%s", out.String())
	}
	if !errors.Is(runErr, errSharedInodeManagedTarget) {
		t.Fatalf("want a hard-link refusal, got %v\n%s", runErr, out.String())
	}
	after, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != victimContent {
		t.Fatalf(".git/config was written through a hard link:\n%s", after)
	}
}
