# Graph advantage implementation ledger

Requirements: `/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md` (2026-09-05), read in full.
Baseline: `3a2a715fad1948e83dc7ebe0d307377ba29e065a`, fetched from origin/main on 2026-09-05.
Branch: `codex/graph-advantage`. Primary checkout preserved; isolated worktree.

## Source and fixture provenance

This task did not consult prior conversations, memory files, competitors, comparison documents, or adjacent evidence. System-provided context includes general prior-work summaries; none is used as implementation evidence. No claim of an independently certified history is made.

Consulted Entire sources at the baseline: root AGENTS.md and .entire/graph-agent.md; docs/adr/0001-ga-schema-contract.md; docs/trust-and-security.md; mise.toml; internal/sem/{provider_parallel.go,provider_parallel_test.go,provider.go,model.go,parser.go,search_cache.go}; internal/bench/bench.go; cmd/graph-bench/main.go. Graph search located processProviderFile. These establish extraction, metadata, ordered reduction, cache refusal, and measurement boundaries. Repository files subsequently changed or named in tests are also implementation evidence; no external correctness oracle is used.

New fixture origins: authored directly from P1-A/P1-C and private metadata contracts. Hand-derived expected declarations and exact round-trip equality; existing Entire parser provides baseline equivalence, not an external product.

## Tasks

| Task | Implementation / status | Tests and evidence | Remaining |
|---|---|---|---|
| P1.1 | Characterization started; extraction phase benchmark added | evidence/baseline-bench.txt, baseline.cpu.pprof, baseline-cpu*.txt, baseline-environment.txt, seam-phases.txt | Separate IO/extraction/resolution costs; pinned release corpus and controlled reader schedule across the full operation |
| P1.2 | Partial: minimal extraction seam and bounded operation-store primitive | extraction_record_test.go, captured_store_test.go; seam-tests.txt, capture-store-tests.txt, focused-final.txt; ADRs 0030/0031 | Integrate one capture lifetime through prefix/oversize/context reads, resolution and rendering; audit all resolver fields |
| P1.3 | Pending | None yet | Full task |
| P1.4 | Pending | None yet | Full task |
| P1.5 | Pending | None yet | Full task |
| P1.6 | Pending | None yet | Full task |
| P2.1 | Blocked for live feasibility; evidence ADR written | ADR 0034; Darwin host and absent gopls recorded | Select/test Linux isolation launcher and pin actual server; positive executable probe |
| P2.2 | Partial: framing, UTF-16 mapping, context identity primitives | internal/compiler; compiler-primitives-tests.txt, focused-final.txt | Immutable capsule, complete protocol lifecycle/client, deadlines and process-tree cleanup |
| P2.3 | Pending | None yet | Full task |
| P2.4 | Partial: input-bound evidence validation; categories remain distinct | internal/compiler/identity.go and compiler_test.go | Actual symbol mapping, per-call-site reconciliation, public schema/projection fixtures |
| P2.5 | Pending | None yet | Full task |
| P3.1 | Partial: conservative relation policy defined; defaults untouched | ADR 0032; real Go/TypeScript/Python source-chain fixtures | Field/value/endpoint/resource compositions, configuration fixtures and default-output golds |
| P3.2 | Partial: deterministic level-order bounded traversal core | impact_paths.go; impact-core-tests.txt, impact-source-tests.txt | Immutable adjacency reuse and full composition policies; realistic performance gates |
| P3.3 | Partial: parallel evidence and stronger alternative paths; terminal tests and stop codes | impact_paths_test.go including shuffled hub, cycle and diamond fixtures | Output integration, omitted-path counts, byte-budget truncation notice and compiler provenance |
| P3.4 | Pending | None yet | Full task |
| P3.5 | Pending | None yet | Full task |
| P3.6 | Pending | None yet | Full task |
| P4.1 | Partial: preliminary evaluation manifest before tuning | evaluation-manifest.md; ADR 0033 | Captured retrieval baseline, adjudicated labels and repository-disjoint splits in GraphMark |
| P4.2 | Partial: pure rerank-only PageRank core with bounded topology | search_graphrank.go and tests; experimental-core-tests.txt, focused-final.txt | Broader hub/exact-match fixtures and query integration; no retrieval-quality gate passed |
| P4.3 | Pending | None yet | Full task |
| P4.4 | Pending | None yet | Full task |
| P4.5 | Pending | None yet | Full task |
| P4.6 | Pending | None yet | Full task |

