# GraphAudit Architecture

**Target Command**: `entire graph audit --base <ref> [--test "<command>"] [--json]`

---

## High-Level Execution Pipeline

GraphAudit operates as a deterministic decision layer over precomputed AST relationships and explicit test execution.

```text
       [ Base Ref ] ─── vs ─── [ HEAD Ref / Worktree ]
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │    semantic diff    │
                          └──────────┬──────────┘
                                     │ Changed AST Entities
                                     ▼
                          ┌─────────────────────┐
                          │   Graph snapshot    │
                          │   (Current HEAD)    │
                          └──────────┬──────────┘
                                     │ Symbol Definitions & Types
                                     ▼
                          ┌─────────────────────┐
                          │  structural impact  │
                          │ (CALLS, CONSTRUCTS, │
                          │     USES_TYPE)      │
                          └──────────┬──────────┘
                                     │ Transitive Direct Reach
                                     ▼
                          ┌─────────────────────┐
                          │  Audited Structural │
                          │       Surface       │
                          └──────────┬──────────┘
                                     │
                 ┌───────────────────┴───────────────────┐
                 ▼                                       ▼
    ┌─────────────────────────┐             ┌─────────────────────────┐
    │  structural test        │             │   completeness &        │
    │  evidence (*_test.go)   │             │   parser diagnostics    │
    └────────────┬────────────┘             └────────────┬────────────┘
                 │ Matched Tests                         │ Warnings / Drops
                 └───────────────────┬───────────────────┘
                                     ▼
                          ┌─────────────────────┐
                          │  verification gaps  │
                          └──────────┬──────────┘
                                     │
                                     ▼
                     [ Optional Explicit Test Command ]
                     (e.g., --test "go test ./...")
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │    audit result     │
                          │ (BLOCKED / REVIEW   │
                          │  REQUIRED / CHECKS  │
                          │     SATISFIED)      │
                          └─────────────────────┘
```

---

## Architectural Stages

1. **Semantic Diffing**:
   Compares target refs using Entire Graph's entity diffing engine to identify modified functions, methods, structs, and interface signatures rather than raw text hunks.

2. **Snapshot Lookup**:
   Resolves the current AST definitions, containers, and identifiers for the audited tree.

3. **Conservative Structural Impact**:
   Follows direct AST dependencies (`CALLS`, `CONSTRUCTS`, and immediate parameter/return `USES_TYPE`). Filters out ambient noise like co-change files and non-call siblings to prevent unmanageable gap lists.

4. **Audited Structural Surface Assembly**:
   The bound collection of production entities directly affected by the diff that require structural verification evidence.

5. **Structural Test Evidence Extraction**:
   Inspects `*_test.go` symbols in the graph to find incoming relationships directed at entities in the Audited Structural Surface.

6. **Completeness & Diagnostic Audit**:
   Checks whether the underlying parse produced any partial failures, syntax errors, or unindexed files in the affected scopes.

7. **Verification Gap Accounting**:
   Identifies all members of the Audited Structural Surface lacking incoming structural edges from test files.

8. **Decision Adjudication**:
   Evaluates gaps, completeness warnings, and the exit status of any supplied test command to emit a definitive terminal report or structured JSON.
