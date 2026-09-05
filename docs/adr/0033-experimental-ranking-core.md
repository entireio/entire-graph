# ADR: query-seeded ranking core and frozen initial parameters

Status: accepted experimental implementation; release advantage not established
Date: 2026-09-05

Decision: implement the plan's recurrence directly, with alpha 0.25, at most 25 iterations, L1 residual 1e-8, and normalized-max blend 0.8 lexical + 0.2 graph. Work only on lexical candidate IDs. Initial transitions are calls/constructs/async at 1.0, type uses/parameters/returns at 0.5, reverse at half weight, multiplied by confidence bands 1/.7/.3. Zero confidence is excluded because the existing numeric field cannot distinguish missing from explicit zero. Collapse duplicate directed source/target/relation evidence to strongest weight. Retain distinct relation-family contributions.

Bound topology to 2000 candidate nodes and 10000 directed transitions, falling back explicitly to current scores if exceeded. Invalid numerical inputs fail rather than contaminating sorting. Sorted IDs and transitions determine accumulation order. No topology persistence in this stage.

Alternatives: uniform global centrality ignores the query; graph expansion changes scope and must be a separate evaluation arm; numerical tuning without development labels violates the requirements.

Tests: hand-derived two-node first iteration, unit mass with dangling nodes, duplicate evidence invariance, outside-scope exclusion, no-seed and resource fallback, deterministic shuffled input and invalid weights. This does not establish retrieval quality. Exact-match precedence, rendering and byte-budget contracts must be verified during integration. GraphMark held-out and powered agent experiments have not passed; current ranking remains the product default. The pure core has no independent public mode.

Integration decision: `--ranking experimental-graph` reranks the existing semantic
candidate pool after existing priors and before diversity/byte-budget selection.
It uses one captured operation. Candidate expansion is unchanged; topology is
operation-owned and discarded, so it cannot outlive its captured identity/scope.
Exact-code/symbol queries and deep sparse fusion conservatively fall back to the
current ranking with an explicit reason until separately evaluated. Components
and work counts are additive diagnostics; current remains the default. A failed
or inconclusive retrieval gate blocks the conditional agent experiment.


Development review (2026-09-05): initial settings remain unchanged after six
independent synthetic repository fixtures (Go, TypeScript, Python). Both current
and weighted modes found all author-labeled sites in 120 observations; relative
recall improvement was zero, below the 5% proposed gate. This is a ceiling-limited
development result, not a held-out or release result. Twelve sampled text renders
stayed within 4096 bytes. Pure-core uniform/weighted ablations are separate from
end-to-end retrieval and do not identify a winning ranker. No compiler-winning
arm or downstream agent study is run without its prerequisite. Evidence and
reproduction scripts live in GraphMark's `advantage-ranking-20260905/` directory.

Topology remains operation-owned, without a persistent cache. Its lifetime is
strictly shorter than captured graph/scope/compiler identity, so there is no
cross-operation topology reuse to invalidate. A future persistent cache would
require all identities and topology-policy version in its key. Candidate-only
scope, normalized outgoing weights and the 0.2 blend bound popularity influence;
no claim is made that these controls improve relevance on production hubs.


The uniform ablation is now also exercised through full retrieval via an
unexported test-only option. It changes no CLI mode or default. The unchanged
six-repository development set produced 180 additional observations with equal
recall/coverage in current, uniform and weighted arms; no weight selection or
release promotion followed. Full evidence is retained in GraphMark.

Implementation-first review follow-up: keep all settings frozen and defer new
comparative runs. The retrieval harness must accept explicit current,
current-expansion (identity control while expansion is unchanged), uniform,
weighted, and weighted-compiler arms. All arms use captured input handling;
compiler requires explicit pinned configuration. Repetitions and selected arms
are explicit harness inputs. Smoke tests validate mode selection and one-query
plumbing only, without scoring quality or collecting performance observations.
No arm is a public default or authorization to launch a holdout/agent campaign.
