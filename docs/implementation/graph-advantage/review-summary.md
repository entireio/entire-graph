# Graph advantage implementation review summary

This summary supersedes the historical `88dd1dc9` checkpoint narrative. The
authoritative 23-task implementation, correctness, comparative-evaluation and
release-gate view is in [ledger.md](ledger.md). Historical claims below remain
labelled by their source checkpoint and are not current-head certification.

## Source and verification boundary

The latest completed immutable full check is `450bede9`: serialized
`mise run check` exited 0 in 1204.774 seconds at exact source
`450bede9970e05c994d80986cf105dd068e9fc29`, with 151 shell checks passed,
unchanged HEAD, clean before/after status and byte-equal tracked-source
manifests. It performed no product or corpus evaluation; evidence is retained
under `evidence/check-450bede9/`. The `90ac3e16`, `8c8b075d` and `1f20f694`
passes remain historical evidence. The first `90ac3e16` launcher failure on
`mise` trust remains retained separately as a non-test failure. The profiler harness at exact source
`90ac3e16b96c34ab219ecd2a90eca4600fd0c586` was reviewed and committed. Its
pinned Linux correctness run passed 28 compiler tests, including 10 live
tests, with zero compiler skips or failures; profiler normal passed 13 tests
plus the expected Windows-only skip, profiler race passed 12 tests plus the
same expected skip, and the evaluator build produced binary SHA-256
`43c85dfa31358f4911d565e005ddc943cf58d8ddc09790f60714e152e241b86b`.
Evidence was committed at `5db068ee` under
`evidence/diagnostics-linux-90ac3e16/`; it contains no product or corpus
invocation and supports no performance claim. Focused YAML/ABI checks for the
post-fix `12574522` source passed, including the seven-test ABI check reported
at 1.600s and 2.983s, and the pinned Linux compiler/evaluator check
and evaluator build passed with 28 top-level tests, including 10 live tests,
zero skips and zero failures. The local full `mise run check` at `12574522` is
incomplete: its first attempt ended with Darwin compile/cgo workers killed
before wrapper finalization, and the first authorized serialized retry also
ended before a terminal result. Retry 2 reported `Finished in 86.27s` followed
by `[test:ci] ERROR task failed` without a completed `test:ci` result; its
foreground controller sent Ctrl-C after a GitHub username prompt. This is a
task/transport failure, not a test assertion. Retry 2 retains the same
source/head and tracked-hash snapshots, but `result.json` is missing and
`postcheck_completed` is null, so those post-check files cannot be conclusively
attributed to wrapper completion. The observed before/after source snapshots
match, but retry 2's capture provenance is indeterminate; none is an immutable
source-pass or release claim. Evidence is retained under
`evidence/check-12574522/`, `evidence/check-12574522-retry-1/`,
`evidence/check-12574522-retry-2/` and `evidence/diagnostics-linux-12574522/`.
The offline fixture correction and focused normal/race proof that removed the
credential prompt are retained under
`evidence/diagnostic-graph-bench-offline-fixtures-12574522/`.
The earlier `6cf92c9c` full and
pinned-Linux checks remain historical evidence. Later accepted changes include
protobuf parse handling (`4cd72774`), Go new-expression arguments
(`c5971406`), Bash assignment-prefixed namespaced commands (`5ed08ed6`), YAML
multiline quoted scalars (`7c3405ae`), protobuf compatibility context
(`84ab23aa`) and harness phase breadcrumbs (`84cbb46`). The prior `8763b0d8`
full check remains retained as a separate failed source-state record.

The source-`6f23da0a` immutable full `mise run check` terminated failed at
`2026-09-07T16:39:00.860837Z` after 2334.829470 seconds with return code 1.
The checkout head and tracked-source hashes were unchanged before and after;
the `internal/sem` race suite failed after 1801.172 seconds. Raw evidence is
retained under `evidence/check-6f23da0a/`. This is a failed full check, not a
release or admission pass.

