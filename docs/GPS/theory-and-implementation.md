# GPS Theory and Implementation

GPS extends Entire Graph with repository-local intent, traceability, and
verification evidence. It is designed to improve change understanding without
claiming that static analysis or a passing test proves arbitrary product
behavior.

## Theory

### The Problem

A code graph can identify symbols, relations, callers, dependencies, and
likely tests. It cannot know, without authored input, which product behavior a
symbol implements or whether a nearby test is the test a team considers
required.

GPS introduces explicit, versioned intent records and joins them to the
existing semantic graph:

```text
Requirement -> acceptance criterion -> declared test mapping
     |
     +-> reviewed anchor -> stable code symbol -> graph relations
```

The resulting output distinguishes three evidence classes:

| Class | Meaning | Examples |
| --- | --- | --- |
| Confirmed structural evidence | A selected repository input resolved deterministically. | A reviewed anchor resolves to a stable symbol; a parsed `CALLS` relation exists; a declared mapping resolves to one test symbol. |
| Heuristic or incomplete evidence | Evidence is a candidate, or analysis could have omitted facts. | Name-matched `Test*` candidate; partial parse failure; ambiguous symbol; candidate rebind. |
| Requires verification | The claim concerns behavior that static structure cannot prove. | A requirement is met; a schema is correct; a test passed; a route is reached at runtime. |

GPS never upgrades a candidate into approved intent. It also never treats a
declared mapping as a passing test.

### Authority Model

GPS has separate authorities rather than one blended confidence score:

| Input | Authority | What it establishes |
| --- | --- | --- |
| Specification YAML | Product intent | Requirements, acceptance criteria, and declared verification obligations. |
| Reviewed anchor binding | Traceability | The chosen implementation symbol and its approved baseline. |
| Semantic graph | Code structure | Parsed symbols, relation edges, callers, callees, and completeness diagnostics. |
| Verification baseline | Observed execution | Result from one explicitly authorized command under a recorded scope and policy. |
| Git history/checkpoint trailer | Local provenance | Historical context only; not proof of intent or correctness. |

This separation prevents common errors: a Git commit does not become a
requirement, a name-matched test does not become an approved mapping, and a
static graph edge does not become runtime proof.

### Safety Boundaries

- Analysis is local and no-egress.
- GPS does not use embeddings, hosted models, telemetry, remote grammar
  downloads, or a new service.
- `context`, `check`, `why`, `review`, and `impact` are read-only and never
  execute commands found in specs or source.
- Only `verify` executes a command, and only one explicitly supplied by the
  caller.
- Partial, ambiguous, unsupported, or changed inputs become visible gaps or
  incomplete results rather than silent certainty.

## Repository Model

GPS intent is stored beside a repository, not in a cache:

```text
.entire/graph/
  intent.yaml                 # policy and document roots
  specs/*.yaml                # specifications
  anchors/*.yaml              # reviewed bindings and baselines
  decisions/*.yaml            # optional authored decisions
  verification.yaml           # optional scope/command policy
```

`spec init` creates the base layout. It does not generate requirements or edit
application code.

### Intent Documents

Specifications contain a stable ID, title, intent, requirements, acceptance
criteria, anchors, declared tests, and optional relationships. Each acceptance
criterion belongs to a requirement. Each declared test maps to an acceptance
criterion. Anchors link requirements to implementation locations.

The `internal/intent` package enforces:

- versioned schemas;
- known YAML fields only;
- alias rejection;
- bounded document reads;
- safe, non-symlink recursive discovery;
- unique identifiers and valid local references;
- deterministic ordering and SHA-256 intent digests.

`spec validate` uses a separate aggregate validation path. It returns all
independently readable document diagnostics, with path, diagnostic code, and
message, while consumers such as `context` and `check` remain strict and refuse
an invalid intent set.

### Reviewed Anchors

An anchor binding stores:

- anchor ID;
- stable `compound-v1` symbol ID;
- qualified name, kind, and file selector;
- baseline fingerprint version;
- signature hash, container ID, body hash, and file blob.

`anchor bind` writes a binding only after exact symbol selection. Existing
bindings require `--update`; GPS never silently rewrites an approved baseline.

`anchor resolve` compares the stored baseline with a selected graph snapshot:

| State | Meaning |
| --- | --- |
| `VALID` | Stable symbol and baseline still match. |
| `CONTENT_DRIFT` | Symbol body changed while structure remains compatible. |
| `STRUCTURAL_DRIFT` | Signature, container, kind, or file identity changed. |
| `MISSING` | No symbol or candidate was found in complete relevant input. |
| `AMBIGUOUS` | More than one candidate could match. |
| `CANDIDATE_REBIND` | One cross-file/name candidate exists; it is not applied. |
| `UNVERIFIABLE` | Relevant parsing was partial, so absence cannot be trusted. |

## Command Architecture

GPS is implemented in `internal/cli/gps.go`, using `internal/intent` for
authored records, `internal/sem` for graph snapshots/search, and
`internal/gitutil` for selected revisions.

| Command | Implementation role |
| --- | --- |
| `spec init|list|show|validate|relationships` | Initialize and inspect strict authored intent. |
| `anchor bind|list|resolve` | Persist and inspect reviewed code bindings. |
| `context` | Build a bounded intent-aware package for a natural-language request. |
| `check` | Perform static traceability, drift, mapping, delta, and completeness checks. |
| `why` | Explain a symbol through anchors, requirements, tests, decisions, and optional history. |
| `review` | Project a base/head change into affected requirements, tests, and symbol deltas. |
| `impact --intent` | Add explicit bound anchor IDs to normal code-graph impact output. |
| `verify` | Record or compare explicitly authorized execution evidence. |

