# P4 implementation and gate review

Baseline branch commit: `29520508be39f937193b6b034b5792d2cc56be9c`; integration evidence identifies the dirty source by per-file SHA-256 and the compiled executable SHA-256 in GraphMark `advantage-ranking-20260905/results/development-v1/provenance.json`.

| Task | Implementation and evidence | Remaining gate |
|---|---|---|
| P4.1 frozen baseline | Fresh GraphMark manifest, six independently authored development repositories, exact source hashes, required-symbol labels, captured outputs and binary/script provenance | Independent label adjudication and repository-disjoint held-out collection remain absent |
| P4.2 pure ranking | `search_graphrank.go`; hand-derived recurrence, unit mass, invalid weights, duplicate/scope invariance, empty/capped fallback, candidate hub and transition bounds | No production quality inference from numerical correctness |
| P4.3 constrained integration | Explicit `experimental-graph`; captured input lifetime; candidate-only rerank; exact/deep fallback; source/guidance and current query parity tests | Shared CLI/schema checks handled by integration stage |
| P4.4 development ablations | 120 CLI current/weighted observations plus 180 full-retrieval current/uniform/weighted observations; identical-expansion identity control; four pure-core graph fixtures; frozen parameters unchanged | All three unconditional retrieval arms executed; conditional winner-plus-compiler arm not run because no retrieval winner passed its gate |
| P4.5 held-out evaluation | Protocol and thresholds recorded; no held-out outcome fabricated | Development recall improvement is 0%; held-out corpus/adjudication prerequisite missing |
| P4.6 downstream gate | Prospective sample-size planning assumption and paired/cluster protocol recorded | Conditional agent study not run; retrieval prerequisite has not passed; current stays default |

Focused check: `go test -race ./internal/sem -run 'TestGraphRank|TestPageRank|TestSearchCacheCaptureBudgetFallsBackToUncachedBuild' -count=1`; retained at `evidence/p4-focused-tests.txt`. The named search VERIFY check is included. Fixtures cover operation mutation after preselection, next-query freshness, exact-symbol whole-result/verification parity, excluded scope, component consistency, deep fallback, hub exclusion, and explicit graph-work fallback.

The six synthetic development repositories produced no query failures. Both modes had recall 1.0 and complete-site coverage 1.0 under author labels, so relative recall gain was 0%, below 5%. Twelve sampled text renders satisfied the 4096-byte limit. Secondary MRR/nDCG and timing diagnostics remain development observations; no held-out or agent benefit is claimed. The raw timing difference also includes capture/preselection-path changes and is not isolated ranker acceleration. The small ceiling-limited fixture set is unsuitable for a production noninferiority conclusion.

Contract delta: optional `results[].ranking` components and `stats.ranking` work/fallback diagnostics; no default facts, IDs, or source rendering change. Topology is discarded with its operation; there is no persistent topology or working-tree snapshot cache.

Rollback: select `--ranking current` (the default). No data migration or cache cleanup is required for ranking. Keep the feature experimental. Do not optimize against these results or replace this fixed development set to manufacture the proposed advantage.

Source/fixture origins are recorded in the fresh GraphMark directory README. Only the plan and Entire implementation/instructions were used. Existing implementation comments mentioning historical measurements were incidentally visible and were not used as evidence or design input. No previous GraphMark evidence was opened.


Full-retrieval uniform extension: a private test-only option changes transition
weighting within the same SearchRepository pipeline. No public mode was added.
One frozen test executable produced 180 observations, zero failures and equal
recall/coverage (1.0) in all three arms on the original development set. The
source was hashed before/after build with no drift. No tuning followed. Larger
native JSON in captured modes includes operation provenance; source context
budget and separate text-render budget tests remain distinct from metadata bytes.
Independent reviewer adjudication is absent; the plan does not require a human-only
reviewer, and no such requirement is inferred here.
