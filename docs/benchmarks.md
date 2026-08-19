# Benchmarks

Reproducible performance and quality measurement for `entire-graph`. The
harness lives in `cmd/graph-bench` (driver) and `internal/bench` (measurement
core); see `bench/README.md` for layout and flags. Its original WP10 plan is
[archived](archive/2026-06-18-entire-graph-v2-implementation-plan.md).

## External native-memory comparison

GraphMark's verified `memory-native-v52` release pairs the production prose
search path with Graphify on 300 LOCOMO and 50 LongMemEval-S questions. Entire
Graph scored 0.914 vs. Graphify 0.787 on LOCOMO recall@10, 77.2% vs. 59.3% on
LOCOMO QA accuracy, and 76.0% vs. 68.0% on LongMemEval-S QA accuracy. All
systems used zero build-time LLM credits. Codebase Memory MCP is present in the
release as an off-domain native diagnostic: v0.9.0 indexes the Markdown content
as `Section` nodes but excludes that node type from its public natural-language
BM25 route, so its column is not a third apples-to-apples memory comparison. On
that route it retrieved nothing at all — all 1,050 CMM cells returned a
byte-identical empty context (24 bytes, zero hits), so its QA number is the
shared reader answering from the question alone.

Those 300 + 50 cases are not tune-disjoint: 35 of the 350 overlap the tune
phase. The tune-disjoint holdout is the clean generalization estimate — Entire
Graph scored 75.6% LOCOMO QA accuracy and 0.900 recall@10 on it (n=30; the
LongMemEval-S holdout is n=5).

The product under test is commit
`c9641bf1caaf41d64ce8a4a421f041939feecca3`. Its native JSON result contains a
backward-compatible primary `snippet` plus optional exact, non-overlapping
same-file `passages`. The neutral GraphMark adapter validates the primary
locator, validates every additive passage's path, line range, and text against
the materialized corpus, and reapplies the shared byte cap. It performs no
answer-aware selection or adapter-side source expansion.

Release evidence is bound to protocol
`beb891c8985ef3d4565c7fc32b6b06478b4384a0975fbe82592715e8c6f7c02d`
and full completion
`0d85eafde84ad52480454cab1906e2ce37e6d2003d3f0f537835228c733c07e7`.
The GraphMark publication bundle records exact inputs, revisions, retry ledger,
statistics, costs, artifact hashes, and limitations. The bundle is not
distributed in this repository and has no public download location yet: the
hashes above let a holder of the bundle verify it, but a reader here cannot
fetch it independently. Until it is published, treat this external comparison
as attested rather than independently reproducible. Call the result a
**public-protocol reimplementation**: Graphify does not publish the original
memory harness or selectors behind its historical README numbers.

## LoCoMo memory-system comparison

A separate comparison from the one above: eight memory systems (entire-graph, mem0 OSS, cognee,
BM25, cmm, graphify, letta, supermemory) on all 1,540 questions of LoCoMo, under one shared reader
model, one shared judge model, and a 200-item retrieval budget for every arm. Where the GraphMark
comparison above is attested against an unpublished bundle, this one's full methodology, every
retraction, and every quoted number's provenance are committed in this repository.

