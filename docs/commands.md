# Command reference

Day to day, Entire Graph is driven by a coding agent following the guide that
`init-agents` installs. The commands below are the same surface used directly —
for manual inspection, debugging a result the agent acted on, or scripting.
`entire graph help` lists every public command for your installed version, and
`entire graph <command> --help` is authoritative for flags; this page groups
the commands by task and records the defaults that matter most, verified
against v0.3.0.

Pass `--repo .` (or a path) when running outside an Entire session.

## Set up

| Command | What it does |
| --- | --- |
| `init-agents` | Writes `.entire/graph-agent.md` and managed blocks in `AGENTS.md`/`CLAUDE.md`. See [agent activation](agents.md). |
| `agent-guide` | Prints the operating guide `init-agents` installs, for inspection or piping elsewhere. |
| `index` | Prewarms one committed-tree cache variant before a batch of `--head` queries. Defaults to `--profile full`; see [operations — cache](operations.md#cache). |
| `capabilities` | Reports semantic vs inventory-only languages, relation types, profiles, and features as JSON. Feature-detect with this before relying on a relation family. |

## Inspect

These five form the interactive query family. They read the **working tree by
default**; `--head` switches to the committed tree.

| Command | What it does |
| --- | --- |
| `search` | Ranked source regions for a plain-language query. Defaults: `--format json`, `--profile fast` (the installed agent guide asks for `--profile full`). Formats: `json`, `ndjson`, `text`, `agent`. See [search results](search.md). |
| `def` | One name's declaration, fields, and method surface. Default format is text. |
| `explain` | Resolves symbols named by a failing build or test into definitions and context. |
| `neighbors` | Direct relations of one symbol (`--relation`, `--direction`, `--depth 1\|2`). Ambiguous names return a definition list; disambiguate with `--file`. |
| `impact` | One-shot blast radius for a symbol: direct and transitive callers (depth ≤ 2), callees, type consumers, data flows, co-change files, siblings. |

Cache visibility differs by command and format: `search` JSON reports
`stats.index_cache_hit`; `impact` and `neighbors` print an `Index: cache-hit`
or `cache-miss` header in text and agent formats; `search --format text` and
`explain` report no cache state. `def` and `explain` also skip the per-user
fallback cache directory that the other query commands use — details in
[operations — cache](operations.md#cache).

## Analyze changes

| Command | What it does |
| --- | --- |
| `commit <ref>` | Entity-level change list for a commit vs its first parent, with heuristic dependent counts. |
| `diff --base A --head B` | The same between two refs. `analyze` is an alias of `diff`. |
| `checkpoint <id>` | Analyzes the commit behind an Entire-Checkpoint trailer. |
| `verify` | Runs a caller-provided test command and returns an adjudicated verdict. This executes the command you pass it — see [trust and security](trust-and-security.md). |

## Export

Bulk NDJSON streams. These default to the **committed tree**; pass
`--worktree` to stream the working tree instead.

| Command | What it does |
| --- | --- |
| `snapshot` | The whole graph: header, files, externals, symbols, relations, summary. Also supports `--format compact-ndjson`. |
| `symbols` | Symbol records only. No name filter — grep the stream, or use `search`/`def` for a targeted lookup. |
| `edges` | Relation records, filterable server-side with `--to`, `--from`, `--relation`. |
| `snapshot-query` | Queries a saved compact snapshot without rebuilding the graph. |

Stream and record formats are specified in
[snapshot format](snapshot-format.md).

## Report

`stats` is a human-facing, read-only report of graph vs grep/read usage in
local coding-agent transcripts, with an explicitly modeled (not measured)
token-savings estimate. It is not part of the agent workflow; see
[trust and security](trust-and-security.md) for what it reads.
