# ADR 0001 — GA semantic-provider schema contract

Status: Accepted
Date: 2026-07-03

## Context

`entire-graph` emits a semantic index consumed by downstream tools (notably
`entire-brain`). The wire format carries `schema_version` in `major.minor` form.
The provider currently advertises **`1.2`** (`internal/sem/provider.go`
`SchemaVersion`), where the `1.1` minor adds *optional, additive* relation fields
that tolerant readers ignore. A compatibility policy already exists in the
[semantic provider requirements](../semantic-provider-requirements.md), but it
was never ratified as the frozen contract for General Availability, and the
requirements document's example header still showed the older `1.0`.

For GA we need a single, stable, machine-checkable contract so consumers can pin
against it and so future changes have clear, non-breaking rules.

## Decision

**GA ships on schema `1.x`, with `1.2` as the current minor. `1.x` is the frozen,
stable GA contract.** We do NOT roll back to `1.0`; `1.1` is strictly additive
over `1.0` and every `1.0` reader already tolerates it.

The contract, stable for the entire `1.x` major:

1. **Major = compatibility boundary.** Consumers refuse an unknown *major*
   version. Everything within `1.x` is guaranteed mutually intelligible.
2. **Minors are additive only.** A new minor may add optional fields or optional
   record kinds; it may never remove a field, change a field's meaning, or make a
   previously-optional field required.
3. **Tolerant readers required.** Consumers ignore unknown fields within a
   supported major, and warn (not fail) when they see a newer supported-major
   minor, since additive facts may have been skipped.
4. **Extensions are namespaced.** Unknown/experimental relation types use an
   `X-provider:RELATION` namespace so they never collide with core types.
5. **Breaking changes require a major bump** (`2.0`) and a migration note; they are
   out of scope for the `1.x` GA line.

`entire-brain` ingestion MUST follow the tolerant-reader rules above: accept any
`1.x`, ignore unknown fields, warn on a newer minor.

## Amendment (2026-08-29) — `schema_version` answers two questions, not one

The rules above govern **interchange**: bytes one build produces and a
*different* build reads. Three separate changes then applied `SchemaVersion` to
three different jobs, and two of them read the same field by different rules,
which looked like a contradiction until it was written down. It is not one.

**Interchange compatibility is per-major.** A consumer reading a snapshot
another build wrote accepts any `1.x`, ignores unknown fields, and warns on a
newer minor (clauses 1-3 above; implemented by `CheckReadableSchemaVersion`).
There is a compatibility promise here, so there has to be a tolerance band.

**Cache identity is exact.** An on-disk cache entry is bytes *this* build wrote
for its own later reuse. There is no second party, no compatibility promise to
keep, and no migration path — and the always-correct answer to "was this written
under a different schema" is simply to rebuild, which costs one index and is
never wrong. So cache-entry validity requires `SchemaVersion` to match
**exactly**, and an absent or unparseable version fails closed into a rebuild.

These are not in tension: a tolerance band exists to avoid discarding data you
cannot regenerate, and a cache is by definition data you can regenerate. Reading
the per-major rule as governing cache validity would serve entries written by a
build whose record shape has since changed; reading the exact rule as governing
interchange would refuse snapshots the contract above promises to accept.

**The persisted `Result` payload is interchange**, so its shape is governed by
the major and may only grow within it. That shape is frozen and its digest is
pinned beside the exact version string, so the shape cannot move without the
version question being asked in the same edit. The reflection guard covers every
reachable user-defined named type: struct fields include anonymous-promotion and
`encoding/json`-valid explicit-name taggedness in an unambiguous quoted record,
while named scalars, slices, maps, arrays, and pointers record a canonical
underlying-type descriptor. Type references recurse through composite wrappers
and qualify named types by full import path. Reachable interfaces, named or
unnamed, are rejected because their runtime concrete values cannot be statically
frozen. The guard also rejects custom `json.Marshaler` and
`encoding.TextMarshaler`
implementations, including pointer receivers; either can replace ordinary value
bytes, and text marshaling also controls supported map keys. Finally, an exact
`omitzero` field may not use a value- or pointer-receiver `IsZero() bool` hook,
because that can change field omission without changing its reflected shape.
Any of these customizations requires an explicit serialized contract plus the
same schema-version decision.

| question | rule | enforced by |
|---|---|---|
| may I *read* bytes another build wrote? | same major; warn on newer minor | `CheckReadableSchemaVersion` |
| may I *reuse* a cache entry I wrote? | exact match; absent fails closed | cache-entry validity checks |
| may this payload's shape change? | additive within a major; break needs `2.0` | frozen closed reachable shapes + pinned digest; interfaces and JSON/text/`IsZero` hooks rejected |

## Consequences

- Consumers may pin `>=1.0 <2.0` and rely on additive-only evolution.
- The `1.1` additive relation fields are part of GA; they are not gated or
  experimental.
- A follow-up adds a brain-side ingestion contract test that asserts
  `entire-brain` parses current `entire-graph` `1.x` output (tracked separately).
- The stale `1.0` example header in the
  [semantic provider requirements](../semantic-provider-requirements.md) is
  updated to `1.1` for consistency with the emitted version.

### Parser identity corrections and consumer upgrades

`identity_revision` is an additive, opaque field on the snapshot header and on
`graph version --json`, and the persisted diff/checkpoint Result payload.
Schema `1.2` adds this optional field to the `1.1` contract; its presence does
not change the stable-ID format or require a major bump. It identifies parser
rules that affect existing symbol
IDs or entity-history keys; it does not replace `schema_version` or
`stable_id_version`. Its absence means legacy parser rules. Consumers comparing
stored and current revisions must treat inequality (including missing/present)
as a need to refresh derived semantic data. History consumers must also migrate
previously persisted entity deltas rather than merely append newly parsed ones.

The revision `js-ts-callable-scope-1` covers trail 154's JS/TS callable-scope
corrections. Snapshot/search cache namespaces include this revision so an
unchanged tree and unchanged development release string cannot reuse old parser
output. The companion Brain migration recomputes already indexed commits
atomically while preserving source commits, checkpoint/session provenance, and
authored memory. Ship the consumer support before enabling this producer change.
Future changes that re-key existing symbols must revise this token and document
the corresponding consumer migration; ordinary body edits do not change it.


The revision `js-ts-callable-scope-2` additionally covers trail 163's anonymous
JS/TS default-export corrections: callable exports previously classified as
classes receive corrected function IDs, phantom exports in comments and literals
are removed, and corrected source ranges/signatures can affect entity history.
Consumers upgrading from `js-ts-callable-scope-1` must refresh derived snapshots
and recompute persisted entity deltas using the corrected parser, preserving
source commits and checkpoint/session provenance. A revision mismatch is the
migration trigger; changing the producer token alone does not implement the
consumer migration. Verify consumer support before deploying this revision.

### Compact snapshot reader compatibility

Trail 163 raises the compact summary allowance from 16 MiB to 128 MiB and enforces
the same ceiling on encoding and decoding. Older readers can still reject summary
records larger than 16 MiB; upgrade readers before exchanging these larger
artifacts. The identity revision describes parser identity rules, not compact
reader compatibility, and does not remove this reader upgrade requirement.
