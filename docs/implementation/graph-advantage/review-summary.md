# Review summary: initial graph advantage stages

This is an incomplete implementation of the four-workstream requirements. It provides tested primitives and the initial behavior-preserving provider seam. It does not implement the requested end-to-end capabilities yet.

## Changes

- P1: one-file capture/extraction handoff, explicit serializable declaration metadata, nil/empty and private-field round-trip guard; bounded operation-owned capture store with private spill, cancellation and corruption checks. The store is not wired into queries and persistence is absent.
- P2: compiler input fingerprints, source-bound evidence validation, distinct direct/candidate categories, strict UTF-16 mapping and bounded LSP framing. No live process, protocol client lifecycle, source capsule, semantic reconciliation or public overlay is implemented.
- P3: conservative structural traversal core with stable level order, predecessor paths, alternative stronger explanations, terminal covering tests, graph-work limits and separately bounded materialized output. CLI integration, field/value/endpoint composition and quality evaluation are absent.
- P4: pure candidate-only query-seeded PageRank, deterministic weighted topology, duplicate-evidence control, source-scope confinement and explicit numerical/resource fallback. CLI integration, frozen retrieval labels, ablations, holdout and agent evaluation are absent.

Production default change is restricted to the extraction handoff in processProviderFile. No feature flags/defaults, public schema, stable-ID logic, snapshot cache policy, packaging, installation, generated summaries or Brain code changed. The other primitives are only invoked by tests.

## Validation and limitations

See the ledger and evidence files for exact commands. The Stage A complete mise check passed. All added core fixtures pass focused race tests, including real Go/TypeScript/Python call chains, same-size capture mutations, spill integrity and hand-derived PageRank values. The full `mise run check` passed against product-code commit a70a1892 in 730.00 seconds, including formatting, vet, race tests, build and 151 status-line checks. No product code changed during that final check.

The CPU profile and worker timings are characterization, not evidence of extraction reuse or a performance advantage. No quality/release gate has passed. Linux isolation and a pinned gopls have not been validated; installed local VM tooling is inactive. Ordinary engineering work remains too, and must not be described as blocked only by environmental prerequisites.

## Next reviewable stage

Complete P1.1 characterization and P1.2 operation-wide capture, including the lifecycle shared by source preselection, graph construction, contextual reads, prefix/oversize classification, resolution and source rendering. Audit all resolver-read private fields. Only then add exact keyed storage, bounded cleanup, CLI options and entity reuse. Keep the other cores experimental and unexposed until their integration and acceptance tests exist.

## Rollback

On this branch, reverting the commits after baseline 3a2a715fad1948e83dc7ebe0d307377ba29e065a removes the groundwork. On another branch, revert only commits cherry-picked from this series. No persistent extraction cache or overlay was created, so no data migration or cleanup is needed. Temporary capture directories in tests are operation-owned and removed. The primary /Users/thomi/Projects/entire-graph checkout remains unchanged.
