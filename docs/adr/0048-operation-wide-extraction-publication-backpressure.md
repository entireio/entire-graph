# ADR 0048: Bound detached extraction batches across the operation

Status: accepted for implementation; resource effect unmeasured.

Plan tasks: P1.3/P1.6. Sources: supplied plan, Entire extraction ownership
implementation at 1c0b8e24, and the read-only memory-lifecycle audit. The failed
single-pair RSS screen motivates review, but does not prove this is its cause.

Current producers detach a whole pending batch before waiting for admission.
Up to eight provider workers can therefore retain distinct waiting batches,
even though publication is serialized. The per-batch bound is not a single
operation-wide pending bound. Merely clearing duplicate references inside a
chunk would not prevent that multiplication.

Introduce a private cancellation-aware one-token publication gate, acquired
before appending or detaching pending items. Hold it through publication when
the threshold triggers; release immediately after a below-threshold append.
Noncancelled producers wait and preserve eligible publication. Cancelled
producers return without enqueueing. A nil context remains valid. Recheck
cancellation after acquisition because token and cancellation can become ready
together. Release the token on every return path.

Final flush acquires this gate independently of the operation's cancelled
context, drains pending items, and releases the admission session before
releasing the token. This preserves cleanup and later reuse of the cache
object. The channel is never closed. Lock order is publication gate, then
pending lock (released before publication), then admission and quota/session.
No path waits for the gate while holding pending, admission or quota locks.

Keep the existing admission mutex and held-directory/session protocol. Moving
that mutex alone before detachment would introduce uncancellable waits and
self-deadlocks through existing publish/release calls. Dropping contended
eligible publication would weaken reuse semantics. A background worker or
encoding-boundary redesign is unnecessary for this narrower ownership fix.

The resulting bound is one detached batch OR the cache's pending batch,
including the existing final-entry threshold overshoot. Other workers may
still retain one current encoded item each: encoding occurs before enqueue.
This is not a total heap/RSS bound. Do not change compression, cache quotas,
worker counts, GC settings, output ordering, payloads, or snapshot policy.

Distinguishing tests: hold admission while one producer detaches; a second
producer cannot build another pending batch. Verify eventual eligible writes,
cancelled waiter return, cancelled flush cleanup, token release after refusal
and error, repeated cache reuse, and race-enabled concurrent producers. Keep
existing transient reservation, filesystem confinement and artifact parity
checks. No performance claim follows from these tests.

Rollback: disable extraction reuse, or revert this private gate change. No
schema, key or persisted-artifact migration is needed.
