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
| P1.2 | Minimal extraction/capture seam; explicit private declaration payload | extraction_record_test.go; evidence/seam-tests.txt; ADR 0030 | Operation-wide capture integration; resolver field audit beyond Entity; bounded store follow-up |
| P1.3 | Pending | None yet | Full task |
| P1.4 | Pending | None yet | Full task |
| P1.5 | Pending | None yet | Full task |
| P1.6 | Pending | None yet | Full task |
| P2.1 | Pending | None yet | Full task |
| P2.2 | Pending | None yet | Full task |
| P2.3 | Pending | None yet | Full task |
| P2.4 | Pending | None yet | Full task |
| P2.5 | Pending | None yet | Full task |
| P3.1 | Pending | None yet | Full task |
| P3.2 | Pending | None yet | Full task |
| P3.3 | Pending | None yet | Full task |
| P3.4 | Pending | None yet | Full task |
| P3.5 | Pending | None yet | Full task |
| P3.6 | Pending | None yet | Full task |
| P4.1 | Pending | None yet | Full task |
| P4.2 | Pending | None yet | Full task |
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

Stage A validation: focused seam tests pass. mise run check initially refused the newly created worktree config; after trusting the inspected mise.toml, the check started successfully. Full semantic race suite is still running; no full-check success claimed yet. The phase benchmark serialization timing overlaps enclosing provider phases and must not be added to them. Syntax-only worker baseline does not characterize full-profile relation parsing.
