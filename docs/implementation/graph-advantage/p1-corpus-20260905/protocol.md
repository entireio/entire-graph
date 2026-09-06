# P1 corpus campaign protocol (2026-09-05)

Status: preregistered protocol. No observations are included here, and no
campaign has run under this protocol.

This document fixes the comparison before any result is inspected. It covers
the extraction-reuse P1 gate for the `snapshot` and `search` verbs. Product
defaults, compiler resolution, and graph ranking are held at `--compiler off`
and `--ranking current`; this campaign does not evaluate those features.

## Workload and accounting

The campaign has six pinned repositories, three profiles (`syntax-only`,
`fast`, and `full`), and these eight scenarios:

1. `cold`: an empty extraction cache, with the operating-system page cache in
   the fixed warm regime described below;
2. `unchanged`: the same captured inputs queried again without an edit;
3. `one-edit`: one function body changed;
4. `ten-edit`: ten independently selected function bodies changed;
5. `rename`: a selected source file renamed while its contents are preserved;
6. `delete`: a selected source file deleted;
7. `branch-switch`: the requested branch/ref changes between queries; and
8. `manifest-edit`: a package/module/workspace manifest changes while a
   caller's source bytes remain unchanged.

Each repository/profile/scenario/verb cell has 30 paired repetitions. A pair
has one baseline (`reuse=false`) and one treatment (`reuse=true`) request, and
the order alternates by trial: baseline first on even trials and treatment
first on odd trials. The two requests in a pair use the same binary, captured
inputs, query, scope, output budget, compiler/ranking settings, and resource
limits. The pair differs only in the extraction-reuse setting. A repeated
request is a new process invocation where the harness says so; process reuse
must be recorded rather than inferred.

The planned measured request count is:

```
6 repositories x 3 profiles x 8 scenarios x 30 pairs x 2 arms x 2 verbs
= 17,280 measured requests
```

Warm-up and fixed page-cache preparation are additional and are not included
in this count. They must be logged separately. A runner may need hours and
material temporary storage; reducing repetitions, repositories, profiles,
verbs, or resource measurements requires a new protocol and a new decision.

The binary and input manifests are frozen before the first paired request.
The run manifest records source revision, executable digest, toolchain, OS and
architecture, repository/input digests, profile, scenario definitions, query
and output settings, and the protocol version. Every request records its
repository, profile, scenario, verb, trial, arm, status, error, elapsed and
wall time, process-specific peak RSS, cache disk bytes, semantic output digest,
source digest, extraction telemetry, phase telemetry, and partial-failure
count. The scorer accepts the flat JSON/NDJSON shape in `summarize.py`; unknown
extra fields are retained by the raw artifact and ignored by the scorer.

## Cache and filesystem regime

“Cold” means the application extraction cache is empty. It does not mean a
disk-cold or empty-OS-page-cache filesystem. Before every measured request,
the harness performs the same fixed prime read over the captured inputs and
records its completion. The prime sequence, byte ranges, and order are fixed
in the manifest. This is a controlled warm-OS-page-cache regime; no result may
be described as disk-cold.

The extraction cache is cleared only at the declared boundary for a `cold`
scenario. Cache clearing is operation-scoped and must not remove unrelated
user data. Cache bytes are measured from the cache namespace owned by the
request/repository, with the accounting root and before/after snapshots in the
manifest. Temporary files and cache entries are distinguished. Cache disk
accounting is reported separately from source bytes read and process RSS.

Peak RSS is process-specific and is measured for the request process (and its
documented children, if the platform collector includes them), with the
collector and sampling limitations recorded. Harness, runner, and prime-read
overhead are not silently subtracted. They are a stated limitation and are
reported alongside the request value. RSS is not inferred from Go allocation
telemetry.

The two arms use identical binary inputs and the same fixed prime procedure.
No live filesystem mutation is permitted during a pair. A source or policy
digest mismatch invalidates the pair for comparison, but the raw rows remain
in the denominator and in the failure report.

