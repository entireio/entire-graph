# GraphAudit — Pre-Curveball Design Snapshot

**Track**: Track 2 — Build with Graph Intelligence  
**Status**: Pre-Implementation Design Specification (Pre-Curveball)

---

## 1. Project Intent & Product Question

**Product Question**:
> “Given a semantic code change and the available structural/test evidence, what structurally affected surface remains unverified?”

Coding agents frequently run broad test commands (e.g., `go test ./...`), receive an exit code 0, and conclude that their specific modification is verified. In reality, a green test execution does not establish which changed or structurally affected entities have corresponding verification evidence.

**Core Differentiator**:
*Zero-Evidence Green Test Detector*: Detect when a user-supplied test command passes, yet Entire Graph identifies changed or affected structural entities for which no corresponding structural Go test evidence was established.

---

## 2. Terminology Contract

GraphAudit enforces strict terminology to prevent deceptive claims:

| Approved Terminology | Strictly Forbidden Terminology |
| :--- | :--- |
| Structural Verification Audit | Proof / Proven |
| Audited Structural Surface | Safe / Formally verified |
| Structural Test Evidence | Correctness guaranteed |
| Verification Gap | Fully covered |
| Unverified Structural Surface | Runtime coverage |
| Evidence Completeness | Zero coverage |
| `REVIEW REQUIRED` | Mathematical certainty |
| `BLOCKED` | Flawless |
| `STRUCTURAL CHECKS SATISFIED` | Bug-free |

---

## 3. Proposed Command & Operational Model

```bash
entire graph audit --base <ref> [--test "<command>"] [--json]
```

- **Scope**: Audits the currently checked-out `HEAD` relative to `--base <ref>`.
- **Mode**: CLI-native orchestration consuming static AST relations and optional test execution.

### Conceptual Workflow
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
  → bounded audit result
```

---

## 4. Result Semantics

The audit emits one of three strict verdicts:

1. **`BLOCKED`**:
   - An explicitly supplied test command was executed and exited with a non-zero exit status (test failure).

2. **`REVIEW REQUIRED`**:
   - No explicit test command was supplied where execution evidence is needed.
   - Relevant Graph/index analysis is incomplete, degraded, or reports parser warnings.
   - A changed symbol cannot be reliably mapped in the index.
   - An audited structural entity has no corresponding structural test evidence established.
   - Changes fall outside supported V1 language/capability capabilities (e.g., non-Go files).

3. **`STRUCTURAL CHECKS SATISFIED`**:
   - Relevant Graph analysis is confirmed complete with zero parser warnings or degraded scopes.
   - All changed symbols map unambiguously to AST entities.
   - Zero unresolved Verification Gaps remain across the entire Audited Structural Surface.
   - An explicit test command was executed and succeeded.

> **CRITICAL DISCLAIMER**: `STRUCTURAL CHECKS SATISFIED` explicitly does **NOT** denote runtime coverage, runtime safety, or semantic correctness. It confirms only that within the static call graph, structural test relationships and passing command execution were reconciled.

---

## 5. Audited Structural Surface & Go Test Evidence

### Bounding the Audited Surface
To prevent noisy false positives, V1 bounds the surface conservatively to direct, high-confidence relationships:
- Direct `CALLS`
- Direct `CONSTRUCTS`
- Selected strong type-consumer relationships (`USES_TYPE`, `PARAM_TYPE`, `RETURNS_TYPE`)

*Excluded from Mandatory Gaps*: Co-changing files, same-container siblings, unresolved dynamic calls, and heuristic associations.

### Go Test Evidence Contract
- Constrained strictly to Go source files (`*_test.go`).
- Requires a direct structural relationship between a test function/method and the audited production symbol.
- Absence of an edge means exclusively: *“No structurally related Go test evidence was established.”* It must never be interpreted as *“No test exists.”*

---

## 6. Planned Existing Entire Primitives for Reuse

GraphAudit is designed to orchestrate existing Entire Graph primitives without introducing redundant parsing engines:
- Semantic diff functionality (`diff` / `commit` / `checkpoint`).
- Current working tree and committed Graph snapshot indexing.
- Structural impact and blast-radius traversal (`impact` / `neighbors`).
- Completeness, diagnostics, and warning emitters.
- Command execution runner (`verify`).
- CLI dispatch and help formatting.

*(Note: These primitives are identified for reuse; integration into GraphAudit is not yet implemented.)*

---

## 7. Technical Risks

- **Interface Obfuscation**: In Go, calls dispatched through interfaces may not resolve to concrete implementations, potentially hiding callers.
- **Dynamic Dispatch & Reflection**: Cannot be resolved via static Tree-Sitter AST inspection.
- **Branch Blindness**: Static relationship does not verify whether execution traversed the specific edited lines or an early return.
- **Graph Incompleteness**: An absent edge does not establish absence of a dependency at runtime.

---

## 8. Explicit Non-Goals (Out of Scope Before Curveball)

- Runtime coverage calculation (no bytecode/tracing instrumentation).
- Correctness or safety proofs.
- Multi-language test framework heuristics beyond Go `*_test.go`.
- Automatic guessing or inference of test commands.
- External LLM evaluation or scoring sidecars.
- Python sidecars, web dashboards, or cloud database dependencies.
- Autonomous pull request merging or code generation.

---

## 9. Implementation Status at this Milestone

- **Completed**:
  - Track 2 product positioning and core problem framing.
  - Architecture pipeline design and result classification contract.
  - Primitive reuse identification within the Entire codebase.
  - Conservative risk analysis and test plan formulation.
  - Strict terminology boundaries and non-goals definition.
- **Not Completed (Implementation Not Started)**:
  - Native `audit` subcommand registration and Go implementation.
  - Test suites and test fixtures for GraphAudit.
  - Integration with existing `verify` or `impact` Go internal packages.
  - Live execution demonstration.
