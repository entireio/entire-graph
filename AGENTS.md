# AGENTS.md — operating guide for coding agents

Hand this to any coding agent working in a repo where the `entire graph` plugin is installed. It is the difference between the graph saving tokens and not: it moves you from grep/read exploration to graph queries, which is where most of a session's token budget goes.

## What this gives you

A precomputed, **deterministic** code graph is available through the `entire graph` command — functions, classes, methods, types, routes, and the calls/inheritance/field/service relations between them, parsed with tree-sitter, 100% locally (no network, no model, no keys). Use it to **LOCATE** and **UNDERSTAND** code *before* any grep / find / cat / whole-file read. Every command is no-egress and safe to run inside a sandboxed session. The same commit always yields the same graph, so once the graph shows you *where* code is, you can trust that and act — no need to re-confirm the location with a second tool. (That is a licence to skip re-grepping, not to skip checking that the edit you then write actually builds — see the doctrine below.)

Default flags to remember: pass `--repo .` when you're not inside an Entire session; the graph reads your **working tree by default** (your uncommitted edits are visible), and `--head` switches to committed-tree semantics with a cached, reusable index.

---

## The parts of the graph

Reach for the smallest tool that answers your question.

### 🔍 search — *find the code for a task* (your first move)
Ranked source regions for a plain-language description, with the source and `file:line` inline. Hybrid ranking over bodies, identifiers (camelCase/snake_case aware), signatures, paths, and graph neighbors. Output is budgeted (24 KiB by default) to drop straight into context.

```sh
entire graph search --repo . --query "<the task or bug in one plain sentence>" --format text --top-k 8
```

- `--format agent` for compact ranked output with latency telemetry; `json`/`ndjson` for the full schema (completeness, partial failures, diagnostics).
- `--top-k N` result count; `--max-context-bytes N` byte budget (`0` = unbounded, default 24576 — see "the budget is sized in turns" below).
- Working tree by default; add `--head` for committed-tree + cache reuse.
- `--profile syntax-only|fast|full` (default `syntax-only`); `--index-all-files` or `--max-indexed-files N` to widen/bound cold-search parsing.

**Ranking priors you should expect (they are deliberate, not bugs):**

- **Source outranks non-source.** Prose documentation (`.md`/`.mdx`/`.rst`/`.adoc`/`.txt`, `docs/`, `website/`, `versioned_docs/`, README/CHANGELOG), vendored trees (`vendor/`, `node_modules/`, `third_party/`), generated artifacts (`dist/`, `single_include/`, lock files), serialized data and configuration (`.json`/`.yaml`/`.toml`/`.xml`/`.ini` — package manifests, command schemas, option tables) and `examples/` carry a **multiplicative** relevance prior below 1, so they must be clearly more relevant than the best source hit to outrank it. Nothing is filtered: a documentation hit still ranks first when it is the only match, and the prior switches off entirely when your query asks for that class ("update the **docs** for…", "fix the **example**", "regenerate the **dist** bundle", "the **yaml** **config** parses the wrong timeout"). Demoted hits are labelled with a `doc-prior` / `vendored-prior` / `generated-prior` / `data-prior` / `example-prior` signal.
- **Intent is read from the words you wrote.** The switch-off above triggers on words, not on fragments of identifiers you happen to quote: mentioning `NamedByteArrayTest.java` does not turn off the test-file demotion, and "a **regression** in routing" is a report about behaviour, not a request for the regression suite. Write "add a **test**", "fix the **docs**" when you do mean the artifact.
- **Near-duplicate copies are collapsed.** Two hits that are the same content in different files — versioned documentation trees, vendored snapshots, generated mirrors — are merged into the best-ranked copy, which then reports a `+N similar` signal. The freed result slots go to genuinely different code.
- **A hit is named after the smallest thing that contains it.** A matching region deep inside a 3,000-line class is attributed to the method it actually lies in, not to the class — so the class name cannot lend its score to every region in the file, and `symbol_name` describes what you are looking at.

