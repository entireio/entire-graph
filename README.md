# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they stop burning your budget on grep-and-read
exploration and go straight to the fix.

On the official **SWE-bench Multilingual** benchmark (300 real issues, 9 languages), a
**Claude Code agent running Haiku** with Entire Graph used **55% fewer tokens** than the same
agent working with no tool at all — same tasks, same model, identical completion rate. It also
roughly doubled the savings of [codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp)
(an open-source code-memory tool we benchmarked against as the closest comparable). 100% local,
no network, no model calls, no keys.

## Install (one minute)

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

## What your agents get

| agent workflow | before | with Entire Graph |
|---|---|---|
| **Locate a fix** | grep → open files → grep again (~90% of session tokens) | one `entire graph search` → read a line range → edit |
| **Impact of a change** | repo-wide grep for callers | `entire graph neighbors --symbol X --relation CALLS --direction in` |
| **Review a diff** | file-by-file reading | `entire graph diff` — entity-level changes with dependent counts |

The search understands natural language ("XTRIM trims wrong stream entries"), ranks real
implementation code above tests and docs, bridges vocabulary gaps through the call graph, and
returns budgeted output designed to drop straight into an agent's context.

Want the exact agent instructions? `entire graph agent-guide` prints them; they also live in
[AGENTS.md](AGENTS.md), including the copy-paste prompt block that produced the benchmark numbers.

## Where it fits in Entire

Entire Graph is the **semantic layer** of the Entire platform — infrastructure, not another
workflow to learn:

- **Entire Search / Why / Blame** consume it to answer developer questions with entity-level
  precision.
- **Checkpoints and Trails** use its `diff`/`commit` analysis to describe what an agent actually
  changed.

You (a human) will mostly experience it *through* those surfaces. Your agents call it directly.

## Numbers, honestly

All savings are measured against the **baseline**: the same agent, same model, same task, with
no code tool at all. For scale, the same suites also ran
[codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) ("cmm"), an open-source
code-memory tool and the closest comparable.

| agent + model | Entire Graph savings vs baseline | cmm on the same suite |
|---|---|---|
| Claude Code · Haiku — official SWE-bench Multilingual 300 | **54.9%** | 27.4% |
| Claude Code · Sonnet — 23 instances, 3× replicated | **57.7%** | 36.6% |
| Pi agent · open-source models (gpt-oss, Kimi K2.6, DeepSeek V4, GLM-5.2) | **31–73%** | — |
| Task completion (patch produced) | parity with baseline in every suite | parity |

Methodology, prompts, harness, and every caveat (variance bands, fairness controls, per-model
configs) are public in the [graphmark](https://github.com/entirehq/graphmark) repo
(`agentic-swebench/REPRODUCE.md` reproduces everything end to end).

## More

- [AGENTS.md](AGENTS.md) — the agent operating guide (also: `entire graph agent-guide`)
- [docs/DETAILS.md](docs/DETAILS.md) — full command reference, architecture, language support,
  performance and accuracy benchmarks, security model
- `entire graph help` — command list; `entire graph doctor --json` — environment check

## License

See [LICENSE](LICENSE).
