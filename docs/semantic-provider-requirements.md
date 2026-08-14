# Semantic Provider Requirements

This document describes the requirements for `entire-graph` to serve as the
semantic provider for Entire Brain.

`entire-graph` should remain responsible for parsing and semantic extraction.
Entire Brain should remain responsible for persistence, indexing, query
behavior, freshness policy, and agent presentation.

## Scope

`entire-graph` is an artifact-emitting provider. It parses source and emits
versioned semantic facts. It should not own the brain store, workspace model, or
agent UX.

Required responsibilities:

- Tree-sitter parsing.
- Entity extraction.
- Language-specific symbol models.
- Semantic diffs.
- Import, call, inheritance, field access, route, and tool relation extraction.
- Parser capability reporting.
- Partial failure reporting.
- Stable provider contracts for downstream consumers.

## No-egress constraint

Provider indexing is local-only. During indexing, `entire-graph` must not:

- Fetch remote code.
- Download grammars or parser assets.
- Upload telemetry.
- Call hosted model APIs.
- Call remote embedding providers.
- Perform implicit network discovery.

`doctor --json` should expose enough information for Entire Brain and CI to
assert that the provider can run without network egress.

## Provider integration commands

The provider and export surface includes:

```sh
entire graph doctor --json
entire graph version --json
entire graph capabilities --json
entire graph snapshot --repo . --format ndjson
entire graph symbols --repo . --format ndjson
entire graph edges --repo . --format ndjson
entire graph snapshot --repo . --format ndjson --worktree --ignore-file .brainignore
entire graph snapshot --repo . --format ndjson --worktree --include-file .graphinclude
entire graph diff --base main --head HEAD --json
```

This is not the complete user-facing CLI. Run `entire graph help` for the
current command registry and `entire graph <command> --help` for flags.

Whole-repo provider output uses newline-delimited JSON. `snapshot` also supports
the separate, full-snapshot-only `compact-ndjson` artifact described below;
`symbols` and `edges` accept only `ndjson`.

### Streaming NDJSON contract

The `snapshot`/`symbols`/`edges` commands stream records to stdout as they are
produced, so the stream no longer holds full relation payloads, their evidence,
or file contents in memory. Peak memory is bounded by the symbol/index metadata
plus the relation dedup set, which holds one compact 64-bit key per unique
relation. That dedup set still grows with the number of unique relations (the
remaining relation-count-scaled component), but at a constant per relation
rather than the full payload. The stream is emitted in this order:

1. exactly one header line (a record with `schema_version`),
2. `file` records, then `symbol` records (emitted per file as parsing
   progresses, before any relation is resolved),
3. `relation` records and the `external` endpoint records they reference,
4. exactly one trailing `summary` record (`record_type: "summary"`).

**The first header is intentionally lean.** It carries identity (`provider`,
`provider_version`, `repo_root`, `repo_key`, `commit`, `tree`), `schema_version`,
`capabilities`, `schema_features`, `language_versions`, and the **profile
metadata** — `profile`, `profile_limits`, `relation_set`, and
`skipped_relation_families`. Its `languages`, `warnings`, `partial_failures`,
`stats`, and `completeness` are empty/zero — those totals are not known until
the whole repository has been processed, and the header is emitted before that
so consumers can begin work immediately. The profile metadata is **header-only**:
it is known up front and is therefore not repeated in the summary.

**The final `summary` record is authoritative for aggregate metadata.** It
carries the real `languages`, `warnings`, `partial_failures`, `stats` (including
the `relations` count and `completeness_level`), and the `completeness`
breakdown. It does **not** carry profile metadata (`profile`, `profile_limits`,
`relation_set`, `skipped_relation_families`); consumers should read those from
the lean header and must not expect them in the summary unless a future schema
version adds them.

**Merging the two.** A consumer that wants one fully-populated header should
take the lean header and overlay the summary's aggregate fields (`languages`,
`warnings`, `partial_failures`, `stats`, `completeness`) on top of it — summary
wins for any field both records carry, the header wins for the profile metadata
the summary omits. The in-memory `BuildProviderSnapshot` path does exactly this
merge internally, so its single emitted header is fully populated. For any
aggregate total, read the summary, never the lean header.

### Compact snapshot NDJSON v1

