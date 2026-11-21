# Kernel-scale performance pass and Claude Code status line — design

Date: 2026-11-03 · Status: approved; W2 and W3 superseded on 2026-11-04 by the [store-native index design](2026-11-04-store-native-index-design.md)

## Why

An independent eval on a `--depth 1` Linux kernel clone (65,797 files) measured
this provider against a tuned tree-sitter graph tool:

| Operation (kernel) | entire graph today |
|---|---|
| `index --head --profile full` cold | 963 s, 43.0 GiB peak RSS, 404 MiB cache |
| `index` no-op (cache hit) | 54 s, 41.1 GiB |
| `neighbors` / `def` `--head` | 57–62 s, 42–45 GiB per call |
| `query --head --profile full` | 213–651 s, ~47 GiB per call |

Its call-graph answers were the more accurate of the two tools (4/4 callers of
`wake_up_new_task` vs 0/4). The cost, not the answers, is what blocks use on
large repos. A 41 GiB floor per lookup means a 16 GiB laptop cannot query the
kernel at all.

## Root causes (traced, to be confirmed by profile in W0)

1. Every `neighbors`/`def`/`impact`/`explain` call decodes the whole 423 MB
   gz-JSON entry into a `cachedSearchSnapshot` (`search_cache.go:1641`) with no
   string interning. Then each verb builds whole-repo maps (`neighbors.go:340-452`,
   `def.go:761-814`, `impact.go:367-540`) to answer about one symbol.
2. The opt-in sqlite sidecar (`ENTIRE_GRAPH_SEARCH_STORE`) already indexes
   `relations(from_id)` and `relations(to_id)`, but those verbs use it only as a
   full-load substitute (`search_store.go:369-565`).
3. `query --head` holds the full snapshot and the selective view at once, and
   lists the tree again for preselection (`search.go:1226`, `search_cache.go:956`).
4. The index build keeps about 5–7 by-value `SymbolRecord` copies alive together
   (`provider.go:1382-1645`, `4996-5137`). Relation sort comparators concatenate
   strings on every compare (`provider.go:1124`, `search_cache.go:1121,1511`). GC
   runs at the default GOGC=100 with no memory limit.

## Constraints (from AGENTS.md and docs)

- Schema `1.x` is frozen and additive-only. Verb output must be byte-identical
  before and after this pass.
- No egress.
- No daemon or watch mode: this was explicitly declined in
  `docs/brain-and-graph-boundaries.md`. Every speed-up must be a rebuildable
  on-disk derivative.
- A cache format change bumps the cache version (`searchSnapshotCacheVersion`).
- Unparseable or unsupported files still surface as partial failures.

## Workstreams

Each change is measured before and after with the same command on the same
clone. It is kept only if it beats run-to-run noise and the full test suite
stays green. Every attempt, kept or reverted, goes in `docs/perf-ledger.md`.

**W0 — Baseline and profile.**
- Build HEAD; measure on orbweaver and the kernel the cold index, the no-op
  index, and `neighbors`/`def`/`impact`/`query` with `--head`, with and without
  the sidecar.
- Profiling goes in a dev-only `//go:build pprof` file in `cmd/entire-graph`. It
  writes CPU and heap profiles when `ENTIRE_GRAPH_PPROF=<dir>` is set, and is
  absent from release builds.

**W1 — Focused lookups from the sidecar (largest expected win).**
- When a sidecar matching the entry exists, `neighbors`, `def`, `impact` and
  `explain` load a focused `ProviderSnapshot` instead of the full one. It holds:
  - the symbols matching the requested name (and file) and the relations
    touching them, to the verb's depth (≤ 2 hops);
  - the endpoint records those relations reference;
  - whatever per-verb extras the verb reads (containers and siblings, type
    relations, FILE_CHANGES_WITH for the focus file);
  - the header.
- The existing verb logic then runs unchanged on that snapshot.
- The starting set is a superset of everything the verb's exact resolution
  could return:
  - ID, or Unicode case-folded name or qualified name, via new indexed columns;
  - def's rewritten forms;
  - all of `def A B C`'s names.
  - Containment uses a new indexed `container_id` column, because the verbs
    scan `ContainerID`, not CONTAINS edges.
  - These columns land with their first reader, as `search_store.go` requires.
- The header and FILE_CHANGES_WITH refresh go through the same post-load
  function as the full path, with relation totals counted from the sidecar.
- Falls back to the full load:
  - when the verb's exact resolution comes back empty on a focused snapshot.
    The fuzzy ladder therefore always ranks the full set; a name-only fuzzy
    scan is a follow-up;
  - for positional `path:line` refs;
  - when there is no exact sidecar.
- The sidecar is on by default, for `index` and for lookup/query cache misses.
  An explicitly set `0`/`false`/`off`/`no` opts out.
  - A sidecar repair failure on a cache hit is a header warning, not an
    `index` failure.
  - The no-op index's second full store decode is removed first, so the
    default doesn't double no-op time (51 s → 104 s on the kernel today).
  - Cold index pays ~+26% to build the sidecar, measured and recorded.
