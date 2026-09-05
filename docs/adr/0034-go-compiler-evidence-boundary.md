# ADR: compiler evidence contract; live execution unavailable

Status: accepted for protocol/import validation; live launcher unresolved
Date: 2026-09-05

Decision: keep compiler evidence separate from static RelationRecord. Bind imported evidence to a length-prefixed SHA-256 context identity containing captured source/dependency inputs, ordered build configuration, selected packages, adapter, toolchain and server versions. Distinguish direct declarations and implementation candidates. Validate caller and target digests/ranges exactly; an absent answer never disputes static evidence. No compiler labels are added to the public schema in this stage.

The live target remains Linux. This Darwin host has no installed gopls and cannot provide the required tested Linux read-only source/network-denying process-tree boundary. Do not start gopls, download dependencies or claim that environment settings are an isolation boundary. Selection and positive testing of a launcher and pinning a server remain P2.1 blockers; live mode is not implemented or enabled here. Protocol/import primitives are allowed by plan section 7 while that prerequisite is unresolved.

Implement LSP 3.17 UTF-16 positions from captured strings, with explicit refusal of invalid UTF-8, split surrogate pairs or offsets inside CRLF. Use bounded 8 MiB protocol bodies and 8 KiB headers. These stricter importer checks avoid opportunistically mapping malformed evidence to a nearby symbol.

Official sources consulted 2026-09-05: LSP 3.17 specification (HTML SHA-256 443b9cca4e49a77096de586de82089eca4e8b94aaa2c34f376b2d53684ea7f00); https://go.dev/gopls/features/navigation; https://go.dev/gopls/settings; https://go.dev/ref/mod. Navigation definition identifies a declaration; implementation matching is a candidate relation, not runtime dispatch proof. No server executable behavior is claimed.

Tests distinguish UTF-16/non-BMP/Unicode/CRLF offsets, malformed frames and oversized bodies, stale caller/dependency/build context, candidate versus direct evidence. Fake protocol fixtures are independently authored from the official framing specification and plan, not captured from another implementation.
