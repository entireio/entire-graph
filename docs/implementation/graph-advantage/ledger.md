# Graph advantage implementation ledger

Requirements: `/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md` and the explicitly requested interim review `/Users/thomi/Projects/entire-plan/graph-advantage-progress-review-2026-09-05.md`.
Baseline: fetched main `3a2a715fad1948e83dc7ebe0d307377ba29e065a`.
Branch: `codex/graph-advantage`, isolated worktree; primary checkout preserved. No merge.

## Current phase

The user has now authorized the fixed P1 corpus campaign, delegated to three `gpt-5.6-luna` subagents with root integration review. P1 preparation and correctness checks are complete; all 108 baseline requests finished (69 complete, 33 partial, 6 timeouts); the paired campaign is paused for diagnosis; all three jobs are stopped. P2/P3/P4 comparative campaigns remain deferred. Earlier results and failed gates remain retained; none establish the revised source performance or quality.

The review findings are addressed in code: type declarations no longer become compiler-confirmed calls; ordinary search and every impact depth consume the effective compiler relation view. Candidate implementation paths remain separate, including terminal covering tests. Source is frozen; immutable integration checks passed.

- **Final implementation commit:** 88dd1dc95a996999ae4e456879b6dd86d8027f71
- **Required repository check:** Passed `mise run check` in 653.107 seconds with unchanged source; see `evidence/review-check-result.json`.
- **Pinned Linux correctness check:** Passed at the same commit: 10 pinned live tests plus focused race suites; see `evidence/review-linux-result.json` and raw logs.
- **Release status:** no workstream's complete release gate has passed; defaults remain unchanged.

## Authoritative task status

This table supersedes earlier checkpoint/status statements. “Complete” in the implementation column describes code or harness delivery only. Evaluation tasks cannot be declared complete merely because their harness exists. “Focused” correctness does not replace the final immutable integration check above.

