# ADR: experimental bounded impact paths

Status: accepted for traversal core; no default promotion
Date: 2026-09-05

Decision: preserve the current CLI depth-one/two implementation. An independent
opt-in traversal uses reverse CALLS/CONSTRUCTS/ASYNC_CALLS,
USES_TYPE/PARAM_TYPE/RETURNS_TYPE, EXTENDS/INHERITS/IMPLEMENTS/OVERRIDES, and
terminal reverse TESTS. Field, endpoint/channel, and resource compositions follow
the exact-identity rules below; DATA_FLOWS stops after one relation because its
records lack a composable value identity. Containment, similarity and historical
association do not propagate. Exclusion is policy coverage, not evidence that
these effects cannot occur.

The dependency mode composes the included structural families; the test mode is terminal. Keep original fact directions and complete evidence in every step. Enumerate simple predecessor paths in stable level order; retain the two best discovered paths per result by weakest edge confidence, then depth, then lexical identity. Do not discard a later stronger explanation because a weaker short explanation arrived first. Bound examined edges, unique nodes, total admitted predecessor states, depth and output paths separately. No optimality claim after a bound.

Alternative rejected: unrestricted undirected graph reachability would propagate containment/co-change into false structural dependencies. One visited bit per symbol discards alternative evidence. Copying full paths into the frontier unnecessarily multiplies memory.

Tests: chain/cycle/diamond and shuffled parallel evidence; containment and co-change must not bridge components; TESTS must terminate; independent budget exhaustion, cancellation and incomplete graph codes. Manually authored facts derive from the plan and Entire relation directions. Real extractor direction fixtures cover Go, TypeScript, Python, HCL resources and Go HTTP routes. Independent reviewer-adjudicated multilingual quality evaluation remains required before release.

Output-memory decision: retain predecessor indices for best results as well as the frontier. Materialize paths only after traversal, with an independent default ceiling of 20000 output steps and explicit output_path_bound. Without this ceiling a long chain would retain quadratic path storage despite bounded nodes. This is an initial experimental safety bound, not a tuned retrieval setting. The distinguishing fixture traverses a long chain with a tiny output-step allowance and checks full visited counts alongside truncated output.

Integration policy extension: field writers propagate through the exact field ID
to readers; route handlers reach HTTP consumers only through an ID shared by
HANDLES_ROUTE and HTTP_CALLS; emitters reach listeners only through an exact shared
channel ID. Unmatched external endpoints remain terminal. Resource dependents
follow RESOURCE_DEPENDS_ON in reverse; CONFIGURES follows the directed configured
target. DATA_FLOWS contributes a terminal one-hop explanation because current
records do not provide a composable value ID; do not join arbitrary flows at a
function. Historical co-change and containment never enter this traversal.

Deeper CLI output is additive and opt-in (depth >2 or all), with independent
work limits and evidence paths. Depth 1/2 uses the existing renderer unchanged.
A reserved text truncation marker precedes any optional entries when the output
budget cannot represent the complete discovered result.

2026-09-05 bounded-construction and compiler-path decision: independently cap
adjacency input at 100,000 records and evidence/identity storage at 8 MiB,
before constructing maps or copying evidence. Oversized inputs return an empty
partial traversal with `adjacency_bound` or `evidence_bound`, rather than an
input-order-dependent prefix. Both limits are configurable. These bounds cover
traversal-owned work; provider extraction still has its existing independent
source bounds. Retain structural and compiler-candidate alternatives under
separate per-category caps; a candidate anywhere in a path labels the whole
path `compiler_candidate`, including subsequent structural steps. A candidate
is not upgraded by later high-confidence static facts. Distinguishing tests
exercise independent construction bounds, shuffling, and one-path-per-category
retention when structural and candidate explanations reach the same result.

The same 8 MiB evidence ceiling also bounds materialized path evidence, including
repeated steps across alternatives (`output_evidence_bound`). The JSON path step
encoding is measured once per adjacency arc, with conservative framing/ID
allowances, before retaining output paths. A chain with long evidence strings
proves that the output-step count alone does not authorize quadratic evidence
payloads. Text is emitted by a bounded whole-line writer with notice space
reserved before path construction. Graph work is independent of text budget.

Deeper-only filters/work budgets require an explicit depth greater than two or
`all`; accepting them silently at legacy depth would imply enforcement that the
legacy algorithm does not provide. Invalid relation policies are rejected before
source analysis even when the focus would be missing or ambiguous. Cancellation
returns an operation error and cannot publish a completed response.
