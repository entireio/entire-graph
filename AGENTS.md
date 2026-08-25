# AGENTS.md — operating guide for coding agents

Hand this to any coding agent working in a repo where the `entire graph` plugin
is installed. It moves initial code-location work from broad grep/read
exploration to targeted graph queries; token impact depends on the task and
model, and no end-to-end savings claim is current.

Two guidance surfaces coexist here on purpose. `.entire/graph-agent.md` is the
generated activation artifact — regenerated in full by `init-agents`, so never
edit it by hand — and is what agents load through the managed block at the end
of this file. This document is the maintained long-form reference; its examples
pass flags explicitly (such as `--format text`) for readability rather than
relying on command defaults.

## What this gives you

A precomputed code graph is available through the `entire graph` command — functions, classes, methods, types, routes, and the calls/inheritance/field/service relations between them, parsed with tree-sitter. Built-in analysis is local and no-egress (no network, no model, no keys). Use it to **LOCATE** and **UNDERSTAND** code *before* broad grep / find / cat / whole-file exploration. The same repository view and options yield the same graph, but static relations can be heuristic or incomplete; inspect focused source and verify the resulting change. Some commands write derivative caches or explicitly requested setup/report files; inspect command help when filesystem writes matter.

Default flags to remember: pass `--repo .` when you're not inside an Entire session. The interactive query family (`search`, `def`, `explain`, `neighbors`, and `impact`) reads your **working tree by default** so uncommitted edits are visible; `--head` switches those commands to committed-tree semantics. Other command families have different defaults.

---

## The parts of the graph

Reach for the smallest tool that answers your question.

### 🔍 search — *find the code for a task* (your first move)
Ranked source regions for a plain-language description, with the source and `file:line` inline. Hybrid ranking over bodies, identifiers (camelCase/snake_case aware), signatures, paths, and graph neighbors. Output is byte-budgeted to drop straight into context.

```sh
entire graph search --repo . --query "<the task or bug in one plain sentence>" --format text --top-k 8
```

- `--format agent` for compact ranked output with latency telemetry; `json`/`ndjson` for the full schema (completeness, partial failures, diagnostics).
- `--top-k N` result count; `--max-context-bytes N` byte budget (`0` = unbounded).
- Working tree by default (always rebuilt and never cached); add `--head` for cached committed-tree semantics.
- `--profile syntax-only|fast|full` (default `fast`); `--index-all-files` or `--max-indexed-files N` to widen/bound cold-search parsing.

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
- `--depth` 1|2 (default: 2); `--limit N` per-section entry cap; `--max-context-bytes N` total text budget; `--exclude-tests`; `--head` / `--profile` as in `neighbors`.

**When:** before changing behavior of a specific function/type — "you're changing ordering: here is every place results are ordered, limited, or consumed downstream" — one command instead of chaining neighbors + edges + git log.

### 📇 symbols — *definitions*
Full stream of symbol records (stable `compound-v1` ID, kind, qualified name, source range, signature, language, container). This is a **bulk NDJSON stream of the whole repo**, filtered to the symbol record type — there is **no positional name argument** and no server-side name filter; grep the stream client-side, or prefer `search`/`neighbors` for a targeted single-symbol lookup.

```sh
entire graph symbols --repo . --format ndjson [--worktree]
```

**When:** you need the complete definition inventory (e.g. ingesting into a store), not a single lookup.

### 🔗 edges — *relations*
Full stream of relation records across all 30 types (`CALLS`, `IMPORTS`, `EXTENDS`, `HANDLES_ROUTE`, …), each tagged with resolution and confidence. The stream is whole-repo by default; `--to`, `--from`, and `--relation` filter it server-side. For one symbol's callers/callees, prefer `neighbors` because it annotates resolved definitions and call sites from source.

```sh
entire graph edges --repo . --format ndjson [--worktree] [--to ID|NAME] [--from ID|NAME] [--relation TYPE[,TYPE...]]
```

**When:** you want every relation (bulk export / ingestion) or a filtered relation stream. For a targeted one-symbol question, use `neighbors`.

### 🗺️ snapshot — *the whole graph*
One header record, then file, external-endpoint, symbol, and relation records, streamed so memory stays bounded. Superset of `symbols` + `edges` + files.