| Task | Implementation | Correctness evidence | Comparative evaluation | Release gate |
|---|---|---|---|---|
| P1.1 characterize | Complete: phase/capture characterization and reproducible paired harness | Existing pinned phase artifacts and controlled-reader fixtures | Fixed six-repository campaign prepared; baseline complete; paired campaign paused for diagnosis | Not passed: full fixed corpus/RSS matrix incomplete |
| P1.2 pure extraction | Complete: explicit metadata, shared bounded source/policy capture, manifests and sticky errors | Entity field checklist, malformed/overload/language round trips, reader mutation tests; final immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.3 storage | Complete: cross-process admission, orphan cleanup, versioned keys, integrity, private atomic writes and bounded decode | Independent-operation/subprocess contention, corruption and no-follow regressions passed race; final immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.4 entity integration | Complete: command/policy capture, ordered reuse and fresh IDs/aliases/resolution | P1-A parse counts; manifest/rename/delete/ignore and selective-scope freshness fixtures; final immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.5 relation inputs | Complete for measured raw-import family: Go/TypeScript/Python fast/full; explicit family presence | Exact relation parity and reuse tests; other families deliberately absent | Further profiling deferred; no new family selected by benchmark tuning | Not passed |
| P1.6 diagnostics/gates | Complete: diagnostics, policy coverage and rollback controls | Separate parsed/reused/source/cache/phase telemetry; final immutable check passed | Existing cold regression retained; fixed corpus campaign paused for diagnosis | Not passed: performance target previously failed |
| P2.1 feasibility | Complete: pinned gopls v0.20.0 and Linux Bubblewrap execution boundary | Existing positive no-egress/read-only/descendant cancellation checks; final Linux race passed | Hard-Go comparative quality deferred | Not passed |
| P2.2 client/capture | Complete: bounded lifecycle, source capsule/context identity, mapping and cancellation | Protocol/UTF-16/malformed reply/context/process tests; final immutable check passed | Deferred | Not passed |
| P2.3 positive integration | Complete: direct declarations and separate implementation candidates; conversions excluded | New conversion/alias/generic fixtures and pinned live fixture; signature/workspace invalidation tests; final Linux race passed | Independent realistic quality evaluation deferred | Not passed |
| P2.4 overlay/query contract | Complete: exact-site reconciliation, additive evidence, projection refusal; effective view in ordinary search and all impact depths | F1/F2 regressions, schema guards and new pinned ordinary-query fixture; Linux race passed; full immutable repository check passed | Deferred | Not passed |
| P2.5 invalidation/evaluation | Invalidation and evaluation interfaces complete; comparative task not completed | Source/dependency/configuration identity, fallback and signature/workspace regression fixtures; Linux race passed; full immutable repository check passed | Frozen earlier synthetic outcomes retained; new studies deferred | Not passed: broad adjudicated quality evidence absent |
| P3.1 policy/compatibility | Complete: documented relation directions/compositions and default depth compatibility | Go/TypeScript/Python chains, route/resource fixtures, default output tests | Adjudicated realistic changes deferred | Not passed |
| P3.2 traversal | Complete: deterministic bounded adjacency/predecessor traversal | Cycle/diamond/shuffle/hub, cancellation and independent work-limit tests; focused race passed | Total-query cost/RSS evaluation deferred | Not passed |
| P3.3 paths/tests | Complete: parallel evidence, representative alternatives, terminal tests and explicit partial counts | Reconstructed path proofs, confidence/evidence/output bounds; candidate terminal-test regression passed | Covering-test relevance study deferred | Not passed |
| P3.4 CLI/output | Complete: depth N/all, filters, budgets, additive JSON and bounded text | Depth compatibility, invalid controls, truncation-notice byte bounds; focused race passed | Deferred | Not passed |
| P3.5 compiler view | Complete: shared effective relations at every depth; candidate paths never become structural facts | Missing/disputed caller and callee fixture at depths 1/2/3/all; candidate separation; native records unchanged | Optional compiler quality ablation deferred | Not passed |
| P3.6 validation/docs | Contract/stress fixtures and propagation limits complete; comparative task not completed | Independent source fixtures and bounded stress correctness; final immutable check passed | Realistic precision/recall/coverage/latency/RSS study deferred | Not passed |
| P4.1 baseline | Development fixture/provenance and harness interfaces complete; adjudicated release baseline pending | Existing fresh GraphMark manifests and recorded outputs preserved | New collection/adjudication and split freeze deferred | Not passed |
| P4.2 ranking core | Complete: deterministic bounded candidate-only PPR | Mass/dangling/cycle/invalid-weight/duplicate/hub/scope tests; final immutable check passed | No quality inference from numerical correctness | Not passed |
| P4.3 query integration | Complete: explicit experimental mode, scope/render/guidance constraints and deterministic fallback | Capture/freshness/exact-match tests; compiler view used consistently; final immutable check passed | Deferred | Not passed |
| P4.4 development ablations | Harness controls implemented; comparative task not completed | Current/uniform/weighted and expansion-control plumbing; correctness smoke checks only this phase | Existing zero-gain result retained; further ablations deferred | Not passed: no demonstrated winning configuration |
| P4.5 held-out | Protocol and required harness interfaces available; study not completed | No held-out result claimed | Deferred; requires adjudicated disjoint set and frozen candidate | Not passed |
| P4.6 downstream | Conditional protocol available; study not completed | Current remains default; no promotion | Deferred until retrieval gate and prospective power design | Not passed |

P1 final focused race passed (49.124s); graph-requested regression passed (0.811s). Windows admission helper compiled successfully; this is compile-only evidence.

## Review changes and focused checks

