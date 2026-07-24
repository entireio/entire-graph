package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentGuide is the canonical, agent-agnostic operating guide for coding agents using the
// graph in a CONSUMING project (not this repo). It ships inside the binary so every install
// carries the current doctrine; `init-agents` distributes it into a project's AGENTS.md /
// CLAUDE.md via a small pointer block, and `agent-guide` prints it for any agent or human.
// The prompt block is the exact instruction set that won the official SWE-bench Multilingual
// benchmark (300 instances / 9 languages: 54.9% weighted token savings vs a no-tool agent,
// double the next-best tool's 27.4%, 8/9 languages; Sonnet 3x replication 57.7% vs 36.6% —
// see the graphmark repo for methodology, prompts, and caveats).
const agentGuide = `# entire-graph — instructions for coding agents (follow directly)

You have a deterministic local code graph: ` + "`entire graph`" + ` (functions, classes, methods,
types, routes + call/inheritance relations; no network). These instructions are FOR YOU, the
agent reading this file. Following them is measured to cut session tokens roughly in half.

## The workflow (mandatory for locate/fix/change tasks)

Your FIRST action on any task that requires finding code must be ONE search:

    entire graph search --repo . --profile full --query "<the task or bug in one sentence>"

Then open the top hit's file with your file-read tool (pass a line range around the reported
line), make the minimal edit, and STOP. The top hit is the fix site on most tasks — go straight
there and edit; do NOT re-search or grep to "confirm".

## Hard rules (each violation costs real money)

1. SEARCH FIRST — never grep/find/cat to locate code before you have searched.
2. ONE search, then act. Do not run a second search unless the first clearly missed.
3. After search, READ a line range and EDIT. Do not chain more graph commands to "explore".
4. NEVER read a whole file; read at most ~120 lines around the reported line.
5. Impact question ("who calls X")? ONE targeted query, not a graph dump:
       entire graph neighbors --repo . --symbol X --relation CALLS --direction in
6. Do not run builds or test suites unless the task explicitly requires it.
7. Every extra turn re-reads your whole context — that is the token cost. Reach the edit in as
   few turns as possible and stop the moment you can justify the fix.

## When NOT to use the graph

If the task already names the exact file and it is small, just read it — the graph saves tokens
by eliminating exploration; when there is nothing to explore, skip it.

## Reference

    locate  ->  entire graph search --repo . --profile full --query "..."
    impact  ->  entire graph impact --repo . --symbol X   (one shot: callers, callees, type consumers, data flow, co-change, siblings)
    callers ->  entire graph neighbors --repo . --symbol X --relation CALLS --direction in
    change  ->  entire graph diff --base A --head B --json
    detect  ->  entire graph capabilities --json   (inventory-only languages have no relations)
`

// agentPointerBegin/End delimit the block init-agents manages inside AGENTS.md / CLAUDE.md,
// so re-runs update in place instead of appending duplicates.
const (
	agentPointerBegin = "<!-- entire-graph:begin -->"
	agentPointerEnd   = "<!-- entire-graph:end -->"
)

func runAgentGuide(opts Options, args []string) error {
	fs := flag.NewFlagSet("agent-guide", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprint(opts.Stdout, agentGuide)
	return nil
}

// runInitAgents installs the guide into a consuming project so ANY coding agent finds it:
// writes .entire/graph-agent.md (plugin-managed, overwritten on re-run) and upserts a
// marker-guarded pointer block into AGENTS.md (the cross-agent convention) and CLAUDE.md
// (which additionally understands the @-import line).
func runInitAgents(opts Options, args []string) error {
	fs := flag.NewFlagSet("init-agents", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	repo := fs.String("repo", ".", "project root to install the agent guide into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("init-agents: %s is not a directory", root)
	}

	guidePath := filepath.Join(root, ".entire", "graph-agent.md")
	if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(guidePath, []byte(agentGuide), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "wrote %s\n", guidePath)

	pointer := agentPointerBegin + "\n" +
		"This repo has the entire-graph code graph installed. Before exploring code with\n" +
		"grep/find/whole-file reads, read .entire/graph-agent.md — search-first doctrine for\n" +
		"coding agents (measured to cut agent token usage roughly in half on SWE-bench tasks).\n" +
		"@.entire/graph-agent.md\n" +
		agentPointerEnd + "\n"

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(root, name)
		if err := upsertPointerBlock(path, pointer); err != nil {
			return fmt.Errorf("init-agents: %s: %w", name, err)
		}
		fmt.Fprintf(opts.Stdout, "updated %s\n", path)
	}
	return nil
}

// upsertPointerBlock appends the block to path (creating the file if absent), or replaces the
// existing marker-delimited block in place, so repeated runs never duplicate content.
func upsertPointerBlock(path, block string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	begin := strings.Index(content, agentPointerBegin)
	end := strings.Index(content, agentPointerEnd)
	switch {
	case begin >= 0 && end > begin:
		content = content[:begin] + strings.TrimSuffix(block, "\n") + content[end+len(agentPointerEnd):]
	case len(content) == 0:
		content = block
	default:
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + block
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
