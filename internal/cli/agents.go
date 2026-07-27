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
//
// The search-first half of this guide is what produced the official SWE-bench Multilingual
// token result (300 instances / 9 languages: 54.9% weighted token savings vs a no-tool agent,
// double the next-best tool's 27.4%, 8/9 languages; Sonnet 3x replication 57.7% vs 36.6%).
// The verification half is a correction to that same run: the frugality clamp it carried ("do
// not run builds or test suites", "one edit, then STOP") measurably cost correctness — 131/300
// resolved (43.7%) vs the baseline's 150/300 (50.0%), McNemar p=0.013, and on the 31 paired
// losses where both agents found the right file the graph agent ran zero builds/tests on 22 and
// made a single edit on 22 (baseline 26/31 and 8/31), with two patches that could not compile.
// The rules below therefore keep the token discipline (search instead of grepping, line ranges
// instead of whole files, no re-confirming a deterministic index) and drop the clamp: verify
// once, bounded, and check the sibling sites before finishing. See the graphmark repo for
// methodology, the as-measured prompt, and caveats.
const agentGuide = `# entire-graph — instructions for coding agents (follow directly)

You have a deterministic local code graph: ` + "`entire graph`" + ` (functions, classes, methods,
types, routes + call/inheritance relations; no network). These instructions are FOR YOU, the
agent reading this file. Following them is measured to cut session tokens roughly in half —
while still shipping a patch that compiles.

## The workflow (mandatory for locate/fix/change tasks)

Your FIRST action on any task that requires finding code must be ONE search:

    entire graph search --repo . --profile full --query "<the task or bug in one sentence>"

The first five hits come back as the COMPLETE body of their enclosing function/method (marked
` + "`complete-symbol`" + `; ` + "`snippet_start_line`..`snippet_end_line`" + ` is the whole callable, verbatim).
When the hit you want is one of them, EDIT DIRECTLY FROM THE SEARCH OUTPUT — you already have
the entire function, and opening the file costs a whole extra turn for nothing. Only when the
hit you want carries a two-line locator window instead, open its file at the reported line
range. Either way: make the minimal edit that fixes the root cause. The top hit is the fix site
on most tasks — go straight there; do NOT re-search or grep to "confirm".

Then finish the job. Two bounded steps, not a loop:

**1. Complete the fix.** A fix is often not one edit in one place. Before you call it done, ask
the graph once for the sites that share the defect:

    entire graph impact --repo . --symbol X

One shot: callers, callees, type consumers, data flow, co-change files, and same-container
siblings. Apply the same change to the ones that carry the same bug; ignore the rest.

**2. Verify once.** Compile what you touched, or run the nearest existing test. This is NOT
optional overhead. An unverified edit is how a patch ships an unused variable, a wrong field or
method name, or the wrong arity — and a patch that does not build fails 100% of the task. One
verification turn costs one turn; a patch that does not compile costs everything.

Above the ranking sits the **CONTAINER MAP** of the top hit: the file's total line count and the
enclosing class/module's members, each with its line range and a one-line identity, with the hit's
own member marked ` + "`*`" + `. Read it before you decide to open anything. It is what lets you size a
RANGE read instead of reading the file — and it is the only place the payload names the members a
member-ranked list cannot rank, such as a private nested enum whose constants are the variant set
you have to extend. A member marked ` + "`NESTED`" + ` with a ` + "`+N members`" + ` count is a type declared inside
this one; its range is exactly where it is.

The output is grouped, and the groups mean different things:

* the ranked list is **candidate fix sites** — start at the top.
* **RELATED SITES** (` + "`section: \"related\"`" + `) are the other places this change usually has to
  land: a near-duplicate body needs the SAME edit, a sibling implementation needs the same edit
  in its own terms, a caller needs adjusting to the changed contract. Before you finish, check
  them — a patch applied to one site of a family is the commonest way a correct fix still fails.
* **DOCS & FIXTURES** (` + "`section: \"docs-and-fixtures\"`" + `) matched your words but hold no program
  text. Do not spend a read there looking for the bug unless the task IS the document.
* **TYPES IN THIS SIGNATURE** are the declarations of the types the top hit's own signature
  names — their fields and the SIGNATURES of their members, no bodies. That is the answer to
  "what else can build one of these / what can I call on it", so you do not have to open the
  type's file to write the patch. It is funded out of redundant tail locators, so it costs no
  extra bytes, and it is deliberately NOT transitive.

## Hard rules (each violation costs real money)

1. SEARCH FIRST — never grep/find/cat to locate code before you have searched.
2. ONE search, then act. Do not run a second search unless the first clearly missed.
3. Do NOT re-read what search already gave you. A ` + "`complete-symbol`" + ` hit is the whole function:
   edit it. Read a line range only for hits search returned as locators.
4. NEVER read a whole file; read at most ~120 lines around the reported line.
5. Impact question ("who calls X" / "what could this change break" / "where else needs this
   same change")? ONE targeted query:
       entire graph impact --repo . --symbol X
   (one shot: callers, callees, type consumers, data flow, co-change files, siblings)
6. ALWAYS verify your edit at least once before you finish: build/compile it, or run the nearest
   existing test. Pick the narrowest command that would still catch a syntax, type, name, or
   arity error (one package, one file, one test — not the whole suite).
7. VERIFY, DON'T CHASE. Verification is bounded: run it, read the error, fix exactly what the
   error names, re-run — a couple of iterations, not more. Do NOT enter an edit->test->edit loop
   hunting a green suite, and do not "fix" failures that were already failing before you started.
8. Every extra turn re-reads your whole context — that is the token cost. Reach the edit in as
   few turns as possible, then spend the one turn that proves it builds, and stop.

## When NOT to use the graph

If the task already names the exact file and it is small, just read it — the graph saves tokens
by eliminating exploration; when there is nothing to explore, skip it. Verification still
applies: it is about the edit you made, not about how you found it.

## Reference

    locate  ->  entire graph search --repo . --profile full --query "..."
    surface ->  entire graph def --repo . NAME    (what a name IS: for a type its fields + member
                signatures — impl blocks, receiver methods, extension members, partial parts, one
                hop of trait/module/base acquisition; for a method its OWNING type; for a
                trait/interface WHO IMPLEMENTS it. Use it when you need a type's API, not as a
                routine follow-up to search.)
    impact  ->  entire graph impact --repo . --symbol X   (one shot: callers, callees, type consumers, data flow, co-change, siblings)
    callers ->  entire graph neighbors --repo . --symbol X --relation CALLS --direction in
    change  ->  entire graph diff --base A --head B --json
    detect  ->  entire graph capabilities --json   (inventory-only languages have no relations)
    verify  ->  this project's own narrowest build/test command, once, after editing
                (e.g. ` + "`go build ./internal/foo/...`" + `, ` + "`pytest tests/test_foo.py -k name`" + `, ` + "`cargo check -p crate`" + `)
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
		"grep/find/whole-file reads, read .entire/graph-agent.md — the search-first, verify-once\n" +
		"doctrine for coding agents: search instead of grepping, then check the sibling sites and\n" +
		"compile (or run the nearest existing test) once before you finish.\n" +
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
