# ADR 0047: One compressor per extraction admission session

Status: accepted; performance unverified.

Plan tasks: P1.3/P1.6. Sources are the supplied plan, Entire's publication
implementation, and the retained ON-only CPU profile. The latest corrective
pair still fails the cold screen; no broader evaluation is admitted.

Each file currently creates a gzip compressor in the serialized admission
writer. The change gives the admission session one lazily allocated
compressor, reset for each sequential write and discarded on session close.
This avoids repeated compressor construction without a global pool, additional
workers, a compression-level change, or benchmark-selected parameters.

All writes remain serialized by the existing per-cache admission mutex. Reset
the writer to an inert sink after every use so it cannot retain a closed file
handle. Generic cache consumers retain their current behavior. Existing held-
directory confinement, atomic rename, exact installed-size accounting,
conservative transient reservations, pending/decode limits, and cancellation/
error release must be unchanged. No source bytes or worktree view are cached
by this object.

Distinguishing tests compare fresh and reused compressor bytes across different
payloads, decode every artifact, check reset/header/source isolation, cover
failure cleanup, and prove lazy one-per-session ownership and release. Session
quota/race fixtures remain required. This is an allocation-lifecycle correction,
not a changed compression policy or a performance claim.

Rollback: keep extraction reuse off or revert this private compressor change.
No payload/version/key migration is needed.
