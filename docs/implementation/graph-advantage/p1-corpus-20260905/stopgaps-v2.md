# Campaign diagnostic gates v2

This user-directed policy supersedes the historical three-failure stratum rule
for new runs. Prior data and the original protocol remain unchanged. The old
campaign stays paused. No VMs or measurements were started to validate this code.

A worker pauses the entire local run on the first timeout, process or output
failure, unreviewed partial result, source identity drift, missing semantic or
resource measurement, paired semantic mismatch, manual STOP, or expired
supervisor lease. It persists the failing observation, a pause reason and
unrun accounting. Interrupted requests remain distinct from observations that
completed. There is no automatic resume or deletion of the previous evidence.

The external supervisor checks all workers before renewing any lease. Any
worker pause, failed service, missing status or control-plane error stops the
other worker services. Polling is every 30 seconds plus control-plane latency;
workers independently enforce a 180-second lease, including during a request.
This is bounded propagation, not an instantaneous distributed stop. Provider
analysis remains no-egress; Azure control calls belong to the external harness.

New launches require an isolated `--run-id` and `--supervisor-output`. Existing
run directories cannot be reused. The launcher remains attached as supervisor;
losing that process expires worker leases. Active P1 services prevent duplicate
launches. Never launch this command as a short-lived setup call and assume that
unsupervised jobs will continue.

A `--canary` campaign uses one pair per workload cell. The first detected issue
pauses it. Full repetitions require all three `--canary-results` directories
from a completed canary, matching binary, input manifest, runner and scenario
hashes. Every expected cell must be present exactly once, complete, fresh,
resource-measured and semantically equivalent. Missing/partial/unrun evidence
cannot admit expansion. A cold canary latency or RSS increase above 10% pauses
expansion for diagnosis; a single-pair warning is not a statistical finding.
The complete preregistered evaluation and release gates remain required later.

Before a new evaluation, finish diagnosis and correctness verification, rebuild
and freeze the executable, and prepare a new baseline and run identity. The old
binary in build.json is historical and must not be reused to claim these fixes.
The unresolved campaign-scale partial-membership differences remain a blocker;
the new small deterministic fixture does not explain all 55 retained mismatches.

On pause, retain raw rows, request artifacts, manifests and supervisor events.
Collect them before deallocating task-owned VMs. Fix and verify the cause before
preparing a separately versioned run. Do not tune corpus or thresholds from the
failed run, suppress diagnostic fields, or declare a release gate passed.
