# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they stop burning your budget on grep-and-read
exploration and go straight to the fix.

On the official **SWE-bench Multilingual** benchmark (300 real issues, 9 languages), an agent with
Entire Graph used **55% fewer tokens** than the same agent without it — roughly **double the
savings of the next-best code-memory tool** — winning 8 of 9 languages, with identical task
completion. 100% local, no network, no model calls, no keys.

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
- **Entire Brain** ingests its snapshot stream (`symbols`, `edges`, `snapshot` NDJSON) as the code
  half of its durable memory.
- **Checkpoints and Trails** use its `diff`/`commit` analysis to describe what an agent actually
  changed.

You (a human) will mostly experience it *through* those surfaces. Your agents call it directly.

## Numbers, honestly

| measurement | result |
|---|---|
| Official SWE-bench Multilingual 300, Haiku | **54.9%** token savings vs no-tool agent (next-best tool: 27.4%) |
| Sonnet, 23 instances, 3× replicated | **57.7%** vs 36.6% |
| Open-source models (gpt-oss, Kimi K2.6, DeepSeek V4, GLM-5.2) via the Pi agent | **31–73%** savings vs each model's own baseline |
| Task completion (patch produced) | parity with baseline in every suite |

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
