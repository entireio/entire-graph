package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentGuidePrintsDoctrine(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"agent-guide"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SEARCH FIRST", "entire graph search", "--profile full", "neighbors"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("agent-guide output missing %q:\n%s", want, out.String())
		}
	}
}

// The frugality clamp ("one edit, never build or test") measurably produced patches that could
// not compile: 131/300 resolved vs the baseline's 150/300 on SWE-bench Multilingual, with zero
// builds/tests on 22 of the 31 paired losses. The guide must require bounded verification and
// must never reintroduce the clamp, so both halves are asserted here.
func TestAgentGuideRequiresBoundedVerification(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Stdout: &out, Stderr: &out}, []string{"agent-guide"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	// verification is mandatory, framed with its honest cost, and reachable from the reference
	for _, want := range []string{
		"ALWAYS verify your edit at least once",
		"run the nearest existing test",
		"optional overhead",
		"does not build fails 100% of the task",
		"VERIFY, DON'T CHASE",
		"verify  ->",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide missing verification doctrine %q:\n%s", want, got)
		}
	}

	// a fix is often more than one edit: the sibling/caller sweep must be part of finishing
	for _, want := range []string{
		"not one edit in one place",
		"entire graph impact --repo . --symbol X",
		"co-change files, and same-container",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide missing completeness doctrine %q:\n%s", want, got)
		}
	}

	// bounded, not unbounded: the anti-loop clause has to survive alongside the mandate
	for _, want := range []string{
		"edit->test->edit loop",
		"already failing before you started",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide missing verification bound %q:\n%s", want, got)
		}
	}

	// the token discipline that was genuine must not be lost while fixing the clamp
	for _, want := range []string{
		"never grep/find/cat to locate code",
		"NEVER read a whole file",
		"do NOT re-search or grep to \"confirm\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide lost token discipline %q:\n%s", want, got)
		}
	}

	// The three blocks that replace a tool call have to be taught as INSTRUCTIONS, in the order an
	// agent uses them, and each with the specific action that removes the round-trip. Their wording
	// is pinned because it has to stay consistent with the measured harness prompt.
	for _, want := range []string{
		"SAME-CONCEPT LITERALS",
		"This IS your sweep",
		"Do not grep for any of them",
		"It is a\ncommand, not a suggestion",
		"Run it ONCE when your edits are in",
		"Never hunt a green suite",
		"CLOSED-SET WARNING",
		"add the missing arm before you stop",
		"not a build error",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide missing agent-asked block doctrine %q:\n%s", want, got)
		}
	}

	// The reference blocks are off by default, and the guide has to say so honestly — an agent that
	// expects a container map and does not get one will go and read the file instead.
	for _, want := range []string{
		"Reference blocks (off by default)",
		"--reference-blocks all",
		"cost turns and money without improving the result",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("agent-guide missing the reference-block gating note %q:\n%s", want, got)
		}
	}

	// regression guard: the exact clamp wording, and its softer variants, must stay gone
	for _, banned := range []string{
		"Do not run builds or test suites",
		"do not run builds or test suites",
		"unless the task explicitly requires it",
		"minimal edit and STOP",
		"stop the moment you can justify the fix",
	} {
		if strings.Contains(got, banned) {
			t.Fatalf("agent-guide reintroduced the frugality clamp %q:\n%s", banned, got)
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
	// the installed file must carry the same doctrine the binary prints — both halves of it
	for _, want := range []string{"SEARCH FIRST", "ALWAYS verify your edit at least once", "VERIFY, DON'T CHASE"} {
		if !strings.Contains(string(guide), want) {
			t.Fatalf("installed guide missing %q:\n%s", want, guide)
		}
	}
	if strings.Contains(string(guide), "Do not run builds or test suites") {
		t.Fatalf("installed guide reintroduced the frugality clamp:\n%s", guide)
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
	// the pointer is the only text an agent sees before opening the guide: it must advertise
	// both halves, or the verification half is invisible to agents that never follow the link
	for _, want := range []string{"search-first, verify-once", "run the nearest existing test"} {
		if !strings.Contains(string(agents), want) {
			t.Fatalf("pointer block missing %q:\n%s", want, agents)
		}
	}

	claude, err := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	if !strings.Contains(string(claude), "@.entire/graph-agent.md") {
		t.Fatalf("CLAUDE.md missing import line:\n%s", claude)
	}

	// idempotence: second run must not duplicate the block
	run()
	agents2, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if got := strings.Count(string(agents2), agentPointerBegin); got != 1 {
		t.Fatalf("pointer block duplicated (%d occurrences):\n%s", got, agents2)
	}
	claude2, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if got := strings.Count(string(claude2), agentPointerBegin); got != 1 {
		t.Fatalf("CLAUDE.md block duplicated (%d occurrences):\n%s", got, claude2)
	}
}
