package cli

import (
	"bytes"
	"context"
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
			repo := t.TempDir()
			linkPath := filepath.Join(repo, tt.link)
			if err := os.Symlink(tt.target, linkPath); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

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

// skipIfSymlinksUnrepresentable guards the containment tests. The attack input is a symlink
// committed to a repository, and a default Windows checkout does not materialize one: without
// core.symlinks (which needs SeCreateSymbolicLinkPrivilege) git writes a plain text file holding
// the link target instead, so the escaping alias cannot exist in a checked-out tree there.
func skipIfSymlinksUnrepresentable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a committed symlink is not checked out as a symlink on Windows by default")
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
			if err := os.Symlink(filepath.Join("..", "victim.md"), filepath.Join(repo, aliasName)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

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
	if err := os.Symlink(filepath.Join("docs", "shared.md"), aliasPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

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
				if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(repo, ".entire")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
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
				if err := os.Symlink(filepath.Join("..", "..", "victim.md"), link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
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
	if err := os.Symlink(filepath.Join("..", "victim.md"), filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "absent.md"), filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

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
		t.Skipf("symlinks unavailable: %v", err)
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

// aliasPlant installs one alias spelling into a repository. target names the install path whose
// resolution it subverts, and plant returns the in-repository file that spelling reaches only
// after ".." is collapsed as text — the file init-agents must not touch.
type aliasPlant struct {
	name   string
	target string
	plant  func(t *testing.T, repo string) string
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
		name:   "landing inside the git directory",
		target: "AGENTS.md",
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
// on the file the kernel's own resolution reaches. Every alias here stays inside the repository,
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
