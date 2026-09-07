# Chapter 3: Architecture and Data Model

## Architecture

GPS is not a second parser or service. It composes existing Entire Graph
components with an intent layer:

```text
internal/intent     strict authored YAML, validation, bindings, digests
        |
internal/sem        symbols, definitions, relations, search, snapshots
        |
internal/gitutil    committed revisions, changed files, local history
        |
internal/cli/gps.go context, check, why, review, anchor and spec commands
```

All processing is local and no-egress. GPS does not use a hosted model,
telemetry, embeddings, remote grammars, or background service.

## Repository Inputs

GPS stores authored inputs under `.entire/graph/`:

```text
.entire/graph/
  intent.yaml                 selection policy and roots
  specs/*.yaml                requirements, acceptance criteria, test mappings
  anchors/*.yaml              reviewed symbol bindings and fingerprints
  decisions/*.yaml            optional rationale linked to specs or anchors
  verification.yaml           optional verification-scope metadata
```

`intent.yaml`, specs, anchors, and decisions are versioned source inputs. They
are not cache state and therefore remain reviewable through Git.

## Core Records

| Record | Purpose | Important fields |
| --- | --- | --- |
| Specification | States expected behavior. | Stable ID, intent, requirements, acceptance criteria. |
| Test mapping | Declares a verification obligation. | Test ID, acceptance ID, source-symbol selector. |
| Anchor | Connects a requirement to code. | Anchor ID and requirement ID. |
| Binding | Records the approved code target. | `compound-v1` symbol ID, selector, signature/body fingerprints. |
| Decision | Captures explicit rationale. | Decision ID, text, affected specs, anchors. |
| Execution evidence | Records test execution. | Command, scope, result, repository identity, intent digest. |

## Stable Identity and Drift

The semantic symbol ID is the primary code identity. A source location is a
citation, not a binding key. Bindings capture a signature hash, container ID,
body hash, and file blob so GPS can distinguish these states:

| State | Interpretation |
| --- | --- |
| `VALID` | Binding and baseline match. |
| `CONTENT_DRIFT` | Implementation body changed; review is needed. |
| `STRUCTURAL_DRIFT` | Signature, container, kind, or file changed. |
| `MISSING` | Bound symbol is absent from complete relevant analysis. |
| `AMBIGUOUS` | Multiple possible recovery targets exist. |
| `CANDIDATE_REBIND` | A possible replacement exists but is never adopted automatically. |
| `UNVERIFIABLE` | Partial parsing prevents a trustworthy absence claim. |

The binding remains unchanged until a reviewer explicitly runs `anchor bind
--update`. This makes refactoring and migrations reviewable instead of silently
rewriting traceability.
