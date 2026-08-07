# AGENTS.md — operating guide for coding agents

Hand this to any coding agent working in a repo where the `entire graph` plugin
is installed. It moves initial code-location work from broad grep/read
exploration to targeted graph queries; token impact depends on the task and
model, and no end-to-end savings claim is current.

## What this gives you

A precomputed, **deterministic** code graph is available through the `entire graph` command — functions, classes, methods, types, routes, and the calls/inheritance/field/service relations between them, parsed with tree-sitter, 100% locally (no network, no model, no keys). Use it to **LOCATE** and **UNDERSTAND** code *before* broad grep / find / cat / whole-file exploration. Every command is no-egress and safe to run inside a sandboxed session. The same commit yields the same graph, but static relations can be heuristic or incomplete; inspect focused source and verify the resulting change.

Default flags to remember: pass `--repo .` when you're not inside an Entire session; the graph reads your **working tree by default** (your uncommitted edits are visible), and `--head` switches to committed-tree semantics with a cached, reusable index.

---

## The parts of the graph

Reach for the smallest tool that answers your question.

### 🔍 search — *find the code for a task* (your first move)
Ranked source regions for a plain-language description, with the source and `file:line` inline. Hybrid ranking over bodies, identifiers (camelCase/snake_case aware), signatures, paths, and graph neighbors. Output is budgeted (16 KiB by default) to drop straight into context.

```sh
entire graph search --repo . --query "<the task or bug in one plain sentence>" --format text --top-k 8
```

- `--format agent` for compact ranked output with latency telemetry; `json`/`ndjson` for the full schema (completeness, partial failures, diagnostics).
- `--top-k N` result count; `--max-context-bytes N` byte budget (`0` = unbounded).
- Working tree by default; add `--head` for committed-tree + cache reuse.
- `--profile syntax-only|fast|full` (default `fast`); `--index-all-files` or `--max-indexed-files N` to widen/bound cold-search parsing.

**When:** the start of essentially every task. One good query lands you on the fix area.

### 🧾 def — *what is this name, and what can I do with it* (structural declaration lookup)
One declaration, with everything the graph structurally attaches to it. For a **type**: its fields and the SIGNATURES of its associated functions and methods — including the ones written outside the type declaration, which is where most languages actually put a type's API. For a **method**: its owning type, so `deletion` reports as `Edit::deletion`. For a **trait/interface**: who implements it.

```sh
entire graph def --repo . Edit          # fields + impl surface of a type
entire graph def --repo . Edit::deletion  # a member, with its owning type
entire graph def --repo . Ranged       # a trait, with its implementors
```

- Membership is **structural, never a name match**. A member belongs to a type because a `CONTAINS` edge or a `container_id` says so, or because the type acquires it from a supertype one hop up. Asking for `Edit` can never return members of `Fix` because `edits` happens to contain `edit`.
- The member set is joined across every place a language writes it: Rust inherent `impl` and `impl Trait for Type` blocks, Go receiver methods, Swift extensions, C# extension methods and further `partial` declarations, Kotlin extension functions/properties, PHP `use SomeTrait`, Ruby `include SomeModule` and `def self.name`. Acquired members are labelled `[via Super]`, extension members `[ext]`, and a member that satisfies a trait/interface declaration `[impl Trait]`.
- Inheritance is followed **one hop only** and never transitively: the point is this type's surface, and a transitive walk in a deep hierarchy is unbounded.
- Ambiguous names list each declaration separately (bounded, with a count). Partial declarations of ONE type are merged; two unrelated types that share a name are not.
- `--members N` caps each member list (default 15, truncated lists report the omitted count); `--max-context-bytes N` is a ceiling that shrinks member lists before dropping the identity line; `--file`/`--line`/`--kind` select one declaration; `--format text|json`.

**When:** you have a type or member name and need its API before writing the patch — instead of opening its file. Not a routine follow-up to every search: search's own `TYPES IN THIS SIGNATURE` block already carries the surface of the types the top hit's signature names.

