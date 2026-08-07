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
// It is resolution-first: graph retrieval narrows exploration, but source inspection and
// focused verification remain required before an agent declares the task complete.
const agentGuide = `# entire-graph — instructions for coding agents (follow directly)

You have a deterministic local code graph: ` + "`entire graph`" + ` (functions, classes, methods,
types, routes + call/inheritance relations; no network). These instructions are FOR YOU, the
agent reading this file. Use the graph to narrow exploration without trading away correctness.

## The workflow (mandatory for locate/fix/change tasks)

Your FIRST action on any task that requires finding code must be ONE search:

    entire graph search --repo . --profile full --query "<the task or bug in one sentence>"

Then open the top hit's file with your file-read tool (pass a line range around the reported
line), inspect enough surrounding behavior to justify the change, and make the smallest complete
edit. Treat graph output as evidence, not an oracle.

## Hard rules

1. SEARCH FIRST — never grep/find/cat to locate code before you have searched.
2. READ focused source around the result. Widen the check when aliases, generated code, dynamic
   dispatch, or related implementations could matter.
3. Use graph follow-ups only when they answer a real question. For impact or callers, prefer:
       entire graph impact --repo . --symbol X
   (one shot: callers, callees, type consumers, data flow, co-change files, siblings)
   A caller is reported at its CALL SITE: ` + "`- f (path:476, def :24)`" + ` means the call is on line 476
   inside a function starting at line 24 — go to the first number. For the conditions in force at
   that call (the enclosing ` + "`if let`" + ` / ` + "`match`" + ` chain, verbatim, plus a ~10-line window), ask
       entire graph neighbors --repo . --symbol X --relation CALLS --direction in
   which is enough to patch safely without reading the caller's body. A direction you did not ask
   for is reported as "not queried", never as empty; use ` + "`--direction both`" + ` when you want both.
   The first relation query on a repo indexes the WHOLE repository (search parses only its
   candidate files); after that it is cached and sub-second, so ask twice rather than batching.
   A ` + "`Completeness:`" + ` line scoped to another language ("no parse failures in Rust; N elsewhere")
   means the answer is complete — it is not a reason to fall back to grep.
6. ALWAYS verify your edit at least once before you finish: run the VERIFY command the payload
   gave you, or — when it gave none — build/compile what you touched or run the nearest existing
   test. Pick the narrowest command that would still catch a syntax, type, name, or arity error
   (one package, one file, one test — not the whole suite).
6b. The SAME-CONCEPT LITERAL block IS your repo-wide sweep: fix its ` + "`EDIT`" + ` sites, ignore its
   ` + "`CONSUMER`" + ` sites, and do not grep for either. Its header states the repository's own totals.
6c. Adding a variant to an enum / sealed set / union / const group? If the CLOSED-SET WARNING says
   ` + "`checked at runtime`" + `, add the missing switch arm before you finish. That failure is a runtime
   throw, not a compile error, so verification will not catch it either.
6d. WHEN THE BUILD FAILS, PIPE IT — do not grep. Run the verify command with the declaration
   lookup already attached, as ONE command, so the failure and the declarations it names arrive
   together:
       <the VERIFY command> 2>&1 | entire graph explain --repo .
   For every name the error mentions — unknown identifier, wrong signature, missing field or
   method, wrong arity — that prints where it is declared and its signature, read from your
   WORKING TREE, so it reflects the edit you just made. A name the repository does not define is
   reported as such, which is the answer when you have invented a method. This exists because it
   is the phase the graph was losing: measured on a 30-instance paired run, pre-edit exploration
   fell 9.43 -> 1.34 turns per session while post-edit exploration only moved 8.60 -> 6.76 and
   post-edit reads went the wrong way, 3.57 -> 4.07. Piping costs no turn of its own; grepping the
   same answer costs three.
7. VERIFY, DON'T CHASE. Verification is bounded: run it, read the error, fix exactly what the
   error names, re-run — a couple of iterations, not more. Do NOT enter an edit->test->edit loop
   hunting a green suite, and do not "fix" failures that were already failing before you started.
8. Every extra turn re-reads your whole context — that is the token cost. Reach the edit in as
   few turns as possible, then spend the one turn that proves it builds, and stop.

## When NOT to use the graph

If the task already names the exact file and it is small, just read it — the graph saves tokens
by eliminating exploration; when there is nothing to explore, skip it.

## Reference

    locate  ->  entire graph search --repo . --profile full --query "..."
    impact  ->  entire graph impact --repo . --symbol X   (one shot: callers, callees, type consumers, data flow, co-change, siblings)
    callers ->  entire graph neighbors --repo . --symbol X --relation CALLS --direction in
    change  ->  entire graph diff --base A --head B --json
    detect  ->  entire graph capabilities --json   (inventory-only languages have no relations)
    verify  ->  the VERIFY line the search printed, run once, after editing. When there was none,
                this project's own narrowest build/test command
                (e.g. ` + "`go build ./internal/foo/...`" + `, ` + "`pytest tests/test_foo.py -k name`" + `, ` + "`cargo check -p crate`" + `)
    explain ->  <verify command> 2>&1 | entire graph explain --repo .   (declarations for every
                symbol the FAILURE names, from the working tree so your own edits are included;
                run it as one command with the build — never grep a compiler error)
    extras  ->  entire graph search ... --reference-blocks all   (container map, signature types,
                declaration card — off by default; measured to cost turns in agent sessions, kept
                for interactive reading)
    stats   ->  entire graph stats --repo .        (human-facing token-savings report; not part of your workflow — do not run it unless asked)
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
		"grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance\n" +
		"for using graph retrieval, focused source inspection, and verification.\n" +
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