Measured 2026-08-14, on the retrieval path that first shipped in
[v0.4.0](https://github.com/entireio/entire-graph/releases/tag/v0.4.0)
([#104](https://github.com/entireio/entire-graph/pull/104)) — v0.3.0 and earlier score 91.56 on
the same benchmark, without it. Full before/after breakdown:
[`LOCOMO-COMPARISON.md` §3](../bench/memory/LOCOMO-COMPARISON.md#3-the-finding-we-were-structurally-unable-to-spend-the-retrieval-budget).

entire-graph ranks first at **94.74**; the margin over the strongest inference-built competitor
(mem0 OSS, 93.83) is **+0.91pp** and does not clear statistical significance (McNemar *p* = 0.125).
The margin over a BM25 lexical baseline built at identical zero cost — **+2.86pp at *p* =
5.7×10⁻⁷** — does, and is the one accuracy result in this comparison not in statistical doubt.
Index-time cost is directly metered, not estimated: entire-graph builds its index with **zero**
model calls against mem0's measured 50.85 million tokens, a cost paid again on every corpus
revision.

Full table, per-category breakdown, retractions, and reproduction steps:
[`bench/memory/LOCOMO-COMPARISON.md`](../bench/memory/LOCOMO-COMPARISON.md) ·
[run-by-run provenance](../bench/memory/RUN-INDEX.md) ·
[reproduce it yourself](../bench/memory/RUNNING-LOCOMO.md).

## Running

```sh
# Pin repo commits once (writes bench/repos.lock.json) — commit the result.
go run ./cmd/graph-bench -update-lock

# Fast tier — routine per-phase tracking (minutes):
go run ./cmd/graph-bench -manifest bench/repos.fast.json

# Full tier — all 24 languages x 10 repos (slow; includes mega-repos):
go run ./cmd/graph-bench

# Quick subset / offline:
go run ./cmd/graph-bench -languages Go,Rust -limit 3
go run ./cmd/graph-bench -skip-clone
```

For long runs, add `-progress` to print provider phase telemetry to stderr
without changing the JSON report. Guardrails can make local or CI runs fail
after writing the report:

```sh
go run ./cmd/graph-bench -profile syntax-only -languages Go -limit 1 \
  -min-loc-per-sec 50000 -max-rss-bytes 1000000000
```

`-cpuprofile` is intentionally rejected while per-repository process isolation
is mandatory. A parent-only profile would contain orchestration and waiting but
omit provider work in the child processes. Per-worker profile collection and
deterministic merging must be implemented before this flag can be enabled.

### Per-profile examples

Each profile measures the production streaming path at a different depth. Small
or medium runs make the trade-off visible:

```sh
# syntax-only — fastest; symbol inventory + structure only.
go run ./cmd/graph-bench -profile syntax-only -languages Go -limit 3

# fast — symbols, imports, shallow calls, boundaries, IaC; no deep relations.
go run ./cmd/graph-bench -profile fast -languages Go,Python -limit 5

# full — the complete relation graph (default).
go run ./cmd/graph-bench -profile full -languages Go,Python,TypeScript -limit 5
```

Read speed/throughput numbers from `fast` (and `syntax-only` for the floor), and
semantic-depth/coverage numbers from `full`.

Cloning (network) is a distinct phase from measurement, which runs the provider
with `NoNetwork` set, so the measured path is the same local-only path the
provider guarantees in production. The measured path is `StreamSnapshot` (the
production streaming path, memory-bounded), not the in-memory accumulator, so
large-repo runs do not OOM. Cloned repos live under `bench/.cache/` (gitignored)
and never enter our commits.

Pass `-profile full|fast|syntax-only` to measure a given indexing depth (default
`full`); the report records the profile, hardware (OS/arch/CPUs), and process
peak RSS.

The provider CLI also accepts `--progress` on `snapshot`, `symbols`, and
`edges`. Progress lines are written to stderr and include phase, file/symbol/
relation counts, heap, RSS, phase elapsed time, and total elapsed time; stdout
remains valid NDJSON.

### Cold-build phase boundaries

The benchmark reports four synchronous, process-local phases: `inventory`
(source preparation and file discovery), `parse` (header output, registration
alias indexing, and per-file file/symbol output plus index construction),
`relations` (relation resolution), and `finalize` (external sorting/output,
summary preparation, and summary serialization). These boundaries partition
provider cold work from snapshot entry through trailing-summary output exactly
once. Progress telemetry and the caller's synchronous callback are measurement
overhead, excluded from both phase duration and `wall_ms`; the CLI prints phase
shares using the sum of the four phases. Phase durations are process-local
performance diagnostics, not semantic provider schema fields.

Each repository is measured in a fresh child process. The worker captures that
repository's cold peak RSS and `wall_ms` before compact preflight, preventing a
preflight peak from contaminating the current cold values or any later
repository. After the measured interval, preflight serializes native NDJSON and
compact NDJSON, loads compact bytes through the production loader, verifies its
canonical semantic SHA-256 and decoded public projection against native output,
and performs exact symbol/relation queries through the production query index.
It adds worker runtime but never contaminates the cold wall/RSS measurement. Raw
compact bytes include headers, every dictionary line, data records, and the
summary; the dictionary count is a breakdown, not a subtraction.
Worker start/crash/protocol errors retain repository name, language, profile,
and error text in their report row. Error rows remain excluded from score and
throughput aggregates, but an RSS guard violation still fails the run after the
report is written using the captured cold RSS from every requested row.

## Comparing across phases

Both tiers pin commits via `bench/repos.lock.json`, so the source under analysis
is fixed and only the analyzer changes between runs. To compare two work phases:

1. Check out phase A, run a tier, keep its `bench/results/result-*.json`.
2. Check out phase B, run the same tier.
3. Diff the `by_language` and `totals` aggregates.

Quality regressions show up as shifts in the resolution/confidence distributions
or rising parse failures; performance regressions show up as falling LOC/sec or
rising wall time.

## Metrics

Every report includes the profile, hardware (OS/arch/CPUs), maximum successful
repository cold RSS, provider version, and schema version at the run level, and
per repository the relation set, languages, files/LOC, wall time, and output
size. Full breakdown:

Run-level: profile, hardware (OS/arch/CPUs), maximum successful repository cold
RSS, provider version, schema version. Per repository (and aggregated per
language and overall):

- **Performance:** wall time, `phase_ms`, files, lines of code, files/sec,
  LOC/sec, output bytes (of the streamed NDJSON), exact native and compact raw
  artifact bytes, compact dictionary bytes, bytes per projected fact, Go
  allocation bytes, profile, relation set.
- **Quality:** symbols, relations by type, resolution distribution
  (`exact`/`import_resolved`/`type_inferred`/`name_only`/`pattern`), confidence
  bands (`exact`/`strong`/`heuristic`/`weak`), parse-failure codes, unresolved
  relative imports.

The streaming path's only relation-count-scaled memory is the dedup set (one
compact 64-bit key per unique relation), so its entry count equals the reported
unique `relations` total — no separate dedup-count metric is emitted because
`relations` already measures it.

## Findings to date (historical, pre-streaming)

These observations come from **early runs that measured the in-memory path,
before the benchmark measured `StreamSnapshot` and before profiles existed**.
Treat the numbers as historical; re-run with the current streaming benchmark
(and a named profile) for current figures.

- **Route over-firing.** An early run showed gin emitting 1039 `HANDLES_ROUTE`
  edges — every path-like string literal counted as a route. Requiring routing
  context on the literal's line cut that to 206 (real registrations only).
- **C/C++ throughput used to be the floor.** Early C/C++ runs parsed at
  ~1.5–3.5k LOC/s because HEAD snapshots spawned `git show` once per file and
  C/C++ field symbols were emitted even though C/C++ field-access relations are
  not consumed. Current HEAD snapshots use one `git cat-file --batch` process,
  syntax-only skips relation-resolution indexes, oversized generated files emit
  `E_FILE_TOO_LARGE` instead of being parsed, and C/C++ field symbols are
  suppressed. A Linux syntax-only run on this branch measured ~235k LOC/s over
  38.2M LOC.
- **Syntax-only memory is compacted separately.** The syntax-only profile only
  emits `DEFINES` and `CONTAINS`, so it does not need to retain full
  `SymbolRecord` payloads after those records are streamed. A follow-on memory
  run over Linux kept the same 3.37M symbols / 3.37M relations while reducing
  peak RSS from ~5.82 GB to ~1.62 GB by retaining only structural symbol
  metadata for phase 2.
- **Peak memory scaled with repo size.** The in-memory snapshot accumulated
  every relation with its evidence and source contents, reaching ~20 GB RSS on
  tensorflow. The streaming path (`StreamSnapshot`, now the benchmark's measured
  path) emits records as produced and no longer holds full relation payloads,
  their evidence, or file contents in memory. Peak memory is instead bounded by
  the symbol/index metadata plus a compact relation dedup set (one 64-bit key
  per unique relation). That dedup set still scales with the count of unique
  relations — it is the remaining relation-count-scaled component — but at a
  tiny constant per relation rather than the full payload, which is what kept
  the in-memory path from finishing on the largest repos.

## Notes

- A full-tier run is heavy (the manifest includes linux, tensorflow, vscode,
  etc.); use the fast tier for routine tracking and the full tier occasionally.
- `internal/bench` is unit-tested (`MeasureRepo`, `BuildReport`) and needs no
  network, so the measurement logic is covered in CI; only the clone phase
  touches the network.
