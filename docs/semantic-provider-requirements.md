# Semantic Provider Requirements

This document describes the requirements `entire-graph` meets as the semantic
provider consumed by Entire Brain, and the ownership boundary between the two.

Entire Graph owns everything repository-local: tree-sitter parsing and
semantic extraction, repository indexing and its derivative caches, cache
freshness for one requested repository view, the interactive query surface
(`search`, `def`, `explain`, `neighbors`, `impact`), and distribution of the
agent instruction files via `init-agents`. Entire Brain owns durable and
cross-repository state: persistence of ingested snapshots, memory that
outlives one command, cross-project reconciliation, and the MCP/presentation
surfaces built on top. The boundary rationale is in
[Entire Brain and Entire Graph boundaries](brain-and-graph-boundaries.md).

## Scope

As a provider, `entire-graph` parses source and emits versioned semantic
facts for ingestion. It does not own the brain store, workspace model, or
cross-repository UX.

Provider responsibilities:

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

The provider's diagnostic output exposes enough information for Entire Brain
and CI to assert that it can run without network egress.

## Provider integration commands

The provider and export surface includes:

```sh
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

### Stream and artifact formats

The streaming NDJSON contract (record order, lean header vs authoritative
summary, ordering and unknown-record rules), the compact snapshot artifact,
and progress telemetry are specified in
[snapshot format](snapshot-format.md), which is the canonical format
reference.

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
ignored paths. Configuration-derived `core.excludesFile` is deliberately empty
at the Git subprocess boundary and is not part of the effective ignore policy.

## Schema Contract

Provider output uses `schema_version` in `major.minor` form.

Compatibility policy:

- Consumers refuse unknown major versions.
- Consumers may ignore unknown fields within a supported major version.
- If `entire-graph` emits a newer supported-major minor version, consumers should
  warn that some facts may have been skipped.
- Unknown relation types should use an extension namespace, such as
  `X-provider-name:RELATION`.

Provider records are typed; representative record examples and the header,
summary, and unknown-record rules are in
[snapshot format](snapshot-format.md).

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

## Git Subprocess Configuration Boundary

Every production Git subprocess must discard inherited repository-selection,
command-scope configuration injection, attribute-source and pathspec-control
variables, `GIT_TRACE*` output targets, and Git for Windows'
`GIT_REDIRECT_*` standard-stream targets. `GIT_TEXTDOMAINDIR` is also discarded
because Git probes that directory during startup. Those variables can otherwise
make Git open arbitrary paths or sockets, including UNC targets, or override the
explicit path and attribute semantics of machine-readable commands. Inherited
global and system Git configuration is disabled. To preserve intentionally
shared or foreign-owned repositories, protected command-scope `safe.directory`
entries authorize only the selected command directory and its ancestors: the
exact candidates Git can discover while walking upward from that directory.
The provider never uses the unrestricted `safe.directory=*` exception.
Repository-local configuration remains available for structural settings
required to read the selected repository, but the bounded metadata preflight
rejects active `[include]` and `[includeIf]` sections before Git starts,
including sections in `config.worktree` when `extensions.worktreeConfig`
enables it. The same preflight parses and checks an active `core.worktree` path.
It also rejects active partial-clone/promisor
configuration: `extensions.partialClone`, a true `remote.*.promisor`, or any
`remote.*.partialCloneFilter`. Git before 2.45 can inspect a local or UNC
promisor URL before applying the transport deny-list, so partial clones are
refused across the supported Git range rather than weakening no-egress on older
clients.

Command-scope configuration must set selected-path `safe.directory` entries,
`core.fsmonitor=false`,
`log.showSignature=false`, `log.mailmap=false`, `submodule.recurse=false`,
`core.excludesFile=` and `core.attributesFile=`, and pin `diff.orderFile` to the
platform null device. Thus Git cannot connect to its configured fsmonitor daemon,
trigger log signature verification, consult configuration-derived external
ignore/attribute/mailmap/diff-order files, or make grep enter a nested submodule
checkout. Machine-readable grep calls also force no line numbers, columns,
color, or full-name rewriting so repository configuration cannot change their
record grammar. Working-tree snapshot caching remains disabled rather than
running Git's conversion-aware status machinery, which can execute configured
clean or process filters. In-repository `.gitignore`, `.git/info/exclude`,
per-worktree excludes, and `.gitattributes` remain available; only the
configuration-derived external files are neutralized.

Before the first worktree Git command that recursively enumerates untracked
content, the provider completes a bounded traversal beneath a held repository
root. The preflight never reads or resolves nested case-insensitive `.git`
markers and refuses Git enumeration when it encounters one, a traversable
redirect, a mount boundary, an unreadable directory, cancellation, or a
traversal ceiling. A
refusal routes to the existing bounded filesystem fallback before
`git ls-files --others` can inspect a repository-controlled gitfile target.

Implicit repository discovery is the sole ceiling-sensitive path. When
`GIT_CEILING_DIRECTORIES` is present, the CLI applies its usable absolute entries
lexically in-process and never passes the raw list to Git, because Git's own
canonicalization could probe an off-volume or UNC entry. Commands operating on
an already selected repository discard the ceiling with every other inherited
repository-selection variable.

## Listing And Memory Bounds

A snapshot's retained graph memory is set by the caller's limits, not by what
happens to be on disk. Discovery that must inspect more than the retained file
set for security and policy fidelity has separate fixed raw-input bounds. The
following bounds are enforced:

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
- **Git worktree discovery cap** — each index, eligible-worktree, or explicitly
  requested ignored-worktree listing accepts at most 1,000,000 raw
  NUL-delimited records and 256 MiB including delimiters. These listings must
  inspect paths beyond `MaxFiles` to find later git-directory evidence before
  retaining a deterministic prefix. Crossing either bound terminates Git and
  fails the listing explicitly rather than buffering attacker-sized output or
  retrying through another unbounded path.
- **Git metadata validation cap** — before a production Git subprocess starts,
  the resolved Git administrative directory, common directory, and every
  recursively discovered alternate object store are walked from held directory
  handles. The walk accepts at most 2,000,000 entries and 256 MiB of aggregate
  metadata-path bytes relative to the held resolver root, so its bound is
  independent of the checkout's absolute path prefix. It refuses mount points,
  off-volume redirects, and special files, except for Git's own fsmonitor daemon
  socket while fsmonitor is explicitly disabled for the subprocess; crossing
  either bound fails closed and refuses the subprocess rather than validating an
  incomplete metadata tree.
  Immediately adjacent cache/HEAD/source setup helpers may reuse one
  repository-bound validation receipt carried by their context; it is neither
  global nor persisted beyond that operation setup.
- **Listed-directory observation cap** — the git-directory exclusion pass
  retains at most 200,000 unique ancestor directories and 64 MiB of their path
  bytes. Crossing either cap fails the listing explicitly; it never continues
  with incomplete pointer evidence.
- **Filesystem-fallback discovery cap** — a fallback traversal accepts at most
  1,000,000 raw directory entries and 256 MiB of their repository-relative path
  bytes, and retains at most 200,000 directories and 64 MiB of directory paths.
  Directories are read in batches of 256 rather than materialized by an
  unbounded `ReadDir`. Crossing any bound fails explicitly before a partial
  listing can be returned.
- **`.git`-pointer sweep budget** — 20,000 admitted directories and 20,000
  inspected directory entries per working-tree listing
  (`ENTIRE_GRAPH_SWEEP_DIR_BUDGET` overrides; 0 or negative removes it). The
  sweep that looks for a suppressed `.git` pointer descends the roots Git
  collapsed, ignored trees included, so its size is set by content Git omits — a
  `node_modules`, a package store, a build cache — and without a bound one query
  is a whole-tree scan of it. The traversal stays beneath a held repository root,
  refuses symlinks, reparse points, and mount points (Linux uses `openat2`
  no-crossing resolution when available, with a bounded mount-table preflight
  plus held no-follow descriptors on older kernels), and reports a refused
  directory as `W_GITDIR_SWEEP_UNREADABLE_DIRECTORY` rather than following it.
  Rooted path-component work is capped at 16 times the configured directory
  budget. The budget is one ledger for the whole listing, so
  splitting one large ignored tree into many, or flattening it into arbitrarily
  many files, buys no extra allowance, and
  exhaustion is not silence: it records hidden evidence, which makes every
  observed directory carrying a git directory's structure an exclusion target,
  and it is reported as `W_GITDIR_SWEEP_BUDGET` (`W_GITDIR_SWEEP_CANCELLED` when
  the caller's context ended it). Running the ledger out therefore produces a
  WIDER exclusion than a completed sweep, never a narrower one.
- **Git directory-listing raw-output cap** — 2,000,000 raw NUL-delimited
  records or 64 MiB including delimiters, charged before non-directory
  filenames are discarded. A pattern-only ignore can otherwise make
  `git ls-files --directory` emit one record per ignored file without spending
  the directory ledger. Crossing either cap terminates Git, treats the listing
  as incomplete, and selects the fail-closed derived sweep.
- **Aggregate gitfile read cap** — 64 MiB of `.git` and `commondir` pointer bytes
  per working-tree listing. Individual `.git` files retain Git's 1 MiB fidelity
  limit and `commondir` retains a 64 KiB lexical window, but those per-file
  allowances cannot multiply by 20,000 observations. Exhaustion records hidden
  evidence and emits `W_GITDIR_POINTER_READ_BUDGET`.

The working-tree listing is Git's own view of the working tree (tracked files plus
untracked files no effective exclude rule covers). Repository-controlled exclude
sources Git applies — nested `.gitignore` files, `.git/info/exclude`, and
per-worktree excludes — therefore apply to the graph. Configuration-derived
`core.excludesFile` is neutralized at command scope and does not apply. A
filesystem walk that honours the ignore stack per directory is the fallback for a
tree Git cannot enumerate. That fallback conservatively retains ambiguous
vendored directories and emits `W_GIT_WORKTREE_FALLBACK`, because it cannot
reproduce every Git-only policy and excluded files can therefore be present.

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
- Machine-readable provider capability and diagnostic commands.
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
