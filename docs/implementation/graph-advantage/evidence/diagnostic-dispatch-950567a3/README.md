# Seventh delayed Kubernetes snapshot diagnostic

Dispatch `p1-diag-950567a3-k8s-late-relations-off-01` ran exactly one
cache-OFF, full-profile, snapshot diagnostic against the frozen Kubernetes
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

The retained result reports a complete profile, the exact
88,000,000,000-nanosecond request, and an actual start at 88,012,954,726
nanoseconds, inside the required 88-to-90-second interval. The status also
retains the first and latest relation progress values and the number of relation
progress events. Repeated stack samples or progress values alone do not prove a
loop, and this diagnostic package makes no performance, timeout-cure,
admission, stability, release, or campaign claim.

The global batch ceiling remains 100, while this dispatch's effective attempt
cap is exactly one. The durable claim, active-service guard, fresh remote paths,
exact source and binary hashes, and control archive hashes remain fail-closed.
At the prepared-only boundary recorded below, reservation, product, and corpus
invocation counts were zero and `controller/dispatch-claim.json` was absent.
Those statements describe preparation before the single authorized execution.

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

## Retained terminal outcome

After the prepared package was committed and independently reviewed, the
authorized controller was executed once through an external wrapper whose
SHA-256 was
`a45eb9a9e64c354eb1e6f9fbce8afd3de2e31488b597396781781def63dabaa0`.
The wrapper and controller exited zero after retaining the remote result; the
collector exited one because the product request reached its 120-second
deadline. The product process started once, timed out, and exited `-9`.
The budget ledger records one reserved arm, one started arm, zero completed
arms, and one failed arm. This is historical product diagnostic invocation
seven. No retry, ON arm, warm-up, comparison, stability sample, admission, or
campaign was run.

Parsing ended and the relations phase began at 54.107255756 seconds with 30,866
files, 418,711 symbols, and the first observed relation count of 512. The CPU
profile began at 88.012954726 seconds and ran for 20.033042049 seconds. It
retained 41,212 bytes with SHA-256
`f1d82515bf50daee3caf05a30c0a37d81c8f3ca92b20f4baf77506f9ce8f253e`.
The status recorded 1,302 relation progress callbacks and a latest value of
666,624, but those counters are cumulative from the first relations callback
through profile completion. There is no counter at profile start, so they do
not establish how many relations were observed specifically during the
88-to-108-second profile window or prove a loop.

The before and after effective input SHA-256 remained
`d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`,
and the before and after tracked-manifest SHA-256 remained
`7112c36429cb59223135260d8119a88158edd65786633a0efca30b32a0a719d1`.
Post-request identity recorded no mismatch or error. GNU time did not retain a
valid peak-RSS value after the kill. The cleanup capture records all three VMs
as deallocated.

## Bounded offline profile attribution

Local `go tool pprof` was given the retained profile only. No executable was
supplied and no locally built substitute binary was used; function names,
lines, the `p1-evaluator` name, and build ID
`0070167e1b3ec43a4aec220634b1ec83b7f78467` came from the profile's embedded
metadata. The runtime bindings independently tie that profile to binary SHA-256
`623fd971be1b1c9813c98b8afeb1bb5b0bf5c43e8d3135f7e13b5bca3c33ec73`.
The source lists used the local tree only after confirming `internal/` is
unchanged from source commit `950567a3c2796c99c9f46af84f7c0a8afac3351d`.

The profile contains 30.66 CPU-seconds of samples over 20.02 seconds of wall
time. `forEachRelation.func2` accounts for 24.17 cumulative sample-seconds.
Regular-expression execution accounts for 21.70 cumulative sample-seconds in
`regexp.doExecute` and 19.78 in `regexp.allMatches`; backtracking accounts for
12.09. The largest project paths are `returnFlowCalls` at 9.79 cumulative
sample-seconds and `receiverCallRelations` at 7.03. Runtime GC drain accounts
for another 6.04 cumulative sample-seconds.

Within `returnFlowCalls`, the largest call lines are expression-assigned returns
at 2.21 sample-seconds, collection forwarding at 1.73, and object-field
forwarding at 1.61. Within `receiverCallRelations`, seven generic receiver,
constructor, and returned-chain scans total about 2.78 cumulative
sample-seconds; `localVarTypes` accounts for 0.98, imported receiver typing for
0.55, and candidate-filtered receiver resolution for at least 1.82. These are
sample attributions inside one late relation window, not elapsed-time savings
or evidence that any candidate change will cure the timeout.

The late profile overlaps the earlier profile in relation construction,
regular-expression work, `returnFlowCalls`, `receiverCallRelations`, and GC.
The windows start at different times and use different source, so their sample
amounts are not a performance comparison. The earlier profile also exposed
`assignmentFlowEvents`; its resulting return-match-first change remains covered
by the immutable full check but the seventh request still timed out.

Focused source inspection found eight top-level forwarding helpers that run the
same `flowCallSiteRe` over the same stripped body, while the ninth occurrence
scans a callback substring and is semantically distinct. Sharing the eight
ordered capture views through invocation-local lazy facts can preserve the
existing helper gates, loops, dedupe, and sorting, but only 0.76 sample-seconds
were attributed directly to four of those scan lines. It is therefore too small
to justify another full-check and runtime cycle alone. The broader observed
design opportunity is the set of independent whole-body regex passes per
function: return-flow helpers reconstruct assignment, object, collection,
alias, and call-site facts, while receiver resolution separately runs seven
generic chain patterns plus local, factory, and import typing scans. An
invocation-owned immutable analysis context is only a candidate design; it does
not remove unique regex passes by itself. A concrete eliminated-work map and
differential output strategy are required before claiming a structural fix. No
source implementation was changed during this diagnosis.

## Consulted sources

- Current source at commit `950567a3c2796c99c9f46af84f7c0a8afac3351d`.
- `docs/implementation/graph-advantage/evidence/diagnostic-dispatch-25887f69/`
  as the reviewed controller, collector, budget, gate, and test template.
- `docs/implementation/graph-advantage/evidence/check-950567a3-linux-full-01/`
  and `docs/implementation/graph-advantage/evidence/diagnostics-linux-950567a3-evaluator-build-01/`
  as the retained full-check and evaluator-build identities.
- `entire-plan/entire-graph-advantage-implementation-plan.md`
  and the authorized `graph-advantage-progress-review-2026-09-05.md` review.
- This dispatch's raw process, identity, budget, profile-status, CPU-profile,
  transport, claim, result archive, and terminal VM-state artifacts.
- Focused source in `internal/sem/types.go`, `internal/sem/provider.go`,
  `internal/sem/langcompat.go`, and `internal/sem/symbol_body.go`.

No competitor or comparison documents, prior transcripts, or memory were used.
