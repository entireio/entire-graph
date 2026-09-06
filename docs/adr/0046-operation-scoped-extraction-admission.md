# ADR 0046: Operation-scoped extraction admission on a held directory

Status: proposed; implementation design review required.

Plan tasks: P1.3/P1.6. Sources: supplied plan, Entire cache/admission/capture
implementation and retained `cold-profile-d793b2be` CPU samples. No external
implementation informed this design. Cumulative CPU samples are not additive
wall-time attribution or evidence of a performance gate.

Repeated full inventories are material work. Do not optimize them by keeping
unprotected quota headroom or merely retaining the existing lock file: that
API closes its directory handles, while later writers reopen the pathname.
An operation-length lease therefore requires one coherent directory capability
for admission, inventory, eviction and publication.

Proposed boundary:

- Open/validate the cache family/version directory and acquire the existing
  nonblocking lock while retaining that directory handle.
- Build one bounded inventory. Keep conservative byte/count accounting only
  under this continuous lock and use the same held directory for mutations.
- Preserve pending/decode/cache quotas and deterministic eviction. Account for
  replacements and failed writes without understating occupancy.
- Final flush releases the session idempotently; cancellation/error paths
  also release it. Later reuse of the cache object reacquires and rescans.
  Producers must finish before final flush, as under the existing lifecycle.
- Never hold the process-global maintenance mutex over parsing. Concurrent
  operations retain best-effort, nonblocking cache-admission semantics.
- No persistent quota ledger, recovery journal, cache-format change, threshold
  tuning, source freshness shortcut or default enablement.

A killed process loses the kernel lock. The next session rebuilds inventory
and performs existing orphan cleanup; accounting never survives that boundary.
Every write retains no-follow and atomic replacement protections. The existing
cache contract's exclusion for privileged concurrent relocation after an
admitted directory handle is retained, not expanded into a pathname shortcut.

Required fixtures: multiple bounded batches with one inventory; repeated
flush/reacquisition; independent and subprocess quotas; replacements and write
failures; cancellation and crash release; orphan cleanup; symlink and directory
substitution checks using held capabilities; unchanged native/cache semantics.
Focused checks precede immutable repository and pinned correctness verification.

Tradeoff: a long operation can deny competing cache publishers for longer.
Analysis must remain correct and complete under that best-effort refusal.
If capability/lifetime/accounting cannot be kept bounded and coherent, reject
this design and retain the failed performance gate rather than weakening it.

Rollback: extraction reuse remains default-off; reverting this admission
change restores per-batch scans without a migration.