### 🧯 explain — *my build just failed; what are these names?* (pipe, don't grep)
Reads a failing build or test run on **stdin** and prints the declaration of every symbol the error
names — `file:line`, kind, owning type, signature — resolved against your **working tree**, so it
reflects the edit you just made. A name the repository does not define is reported as such, which is
exactly the answer when you have invented a method or misspelled one.

```sh
<the VERIFY command> 2>&1 | entire graph explain --repo .
```

Run it **as one command with the build**, not as a follow-up. That is the whole design: it is not a
tool you decide to reach for after reading an error, it is part of the command that produces the error.

- Recognises the "cannot resolve this name" shape in Go, Rust, Java/Kotlin, TypeScript/JavaScript,
  Python, PHP and Ruby — `undefined:`, `has no field or method`, `cannot find function`, `no method
  named`, `cannot find symbol`, `Property 'x' does not exist`, `is not defined`, `has no attribute`,
  `Call to undefined method`, `undefined method`, plus arity complaints that still name the callee.
- Bounded: `--max-symbols` (default 8 — a cascading failure names the cause first and consequences
  after) and `--max-context-bytes` (default 2048). `--head` switches to committed-tree semantics,
  which is almost never what you want here.
- Silent when the output names nothing it can resolve. A header over an empty list would assert that
  the build named symbols when it did not.

**Why it exists.** Measured on a 30-instance paired run against a no-tool baseline, the graph had
already won the locate phase and was doing nothing for the rest:

| turns/session | baseline | graph arm |
|---|---|---|
| pre-edit exploration | 9.43 | **1.34** |
| post-edit exploration | 8.60 | 6.76 |
| post-edit file reads | 3.57 | **4.07** |

Half the assisted session is the edit→build→fix loop, and `def` — which already had the answer — was
used **0.04 times per session across 28 sessions** when the prompt offered it for exactly this case.
An agent whose build just failed is already in the shell; it does not stop to choose a tool. So the
fix is not another instruction, it is a command that composes with the build.

**When:** every failing verify run. Never grep a compiler error.

### 🕸️ neighbors — *who calls this / what does it call* (targeted relations)
Direct incoming/outgoing relations for **one** symbol, with definition locations, plus bounded two-hop paths at `--depth 2`. For the full blast radius of a change, prefer `impact`; use `neighbors` when you want one specific relation/direction. Never `edges` for this (full stream).

```sh
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction in   # who calls NAME
entire graph neighbors --repo . --symbol NAME --relation CALLS --direction out  # what NAME calls
```

- `--file path` — **required** when the symbol name is ambiguous (multiple defs); the graph returns definitions only until you disambiguate.
- `--relation CALLS` (default is the call family) — pick another relation to follow it instead.
- `--direction both|in|out`, `--depth 1|2`, `--limit N`.
- `--internal-only` drops unresolved external endpoints; `--exclude-tests` drops test-only neighbors.
- `--format agent|text|json`; `--head` for cached committed-tree; `--profile fast` for shallow call resolution (default `full` favors correctness).

**When:** "what breaks if I change X", "who uses this", tracing a call chain — after search has given you a concrete symbol name.

### 💥 impact — *one-shot blast radius for a change*
Everything the graph knows about changing **one** symbol, in a single bounded explanation: direct + transitive callers (depth ≤ 2), callees, type consumers (`USES_TYPE`/`PARAM_TYPE`/`RETURNS_TYPE`), data flows, files that historically change together with the symbol's file, and same-container siblings. Text output is sectioned, `file:line` per entry, capped per section and ~4 KB total.

```sh
entire graph impact --repo . --symbol NAME [--file path] [--depth 1|2] [--format text|json]
```

- Ambiguous names return the definition list — rerun with `--file` to pick one.
- `--limit N` per-section entry cap; `--max-context-bytes N` total text budget; `--exclude-tests`; `--head` / `--profile` as in `neighbors`.

**When:** before changing behavior of a specific function/type — "you're changing ordering: here is every place results are ordered, limited, or consumed downstream" — one command instead of chaining neighbors + edges + git log.

### 📇 symbols — *definitions*
Full stream of symbol records (stable `compound-v1` ID, kind, qualified name, source range, signature, language, container). This is a **bulk NDJSON stream of the whole repo**, filtered to the symbol record type — there is **no positional name argument** and no server-side name filter; grep the stream client-side, or prefer `search`/`neighbors` for a targeted single-symbol lookup.

