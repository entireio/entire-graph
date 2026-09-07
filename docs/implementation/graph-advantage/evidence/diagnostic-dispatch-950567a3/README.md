# Prepared 950567a3 delayed Kubernetes snapshot diagnostic

Dispatch `p1-diag-950567a3-k8s-late-relations-off-01` is prepared for exactly
one cache-OFF, full-profile, snapshot diagnostic against the frozen Kubernetes
input at commit `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe` and effective tracked-input
SHA-256 `d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`.
It uses evaluator source `950567a3c2796c99c9f46af84f7c0a8afac3351d`
and binary SHA-256
`623fd971be1b1c9813c98b8afeb1bb5b0bf5c43e8d3135f7e13b5bca3c33ec73`.
The retained evaluator build manifest has SHA-256
`50c8936cef23e5130c54ca27d5c78688898dc076dff16a8e55a54b890157913d`
and was committed at `978190c35ace88abb6d7255f190a8b4f98c23259`.

The full-check gate consumes immutable archive-native evidence committed at
`85be48c6d49cd60254c8452c2ddf42bd68bc37b7`. It binds the exact evaluator
source and binary to the successful Linux full check, its raw task log, and
identical before/after 2,398-entry Git `100644`/`100755` mode-and-content
manifests with SHA-256
`7b5854a5927df8968d82df143125234538dc86be411f385f564590e49d8a9371`.
The source archive SHA-256 is
`dfe4b484b98535d0b9c35b6bb0b524352ddb93e61f8bde391aa6663db4c2f91b`.
That full check disclosed three platform or ownership skips and did not claim
full coverage.

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
historical product invocation count therefore remains six.

## Local preparation verification

The first package test run passed seven of eight tests and exposed unexpected
macOS AppleDouble `._*` archive members. The control archive was rebuilt with
`COPYFILE_DISABLE=1`. The next run again passed seven of eight tests and exposed
that the archived pin validator treated the new numeric delay pin as though all
pins were strings. The corrected validator requires every archived source,
binary, and delay pin to have the exact expected type and value. Both failures
were local preparation checks and created no claim or product invocation.

The corrected focused suite was then executed once:

```sh
python3 -m unittest -v controller/test_controller.py
```

It passed all eight tests in 1.094 seconds. The independently authored delay
fixtures cover a changed integer, an integer-equivalent float, a boolean, an
ambient override attempt, and out-of-window complete-profile status. The other
seven controller cases and the control inputs were mechanically copied from the
reviewed `diagnostic-dispatch-25887f69` package and rebound to the identities
listed above.

The no-execute CLI was then run exactly once:

```sh
python3 controller/controller.py
```

It exited zero with `preflight passed; no claim created`, reported the expected
dispatch ID and zero reserved product invocations, and left the durable claim
absent.

## Consulted sources

- Current source at commit `950567a3c2796c99c9f46af84f7c0a8afac3351d`.
- `docs/implementation/graph-advantage/evidence/diagnostic-dispatch-25887f69/`
  as the reviewed controller, collector, budget, gate, and test template.
- `docs/implementation/graph-advantage/evidence/check-950567a3-linux-full-01/`
  and `docs/implementation/graph-advantage/evidence/diagnostics-linux-950567a3-evaluator-build-01/`
  as the retained full-check and evaluator-build identities.
- `entire-plan/entire-graph-advantage-implementation-plan.md`
  and the authorized `graph-advantage-progress-review-2026-09-05.md` review.
