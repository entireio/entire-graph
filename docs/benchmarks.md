# Benchmarks

Reproducible performance and quality measurement for `entire-graph`, per v2-plan
WP10. The harness lives in `cmd/graph-bench` (driver) and `internal/bench`
(measurement core); see `bench/README.md` for layout and flags.

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

## External native-memory comparison

GraphMark's `memory-native-v2` suite evaluated this repository's candidate
commit `dea450b924e0ba4e9b5a7dc3ae5db72cf3aa857a` alongside Graphify and
Codebase Memory MCP. This is an external public-protocol reimplementation of
Graphify's advertised memory surfaces, not a `cmd/graph-bench` run and not a
reproduction of Graphify's unpublished harness.

Frozen product identities:

- Entire Graph candidate: `dea450b924e0ba4e9b5a7dc3ae5db72cf3aa857a`
- Entire Graph stable reference: `90a3346a624d76f7fee21bd894721e5438dd9ac2`
- Graphify v8/v0.9.34: `07b9143d4b90b1e1cb88dc71423f742a501efd29`
- Codebase Memory MCP v0.9.0: `b637e3330c96cfe452da623db068c241aaa3ec01`
- GraphMark harness: `09d14f2f149e8459d98b30ecf6bf1a31c757f04d`
- protocol SHA-256: `524e64ce2b0e8c888169d68d990466bed71336e2d7cd30b2723a78ba679fdb1b`
- full completion SHA-256: `30c8c19c24d7077abc28b30bb5853391b3f86700801f5e9a371acec0c1938a76`

The full run used 300 LOCOMO and 50 LongMemEval-S cases, three repetitions,
Kimi K3 reader/grader, a deterministic 20% Opus 5 audit, top 10, and a shared
128,000-byte context ceiling. It followed a sealed 1% validation and untouched
10% validity holdout; the full run was outcome-independent. Each product has
exactly 1,050 raw cells with identical case/question/repetition topology.

Entire Graph receives no benchmark-specific query rewrite. For each original
question the adapter executes `entire graph search --format json --top-k 10`
and gives the shared reader only the returned native snippets and neutral
locator headers. It does not open surrounding files after search. The build and
query cache is isolated from every other arm.

The verified full result is:

| Metric | Entire Graph | Graphify | cmm |
|---|---:|---:|---:|
| LOCOMO recall@10 | **0.918** | 0.844 | 0.000 |
| LOCOMO QA accuracy | 51.0% | **67.4%** | 22.3% |
| LongMemEval-S QA accuracy | 44.7% | **62.7%** | 6.0% |
| graph-build LLM credits | 0 | 0 | 0 |

Integrity record: 3,150/3,150/630 raw/grade/audit cells, zero invalid
attempts, promotion passed with zero findings, final artifact verification
passed, and Opus agreement was 97.94% (kappa 0.9583). Five transient raw-reader
timeouts and two transient grader timeouts were recorded and recovered within
the sealed retry policy; no audit retry occurred.

The publication bundle and review are in
[entirehq/graphmark#63](https://github.com/entirehq/graphmark/pull/63). Raw
artifacts remain outside Git because they occupy about 420 MB; the bundle
publishes their SHA-256 values and the exact reproduction contract.

## Notes

- A full-tier run is heavy (the manifest includes linux, tensorflow, vscode,
  etc.); use the fast tier for routine tracking and the full tier occasionally.
- `internal/bench` is unit-tested (`MeasureRepo`, `BuildReport`) and needs no
  network, so the measurement logic is covered in CI; only the clone phase
  touches the network.