`snapshot --format ndjson` remains the default interoperable object stream. `snapshot --format compact-ndjson` is a public, full-snapshot-only artifact with a separate cache mode, `snapshot:compact-ndjson-v1`; it is rejected for `symbols`, `edges`, and targeted `--to`/`--from`/`--relation` output. Its first line is `["h", 1, header]`, and the version appears nowhere else. Deterministic first-seen dictionary lines `d` precede positional `f` (file), `x` (external), `s` (symbol), and `r` (relation) rows; a trailing `m` summary is mandatory. Consumers must reject unknown versions, malformed row arity, non-first or duplicate headers, and missing summaries.

All `h`, `d`, data, and `m` bytes count as raw compact artifact bytes; dictionary overhead must never be subtracted. Compact output is loaded only through the production compact loader and queried with `snapshot-query --input <file> --symbol <id-or-name> [--from <stable-id> --relation <TYPE>] --format ndjson`, which writes deterministically ordered native symbol/relation records. Its decoded public projection and canonical semantic SHA-256 (normalized native records in record order) must equal the normal NDJSON snapshot. Matching only the hash is not sufficient evidence of losslessness.

### Process-local cold-build telemetry

The optional provider progress callback exposes only process-local performance
telemetry. Its typed phases are `inventory` (source preparation/file discovery),
`parse` (header output, registration aliases, file/symbol output, and index
construction), `relations` (relation resolution), and `finalize` (external
output plus trailing-summary construction and serialization). Each event carries
both elapsed time since that phase began and total provider-work time since
snapshot entry. Progress sampling and the synchronous caller callback are
measurement overhead and are excluded from both durations; the next phase clock
starts only after the prior terminal callback returns. The final event is sent
after the trailing summary has been emitted. These telemetry fields are not
snapshot schema fields and must never alter emitted semantic records.

**Ordering.** For a fixed input and profile the stream is deterministic and
stable (file, symbol, and relation order are reproducible across runs), but it
is not globally sorted the way the in-memory path sorts relations. Consumers
should key on record `id`/identity, not on stream position.

**Unknown record types.** Consumers must ignore record types they do not
recognize within a supported major schema version (forward compatibility for new
record types), and must not assume every line is a known type. A consumer that
reads only the header and relations, for example, should skip `file`, `symbol`,
`external`, and `summary` lines it does not need rather than erroring on them.

### Indexing profiles

`--profile full|fast|syntax-only` selects indexing depth. Provider snapshot
commands (`snapshot`, `symbols`, and `edges`) default to `full`; search defaults
to `fast` unless the caller selects a profile explicitly. The snapshot header
reports the selected `profile`, its `profile_limits` (evidence, call
resolution), the emitted `relation_set`, and the
`skipped_relation_families`; capabilities reports `relation_support_by_profile`.
Skipped families are always declared (in the header and capabilities) — a
profile never silently drops a relation family.

- `full` — the complete relation graph: `DEFINES`, `CONTAINS`, `IMPORTS`,
  `CALLS`, `CONSTRUCTS`, `ASYNC_CALLS`, `EXTENDS`, `INHERITS`, `IMPLEMENTS`,
  `OVERRIDES`, `USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`, `READS_FIELD`,
  `WRITES_FIELD`, `ACCESSES`, `HANDLES_ROUTE`, `HANDLES_GRPC`,
  `HANDLES_GRAPHQL`, `HANDLES_TRPC`, `HTTP_CALLS`, `EMITS`, `LISTENS_ON`,
  `HANDLES_TOOL`, `CONFIGURES`, `SIMILAR_TO`, `TESTS`,
  `RESOURCE_DEPENDS_ON`, `DATA_FLOWS`, and `FILE_CHANGES_WITH`, with full
  evidence. **Semantic-depth and accuracy claims belong to `full`.**
- `fast` — symbol inventory plus `DEFINES`, `CONTAINS`, `IMPORTS`, `CALLS`,
  `CONSTRUCTS`, and `CONFIGURES`; call resolution is shallow and limited to
  single-target, high-precision resolutions — same-file
  `exact`, unique same-package `package`, and import-bound
  `import_resolved`; name-only and pattern fanouts stay full-only. It also emits
  `HANDLES_ROUTE`, `HANDLES_TOOL`, and
  `RESOURCE_DEPENDS_ON`. Evidence is omitted and the deep families
  (type/field/similarity/HTTP/channel/test/uses-type/override) are skipped and
  their content scans avoided. **Speed/throughput claims belong to `fast`.**
