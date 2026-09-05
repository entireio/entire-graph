# ADR: private extraction and capture boundary

Status: accepted for P1.1 / minimal P1.2 only
Date: 2026-09-05

Decision: represent one successfully acquired file as an immutable string, normalized existing path, and SHA-256 digest. The initial seam captures only the already authorized bounded read in processProviderFile; it does not claim operation-wide source capture. Keep existing unsupported/oversize routing and failures unchanged. A private versioned declaration payload explicitly maps every Entity field, including metadata omitted from public JSON. The ordinary path hands owned entities directly to existing entitySymbols and synthetic boundary creation. Payload round-trip tests reconstruct the same entities; default-off persistence pays no payload allocation or serialization cost. No public schema fields change.

Alternatives: serializing Entity drops private metadata; serializing final SymbolRecord also captures repository-dependent identities. Persisting a working-tree snapshot violates the plan and cache policy. None is acceptable.

P1.2 follow-up prerequisite: an operation-owned 64 MiB bounded store with confined spill, shared prefix/content reads, contextual inputs, failure memoization and cleanup must replace all rereads before enabling reuse. Do not silently retain entire repositories in memory. Relation inputs are absent, not computed empty. P1.3 must add exact parser/build/options/path identity before persistence.

Distinguishing tests: reflection checklist over all Entity fields, JSON round trip including private booleans and nil versus empty parameter names, detached copies, all-profile parser equivalence, mutation after acquisition cannot change extraction. Existing oversize shebang test must pass. Public schema and snapshot-cache refusal remain unchanged.

Deterministic failure amendment: cache syntax failures only when the parser has
successfully produced a tree whose error nodes establish malformed input.
An explicit private ParseStatus bit carries that provenance; generic parser
failure, timeout, depth/resource truncation and IO remain nonpersistent. Inferring
determinism from the existing E_PARSE_ERROR code is unsafe because it also covers
native parser failures. Malformed fixture warm hits must preserve exact warnings,
failures and completeness.