## Release gates

All four workstream release gates remain unpassed. Defaults remain unchanged. No performance or quality advantage is claimed.

Known environment limitations: host is Darwin/arm64, whereas P2 live execution requires a tested Linux process boundary; gopls is not installed on this host. Linux positive/no-egress tests cannot be claimed from this host. GraphMark corpus, reviewer labels, held-out and powered agent evaluations are separate deliverables and have not been run.

## Rollback

Revert this branch's commits to restore baseline implementation. No cache migration, installation, generated summary, or Brain changes are introduced by the initial seam.

## Touchpoint revalidation

At current main: processProviderFile remains in provider_parallel.go; parseWithProfile at provider.go:1407; prepareSource at 1650; entitySymbols at 1818; forEachRelation at 4509. parseImpactFlags and buildImpactResponseFromReader remain at impact.go:181/330; expandGraphCandidates remains at search.go:3288. cache_policy.go retains captured-policy validation; cache_entry.go retains confined atomic storage. All plan areas exist; current main added parallel pipeline changes after the plan baseline.

Stage A validation: focused seam tests pass. mise run check initially refused the newly created worktree config; after trusting the inspected mise.toml, the check started successfully. The Stage A full check passed in 634.69 seconds; see evidence/check-stage-a.txt. The final check on product-code commit a70a1892 passed in 730.00 seconds (evidence/check-a70a1892.txt). No release gate is inferred from this test pass. The phase benchmark serialization timing overlaps enclosing provider phases and must not be added to them. Syntax-only worker baseline does not characterize full-profile relation parsing.

## Additional source and fixture ledger

- `cache_entry.go` was inspected for its existing held-root atomic-write boundary; no extraction-cache storage implementation is added yet.
- `provider.go` TESTS/type/data-flow emission was inspected to verify direction. DATA_FLOWS composition is deliberately absent from the new core because symbol-level composition would lose value identity. Existing product behavior is untouched.
- LSP 3.17 specification: https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/ (retrieved 2026-09-05; HTML SHA-256 in ADR 0034), for framing, lifecycle and UTF-16/EOL contracts.
- Official https://go.dev/gopls/features/navigation, https://go.dev/gopls/settings and https://go.dev/ref/mod retrieved 2026-09-05 for declaration/candidate semantics and offline-tooling constraints. No executable gopls behavior verified.
- `compiler_test.go` uses fake version strings explicitly labeled fixture/fake, hand-authored frames and Unicode strings. These are protocol/import tests, not P2 positive-server evidence.
- `impact_paths_test.go` contains hand-authored graph facts plus new source-only Go, TypeScript and Python chains. Labels derive from the written calls. No external oracle or historical patch labels used.
- `search_graphrank_test.go` derives expected first/second iterations by hand from the plan recurrence. It is numerical correctness evidence only; no retrieval corpus or success-rate claim.

## Unfinished scope and honest admission boundary

This branch contains groundwork, not completion of the four workstreams. No extraction persistence/reuse, live compiler, deeper-impact CLI mode or graph-ranking CLI mode is exposed. P1.3-P1.6, live P2, P3 integration and complete policy, and P4 integration/evaluation remain unfinished. Ordinary engineering integration work is still outstanding; it is not all blocked by the platform or missing evaluation data. Only the compiler live-platform and external evaluation evidence are presently environmental prerequisites.

Do not promote or describe these primitives as shipped user capabilities. Next implementation stage is operation-wide capture and field audit before storage/reuse. Preserve the plan's reviewable separation and complete the required acceptance fixtures before promotion.

## Reviewable commit stages

