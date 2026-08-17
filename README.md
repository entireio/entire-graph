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
example is below, after setup.

Entire Graph ranks first among eight memory systems on LoCoMo, a
long-conversation benchmark, and builds its index with zero model calls where
every inference-built competitor spends millions of tokens doing the same job.
See [benchmarks](docs/benchmarks.md) for the numbers, the methodology, and
what does and does not clear statistical significance.

## Install

Entire Graph requires Entire CLI 0.10.0 or later on `PATH`. The commands
below are the official ones from the [Entire CLI installation
guide](https://docs.entire.io/installation), which also covers Windows and
other channels.

On macOS (`brew trust` is an external subcommand provided by the tap, not
core Homebrew):

```sh
brew tap entireio/tap
brew trust entireio/tap
brew install --cask entire
```

On Linux:

```sh
curl -fsSL https://entire.io/install.sh | bash
```

Then install the plugin from the
plugin index and confirm both layers:

```sh
entire version
entire plugin install graph
entire graph version
```

`entire graph version` printing a release tag (for example `v0.3.0`) is the
installation check; a plugin built from source prints `dev` instead (see
[operations](docs/operations.md)). `entire enable` configures Entire session capture and is
not required for Entire Graph.

## Activate it for your agent

Activation is per repository, and it writes files, so here is exactly what
`init-agents` touches:

- `.entire/graph-agent.md` — the agent operating guide. Generated in full and
  regenerated in full on each successful rerun; manual edits there do not
  survive.
- `AGENTS.md` and `CLAUDE.md` — created if absent; otherwise one managed block
  between `<!-- entire-graph:begin -->` and `<!-- entire-graph:end -->` markers
  is added or replaced. Text outside the markers is preserved.

From the repository root:

```sh
entire graph init-agents --repo .
```

Review the three files, then commit them together when the instructions should
apply to the team. Committing also matters for performance: the files are
indexable Markdown, and while they sit uncommitted the working tree counts as
dirty, which turns off query cache reuse (details below). Finally, start a
fresh agent session in the repository — a session that was open during
activation has not seen the new instructions.

Marker handling, rerun behavior, client specifics, and recovery from damaged
instruction files are covered in [agent activation](docs/agents.md).

## Ask your first question

Ask the agent a repository question in your own words. The transcript below is
a real captured session, recorded with pinned inputs: the installed Entire
Graph v0.3.0 release, a fresh clone of `gorilla/mux` at commit `db9d1d0` with
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

Only after the graph queries did the agent read source, in narrow line ranges
around the reported locations. Its answer traced matching through
`Router.Match` (`mux.go:151-182`), `Route.Match` (`route.go:47-114`), and
`routeRegexp.Match` (`regexp.go:189-209`), and named what a change would
touch: handler dispatch and 404-vs-405 selection, route variables via
`setMatch`, URL reversing built by `newRouteRegexp`, and the CORS middleware
path through `getAllMethodsForRoute`. It also flagged a graph limit it
verified by hand — `impact --symbol Route.Match` reports zero callers because
both real call sites reach it through an interface loop.

That last point is the working relationship to expect: graph output is
evidence for the agent to check against source, not an oracle. The complete
record — capture conditions, every tool event, the full command outputs, and
the answer verbatim — is in the
[captured session evidence](docs/evidence/2026-08-16-mux-agent-session.md).

Each layer of the setup has its own success signal. Installation: both
`entire version` and `entire graph version` succeed. Activation: the three
files exist with intact markers. Adoption: a fresh session's first
code-locating call is an `entire graph search`; an agent that opens with grep
did not get the instructions (see [agent activation](docs/agents.md)).
Grounding: the answer cites files and lines the agent actually opened.

## What to ask

Prompts are the interface. The commands are what the agent runs underneath;
you can also invoke them directly for manual inspection, debugging, or
automation — see the [command reference](docs/commands.md).

| Goal | Example prompt | Graph command |
| --- | --- | --- |
| Find the implementation | Find where request routing is implemented. | `search` |
| Read one definition | Show the definition of `ResolveRoute`. | `def` |
| Trace callers or callees | What calls `ResolveRoute`? | `neighbors` |
| Check the blast radius | What would changing `ResolveRoute` affect? | `impact` |
| Review a branch | Summarize the semantic changes from `main` to `HEAD`. | `diff` |
| Export the full graph | Export the repository graph as NDJSON. | `snapshot` |

## Working tree and cache

The interactive query family — `search`, `def`, `explain`, `neighbors`,
`impact` — reads the working tree by default, so agents see uncommitted edits.
Add `--head` to ask about the committed tree instead. Bulk streams
(`snapshot`, `symbols`, `edges`) and ref-based analysis (`diff`, `commit`)
default to committed state.

Queries write a derivative local cache; they never modify repository files.
When the working tree is clean, a repeated query reuses a snapshot keyed to
the committed tree and the query options. A dirty file the graph can index —
including an extensionless file or a root dependency manifest such as `go.mod`
or `package.json` — turns reuse off for the repository until the tree is clean
again; dirty files the graph cannot index (a `.bin`, say) do not. Changing
`.graphignore` selects a different cache entry.

Cache state is visible where the format reports it: the default `search` JSON
carries `stats.index_cache_hit`, and `impact`/`neighbors` text output opens
with an `Index: cache-hit`/`cache-miss` line, as in the capture above.
`search --format text` does not report cache state.

`entire graph index` prewarms committed-tree (`--head`) queries only, and
defaults to profile `full` while plain `search` defaults to `fast` — a default
`index` run does not warm the default working-tree path. One caveat inside
the query family: `def` and `explain` only cache when `--cache-dir` or
`ENTIRE_PLUGIN_DATA_DIR` is set, unlike the other query commands. Cache
locations, key inputs, and prewarming are documented in
[operations](docs/operations.md#cache).

## Limits

Static analysis is heuristic. Calls through interfaces, reflection, dynamic
dispatch, and generated or runtime-wired code can be missed or unresolved —
the captured session above shows one such case. Dependent counts are guidance
for inspection, not compiler facts. Files the parser cannot handle surface as
machine-readable partial failures rather than silent gaps.

[Language coverage](docs/language-support.md) has two tiers: 36 languages with
semantic parsing, and inventory-only filetypes that get file and symbol
structure without call or type analysis. Check the current build with
`entire graph capabilities --json`.

Entire Graph is a one-shot local CLI for one repository. It is not a daemon,
a watcher, an MCP server, or a memory system. The full data-flow and
write-surface description, including what runs caller-provided commands, is in
[trust and security](docs/trust-and-security.md).

## Documentation

- [Documentation map](docs/README.md)
- [Agent activation and verification](docs/agents.md)
- [Command reference](docs/commands.md)
- [Search results and ranking](docs/search.md)
- [Operations: installs, cache, troubleshooting](docs/operations.md)
- [Trust and security](docs/trust-and-security.md)
- [Language support](docs/language-support.md)
- [Benchmark methodology and evidence](docs/benchmarks.md)

To work on Entire Graph itself:

```sh
mise run build
mise run test
mise run check
```

Report problems in [GitHub Issues](https://github.com/entireio/entire-graph/issues).
Entire Graph is distributed under the [MIT License](LICENSE).
