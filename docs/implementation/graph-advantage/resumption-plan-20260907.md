# Revised resumption plan — bounded P1 stability sampling

User-directed on 2026-09-07. This changes sequencing and adds an explicit
approval boundary. The user subsequently resumed the revised controlled sequence, including
 diagnostics and fixes, then explicitly paused all work on 2026-09-08. The
campaign is not currently running and release gates remain unchanged. It
supersedes earlier handoff/ledger suggestions that a canary pass can lead
directly to the full campaign. Historical protocols/results remain unchanged.
The full P1 campaign still requires explicit user approval and is NOT yet
authorized.

## 0. Mandatory model routing

Every substantive step is delegated to a cheaper-model worker: routine work to
Luna and complex work to Sol. This includes implementation, diagnosis,
source/data inspection, fixture and harness work, execution/testing, result
summaries, documentation, and Git, VM, or automation/control-plane execution.
The main Astra agent only decomposes and assigns work, coordinates workers,
reviews evidence and diffs, adjudicates results, chooses the next step, and
communicates with the user. Do not create Astra subagents or silently fall
back to Astra execution; if the required cheaper worker is unavailable, report
the worker unavailable and leave the step unexecuted.

Workers may execute otherwise authorized commands or VM tasks, but delegation
adds no budget or authorization. The shared 100-run batch cap, first-issue
stops, and explicit user approval before any full campaign remain in force.
The bounded immutable repository verification authorized after the user’s
resume is complete. The active implementation worktree is
`<REPO_ROOT>`, branch `codex/graph-advantage`,
tip `ef1df4ed`. Eight controlled product invocations have been consumed and
zero clean stability batches have run. The latest immutable full-check and
compiler-correctness evidence is at source `effa358f`; the latest diagnostic
timed out. One completion diagnostic without an arbitrary elapsed product
deadline is user-authorized in principle, but execution is paused until the
user resumes and the 14 GiB/`TasksMax=512` controls are implemented and
verified. Full-campaign approval remains absent.

## 1. Resolve known issues before sampling

Close the fixture-packaging immutable-check issue; retain its failed-state
evidence. Collect/review complete partial diagnostics and investigate the
outstanding snapshot timeouts with targeted reproductions. Fix concrete
problems and run affected correctness checks. Do not launch the complete
scenario matrix to rediscover known failures or regenerate unrelated evidence.
Required repository checks still apply when relevant source changes require
them; sampling is not a substitute for correctness.

## 2. Implement a hard batch budget before any sampled run

One batch permits at most100 product/evaluator invocations TOTAL across all
workers. A run is one request, not a Go unit test. OFF and ON each consume a
run, so100 permits at most50 comparisons. Cache-warming and other preparatory
product invocations also consume the same budget; incomplete pairs must never
be silently scored. Reserve the full known cost of a pair and its preparation
before dispatch. Metadata/priming/control overhead is separately logged and
bounded, never disguised as free product work.

The launcher must receive only the selected batch manifest, not the entire
17,280-request queue with an intention to interrupt it later. It must refuse
budget overflow and duplicate dispatch across workers. Keep existing resource,
lease and process-group limits. No next batch starts until its predecessor
has been collected and reviewed. Batch counters must count attempts, including
failures and interrupted requests, without an automatic retry budget.

These hard100-run and full-approval execution controls are PLANNED, not yet
implemented by this document. Existing scripts must not be launched unchanged
as though they enforce these new boundaries. Add bounded synthetic controller
tests before use; no real corpus campaign is needed to test accounting.

## 3. Sample deliberately, then inspect each batch

Freeze the selected request list, seed/order, source/binary/configuration and
input identities before each batch. Keep the original six repositories and
workload definitions; rotate samples rather than repeatedly choosing easy
passing cells. The first batch emphasizes historical timeout/partial/mismatch
cases. Across subsequent batches cover all three profiles, both verbs, all
six repositories and each mutation scenario, with fresh OFF/ON pairs and
alternating order where applicable. Explicitly distinguish marginal coverage
from full Cartesian coverage; sampling does not exercise every combination.
Retain the original parse-dominated membership and numerical thresholds.

Stop the whole batch at the FIRST timeout, process/control failure, novel or
unreviewed partial, semantic discrepancy, source drift, missing artifact or
measurement, lease expiry, or resource-limit violation. Retain the failure
and mark unscheduled requests unrun. Do not finish100 for the sake of a round
number. Diagnose/fix first, reproduce narrowly, then prepare a new identified
batch; never automatically replay a failed one.

Known partials remain visible as partial coverage. They are not automatically
acceptable because their codes were seen before. Their complete membership
and source-grounded classification must meet the separately adopted protocol.
ADR0049 remains proposed; this plan does not silently adopt it or relabel a
failure as success. Performance warning screens retain their existing meaning
and trigger diagnosis without being presented as statistical conclusions.

## 4. Stability decision, not a benchmark result

Initial target: three consecutive clean batches, each within100 runs, on one
unchanged product/harness/protocol configuration, with the coverage described
above. A relevant fix resets the clean-batch streak; old evidence remains.
Clean means no unexpected failures, unreviewed partials, identity/semantic
mismatches, missing measurements or worker-control issues; reviewed partial
coverage counts are reported separately. Resource behavior must remain within
bounds with any warning or unexpected drift explained.

After every batch report attempted/completed/paired/unrun counts, failure and
partial counts, new issue categories, coverage, elapsed time/resource use,
what changed and the reason for the next batch. Use deterministic summaries;
do not spend model turns reviewing thousands of repetitive rows.

Three clean batches are an engineering admission threshold, not a statistical
failure-rate bound or proof of performance. Repository/request dependence
precludes treating them as independent Bernoulli trials. If coverage or
stability remains insufficient after that checkpoint, identify the concrete
gap and justify the next100-run batch; do not queue open-ended sampling or
expand to the full matrix merely because several batches ran.

## 5. Full campaign requires the user's explicit approval

Once sampling supports stability, STOP and present the evidence, remaining
risks, exact full-run manifest/request count, expected runtime and resource/cost
estimate with its assumptions, and all active stopgaps. Ask for approval then.
No full-run approval has been granted. A clean canary, three clean batches,
prior broad autonomy, heartbeat instruction or existing goal is not approval.

Before implementing any full-launch path, require an explicit approval record
bound to the proposed source/binary, harness/protocol, corpus/workload manifest
and budget. Missing or mismatched approval refuses launch. Approval must come
from the user, not a generated record or inferred consent. Material scope or
configuration changes require renewed approval. First-issue stopping applies
throughout an approved full campaign as well.

Sampled diagnostic rows remain separate from formal release measurements;
do not silently pool selected or failure-conditioned samples into the final
benchmark dataset. Failed gates and experimental defaults remain unchanged.

## Pause and handoff

Planning documents and the bounded verification status were reconciled after
the explicit resume. Work is now paused at the user’s request; do not execute,
resume or heartbeat until the user resumes. The prepared completion-diagnostic
package is WIP and has not been reviewed, tested or launched. Preserve its
exact source, binary, input and control identities, plus pending test contracts;
do not alter protected untracked files. This resumption plan governs the
earlier shutdown handoff and the authorized but paused completion diagnostic.
