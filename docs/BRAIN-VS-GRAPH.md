# Brain vs. graph — what entire-graph deliberately does not do

entire-graph is a **provider**: it turns a git tree into a deterministic, typed, no-egress
snapshot and answers ranked queries over it. Entire Brain is the **consumer**: it ingests that
snapshot alongside durable facts, sessions and checkpoints, and is where knowledge that is not
derivable from one tree lives.

The line is not organizational, it is a property of the artifact. Anything that is a pure function
of one repository at one revision belongs here, because it must stay deterministic and reproducible
from the tree alone. Anything that accumulates state across repositories, across time, or across
users belongs to Brain, because the moment this provider holds that state, "the same commit yields
the same graph" stops being true and every downstream claim built on it weakens.

This file exists so the items below are not re-litigated each time a competitor ships one.

## The verdicts

### MCP server surface — **Brain's job**
An MCP server is a stateful, long-lived process with a session, an auth story, and an audience
(which host, which tools exposed, which redaction). None of that is a function of a git tree, and
all of it is what Brain already is. The provider stays a one-shot, no-egress binary whose output is
a stable NDJSON contract; whoever wants MCP wraps that contract. Note the honest cost: until Brain
ships, every graph call an agent makes is a shell-out plus a permission prompt, and a competitor's
native tool call is cheaper per invocation. That is a real gap in **invocation economics** — it is
just not a gap this repository should close by growing a server.

### Multi-repo / cross-project graph — **Brain's job**
`repoKey` is per-repository and every command takes one `--repo` on purpose: the cache key is the
git **tree hash**, and there is no such thing as a tree hash for "nine repositories as of roughly
now". A merged cross-project graph is inherently a stateful, continuously-reconciled store — which
is Brain's shape, not a provider's. The provider's contribution is that its snapshots are cheap to
ingest and its symbol IDs are stable, so N of them can be merged upstream.

### save-result / reflect feedback loop — **Brain's job**
A feedback loop is memory: outcomes recorded per user over time, decayed, corroborated, and fed
back as a ranking overlay. Landing that here would make search results depend on who ran it and
what they clicked last week — the exact opposite of the determinism the schema contract and every
benchmark claim rest on. Brain is the correct home precisely because it is allowed to be stateful.
If a learned overlay ever influences provider ranking, it must arrive as an explicit input to a
query, never as hidden local state.

### Human dashboards beyond a markdown report — **neither / not worth it**
`index --report GRAPH_REPORT.md` is now in the provider, and it is the right amount: a
deterministic, diffable, committable projection of a snapshot the tool already has in memory, which
lets a human judge the tool in thirty seconds. An interactive HTML force-graph is not that. It is a
UI product with its own rendering and performance envelope, it is unusable at the node counts this
graph reaches on a real monorepo, and Brain's web surfaces are where an Entire user is already
looking at their code. Building one here would be the most visible and least useful thing in this
document.

### Per-file incremental invalidation — **graph's job, not done yet**
Tree-hash keying means a one-character commit pays a full rebuild. That is a provider concern
(cache correctness, no state beyond the tree) and it is the highest-value unbuilt item on this
side of the line. It is unbuilt, not declined.

### Watch mode / daemon — **neither / not worth it**
Explicitly declined, and the reasoning holds: re-running `index` *is* the refresh, a changed tree
misses the cache by construction, and a daemon adds a stateful moving part to a tool whose main
claim is that it has none.

### Named per-host installers — **graph's job, not done yet**
`init-agents` writing `AGENTS.md`/`CLAUDE.md` already covers the instruction-file hosts implicitly.
Per-host installers are cheap, additive, and a real distribution win; they are simply not
architecture, so they queue behind product behavior.

## One-line test for anything new

Would this make two runs on the same tree differ, or require state that outlives one command? If
yes, it is Brain's. If no, it is arguably ours — and then the only question is priority.