A second immutable check of the same source, with `MISE_JOBS=1`,
`GOFLAGS='-p=1 -v'` and `GOMAXPROCS=4`, also failed. It ran from
`2026-09-07T16:49:14.206582Z` to `2026-09-07T17:36:04.432858Z`
(`2810.177653s`); the cumulative `internal/sem` race run reached its 30-minute
timeout. Raw terminal evidence is retained under
`evidence/check-6f23da0a-cpu4/`; source and tracked state were unchanged. This
configuration change does not establish a cause, and no concurrency ladder or
further retry is authorized. The profiler worker is analyzing the existing
verbose log only; the 36-second and 6-second tests were the tests running when
the alarm fired, not isolated reruns or results, and the prior isolated
normal/race passes belong to different tests in the first failed check. Counts
remain five controlled product invocations and zero clean stability batches,
with all VMs off.

Two separate Linux cache-fixture infrastructure attempts did not reach tests. The original `evidence/check-6f23da0a-linux-cache-fixtures/` attempt stopped at the URL-substitution guard before tests; exit 73 is inferred from the guard path rather than observed. The `evidence/check-6f23da0a-linux-cache-fixtures-r1/` attempt observed remote exit 74 because the source directory or the temporary source archive was absent; retained evidence does not identify which. Transport and upload both recorded exit 0. Both attempts ran zero tests and made zero product or corpus invocations; the VM reached terminal deallocated state at the end of r1. The r2 durable-archive repair/check succeeded for its scoped cache-fixture validation: exactly three named tests passed with zero skips or failures in 2.190 seconds, using Go 1.26.1 and gopls v0.20.0; source-hash and source-comparison checks passed. mise was absent, so this is not a full `mise run check`, a timeout explanation, or a performance result. All three validation VMs were deallocated.

A separate Linux full-check attempt used the complete 2,059-file source archive and genuine unchanged `mise run check`. Its source, official checksum-bound mise 2026.4.11 and offline-linked Go 1.26.1 prechecks passed. Formatting, vet, the full race suite and build completed; `internal/sem` passed in 457.108 seconds. The overall mise run lasted 764.628 seconds; the required status-line task failed with 62 assertions passed, 78 failed and three platform/ownership checks skipped, so the full check failed and status-line coverage was incomplete. Its separate task duration was not recorded. Pre/post tracked source identity was unchanged. The retained output does not prove the environmental cause of the empty renders and missing cache artifacts. No retry, product or corpus invocation followed, and all three validation VMs were deallocated. Evidence: `evidence/check-6f23da0a-linux-full/`.

## Current status

The revised resumption controls are implemented at `58f03a22`: each selected
batch derives and reserves its preparation and arm costs under the shared
100-invocation cap, uses durable worker claims, refuses duplicate or retry
dispatch, and stops on the first issue. The 90 synthetic-only controller tests made no diagnostic, sampling, VM or
product request. Full-campaign execution remains explicitly approval
bound, and no such approval is recorded.

The retained P1 query evidence at `1c0b8e24` covers three profile paths with
exact semantic, warning, completeness and 11-record partial-output parity.
The bounded Kubernetes syntax-only diagnostic separately collected 194
known partials and one warning. All 194 entries have been classified in the
source-review packet, but that classification does not close parser defects,
verify every current file, or adopt proposed ADR0049 reviewed-partial
admission. The resumed sequence has consumed five controlled product
invocations: that completed syntax-only diagnostic and the four full-profile
timeouts described below. Zero stability batches have run. The first full-profile OFF dispatch attempted
transport once but failed before product startup because the packaged collector
still contained placeholder constants (`consumed=0`, `reserved=1`); it is
retained at `evidence/diagnostic-dispatch-12574522/` with archive hash
`ed3201385eed9691eb4e21cecd826ad6354626f5e98c1c0dc936853d568b044c`. A second
dispatch consumed one product attempt and timed out at 120 seconds with
process exit `-9` and no observation or diagnostics; its archive hash is
`1a7b81a13e971513546e1beccb38d147696fe7400e1e26834b267a9a6d844850` under
`evidence/diagnostic-dispatch-12574522-r2/`. Neither is a stability batch or
performance result, neither was retried, and the r2 relation progress count
of 512 is explicitly non-final. A new collector/package review remains
pending, the timeout remains unresolved, and no new corpus result exists.
The independently identified `diagnostic-dispatch-90ac3e16` then consumed
exactly one cache-off full-profile snapshot invocation and timed out at 120
seconds (`process=-9`, `collector=1`) with no observation or diagnostics. It
retained a complete diagnostic-only 20-second relations CPU profile, SHA-256
`3f9b2fbe80daff71816e2d6cf2e8d66db0203cda5b08aca758aaa88a47cab88b`,
with unchanged before/after corpus identity and zero control-identity
mismatches. The VM is deallocated. The retained profile is diagnostic evidence
only and establishes no performance result or stability evidence.

