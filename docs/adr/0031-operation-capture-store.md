# ADR: bounded operation-owned capture storage

Status: accepted for internal store development; integration not admitted
Date: 2026-09-05

Decision: memoize each authorized reader result once per operation, including unavailable inputs, behind per-path synchronization. Keep at most 64 MiB of content strings in the store; larger captures spill to one private temporary directory, opened through os.Root, using generated exclusive names. Never derive spill paths from repository paths. Read spilled bytes through the held root with exact length and digest validation. Close cancels pending work, waits for readers, closes backing handles and removes owned files. Errors fail closed rather than reacquiring changed live bytes.

Alternative rejected: an unbounded map defeats the existing bounded-memory provider. A disk snapshot keyed by HEAD is forbidden. Stat identity is not captured-content identity.

The store accepts a confined bounded upstream reader; it does not authorize paths or widen scope. A separate integration change must connect prefix/oversize routing, manifest readers, source rendering and selective search to the same lifetime and expose fatal spill errors. Until then the store is private and unused in production.

Tests distinguish single acquisition under concurrency, same-size mutation, failed-read memoization, tiny-budget spill, cleanup, cancellation, digest corruption and detached operation lifetimes. No public contract changes.