- Target on the kernel: lookups in single-digit seconds and under 1 GiB RSS.

**W2 — Load and index memory, profile-driven.**
- Candidates:
  - intern repeated strings while decoding;
  - hold symbols by pointer or index instead of by-value copies during the build;
  - allocation-free relation comparators, with exact concatenation ordering.
- No default memory limit: sizing from RAM ignores cgroups. `GOMEMLIMIT` is
  documented instead.
- Each candidate is measured on its own.

**W3 — `query --head`.**
- Stop loading the full snapshot before preselection:
  - "symbols considered" and the diagnostics header come from the sidecar;
  - the selective view feeds the preselected files' sidecar rows (symbols and
    file-sourced relations, in rowid order) through the same
    emit/external/dedupe pipeline the full path uses.
- A selection covering every file keeps the full load.
- Profile-driven, same keep/revert rule.

**W4 — Claude Code status line.**
- New `entire graph statusline` subcommand:
  - reads Claude Code's status line JSON on stdin;
  - prints nothing and exits 0 on any failure;
  - never runs a subprocess on the render path except `git` for the
    indexed-state check.
- Rendered in graft's layout, tokens only, never dollars:
  ```
  ◤ entire graph · ✓ indexed · 23 lookups · ~184K tok gross · ~12.4K net
  ▸ ctx 42%
  ```
  - `ctx N%` is `context_window.used_percentage` from stdin.
  - `net` is the existing modeled balance from `stats` (it can be negative).
  - `gross` is new in `stats`: for each successful graph locate result, the
    summed on-disk size of the distinct files the result names, minus the
    result's bytes, /4.
    - It never goes below zero per result.
    - Files resolve against the root that call ran on (its `--repo`, else its
      cwd) through `os.OpenRoot`.
    - Sizes are computed at report time, never stored in the per-transcript
      memo.
  - `✓ indexed` / `not indexed` checks whether a cache entry exists for HEAD
    under any profile.
    - It only stats paths: it never hashes or opens the entry.
    - `DescribeSearchStore` hashes the whole entry, so it is too slow for this.
  - Segments are dropped from the right to fit `COLUMNS`. Line 2 is dropped
    when `used_percentage` is absent or null.
  - The computed figures, not the rendered line, are cached. The key is every
    in-scope transcript file's size and mtime, plus HEAD and the binary.
- `entire graph setup` installs it into the repo's `.claude/settings.local.json`
  (the file it already writes its SubagentStart hook to):
  - the command is the bare `entire graph statusline` on every OS:
    `statusLine` has no `shell` key, and Windows runs it under Git Bash or
    PowerShell;
  - `--no-statusline` opts out;
  - a local status line overrides the user's own, so setup leaves things alone
    when any scope (local, shared project, user) already has a foreign
    `statusLine`, and reports that instead;
  - `--statusline` installs anyway, overriding a shared or user line in this
    repo only;
  - a foreign local line is never replaced;
  - re-running it is a no-op.
- The shell script stays for now. The README points to the subcommand.

**W5 — Re-run the eval.** Re-run the harness in `graph-eval/` on orbweaver and
the kernel, then update the comparison doc.

## Testing

Tests pin behavior at public boundaries and never assert source text or
internal structure.
- **Differential equivalence (W1, W3).** The focused path's output must be
  identical to the full-load path's (JSON with telemetry stripped recursively;
  agent/text output byte-for-byte minus telemetry lines). The guard covers:
  - sampled symbols from every fixture;
  - every way of naming a symbol: name, case variant, qualified name, ID,
    `--file`, `--kind`, `--line`, `path:line`, def rewrites and multi-name, a
    miss, a misspelling;
  - both directions and depths.

  Two new fixtures cover what the existing ones cannot:
  - case-colliding names, extension members, inherited members, overrides, and
    multi-commit co-change history that changes on an unchanged tree;
  - a C fixture.

  The guard needs no hand-written expectations and survives any internal
  refactor.
- **Status line (W4).** Real-shaped stdin JSON plus a small transcript fixture
  go in; hand-derived literal values come out: ctx %, lookup count, gross and
  net tokens. Malformed or empty stdin gives empty output and exit 0. A width
  limit drops whole segments.
- **Gross figure (W4).** A transcript naming two files of known sizes gives
  hand-computed literal tokens. A result naming no file contributes 0.
- **Setup (W4).** A second run changes nothing. A foreign `statusLine` survives
  and is reported. `--no-statusline` writes none.
- W2 needs no new behavior tests: the existing suite plus the differential
  tests guard it.

## Out of scope (next pass)

Accuracy: C macro-heavy parse failures (48% of kernel C files carry error
nodes), function-pointer references (`.backlog_rcv = tcp_v4_do_rcv`), Rust
path-qualified calls (`crate::normalize_angle(..)`), closure-call resolution in
`impact`, test functions labelled as production callers. Parser changes there
bump `IdentityRevision`.
