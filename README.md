![entire-graph theme](docs/images/gh-repo-cover.png "entire-graph cover image")

# Entire Graph

Coding agents lose time before the edit, while they are still looking for the
right code. Entire Graph is a plugin for the Entire CLI that gives an agent a
precomputed map of one Git repository: ranked code search plus definitions,
callers, types, routes, and change impact, each with `file:line` locations. The
built-in analyzer parses the repository locally with tree-sitter and makes no
network requests, model calls, or API-key lookups. Installing the plugin is the
networked step.

Setup happens once per repository. After that, the interface is your coding
agent: you ask a code question in plain language, the agent runs graph queries,
reads the code the graph points at, and answers with citations. A captured
example is shown below.

## Benchmarks

The Entire Graph retrieval engine ranked first in an eight-system LoCoMo comparison
(1,540 questions, shared reader and judge, 200-item retrieval budget for every
arm) while building its index without model calls. Measured 2026-08-14, on the
retrieval path that first shipped in
[v0.4.0](https://github.com/entireio/entire-graph/releases/tag/v0.4.0)
([#104](https://github.com/entireio/entire-graph/pull/104))

| System | LoCoMo | Index-time tokens | Version tested |
| --- | --- | --- | --- |
| **entire-graph** | **94.74** | **0** | [v0.4.0](https://github.com/entireio/entire-graph/releases/tag/v0.4.0) ([#104](https://github.com/entireio/entire-graph/pull/104)) |
| [mem0](https://github.com/mem0ai/mem0) | 93.83 | 50.85M | commit [`4debc58`](https://github.com/mem0ai/mem0/commit/4debc58a83377b18be81ae1e5969a300736b2fac) |
| [cognee](https://github.com/topoteretes/cognee) | 92.86 | 12.35M | commit [`38eece5`](https://github.com/topoteretes/cognee/commit/38eece5bbb0cb9f5706fed908abd16dba0f5505e) |
| [bm25](https://github.com/dorianbrown/rank_bm25) (lexical baseline) | 91.88 | 0 | [0.2.2](https://github.com/dorianbrown/rank_bm25/releases/tag/0.2.2) |
| [codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) (cmm) | 91.30 | 0 | [v0.9.0](https://github.com/DeusData/codebase-memory-mcp/releases#release-v0.9.0) |
| [graphify](https://github.com/Graphify-Labs/graphify) | 87.34 | 0 | [v0.9.43](https://github.com/Graphify-Labs/graphify/releases/tag/v0.9.43) |
| [letta](https://github.com/letta-ai/letta) | 84.68 | not projectable | [0.16.8](https://github.com/letta-ai/letta/releases/tag/0.16.8) |
| [supermemory](https://github.com/supermemoryai/supermemory) | 82.08 | hosted | [server-v0.0.7-rc.2](https://github.com/supermemoryai/supermemory/releases/tag/server-v0.0.7-rc.2) |

See [benchmarks](docs/benchmarks.md) for full methodology, per-category results,
retractions, and reproduction steps.

## Install

Entire Graph requires Entire CLI 0.10.0 or later and Git 2.36 or later on
`PATH`. Git 2.36 added the single-session object protocol Entire Graph uses to
inspect an object's type before reading its contents. The commands below are
the official ones from the [Entire CLI installation
guide](https://docs.entire.io/installation), which also covers Windows and
other channels.

On macOS:

```sh
brew tap entireio/tap
brew trust entireio/tap
brew install --cask entire
```

On Linux:

```sh
curl -fsSL https://entire.io/install.sh | bash
```

Then **install the plugin** from the plugin index and confirm its version:

```sh
entire plugin install graph
entire graph version
```

`entire graph version` printing a release tag confirms that a versioned build
is active.
The local install helper reports the source checkout's Git description; a raw
unversioned build prints `dev` (see [operations](docs/operations.md)).

## Activate it for your agent

Activation is per repository:

```sh
entire graph init-agents --repo .
```

The command creates or updates these files:

- `.entire/graph-agent.md`: the agent operating guide. Generated in full and
  regenerated in full on each successful rerun; manual edits there do not
  survive.
- `AGENTS.md` and `CLAUDE.md`: created if absent; otherwise one managed block
  between `<!-- entire-graph:begin -->` and `<!-- entire-graph:end -->` markers
  is added or replaced. Text outside the markers is preserved.

Review the three files, then commit them together when the instructions should
apply to your team.

Finally, start a fresh agent session in the repository. A session that was open
during activation has not seen the new instructions.

## Ask your first question

Ask the agent a repository question in your own words. The transcript below is
a real captured session, recorded with pinned inputs: the installed Entire
Graph release, a fresh clone of `gorilla/mux` at commit `db9d1d0` with
no prior agent instructions, activation files committed, and Claude Code
2.1.233 (model Claude Fable 5) as the client. The prompt:

> Without changing any files, find where route matching is implemented in this
> repository, what calls it, and what is most likely to be affected if its
> behavior changes. Cite the relevant source.

The session's tool calls, in order (long commands wrapped for width, the last
one truncated):

```text
Bash: entire graph search --repo . --profile full --query "route matching
      implementation for incoming request paths"
Bash: entire graph impact --repo . --symbol Route.Match
Bash: entire graph impact --repo . --symbol Router.Match
Bash: sed -n 145,230p mux.go
Bash: sed -n 40,120p route.go
Bash: grep -n "func.*Match\|matcher\b" route.go regexp.go | head -40
Bash: sed -n 185,225p regexp.go; grep -n "\.Match(" mux.go route.go middleware.go ...
```

The first call is a graph search. That is the activation instructions at
work: Claude Code loads `CLAUDE.md` at session start and resolves its import
of the guide, so the agent reached for the graph before any grep.
Search returns ranked JSON evidence; the top hit for this query was
`Route.addMatcher` at `route.go:237` with `newRouteRegexp` at `regexp.go:41`
right behind it. The agent then asked for blast radius. The start of the
`impact` output it received, verbatim except for one line wrapped to fit:

```text
Index: cache-hit (49ms) | Query: 0ms | Total: 50ms
Impact: Router.Match (mux.go:151) def=151 span=151-182 [method in Router]
Blast radius: 1 caller (1 direct, 0 transitive), 0 callees, 3 type consumers,
  1 data flow, 7 co-change files, 29 siblings.
Callers (1 direct, 0 transitive; who breaks if behavior changes):
- Router.ServeHTTP (mux.go:203, def :188)
```

That point-in-time capture predates ADR 0004's security correction. A current
default working-tree query reports `cache-miss`; committed-tree queries can
still report `cache-hit` when a matching entry exists.

Only after the graph queries did the agent read source, in narrow line ranges
around the reported locations. Its answer traced matching through
`Router.Match` (`mux.go:151-182`), `Route.Match` (`route.go:47-114`), and
`routeRegexp.Match` (`regexp.go:189-209`), and named what a change would
touch: handler dispatch and 404-vs-405 selection, route variables via
`setMatch`, URL reversing built by `newRouteRegexp`, and the CORS middleware
path through `getAllMethodsForRoute`. It also exposed a graph limit that the
agent verified against source: `impact --symbol Route.Match` reports zero
callers, while source inspection finds two direct call sites.

That last point is the working relationship to expect: graph output is
evidence for the agent to check against source, not an oracle. The
[supporting record](docs/evidence/2026-08-16-mux-agent-session.md) includes the
capture conditions, relevant agent and tool events, complete graph-command
outputs, and the final answer verbatim.

Each layer of the setup has its own success signal. Installation: both
`entire version` and `entire graph version` succeed. Activation: the three
files exist with intact markers. Adoption: in a fresh session, the first
code-locating call is `entire graph search`. If the agent begins with broad
grep or whole-file exploration, the guide may not have loaded or may not have
been followed. Check the activation files and the client's instruction view;
see [agent activation](docs/agents.md). Grounding: the answer cites files and
lines the agent actually opened.

## What to ask

Prompts are the interface. The commands are what the agent runs underneath;
you can also invoke them directly for manual inspection, debugging, or
automation. See the [command reference](docs/commands.md).

| Goal | Example prompt | Graph command |
| --- | --- | --- |
| Find the implementation | Find where request routing is implemented. | `search` |
| Read one definition | Show the definition of `ResolveRoute`. | `def` |
| Trace callers or callees | What calls `ResolveRoute`? | `neighbors` |
| Check the blast radius | What would changing `ResolveRoute` affect? | `impact` |
| Review a branch | Summarize the semantic changes from `main` to `HEAD`. | `diff` |
| Export the full graph | Export the repository graph as NDJSON. | `snapshot` |

## Working tree and cache

The interactive query family (`search`, `def`, `explain`, `neighbors`, and
`impact`) reads the working tree by default, so agents see uncommitted edits.
Add `--head` to ask about the committed tree instead. Bulk streams
(`snapshot`, `symbols`, `edges`) and ref-based analysis (`diff`, `commit`)
default to committed state.

Queries can write a derivative local cache; they never modify repository files.
Default working-tree queries always build fresh and do not load or store cache
entries. A `--head` query can reuse a snapshot keyed to the committed tree and
query options; changing `.graphignore` selects a different committed-tree
entry.

Cache state is visible where the format reports it: the default `search` JSON
carries `stats.index_cache_hit`, and `impact`/`neighbors` text output opens
with an `Index: cache-hit`/`cache-miss` line. Default working-tree queries
report a miss; matching `--head` queries may hit. `search --format text` does
not report cache state.

`entire graph index` prewarms committed-tree (`--head`) queries only, and
defaults to profile `full` while plain `search` defaults to `fast`. A default
`index` run therefore does not warm the default working-tree path. One caveat
inside the query family: `def` and `explain` only cache when `--cache-dir` or
`ENTIRE_PLUGIN_DATA_DIR` is set, unlike the other query commands. Cache
locations, key inputs, and prewarming are documented in
[operations](docs/operations.md#cache).

## Limits

Static analysis is heuristic. Calls through interfaces, reflection, dynamic
dispatch, and generated or runtime-wired code can be missed or unresolved.
The captured session above shows one such case. Dependent counts are guidance
for inspection, not compiler facts. Files the parser cannot handle surface as
machine-readable partial failures rather than silent gaps.

[Language coverage](docs/language-support.md) has two tiers: 36 languages with
semantic parsing, and inventory-only filetypes that get file and symbol
structure without call or type analysis. Check the current build with
`entire graph capabilities --json`.

Entire Graph is code intelligence for the repository your agent is working in:
ranked search, relationships, and change impact grounded in source. It does _not_
store user or conversation memory, run in the background, or expose its own MCP
server. The full data-flow and write-surface description, including what runs
caller-provided commands, is in
the [trust and security](docs/trust-and-security.md) documentation.

## Documentation

- [All Documentation](docs/README.md)
- [Command reference](docs/commands.md)
- [Agent activation and verification](docs/agents.md)
- [Search results and ranking](docs/search.md)
- [Operations: installs, cache, troubleshooting](docs/operations.md)
- [Trust and security](docs/trust-and-security.md)
- [Language support](docs/language-support.md)
- [Benchmark methodology and evidence](docs/benchmarks.md)

Please report problems in [GitHub Issues](https://github.com/entireio/entire-graph/issues)
or open a pull request. Thank you! ❤️

## License

Entire Graph is distributed under the [MIT License](LICENSE).