```sh
entire graph snapshot --repo . --format ndjson [--worktree]
entire graph snapshot --repo . --format scip > index.scip 2> index.scip.omissions.json
```

The experimental `scip` format is a complete-snapshot binary projection for
standard SCIP consumers. Native NDJSON remains the lossless default; SCIP
retains one complete index in memory and reserves stderr for its JSON omission
note, so it cannot be combined with `--progress`. Its SCIP package version is the
project's own declared version from the root manifest (`0` when none declares
one), never the commit, so a symbol keeps one identity across commits; worktree
provenance is marked in the note rather than in the version.

**When:** ingesting the full graph into agent memory or a store such as Entire Brain, or exporting a navigation-oriented index to a SCIP consumer.

### 🧬 diff / analyze / commit / checkpoint — *what changed + risk*
Entity-level change list (added / removed / renamed / signature-changed / body-changed) with a heuristic **dependent count**, so a signature change with many dependents stands out.

```sh
entire graph commit HEAD --json                     # a commit vs its first parent
entire graph diff --base main --head HEAD --json    # between two refs (analyze is an alias)
entire graph checkpoint <id> --json                 # the commit behind an Entire-Checkpoint trailer
```

**When:** judging whether a change is safe to keep / revert / continue, or reviewing a branch/PR. High dependent counts on a signature change = run tests first.

### 🏗️ index — *build / warm one cache variant*
Prebuilds a durable, complete committed-tree snapshot and verifies it was
written before latency-sensitive work. Reuse is cache-variant-specific: a
later `--head` query finds the entry only when caching is enabled and it
resolves the same cache directory, profile, ordered ignore/include paths and
contents, and `.graphignore`.

```sh
entire graph index --repo . --head --profile full --cache-dir /path/to/cache --format json
```

**When:** once, up front, on a large repo before a batch of `--head`
searches/neighbors queries that use the same cache variant. `index` defaults to
`--profile full` while `search` defaults to `fast`, so the command above warms
neither a default `search --head` nor the default working-tree agent path.
Match the whole variant: unchanged keyed inputs and tree produce a hit, while
a changed tree or changed input selects or builds another entry.

### 🧭 capabilities / version — *feature-detect*
```sh
entire graph capabilities --json    # semantic vs inventory-only languages, relation types, features
entire graph version [--json]       # provider name + plugin version
```

**When:** before assuming a language is semantically parsed, or to confirm which build is installed.

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

The former “measured-best” early-stop prompt and its 54.9%/57.7% token claims
are withdrawn. It optimized token use without preserving resolution parity and
could reward a cheap wrong patch. Use this correctness-first guidance instead;
substitute your search invocation for `<search-cmd>`:

```text
A precomputed code-search tool is available: <search-cmd> . Use it to LOCATE the fix BEFORE any
grep/find. Your FIRST action must be ONE search:
  <search-cmd> "<the bug in one sentence>"   <-- ranked relevant code (file:line + source)
Then open the top hit's file with your native Read tool (pass a line range around the reported
line), inspect enough surrounding behavior to justify the change, and make the smallest complete
edit. Treat graph output as evidence, not an oracle. Check callers/impact when the change can affect
other sites. If the result prints a `VERIFY:` line, run that exact command after editing before
claiming the task is done; it may be a per-file command or a whole-suite fallback when no narrow
command can be derived. Never report that tests pass without executing the command. If verification
cannot be run, state why and perform a bounded source-level check. Optimize turns only after
correctness.
```

For bug-fix/locate tasks, run search at `--profile full` (call-graph expansion active). Search's
default output is JSON; pass `--format text` for the tiered human view (full snippet for the top
hits, terse locators after) or `--format agent` for compact output with a cache/latency header.
Prefer targeted follow-up queries over whole-graph dumps, but use the graph and source
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

<!-- entire-graph:begin -->
This repo has the entire-graph code graph installed. Before exploring code with
grep/find/whole-file reads, read .entire/graph-agent.md — resolution-first guidance
for using graph retrieval, focused source inspection, and verification.
@.entire/graph-agent.md
<!-- entire-graph:end -->
