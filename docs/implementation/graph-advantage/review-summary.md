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
that route-regex work remains a CPU hotspot; a route refactor is in progress,
and no further sampling is authorized before the fix and its correctness
evidence. Zero clean stability batches have run, and no stability, release,
timeout-resolution or performance claim follows. The resumed invocation total
is five.

The historical P1 baseline contains 108 requests: 69 complete, 33 partial and
6 timeouts. It is therefore collected but incomplete. The campaign remains
paused; explicit unrun cells and failed observations remain retained. No P1,
P2, P3 or P4 release gate has passed.

| Workstream | Implementation and correctness | Evaluation and release status |
|---|---|---|
| P1 | Extraction, storage, relation-input, diagnostics, freshness and bounded-control code has focused fixture and race evidence. Retained profile and diagnostic outputs have exact parity where stated above; the latest immutable full check is the historical `450bede9` pass, `8689fc3d` has focused Linux evidence, and its final full check remains pending. | Fixed-corpus baseline and paired matrix are incomplete; timeout/partial admission, performance and RSS remain open. |
| P2 | Pinned Go analysis, lifecycle, source identity, mapping, compiler-view integration and invalidation contracts have implementation and focused correctness evidence. | Hard-Go quality and adjudicated precision/recall studies are deferred; no gate passed. |
| P3 | Relation policy, bounded traversal, path evidence, CLI/output and compiler-view contracts have focused correctness evidence. | Realistic affected-site precision/recall/cost studies and coverage gates are deferred; no gate passed. |
| P4 | Candidate-only ranking, experimental integration and fallback behavior have numerical and contract fixtures. The development-ablation interface is incomplete because current expansion still aliases current ranking instead of holding candidate expansion identical while ranking varies. | The expansion-control correction and focused proof are in progress. No winning configuration, held-out result or downstream agent study is established; no gate passed. |

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
shows route-regex work as a CPU hotspot; the route refactor and correctness
evidence must complete before any further sampling. No timeout-resolution
claim exists, and no new product invocation or full campaign is authorized by
this evidence.

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
