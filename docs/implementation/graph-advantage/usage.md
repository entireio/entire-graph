# Opt-in usage and rollback

All existing defaults remain. No advantage release gate has been declared passed.

```
entire graph search --repo . --query 'request validation' --extraction-cache on
entire graph impact --repo . --symbol HandleRequest --depth all --max-nodes 5000 --format json
entire graph search --repo . --query 'request validation' --ranking experimental-graph --format json
```

Linux compiler requests add `--compiler go --gopls /installed/gopls
--gopls-sha256 DIGEST --go-toolchain /installed/go`. The supported pinned server
is v0.20.0; an installed Bubblewrap launcher defaults to `/usr/bin/bwrap`.
Search, snapshot/record streams, impact and index accept these options. Index
persists only the static snapshot and computes fresh compiler evidence for its
response. Use `--require-compiler` when partial/unavailable coverage should fail.
Missing tools/dependency closure otherwise retain static results and explicit
compiler status. Def, explain and neighbors retain their static interfaces.

Native schema 1.2 adds optional extraction telemetry, operation input provenance
and compiler evidence. Retained capture memory is bounded separately from total
process RSS. Tighten cache limits with positive capped
`ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES` and
`ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_ENTRIES`; invalid values use defaults.
Search and impact retain envelope version 1 with additive diagnostics. Existing
IDs and native static relations retain their meanings. Compact/SCIP refuse an
enriched compiler projection. Ranking components are diagnostic JSON only. Graph diagnostics include input
relations, examined relations and connected candidate nodes; more than 100,000
input relations falls back to current ranking before scanning.

Deeper impact supports depth N/all, node/edge/frontier/retained-path/output-step
limits, adjacency/evidence input limits, relation families and minimum edge
confidence. Deeper-only controls require deeper mode; ordinary depth one/two
keeps its prior behavior with compiler off. Explicit compiler mode uses the
same enriched direct-call view at every depth and throughout search caller
boosts, expansion, ranking and related-site selection. Type conversions do not
create confirmed calls; implementation candidates remain separate. `all` is bounded closure of the chosen static policy,
not all runtime effects. Candidates and historical associations remain distinct.
See ADR 0032 for composition rules and terminal boundaries.

Rollback switches: `--extraction-cache off`, `--compiler off`, `--depth 2`, and
`--ranking current`. They are the defaults. `--no-cache` also disables extraction
reuse on participating cache-aware verbs. Extraction artifacts are disposable;
remove only the `extraction-*` namespace under the chosen private cache root.
Compiler/ranking state disappears at operation close. No migration, installation,
MCP/Brain change or persistent working-tree snapshot cache is involved.
