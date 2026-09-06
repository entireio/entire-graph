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
report the numbers that matter and what they mean. The bare command prints only the headline
number, so use `--verbose` (or `--format json`) whenever you need the per-verb and per-kind
detail the reporting rules below ask for.

## Run it

```sh
entire graph stats --repo .            # last 30 days (default) -> ONE line: tokens saved
entire graph stats --repo . --verbose  # the full report: tables, billed tokens, model text
entire graph stats --repo . --since all
entire graph stats --repo . --format json   # for scripting; --verbose does not change it
entire graph stats --repo . --transcript ~/.claude/projects/<slug>/<session>.jsonl
```

The default output is a single line — `[entire-graph] ~128,400 tokens saved` — because that is
the question people ask. Everything else moved behind `--verbose`; `--format json` is unchanged.

`--since 7d|30d|all` windows the sessions, and it now prunes transcripts by file mtime BEFORE
parsing them, so a narrow window is fast instead of reading the whole directory. `--sessions-dir <path>` overrides transcript
discovery. `--transcript <path>` narrows the report to ONE session — that transcript plus its
`<session>/subagents/*.jsonl` — instead of a whole project directory; mutually exclusive with
`--sessions-dir`. Per-transcript summaries are memoised under the cache directory, keyed on file
identity (path + size + mtime) and on the binary's own identity; `--no-cache` re-parses
everything and `--cache-dir` relocates the memo. Requires the `entire-graph` plugin on PATH
(`entire graph version`).

## What it reads

Coding-agent session transcripts already on disk: `~/.claude/projects/<repo-path-slug>/**/*.jsonl`
(the slug is the repo's absolute path with non-alphanumerics folded to `-`). Nothing is
uploaded and no model is called; the only thing written is the summary memo under the cache
directory. If there are no sessions for the repo it says so and exits 0.

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

It is an **estimate with a stated assumption, not a measurement** — but both of its prices are
measured from the session's own transcript, so only the substitution ratio is assumed:

1. Only **locate** calls are credited — `search`, `neighbors`, `impact`. Bulk verbs
   (`symbols`, `edges`, `snapshot`, `diff`, …) appear in the table but are never credited.
2. Within each session, both per-call prices are **measured**:
   `graph bytes/call = locate result bytes ÷ locate calls`, and
   `exploration bytes/call = exploration result bytes ÷ exploration calls`.
3. **saving per substitution = exploration bytes/call − graph bytes/call, floored at 0**, and
   each credited locate call is credited with one substitution.
4. Bytes → tokens at 4 bytes ≈ 1 token (real code runs ~3.2–3.6, so this understates).

The 1:1 substitution ratio is the only assumption, and it is not invented: a paired A/B
benchmark of this tool measured **0.980 exploration calls displaced per graph call**. 1:1 is
that number rounded in the understating direction.

Two consequences worth stating to the user:

- It can legitimately report **~0**, and often does. A session whose graph calls returned more
  bytes per call than the exploration they displaced saved nothing — this repo's own paired
  benchmark found exactly that pattern, and the report must be able to say it. On the machine
  this was developed against, 4 of 10 sessions had a positive per-call advantage and 4 had a
  negative one.
- It goes to **zero when the graph isn't used**, and zero in a repo full of activity is the
  report working, not a bug.

## Reporting rules

- Lead with the ratio: graph calls vs exploration calls, and the graph-first rate. That is
  the behavioral signal, and it is measured, not modelled.
- Give the estimate with its label: "estimated; both per-call costs measured from this
  session, 1:1 substitution assumed". Never drop the `~`.
- If graph calls are 0 while exploration is high, say so plainly and point at the fix:
  `entire graph init-agents` installs the search-first, verify-once doctrine into `AGENTS.md`/`CLAUDE.md`
  so agents in this repo actually use it.
- Never present the estimate as billed savings; the billed number in the same report is the
  session total, not a saving.

## Status line

The same numbers render as a live one-line Claude Code badge:

```text
[GRAPH] ↗ 2.1M saved · 28 search · 9 impact · graph-first ✓ · 75% of locates · 12% of session
```

If the user wants it on, add to `~/.claude/settings.json` (project-local: `.claude/settings.json`):

```json
{
  "statusLine": {
    "type": "command",
    "command": "sh /path/to/entire-graph/scripts/entire-graph-statusline.sh"
  }
}
```

Under the plugin, the path is `sh "$CLAUDE_PLUGIN_ROOT/scripts/entire-graph-statusline.sh"`.
The plugin manifest declares the same block, but Claude Code drops non-allowlisted
plugin-provided settings (`statusLine` is not on the list as of 2.1.219), so the settings.json
entry is what actually enables it.

The badge is scoped to the CURRENT session via `--transcript` and is cached on the transcript's
size+mtime, so it costs one bounded transcript scan rather than a re-scan of every session in the
project. Its savings number carries exactly the caveats above — same estimator, same assumption.