- 58f663a6: P1.1 / minimal P1.2 seam and baseline artifacts.
- 839d8e41: bounded capture-store primitive and allocation-free default handoff; isolated baseline rerun.
- 338afe30: P2 evidence/protocol primitives; no live execution.
- b5eaed4b: P3 bounded structural traversal core.
- f0a4fa11: P4 pure experimental ranking core.
- 06a018c9: byte-exact evidence identities, including invalid-UTF-8 Git paths.
- a70a1892: predecessor-only retention and a separate 20000 materialized-step ceiling; long-chain output-bound fixture.

Local Linux probes: docker/colima/limactl are installed, but the Docker daemon is unreachable and colima is stopped. No Linux provisioning attempted. This refines the initial host limitation: a usable enforced Linux boundary has not been established, rather than asserting that this machine can never provide one.

See review-summary.md for the incomplete scope, continuation sequence and rollback. Development failures are retained separately in evidence/development-failures.txt.

## Final validation result

`mise run check` passed on a70a1892: formatting, vet, full race suite, build and 151 status-line checks. No product code changed while that final build/check ran. Final focused race tests and the exact graph VERIFY command also passed. Final documentation changes only record these results. All release gates remain unpassed, and all end-to-end workstream completion boxes remain open.

## Resumed implementation (2026-09-05)

User authorized Azure Linux/Windows VM provisioning. Dedicated test resource:
`rg-entire-graph-advantage-20260905/graph-validation-linux`, Ubuntu 22.04,
westus3, Standard_D4s_v5, no public IP. Existing VMs are untouched. Deallocate
this task-owned VM after validation; remove its resource group when its evidence
has been retained and it is no longer needed.

P1.2 now has an opt-in operation capture shared across search preselection,
snapshot resolution and rendering. Prefix/oversize acquisition uses one opened
file; memory spill is bounded and private. P1.3/P1.4 now have binary/content/path
keyed atomic per-repository entries, bounded decode and bounded quota scans;
only complete declaration extractions persist. Search and bulk snapshot expose
`--extraction-cache off|on`, default off. Snapshot caching remains bypassed for
all worktree operations. Opt-in search also bypasses committed snapshot reuse
so per-file telemetry cannot be confused with cached historical counters.

P1-A tests verify 3 cold parses, 0 parses/3 reuses unchanged, 1 parse/2 reuses
after declaration edit; exact full graph equality with uncached output includes
private metadata. The capture mutation fixture proves consistent snippets and
fresh subsequent operations. `p1-reuse-integration-race-v2.txt` passes the focused
race suite and required graph verification test. The earlier race log retains a
fixture error: a tiny all-file preselection did not actually read content before
the injected mutation. The corrected fixture forces lexical preselection before
mutation; this was not a product capture failure.

P1.5 relation-input families remain absent. P1.6 release evaluation remains
incomplete; the reproducible product-local paired harness covers independently
generated 12/120/600-file Go repositories, three profiles, cold/unchanged/one-edit,
30 alternating trials, full semantic comparison. It does not substitute for
pinned real repositories, remaining edit scenarios, cold process cost/RSS or
GraphMark evaluations. No advantage or release promotion is claimed.

P2.1 Linux feasibility is no longer blocked: the actual pinned gopls LSP probe
passed, with ENETUNREACH in a grandchild and EROFS for a source write. Source and
fixture origins are recorded in ADR 0034 and the probe script. Azure CLI help
was consulted solely for provisioning commands; installed packages/toolchains
came from their normal distribution channels, not implementation references.

Additional P1 source audit: every Entity field is explicitly represented in
extraction_record.go and guarded by reflection/round-trip tests. SymbolRecord
IDs, repository aliases, C++ specialization aliases, synthetic boundary symbols,
imports and relation outputs are recomputed by existing reducers from captured
source. Symbol parameter names/knownness, signature types/knownness, source byte
ranges, bodyless/local/C linkage fields originate in the explicit declaration
payload. No final symbol or relation identity is stored. Inputs that fail an
exact private-payload JSON round trip bypass persistence.