## Baseline phase profiling and selection

Before collecting paired comparisons, the campaign runs a separate baseline
phase profile on unchanged inputs for every frozen repository/profile/verb
combination (108 requests total). Snapshot membership is shared across the
subsequent scenarios. It uses three cache-off repetitions per cell and summarizes phase
times by their medians. The current search harness has no equivalent parser
phase hook, so `search` parse-dominated membership is unavailable by protocol;
search is still measured and reported in full, but it cannot enter the 25%
one-edit gate. The profile is used to preregister the snapshot subset; it is
not selected after looking at treatment gains, and paired observations cannot
alter the membership list.

For each declared snapshot cell, the broad `phaseParse` category (read,
classification, and declaration extraction) is parse-dominated when its median
baseline phase is at least half of median baseline total time:

```
extraction_share = median(baseline_phaseParse_ns) / median(baseline_total_ns)
parse-dominated iff extraction_share >= 0.50
```

The frozen manifest contains the phase values, the computed share, and the
resulting snapshot membership. `phaseParse` is a broad instrumented category;
it is not claimed to be pure parser CPU. Invalid or missing baseline phase data
leaves the cell `evidence_incomplete`; it cannot be admitted because its
observed gain looks favorable. The scorer reports every cell, including the
non-dominated snapshot cells with `parse_dominated=false` and search cells with
`parse_classification=unavailable`. The 25% one-edit gate is evaluated over the
complete preregistered snapshot parse-dominated one-edit set.

## Measurement and failure handling

The per-request timeout is 120 seconds. A timeout, process failure, malformed
response, partial result, missing digest, or resource-collector failure is a
retained observation. It is never dropped to make a pair scoreable. A valid
successful pair for latency requires both arms to have status `ok` (the scorer
also accepts equivalent success spellings), finite positive total time, and
matching input/source identity. Failed requests remain visible in group counts
and the error list.

Semantic equivalence is exact. For each successful pair, the canonical native
output is hashed after excluding only explicitly operational fields such as
elapsed time and callback telemetry. Warnings, evidence, failures,
completeness, ordering where promised, and all semantic records remain in the
digest. A missing or mismatched `semantic_digest` fails the equivalence gate;
it is not converted to an empty output.

The no-stale-source gate requires an explicit false stale-source indication on
all successful rows. The runner may set that field false only after verifying
that the pair's captured source stayed unchanged and its semantic digest
matches the fresh extraction-cache-off baseline; it must not hardcode false
before that comparison. The unchanged-reparse gate applies to eligible cacheable
files only. Each successful `unchanged` row therefore carries an explicit
numeric `unchanged_eligible_reparses` (the scorer also accepts the equivalent
`unchanged_reparses`) and eligibility accounting. `files_parsed` alone is not
that value: it can include intentionally uncacheable resources or transient
failures. If eligibility accounting is missing, or any uncacheable/failed
file prevents determining the eligible denominator, the gate is evidence
incomplete rather than a blanket failure. A missing reparse field is never
converted to zero.

Zero denominators are reported as `N/A` with a reason. They are never changed
to zero, one, or a passing percentage. “Inability” means the harness could
not run or measure the requested operation under the declared limits (for
example, a timeout or unavailable RSS collector). “Evidence incomplete” means
the run exists but lacks a required identity, phase, digest, or metric. Both
states are distinct from a measured gate failure.

## Statistics

The primary latency statistic is total request time (`elapsed_ns`), reported
as p50 and nearest-rank p95 in milliseconds for each arm and group. Cache
bytes, peak RSS, source bytes, phase times, and reuse/parse counts are reported
separately. All groups and all statuses are included in the summary.

For each valid paired group, uncertainty uses paired bootstrap resampling of
the pair indices, with seed `20260905` and 10,000 resamples. Each resample
computes the p50 and p95 treatment-to-baseline benefit:

```
benefit = 1 - statistic(treatment) / statistic(baseline)
```