- `syntax-only` — file/symbol inventory and structure (`DEFINES`, `CONTAINS`)
  only, plus warnings, partial failures, and freshness metadata. No relation
  resolution and no per-file content re-read.

Worktree provider snapshots should honor the repository root `.gitignore` before
walking or reading files. Callers may pass repeatable `--ignore-file <path>`
flags for additional gitignore-style exclusions such as `.brainignore`; relative
ignore-file paths resolve against `--repo`, and missing ignore files should fail
closed with a clear error. Callers may also pass repeatable `--include-file
<path>` flags containing gitignore-style inclusion rules. Include files are
applied after `.gitignore` and `--ignore-file`, so they can reopen otherwise
ignored paths.

## Schema Contract

Provider output uses `schema_version` in `major.minor` form.

Compatibility policy:

- Consumers refuse unknown major versions.
- Consumers may ignore unknown fields within a supported major version.
- If `entire-graph` emits a newer supported-major minor version, consumers should
  warn that some facts may have been skipped.
- Unknown relation types should use an extension namespace, such as
  `X-provider-name:RELATION`.

Provider records are typed. The streaming header and summary fields are defined
above; representative data records look like this:

```json
{"record_type":"file","id":"gh/org/repo:file:internal/auth/token.go","path":"internal/auth/token.go","blob":"..."}
{"record_type":"external","id":"external:import:net/http","kind":"import","value":"net/http"}
{"record_type":"symbol","id":"...","kind":"function","name":"ValidateToken"}
{"record_type":"relation","from_id":"...","to_id":"...","type":"CALLS"}
```

Relation endpoints may point to file records, symbol records, or external endpoint
records. Consumers should ignore unknown record types within the supported major
schema version, but should not assume every relation target is a symbol.

## Symbols

Symbols should include:

- `id`
- `stable_id_version`
- `kind`
- `name`
- `qualified_name`
- `file_path`
- `start_line`
- `end_line`
- `signature`
- `body_hash`
- `language`
- `container_id`

### Symbol kinds

Common kinds: `function`, `method`, `class`, `interface`, `struct`, `type`,
`enum`, `trait`, `field`, plus boundary kinds (`route`, `tool`, `workflow`) and
language-specific kinds (`message`, `service`, `rpc`, `table`, `block`, ...).

`field` is the canonical kind for declared data members of a struct, class,
interface, or record. Properties (e.g. C# properties, TypeScript accessors) map
to the same `field` kind when added, rather than a separate `property` kind, so
consumers have one kind to query. A field carries `container_id` (the enclosing
type symbol), a `signature` of its name and type text, and a `body_hash` of its
type text; its compound ID is stable across edits elsewhere in the file
(including method-body edits) because the ID does not encode line numbers.
Parameters and local variables are not fields and are not emitted as symbols.

Field extraction covers Go/Rust struct fields, Java/C# class fields (and C#
properties, mapped to `field`), and TypeScript class fields and
interface/type-literal properties. C/C++ struct/class fields are intentionally
not emitted because C/C++ field-access relations are not part of the advertised
relation matrix; emitting millions of C register/header fields adds indexing
cost without a consumed relation. Field extraction is declaration extraction
only — Python instance attributes and other inference-based members are out of
scope here and belong to later field-access inference.

The first stable symbol ID version should use a documented compound identity:

```text
<repo-key>:<language>:<file-path>:<kind>:<qualified-name>
```

This is stable across ordinary content edits. Duplicate same-name symbols are
disambiguated by signature hash plus a definition ordinal
(`...#sig:<hash>[#<n>]`) rather than source line ranges, so overloads keep
stable IDs across edits that shift line numbers. File moves and some renames are
reconciled in the semantic diff (see below) rather than in the snapshot ID.
`entire-graph` should document that breakage and emit enough diff data for later
rename reconciliation using body hash, signature similarity, and semantic diff
records.

If a change report spans a file rename or move that cannot be reconciled to
stable symbols, `entire-graph` should emit an explicit warning instead of silently
dropping edges.

