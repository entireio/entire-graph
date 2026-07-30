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

## The three blocks that replace a tool call

One search returns three more blocks, and each one exists because it removes a round-trip that was
measured out of real sessions. Use them as instructions, not as background reading.

**SAME-CONCEPT LITERAL** — every place in the repository where the one distinctive literal that
names this concept is spelled out, each tagged ` + "`EDIT`" + `, ` + "`CONSUMER`" + ` or ` + "`DOC`" + `, with the header stating
the repository-wide totals. **This IS your sweep.** Fix the ` + "`EDIT`" + ` sites — they declare or register
the concept. Ignore the ` + "`CONSUMER`" + ` ones — they only pass the string, so they need no change. ` + "`DOC`" + ` is
prose that usually has to be updated with the behaviour. Do not grep for any of them: the totals in
the header are the repository's own counts, so when the block is present you have already seen the
whole set. A site the payload already printed is not repeated in the list.

**VERIFY** — the narrowest command that exercises the file you are changing, derived from the
repository's own build files, with the test file it targets and the evidence it came from. It is a
command, not a suggestion. Run it ONCE when your edits are in. Read the error, fix exactly what it
names, re-run at most once. Never hunt a green suite, and never write a throwaway test script. A
patch that does not build fails 100% of the task, which costs far more than the one turn this takes.
When the block is absent the repository's build files did not license a narrow command — use the
narrowest one you know for the file you touched.

**CLOSED-SET WARNING** — when the top hit is, or belongs to, a closed variant set (an enum, a sealed
hierarchy, a union type, a typed const group), this names the switch/match sites over it and says
for each whether it is exhaustive, what its fall-through arm does, and whether a missing arm is
caught by the COMPILER or only at RUNTIME. If you are adding a variant and the block says
` + "`checked at runtime`" + `, **add the missing arm before you stop** — that failure is a throw in
production, not a build error, so nothing else in your workflow will catch it. The block only
appears when the compiler would not catch it; its silence means the compiler has you covered.

## Reference blocks (off by default)

The CONTAINER MAP, TYPES IN THIS SIGNATURE and DECLARATIONS blocks are OFF by default. Measured on
real sessions they cost turns and money without improving the result — they answer questions an
agent was not about to ask, and their bytes are replayed on every later turn. They are still there
for interactive use: ` + "`--container-map`" + `, ` + "`--signature-types`" + `, ` + "`--type-card`" + `, or
` + "`--reference-blocks all`" + ` (env ` + "`ENTIRE_GRAPH_REFERENCE_BLOCKS`" + ` for a whole session).

The output is grouped, and the groups mean different things:

* the ranked list is **candidate fix sites** — start at the top.
* **RELATED SITES** (` + "`section: \"related\"`" + `) are the other places this change usually has to
  land: a near-duplicate body needs the SAME edit, a sibling implementation needs the same edit
  in its own terms, a caller needs adjusting to the changed contract. Before you finish, check
  them — a patch applied to one site of a family is the commonest way a correct fix still fails.
* **DOCS & FIXTURES** (` + "`section: \"docs-and-fixtures\"`" + `) matched your words but hold no program
  text. Do not spend a read there looking for the bug unless the task IS the document.
* **COVERING TEST** (` + "`section: \"covering-test\"`" + `) is the existing test that exercises hit 1 — the
  statement of what your fix has to ACHIEVE. It is not a fix site, and the VERIFY command above is
  derived from its path.
* **ALSO COVERING** names the OTHER tests that exercise the same code and states how many there
  are. They all have to keep passing, so a change whose justification only satisfies the one test
  printed above is not finished. Run them together — the VERIFY command already does.

## Hard rules (each violation costs real money)

1. SEARCH FIRST — never grep/find/cat to locate code before you have searched.
2. ONE search, then act. Do not run a second search unless the first clearly missed.
2a. TWO SEARCHES MAXIMUM, THEN SWITCH TOOLS. If two searches have not put you on the fix site,
   the wording is not the problem and a third phrasing will not help — grep for a LITERAL from
   the issue instead (an error message, an identifier, a flag, a rule or error code, a constant),
   then read around the hit. Rephrasing the same question is the single most expensive way to
   fail: measured on SWE-bench Multilingual, sessions that ran away did 8.4 searches on average
   against 2.7 for normal sessions (worst case 23 near-identical rephrasings), and those runaway
   sessions cost 2.2x what a no-tool agent spent on the same task. Search is how you start; it
   is not how you recover.
3. Do NOT re-read what search already gave you. A ` + "`complete-symbol`" + ` hit is the whole function:
   edit it. Read a line range only for hits search returned as locators.
4. NEVER read a whole file; read at most ~120 lines around the reported line.
5. Impact question ("who calls X" / "what could this change break" / "where else needs this
   same change")? ONE targeted query:
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
7. VERIFY, DON'T CHASE. Verification is bounded: run it, read the error, fix exactly what the
   error names, re-run — a couple of iterations, not more. Do NOT enter an edit->test->edit loop
   hunting a green suite, and do not "fix" failures that were already failing before you started.
8. Every extra turn re-reads your whole context — that is the token cost. Reach the edit in as
   few turns as possible, then spend the one turn that proves it builds, and stop.

## When NOT to use the graph

If the task already names the exact file and it is small, just read it — the graph saves tokens
by eliminating exploration; when there is nothing to explore, skip it. Verification still
applies: it is about the edit you made, not about how you found it.

That is not a minor caveat, so here is the measurement behind it. A search payload enters the
context and is re-read on every later turn, which costs roughly 10-20% more per turn. The graph
therefore only pays when it DELETES turns you would otherwise spend grepping and reading. Measured
on SWE-bench Multilingual, same instances, same prompt discipline, comparing against an agent with
no tool at all:

	baseline turns   turns the graph removed   total tokens
	30.2             6.3                       -32.3%   (CI excludes zero)
	15.6             -0.6 (it ADDED turns)     +13.9%   (not significant, n=19)
	10.1             -0.9 (it ADDED turns)     +31.4%   (CI excludes zero)

Break-even is near a 20-turn baseline. So: reach for the graph when you expect to hunt — an
unfamiliar or large repository, a symptom whose cause is somewhere you cannot name, a change whose
sibling sites you cannot enumerate. Skip it when you already know where to go, because on a task
you would have solved in ten turns the payload is pure overhead. This is a property of the TASK,
not of the tool being good or bad.

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
    verify  ->  the VERIFY line the search printed, run once, after editing. When there was none,
                this project's own narrowest build/test command
                (e.g. ` + "`go build ./internal/foo/...`" + `, ` + "`pytest tests/test_foo.py -k name`" + `, ` + "`cargo check -p crate`" + `)
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
