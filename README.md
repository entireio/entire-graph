# Entire Graph

Coding agents often lose time before the edit, while they are still trying to
find the right code. Entire Graph gives them ranked search results and a local
map of the repository. It connects definitions to callers, types, routes, and
change impact, with `file:line` locations for resolved code.

Entire Graph runs as a plugin for the Entire CLI. Its built-in analyzer parses
the repository with tree-sitter and makes no network requests, model calls, or
API-key lookups.

## See it work

Here is a real question from this repository:

> Where do we keep terminal escape sequences out of CLI output, and what
> depends on that code?

Running `search` for that question found the implementation; `impact` then
showed its blast radius. This is abridged from the captured command output:

```text
search
1. internal/termsafe/termsafe.go:295-317  appendEscaped

impact
Impact: appendEscaped (internal/termsafe/termsafe.go:295)
Blast radius: 34 callers (3 direct, 31 transitive), 1 callee

Direct callers:
- Writer.Write (internal/termsafe/termsafe.go:83)
- Line         (internal/termsafe/termsafe.go:112)
- Bytes        (internal/termsafe/termsafe.go:135)

(31 transitive callers omitted)

Callee:
- escapedAt (internal/termsafe/termsafe.go:234)
```

The result identifies the fix site, its three direct callers, and the broader
set of code worth inspecting.

## Quick start

Entire Graph requires Entire CLI 0.10.0 or later. Install `entire` and make sure
it is on `PATH`.

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

For Windows, source builds, and other options, see the [Entire CLI installation
guide](https://docs.entire.io/installation). Then install the plugin:

```sh
entire version
entire plugin install graph
entire graph version
```

`entire enable` configures Entire session capture. Entire Graph can run without
it. Built-in graph analysis has no network egress; installation is the
networked step.

### Add the agent instructions

Activation is per repository. `init-agents` writes
`.entire/graph-agent.md` and creates or updates a managed block in `AGENTS.md`
and `CLAUDE.md`. It preserves text outside those blocks. A successful rerun
regenerates `.entire/graph-agent.md` and updates the managed blocks, so do not
edit the generated guide by hand.

From the repository root, run:

```sh
entire graph init-agents --repo .
```

Review the three files and commit them when the instructions should apply to
the team. Start a fresh agent session in the repository so the client discovers
the new instructions.

Then ask a normal code question. For example:

> Find where working-tree cache reuse is decided. Show me what feeds that
> decision and which source I should inspect before changing it.

The generated guide asks the agent to use the graph for location, open the
relevant source, check related sites when they matter, and run focused
verification after an edit.

## What to ask

Prompts are the main interface. You can also run the commands directly for
debugging or automation.

| Goal | Example prompt | Graph command |
| --- | --- | --- |
| Find the implementation | Find where request routing is implemented. | `search` |
| Read one definition | Show the definition of `ResolveRoute`. | `def` |
| Trace callers or callees | What calls `ResolveRoute`? | `neighbors` |
| Check the blast radius | What would changing `ResolveRoute` affect? | `impact` |
| Review a branch | Summarize the semantic changes from `main` to `HEAD`. | `diff` |
| Export the full graph | Export the repository graph as NDJSON. | `snapshot` |

For a command overview, run `entire graph help`. Use
`entire graph <command> --help` for its flags and output formats.

## Working tree and cache

Interactive queries such as `search`, `def`, `explain`, `neighbors`, and
`impact` read the working tree by default, so agents can see uncommitted edits.
Add `--head` when the question is specifically about committed state.

Queries may write a derivative local cache. `entire graph index` warms one
committed-tree cache variant for later `--head` searches and neighbor lookups;
it does not prewarm the default working-tree path.
`index` defaults to profile `full`, while `search` defaults to `fast`, so choose
the same profile when prewarming search. Run `entire graph index --help` for
cache and include/ignore controls.

## Using the results

Treat graph results as a map back to the code. Read the cited source and run the
repository's tests or build before keeping a change. Static relationships can
miss dynamic dispatch, reflection, and generated or runtime-wired code.

[Language coverage](docs/language-support.md) varies. Some languages provide
file inventory without deeper symbols or relationships.

For durable cross-project memory and an MCP interface, use
[Entire Brain](docs/brain-and-graph-boundaries.md).

## Documentation

- [Documentation map](docs/README.md)
- [Language support](docs/language-support.md), or inspect the current build
  with `entire graph capabilities --json`
- [Benchmark methodology and evidence](docs/benchmarks.md)
- [Entire Brain and Entire Graph boundaries](docs/brain-and-graph-boundaries.md)

Run `entire graph doctor --json` to inspect repository resolution and runtime
settings.

To work on Entire Graph itself:

```sh
mise run build
mise run test
mise run check
```

Report problems in [GitHub Issues](https://github.com/entireio/entire-graph/issues).
Entire Graph is distributed under the [MIT License](LICENSE).
