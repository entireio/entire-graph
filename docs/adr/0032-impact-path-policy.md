# ADR: experimental bounded impact paths

Status: accepted for traversal core; no default promotion
Date: 2026-09-05

Decision: preserve the current CLI depth-one/two implementation. An independent opt-in traversal uses reverse CALLS/CONSTRUCTS/ASYNC_CALLS, USES_TYPE/PARAM_TYPE/RETURNS_TYPE, EXTENDS/INHERITS/IMPLEMENTS/OVERRIDES, and terminal reverse TESTS. DATA_FLOWS is excluded until producer/consumer value identity survives composition; generic symbol reachability could conflate unrelated values. READS_FIELD/WRITES_FIELD, endpoint joins, resource relations, containment, similarity and historical associations are excluded pending composition-specific fixtures. Exclusion is policy coverage, not evidence that these effects cannot occur.

The dependency mode composes the included structural families; the test mode is terminal. Keep original fact directions and complete evidence in every step. Enumerate simple predecessor paths in stable level order; retain the two best discovered paths per result by weakest edge confidence, then depth, then lexical identity. Do not discard a later stronger explanation because a weaker short explanation arrived first. Bound examined edges, unique nodes, total admitted predecessor states, depth and output paths separately. No optimality claim after a bound.

Alternative rejected: unrestricted undirected graph reachability would propagate containment/co-change into false structural dependencies. One visited bit per symbol discards alternative evidence. Copying full paths into the frontier unnecessarily multiplies memory.

Tests: chain/cycle/diamond and shuffled parallel evidence; containment and co-change must not bridge components; TESTS must terminate; independent budget exhaustion, cancellation and incomplete graph codes. Manually authored facts derive from the plan and Entire relation directions. Real extractor fixtures and multilingual quality evaluation remain required before release.

Output-memory decision: retain predecessor indices for best results as well as the frontier. Materialize paths only after traversal, with an independent default ceiling of 20000 output steps and explicit output_path_bound. Without this ceiling a long chain would retain quadratic path storage despite bounded nodes. This is an initial experimental safety bound, not a tuned retrieval setting. The distinguishing fixture traverses a long chain with a tiny output-step allowance and checks full visited counts alongside truncated output.
