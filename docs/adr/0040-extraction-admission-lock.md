# ADR: cross-process extraction quota admission

Status: accepted, experimental and default-off
Date: 2026-09-05

P1.3 requires bounded persistent reuse under concurrent writers. Per-operation
free-space reservations become stale when another process publishes entries.
Replace them with an operating-system advisory lock covering bounded directory
maintenance and atomic publication. The lock is opened through the same held,
non-redirecting directory capability as cache entries. Nonblocking contention
skips persistence; extraction still succeeds. Kernel locks release on process
exit. Unsupported platforms refuse publication rather than weaken the bound.

Byte admission conservatively includes compression framing overhead. Limits
remain sampled once per operation. Every publication rereads bounded directory
usage; no operation retains a stale global reservation. This trades additional
cache IO for correctness. Comparative performance evaluation is deferred.

Alternatives rejected: a process-local mutex cannot coordinate processes;
a persistent lock directory can strand reuse after a crash; time-based stealing
can admit two live writers. The lock file is metadata, not an extraction entry.

Tests distinguish independent operation reservations, concurrent subprocesses,
lock contention and release, redirected lock leaves, and byte/entry ceilings.
Fixtures are independently generated from P1.3's concurrency requirements.
Rollback: --extraction-cache off. This does not enable worktree snapshot reuse.

Maintenance also removes orphan temporary files with the exact cache-owned
.extract-<32 lowercase hex>.json.gz naming format while admission is held. A
cooperating writer cannot be active then. Unrecognized names and redirected
entries are untouched; oversized directory scans refuse admission. The scan
allows one extra lock metadata entry and one overflow sentinel (100,002 reads).
This supersedes the earlier 100,001-entry scan description in ADR0039.