```sh
entire graph symbols --repo . --format ndjson [--worktree]
```

**When:** you need the complete definition inventory (e.g. ingesting into a store), not a single lookup.

### 🔗 edges — *relations*
Full stream of relation records across all 30 types (`CALLS`, `IMPORTS`, `EXTENDS`, `HANDLES_ROUTE`, …), each tagged with resolution and confidence. Like `symbols`, this is the **whole-repo stream** — there is **no `--to`/`--from`/`--relation` filter**; for one symbol's callers/callees use `neighbors`, not `edges`.

```sh
entire graph edges --repo . --format ndjson [--worktree]
```

**When:** you want every relation (bulk export / ingestion). For a targeted question, use `neighbors`.

### 🗺️ snapshot — *the whole graph*
One header record, then file, external-endpoint, symbol, and relation records, streamed so memory stays bounded. Superset of `symbols` + `edges` + files.

```sh
entire graph snapshot --repo . --format ndjson [--worktree]
```

**When:** ingesting the full graph into agent memory or a store such as Entire Brain.

### 🧬 diff / analyze / commit / checkpoint — *what changed + risk*
Entity-level change list (added / removed / renamed / signature-changed / body-changed) with a heuristic **dependent count**, so a signature change with many dependents stands out.

```sh
entire graph commit HEAD --json                     # a commit vs its first parent
entire graph diff --base main --head HEAD --json    # between two refs (analyze is an alias)
entire graph checkpoint <id> --json                 # the commit behind an Entire-Checkpoint trailer
```

**When:** judging whether a change is safe to keep / revert / continue, or reviewing a branch/PR. High dependent counts on a signature change = run tests first.

### 🏗️ index — *build / warm the cache*
Prebuilds the durable, query-independent committed-tree index and verifies it was written, before latency-sensitive work.

```sh
entire graph index --repo . --head --profile full --cache-dir /path/to/cache --format json
```

**When:** once, up front, on a large repo before a batch of `--head` searches/neighbors queries. Re-running it is also how you "refresh" a committed-tree cache — same tree hits, changed tree rebuilds.

### 🧭 capabilities / doctor / version — *feature-detect*
```sh
entire graph capabilities --json    # semantic vs inventory-only languages, relation types, features
entire graph doctor --json          # environment, repo resolution, no_egress=true
entire graph version [--json]       # provider name + plugin version
```

**When:** before assuming a language is semantically parsed, or to confirm the no-egress environment.

### 📊 stats — *did the graph actually save anything?* (for humans, not for you)
```sh
entire graph stats --repo . [--since 30d|7d|all] [--format text|json] [--sessions-dir path|--transcript path]
```

Local, read-only report over the coding-agent session transcripts already on disk
(`~/.claude/projects/<path-slug>/*.jsonl`; `--sessions-dir` overrides the lookup). Reports graph
calls per verb vs. exploration calls (`Read` whole-file / `Read` line-range / `Grep` / `Glob` /
shell `grep|find|cat|head|tail|sed|awk`), the bytes each path pulled into context, billed session
tokens read from transcript `usage`, a graph-first rate (share of sessions whose first locate-ish
tool call was a graph call), and an **estimated** token saving. The savings model is an explicit
assumption printed next to the number: each `search`/`neighbors`/`impact` call is credited with the
one whole-file read it replaced — on-disk size of the top-hit file it pointed at (repo median
tracked-file size when unresolvable) minus the bytes that call returned, floored at 0, at 4 bytes =
1 token. It is not a measured counterfactual. No network, no writes. `--transcript <path>` narrows
the whole report to one session (that transcript plus its `<session>/subagents/*.jsonl`), which is
what `scripts/entire-graph-statusline.sh` renders as a live Claude Code status line badge.

**When:** a human asks what the graph is buying them. Agents should not run it as part of a coding task.

---

## Resolution-first agent prompt (copy-paste this)

Give this to any coding agent that has `entire graph` available — substitute your search
invocation for `<search-cmd>`, the VERIFY line the payload printed for `<verify-cmd>`, and your
explain invocation for `<explain-cmd>`:

