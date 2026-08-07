# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they can start from ranked
structural evidence instead of broad grep-and-read exploration. It is 100% local:
no network, no model calls, no keys.

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

For Markdown and other prose-heavy repositories, ranked hits carry
`retrieval_mode=prose-parent`. Unless the caller explicitly sets
`--head-window-lines`, `search` widens the highest-ranked parent files to
bounded 80-line read windows. Prose windows may use the caller's remaining
`--max-context-bytes` capacity; an oversized window degrades to the largest
verbatim window that fits instead of collapsing to a one-line locator. Ordinary
code search keeps its measured growth allowance, explicit caller settings take
precedence, and search returns the ranges natively without requiring
adapter-side source expansion.

When useful prose from the same selected parent is split across distant regions,
JSON and NDJSON results also carry an additive `passages` list. Each passage is
an exact, non-overlapping source range from the result's file. Passages are
selected by marginal query-term coverage, allocated round-robin across ranked
parents, source-ordered, and charged to the same `--max-context-bytes` ceiling.
Existing clients can keep reading the primary `snippet`; text and agent formats
render the extra ranges automatically. `stats.prose_passages` and
`stats.prose_passage_bytes` make the additional context auditable.

**One search returns everything the next three turns would have cost:**

- **candidate fix sites**, the top hits as complete function bodies, plus **RELATED SITES**
  (callers, siblings, near-duplicate bodies) and the **COVERING TEST** that exercises the fix site.
- **SAME-CONCEPT LITERAL** — every place in the repository the queried concept is spelled out,
  each tagged `EDIT` (declares or registers it), `CONSUMER` (only passes it) or `DOC`, with the
  repository's own totals. That is the sweep, so there is no grep to run.
- **VERIFY** — the narrowest test command for the file being changed, derived from the repository's
  own build files (Cargo workspace member, Go module, Maven module, the `package.json` runner,
  PHPUnit, pytest, Rake/RSpec, a `Makefile` target) and the test file it targets. When the build
  files do not license a narrow command, nothing is emitted: a wrong command costs more than none.
- **CLOSED-SET WARNING** — when the hit belongs to an enum, sealed hierarchy, union type or typed
  const group, the switch/match sites that would throw at RUNTIME rather than fail to compile if a
  variant were added without an arm. It stays silent where the compiler already checks (Rust
  `match`, exhaustive Kotlin `when`, a TypeScript `never` assertion).

Three further reference blocks — a container map, the signature's types, a declaration card — are
available behind `--reference-blocks all` but **off by default**: measured across real agent
sessions they raised turns and cost without improving the result, because they answer questions the
agent was not about to ask. They remain useful when a human is reading one search.

Want the exact agent instructions? `entire graph agent-guide` prints them; they also live in
[AGENTS.md](AGENTS.md), including the copy-paste prompt block and what the benchmark did and did
not measure about it.

## Where it fits in Entire

Entire Graph is the **semantic layer** of the Entire platform — infrastructure, not another
workflow to learn:

- **Entire Search / Why / Blame** consume it to answer developer questions with entity-level
  precision.
- **Checkpoints and Trails** use its `diff`/`commit` analysis to describe what an agent actually
  changed.

You (a human) will mostly experience it *through* those surfaces. Your agents call it directly.

## Long-conversation retrieval, measured fairly

The native prose search path above was evaluated in a paired comparison with
Graphify on the same 300 LOCOMO and 50 LongMemEval-S questions, with three
repetitions, one shared Kimi K3 reader/grader, a deterministic 20% Opus 5 audit,
top ten, and a 128,000-byte context ceiling. Codebase Memory MCP is included as
an off-domain native diagnostic, not as a third apples-to-apples prose-memory
competitor.

| Benchmark | Metric | Graphify | Entire Graph | CMM diagnostic* |
|---|---|---:|---:|---:|
| LOCOMO (n=300) | recall@10 | 0.787 | **0.914** | 0.000 |
| LOCOMO (n=300) | QA accuracy | 59.3% | **77.2%** | 22.3% |
| LongMemEval-S (n=50) | QA accuracy | 68.0% | **76.0%** | 6.0% |
| Graph build | LLM credits | 0 | 0 | 0 |

\* CMM v0.9.0 indexed the Markdown conversations as `Section` nodes, but its
public natural-language `search_graph(query=...)` BM25 path excludes that node
type. Its separate `search_code` verb can find literal source phrases, but the
unchanged benchmark question is not a literal source pattern. The CMM column
therefore reports that public route on an off-domain corpus and is not a claim
about CMM's code-search quality.

All 3,150 raw cells, 3,150 blinded primary grades, and 630 fixed audit cells
passed the sealed integrity gates. The Opus audit agreed with the primary judge
on 98.41% of cells (Cohen's kappa 0.9682), and there were zero invalid attempts.
Across all 350 paired memory cases, Entire minus Graphify semantic accuracy was
+0.1648 (cluster 95% CI +0.1034 to +0.2146; McNemar p=1.70e-11).

This is a **public-protocol reimplementation**, not a reproduction of
Graphify's historical README run: Graphify's advertised memory harness and
original selectors are not public. The comparison froze Graphify public `v8`
at `9f25a3a`, used disjoint precommitted samples, ran an untouched 10% validity
holdout before the full population, kept prompts/models/limits identical, and
did not selectively rerun cells. Graphify's reader received its native BFS text;
Entire's reader received only native snippets and additive passages returned by
`search`; the neutral adapter source-validates every additive passage, validates
the primary locator, and reapplies the shared byte cap. Full protocol, bridge
details, historical references, failed-attempt lineage, and artifact hashes are
published in GraphMark's `memory-native-v52` result bundle.

QA accuracy means the share of questions answered correctly. Recall@10 means
the share for which the evidence-bearing conversation appears among the first
ten retrieved results. A score such as 0.914 is 91.4%.

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

### Compact snapshot artifact

`entire graph snapshot --repo . --format ndjson` remains the interoperable default: it is the existing object-per-line stream. For a complete, local compact artifact, use:

```sh
entire graph snapshot --repo . --format compact-ndjson > graph.compact.ndjson
entire graph snapshot-query --input graph.compact.ndjson --symbol Cache.Refresh --format ndjson
entire graph snapshot-query --input graph.compact.ndjson --from '<stable-id>' --relation CALLS --format ndjson
```

Compact NDJSON v1 is full-snapshot-only; targeted `--to`, `--from`, and `--relation` output stays native NDJSON. Its first `h` line is the only version marker, dictionary `d` lines are part of the artifact and its raw byte count, and unknown versions are rejected. The compact and native streams must have the same decoded public projection and canonical semantic SHA-256; hash equality alone is not a losslessness proof. Compact cache entries use a separate namespace from native snapshot entries.

- [AGENTS.md](AGENTS.md) — the agent operating guide (also: `entire graph agent-guide`)
- [docs/DETAILS.md](docs/DETAILS.md) — full command reference, architecture, language support,
  performance and accuracy benchmarks, security model
- `entire graph help` — command list; `entire graph doctor --json` — environment check

## License

See [LICENSE](LICENSE).
