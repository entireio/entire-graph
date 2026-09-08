# Authorized Kubernetes completion diagnostic

Status: paused by user on 2026-09-08; existing authorization does not resume
execution. The user-authorized one-time completion diagnostic has its
execution-control correction pending with `p1_diagnostic_prepare`.
This document authorizes no execution by itself and does not change the frozen
P1 campaign protocol or approve a full campaign.

## Authorized scope

Run one separately versioned, non-admission completion diagnostic using the
same cache-OFF, full-profile Kubernetes snapshot and a new dispatch ID, claim
and output directory. It is not a retry of the eighth invocation. The user has
authorized completion without an arbitrary product deadline; the diagnostic
must stop on the first actual issue, require progress monitoring that does not
terminate or cancel ongoing work, and may not add retries, comparisons or
campaign expansion.

The existing campaign deadline remains 120 seconds for campaign measurements. The preregistered protocol
defines that per-request limit and retains timeouts as failed observations
(`p1-corpus-20260905/protocol.md:117-125`). The active stopgap calls the 14 GiB
cgroup, 512-task limit and 120-second request deadline the fixed absolute
campaign boundaries (`p1-corpus-20260905/stopgaps-v2.md:61-64`). A result from
this proposed diagnostic therefore cannot replace, relabel or enter the
campaign's baseline, canary, stability or release measurements.

The first-issue rule remains active. The resumption plan stops a batch on the
first timeout, process/control failure, partial, identity drift, missing
artifact or resource measurement, and forbids automatic replay
(`resumption-plan-20260907.md:78-83`). The current accepted diagnostic decision
also permits only one OFF/full/snapshot invocation, consumes that invocation on
timeout and grants no ON arm, warm-up, retry, comparison or campaign expansion
(`diagnostic-validation-policy-proposal.md:54-63`). The eighth claim is
consumed, so this separately authorized completion diagnostic must use a new
package and preserve the non-admission boundary.

## Immutable inputs and execution boundary

The proposed diagnostic must bind exactly these retained identities:

- evaluator source commit
  `effa358f2ceaca2b9accd829984272598fa78078`;
- evaluator binary SHA-256
  `f2c2940a565397a010488843af99ff469c725dd54a720c90ed190311249ed1f6`;
- evaluator build-manifest SHA-256
  `fcb17b1b279acbfd80bde9f753b1fcad0e6cae2aef55db88e55bb319a0cecb4c`;
- frozen input-manifest SHA-256
  `d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0`;
- Kubernetes commit
  `b2ec8b6fefac451a2dedafc4dd71f2f16c7a6abe` and effective input SHA-256
  `d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4`;
- cache OFF, profile `full`, verb `snapshot`, scenario `diagnostic`, arm
  `reuse=false`, one worker and no preparatory invocation; and
- the existing no-egress environment, pinned runtime and process-group cleanup
  behavior, plus an explicitly enforced 14 GiB cgroup memory ceiling and
  `TasksMax=512`, with
  the effective limits recorded before the product starts.

The last item is a prospective prerequisite, not a claim about the eighth
diagnostic. Focused inspection of its retained launch command found
`GOMAXPROCS=4`, the outer timeout and process-group cleanup, but no
`systemd-run`, `MemoryMax` or `TasksMax` enforcement. Its missing GNU-time RSS
record also cannot prove a memory ceiling. The proposed run must close that
control gap rather than describe the protocol limits as inherited guarantees.

The package cap is one reserved and attempted product invocation. The shared
100-run accounting remains in force. If executed, this is controlled product
invocation nine whether it completes, returns partial, fails or times out.
There is no ON arm, cache warm-up, repeat, retry, comparison, admission,
performance claim or automatic follow-up. Progress monitoring is required and
must not terminate or cancel the ongoing workload.

