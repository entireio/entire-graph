# Graph advantage implementation review summary

This summary supersedes the historical `88dd1dc9` checkpoint narrative. The
authoritative 23-task implementation, correctness, comparative-evaluation and
release-gate view is in [ledger.md](ledger.md). Historical claims below remain
labelled by their source checkpoint and are not current-head certification.

## Source and verification boundary

The latest completed immutable full check is `8c8b075d`: serialized
`mise run check` exited 0 in 1265.559 seconds at exact source
`8c8b075d95503b931c7e9fa3bf2838e22c0f4be1`, with unchanged HEAD, clean
before/after status and byte-equal tracked-source manifests. It performed no
corpus measurement or VM work; evidence is retained under
`evidence/check-8c8b075d/`. The earlier `1f20f694` pass remains historical
evidence. Focused YAML/ABI checks for the
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
The single bounded Kubernetes syntax-only diagnostic separately collected 194
known partials and one warning. All 194 entries have been classified in the
source-review packet, but that classification does not close parser defects,
verify every current file, or adopt proposed ADR0049 reviewed-partial
admission. That one invocation remains the only resumed corpus diagnostic;
zero stability batches have run. The first full-profile OFF dispatch attempted
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
pending.

The historical P1 baseline contains 108 requests: 69 complete, 33 partial and
6 timeouts. It is therefore collected but incomplete. The campaign remains
paused; explicit unrun cells and failed observations remain retained. No P1,
P2, P3 or P4 release gate has passed.

| Workstream | Implementation and correctness | Evaluation and release status |
|---|---|---|
| P1 | Extraction, storage, relation-input, diagnostics, freshness and bounded-control code has focused fixture and race evidence. Retained profile and diagnostic outputs have exact parity where stated above, and the current immutable full check passed. | Fixed-corpus baseline and paired matrix are incomplete; timeout/partial admission, performance and RSS remain open. |
| P2 | Pinned Go analysis, lifecycle, source identity, mapping, compiler-view integration and invalidation contracts have implementation and focused correctness evidence. | Hard-Go quality and adjudicated precision/recall studies are deferred; no gate passed. |
| P3 | Relation policy, bounded traversal, path evidence, CLI/output and compiler-view contracts have focused correctness evidence. | Realistic affected-site precision/recall/cost studies and coverage gates are deferred; no gate passed. |
| P4 | Candidate-only ranking, experimental integration, ablation controls and fallback behavior are implemented with numerical and contract fixtures. | No winning configuration, held-out result or downstream agent study is established; no gate passed. |

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
