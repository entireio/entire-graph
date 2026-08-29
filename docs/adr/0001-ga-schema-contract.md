# ADR 0001 — GA semantic-provider schema contract

Status: Accepted
Date: 2026-07-03

## Context

`entire-graph` emits a semantic index consumed by downstream tools (notably
`entire-brain`). The wire format carries `schema_version` in `major.minor` form.
The provider currently advertises **`1.1`** (`internal/sem/provider.go`
`SchemaVersion`), where the `1.1` minor adds *optional, additive* relation fields
that tolerant readers ignore. A compatibility policy already exists in the
[semantic provider requirements](../semantic-provider-requirements.md), but it
was never ratified as the frozen contract for General Availability, and the
requirements document's example header still showed the older `1.0`.

For GA we need a single, stable, machine-checkable contract so consumers can pin
against it and so future changes have clear, non-breaking rules.

## Decision

**GA ships on schema `1.x`, with `1.1` as the current minor. `1.x` is the frozen,
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
version question being asked in the same edit.

| question | rule | enforced by |
|---|---|---|
| may I *read* bytes another build wrote? | same major; warn on newer minor | `CheckReadableSchemaVersion` |
| may I *reuse* a cache entry I wrote? | exact match; absent fails closed | cache-entry validity checks |
| may this payload's shape change? | additive within a major; break needs `2.0` | frozen shape + pinned digest |

## Consequences

- Consumers may pin `>=1.0 <2.0` and rely on additive-only evolution.
- The `1.1` additive relation fields are part of GA; they are not gated or
  experimental.
- A follow-up adds a brain-side ingestion contract test that asserts
  `entire-brain` parses current `entire-graph` `1.x` output (tracked separately).
- The stale `1.0` example header in the
  [semantic provider requirements](../semantic-provider-requirements.md) is
  updated to `1.1` for consistency with the emitted version.