**Snippets are allocated by rank, not spread evenly.** The **top 5 hits** come back as the
**complete body of their enclosing function/method** — snapped to the graph's own symbol
bounds, marked with the `complete-symbol` signal, and counted in
`stats.complete_symbol_snippets`. Those results need no follow-up read: `snippet_start_line` ..
`snippet_end_line` is the whole callable, verbatim. **Edit straight from the search output.**
To pay for that, results below the head are reduced to a two-line **locator** window (counted
in `stats.locator_snippets`) — still exact `file:line` + symbol identity, just not reading
material. Symbols too large to return whole (>160 lines) keep their focused window.

**Results are grouped, and the groups answer different questions.** Every hit stays in
`results` with its rank; a `section` field says how to read it, and the text renderer prints
each group under its own header.

- **(no `section`) — candidate fix sites.** The ranked answer to "where is it?".
- **`section: "related"` — RELATED SITES.** Not a second ranking: the other places the change
  usually has to land, one graph hop from the head of the ranking. Each entry is a one-line
  locator (`file:line`, symbol, and a `related:<kind>` signal saying why): **near-dupe** — a
  near-duplicate body, which needs the *identical* edit; **sibling** — the same member on a
  sibling implementation, or a member declared beside the anchor in a small unit; **caller** —
  an incoming call, reported at the **call site**, which needs adjusting to a changed contract.
  Check the block before you finish: a patch applied to one site of a family is the commonest
  way a correct fix still fails review. The block is funded out of the tail of the ranking, so
  it costs no extra bytes and never displaces the head or the only mention of a file; its size
  is in `stats.related_sites`.
- **`section: "docs-and-fixtures"`.** Hits that matched your words but hold no program text
  (prose, HTML templates, changelogs, serialized config, recorded fixtures). They are never
  suppressed, dropped or re-ranked — a fixture or a rule document is sometimes exactly the file
  that has to change — but they are not presented as fix sites, so do not spend a read there
  looking for the bug. When a payload has *nothing but* non-code hits, they stay the primary
  list: they are the answer.

**The budget is sized in turns, not in bytes.** A search payload is ~0.6% of what a session
spends; one extra agent turn is ~42.5k tokens, because 95.9% of billed tokens are context
re-read. A search that stops one Read short of an edit therefore costs about 40x the whole
payload that caused it. The 24 KiB default exists so the ceiling is never the reason a head
result comes back as half a function — it is a ceiling, not a target: payloads only grow to
buy complete head bodies, and the allocator always picks the cheapest plan that delivers them
(measured across 14 repos: mean payload 11.8 KiB, half the ceiling). Pass
`--max-context-bytes` to tighten it; bodies then degrade to focused windows, shallowest ranks
kept last.

**When:** the start of essentially every task. One good query lands you on the fix area.

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

## The agent prompt (copy-paste this)

Give this to any coding agent that has `entire graph` available — substitute your search
invocation for `<search-cmd>`:

```text
A precomputed code-search tool is available: <search-cmd> . Use it to LOCATE the fix BEFORE any
grep/find. Your FIRST action must be ONE search:
  <search-cmd> "<the bug in one sentence>"   <-- ranked relevant code (file:line + source)
The top hits come back as COMPLETE function bodies — edit straight from the search output. Only
when the hit you want is a two-line locator, open its file with your native Read tool at the
reported line range. The search top hit is the fix site on most tasks — go straight there and
edit; do NOT re-search or grep to 'confirm'. Reach the edit in as FEW turns as possible (every
turn re-reads your whole context — that is the token cost). Hard rules:
(1) SEARCH FIRST. (2) After search, edit from the returned body, or READ one line range and edit
— do not chain more searches. (3) NEVER read a whole file to explore; pass a line range.
(4) NEVER search outside this repo. (5) A fix is often not one edit in one place: before you
finish, check the sibling / duplicate / caller sites that need the same change — 'impact --symbol
X' returns callers, type consumers, co-change files and siblings in one shot. (6) VERIFY ONCE,
DON'T CHASE: after editing, compile the package you touched or run the nearest existing test.
This is not optional overhead — an edit that does not build fails 100% of the task, which costs
far more than the one turn the check costs. Read the error, fix exactly what it names, re-run;
a couple of iterations, not more. Do NOT loop edit->test->edit chasing a green suite, and do not
'fix' failures that were already failing before you started. Then stop.
```

