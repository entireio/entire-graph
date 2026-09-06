# ADR 0042: Captured working-tree preselection

Status: implementation decision; correctness and release gates remain separate.

## Context

P1 diagnosis found that extraction reuse bypassed Git's bounded preliminary
candidate pool. Equal worktrees consequently selected different files. Restoring
Git's live content scan would violate the operation's single-observation contract.
A first Go-only replacement also missed Git binary attributes and caller-locale
behavior. These were correctness findings, not performance-tuning opportunities.

## Decision

Observe fixed-pattern matches while the confined descriptor is read for capture.
The observer sees oversized tails during the existing digest pass; it retains
query-term presence, a bounded match window and a matching-line budget, not the
oversized body. Parser limits remain unchanged. Retain a matched-file marker even
when a matching substring has no normalized query-term contribution, preserving
the preliminary pool's existing behavior. Later query stages cannot enable live
Git content reads in captured mode.

Use Git to evaluate attributes in an ephemeral attribute-only worktree. Populate
it from captured ancestor `.gitattributes` bytes. For absent worktree policies,
freeze the index's stage-0 blob identity and read that immutable blob. Materialize
empty placeholders when neither observation supplies a policy. Git never reads
source bodies in this step. Keep automatic binary sniffing distinct from forced
binary and forced text, including custom driver settings. Enforce aggregate
policy/output bounds and validate repository metadata before invoking Git.

Retain the first effective attribute decision per path in the operation store,
and include the decision digest in operation input identity. Provider-owned
policy bytes share source capture. Git's other listing/configuration/metadata
probes remain an explicit opaque coverage boundary; no atomic repository claim
or persistent working-tree snapshot is introduced.

C/POSIX matching folds ASCII only. Other locale matching currently uses Go's
Unicode simple folding; discriminating fixtures cover C, POSIX and one available
UTF-8 locale. This evidence does not establish equivalence for every locale or
platform. Repository-subdirectory attribute capture is implemented at
`9c7c70b8`, with a fixture covering ancestor policy and caller-relative paths.
Full integration verification and retained-request parity remain pending.

## Verification and rollback

Independent fixtures cover chunk boundaries, long lines, match-line limits,
oversized mutations, binary attributes, forced text, custom drivers, immutable
index fallback, policy/configuration mutation, input identity and a no-egress
tripwire. Consulted implementation sources are Entire source and the authorized
plan. Git behavior supplies the existing implementation oracle; no competitor
source or external measurement supplied expectations.

Keep extraction reuse default-off. Disabling it restores the ordinary path.
Temporary policy worktrees are operation-owned and removed on return. A full
end-to-end diagnostic replay and remaining compatibility work are required
before the fixed corpus campaign can resume; no release or speed claim follows
from these fixtures.
