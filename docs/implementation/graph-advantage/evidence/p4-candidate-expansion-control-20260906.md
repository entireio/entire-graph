# P4 candidate-expansion control evidence

Implementation baseline: `78c8b4963a5b0a93ed4118b4ce1251a144098b43` on `codex/graph-advantage`.

The implementation follows Workstream P4.4 in `entire-graph-advantage-implementation-plan.md` and the repository's graph-agent instructions. Source changes are confined to `internal/sem/search.go`, `internal/sem/search_graphrank.go`, and `internal/sem/search_graphrank_harness_test.go`. Fixtures are locally generated Go repositories and direct relation records derived only from the plan's required arm, scope, budget, fallback, and determinism contracts.

The internal evaluation harness applies one bounded candidate-expansion policy to `current-expansion`, `uniform`, `weighted`, and `weighted-compiler`. `current-expansion` retains baseline ranking scores; graph arms rerank the captured pool. The no-compiler `current-expansion`, `uniform`, and `weighted` arms are verified to use an identical candidate pool; compiler evidence may change eligible relations and therefore the `weighted-compiler` pool. The expansion reuses P4 call/type relation and confidence eligibility, respects the selected file set, bounds nodes, input relations, unique links, and added candidates, and reports request/state/work, truncation, fallback, and source-read failures. The control remains internal and default-off; no CLI mode or default search behavior changed. Compiler implementation candidates remain separate.

Verification:

```text
go test ./internal/sem -run '^TestGraphRankingEvaluation(Expansion|Harness)' -count=1
ok  github.com/entireio/entire-graph/internal/sem  2.391s

go test -race ./internal/sem -run '^TestGraphRanking(Evaluation|Candidate|Fallback|Scores|Personalized)' -count=1
ok  github.com/entireio/entire-graph/internal/sem  4.976s

git diff --check -- internal/sem/search.go internal/sem/search_graphrank.go internal/sem/search_graphrank_harness_test.go
(no output)
```

The fixtures verify exact pre-rank pool equality across the no-compiler expansion-enabled arms, baseline score preservation, a distinct expansion result, SearchRepository file-scope confinement, deterministic caps and fallback, duplicate/reordered relations, equal-product proposal ties, partial source reads, and cancellation. They do not claim byte-identical compiler-arm pools.

No retrieval measurement, tuning, benchmark, held-out evaluation, compiler run, cloud call, or agent study was performed. Historical identity-control results remain evidence only for their original code version and are not reinterpreted.

Rollback: leave the internal expansion option false or remove it from the evaluation-arm mapping. Production `current` and `experimental-graph` behavior is unchanged because ordinary callers cannot enable the control.