This is the wording the harness measures (`agentic-swebench/tools/run_3arm.sh`,
`ops_clamp_eg_prompt`, `PROMPT_FAMILY=briefed`). Keep the two in step — a prompt that promises
blocks the tool no longer returns is worse than no prompt.

```text
A precomputed code graph is available: <search-cmd>
(1) FIRST ACTION: one search.
      <search-cmd> "<the bug in one sentence>"
(2) EDIT from the body it printed. Do not re-read that file. Do not search again to confirm.
(3) SAME-CONCEPT LITERAL block is your repo-wide sweep. Fix its EDIT sites. Ignore its CONSUMER
    sites. Do not grep for either.
(4) CLOSED-SET WARNING block: add the missing switch arm before you finish.
(5) VERIFY block is the command for the file you changed. Run it once when your edits are in. Read
    the error. Fix exactly what it names. Re-run at most once. Never hunt a green suite. Never write
    a throwaway test script. A patch that does not build fails the whole task.
(6) Build failed on a name you did not write? Do not grep it. Do not open its file. Run:
      <verify-cmd> 2>&1 | <explain-cmd>
(7) Reads and edits the search already located do not depend on each other. Ask for them in one
    message.
(8) Top hit clearly wrong (LOW CONFIDENCE, or a body that does not match the issue)? Search once
    more with different words. If that misses, fall back to the shell in one batched call.
```

**Why this is written as bare imperatives, and why that is not a cosmetic choice.** An earlier version
of this block said the same things in explanatory prose — 446 words, 34 sentences, 24% of them opening
with a directive. Measured against a no-tool baseline whose own block was 310 words / 21 sentences /
38% imperative, on 29 paired haiku sessions:

| assistant messages per session | baseline | graph arm |
|---|---|---|
| tool calls | 39.31 | 31.03 |
| thinking-only | 39.93 | 31.72 |
| **text-only, no tool call** | **11.93** | **21.79** |
| text-only per tool call | **0.30** | **0.70** |

The graph arm cut tool calls by 19% and then gave the saving back as narration: 9.86 extra prose
messages per session, each replaying the whole context (~41k tokens) — roughly 404k tokens, larger
than the entire measured saving. The effect is uniform after every tool (24-27% following Bash, Read,
Edit and search alike), so it is not the payload: a variant that halved payload bytes via `--top-k 5`
still narrated at 0.74. It is register. A weak model mirrors the voice it is given, and prose invites
prose. Sonnet (0.24 vs 0.23), opus (0.28 vs 0.21) and fable (0.28 vs 0.21) barely move, so this is a
small-model failure mode specifically.

Rule parity with the prose version is exact — nothing was added. In particular no "do not narrate"
clause appears here: that instruction already lives in the tool-agnostic block both arms receive, and
adding it only to this one would be the asymmetric-discipline defect that withdrew the 31.6% figure.



**Why the prompt now permits `def` after the edit.** The earlier wording ended "Then STOP. No further
searching", which suppressed every graph call once the first edit landed — and could not suppress the
shell. Measured on a 30-instance language-stratified haiku run, paired against the no-tool baseline on
the same instances:

| turns/session | baseline | graph arm |
|---|---|---|
| exploration BEFORE the first edit | 7.87 | **2.67** |
| search calls (all of them pre-edit) | 0 | 3.27 |
| exploration AFTER the first edit | 8.93 | **8.57** |
| graph calls after the first edit | — | **0.00** |

The locate half is close to saturated: 7.87 shell turns become 2.67. The other half is untouched —
8.57 turns of `grep`/`cat` after the first edit (4.40 symbol greps, 3.63 file reads), hunting
identifiers in the file the agent had just edited, while the tool that answers exactly that question
in one call went unused because this prompt forbade it. That is now the single largest remaining cost
in a graph-assisted session, which is why the rule above redirects it to `def` rather than banning it.