- `cb98dc25`: cross-process cache quota admission and orphan cleanup.
- `88dd1dc9`: complete provider policy capture and explicit metadata checklist.
- `c8179135`: bounded ranking input scans and topology coverage.
- `a2e97f12`: F1 conversion/type-alias/generic target discrimination.
- `9e22e871`: compiler signature/workspace invalidation correctness fixtures.
- `57e000a1`: F2 impact relation integration and candidate terminal-test provenance.
- `ffc8497a`: F2 ordinary search relation view and ablation controls.
- `ab7ae182`: pinned Linux ordinary-query correctness fixture.

For `57e000a1`, `go test ./internal/cli -run TestImpact -count=1` passed (3.092s); the same under race passed (4.464s). Semantic impact and graph-requested compiler reconciliation regression passed (1.118s); semantic impact race passed (2.599s). The new pinned ordinary-query test passed on Linux with the actual backend; raw responses are retained.

The earlier `0038ef70` full `mise run check` passed in 626.23s, but does **not** verify these subsequent changes. Its logs, earlier schema-golden failure/correction and source freeze remain historical evidence. Final checks must identify the new immutable source commit.

## Retained comparative evidence

The prior implementation phase ran no comparative campaigns. The newly authorized P1 campaign has its own frozen protocol and raw artifacts under `p1-corpus-20260905/`. No P2/P3/P4 campaign is authorized in this phase. Existing artifacts remain unchanged:

- P1: 810 paired comparisons / 1,620 observations at the earlier frozen source; all recorded semantic comparisons equal. Large full-profile one-edit median improved approximately 30%, while cold latency regressed approximately 22%; other profiles also failed targets. These are earlier synthetic development results.
- P2: earlier small compiler-checked synthetic fixture reported static 4/6 versus compiler 6/6 direct targets, with separate candidates 2/2. It does not establish a broad adjudicated quality gate, and does not replace conversion regression verification.
- P3: earlier isolated traversal measurements remain component measurements, not end-to-end cost or realistic recall evidence.
- P4: GraphMark `c22eda1` retains 120 CLI and 180 API development observations; author-labeled recall/coverage reached 1.0 for every arm, giving zero recall gain. No holdout or agent outcome is claimed.

## Functional limits and rollback

Defaults remain `--extraction-cache off`, `--compiler off`, `--depth 2`, `--ranking current`. Compiler execution is supported only by the tested Linux launcher. Unsafe external dependency roots and unresolved dynamic targets produce explicit coverage limitations. Candidate evidence is not runtime invocation evidence.

Raw relation reuse currently covers imports only; unsupported families are explicitly absent and recomputed. The 64 MiB capture bound covers retained source payload, not total process RSS. Capture manifests identify observed inputs with coverage limits; they do not describe an atomic worktree revision. Root/nested ignore and info-exclude reads now share the capture store; mutation/spill/overlap regressions and orphan-temp cleanup passed focused race. Cross-process quota admission is locked. Git listing/global configuration and metadata probes retain explicit coverage limits.

Rollback uses those default flags. Disposable extraction records occupy their separate cache namespace; no migration is needed. Revert the reviewable implementation commits to remove the features. Persistent working-tree snapshot reuse remains disabled. Installation, MCP packaging, generated summaries and Brain remain out of scope.

## Next phase: proposed benchmark sequence only

1. After final correctness passes, prospectively freeze the binary, source, environments, workload matrix, labels, metrics and trial rules. Preserve earlier failed outcomes.
2. P1: execute the fixed small/medium/large and synthetic cold/edit/rename/delete/branch/manifest scenarios with at least 30 paired repetitions, identical inputs, documented page-cache state and RSS/disk measurements.
3. P2/P3: independently adjudicate exact required/allowed/forbidden targets and affected sites, keeping candidates separate. Run the hard-Go and depth-sensitive slices with total cost and coverage accounting.
4. P4 development: run current, identical-expansion baseline, uniform graph and weighted graph under identical captured inputs and byte budgets. Run the compiler arm only under the preregistered dependency and include startup cost. Freeze any selected configuration without tuning on held-out outcomes.
5. Run a disjoint adjudicated holdout once per frozen candidate. Only after retrieval clears its gate, perform the prospectively powered paired agent experiment. Failed or inconclusive gates retain experimental/default-off status.