**What was measured, and the caveat.** The token figure comes from the **official SWE-bench
Multilingual benchmark** (300 instances, 9 languages, Haiku): **54.9% weighted token savings vs a
no-tool agent** — double codebase-memory-mcp's 27.4% — winning 8/9 languages and 216/293
instances. Replicated on Sonnet (3×: 57.7% vs 36.6%) and on open-source models (31–73% via the Pi
agent). **Caveat:** that 54.9% was measured with the frugality clamp active (the earlier prompt's
"one search, minimal edit, STOP" plus "do not run builds or test suites") and against a baseline
that received *no working-policy instructions at all*; the same configuration resolved **131/300
(43.7%)** against the baseline's **150/300 (50.0%)** on those same 300 instances — 127 vs 146 on
the 273 both agents submitted a patch for, McNemar p=0.013.

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
text output (tiered: full snippet for the top hits, terse locators after). Measured detail that
matters: chaining `search -> def -> callers` to "explore the tool" was the #1 measured token
waste — prefer the search-only fast path above, then verify. (Full methodology, prompts, fairness
controls and caveats: the graphmark repo, `agentic-swebench/REPRODUCE.md` +
`BEAT-CMM-VERDICT.md`.)

## Operating doctrine (the token-saving rules)

1. **Search first — always.** Your first move on any task is one `entire graph search --query "<task>"`. Do **not** grep / find / cat to locate code before you have searched. Exploration is where ~90% of a session's tokens are wasted.
2. **Then narrow, only as needed.** Search exposes concrete identifiers → use at most one `impact --symbol X` (blast radius) or read the returned line ranges. Don't fan out.
3. **Trust the graph.** Once search or neighbors shows you the function and its source, **edit it**. Do not re-read the whole file or re-grep to "confirm" what the graph already showed — the graph is deterministic.
4. **Never read a whole file to explore.** If you must read, read the line range around the symbol. To understand a type/class, query it — don't open its file.
5. **Impact = one targeted query.** For "what breaks if I change X", use `neighbors --symbol X --relation CALLS --direction in` — not a whole-graph `snapshot`/`edges` dump, and not a repo-wide grep.
6. **Minimise turns — in discovery, not in verification.** Token cost is roughly turns × context, so prefer one precise query over three broad ones and stop *discovery* once you can defend the edit with a focused hypothesis. Turn economy applies to finding code; it is not a licence to skip the check that your edit builds.
7. **Complete the fix.** A fix is often not one edit in one place. Before finishing, run one `impact --symbol X` and apply the same change to the sibling / duplicate / caller sites that carry the same defect. Measured: single-edit patches were 22 of 31 paired losses (baseline 8/31).
8. **Verify once — always.** After editing, compile what you touched or run the nearest existing test, at the narrowest scope that would still catch a syntax, type, name, or arity error. Measured: the clamped agent ran zero builds/tests on 22 of 31 paired losses, two of which could not compile. One verification turn is far cheaper than a wrong patch.
9. **Verify, don't chase.** Verification is bounded: run it, read the error, fix exactly what the error names, re-run — a couple of iterations, not fifty. Do not enter an edit→test→edit loop hunting a green suite, and do not "fix" failures that predate your change.
10. **Feature-detect before you trust.** If a language might be inventory-only, check `capabilities --json` first — inventory-only files have file records but no semantic relations.

Quick mental model:

```text
locate  →  entire graph search --query "..."          (ranked code + file:line)
impact  →  entire graph impact --symbol X              (one-shot blast radius: callers, types, data flow, co-change)
callers →  entire graph neighbors --symbol X ...       (targeted callers/callees of X)
change  →  entire graph diff --base A --head B          (entity-level, with dependents)
ingest  →  entire graph snapshot --format ndjson        (whole graph)
report  →  entire graph stats --repo .                  (human-facing: graph vs grep/read usage + estimated token savings)
verify  →  the project's own narrowest build/test cmd    (not a graph command — run it once, after editing)
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
