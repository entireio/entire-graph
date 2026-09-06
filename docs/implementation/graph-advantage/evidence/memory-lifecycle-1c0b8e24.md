# P1 extraction-cache memory-lifecycle audit at `1c0b8e24`

## Scope and evidence status

This is a read-only source audit of extraction-cache payload ownership at exact
product commit `1c0b8e24` (`perf(sem): reuse admission gzip writer`). It covers
the extraction pending queue, admission session, and encoded held-directory
writer. It does not profile grammar extraction, change code, or add a
measurement.

The motivating isolated cold snapshot pair was supplied to this audit: exact
semantics and 194 partial failures in both arms, OFF 58.181 seconds and ON
62.394 seconds, with GNU time peak RSS 2,686,144,512 bytes and 3,142,365,184
bytes respectively. This single pair failed the memory screen. It is not a
statistical result, and the source finding below does not establish the cause of
the 456,220,672-byte difference.

## Source-derived limits

At `1c0b8e24`, `internal/sem/extraction_cache.go:22-30` sets:

- a strict encoded-entry ceiling below 64 MiB (`len(encoded) < 64 << 20` is
  enforced at `internal/sem/extraction_cache.go:303-307`);
- a pending publication trigger at 128 entries or 16 MiB of conservative gzip
  bounds; and
- persistent per-repository ceilings of 100,000 entries and 1 GiB.

The pending trigger is evaluated after appending the newest entry
(`internal/sem/extraction_cache.go:103-117`). Therefore one detached batch can
contain less than 16 MiB of earlier encoded payloads plus one final encoded
payload of less than 64 MiB: a source-derived bound below 80 MiB of encoded
payload bytes. This is a reference-lifetime bound, not an RSS estimate; slice
headers, records, compressor buffers, parsed graph state, allocator behavior,
and other operation data are separate.

The provider uses up to eight file workers
(`internal/sem/provider_parallel.go:26-37`) and invokes extraction inside those
workers (`internal/sem/provider_parallel.go:217`). Each worker runs one process
call at a time (`internal/sem/provider_parallel.go:322-340`).

## Finding: detached and processed payload references outlive their immediate need

`takePendingLocked` transfers the pending backing array to a local `batch` and
correctly clears the cache field (`internal/sem/extraction_cache.go:121-125`).
The caller then invokes `publishBatch` outside `pendingMu`
(`internal/sem/extraction_cache.go:103-118`). `publishBatch` waits for the single
per-cache admission mutex at `internal/sem/extraction_cache.go:147-152`.

While one worker publishes, another worker may fill, detach, and wait with a
different local batch. The number is bounded by the provider's eight workers,
but the cache's 16 MiB/128-entry trigger does not bound the aggregate of these
already-detached batches. The source-derived encoded-payload reference envelope
is consequently up to eight batches, each below the per-batch bound described
above. This envelope is deliberately conservative and must not be presented as
measured heap or RSS.

Within each publisher, `internal/sem/extraction_cache.go:191-201` copies eligible
`extractionPending` structs into `chunk` without clearing the corresponding
original `batch` slots. The copy does not duplicate payload bytes, but both
slice elements reference the same encoded byte array. Admission consumes files
sequentially at `internal/sem/extraction_admission.go:203-228` without clearing
processed `encoded` fields. A successfully written payload therefore remains
reachable from the active chunk and original detached batch until the complete
publication call returns.

This is a real, bounded ownership-lifetime issue: detached and already-written
payloads stay reachable after their immediate write no longer needs them. It is
not evidence that the issue caused the isolated 456,220,672-byte RSS change.
The retained semantic cases must complete before using the screen result to
choose another design.

## Ownership paths that do release correctly

- The cache-owned pending slice is nilled when ownership transfers
  (`internal/sem/extraction_cache.go:121-125`).
- `flush` defers admission release on every return
  (`internal/sem/extraction_cache.go:131-145`).
- The admission session stores only quota metadata and one compressor, then
  resets and nils the compressor, held capability, inventory, and limits on
  close (`internal/sem/extraction_admission.go:18-29,52-66`).
- The reusable writer resets its output to `io.Discard` after every attempted
  compression (`internal/sem/cache_entry.go:194-248`), so it does not retain a
  temporary file reference between entries.
- The intermediate extraction record and digest serialization are local to
  `extract`; only the final encoded envelope is enqueued
  (`internal/sem/extraction_cache.go:285-310`). No source bytes are added to the
  persistent extraction payload.

## Possible future cleanup — not implemented

A future ownership correction could combine two changes:

1. Transfer eligible `extractionPending` values into a publication chunk while
   zeroing their original batch slots, then clear each chunk slot immediately
   after its write or on every refusal/error return.
2. Add cancellation-safe backpressure so only a bounded number of detached
   publication batches can wait for the per-cache admission session. The design
   must preserve the 128-entry/16-MiB pending limits, nonblocking cross-process
   admission, worker progress, operation flush semantics, and lock ordering; it
   must not replace detached batches with an unbounded cache-owned queue.

Neither cleanup is implemented here. No cache, GC, compression, worker, or
batch parameter should be tuned from this audit or the isolated cold pair.
