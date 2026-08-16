# Entire Graph

Entire Graph is a local static-analysis plugin for the Entire CLI that helps
coding agents find definitions, relationships, and change impact in Git
repositories.

Its built-in analyzer parses repository contents with tree-sitter and returns
ranked source locations, symbols, and static relationships. Analysis runs
without network egress, hosted model calls, or API keys.

Entire Graph is a one-shot repository-local CLI and semantic provider, not a
daemon, hosted memory service, or MCP server.

The recommended path is: [install](#install) → [activate it for an
agent](#activate-it-for-your-coding-agent) → [ask a repository
question](#try-a-repository-question) → inspect the cited source.

## Install

Entire Graph is an external command for Entire CLI 0.10.0 or later. Install
`entire` and make sure it is on `PATH`.

On macOS, install the stable release with Homebrew:

```sh
brew tap entireio/tap
brew trust entireio/tap
brew install --cask entire
```

On Linux, use the stable install script:

```sh
curl -fsSL https://entire.io/install.sh | bash
```

See the [Entire CLI installation documentation](https://docs.entire.io/installation)
for Windows, source builds, updates, and other methods. Then verify the CLI:

```sh
entire version
```

`entire enable` configures Entire session capture; it is not required to run
Entire Graph.

Install the indexed plugin and verify that the command dispatches:

```sh
entire plugin install graph
entire graph version
```

## Activate it for your coding agent

Activation is per repository. Before running it, know that `init-agents`:

- Creates `.entire/graph-agent.md`, or replaces that generated file in full on
  every successful rerun. Manual edits to it are not preserved.
- Creates `AGENTS.md` and `CLAUDE.md` when absent. In existing files it appends
  one managed block when no Entire Graph markers exist, or replaces exactly one
  ordered managed block while preserving text outside it.
- Validates both instruction files before writing. Malformed, reversed, or
  duplicate Entire Graph markers, and instruction-file paths that resolve to
  non-regular targets, stop activation without changing any activation file.

From the repository root, run:

```sh
entire graph init-agents --repo .
```

Review `.entire/graph-agent.md`, `AGENTS.md`, and `CLAUDE.md`. Commit them
together when the instructions should apply to the team. Because all three
generated paths have the supported `.md` extension, default working-tree
queries bypass snapshot reuse repository-wide while Git reports any one as
dirty or untracked.

The generated instructions tell an agent to search the graph before broad code
exploration, inspect focused source, check related sites when needed, and verify
its result. See the [guide used in this repository](.entire/graph-agent.md), or
print the installed version without writing files:

```sh
entire graph agent-guide
```

Instruction discovery differs between coding clients. After activation, start a
fresh task or session rooted at the repository in a client that reads the
generated repository instructions.

## Try a repository question

Ask about code in the repository rather than invoking a graph command yourself:

```text
Without changing repository source files, find where <known feature> is
implemented, identify what calls it or what changing it would affect, and cite
the relevant source.
```

Use the client's visible activity or tool log to verify the workflow:

1. The fresh task discovers the repository instructions.
2. The agent runs `entire graph search` before broad repository scanning.
3. It opens focused source around the ranked result.
4. It uses `neighbors` or `impact` when the relationship or blast radius matters.
5. Its answer cites the source it checked. For a code change, it also runs the
   narrowest relevant verification it can identify.

Because the evidence is specific to your repository and task, verify the live
activity rather than comparing it with canned output. The source task above is
read-only, but a graph query may still write a derivative local cache.

## Common tasks and cache behavior

Prompts are the primary interface. The graph operation is what the agent uses
internally, or what you can run directly for debugging and automation.

| Goal | Ask the agent | Graph operation |
| --- | --- | --- |
| Find code for a task | “Find where … is implemented.” | `search` |
| Understand one definition | “Show and explain the definition of …” | `def` |
| Inspect callers and relations | “What calls …?” | `neighbors` |
| Estimate a symbol's blast radius | “What would changing … affect?” | `impact` |
| Review a commit or branch | “Summarize the semantic changes in …” | `commit` / `diff` |
| Export the graph | “Export the graph for …” | `snapshot` |

`search`, `def`, `explain`, `neighbors`, and `impact` read the working tree by
default, so an agent sees current edits. Use `--head` only when the question is
intentionally about committed state. Whole-graph streams and ref-based analysis
have command-specific tree semantics; check `entire graph <command> --help`.

Working-tree snapshot reuse is conservative and repository-wide. A dirty path
with a supported extension, an extensionless path that could be a shebang
script, or a root manifest used for import resolution bypasses reuse for that
query. Other dirty, non-manifest paths with known unsupported extensions remain
eligible. `.graphignore` content is part of the cache key, and a changed
committed tree selects a different entry.

JSON search output exposes `stats.index_cache_hit`; `--format agent` includes a
compact cache header. Text output does not report cache state. Cache writes are
derivative local state, not repository source changes.

`entire graph index` warms one committed-tree cache variant, not the default
working-tree agent path. A later `--head` query can reuse it only with the same
resolved cache directory and profile, the same ordered `--ignore-file` and
`--include-file` paths with unchanged contents, and unchanged `.graphignore`
content. `index` defaults to profile `full`, while `search` defaults to `fast`,
so their defaults do not share a variant.

## Agent flow, boundaries, and limits

```text
user prompt → coding agent → repository instructions → entire graph CLI
            → ranked graph evidence → focused source/test check → sourced answer
```

- Built-in analysis is local and no-egress; installation obtains software over
  the network.
- `init-agents` writes the three disclosed repository instruction files, and
  queries may write derivative caches.
- Static relations and dependent counts are not compiler-accurate. Calls,
  routes, tool registrations, renames, dynamic dispatch, reflection, generated
  code, and runtime wiring can be heuristic or incomplete.
- Unsupported languages are inventory-only or produce explicit partial
  failures. Check support before relying on semantic relations.
- Working-tree and committed-tree defaults differ by command family.
- Entire Graph is a one-shot CLI and semantic provider. [Entire Brain is a
  separate durable consumer and MCP surface](docs/brain-and-graph-boundaries.md);
  Entire Graph is not a daemon, watcher, hosted memory service, or MCP server.

Treat graph output as evidence, not an oracle: inspect the relevant source and
verify changes with the repository's own tests or build.

## Documentation and manual use

- [Documentation map](docs/README.md) — repository documentation index
- [Language support](docs/language-support.md) — semantic and inventory-only
  languages
- [Entire Brain and Entire Graph boundaries](docs/brain-and-graph-boundaries.md)
- [Benchmark methodology and evidence](docs/benchmarks.md)

For direct inspection, diagnostics, and feature detection:

```sh
entire graph help
entire graph capabilities --json
entire graph doctor --json
```

For development in this repository:

```sh
mise run build
mise run test
mise run check
```

Report problems in [GitHub Issues](https://github.com/entireio/entire-graph/issues).
Entire Graph is distributed under the [MIT License](LICENSE).