## Consulted sources and fixture origins

Implementation sources are the plan, the user-authorized interim review, applicable AGENTS/generated graph instructions, Entire source/tests, schema ADR 0001, trust contract and repository checks. The review is now explicitly a consulted source; older blanket statements excluding every review/comparison document are superseded. No competitor implementation, prior conversation, memory file or adjacent comparative corpus was used as implementation evidence.

Official P2 references remain LSP 3.17, gopls navigation/settings and Go modules, recorded in the existing ADR/source manifests. Tool distribution/Azure provisioning is separate from runtime provider behavior. Runtime analysis does not fetch tools or dependencies.

Fixtures derive from the plan and Entire's maintained behavior, with hand-derived expectations or pinned official compiler checks. New review fixtures cover type conversions/aliases/generic conversions, exact-site redirects and ordinary search/impact propagation. The terminal-test fixture combines P2-B candidate distinction with P3-B terminal test semantics. The pinned ordinary-query fixture uses a promoted method through an alias and records observed static coverage without assuming a static gap. No external product supplies expected answers.

Final validation: source commit `88dd1dc9` passed the complete repository check
and pinned Linux correctness. The initial Linux Git-version prerequisite failure
is retained; the upgraded run passed without product changes. The validation VM
was deallocated after correctness; it and two identical workers are now active
for the separately authorized P1 campaign. Evidence-only commits after the source freeze do not
change tested code. No comparative campaign ran during implementation. No merge was performed.

P1 campaign sources: the user-approved pinned chi, Kubernetes, Zod and Requests
repositories are evaluation inputs only, alongside frozen Entire source and an
independent synthetic fixture. See `corpus/corpus-manifest.json` for revisions,
licenses, selected paths and fixture origins. Official OpenAI model guidance
was consulted only for the requested cheaper subagent selection.

P1 execution monitoring: heartbeat `monitor-p1-corpus-campaign` checks every
15 minutes. Three Luna subagents prepared the corpus, harness and protocol;
the protocol worker monitors the baseline. The full repository check runs
against commit `039d213b`; `p1-corpus-20260905/correctness.json` records its
exit status and source-hash stability when finished. No gate pass is claimed
from an in-progress check or campaign.

P1 baseline freeze: `2840a152`, authoritative `frozen-baseline.json`.
Kubernetes fast/full snapshots each timed out three times at 120 seconds;
the protocol blocks those two strata and preserves 960 planned unrun cells.
The remaining 16,320 paired requests retain the original settings. Full
`mise run check` passed at 039d213b in 1126.765 seconds with source hashes unchanged;
see `p1-corpus-20260905/correctness.json`. No release gate has passed.

Latest P1 monitor (`monitor-20260905T2234.json`): all three workers active;
66 measured requests (63 partial, 3 timeouts), 1,434 explicitly unrun, and
15,780 pending planned cells. Kubernetes syntax-only snapshot cache-on requests
also hit the 120-second limit three times; its six actual requests are retained
and its other 474 cells are unrun. The two baseline-blocked strata retain
960 unrun cells. Frozen binary, input and baseline hashes still match.
No interim performance or quality conclusion is drawn. P4.1 remains pending;
a previous ledger wording error is corrected without changing P4 status.

User-directed diagnostic stop: all three services verified inactive on
2026-09-05 at approximately 22:55 UTC; recurring continuation is PAUSED.
Retained 116 measured requests (113 partial, 3 timeouts), 1,434 protocol-unrun
cells, and 15,730 remaining planned cells. Interrupted in-flight requests
are preserved separately and are not fabricated as completed observations.
No further measurement is authorized by the old continuation; diagnose and
correct the findings before defining a separately versioned evaluation.
