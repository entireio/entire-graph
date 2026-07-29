# Entire Graph

**This release is for your agents.**

Entire Graph gives coding agents a precomputed, deterministic map of your codebase — every
function, type, route, and call relationship — so they stop burning your budget on grep-and-read
exploration and go straight to the fix.

On **SWE-bench Multilingual**, a **Claude Code agent running Haiku** with Entire Graph used
**17.4% fewer tokens** than the same agent with no tool at all (CI [−27.8, −6.5], 48 instances × 3
replicate runs, matched prompt discipline in every arm) at **no loss of resolved issues** (54 vs 57
of 144 matched runs, McNemar p=0.42). Cost per resolved issue: **−6.9%**. The saving is turns
removed from the locate phase, not a cheaper per-turn cost. 100% local, no network, no model calls,
no keys.

The most robust result is retrieval, which is measured without an agent and so is noise-free: the
file the gold patch edits reaches the agent in **96.3%** of sessions versus **81.5%** for the
closest comparable tool, at equal payload bytes and with fewer search calls.

Read [Numbers, honestly](#numbers-honestly) before quoting any of that. In short: the figure is
Haiku-specific and we **cannot** measure it on stronger models at these sample sizes; a **≥35%**
saving is *refuted*, not merely unproven; two earlier claims (55%, then 31.6%) were withdrawn after
we audited our own harness and found the advantage was coming from our prompt, not our index.

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

Current measurement — 48 language-stratified instances × **3 replicate runs per arm** (144 matched
instance-runs), matched prompt discipline across all three arms, tokens from the billing-truth
field:

| Claude Code · Haiku | Entire Graph | baseline (no tool) | cmm |
|---|---|---|---|
| total tokens vs baseline | **−17.4%**  CI [−27.8, −6.5] | — | +7.0%, CI crosses zero |
| geomean of per-instance ratios | **−32.0%**  CI [−41.5, −21.2] | — | −5.7%, CI crosses zero |
| USD vs baseline | **−11.8%** | — | +8.0% |
| resolved (of 144 matched runs) | 54 | 57 | 57 |
| **USD per resolved instance** | **$0.539** (−6.9%) | $0.579 | $0.625 |

Entire Graph vs cmm on tokens: **−22.8%** total, **−27.1%** geomean CI [−36.8, −15.8].

Read the last two rows together, because they are the honest headline: the token saving is real and
its CI excludes zero, but eg resolved **54 against the baseline's 57** (McNemar p=0.42 — a tie, not
a loss), so **cost per resolved issue improves by 6.9%, not by 17%**. A token cut that costs you
resolves is not a saving, and the per-resolved figure is the one to quote.

**Retrieval is the part that holds up best**, because it is measured without an agent in the loop
and so is not subject to the noise below. Pooled over two haiku-30 cells (54 arm-instance pairs
where both tools issued a search), the file the gold patch edits appears in Entire Graph's payload
in **52/54 = 96.3%** of sessions against cmm's **44/54 = 81.5%** — 10 instances we surface that cmm
misses, 2 the reverse, sign test p = 0.0386 — at comparable payload bytes and with **fewer** search
calls (81 vs 112). Caveats that belong with that number: the two arms wrote entirely different
queries (0/27 exact overlap), so it measures each arm end-to-end rather than the retriever alone;
part of cmm's deficit is our own wrapper capping it at 10 BM25 + 5 semantic hits; and in the
second cell the gap narrows to 92.6% vs 88.9% (p = 1.000).

**The saving is model-dependent, and outside Haiku we cannot measure it.** It comes from deleting
grep-and-read turns, so it only pays when the agent would otherwise spend them:

| model | baseline turns | tokens | can this sample detect an effect? |
|---|---|---|---|
| Haiku | 28–30 | **−17.4%** total, CI excludes zero | yes (n=48 × 3 runs) |
| Sonnet | 12–18 | +0.9% geomean, CI [−33, +41] | no — inside noise |
| Opus | 11 | −6.4%, CI crosses zero | no — MDE is 59% at n=10 |
| Fable | 8 | +6.7%, CI crosses zero | no — MDE is 93% at n=5 |

Break-even is near a 20-turn baseline. Reach for the graph when the agent would have to hunt; on a
task it would have finished in ten turns the payload is overhead. That is a property of the task,
not a verdict on the tool.

**The noise floor, so you can judge every figure above.** Re-running a *byte-identical*
configuration moves total tokens by up to **±20%** (paired per-instance log-ratio sd 0.525 across
replicate baseline cells; ±26% observed between three replicates of the same no-tool arm). With
80% power that is a minimum detectable effect of **31% at n=29, 59% at n=10, 93% at n=5**. Any
single-run claim on a 5- or 10-instance sample is unfalsifiable at this scale, including ours —
which is why only the 3-replicate Haiku row is stated as a result.

**A ≥35% token saving is refuted, not merely unmeasured.** In the best-controlled cell the
favourable bound of the confidence interval on total tokens is −27.8%. Configurations that produced
35%+ did so by giving the graph arm prompt discipline the other arms did not have; removing that
advantage twice erased the win both times.

**What was wrong with the old numbers.** An audit of our own harness found eight defects, and every
one of them distorted the comparison rather than the tool:

- The old 54.9% was measured with a frugality clamp on the graph arm against a baseline given **no
  working-policy instructions at all**.
- The 31.6% that replaced it was *also* contaminated: the graph arm's prompt carried a 119-word
  operating-rules paragraph the comparison arm never got, on top of a frugality block all three
  arms already shared. Deleting that redundant restatement moved the same cell from −18.9% to
  **+26.9%** — a ~45-point swing from prompt text alone.
- The graph arm was being measured on an easier subsample: the harness skipped an instance **for the
  graph arm only** when its prebuilt dump was missing, which silently dropped 10 of 30 — and they
  were the large repos. All 10 dumps build fine; restoring them changed the cell by 7 points.
- One model's instance list was not SWE-bench Multilingual at all (it contained a Python
  SWE-bench instance), so that cell was ungradeable and its figure is withdrawn.
- Run-to-run noise on a byte-identical configuration is **±20%**, which is larger than most of the
  effects we had been reporting. Single-run cells at n=5 and n=10 cannot detect anything here.
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