Offline analysis of that retained profile identified unconditional Go
route-regex scans as a CPU hotspot. Source
`450bede9970e05c994d80986cf105dd068e9fc29` adds a route-candidate prefilter;
its pinned Linux evidence passed 28 compiler tests including 10 live tests,
the focused route normal/race and profiler checks, and the evaluator build.
The built binary SHA-256 is
`b4d7e1a5ef72d4e09d045e562ce08cb38ff63a9c3912aef0e002bc70bb5f0674`,
and evidence was committed at `6e050656` under
`evidence/diagnostics-linux-450bede9/`. This establishes correctness and build
status only; it does not establish a speedup or resolve either timeout. The
post-prefilter `diagnostic-dispatch-450bede9` then consumed exactly one
cache-off full-profile snapshot invocation and also timed out at 120 seconds
(`process=-9`, `collector=1`) with no observation or diagnostics. It retained
a complete diagnostic-only 20-second relations CPU profile, SHA-256
`8bec6b6a1d1a4d83ac1a3ba1c9addff49afbfefa840def5a7b240c54e86521c6`,
with unchanged before/after corpus identity and zero control-identity
mismatches; the VM is deallocated. The first route prefilter did not resolve
the timeout.

Source `8689fc3d790e4290ce2b62500d270c96c02087b3` adds a route-token boundary
correction. Its pinned Linux affected normal and race stages each passed 10
tests, and compiler correctness passed 28 tests including 10 live tests, with
zero skips or failures. The evaluator build produced binary SHA-256
`259a56073fb54e74f2d1d60a3a6a98d87a7eec1c992f86a0c665a35c3cdb9559`;
evidence is retained under `evidence/diagnostics-linux-8689fc3d-r1/`. This is
focused correctness and build evidence. No immutable full check exists for
`8689fc3d`; the latest completed immutable full check remains the historical
`450bede9` run. The diagnostic preparation was committed and pushed at
`ad41af6fb21c0b046d3f11716683990954d2ef3d`.

The accepted focused-only `diagnostic-validation-v1` gate then authorized
exactly one `diagnostic-dispatch-8689fc3d` cache-off full-profile snapshot. The
request started and timed out at 120 seconds (`process=-9`, `collector=1`) with
no observation or diagnostics. It retained a complete diagnostic-only
20.027-second relations CPU profile, SHA-256
`2c72e17b6089a06fb0fb991a68c30498d407c88ad5baf3777121d0f78b870776`,
with byte-identical before/after inputs and zero control-identity mismatches.
No retry was made and the VM is deallocated. The focused gate is not a full
check or admission substitute. Offline analysis of the latest profile found
that route-regex work remains a CPU hotspot. The route refactor and focused
correctness evidence are now complete, but controlled runtime verification
remains pending; no further sampling is authorized before that run. Zero clean
stability batches have run, and no stability, release, timeout-resolution or
performance claim follows. The resumed invocation total is five.

The source-`6f23da0a` pinned Linux evidence is separate from the failed full
check: 32/32 affected normal, 32/32 affected race, and 28/28 compiler
correctness tests passed, including 10 live tests; evaluator binary SHA-256 is
`9562a0f5ee9558a0c6e7533ad7cfa6511251e52588e976cf09ee86564fa4966d`.
Evidence is under `evidence/diagnostics-linux-6f23da0a/` and establishes only
the named focused stages. It does not repair the full-check failure or open
diagnostic packaging/runtime execution.

Source `78c8b496` implements the bounded Go HTTP route-parser refactor
motivated by the retained relations profile. Focused normal, race,
compatibility-oracle and resource-bound checks are recorded in
`evidence/route-parser-profile-8689fc3d/`. This is implementation and
correctness evidence only; the controlled runtime timeout remains unresolved
pending focused diagnosis, and diagnostic packaging/runtime execution is
blocked.

