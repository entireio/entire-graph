# Snapshot format

This page is the canonical description of the NDJSON artifacts emitted by
`snapshot`, `symbols`, and `edges`, the compact snapshot variant, and the
schema compatibility rules consumers must follow. The schema contract itself
is [ADR 0001](adr/0001-ga-schema-contract.md).

## Schema versioning

Provider output uses `schema_version` in `major.minor` form.

- Consumers refuse unknown major versions.
- Consumers may ignore unknown fields within a supported major version.
- If the provider emits a newer supported-major minor version, consumers
  should warn that some facts may have been skipped.
- Unknown relation types should use an extension namespace, such as
  `X-provider-name:RELATION`.

Provider records are typed. Representative data records:

```json
{"record_type":"file","id":"gh/org/repo:file:internal/auth/token.go","path":"internal/auth/token.go","blob":"..."}
{"record_type":"external","id":"external:import:net/http","kind":"import","value":"net/http"}
{"record_type":"symbol","id":"...","kind":"function","name":"ValidateToken"}
{"record_type":"relation","from_id":"...","to_id":"...","type":"CALLS"}
```

Relation endpoints may point to file records, symbol records, or external
endpoint records. Consumers should ignore unknown record types within the
supported major schema version, and should not assume every relation target
is a symbol.

## Streaming NDJSON

The `snapshot`/`symbols`/`edges` commands stream records to stdout as they are
produced, so the stream does not hold full relation payloads, their evidence,
or file contents in memory. Peak memory is bounded by the symbol/index
metadata plus the relation dedup set, which holds one compact 64-bit key per
unique relation. The stream is emitted in this order:

1. exactly one header line (a record with `schema_version`),
2. `file` records, then `symbol` records (emitted per file as parsing
   progresses, before any relation is resolved),
3. `relation` records and the `external` endpoint records they reference,
4. exactly one trailing `summary` record (`record_type: "summary"`).

**The first header is intentionally lean.** It carries identity (`provider`,
`provider_version`, `repo_root`, `repo_key`, `commit`, `tree`),
`schema_version`, `capabilities`, `schema_features`, `language_versions`, and
the **profile metadata** — `profile`, `profile_limits`, `relation_set`, and
`skipped_relation_families`. Its `languages`, `warnings`, `partial_failures`,
`stats`, and `completeness` are empty/zero — those totals are not known until
the whole repository has been processed, and the header is emitted before that
so consumers can begin work immediately. The profile metadata is
**header-only**: it is known up front and is therefore not repeated in the
summary.

**The final `summary` record is authoritative for aggregate metadata.** It
carries the real `languages`, `warnings`, `partial_failures`, `stats`
(including the `relations` count and `completeness_level`), and the
`completeness` breakdown. It does **not** carry profile metadata; consumers
should read that from the lean header and must not expect it in the summary
unless a future schema version adds it.

**Merging the two.** A consumer that wants one fully populated header should
take the lean header and overlay the summary's aggregate fields (`languages`,
`warnings`, `partial_failures`, `stats`, `completeness`) on top of it —
summary wins for any field both records carry, the header wins for the
profile metadata the summary omits. For any aggregate total, read the
summary, never the lean header.

**Ordering.** For a fixed input and profile the stream is deterministic and
stable (file, symbol, and relation order are reproducible across runs), but
it is not globally sorted. Consumers should key on record `id`/identity, not
on stream position.

**Unknown record types.** Consumers must ignore record types they do not
recognize within a supported major schema version, and must not assume every
line is a known type. A consumer that reads only the header and relations
should skip `file`, `symbol`, `external`, and `summary` lines rather than
erroring on them.

## Compact snapshot NDJSON v1

`snapshot --format ndjson` remains the default interoperable object stream.
`snapshot --format compact-ndjson` is a public, full-snapshot-only artifact
with a separate cache mode, `snapshot:compact-ndjson-v1`; it is rejected for
`symbols`, `edges`, and targeted `--to`/`--from`/`--relation` output. Its
first line is `["h", 1, header]`, and the version appears nowhere else.
Deterministic first-seen dictionary lines `d` precede positional `f` (file),
`x` (external), `s` (symbol), and `r` (relation) rows; a trailing `m` summary
is mandatory. Consumers must reject unknown versions, malformed row arity,
non-first or duplicate headers, and missing summaries.

All `h`, `d`, data, and `m` bytes count as raw compact artifact bytes;
dictionary overhead must never be subtracted. Compact output is loaded only
through the production compact loader and queried with
`snapshot-query --input <file> --symbol <id-or-name> [--from <stable-id>
--relation <TYPE>] --format ndjson`, which writes deterministically ordered
native symbol/relation records. Its decoded public projection and canonical
semantic SHA-256 (normalized native records in record order) must equal the
normal NDJSON snapshot. Matching only the hash is not sufficient evidence of
losslessness.

## Progress telemetry

The optional provider progress callback (`--progress` on the stream commands)
exposes process-local performance telemetry only. Its typed phases are
`inventory` (source preparation/file discovery), `parse` (header output,
registration aliases, file/symbol output, and index construction),
`relations` (relation resolution), and `finalize` (external output plus
trailing-summary construction and serialization). Each event carries elapsed
time since that phase began and total provider-work time since snapshot
entry. Progress lines go to stderr; stdout remains valid NDJSON. Telemetry
fields are not snapshot schema fields and never alter emitted semantic
records.
