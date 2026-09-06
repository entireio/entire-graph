# Project Name

## Entire Graph GPS: Graph Parsing System

## One-Sentence Summary

Entire Graph GPS connects Git-authored requirements to stable code symbols,
change impact, and explicitly recorded verification evidence so developers and
agents can make changes with traceable, local-first evidence.

## Problem, Intended User and Why It Matters

Developers and coding agents can usually locate code, but they still have to
reconstruct why a symbol exists, which requirement it serves, what a change
affects, and whether the relevant test was actually run. This gap creates
plausible but weakly justified changes.

GPS is for teams using Entire Graph while maintaining production software. It
keeps requirements, acceptance criteria, implementation anchors, and test
mappings in the repository alongside code. This matters because a code search
result or a passing test alone is not proof that an intended behavior remains
correct. GPS makes available evidence and missing evidence visible for human
review.

## Selected Entire Track and Why Entire Is Essential

This project is built for the **Entire Graph** track. Entire Graph supplies the
local semantic graph: parsed files, stable symbol identities, relations, search,
impact analysis, diffs, and explicit test-command verification. GPS extends that
foundation; it does not create a second parser, hosted graph service, or new
product namespace.

Entire is essential for checkpointed development history and for the
`entire graph` plugin surface. GPS uses the graph's stable symbol IDs to make
reviewable anchors resilient to ordinary line movement, and it can expose local
checkpoint-trailer provenance without reading session transcripts or sending
repository data to a remote service.

## Architecture and Main Workflow

GPS has four repository-local layers:

1. **Intent:** strict YAML specifications define requirements, acceptance
   criteria, relationships, decisions, and declared test mappings.
2. **Anchors:** explicit bindings connect intent to a semantic code symbol and
   store signature, container, body, and file fingerprints.
3. **Graph context and checks:** GPS combines matching requirements, anchored
   symbols, ranked code snippets, dependencies, and test candidates into bounded
   cited context. Static checks report drift, broken mappings, incomplete graph
   evidence, and symbol-level change consequences.
4. **Execution evidence:** `verify` is the only execution boundary. A
   caller-authorized test command records its command, scope, repository view,
   policy and intent digests, result, and baseline compatibility.

Typical workflow:

```sh
entire graph spec init --repo .
entire graph spec validate --repo . --format json
entire graph anchor bind --repo . --id ANCHOR-AUTH --symbol Authenticate --file auth.go
entire graph context --repo . --query "change token lifetime" --format json
entire graph check --repo . --head --base HEAD~1 --format json
entire graph verify --repo . --scope auth --test "go test ./internal/auth" --record-baseline /tmp/auth.json
entire graph review --repo . --evidence /tmp/auth.json --format json
```

## Entire Graph Findings and Verification

The implementation is centered in `internal/cli/gps.go` and reuses
`internal/intent/`, `internal/sem/`, and `internal/gitutil/`. The repository's
FastAPI benchmark harness (`scripts/benchmark-fastapi-gps.sh`) contrasts a
code-only graph investigation with GPS: the latter requires requirements,
acceptance criteria, anchors, declared and inferred tests, gaps, and evidence
grades in its report.

The current implementation provides local specification validation, anchor
resolution and drift states, bounded context, base/head-aware checks,
intent-aware impact, local history explanation, review summaries, verification
policy handling, and evidence projection. It deliberately distinguishes:

| Evidence layer | What it establishes | What it does not establish |
| --- | --- | --- |
| Structural | Intent, anchor, and graph joins are valid. | Runtime behavior. |
| Mapping | An acceptance criterion has a declared or inferred test relationship. | That a test ran or covers every path. |
| Execution | An authorized command produced an observed result. | That every requirement is fulfilled. |

The implementation is covered by Go unit, integration, fixture, and golden
contract tests. This document itself was checked with `git diff --check` before
commit; the project-wide validation commands are listed below.

## Noon Curveball: What Changed and How We Adapted

The implementation evolved from a documentation-led GPS proposal into a
concrete, evidence-preserving feature set. The development checkpoints show
the scope expanding beyond intent and anchors to include bounded source context,
pinned repository views, incomplete-result reporting, inferred-test labeling,
verification-policy contracts, and review output.

We adapted by preserving a strict boundary: static graph operations never run
repository commands, while `verify` requires an explicitly authorized command
and records its provenance. We also made incomplete parser coverage, changed
inputs, ambiguous bindings, and stale evidence explicit findings rather than
silent success states.

## Checkpoint Links and What Each Checkpoint Proves

These checkpoints can be inspected locally with
`entire checkpoint explain <checkpoint-id>`:

| Checkpoint | What it proves |
| --- | --- |
| `0d9e2f29c3c6` | Initial intent and anchor command implementation. |
| `e671d4a384fc` | Static context evidence resolution. |
| `fdc8633200fd` | Pinned revision comparison for checks. |
| `f311f0c11263` | Fixture validation and context-evidence completion. |
| `303ac9263789` | FastAPI GPS benchmark harness. |
| `d86085fbb346` | GPS theory and implementation documentation. |
| `027adf421f68` | PDF-ready Graph Parser System documentation. |

For example:

```sh
entire checkpoint explain 303ac9263789
```

## Setup, Run and Test Instructions

Requirements: Go 1.26 and [mise](https://mise.jdx.dev/).

```sh
mise install
mise run build
mise run test
mise run check
```

`mise run build` produces the plugin binary. To install it into a local Entire
CLI, run `mise run install`; then use the `entire graph` commands shown above.

For the optional FastAPI comparison harness, provide a local FastAPI checkout,
a task file, and an executable graph binary:

```sh
scripts/benchmark-fastapi-gps.sh \
  --source /path/to/fastapi \
  --task-file docs/GPS/fastapi-benchmark-task.md \
  --graph-bin ./entire-graph
```

## Databricks Use, Data Sources and Limitations (If Applicable)

Databricks is not used in the local GPS implementation. GPS analyzes selected
repository files, Git revisions, authored specifications, semantic graph data,
and explicitly supplied verification evidence. It makes no network requests,
requires no API keys, uploads no telemetry, and does not use hosted inference.

Databricks or other external tooling may consume an opt-in export for evaluation,
analytics, or governance in the future, but it is outside the local parsing,
context, and checking path.

## Known Limitations and Next Steps

- GPS does not prove arbitrary natural-language requirements or complete runtime
  behavior.
- A declared or inferred test relationship is not measured coverage or a test
  pass; execution remains explicit and developer-authorized.
- Unsupported languages, parser failures, limited context, ambiguous anchors,
  and changed inputs produce incomplete or review-required findings.
- GPS is repository-local. It does not provide a daemon, dashboard, hosted
  service, automatic specification generation, or cross-repository graph.
- Future work includes broader language coverage, more fixture-based evaluation,
  optional external evaluation exports, and further usability work around
  intent authoring and review.

See `docs/GPS/README.md` and `GRAPH_PARSER_SYSTEM/README.md` for the detailed
design and implementation guide.
