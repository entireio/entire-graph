# GraphAudit

## One-sentence summary
A structural verification audit command that cross-references semantic diffs, static call graph impact, and test execution to identify unverified structural surfaces before changes are accepted.

## Problem, intended user and why it matters
**Intended User**: Autonomous coding agents and developers working in automated review or CI pipelines.  
**Problem**: Coding agents frequently execute a blanket test command (such as `go test ./...`), observe a 0 exit status, and prematurely conclude their specific code change is verified. A passing test suite provides zero proof that the specific AST entities modified or impacted by a change actually possess corresponding test evidence.  
**Why it matters**: GraphAudit acts as a "Zero-Evidence Green Test Detector," ensuring changes are not passed off as verified when their structural blast radius has no corresponding test callers.

## Selected Entire track and why Entire is essential
**Selected Track**: Track 2 — Build with Graph Intelligence.  
**Why Entire is Essential**: GraphAudit depends directly on Entire's fine-grained semantic AST diffs, symbol definitions, and structural relation graph (e.g. `CALLS`, `CONSTRUCTS`, `USES_TYPE`). Standard file-level diffs or text greps cannot correlate changed AST entities to structural callers or trace transitive impact to test symbols.

## Architecture and main workflow
```text
semantic diff
  → current-HEAD Entire Graph snapshot
  → changed entities
  → conservative structural impact
  → Audited Structural Surface
  → direct Go structural test evidence
  → graph completeness / diagnostics
  → optional explicit user-supplied test command
  → Verification Gaps
  → bounded audit result (BLOCKED / REVIEW REQUIRED / STRUCTURAL CHECKS SATISFIED)
```

## Entire Graph findings and verification
Existing Entire Graph primitives planned for reuse include semantic diffing (`diff`/`commit`/`checkpoint`), structural impact analysis (`impact`/`neighbors`), completeness/diagnostic reports, and subprocess verification execution (`verify`).

## Noon Curveball
Not yet incorporated in this pre-Curveball design snapshot.

## Checkpoint links
TBD

## Setup, run and test instructions
Implementation not yet started in this snapshot.

## Known limitations and next steps
- **Known Limitations**: Static AST analysis cannot verify runtime execution paths; interfaces, reflection, and dynamic dispatch cannot be fully resolved; V1 structural test detection is restricted to Go `*_test.go`; passing tests do not guarantee runtime correctness.
- **Next Steps**: Formalize the pre-Curveball checkpoint before integrating noon requirements.
