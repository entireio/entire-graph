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
`skipped_relation_families`. Its `languages`, `language_tiers`, `warnings`,
`partial_failures`, `stats`, and `completeness` are empty/zero — those totals
are not known until the whole repository has been processed, and the header
is emitted before that so consumers can begin work immediately. The profile
metadata is **header-only**: it is known up front and is therefore not
repeated in the summary.

**`repo_key` is the symbol-ID namespace, not a repository identity.** Remote
URLs are checked in the provider's established compatibility order: the last
configured `remote.origin.url` is checked first, then every non-origin remote
URL in Git config order. The first URL in that order matching a supported
github.com form yields `gh/<owner>/<name>`. A repository with no such URL — no
remote, gitlab, bitbucket, self-hosted — instead yields `local/<basename>`,
which is **not** globally unique. Two unrelated repositories whose directories
are both named `tools` publish the same `local/tools` in their headers and in
every symbol ID. A consumer must therefore use `repo_key` as a necessary and
not a sufficient identity test: a mismatch proves a foreign snapshot, a match
does not prove a native one. Pair it with `repo_root`, `commit` and `tree` — or
with the consumer's own storage key — for identity. `graph doctor --json`
advertises the same `repo_key` and `repo_root` before a snapshot is built, so
the pair can be checked up front.

**Symbol ID fields are not escaped, so an ID must never be split positionally.**
An ID is `<repo_key>:<language>:<file_path>:<kind>:<qualified_name>` joined with
`:`, and nothing escapes the fields. Two of them can carry a `:` of their own: a
`local/<basename>` key inherits whatever the directory is called, and a file path
may contain one on any POSIX filesystem. `local/weird:name:Python:od:d/mod.py:class:Cache`
is a well-formed ID that splits into seven fields, not five. The trailing fields
already do this routinely — `external:import:std::collections::HashMap`. A
consumer that reads `split(id, ":")[2]` as the path therefore mis-attributes
every record in such a repository, silently and without an error.

The four safe reads, all of which entire-graph uses internally and pins in its
own tests: compare a whole ID; anchor on the LAST separator for a trailing
segment; cut at the FIRST separator only inside the `external:<kind>:<value>`
namespace, whose kind cannot contain a `:`; and take a file path from the
record's own `file_path` field rather than from the ID.

**The final `summary` record is authoritative for aggregate metadata.** It
carries the real `languages`, `language_tiers` (each present language
classified `semantic` or `inventory-only`), `warnings`, `partial_failures`,
`stats` (including the `relations` count and `completeness_level`), and the
`completeness` breakdown. It does **not** carry profile metadata; consumers
should read that from the lean header and must not expect it in the summary
unless a future schema version adds it.

**Merging the two.** A consumer that wants one fully populated header should
take the lean header and overlay the summary's aggregate fields (`languages`,
`language_tiers`, `warnings`, `partial_failures`, `stats`, `completeness`) on
top of it — summary wins for any field both records carry, the header wins
for the profile metadata the summary omits. For any aggregate total,
including per-language tier, read the summary, never the lean header.

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
is mandatory. A v1 relation row has the original 11 fields, plus an optional
twelfth `evidence_dropped` integer when the value is nonzero. For artifacts that
declare the current or an older supported schema minor, consumers accept either
relation arity and reject every other one. For a newer minor in the supported
major, consumers warn, decode each known data row's required positional prefix,
ignore trailing additive fields, and skip unknown public data tags. The outer
`h`, `d`, and `m` arities remain exact because they are compact-envelope control
structure governed by the compact format version, not the record schema.
Consumers must also reject unknown format or schema-major versions, malformed
known field values, missing required fields, non-first or duplicate headers,
and missing summaries.

All `h`, `d`, data, and `m` bytes count as raw compact artifact bytes;
dictionary overhead must never be subtracted. Compact output is loaded only
through the production compact loader and queried with
`snapshot-query --input <file> --symbol <id-or-name> [--from <stable-id>
--relation <TYPE>] --format ndjson`, which writes deterministically ordered
native symbol/relation records. For an artifact produced by the same build, its
decoded public projection and canonical semantic SHA-256 (normalized native
records in record order) must equal the normal NDJSON snapshot; the lossless
preflight enforces both. A newer-minor reader instead hashes its known
projection and carries `W_NEWER_SCHEMA_MINOR`, because intentionally skipped
additive facts cannot equal the newer producer's full projection. Matching only
the hash is not sufficient evidence of losslessness.

## Experimental SCIP snapshot protobuf

