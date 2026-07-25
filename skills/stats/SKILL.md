---
name: stats
description: >
  Report how much this repo's coding-agent sessions used the entire-graph code graph
  versus grep/read exploration, and the estimated tokens that saved. Use when the user
  asks "how much did the graph save", "token savings", "graph usage", "am I actually
  using the graph", or invokes /entire-graph:stats. Read-only, local, no network.
---

# entire graph stats

Run the report, then read it back in plain language. Do not paste the whole output —
report the numbers that matter and what they mean.

## Run it

```sh
entire graph stats --repo .            # last 30 days (default)
entire graph stats --repo . --since all
entire graph stats --repo . --format json   # for scripting
```

`--since 7d|30d|all` windows the sessions. `--sessions-dir <path>` overrides transcript
discovery. Requires the `entire-graph` plugin on PATH (`entire graph version`).

## What it reads

Coding-agent session transcripts already on disk: `~/.claude/projects/<repo-path-slug>/**/*.jsonl`
(the slug is the repo's absolute path with non-alphanumerics folded to `-`). Nothing is
uploaded, nothing is written, no model is called. If there are no sessions for the repo it
says so and exits 0.

## What it counts

- **graph calls** — `entire graph <verb>` invocations, split per verb. A call only counts
  when `entire graph` is in *command position* (a real invocation), not when the string
  appears as an argument — `find ~/src/entire-graph -name '*.go'` is not a graph call.
- **exploration calls** — what the graph is meant to replace: `Read` (split whole-file vs
  line-range), `Grep`, `Glob`, and shell `grep`/`find`/`cat`/`head`/`tail`.
- **graph-first rate** — share of sessions whose *first* locate-type call was a graph call.
  Sessions that never needed to locate anything are excluded from the denominator.
- **session tokens** — billed totals read from the transcript's own usage records
  (input + cache-creation + cache-read + output).

## How the savings estimate is computed (say this out loud when reporting)

It is an **estimate with a stated assumption, not a measurement**:

1. Only **locate** calls are credited — `search`, `neighbors`, `impact`. Bulk verbs
   (`symbols`, `edges`, `snapshot`, `diff`, …) appear in the table but are never credited.
2. For each credited call, the counterfactual is **the one whole-file read it replaced**:
   the on-disk size of the top-hit file that call pointed at (repo median tracked-file size
   when the path can't be resolved).
3. **credit = counterfactual − bytes the call actually returned, floored at 0.**
   A search that returned more bytes than the file itself earns zero credit.
4. Bytes → tokens at 4 bytes ≈ 1 token.

Two consequences worth stating to the user:

- It is **conservative**. Real baseline behavior is grep + several reads over many more
  turns (measured on SWE-bench: no-tool agents take ~2× the turns), and every extra turn
  re-reads the whole context. Crediting a single file read understates the true saving.
- It goes to **zero when the graph isn't used**, and it can legitimately report ~0 even in
  a repo full of activity — that is the report working, not a bug.

## Reporting rules

- Lead with the ratio: graph calls vs exploration calls, and the graph-first rate. That is
  the behavioral signal, and it is measured, not modelled.
- Give the estimate with its label: "estimated, assumes each locate call replaced one
  whole-file read; conservative".
- If graph calls are 0 while exploration is high, say so plainly and point at the fix:
  `entire graph init-agents` installs the search-first doctrine into `AGENTS.md`/`CLAUDE.md`
  so agents in this repo actually use it.
- Never present the estimate as billed savings; the billed number in the same report is the
  session total, not a saving.
