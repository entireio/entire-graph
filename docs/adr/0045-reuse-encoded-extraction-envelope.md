# ADR 0045: Reuse encoded extraction envelopes during publication

Status: accepted implementation decision; performance unverified.

Plan tasks: P1.2/P1.3/P1.6. Sources are the requirements plan, Entire's
extraction/cache implementation, and the retained ON-only CPU diagnosis at
`d793b2be`. No external implementation informed this decision.

The cold path serializes a payload for its digest, serializes the enclosing
record to calculate a safe publication bound, discards those enclosing bytes,
and encodes the enclosing record again during gzip publication. Retain the
already-encoded envelope in the bounded pending item and write those bytes
through the existing private atomic gzip lifecycle. The payload digest still
hashes the same payload representation; JSON decoding and the private cache
format remain compatible. Generic cache consumers retain their behavior.

Keep current decode limits, 128-entry/16 MiB pending limits, conservative
compression bound, quota lock/inventory rules, directory confinement,
no-follow checks, orphan naming/cleanup, atomic rename and failure reporting.
Do not introduce a raw writer path that bypasses these protections. Preserve
actual compressed-byte diagnostics. Invalid UTF-8 remains non-persistent;
nil/empty slices and private declaration metadata remain lossless.

Tests must exercise cold/warm equivalence, exact decode/digest compatibility,
corruption, bounded publication, cross-process quota and no-follow behavior.
New fixtures derive from these contracts. Focused correctness precedes one
immutable integration check after the related implementation is settled.

The retained profile identifies publication as material work, but does not
isolate the benefit of this change or establish a wall-time or release gate.
Repeated quota scans are a separate design question: do not substitute stale
in-memory headroom or silently change admission concurrency in this patch.

Rollback: disable extraction reuse or revert this private publication change.
No schema migration, default enablement or cache-key change is required.
