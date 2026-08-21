# Entire Brain and Entire Graph Boundaries

entire-graph is a **provider**: it turns a repository revision or working tree into a typed,
no-egress snapshot and answers ranked queries over it. Entire Brain is the **consumer**: it ingests
that snapshot alongside durable facts, sessions, and checkpoints, and is where knowledge that is
not reproducible from repository state belongs.

The line is not organizational; it is a property of the artifact. Facts derived from one requested
repository state belong here, including explicitly labeled co-change relations from bounded Git
history anchored at a revision. State accumulated across repositories, users, or independent runs
belongs to Brain. Hidden provider state would make identical requests produce different results and
weaken every downstream claim built on them.

This file exists so the items below are not re-litigated each time a competitor ships one.

## The verdicts

### MCP server surface: **brain's job**
An MCP surface adds client-facing tool, lifecycle, and policy decisions that are not graph facts.
Entire Brain already owns that integration. The provider stays a one-shot, no-egress binary whose
output is a stable NDJSON contract. Direct provider calls remain shell-outs; Entire Brain supplies
the local MCP tool surface for clients that need one. That boundary is intentional, not a reason
to grow a second server in this repository.

### Multi-repo / cross-project graph: **brain's job**
`repoKey` is per-repository and every command takes one `--repo` on purpose. Committed-tree cache
identity includes the repository, checkout, tree, profile, limits, selected files, and ignore
inputs; there is no equivalent identity for "nine repositories as of roughly now." A merged
cross-project graph is inherently a continuously reconciled store: Brain's shape, not a
provider's. The provider's contribution is that its snapshots are cheap to ingest and its symbol
IDs are stable, so N of them can be merged upstream.

### Save-result / reflect feedback loop: **brain's job**
A feedback loop is memory: outcomes recorded per user over time, decayed, corroborated, and fed
back as a ranking overlay. Landing that here would make search results depend on who ran it and
what they clicked last week: the exact opposite of the determinism the schema contract and every
benchmark claim rest on. Brain is the correct home precisely because it is allowed to be stateful.
If a learned overlay ever influences provider ranking, it must arrive as an explicit input to a
query, never as hidden local state.

### Human dashboards beyond a markdown report: **neither / not worth it**
`index --report GRAPH_REPORT.md` is now in the provider, and it is the right amount: a
deterministic, diffable, committable projection of a snapshot the tool already has in memory, which
lets a human judge the tool in thirty seconds. An interactive HTML force-graph is not that. It is a
UI product with its own rendering and performance envelope, it is unusable at the node counts this
graph reaches on a real monorepo, and Brain's web surfaces are where an Entire user is already
looking at their code. Building one here would be the most visible and least useful thing in this
document.

### Per-file incremental invalidation: **graph's job, not done yet**
Tree-hash keying means a one-character commit pays a full rebuild. That is a provider concern
(cache correctness and reuse of repository-derived facts) and it is the highest-value unbuilt item
on this side of the line. It is unbuilt, not declined.

### Watch mode / daemon: **neither / not worth it**
Explicitly declined, and the reasoning holds: re-running `index` *is* the refresh, a changed tree
misses the cache by construction, and a daemon would add an authoritative mutable process where
the provider currently has only rebuildable derivative caches.

### Named per-host installers: **graph's job, not done yet**
`init-agents` writing `AGENTS.md`/`CLAUDE.md` already covers the instruction-file hosts implicitly.
Per-host installers are cheap, additive, and a real distribution win; they are simply not
architecture, so they queue behind product behavior.

## One-line test for anything new

Would this make two identical requests differ, or require mutable state that outlives one command?
If yes, it is Brain's. If no, it is arguably ours, and then the only question is priority.
