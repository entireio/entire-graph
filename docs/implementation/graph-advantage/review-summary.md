# Graph advantage implementation phase review

Branch: `codex/graph-advantage`, based on fetched main `3a2a715fad1948e83dc7ebe0d307377ba29e065a`. No merge or default promotion.

The interim review's conversion and query-integration findings are fixed in code. This phase completes remaining implementation and correctness checks; it deliberately defers comparative benchmarks. The [task ledger](ledger.md) is the authoritative 23-task implementation/evidence map.

## Implementation changes

- **P1:** captured source and policy inputs, explicit declaration metadata, private content-addressed storage and fresh graph reconstruction. Raw imports are reused for Go/TypeScript/Python fast/full; other relation families remain explicitly uncached. Cross-process admission now protects cache quota decisions. Final policy mutation/spill and orphan-temp cleanup work is pending before source freeze.
- **P2:** bounded pinned Go analysis in Linux network isolation, exact source/configuration identity, precise declaration mapping and explicit coverage. Go type conversions, aliases and generic type conversions cannot become compiler-confirmed calls or dispute static facts. Ordinary search uses the effective compiler view for caller boosts, expansion and ranking.
- **P3:** all impact depths use the same effective relation view. Deeper traversal retains bounded valid paths and honest partial counts. Implementation candidates remain separate even when a covering-test rule makes their path terminal.
- **P4:** experimental ranking remains constrained by existing query scope, source/render budgets and deterministic fallback. Harness controls support later controlled ablations; no measurement campaign or outcome-based tuning occurs in this phase.

Schema additions remain optional. Native static records are preserved; compiler-enabled compact/SCIP projections refuse distinctions they cannot represent. Persistent working-tree snapshot reuse remains disabled.

## Correctness status

- **Final implementation commit:** PENDING_ROOT_FINAL_COMMIT
- **Immutable `mise run check`:** PENDING_ROOT_FINAL_CHECK
- **Pinned Linux correctness:** PENDING_ROOT_LINUX_CHECK

New focused impact regressions and race tests passed, covering compiler-off compatibility, redirects, caller/callee sections at all depths, candidate separation and terminal tests. Conversion and source/configuration regressions accompany the compiler fixes. The new Linux ordinary-query fixture compiled on Darwin and skipped there; this is not positive backend evidence.

The previous complete check at `0038ef70` passed in 626.23s. It remains historical evidence and does not certify the newer changes. Final source identity, exact checks and Linux raw responses must be filled above after the new immutable integration run.

## Release status

**No full release gate has passed.** Implementation completion does not authorize default enablement or performance/quality claims.

| Workstream | Retained result | Outstanding gate |
|---|---|---|
| P1 | Earlier semantic comparisons passed; cold performance regressed | Fixed real-corpus/edit/RSS matrix and performance target |
| P2 | Positive pinned compiler/boundary fixtures exist | Final regression run and independently adjudicated hard-Go quality |
| P3 | Contract paths and bounds have focused correctness evidence | Final integration and realistic affected-site precision/recall/cost |
| P4 | Earlier development recall gain was zero | Adjudicated development, disjoint holdout and conditional agent study |

Existing results and failures remain intact. Comparative evaluation is deferred by instruction, including performance sweeps, retrieval studies and agent experiments.

## Functional limitations

Compiler execution is tested Linux-only. Unsupported external dependency closure and dynamic runtime targets remain explicit partial/unavailable or candidate results. Captured inputs are observed bytes, not an atomic repository revision; Git listing/global configuration and metadata coverage limits remain explicit. The capture memory bound excludes total process RSS. Relation-input reuse covers the measured import family only. P1's final mutation/spill/admission audit must finish before implementation is declared frozen.

## Proposed next phase

Freeze final code, environments and prospective evaluation manifests after correctness passes. Run P1's fixed paired workload matrix first; then adjudicated P2/P3 target/path studies. Run P4 development ablations with identical capture, expansion and byte budgets, freeze the selected configuration, and evaluate a disjoint holdout. Run the powered paired agent experiment only after retrieval clears its prerequisite gate. Preserve failed and inconclusive outcomes.

## Rollback

Use `--extraction-cache off`, `--compiler off`, `--depth 2`, and `--ranking current`. Extraction records are disposable in their separate namespace; compiler/ranking state is operation-local. No migration is required. Revert reviewable commits to remove the implementation. Installation, MCP, generated summaries and Brain remain untouched.

Sources and independently authored fixture origins are recorded in the ledger and existing ADR/evidence manifests. The explicitly authorized interim review is included; competitor implementations, prior conversations and memory are not implementation sources.
