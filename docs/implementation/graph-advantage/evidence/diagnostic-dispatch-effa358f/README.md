# Prepared effa358f delayed Kubernetes snapshot diagnostic

Dispatch `p1-diag-effa358f-k8s-late-relations-off-01` is prepared for exactly
one cache-OFF, full-profile, snapshot diagnostic against the frozen Kubernetes
input at commit `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe` and effective tracked-input
SHA-256 `d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`.
It uses evaluator source `effa358f2ceaca2b9accd829984272598fa78078`
and compiled binary SHA-256
`f2c2940a565397a010488843af99ff469c725dd54a720c90ed190311249ed1f6`.
The retained build manifest has SHA-256
`fcb17b1b279acbfd80bde9f753b1fcad0e6cae2aef55db88e55bb319a0cecb4c`
and is committed at `0007d4b50af1757b8502d621b7b0abc6f6424e8c`.

The full-check gate consumes immutable evidence committed at
`a54a0a6440e705690bc3efdf21f67b236c56a9a3`. It binds the exact evaluator
source to the successful Linux full check, its raw task log, and identical
before/after 2,512-entry Git `100644`/`100755` mode-and-content manifests with
SHA-256
`ac4cf87f80b0140d1e9b0ebb21c5a8350a90d7e2e3e22d74ad073b79a33ef267`.
The source archive SHA-256 is
`81c0ba6ba6f58c61270a6f70517b6ed23db806f1e64852ab5c69c9649a45fed3`.
That full check disclosed 52 skipped Go tests, including the ten opt-in live
compiler tests, and three statusline platform or ownership skips. The separate
pinned compiler run at `0007d4b50af1757b8502d621b7b0abc6f6424e8c`
passed the selected 28-test correctness set, including all ten live tests with
zero skips, then compiled this evaluator without executing it. These checks do
not claim full coverage.

The selected dispatch has one derived product invocation, zero preparatory
invocations, one OFF arm, and no ON arm, warm-up, retry, comparison, admission,
or stability sample. The evaluator deadline remains 120 seconds. The relations
CPU profiler is explicitly scheduled after 88,000,000,000 elapsed nanoseconds,
with a 20-second window, a 90,000,000,000-nanosecond latest-start bound, and an
8 MiB artifact limit. The collector builds a fresh runtime environment with
`ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_START_AFTER_NS=88000000000`,
so an ambient value cannot change the request.

A future result is acceptable only when the status reports a complete profile,
the exact 88,000,000,000-nanosecond request, and an actual start from
88,000,000,000 through 90,000,000,000 nanoseconds inclusive. The status also
retains the first and latest relation progress values and the number of relation
progress events. Repeated stack samples or progress values alone do not prove a
loop, and this diagnostic package makes no performance, timeout-cure,
admission, stability, release, or campaign claim.

The global batch ceiling remains 100, while this dispatch's effective attempt
cap is exactly one. The durable claim, active-service guard, fresh remote paths,
exact source and binary hashes, and control archive hashes remain fail-closed.
At this prepared-only boundary, reservation, product, and corpus invocation
counts are zero; `controller/dispatch-claim.json` is absent. No VM, cloud
upload, remote collector, evaluator, corpus, or benchmark was run. The prior
historical product invocation count therefore remains seven.

## Local preparation verification

The focused controller suite ran once:

```sh
python3 -m unittest -v controller/test_controller.py
```

It passed all eight tests in 0.987 seconds. The no-execute CLI preflight then
ran once:

```sh
python3 controller/controller.py
```

It exited zero with `preflight passed; no claim created`, reported the expected
dispatch ID and zero reserved product invocations, and left the durable claim
absent. The eight controller tests and control inputs are mechanically copied
from the reviewed `diagnostic-dispatch-950567a3` package and rebound only to
the source, binary, build, full-check, manifest-count, and dispatch identities
listed above. The independently authored delay cases still cover integer,
integer-equivalent float, boolean, ambient override, and out-of-window status
mismatches.

## Consulted sources

- Current source at commit `effa358f2ceaca2b9accd829984272598fa78078`.
- `docs/implementation/graph-advantage/evidence/diagnostic-dispatch-950567a3/`
  as the reviewed controller, collector, budget, gate, and test template.
- `docs/implementation/graph-advantage/evidence/check-effa358f-linux-full-01/`
  and `docs/implementation/graph-advantage/evidence/diagnostics-linux-effa358f-compiler-01/`
  as the retained full-check and compiler-build identities.
- `entire-plan/entire-graph-advantage-implementation-plan.md`
  and the authorized `graph-advantage-progress-review-2026-09-05.md` review.
