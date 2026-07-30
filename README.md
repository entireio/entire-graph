# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they can start from ranked
structural evidence instead of broad grep-and-read exploration. It is 100% local:
no network, no model calls, no keys.

> **Evidence correction (2026-07-30):** earlier versions of this README claimed
> 54.9%/57.7% end-to-end token savings and task-completion parity. Those claims
> are withdrawn. The evaluations did not make official resolution the primary
> outcome and used asymmetric early-stop guidance; resolution grading
> contradicted the parity claim. No replacement end-to-end savings number is
> current.

## Install (one minute)

Go 1.24+ and a C compiler required first

```sh
go install github.com/entireio/entire-graph/cmd/entire-graph@main
entire plugin install "$(go env GOBIN | grep . || echo "$(go env GOPATH)/bin")/entire-graph" --force
```

Then, in any repo your agents work in:

```sh
entire graph init-agents
```

That's it. `init-agents` drops the operating guide into your project's `AGENTS.md` and `CLAUDE.md`,
so Claude Code, Codex, Gemini, Cursor, Pi — any agent that reads those files — picks up the
search-first workflow automatically. No config, no MCP server, no daemon.

## Status line: estimated exploration savings

A one-line Claude Code badge, updated as the session runs:

```text
[GRAPH] ↗ 2.1M saved · 28 search · 9 impact · graph-first ✓ · 75% of locates · 12% of session
```

- **saved** — heuristic estimated tokens, not a measured counterfactual or
  evidence of end-to-end task savings; it uses the same model as
  `entire graph stats`.
- **verb split** — top three graph verbs by call count for this session.
- **graph-first** — did the session open with a graph call rather than grep/read.
- **of locates** — share of all locate-ish calls (graph vs `Read`/`Grep`/`Glob`/shell `grep|find|cat`)
  that went to the graph.
- **of session** — the estimate as a share of billed session tokens.

Before any graph call it reads `[GRAPH] no graph calls yet · 35 explore`, and if the binary or
transcript is missing it prints nothing at all.

Enable it in `~/.claude/settings.json` (or `.claude/settings.json` for one project):

```json
{
  "statusLine": {
    "type": "command",
    "command": "sh /path/to/entire-graph/scripts/entire-graph-statusline.sh"
  }
}
```

Installed as a plugin, the path is `sh "$CLAUDE_PLUGIN_ROOT/scripts/entire-graph-statusline.sh"`.
The plugin manifest declares the same block under `settings`, but Claude Code only merges an
allowlisted subset of plugin-provided settings (`agent`, `subagentStatusLine` as of 2.1.219) and
drops `statusLine` — so today the settings.json entry above is what actually turns it on. The
manifest entry costs nothing and starts working if that allowlist widens.

Knobs: `ENTIRE_GRAPH_BIN` (explicit binary path), `NO_COLOR`,
`ENTIRE_GRAPH_STATUSLINE_CACHE=0` (disable the render cache),
`ENTIRE_GRAPH_STATUSLINE_SCOPE=project` (whole-project totals instead of this session — it
re-scans the entire transcript directory, which is far slower; the default `session` scope reads
one transcript).

## What your agents get

| agent workflow | before | with Entire Graph |
|---|---|---|
| **Locate a fix** | repeated grep/open cycles | `entire graph search` → inspect focused source → edit and verify |
| **Impact of a change** | repo-wide grep for callers | `entire graph impact --symbol X` — callers, type consumers, data flow, co-change in one shot |
| **Review a diff** | file-by-file reading | `entire graph diff` — entity-level changes with dependent counts |

The search understands natural language ("XTRIM trims wrong stream entries"), ranks real
implementation code above tests and docs, bridges vocabulary gaps through the call graph, and
returns budgeted output designed to drop straight into an agent's context.

Want the exact agent instructions? `entire graph agent-guide` prints them; they
also live in [AGENTS.md](AGENTS.md). The current guide is resolution-first: use
the graph to locate and understand the change, then verify the result.

## Where it fits in Entire

Entire Graph is the **semantic layer** of the Entire platform — infrastructure, not another
workflow to learn:

- **Entire Search / Why / Blame** consume it to answer developer questions with entity-level
  precision.
- **Checkpoints and Trails** use its `diff`/`commit` analysis to describe what an agent actually
  changed.

You (a human) will mostly experience it *through* those surfaces. Your agents call it directly.

## Evidence status

The former 54.9% Haiku, 57.7% Sonnet, and 31–73% open-model token-savings
figures are withdrawn. They were token-first measurements under a prompt policy
that encouraged the graph arms to stop early and did not require equivalent
resolution evidence from every arm. “Patch produced” was also reported as task
completion, which is not the same as passing the task's tests.

A replacement claim requires:

- identical tool-agnostic working instructions across arms;
- each tool's normal interface;
- official task resolution as the primary outcome;
- token and turn comparisons over commonly resolved tasks;
- repeated runs with uncertainty; and
- pinned binaries, prompts, datasets, and retained raw artifacts.

Until that evidence is frozen, `entire graph stats` and the status line are
local heuristic reports only, not benchmark results.

## More

- [AGENTS.md](AGENTS.md) — the agent operating guide (also: `entire graph agent-guide`)
- [docs/DETAILS.md](docs/DETAILS.md) — full command reference, architecture, language support,
  performance and accuracy benchmarks, security model
- `entire graph help` — command list; `entire graph doctor --json` — environment check

## License

See [LICENSE](LICENSE).