**What was measured. The 54.9% figure is WITHDRAWN.** It was measured with a frugality clamp on the
graph arm against a baseline that received *no working-policy instructions at all*, and the same
configuration resolved **131/300 (43.7%)** against the baseline's **150/300 (50.0%)** (McNemar
p=0.013) — it lost on accuracy while the headline claimed parity. An adversarial audit of the harness
then found eight further defects, every one of which distorted the comparison rather than the tool:
the cmm arm was missing three discipline clauses the other arms had, its source snippets were clamped
to 6 lines while ours returned whole function bodies (19.4 lines mean), a concurrency fix had been
applied to our arm only, tokens were summed from a lossy accumulator that undercounted cmm ~25%, and
a grading race submitted 7 real graph-arm patches as empty and 0 baseline patches.

**The 31.6% figure is ALSO withdrawn** — it was measured with a 119-word operating-rules paragraph
in the graph arm's prompt that the comparison arm never received, *on top of* a frugality block all
three arms already shared. Removing that redundant restatement moved the same cell from −18.9% to
+26.9%: a ~45-point swing from prompt text. The advantage was our prompt, not our index.

Current measurement — 48 language-stratified instances × **3 replicate runs** (144 matched
instance-runs), matched prompt discipline in every arm, billing-truth tokens, Haiku:
**−17.4% total tokens** (CI [−27.8, −6.5]), **−32.0% geomean** (CI [−41.5, −21.2]), **−11.8% USD**,
resolving **54 vs the baseline's 57** of 144 (McNemar p=0.42 — a tie), so **cost per resolved issue
−6.9%** ($0.539 vs $0.579). Against codebase-memory-mcp: **−22.8%** total, **−27.1%** geomean.
cmm does not measurably beat no-tool either (+7.0% total, CI crosses zero).

Quote the **per-resolved** figure, not the raw token cut: a token saving that costs resolves is not
a saving.

**The most robust result is retrieval**, because no agent is in the loop: the file the gold patch
edits reaches the payload in **96.3%** of sessions (52/54) against cmm's **81.5%** (44/54), 10
exclusive wins to 2, sign test p=0.0386, at comparable bytes and with fewer search calls (81 vs
112). It is an end-to-end arm comparison, not a retriever-isolated one — the arms wrote different
queries — and part of cmm's deficit is our wrapper's own hit cap.

**Outside Haiku we cannot measure the effect at all.** Re-running a byte-identical configuration
moves total tokens by up to **±20%**; at 80% power the minimum detectable effect is **31% at n=29,
59% at n=10, 93% at n=5**. Sonnet (+0.9% geomean, CI [−33, +41]), Opus (−6.4%) and Fable (+6.7%)
are all inside that noise, so treat them as unmeasured rather than as results. Break-even on turns
is near a 20-turn baseline: the graph pays by deleting locate turns, and a capable agent on a small
task never spends them.

**A ≥35% saving is refuted, not unproven**: the favourable CI bound on total tokens is −27.8%.

**Correction (measured later, 3 runs on 50 language-stratified instances, Haiku).** The block
above says "do NOT re-search or grep to 'confirm'". That is right for the common case and wrong for
the tail, and the tail is expensive. Of 135 instance-run pairs, **8 (6%)** were sessions where the
no-tool baseline finished comfortably and the graph-assisted agent hit the 50-turn cap at **2.2x the
baseline's tokens**. Those 8 alone cost **4.8 points** of the headline token saving (-33.5% with
them, -38.3% without). Cause, from the wrapper call logs: they ran a mean of **8.4 searches** against
2.7 for normal sessions (worst case **23**), each a near-identical rephrasing — e.g. "zero padding
applied to infinity and NaN" then "zero padding NaN format spec" then "write nan padding zeros".
The agent used search as a synonym generator because nothing told it when to stop.

Hence rule **2a** in `entire graph agent-guide`: **two searches maximum, then switch tools** — grep
for a literal from the issue (error text, identifier, flag, rule or error code, a constant) and read
around the hit. Distribution over 142 sessions: 59.2% use exactly ONE search, 87.3% use <=4, and the
>4 tail averages **38.5 turns against 22.2** for the rest. Search is how you start; it is not how
you recover. A worked case: ruff `SIM201` — the gold file `flake8_simplify/rules/ast_unary_op.rs`
ranked outside the top 20 in *every* ranking configuration tried (higher top-k, a fixture-class
prior, a wider preselection pool), and a single `grep SIM201` returns it in 4 hits.

