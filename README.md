# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they stop burning your budget on grep-and-read
exploration and go straight to the fix.

On **SWE-bench Multilingual**, a **Claude Code agent running Haiku** with Entire Graph used
**31.6% fewer tokens** than the same agent with no tool at all, and resolved slightly more
(22.3 vs 20.0 of 50, ahead in all three runs, not statistically separable). It reached the fix in
**23.9 turns against 30.2** — the saving is turns removed from the locate phase, not a cheaper
per-turn cost. 100% local, no network, no model calls, no keys.

Read [Numbers, honestly](#numbers-honestly) before quoting any of that: the figure is
model-dependent, an earlier 55% claim did not survive a fair baseline, and the comparable tool did
not measurably beat no-tool at all.

## Install (one minute)

**You need Go 1.24+ and a working C compiler.** The parsers are tree-sitter grammars, which are C,
so `go install` builds with cgo — on macOS that means the Xcode command line tools
(`xcode-select --install`), on Debian/Ubuntu `build-essential`. Without a compiler the install fails
in the linker rather than telling you why.

```sh
go install github.com/entireio/entire-graph/cmd/entire-graph@main
entire plugin install "$(go env GOBIN | grep . || echo "$(go env GOPATH)/bin")/entire-graph" --force
```

The second line registers the binary as a subcommand of the [Entire CLI](https://github.com/entireio/cli),
which is what gives you `entire graph …`. **It is optional.** The binary is self-contained: if you do
not have the Entire CLI, skip that line and call it directly — `entire-graph search`,
`entire-graph impact`, `entire-graph capabilities --json` all work standalone. Everything below is
written as `entire graph <verb>`; substitute `entire-graph <verb>` if you installed only the binary.

Then, in any repo your agents work in:

```sh
entire graph init-agents
```

That's it. `init-agents` drops the operating guide into your project's `AGENTS.md` and `CLAUDE.md`,
so Claude Code, Codex, Gemini, Cursor, Pi — any agent that reads those files — picks up the
search-first, verify-once workflow automatically. No config, no MCP server, no daemon.

## Status line: what the graph is saving you, live

A one-line Claude Code badge, updated as the session runs:

```text
[GRAPH] ↗ 2.1M saved · 28 search · 9 impact · graph-first ✓ · 75% of locates · 12% of session
```

- **saved** — estimated tokens, same model as `entire graph stats` (see the caveats there).
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
| **Locate a fix** | grep → open files → grep again (~90% of session tokens) | one `entire graph search` → edit (the top 5 hits come back as complete function bodies) |
| **Impact of a change** | repo-wide grep for callers | `entire graph impact --symbol X` — callers, type consumers, data flow, co-change in one shot |
| **Review a diff** | file-by-file reading | `entire graph diff` — entity-level changes with dependent counts |

The search understands natural language ("XTRIM trims wrong stream entries"), ranks real
implementation code above tests, docs and config, bridges vocabulary gaps through the call
graph, and returns budgeted output designed to drop straight into an agent's context — the
top-ranked hits arrive as complete function bodies, so the common case is search → edit with
no follow-up file read at all.

**One search returns everything the next three turns would have cost:**

- **candidate fix sites**, the top hits as complete function bodies, plus **RELATED SITES**
  (callers, siblings, near-duplicate bodies) and the **COVERING TEST** that exercises the fix site.
- **SAME-CONCEPT LITERALS** — every place in the repository the queried concept is spelled out,
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

## Numbers, honestly

All savings are measured against the **baseline**: the same agent, same model, same task, with
no code tool at all. For scale, the same suites also ran
[codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) ("cmm"), an open-source
code-memory tool and the closest comparable.

The numbers below replace an earlier set that claimed 54.9% on Haiku and 57.7% on Sonnet. Those
were withdrawn after an adversarial audit of our own harness; what the audit found is listed under
*What was wrong with the old numbers*.

Current measurement — 50 language-stratified instances (8 languages, max 4 per repo), 3 runs per
arm, matched prompt discipline across arms, tokens taken from the billing-truth field:

| Claude Code · Haiku | Entire Graph | baseline (no tool) | cmm |
|---|---|---|---|
| tokens vs baseline | **−31.6%**  CI [−20.8, −41.3] | — | +5.1%, CI crosses zero |
| resolved (mean of 3 runs, of 50) | **22.3** | 20.0 | 20.3 |
| turns | **23.9** | 30.2 | 29.6 |
| USD per resolved instance | **$0.474** | $0.513 | $0.641 |

Entire Graph vs cmm on tokens: **−35.7%**, CI [−25.8, −44.3].

**The saving is model-dependent, and on stronger models it reverses.** It comes from deleting
grep-and-read turns, so it only pays when the agent would otherwise have spent them:

| model | baseline turns | turns Entire Graph removed | tokens |
|---|---|---|---|
| Haiku | 30.2 | **6.3** | **−31.6%** (CI excludes zero) |
| Sonnet | 15.6 | −0.6 (it added turns) | +13.9% (not significant, n=20, 1 run) |
| Fable | 10.1 | −0.9 (it added turns) | +31.4% (CI excludes zero) |

Break-even is near a 20-turn baseline. Reach for the graph when the agent would have to hunt; on a
task it would have finished in ten turns the payload is overhead. That is a property of the task,
not a verdict on the tool.

**What was wrong with the old numbers.** An audit of our own harness found eight defects, and every
one of them distorted the comparison rather than the tool:

- The old 54.9% was measured with a frugality clamp on the graph arm against a baseline given **no
  working-policy instructions at all**. With matched discipline the figure is 31.6%.
- "Identical completion rate" was wrong: that configuration resolved **131/300 against the
  baseline's 150/300** (McNemar p=0.013). It lost on accuracy and the headline said parity.
- The cmm comparison was rigged by our own prompt and wrapper: cmm was missing three discipline
  clauses the other arms had, its source snippets were clamped to 6 lines while ours returned whole
  function bodies (19.4 lines mean), and a concurrency fix had been applied to our arm only.
- Tokens were summed from a lossy accumulator that undercounted cmm by ~25%.
- A grading race submitted 7 real graph-arm patches as empty and 0 baseline patches.

cmm's 27.4%/36.6% "savings" do not survive any of that. On the corrected harness cmm is **+5.1%
against no tool with a confidence interval crossing zero** — it does not measurably save tokens.

The Pi / open-source row (31–73%) was measured on the same withdrawn methodology and is not
restated here.

**Still open, stated plainly.** These numbers come from a harness that had two known asymmetries
live during the run (our arm carried 4.3% test-path hits where cmm carried none; token accounting was
corrected afterwards in the reporter). A fully symmetric re-run is in progress and will replace this
table. The accuracy lead in particular rests partly on fixing the grading race described above, which
restored patches to our arm and none to the baseline — legitimate, because the baseline had none
voided, but it is a corrected error rather than a newly measured advantage.

Methodology, prompts, harness, and every caveat (variance bands, fairness controls, per-model
configs) will be public soon.

## More

- [AGENTS.md](AGENTS.md) — the agent operating guide (also: `entire graph agent-guide`)
- [docs/DETAILS.md](docs/DETAILS.md) — full command reference, architecture, language support,
  performance and accuracy benchmarks, security model
- `entire graph help` — command list; `entire graph doctor --json` — environment check

## License

See [LICENSE](LICENSE).