The summary reports the observed p50/p95 benefit and the deterministic 95%
bootstrap percentile interval. The same pair is selected for both arms in a
resample; arms are never bootstrapped independently. The scorer's
`--bootstrap-resamples` option exists for small local tests, but release
scoring uses the preregistered value.

The campaign reports all 8 scenarios, all profiles, both verbs, every
repository, successful counts, retained failures/timeouts, zero denominators,
and phase classification. The one-edit gain and cold/RSS comparisons are
never reported only for a favorable subset.

**Historical execution rule:** the paragraph below documents the original run. For every new run, [stopgaps-v2.md](stopgaps-v2.md) supersedes it with a first-issue whole-run pause and all-worker supervision. The original observations and release thresholds remain unchanged.

The runner has a safety circuit breaker for operational failure. In each
repository/profile/verb stratum it tracks consecutive timeout/process-failure
counts separately for the `reuse=false` and `reuse=true` measured arms, plus a
separate count for hard cache-preparation failures. A successful request resets
the counter for its arm; a partial provider result is retained as an observed
partial result and does not count as a process failure for this breaker. Three
consecutive failures in any one of those counters stop the whole stratum. A
hard preparation timeout/process failure marks the planned pair unrun; a
partial warm result may continue to the measurement, with eligibility and
completeness reported by the scorer. Failed rows already observed are
retained. Every remaining planned request is explicitly recorded as `unrun`;
it is not synthesized as a timeout row and is not counted as a measured
request. The breaker never fires for a performance regression, and it does not
reduce the preregistered 30-pair requirement when the stratum is healthy.

## Gates and decision states

The release gate is a conjunction over the fixed comparison set:

- exact semantic equivalence for every valid pair, with no stale-source case;
- zero unchanged eligible reparses in every valid unchanged treatment row;
- at least 25% lower median total time over the complete preregistered
  parse-dominated one-edit set, with its paired bootstrap interval reported;
- no more than 10% median total-time regression in any valid cold cell; and
- no more than 10% median process-specific peak-RSS regression in any valid
  cold cell.

The summary emits `pass`, `fail`, `evidence_incomplete`, `inability`, or
`not_applicable` per gate and overall. A correctness failure is a gate
failure. Missing required data cannot pass by treating the missing value as
zero. An inability or incomplete evidence keeps the result undecided and does
not establish the P1 advantage. Earlier measurements under another source,
binary, corpus, cache regime, or protocol are not substituted for missing
observations.

If the gates pass, the artifact is still a measurement report: it does not
authorize changing defaults by itself. If a gate fails or the evidence is
incomplete, extraction reuse remains disabled for release and the report
retains the diagnosis. No tuning, corpus replacement, or repetition reduction
is allowed after observing results.

## Reproduction artifact

The raw NDJSON, run manifest, baseline phase-profile manifest, binary/input
digests, runner logs, and scorer JSON are retained together. `summarize.py`
is pure post-processing: it does not query repositories, mutate caches, or
rerun requests. Its tests use synthetic raw rows only to exercise pairing,
bootstrap determinism, gate failures, timeouts, zero denominators, and missing
evidence. They are scorer tests, not benchmark observations.

## Fixed worker limits

Three identical Ubuntu 22.04 Standard_D4s_v5 workers each provide four vCPUs.
Each worker runs requests serially with `GOMAXPROCS=4`, a 14 GiB systemd
cgroup memory ceiling (runner plus child), `TasksMax=512`, and the 120-second
child deadline. Git is 2.55.0 on all workers. The evaluation executable is
non-race and shared byte-for-byte; this is production API plus native JSON
serialization timing through a test harness, not distributed CLI timing.
The source corpus archive includes self-contained Git objects and fixture
markers. Private blob transfers are preparation/collection, outside measured
provider execution. Worker resources are deallocated after collection.

An unchanged row with zero total parsed files proves zero eligible reparses.
For nonzero parses, the current telemetry cannot separate ineligible files;
the eligible-reparse value is unavailable, keeping that gate incomplete.
