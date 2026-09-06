# Chapter 5: Implementation Guide

## Command Surface

GPS is implemented under the existing `entire graph` namespace.

| Command | Implementation behavior |
| --- | --- |
| `spec init` | Creates `.entire/graph/intent.yaml`, `specs/`, and `anchors/`. |
| `spec validate` | Reports all independently readable YAML diagnostics. |
| `spec list`, `show`, `relationships` | Inspects authored intent. |
| `anchor bind` | Writes a reviewed binding after an exact symbol selection. |
| `anchor resolve` | Compares a binding baseline to the selected graph. |
| `context` | Produces bounded request-to-code and intent evidence. |
| `check` | Runs static traceability, mapping, drift, completeness, and delta checks. |
| `why` | Explains a symbol through explicit intent links and optional local history. |
| `review` | Projects a committed base/head diff onto requirements and declared tests. |
| `impact --intent` | Adds explicitly bound anchor IDs to normal graph impact output. |
| `verify` | Executes only a caller-supplied command and records execution evidence. |

## Context Assembly

`context --query` follows this deterministic sequence:

1. Capture the requested working-tree or committed repository view.
2. Load strict GPS intent from that same view.
3. Match requirements against the request.
4. Run ranked semantic code search.
5. Resolve selected reviewed anchors.
6. Collect direct incoming and outgoing relations for valid anchors.
7. Add declared test mappings and separately collect inferred test candidates.
8. Apply an output byte budget and report omissions or gaps.

The response contains `repository_view`, `intent_digest`, source citations,
inclusion reasons, evidence classification, gaps, and budget metadata. A small
budget does not silently drop facts: the response reports what was omitted.

## Change Analysis

For committed checks, GPS captures `HEAD` and its tree identity once, then loads
intent and graph evidence from that exact revision. If `HEAD` changes during the
operation, GPS reports `GPS-INPUT-CHANGED` and returns `INCOMPLETE`.

With `check --head --base <revision>`, GPS compares:

1. Intent-set digests and changed GPS documents.
2. Declared test mappings removed since the base revision.
3. Bound-symbol fingerprints in base and head snapshots.
4. Code-only and specification-only deltas.

`review --base <revision>` uses the same committed-view approach and returns
changed files, relevant symbol deltas, affected requirements, and their
declared tests.

## Test Evidence Handling

`verify` is deliberately the only execution boundary. A verification policy may
require scope metadata to match the caller's supplied command, but the policy
does not grant permission to execute a repository-supplied command. `check` and
`review` can read an evidence file and classify it as `CURRENT`, `STALE`,
`FAILED`, or `UNAVAILABLE`; they never execute tests themselves.

## Implementation Locations

| Location | Responsibility |
| --- | --- |
| `internal/cli/gps.go` | GPS command dispatch, joins, context budgets, findings, and revision integrity. |
| `internal/intent/intent.go` | Strict YAML models, validation, safe loading, canonical ordering, and digests. |
| `internal/intent/verification_policy.go` | Optional verification scope metadata validation. |
| `internal/cli/impact.go` | Intent-aware extension of normal impact output. |
| `internal/cli/verify.go` | Explicit command execution and baseline evidence. |