Paired analysis of the 31 losses where the baseline fixed the bug and the graph-assisted agent did
not, both having found the correct file, shows the clamp was the cause: the graph agent ran **zero
builds or tests on 22 of the 31** (baseline ran them on 26/31) and made a **single edit on 22/31**
(baseline 8/31), and two of the losing patches could not compile at all (a left-behind
`declared and not used` variable; a member access missing its required index). Rules (5) and (6)
above exist to remove that cost. The savings figure has **not** been re-measured with them — nor
with search's later change to return the **top 5 hits as complete function bodies**, which
removed the "then open the top hit's file" Read (~3.8 turns per session at ~42.5k tokens each).
The clamp prompt exactly as measured is preserved in the graphmark repo for reproduction only —
do not use it for real work.

For bug-fix/locate tasks, run search at `--profile full` (call-graph expansion active) with default
text output (tiered: full snippet for the top hits, terse locators after). Prefer
targeted follow-up queries over whole-graph dumps, but use the graph and source
checks needed to make and verify a complete fix.

## Operating doctrine

1. **Search first for location tasks.** Start with one `entire graph search --query "<task>"` before broad grep/find exploration.
2. **Treat results as evidence, not truth.** Read focused source around the result and widen the check when behavior, aliases, generated code, or dynamic dispatch could matter.
3. **Use graph follow-ups when they answer a real question.** `impact`, `callers`, and `neighbors` are appropriate for blast radius and related-site checks; avoid exploratory whole-graph dumps.
4. **Make the smallest complete change.** Check sibling sites and contracts when the task implies them.
5. **Verify before stopping.** Run the `VERIFY:` command when search prints one; it may be a whole-suite fallback when no narrow command exists. Never report that tests pass without having run them. If execution is unavailable, perform a bounded source-level verification and disclose the limitation.
6. **Optimize context after correctness.** Prefer precise queries and line ranges, but never trade resolution for fewer turns.
7. **Feature-detect before you trust.** If a language might be inventory-only, check `capabilities --json` first — inventory-only files have file records but no semantic relations.

Quick mental model:

```text
locate  →  entire graph search --query "..."          (ranked code + file:line)
impact  →  entire graph impact --symbol X              (one-shot blast radius: callers, types, data flow, co-change)
callers →  entire graph neighbors --symbol X ...       (targeted callers/callees of X)
change  →  entire graph diff --base A --head B          (entity-level, with dependents)
ingest  →  entire graph snapshot --format ndjson        (whole graph)
report  →  entire graph stats --repo .                  (human-facing: graph vs grep/read usage + estimated token savings)
verify  →  the VERIFY line search printed, run once      (else the project's own narrowest build/test cmd)
explain →  <verify cmd> 2>&1 | entire graph explain     (declarations for every name the FAILURE printed, from
           the working tree — run it WITH the build; never grep a compiler error)
extras  →  entire graph search ... --reference-blocks all (container map, signature types, declaration card — off by default)
```

---

## Working on entire-graph itself

If your task is modifying this repository (not just using it), the build/test surface is in `mise.toml`:

```sh
mise run build   # go build -o entire-graph ./cmd/entire-graph  (needs CGO for tree-sitter)
mise run test    # go test ./...
mise run check   # fmt + vet + race tests + build
```

Contract rules that must not break: schema `1.x` is frozen and additive-only (`docs/adr/0001-ga-schema-contract.md`); the provider is **no-egress** (never add remote fetches, hosted API calls, telemetry, or runtime grammar downloads); `compound-v1` symbol IDs must stay stable across ordinary edits; unsupported/unparseable files must surface as machine-readable partial failures, never silent drops. All logic lives under `internal/` (`sem` = parsing/graph/search, `cli` = hand-rolled dispatch, `gitutil` = git subprocess); `cmd/entire-graph/main.go` is a thin entry point. The plugin manifest (`entire-plugin.yml`) registers the subcommand `graph`, so users type `entire graph ...`. This project was **previously named `entire-sem`** — do not reintroduce the old name. **Entire Brain** (`entire-brain`) is the separate downstream consumer of this provider's NDJSON — not an old name for this project.