The delayed diagnostic CPU profile remains enabled with the retained
88-second requested start, 90-second latest start, 20-second window and 8 MiB
ceiling. Keeping it unchanged preserves disclosure of profile overhead. Explicit
cgroup enforcement is an execution-control correction disclosed separately,
not evidence that conditions matched the eighth diagnostic. It must be
implemented and verified before the authorized completion diagnostic; until
then, no diagnostic run is ready.

## Exact completion-diagnostic control delta

Preparation may mechanically copy the reviewed
`diagnostic-dispatch-effa358f` controls into a new package with a new dispatch
identity. Before any cloud operation, review must verify these bounded changes
and no others:

1. Do not impose an arbitrary product deadline. Preserve process-group cleanup,
   artifact collection and post-run identity checks, with the actual first
   issue ending the diagnostic.
2. Launch the collector and its product child in one uniquely named transient
   systemd scope using `systemd-run --scope`, a unique `--unit`,
   `--property=MemoryMax=15032385536` and `--property=TasksMax=512` around the
   existing `runuser ...` command. Before durable
   claim creation or product start, record the collector's effective cgroup
   path plus its `memory.max` and `pids.max` values and require exact values
   `15032385536` and `512`. Failure to create the scope, read the effective
   limits or match either value refuses the product invocation. Process-group cleanup remains available for an actual issue or explicit
   cancellation.
3. Create a new one-cell batch manifest with `total_attempts=1`,
   `derived_invocations=1`, `preparatory_invocations=0` and only arm `false`.
   Use new claim and output paths; never reuse or mutate the eighth package.
4. Rebuild the control archive because the controller/collector bytes changed,
   bind every new control hash in the manifest, and require the existing exact
   full-check and compiler-build evidence before claim creation.

No command is approved by this document. After package review, the exact
invocation would remain the reviewed controller's single `controller.py
--execute` entry point, and may run only after the frozen package and cgroup
controls are independently verified.

## Required evidence and outcome rules

A completion observation requires all of the following retained and
hash-bound evidence:

- durable reservation, start and terminal budget state for exactly one arm;
- the effective cgroup path, `memory.max=15032385536` and `pids.max=512`
  recorded before claim/product start;
- process exit zero for a complete result;
- a relations phase-end event and every later phase needed for a terminal
  snapshot result;
- the complete semantic result and diagnostic arrays, with their status,
  counts, ordering and digests retained rather than normalized;
- elapsed time and a valid process-specific peak-RSS measurement;
- source, repository, binary, input, batch, gate, runner and control identities
  matching before and after execution; and
- the raw archive, controller/collector statuses, VM terminal records and all
  three VMs deallocated after collection.

The terminal classification is fixed prospectively:

- A complete result establishes only that this exact work reached completion.
  It does not pass the campaign cell or establish performance, stability,
  admission or release, and does not rewrite any of the eight retained
  diagnostic observations.
- A provider partial is retained as partial and is not completion, even if the
  process exits zero. Any missing semantic array/digest, RSS value, phase-end,
  identity or control artifact is likewise an issue.
- A process/control failure, identity drift, invalid resource measurement or
  any other first issue ends runtime investigation.
  It triggers cleanup and retention only: no second attempt, larger deadline,
  alternate arm/profile/verb, source optimization or automatic escalation.

The existing relations progress counters show advancement only through the
completed 88-to-108-second profile window; they are cumulative and reveal no
total or remaining-work denominator. A terminal phase-end and complete result
would distinguish finite completion beyond 120 seconds. Another timeout would
remain causally inconclusive and must not be described as proof of an infinite
loop or pathological repetition.

## Retained history and approval effect

All eight existing controlled diagnostic observations—the completed
syntax-only partial and seven full timeouts—their raw artifacts and the
zero-clean-batch state remain immutable. The existing user authorization
permits only the single diagnostic described here. It does not approve a full
P1 campaign, modify the preregistered 120-second protocol, waive any release
gate, or authorize preparation or execution of another runtime observation.
