# ADR: optional source-bound compiler overlay

Status: accepted for experimental implementation; default off
Date: 2026-09-05

Expose a separately named optional compiler block on native snapshot/query
responses. Static relations retain their original identities, resolution and
confidence. Each compiler call-site item names the exact captured caller token,
source symbol, declared target or implementation candidate, target token,
backend/context identity and reconciliation status. Candidate evidence never
becomes an asserted runtime CALLS fact.

Only positive unique direct declarations with complete compiler coverage may
mark an exactly identified static call site disputed. A line-level aggregate
with multiple call expressions is insufficient: retain it and report ambiguous
static-site reconciliation. An empty/failed/unmapped answer does not contradict
a static edge. Complete means the requested bounded compiler queries completed,
not proof that every possible runtime call or build configuration is covered.

Live configuration uses explicit installed gopls/toolchain/launcher paths and a
server SHA-256 pin, never repository commands. Discover module closure using
`go list -m -json all` in the same OS sandbox; refuse modules outside the captured
source closure. Initial external module-cache import is unsupported and returns
unavailable rather than reading ambient dependencies. Capture complete local
workspace files under existing policy and limits; missing inputs remain partial.

A new operation recomputes the whole overlay. No compiler result or working-tree
snapshot is persisted. Compact and SCIP projections must refuse enriched output
until they explicitly represent every distinction. `--require-compiler` fails
when coverage is not complete. Other platforms explicitly report unavailable.

## Package closure and bounded diagnostics refinement

Before issuing navigation requests, run `go list -e -deps -json` for each
captured module's packages under the same namespace, deadline, and build tags.
Reject package/dependency errors and any nonstandard package outside the capsule.
Report actual package import paths (rather than module names). Standard-library
inputs use the read-only explicitly selected toolchain; include the VERSION
contents and Go executable digest in its identity. Arbitrary ambient module-cache
imports remain unsupported. The alternative of trusting only `go list -m` could
report complete coverage without resolving an imported package.

Return the identical build flags in initialization and configuration responses.
Otherwise a server configuration request could change the selected build while
retaining the earlier context digest. Sanitize compiler errors to bounded codes
and generic explanations rather than forwarding diagnostic/source payloads or
subprocess stderr. A malformed first answer must report partial, never complete.
Multiple direct declarations remain ambiguous; do not emit them as independently
confirmed runtime calls. Function-value declaration responses that cannot map to
existing named declarations remain explicitly unmapped.

Distinguishing fixtures: unchanged caller with alternate tagged declaration,
confined local replacement and refused escaping replacement, closure variable
definition, server configuration refresh, malformed/out-of-order response,
blocked source write and descendant network attempt, and process cancellation.
All fixtures are authored locally from plan P2 and the LSP contract.

Exact-site effective-view filtering uses additive `call_site_line` together with
source ID, target ID, path, and positive reconciliation status. Only the matched
evidence item is removed from a derivative relation; other evidence and all
native records remain intact. Namespace candidate relations as
`X-entire-graph:COMPILER_IMPLEMENTATION_CANDIDATE`. Cancellation sends a bounded
best-effort LSP cancel notification, including the initial-send completion race,
then relies on authoritative process-tree teardown.

Compiler context identity includes the operation capture manifest ID supplied by
the semantic adapter. That manifest covers the compiler's full selected capsule
scope after source/context acquisition, including captured policy and requested
view. A search response may expose a different manifest ID for its narrower
selected ranking scope while retaining all observations; this is intentional.
Only direct compiler-package callers without an operation manifest use the
capsule-only identity fallback. A malformed provided ID is refused before launch.