Existing code-only commands remain valid without GPS documents.

## Context Assembly

`context --query` performs the following steps:

1. Capture the selected repository view.
2. Load strict intent from the working tree or selected commit.
3. Lexically match requirements to the request.
4. Use `sem.SearchRepository` for ranked source evidence.
5. Resolve reviewed anchors for matched requirements.
6. Collect direct incoming and outgoing graph relations for valid anchors.
7. Add declared test mappings and separately add inferred `Test*` candidates.
8. Add linked authored decisions.
9. Apply deterministic section quotas and a serialized UTF-8 byte budget.
10. Return citations, reasons for inclusion, gaps, repository view, and
    evidence classification.

The quota sections are requirements, approved symbols, ranked code,
dependencies, declared tests, and inferred tests. If the response cannot fit,
GPS omits lower-priority material with an explicit omission list. The emergency
manifest stays within the declared budget and reports `BUDGET_TOO_SMALL`.

When no specifications exist, context still returns code evidence with the
`NO_SPECS` gap. This preserves code-only usefulness during adoption.

## Static Check and Delta Analysis

`check` does not execute tests. It reports findings and a disposition:

| Disposition | Meaning |
| --- | --- |
| `PASS` | Selected static checks completed without review findings. |
| `REVIEW_REQUIRED` | Traceability, drift, mapping, or delta evidence needs review. |
| `INCOMPLETE` | Parsing, capture, or input completeness prevents a reliable conclusion. |
| `FAIL` | Invalid or broken traceability prevents the requested join. |
| `NOT_CONFIGURED` | No selected GPS specifications exist. |

Checks include unbound anchors, anchor drift, missing acceptance mappings,
unresolved declared tests, graph completeness, and verification-evidence state.

With `--base ... --head`, GPS pins base and current revisions, loads both intent
sets and semantic snapshots, then compares bound-symbol fingerprints rather
than relying only on changed files. It reports symbol changes and deletions,
removed mappings, changed declared tests, intent changes, spec-only changes,
and code-only changes.

## Capture Integrity and Partial Analysis

Committed GPS operations capture `HEAD` commit/tree identity once. The intent,
snapshot, and delta inputs use that selected revision. If `HEAD` changes during
the operation, the response receives `GPS-INPUT-CHANGED` and an incomplete
result instead of a clean conclusion.

Snapshots carry provider warnings, partial failures, and completeness metadata.
GPS turns degraded or partial input into `GPS-COMPLETENESS-INCOMPLETE` and adds
an `heuristic_or_incomplete` evidence classification stating that relationships
may be absent. For complete snapshots, inferred candidates remain
`candidate_only`; confirmed parsed edges retain structural status.

## Verification Evidence

`verify` is the execution boundary. It receives a caller-provided command and
optionally setup command, captures normalized runner results, and writes a
baseline only to a caller-named path.

The baseline includes repository path, commit/tree when available, command,
scope, parser, platform, timeout, intent digest, optional verification-policy
incompatible repository, command, scope, parser, or policy inputs.

An optional `.entire/graph/verification.yaml` policy defines approved scope and
command metadata. A scoped policy never executes a command from the checkout;
it verifies that the caller explicitly supplied matching metadata.

`check --evidence PATH` and `review --evidence PATH` consume evidence without
executing anything. They classify it as `CURRENT`, `STALE`, `FAILED`, or
`UNAVAILABLE`.

## Explanation, Review, and Provenance

`why` joins a resolved symbol to its explicit anchors, requirements, declared
history plus `Entire-Checkpoint` trailers when available. If unavailable, it
returns `HISTORY_UNAVAILABLE`; history does not change code or intent
conclusions.

`review --base` reports changed files, affected requirements, declared tests,
and symbol-level deltas. It is a reviewer aid, not an approval engine.

## Example Workflow

```sh
entire graph spec init --repo .
entire graph spec validate --repo . --format json
entire graph anchor bind --repo . --id ANCHOR-OPENAPI --symbol get_openapi --file fastapi/openapi/utils.py
entire graph context --repo . --query "OpenAPI route schema" --format json
entire graph check --repo . --head --base HEAD~1 --format json
entire graph why --repo . --symbol get_openapi --file fastapi/openapi/utils.py --history --format json
```

After deliberately authorizing a test command:

```sh
entire graph verify --repo . --scope openapi --test "python -m pytest tests/test_openapi.py" --record-baseline /tmp/openapi.json
entire graph check --repo . --evidence /tmp/openapi.json --format json
```

The output should guide review: it identifies confirmed links, visible gaps,
and behavior that still needs a reviewer or a test. It does not approve a
change automatically.

## Testing and Compatibility

GPS has focused intent and CLI tests, Git-backed fixtures, negative fixtures,
partial-analysis coverage, and JSON golden contracts under
`internal/cli/testdata/gps/`. The token/auth fixture demonstrates the smallest
end-to-end workflow. FastAPI analysis documents provide an external
code-only-versus-GPS example.

GPS adds fields and commands without changing existing code-only snapshot
schemas or `compound-v1` symbol identity. Unsupported or partial analysis is
preserved as structured diagnostics, never silently dropped.