`snapshot --format scip` is an experimental, complete-snapshot-only projection for SCIP consumers. It writes a single binary SCIP `Index` protobuf to stdout and a single JSON omission note to stderr. It is rejected for `symbols`, `edges`, targeted `--to`/`--from`/`--relation` output, and `--progress`, and it is not served from the NDJSON or compact snapshot caches. The SCIP package version is the project's own declared version, read from the root manifest (`package.json`, then `Cargo.toml`, then `pyproject.toml`'s `[project]` or `[tool.poetry]` table), falling back to `0` when none declares one -- which is the common case for Go, since `go.mod` carries a module path and not a version. It is deliberately **not** the commit: a commit there would give every symbol a new identity on every commit, so nothing downstream could match a symbol across commits, and cross-index linking (the reason SCIP has the field) could never work. Commit and worktree provenance are carried instead by the omission note's `commit` and `tree`, by `--worktree` in `ToolInfo.arguments`, and by `worktree_snapshot: true`. Note that SCIP `Metadata` has no revision field of its own -- it carries `ToolInfo`, `ProjectRoot` and an encoding -- so the note is where the revision lives, once per index rather than inside every symbol. The manifest is read from inside the snapshot build, through the same validated content reader every other manifest read uses, so it is bounded, refuses non-regular files, and is pinned to the revision the snapshot describes. A version bumped only in a dirty working tree therefore cannot reach an index that describes a commit, and the version cannot come from a different revision than the documents beside it. Consumers must therefore treat `(repo, commit)` as coming from the envelope, not from the symbol: two indexes of the same repository at different commits share symbol identities by design, and merging them into one flat store without keeping their commits distinct would conflate them. If the underlying snapshot is partial, the same stderr JSON object includes `partial_snapshot`, `completeness_level`, `warning_count`, and `partial_failure_count` instead of mixing human warning text into stderr. Unlike the NDJSON encoders, the SCIP encoder retains the complete graph until it can write the one protobuf message, because a SCIP `Index` is a single protobuf message and cannot be streamed. Its memory use therefore scales with snapshot size: measured on this repository (about 28 MB of native NDJSON, 9,770 definitions, 25,476 references) peak RSS was roughly 170 MB against roughly 115 MB for `ndjson` and roughly 119 MB for `compact-ndjson`, so about 1.5x the streaming formats. That multiplier is not a guarantee for a much larger repository, where the absolute figure is what matters; measure before exporting a monorepo.

The note also carries what SCIP itself cannot express. Its `language_tiers` repeats the header's
per-language `semantic` / `inventory-only` classification, because every discovered file becomes a
`Document`: an inventory-only file -- listed but never parsed for symbols or relations -- is
otherwise indistinguishable from a semantically extracted one, and a consumer scoping trust per
language cannot tell them apart from the protobuf alone. Its `partial_failures` carries the
summary's failure records rather than only their count, because a count says something was
skipped while only the records say which path and why.

The note carries its own contract version in `version`, currently
`entire-graph-scip-omissions/v1`, independent of the snapshot `schema_version`. Within a
version it evolves **additively only**, on the same tolerant-reader terms as
[ADR 0001](adr/0001-ga-schema-contract.md): new optional fields may appear, a consumer must
ignore fields it does not recognize, and it must not require any optional field to be present.
An absent optional field means zero or false, never unknown -- the counters are omitted when
they are zero precisely so a clean export stays quiet. Removing a field, renaming one, or
changing what an existing one counts is a breaking change and bumps the version to `v2`.

The stderr note also reports what the encoder could not carry. `unidentified_records` counts records it could not key at all (a file with no path, a symbol or external endpoint with no id), and `unlocated_symbols` counts symbols with no file path, which are emitted but land in a synthetic document with no navigable definition location. Both are omitted from the JSON when zero, and both are provider bugs rather than expected input, but this format reports them rather than letting an incomplete index look complete.

The protobuf contains `Metadata`, one `Document` per source path, `SymbolInformation` for native symbol definitions, definition occurrences, and reference occurrences for supported reference-like relations: `IMPORTS`, `CALLS`, `CONSTRUCTS`, `ASYNC_CALLS`, `EXTENDS`, `INHERITS`, `IMPLEMENTS`, `OVERRIDES`, `USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`, `READS_FIELD`, `WRITES_FIELD`, and `ACCESSES`. Occurrence ranges cover the complete one-based inclusive line spans supplied by Entire Graph; the export does not invent unavailable columns. Native `DEFINES` and `CONTAINS` relations are represented by symbol metadata and definition occurrences rather than emitted as relation edges. The inheritance family -- `IMPLEMENTS`, `INHERITS`, `EXTENDS`, `OVERRIDES` -- additionally becomes `SymbolInformation.relationships` with `is_implementation`, because SCIP answers "Find Implementations" from relationships and not from occurrences; `emitted_implementations` in the note counts them. A definition occurrence marks the declaration line only, with the full declaration-through-body span carried as its `enclosing_range`, so a definition does not overlap every reference inside its own body and positional lookups stay unambiguous. Other relation families are omitted from the SCIP protobuf and counted by relation type in the stderr note:

```json
{"record_type":"scip_omissions","version":"entire-graph-scip-omissions/v1","format":"scip","unsupported_relation_counts":{"DATA_FLOWS":1},"missing_target_relations":0,"missing_evidence_relations":0,"emitted_definitions":2,"emitted_references":1}
```

The native NDJSON stream remains the lossless provider contract. The SCIP export is an interoperability artifact for navigation tools whose model is definitions and references, so consumers that need full Entire Graph relation semantics must keep using `--format ndjson` or `--format compact-ndjson`.

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