The historical P1 baseline contains 108 requests: 69 complete, 33 partial and
6 timeouts. It is therefore collected but incomplete. The campaign remains
paused; explicit unrun cells and failed observations remain retained. No P1,
P2, P3 or P4 release gate has passed.

| Workstream | Implementation and correctness | Evaluation and release status |
|---|---|---|
| P1 | Extraction, storage, relation-input, diagnostics, freshness and bounded-control code has focused fixture and race evidence. The route refactor at `78c8b496` has focused normal/race/oracle/resource evidence; source `6f23da0a` has separate pinned Linux focused success, while its immutable full check failed in `internal/sem`. | Fixed-corpus baseline and paired matrix are incomplete; timeout/partial admission, performance, RSS and full-check correctness remain open. |
| P2 | Pinned Go analysis, lifecycle, source identity, mapping, compiler-view integration and invalidation contracts have implementation and focused correctness evidence. | Hard-Go quality and adjudicated precision/recall studies are deferred; no gate passed. |
| P3 | Relation policy, bounded traversal, path evidence, CLI/output and compiler-view contracts have focused correctness evidence. | Realistic affected-site precision/recall/cost studies and coverage gates are deferred; no gate passed. |
| P4 | Candidate-only ranking, experimental integration, fallback behavior and one bounded candidate-expansion policy are implemented. `current-expansion`, `uniform` and `weighted` use identical no-compiler candidate pools while `current-expansion` retains baseline ranking; compiler evidence may change the eligible pool in `weighted-compiler`. Focused normal/race contract evidence is retained. | No new retrieval measurement or tuning was run. No winning configuration, held-out result or downstream agent study is established; no gate passed. |

“Complete” in the ledger means implementation or harness delivery. It does
not mean a comparative result or release decision. Existing failures,
partials, timeouts and unrun cells remain part of the evidence record.


## Functional limits and rollback

Compiler execution has Linux-only evidence; dynamic targets and unsupported
external dependency closure remain explicit partial/unavailable or candidate
results. Captured inputs are observed bytes rather than an atomic worktree
revision. The capture memory bound excludes total process RSS, raw relation
reuse covers imports only, and Windows locking has compile-only evidence. A
known Bash coverage limitation remains: the parse view preserves nested
command syntax, but the existing relation scanner does not report calls inside
some quoted-local assignments; grammar-clean controls show the same limitation.

Rollback remains `--extraction-cache off`, `--compiler off`, `--depth 2`, and
`--ranking current`. Disposable extraction records occupy a separate cache
namespace; no migration is required. Revert the reviewable implementation
commits to remove the features. Installation, MCP packaging, generated
summaries and Brain remain untouched.

## Required next decisions

Before any full campaign, preserve a new source, binary, corpus, workload,
environment and protocol freeze; finish the incomplete baseline and review
remaining partial/timeout evidence; and obtain explicit user approval bound to
that freeze. Any bounded sample must continue using the cheaper-worker routing
policy, the shared 100-invocation cap, durable duplicate/retry prevention and
first-issue stop. Three clean representative batches are a checkpoint for
stability only, not a release gate.

The existing timeouts remain failed diagnostics. The latest profile still
shows route-regex work as a CPU hotspot; the route refactor and focused
correctness evidence are complete, but controlled runtime verification is
still required before any further sampling. No timeout-resolution claim
exists, and no new product invocation or full campaign is authorized by this
evidence.

P2 and P3 still require independent adjudication of required, allowed and
forbidden targets with explicit partial/unavailable coverage. P4 requires a
frozen development comparison, a disjoint held-out evaluation and only then a
conditional agent study. Defaults remain extraction reuse off, compiler off,
impact depth two and current ranking.

## Scope and provenance

The implementation plan, the authorized interim review, repository source and
tests, schema/trust-contract ADRs, resumption plan and retained evidence are
the sources for this summary. No competitor implementation, prior
conversation, memory file or adjacent comparative corpus is used as
implementation evidence. The validation VM is deallocated and no campaign is
running.
