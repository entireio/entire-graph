# Evaluation manifest v1 (before optimization)

Baseline: 3a2a715fad1948e83dc7ebe0d307377ba29e065a.
Initial characterization: Entire-owned synthetic Go fixture in BenchmarkStreamSnapshotWorkerCounts, 600 files x 40 functions, syntax-only; one-worker and eight-worker arms, isolated invocation, 1 operation per sample, 3 samples. This is characterization only, not the P1 release comparison set. Record Go version, OS, CPU, source revision, command, raw timing/allocation and CPU profile. OS page cache is uncontrolled; do not call application-cold disk-cold.

Required release evaluation (not yet available): small/medium/large pinned repositories plus large synthetic stress, all profiles, cold/unchanged/one-edit/ten-edit/rename-delete/branch-switch/manifest-edit. At least 30 paired repetitions per scenario; alternate order. Exact semantic comparison excludes only explicit operational durations and callback telemetry, never warnings, evidence, failures or completeness. Retain failed runs in denominators. Median and p95 total/phase latency, peak RSS and disk use; paired bootstrap intervals with fixed seed 20260905 and 10000 resamples. Freeze parse-dominated subset from baseline profiles before optimization. Gates: >=25% one-edit median gain, <=10% cold and RSS regression, exact equivalence, zero unchanged eligible extraction reparses.

P2/P3: exact required/allowed/forbidden sites and candidate categories; macro and category precision/recall; empty denominators N/A. P3 path validity must be 100%, expected reachability complete within budgets, default output unchanged. Freeze independently reviewed depth-sensitive labels before quality evaluation; >=10% relative recall improvement, <=2 percentage points precision loss.

P4: current candidate-only scope initially; alpha .25, 25 iterations, residual 1e-8, graph blend .2; tune development only. Symbol/source-region relevance requires adjudication. Repository/task-family clustered uncertainty. Held-out and agent evaluation belongs in GraphMark, not this repository. No held-out collection, tuning, or promotion until split manifest, labels, byte budget, sample size and interval method are frozen there. Inconclusive evidence keeps current defaults.

Product-local paired harness v1 freezes 12/120/600 generated Go files with a
hand-authored predecessor call per file. Invocation is recorded in
extraction_evaluation_test.go; output requires O_EXCL so reruns cannot overwrite
observations. Three profiles, cold/unchanged/one-edit, 30 paired trials per case,
arm order alternated. Repository enumeration/resolution/serialization input
capture is included in each build. Executable identity is hashed once per test
process, so these are warm-process results; they cannot pass the cold-process
or peak-RSS gate. Complete record equality excludes only the additive
operational extraction telemetry. No parameter tuning follows these results.