Semantic diffs reconcile identity continuity and tag it with explicit
`reconciliation` metadata on each entity change:

- `RENAMED`: a same-file rename (delete+add reconciled by body/signature
  similarity at or above 0.92).
- `MOVED`: a symbol moved across files (a removed entity in one file matched to
  an added entity in another with similarity at or above 0.92). The change
  carries `old_path`/`new_path` and is reported on the destination file; if the
  name also changed, `old_name`/`new_name` are set.

When a move has multiple equally similar destinations (within 0.05), the
provider reports the pair as remove/add and emits a `W_MOVE_AMBIGUOUS` warning
in the diff `warnings` array rather than guessing.

## Relations

Relations should include:

- `from_id`
- `to_id`
- `type`
- `confidence`
- `reason`
- `warning_codes`

Schema `1.1` adds optional relation fields (additive; tolerant readers ignore
unknown fields):

- `relation_scope`: `file`, `module`, `workspace`, `external`.
- `resolution`: how the target was resolved, e.g. `exact`, `package`,
  `import_resolved`, `type_inferred` (receiver-type-inferred calls),
  `name_only`, `pattern`
  (later: `runtime_trace`, `unresolved`).
- `target_kind`: `symbol`, `file`, `external`, `route`, `resource`, `channel`,
  or `config`.
- `evidence`: array of compact `{kind, file_path, start_line, end_line, detail}`
  source pointers.

The snapshot header also carries optional `schema_features` (features present in
the stream), `language_versions` (parser/grammar versions), and `completeness`
(coverage by language and relation type).

Relation vocabulary:

- `DEFINES`
- `CONTAINS`
- `IMPORTS`
- `CALLS`
- `CONSTRUCTS` — a call expression constructs a known local type.
- `EXTENDS` — class extends class, interface extends interface, Rust supertrait.
- `INHERITS` — normalized inheritance edge emitted alongside language-specific
  `EXTENDS` or `IMPLEMENTS` facts where applicable.
- `IMPLEMENTS` — class implements interface, Rust `impl Trait for Type`.
- `OVERRIDES` — a method that redefines a same-named method on a resolved
  supertype (derived from EXTENDS/IMPLEMENTS; only when both the supertype and
  its methods are known local symbols).
- `READS_FIELD` / `WRITES_FIELD` / `ACCESSES` — a function/method reads, writes
  (assignment target), or takes the address of a field. The `receiver.field`
  access is resolved to a known local field via the receiver's type (this/self,
  a Go method receiver variable, or a constructor-assigned local). Accesses with
  an unresolved/dynamic receiver, or to a name that is not a known field, are
  skipped — no guessed edges. Bare implicit-`this` access (no `receiver.`) is
  not resolved in this pass.
- `USES_TYPE` — a function/method references a local type in its signature
  (resolved against known type symbols, so primitives and library types are
  excluded). This is the broad signature edge.
- `PARAM_TYPE` / `RETURNS_TYPE` — a function/method references a local type in
  parameter or return position. These positional edges are emitted only when the
  parser captured enough signature text to classify the reference.
- `HANDLES_ROUTE` — a handler registers an HTTP route (path on a line carrying
  routing context: a verb/route method call or mapping decorator).
- `HANDLES_GRPC` / `HANDLES_GRAPHQL` / `HANDLES_TRPC` — service boundary edges
  from protobuf RPC declarations, GraphQL operation literals, JS/TS GraphQL
  resolver-map fields and modular resolver root objects (`Query`, `Mutation`,
  `Subscription`), GraphQL schema root fields, and tRPC procedure declarations
  to stable external endpoint nodes.
- `HTTP_CALLS` — an outbound HTTP client call (fetch/axios/requests/httpx/http
  client) to a path. Client calls and route registrations to the same path
  share an `external:route:<path>` node, enabling client-to-route matching. When
  that static route has a local handler/boundary in the snapshot, the provider
  also emits a direct pattern-resolved `CALLS` edge from the client symbol to
  that handler/boundary symbol.
- `EMITS` / `LISTENS_ON` — pub/sub and event-emitter calls
  (`emit`/`publish`/`dispatch` and `on`/`subscribe`/`addEventListener`). Emitter
  and listener of the same name share an `external:channel:<name>` node. Weak
  naming-pattern detections: low confidence (0.6) with a `WEAK_PATTERN` code.
