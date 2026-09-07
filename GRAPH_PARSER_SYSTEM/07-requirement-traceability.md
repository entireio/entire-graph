# Chapter 7: Problem Statement Traceability

## Requirement-to-Implementation Matrix

| Problem-statement requirement | GPS capability | Evidence shown | User verification |
| --- | --- | --- | --- |
| Surface definitions, calls, types, routes, and semantic changes. | Existing semantic graph, `search`, `neighbors`, `impact`, `diff`; GPS reuses their snapshots. | Symbol IDs, relation records, source citations, completeness metadata. | Open cited source and use graph commands for focused inspection. |
| Turn graph evidence into a workflow. | `context`, `check`, `why`, and `review`. | Requirements, anchors, relations, tests, gaps, and dispositions. | Follow the disposition and inspect the cited records. |
| Impact-aware code review or change-risk analysis. | `review --base`, `check --head --base`, `impact --intent`. | Bound-symbol deltas, impacted requirements, test mappings, drift, and code/spec deltas. | Review affected symbols and declared tests; resolve findings. |
| Test selection based on affected relationships. | Declared acceptance-to-test mappings returned by `context` and `review`. | Mapping IDs, selectors, resolved test symbols; candidates explicitly marked inferred. | Run the caller-selected test through `verify` and attach evidence. |
| Migration planning or dependency-aware refactoring. | `impact --intent`, anchors, `anchor resolve`, semantic delta analysis. | Callers, callees, type consumers, linked requirements, binding drift. | Inspect graph paths, update code, then explicitly rebind only after review. |
| Codebase onboarding and guided exploration. | `context --query` and `why --symbol`. | Matched intent, approved symbols, ranked code, dependencies, decisions, and gaps. | Read cited code/specification and confirm declared links. |
| Combine graph findings with checkpoint intent. | `why --history` reads local Git history and `Entire-Checkpoint` trailers. | Commit subjects, checkpoint trailer values, availability status. | Use provenance to inspect the associated local checkpoint; do not treat it as intent. |
| Produce a useful decision or recommendation. | Versioned dispositions: `PASS`, `REVIEW_REQUIRED`, `INCOMPLETE`, `FAIL`, `NOT_CONFIGURED`. | Findings with severity, subject, message, input view, and intent digest. | Act on the disposition, not an opaque score. |
| Show evidence origin. | Evidence classification, repository view, intent digest, citations, anchor fingerprints, and execution baseline metadata. | Structural, heuristic/incomplete, and verification-required categories. | Trace each result to source YAML, source code, Git, or a test record. |
| Let the user verify against source or tests. | Citations plus separate `verify` execution boundary. | `file:line`, test selectors, exact command/evidence metadata. | Inspect the source and explicitly run the selected test command. |

## Acceptance Statement

GPS addresses the problem statement by making graph facts actionable but not
authoritative beyond their evidence class. It recommends a workflow, exposes
the inputs used to reach that recommendation, and preserves a human or agent's
ability to verify the conclusion against source code and test execution.
