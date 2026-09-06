# Context and Impact

Status: Proposed design. See [the MVP](MVP.md).

## Goal

`entire graph context` is the GPS flagship command. It assembles the smallest
evidence-backed package needed to begin a change, rather than returning a broad
repository dump or declaring a solution.

```text
Request
  -> matched specifications and requirements
  -> approved anchor symbols and ranked code hits
  -> direct graph dependencies and callers
  -> declared and inferred test evidence
  -> related decisions and selected history, when configured
  -> risks, gaps, and verification obligations
```

It reuses existing search and impact primitives. It does not invoke tools,
execute tests, alter files, or infer developer intent silently.

## Context Package

The JSON contract is versioned independently from provider snapshots:

```json
{
  "schema_version": "1.0",
  "request": "Change token lifetime from one hour to one day.",
  "repository_view": {"head": "...", "worktree": true},
  "intent_digest": "sha256:...",
  "status": "complete_with_gaps",
  "specifications": [],
  "requirements": [],
  "symbols": [],
  "dependencies": [],
  "tests": [],
  "decisions": [],
  "history": [],
  "risks": [],
  "gaps": [],
  "verification": [],
  "budget": {"maximum_bytes": 12000, "rendered_bytes": 0, "omitted": []}
}
```

Every included item contains a stable ID where available, `file:line` citations
for source evidence, an inclusion reason, provenance, and confidence. Every
omission has a reason: budget, unsupported relation, unresolved anchor, partial
graph, or absent mapping. JSON is the integration contract; text is a readable
projection and must never make an uncertain statement sound conclusive.

## Deterministic Retrieval

The MVP does not need semantic embeddings or a model. It ranks deterministically:

1. Exact declared spec, requirement, anchor, and test ID matches.
2. Normalized lexical matches in specification fields and decisions.
3. Existing ranked code search results for the request.
4. Approved anchors attached to matched requirements and specifications.
5. Direct code relations and affected tests from existing graph traversal.
6. Related explicit spec relationships and decisions.

The output records match source and ranking scores. Ties use stable IDs, then
paths and source locations. A later learned overlay is an explicit input and
cannot replace deterministic eligibility, evidence, or authority.

## Context Budget

Context must honour a total serialized UTF-8 byte budget. The budget includes
every section, citations, diagnostics, and headers, not only source snippets.
It uses deterministic section quotas with documented carry-forward only when a
higher-priority section has unused capacity.

Priority order:

1. Direct requirements and acceptance criteria.
2. Approved anchor definitions and direct source snippets.
3. Direct dependencies, callers, and configuration boundaries.
4. Declared verification obligations and most relevant inferred test candidates.
5. Explicit decisions and selected history references.
6. Related specs and broader code context.

If direct intent and source cannot fit, return `BUDGET_TOO_SMALL` and a minimal
manifest rather than silently returning incomplete prose. Existing search budgets
are not automatically sufficient because some current sections use independent
caps; the new command owns its full-package accounting.

The implemented JSON quotas are requirements 25%, approved symbols 10%, ranked
source snippets 35%, dependencies 5%, declared tests 10%, and inferred tests
15%. `budget.section_quotas` records those values, and `rendered_bytes` is the
final serialized UTF-8 JSON size.

## Why and Impact

Future commands use the same join:

```sh
entire graph why --repo . --symbol AuthService.authenticate --format json
entire graph impact --repo . --symbol AuthService.authenticate --intent --format json
```

`why` answers with explicit traceability:

```text
AuthService.authenticate
  implements: REQ-AUTH-INVALID, REQ-AUTH-TOKEN
  specification: SPEC-AUTH-001
  verified by: TEST-AUTH-INVALID, TEST-AUTH-TOKEN
  decision: ADR-017
  code evidence: outgoing calls and source locations
  gaps: no executed result in the selected view
```

No relationship means no claim. `why` shows `NO_INTENT_LINK` for unanchored
symbols rather than fabricating rationale from names or history.

Intent-aware `impact` keeps existing code impact sections intact and adds:

| Section | Source | Meaning |
| --- | --- | --- |
| Specification impact | Approved bindings and spec relationships | Requirements whose linked implementation may change. |
| Verification impact | Declared mappings plus static test associations | Obligations and candidates to examine; not proof of coverage. |
| Decision impact | Explicit decision references | Rationale that may constrain a change. |
| Historical context | Selected Git or Entire evidence | Context only; historical correlation is not intent. |

The `--intent` mode must remain optional until its contract stabilizes. It must
include ordinary code graph partiality and intent resolution state in every
result.

## Risk Guidance

Risk is a ranked review signal, not a pass/fail conclusion or security label.
The initial score is deterministic and produces reasons instead of opaque math:

```text
HIGH
  - 14 downstream code dependencies
  - 3 affected requirements across 2 specifications
  - 1 endpoint in the direct call path
  - no declared test for ACC-AUTH-EXPIRY
  - target anchor has content drift
```

Possible signals are downstream relation count, affected declared specs,
critical typed boundaries such as endpoints/configuration, test-mapping gaps,
anchor drift, partial graph coverage, and explicitly configured criticality.
No historical change frequency, telemetry, user identity, or model suggestion
may affect MVP risk. The JSON result lists each contributing fact and versioned
policy values so it can be reproduced or challenged.

## History Boundary

Git semantic diff and bounded co-change data can be included as local evidence.
Entire checkpoints, session attribution, prompts, and tool calls must be supplied
by an explicit Entire integration or consumed through Brain. Context must label
these as `history`, not `specification` or `decision`, unless a developer linked
an authored decision to the relevant requirement.
