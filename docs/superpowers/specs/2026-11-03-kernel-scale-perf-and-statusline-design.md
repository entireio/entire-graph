# Kernel-scale performance pass and Claude Code status line — design

Date: 2026-11-03 · Status: approved (decisions below), pending spec review

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
- Name lookup needs a `symbols(name)` index, which lands with its first reader,
  as `search_store.go` requires. The fuzzy-suggestion ladder reads a name-only
  column scan.
- `index` writes the sidecar by default. `ENTIRE_GRAPH_SEARCH_STORE=0` opts out.
- A missing or mismatched sidecar falls back silently to today's full load.
- Target on the kernel: lookups in single-digit seconds and under 1 GiB RSS.

**W2 — Load and index memory, profile-driven.**
- Candidates:
  - intern repeated strings while decoding;
  - hold symbols by pointer or index instead of by-value copies during the build;
  - allocation-free relation comparators;
  - a default soft memory limit (`debug.SetMemoryLimit`) unless `GOMEMLIMIT` is
    set.
- Each candidate is measured on its own.

**W3 — `query --head`.**
- Derive the selective view from the sidecar, reading only the preselected files'
  rows, instead of decoding the full snapshot and filtering it. Release the full
  snapshot before ranking where the store is absent.
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
  - `gross` is new in `stats`: for each graph locate result, the summed on-disk
    size of the distinct files the result names, minus the result's bytes, /4.
    It never goes below zero per result.
  - `✓ indexed` / `not indexed` checks whether a cache entry exists for HEAD,
    without decoding it.
  - Segments are dropped from the right to fit the width.
  - Renders are cached by transcript size and mtime.
- `entire graph setup` installs it into the repo's `.claude/settings.local.json`
  (the file it already writes its SubagentStart hook to):
  - `--no-statusline` opts out;
  - it never replaces a `statusLine` it did not write and reports that instead;
  - re-running it is a no-op.
- The shell script stays for now. The README points to the subcommand.

**W5 — Re-run the eval.** Re-run the harness in `graph-eval/` on orbweaver and
the kernel, then update the comparison doc.

## Testing

Tests pin behavior at public boundaries and never assert source text or
internal structure.
- **Differential equivalence (W1, W3).** For every symbol in the existing
  multi-language fixture repos, and for both directions and depths, the focused
  path's verb output is byte-identical to the full-load path's. This needs no
  hand-written expectations and survives any internal refactor.
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
