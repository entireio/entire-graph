# ADR 0038 — additive opt-in query contracts

Decision: schema 1.2 adds optional extraction telemetry and compiler overlay to native snapshot headers. Search/impact envelope version 1 retains existing meanings and adds optional fields. Experimental candidate facts use `X-entire-graph:COMPILER_IMPLEMENTATION_CANDIDATE`; native static relations remain intact. Compact/SCIP refuse an overlay they cannot represent. Existing Result shape is unchanged; its schema-version guard advances with the provider.

Evidence: plan sections 2, 4 and 7; ADR 0001; current native encoders/cache validity checks. Alternatives: redefining CALLS or silently dropping compiler distinctions is rejected. Tests must exercise optional omission, native preservation, unavailable/required behavior, projection refusal and existing tolerant-reader fixtures.

Index remains a durable static HEAD cache. An explicit compiler request runs a fresh operation-local overlay after static indexing, verifies matching HEAD/tree identities, and includes evidence only in the returned index response. Compiler facts never enter the durable cache. Search, snapshot and impact are the other participating compiler verbs; def/explain/neighbors retain their existing static contract. Extraction reuse participates in search/snapshot/impact; index already reuses immutable committed snapshots and does not expose per-file reuse in this iteration.

Ranking remains `current` by default and `experimental-graph` by explicit request. Component scores appear only in diagnostic JSON. Deeper impact is requested through depth greater than two or `all`; normal depth one/two stays unchanged. Missing compiler source selection is explicitly unavailable and fails with require-compiler.

Rollback: select default flags; remove disposable extraction cache entries if desired. No persistent working-tree cache, migration, installation, generated report/summary change or downstream consumer change is required.