- `HANDLES_TOOL`
- `RESOURCE_DEPENDS_ON` — a Terraform/HCL block (resource/module) that
  references another block (e.g. `aws_vpc.main.id`, `var.cidr`) depends on it.
  Blocks are indexed by their referenceable name and references resolved within
  the module.
- `CONFIGURES` — configuration artifacts point at stable external config nodes:
  HCL blocks, Dockerfile stages, Kubernetes-looking YAML sections, and GitHub
  Actions workflow jobs, Kustomize sections, common JSON/TOML/XML project
  configuration, and Make targets.
- `DATA_FLOWS` — high-confidence local return-flow edge from a callee to a
  caller when a callable returns the result of another resolved callable, plus
  direct, branch, conditional/fallback, and expression-assigned local
  assignment-then-return cases, plus
  conservative local caller-to-callee forwarding for exact/import-resolved
  parameter, alias, destructured alias, object-field/object-literal, and
  collection-element cases.
- `ASYNC_CALLS` — async call-site edge for language-level async constructs such
  as Go `go` statements, JavaScript/TypeScript/Python `await`, and common
  spawn/promise patterns when the target resolves to a known symbol.
- `FILE_CHANGES_WITH` — bounded local git co-change edge between files that
  repeatedly changed together in recent history.
- `TESTS` — a test function maps to the unit it covers by naming convention
  (`TestFoo`/`testFoo` → `Foo`, `test_foo` → `foo`, `FooTest`/`FooSpec` → `Foo`)
  when the subject resolves to a non-test function/method/type.
- `SIMILAR_TO` — near-duplicate symbol bodies, found by MinHash+LSH over
  normalized function/method bodies. Tiny bodies are suppressed and only pairs
  above an estimated-Jaccard threshold are emitted, with the estimate as
  confidence. Local-only; advertised as the `near_clone_detection` feature.

`EXTENDS`/`IMPLEMENTS` are extracted from class/interface headers (Java,
TypeScript, JavaScript, C#, PHP, Python) and from Rust impl/supertrait syntax,
resolved to a local type symbol when one exists or an external `type` endpoint
otherwise. C# cannot syntactically separate a base class from interfaces, so it
uses the `I<Upper>` naming heuristic at lower confidence. Per-language support
is reported in `capabilities` under `relation_support_by_language`.

Relation extraction continues to grow. Remaining known expansion areas are
deeper fallback-format semantics and deeper flow analysis; the current contract
already emits positional type, field-access, async, service-boundary,
configuration, high-confidence direct, assigned, branch-assigned, and simple
conditional/fallback return-flow plus expression assignment-then-return flow,
bounded co-change edges, and lightweight inventory for common web/document/
config formats.

## Warnings And Partial Failures

Warnings and partial failures must be machine-readable. Free-form strings are
allowed as human detail, but every warning needs:

- stable code
- severity
- file path when applicable
- effect on semantic completeness
- optional human detail

One parser failure should not fail a whole-repo snapshot. The provider should
emit partial failures and continue where possible.

Impact-sensitive consumers need parse-failure thresholds, so `entire-graph` should
report enough aggregate stats to classify downstream reports as `ok`,
`degraded`, or `unsafe`.

No facts are dropped silently. A profile that omits relation families declares
them in the header (`skipped_relation_families`) and in capabilities; a file
that cannot be parsed or read emits a machine-readable partial failure. The
provider applies a per-file parser input cap to avoid pathological generated
files dominating large-repo runs. Files above the cap still emit file records,
but symbol parsing is skipped and an `E_FILE_TOO_LARGE` partial failure is
reported.

## Listing And Memory Bounds

A snapshot's memory must be set by the caller's limits, not by what happens to be
on disk. Two bounds are enforced, and both are visible in the output:

- **Per-file read cap** — the same limit as the parser input cap (`MaxParseBytes`,
  4 MiB by default). A file above it is never materialized in memory: its size,
  content hash and line count come from a constant-memory streamed digest, it
  emits a file record plus `E_FILE_TOO_LARGE`, and no consumer (including search
  preselection, which scans every listed file concurrently) can read it. Without
  this cap a repository's peak memory is set by its largest file, at twice that
  file's size per read.
- **Listing cap** — `MaxFiles` (200,000 by default, `ENTIRE_GRAPH_MAX_FILES`
  overrides; negative removes it). Truncation is deterministic in sorted path
  order and always reported as `W_FILE_LIMIT`, naming the real count, the limit
  and the override.

The working-tree listing is Git's own view of the working tree (tracked files plus
untracked files no exclude rule covers). Every exclude source Git applies — nested
`.gitignore` files, `.git/info/exclude`, per-worktree excludes, `core.excludesFile`
— therefore applies to the graph. A filesystem walk that honours the ignore stack
per directory is the fallback for a tree Git cannot enumerate.

