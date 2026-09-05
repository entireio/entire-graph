# ADR: query-seeded ranking core and frozen initial parameters

Status: accepted for pure core; CLI integration and evaluation pending
Date: 2026-09-05

Decision: implement the plan's recurrence directly, with alpha 0.25, at most 25 iterations, L1 residual 1e-8, and normalized-max blend 0.8 lexical + 0.2 graph. Work only on lexical candidate IDs. Initial transitions are calls/constructs/async at 1.0, type uses/parameters/returns at 0.5, reverse at half weight, multiplied by confidence bands 1/.7/.3. Zero confidence is excluded because the existing numeric field cannot distinguish missing from explicit zero. Collapse duplicate directed source/target/relation evidence to strongest weight. Retain distinct relation-family contributions.

Bound topology to 2000 candidate nodes and 10000 directed transitions, falling back explicitly to current scores if exceeded. Invalid numerical inputs fail rather than contaminating sorting. Sorted IDs and transitions determine accumulation order. No topology persistence in this stage.

Alternatives: uniform global centrality ignores the query; graph expansion changes scope and must be a separate evaluation arm; numerical tuning without development labels violates the requirements.

Tests: hand-derived two-node first iteration, unit mass with dangling nodes, duplicate evidence invariance, outside-scope exclusion, no-seed and resource fallback, deterministic shuffled input and invalid weights. This does not establish retrieval quality. Exact-match precedence, rendering and byte-budget contracts must be verified during integration. GraphMark held-out and powered agent experiments are unavailable; current ranking remains the product default and no new CLI mode is exposed by the core.
