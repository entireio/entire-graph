# Review summary — graph advantage implementation

Baseline: `3a2a715fad1948e83dc7ebe0d307377ba29e065a` from fetched main.
Product branch: `codex/graph-advantage` in the isolated Entire Graph worktree.
Evaluation branch: GraphMark `codex/graph-advantage-evaluation` (`c22eda1`).
No merge or default feature promotion is requested.

## Implemented behavior

1. Experimental content-addressed extraction reuse captures source for an operation,
   preserves private declaration metadata, recomputes IDs/aliases/resolution and
   uses bounded private atomic storage. Format3 also stores measured raw import
   strings for Go/TypeScript/Python fast/full profiles. Family absence is explicit;
   other relation passes remain uncached. Deterministic syntax diagnostics may
   persist; IO/timeouts/resource failures do not. Quota overrides are positive,
   capped `ENTIRE_GRAPH_EXTRACTION_CACHE_MAX_BYTES` and `_MAX_ENTRIES` values.
2. Optional pinned Go compiler analysis runs only inside tested Linux Bubblewrap
   isolation. It uses immutable captured source, bounded offline package discovery,
   request/process-tree budgets, exact token mapping, independent context identity,
   separate candidates and exact-site reconciliation. Static native facts remain.
   Missing tools/closure return explicit unavailable; require-compiler fails.
3. Deeper impact uses explicit semantic propagation, bounded deterministic
   predecessor paths, separate candidate categories, terminal tests and honest
   coverage/stop codes. Default depth1/2 remains on existing behavior.
4. Candidate-only graph ranking is explicitly experimental. Current/exact/deep
   behavior and query scope/render/guidance constraints remain available. Uniform
   evaluation is a private test hook; no additional public ranking mode was added.

Optional schema1.2 fields carry extraction telemetry, compiler evidence and bounded
operation input provenance. Search/impact envelope1 additions are optional.
Capture manifests bind observed inputs and effective scope/options, with policy
coverage limits explicit; they do not claim an atomic worktree. Late backing-store
errors prevent a successful final result. Compact/SCIP refuse compiler distinctions
they cannot represent. Installation, MCP, generated summaries and Brain are untouched.

## Validation and release decision

Final full repository check passed626.23s at0038ef70; see `evidence/check-integration-final-v2.txt`.
Source freeze is `evidence/final-source-freeze-v2.json`; final checks must match it.
Earlier full check at29520508 passed629.12s. Focused capture, storage, compiler,
impact, ranking and schema/combination tests passed; raw failures and corrections
are retained rather than overwritten.

Final Linux combined CLI race passed6.78s: cache cold→warm→oneedit, compiler
complete source-bound evidence, two separate interface candidates, stable IDs,
ranking and three-hop impact, current digests and no worktree snapshot hit.
Raw responses/source hashes/binary hash are in the v2 combination artifacts.

| Gate | Observed evidence | Decision |
|---|---|---|
| P1 equivalence/performance | 810 paired development comparisons all equal on frozen29520508; large full oneedit median−30%, cold+22%. Final code has additional capture/import changes. | No final release speed/RSS claim. Earlier cold gate fails; full pinned real corpus/scenario/RSS matrix remains incomplete. |
| P2 correctness/quality | Live namespace/source-write/cancellation tests; frozen synthetic direct targets static4/6 versus compiler6/6, zero false confirmations; candidates2/2. | Positive compiler implementation established; six hand-authored/compiler-checked sites do not establish broad independently adjudicated quality. |
| P3 correctness/quality | Contract paths reconstruct from facts; all independent bounds/default fixtures pass; 1000-dependent traversal measured separately. | No realistic adjudicated recall/precision or total-query/RSS gate established. |
| P4 retrieval/downstream | 120 CLI current/weighted observations and180 API current/uniform/weighted observations, zero failures; author-label recall/coverage1 for each arm. | Relative recall gain0%, below5%; keep current default. Held-out collection/adjudication is incomplete; conditional agent study not run. |

Failed or incomplete advantage gates remain open in the task ledger. Synthetic
correctness, ceiling-limited retrieval, isolated traversal cost and warm-process
measurements are not substituted for release evidence. No tuning changed the
fixed development sets or thresholds after outcomes were observed.

## Remaining evidence work

The code paths and independent acceptance fixtures are implemented. Release
admission still needs the prescribed pinned performance matrix and independent
realistic quality corpus/adjudication, with held-out and adequately powered agent
studies only under the plan's dependencies. These are explicit unfulfilled gates,
not claims that the targeted advantages were achieved. Other-platform compiler
execution and external ambient dependency import remain unsupported by design.

## Rollback

Select defaults: `--extraction-cache off`, `--compiler off`, `--depth 2`,
`--ranking current`. Disposable extraction entries live in a separate namespace;
remove only that owned namespace if cleanup is desired. Compiler/topology state
is operation-local. No migration or persistent working-tree snapshot exists.
Revert the branch's reviewable commits to remove the implementations entirely.

Sources, fixture origins, exact focused commands and every task's evidence/remain
mapping are in `ledger.md`, the per-workstream validation notes, ADRs0030–0039,
and the fresh GraphMark evaluation directory. No competitors, comparison artifacts,
prior conversations or memory files were consulted as implementation evidence.