## Capability Reporting

`entire graph capabilities --json` should report:

- supported file extensions
- supported languages
- parser versions
- supported relation types
- relation support per language (`relation_support_by_language`): the relation
  types extractable for each language. `DEFINES` and `CONTAINS` are structural
  for recognized semantic and inventory-only entries; deeper relations are
  listed only where their scanner or extractor exists.
- relation support per profile (`relation_support_by_profile`): the relation
  types each indexing profile emits (`full`, `fast`, `syntax-only`).
- heuristic relation types (`heuristic_relation_types`): relations such as
  `HANDLES_ROUTE` and `HANDLES_TOOL` that are detected by file-path and body
  patterns rather than per-language grammar, so they are not attributed to a
  single language.
- unsupported-but-detected language hints when available
- whether optional local-only features are available
- whether any feature would require network access

The current globally pattern-driven set is reported as
`heuristic_relation_types` (`HANDLES_ROUTE`, `HTTP_CALLS`, `EMITS`, `LISTENS_ON`, `HANDLES_TOOL`,
`SIMILAR_TO`, `TESTS`). A test keeps this list aligned with
`capabilities --json`.

## Tests

Required provider-side tests:

- Golden NDJSON tests for files, symbols, relations, warnings, and partial
  failures.
- Per-language parser fixtures.
- Schema compatibility tests.
- Capability output tests.
- Provider-absent and malformed-output tests at the integration boundary.
- Stable symbol ID tests across content edits.
- Move/rename tests that document known ID breakage or continuity.
- Relation extraction tests for the current relation vocabulary.
- Partial failure tests proving one bad file does not fail the whole snapshot.
- No-egress tests for provider operation.
- Performance smoke tests for medium repositories.
- Memory budget tests for cold snapshots.

## Current Foundation

Useful existing foundation:

- Go implementation.
- Isolated tree-sitter parser boundary.
- Parser-backed semantic support for 36 languages. The generated list lives in
  [language support](language-support.md), and `capabilities --json` is
  authoritative.
- Lightweight deterministic inventory support for 149 reported
  languages/filetypes, for 185 recognized names in total. Inventory-only entries emit file/symbol structure but do
  not claim call/type/data-flow analysis. The capabilities JSON exposes this
  distinction through `semantic_languages` and `inventory_only_languages`; see
  [language support](language-support.md) for the current generated matrix.
- Entity signature and body-hash comparison.
- Checkpoint-aware semantic diffs.

Current implemented foundation:

- Whole-repo NDJSON snapshot output with schema headers.
- Machine-readable provider capability and doctor commands.
- Stable `compound-v1` symbol IDs for ordinary body/signature edits.
- File, symbol, external endpoint, and relation records.
- Stable warning and partial-failure records for unsupported files, syntax errors,
  missing `HEAD`, and explicit working-tree snapshots.

Remaining gaps:

- Relation extraction is still intentionally heuristic.
- Snapshot IDs change when their file path or qualified name changes. Semantic
  diffs reconcile high-confidence moves and renames at the documented `0.92`
  threshold and warn instead of guessing when candidates are ambiguous; weaker
  and duplicate-name cases remain limited.
- `IMPLEMENTS`, `EXTENDS`, `OVERRIDES`, `ACCESSES`, field-access,
  data-flow, service-boundary, config, and resource-dependency relation
  families are implemented for supported high-confidence cases, but they remain
  heuristic and not compiler/type-checker complete.
- Performance and memory budgets need larger benchmark coverage.
