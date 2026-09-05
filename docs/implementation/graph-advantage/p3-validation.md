# P3 implementation and validation

This is product-local correctness and bounded-cost evidence, not a measured
real-world affected-site advantage or a release promotion. Default depth 2 stays
on the existing implementation; deeper traversal requires `--depth N` above 2 or
`--depth all`.

| Task | Implementation and evidence | Remaining release work |
|---|---|---|
| P3.1 | ADR 0032 policy; source-extracted Go/TypeScript/Python call chains, Go HTTP routes and HCL resource direction fixtures; existing depth 1/2 CLI tests | Independent adjudication of realistic changes |
| P3.2 | Deterministic predecessor traversal with distinct semantic states, sorted evidence, exact duplicate deduplication, independent construction/node/edge/frontier/depth limits and cancellation | No known contract blocker; final repository check belongs to integration |
| P3.3 | Stronger alternative selection, original directed evidence, terminal tests, exact field/endpoint/channel composition; output-step and repeated-evidence bounds; machine stop codes and lower bounds | Quality evidence remains distinct from proof validity |
| P3.4 | Deeper flags and additive JSON; streaming bounded text and reserved notice; source capture reused; invalid/ignored controls rejected before analysis | Root-owned help/capability/schema integration |
| P3.5 | Namespaced compiler candidates remain `compiler_candidate` paths through subsequent facts, under a separate alternative cap; positive direct facts use enriched view | Live compiler coverage is P2's independent gate |
| P3.6 | Contract/stress fixtures and retained focused race/benchmark output | Preregistered realistic Go/TS/Python/configuration change corpus with reviewer labels, precision/recall/coverage, total-query latency/RSS/payload evaluation in GraphMark |

## Consulted sources and fixture origins

- `/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md`,
  2026-09-05 requirements, particularly sections 5, 7 and 8.
- Repository `AGENTS.md`, `.entire/graph-agent.md`, current Entire implementation
  in `internal/sem/impact_paths.go`, `internal/cli/impact*.go`, provider relation
  definitions, `routeBridgeRelations`, `goHTTPRouteRelations`, and the focused
  resource/route direction tests in `internal/sem/provider_test.go`.
- ADR 0032 records the exact-identity composition and bounded-construction choices.
  No external implementation, comparison corpus, conversation history, or memory
  was used to derive the new fixtures.
- Chains, cycles, diamonds, hubs, confidence alternatives, tests-as-terminal,
  mismatched fields/routes/channels, candidate paths and resource/HTTP source
  examples were independently authored from plan behavior. Expected reachability
  is hand-derived. The random graph proof check uses seed `30905` and validates
  each emitted step against an original fact, direction, contiguous predecessor,
  no repeated state, target, and minimum-edge confidence.
- The 1,000-dependent star benchmark is generated in
  `BenchmarkImpactPathsThousandDependents`. It measures only traversal on an
  already materialized graph. It is not end-to-end latency or peak RSS, and is
  not a quality/counterfactual study.

## Reproduction and decision

Run from the task checkout:

```sh
go test -race ./internal/sem ./internal/cli -run 'TestImpact|^TestHTTPCallsBridgeToLocalRouteHandler$' -count=1
go test ./internal/sem -run '^$' -bench '^BenchmarkImpactPathsThousandDependents$' -benchtime=10x -count=5 -benchmem
```

Raw outputs are `evidence/p3-focused.txt` and
`evidence/p3-stress-benchmark.txt`. The accompanying `p3-source-manifest.json`
records source hashes and environment. These tests validate contract fixtures;
no synthetic 2-to-4-hop recall ratio is substituted for the required realistic
quality gate. No claim of a 10% recall improvement or <=2 point precision loss
is established. Deeper traversal remains explicitly opt-in/experimental.

Rollback: request depth 1 or 2; omit the compiler overlay. No persisted adjacency
or working-tree snapshot needs invalidation. Existing co-change and container
context remains separate from recursive propagation. Unknown runtime effects,
unmatched external endpoints, unsupported value-flow composition and incomplete
source graphs remain coverage limitations; absence of a path is never a safety
finding.
