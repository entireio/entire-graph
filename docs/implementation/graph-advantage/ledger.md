# Graph advantage implementation ledger

Requirements: `/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md` and the explicitly requested interim review `/Users/thomi/Projects/entire-plan/graph-advantage-progress-review-2026-09-05.md`.
Baseline: fetched main `3a2a715fad1948e83dc7ebe0d307377ba29e065a`.
Branch: `codex/graph-advantage`, isolated worktree; primary checkout preserved. No merge.

## Current phase

The user authorized the fixed P1 corpus campaign, delegated to three `gpt-5.6-luna` subagents with root integration review. P1 preparation is complete; all 108 baseline requests finished (69 complete, 33 partial, 6 timeouts); the paired campaign is paused for diagnosis; all campaign jobs are stopped; the two campaign workers are deallocated; validation VM runs current correctness only. P2/P3/P4 comparative campaigns remain deferred. Earlier results and failed gates remain retained; none establish the revised source performance or quality.

The latest fully verified product source is `d793b2be`, including ADR 0044 cold-path corrections. Full `mise run check` passed in 683.699 seconds in a clean immutable checkout with unchanged HEAD. Pinned Linux passed 28 top-level tests, including 10 live tests with the expected gopls v0.20.0 hash. Raw logs and identities are retained in `evidence/check-d793b2be/` and `evidence/correctness-d793b2be-20260906/`. The initial untrusted-mise setup refusal is retained separately; the successful retry ran the identical commit.

- **Latest completed full check:** `d793b2be`, passing in 683.699 seconds; immutable HEAD and clean status confirmed. Evidence: `evidence/check-d793b2be/`.
- **Linux correctness:** passed at `d793b2be`, including process-boundary, missing-dependency, conversion, ordinary-query, opt-in combination and freshness checks.
- **Interrupted check:** `6102b209` was deliberately stopped after review found additional cancellation gaps; it is not a pass. Those findings were addressed by `05ad9842`.
- **Retained query:** Darwin OFF exceeded its deadline; ON unrun. Linux parity passed for this one retained request at `05ad9842`: both arms381files/11identical partials and identical semantic digest; unchanged corpus. The syntax-only snapshot pair also completes with exact semantics/194identical partials, but its coldON86.3s versusOFF56.9s triggers a screening pause. One pair is not statistical evidence.
- **Execution:** validation VM was restarted only for current correctness; two campaign workers remain off. No campaign is running. The bounded corrective retry at `d793b2be` preserved exact semantics and known partials, but still failed the cold screen (OFF53.5s/ON73.5s). Campaign remains paused for remaining-cost attribution.
- **Current follow-up:** ADR 0044 cold-path fixes are integrated at `d793b2be`: first-consumer spill reuse, under-quota sort avoidance and lossless UTF-8 admission without JSON round-trip. Focused extraction/capture/quota race checks passed (6.360s), and the invalid-UTF-8 persistence regression passed (1.879s). Pinned Linux passed 28 top-level tests including 10 live; the immutable full check passed (683.699s). All 77 campaign-control tests passed (7.809s) after startup, final-lease and accounting fixes. No campaign restart is admitted.
- **New working source:** `b253e18a` publishes the encoded envelope once; focused extraction/cache/quota race checks passed (6.077s). Its full integration check is deferred until the related admission-session design is resolved; `d793b2be` remains the latest fully checked product. ADR 0046 proposes a capability-bound admission session, not an approved performance result. All VMs are deallocated.
- **Release status:** no workstream's complete release gate has passed; defaults remain unchanged.

## Authoritative task status

This table supersedes earlier checkpoint/status statements. “Complete” in the implementation column describes code or harness delivery. Product source `d793b2be` passed the full repository and pinned Linux checks, but retained-corpus parity remains open. Evaluation tasks cannot be declared complete merely because their harness exists; correctness checks do not establish comparative release gates.

| Task | Implementation | Correctness evidence | Comparative evaluation | Release gate |
|---|---|---|---|---|
| P1.1 characterize | Complete: phase/capture characterization and reproducible paired harness | Existing pinned phase artifacts and controlled-reader fixtures | Fixed six-repository campaign prepared; baseline complete; paired campaign paused for diagnosis | Not passed: full fixed corpus/RSS matrix incomplete |
| P1.2 pure extraction | Complete: explicit metadata, shared bounded source/policy capture, manifests and sticky errors | Entity field checklist, malformed/overload/language round trips, reader mutation tests; historical immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.3 storage | Complete: cross-process admission, orphan cleanup, versioned keys, integrity, private atomic writes and bounded decode | Independent-operation/subprocess contention, corruption and no-follow regressions passed race; historical immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.4 entity integration | Implemented; retained-query parity and current diagnostic corrections remain open | P1-A parse counts; manifest/rename/delete/ignore and selective-scope freshness fixtures; historical immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.5 relation inputs | Complete for measured raw-import family: Go/TypeScript/Python fast/full; explicit family presence | Exact relation parity and reuse tests; other families deliberately absent | Further profiling deferred; no new family selected by benchmark tuning | Not passed |
| P1.6 diagnostics/gates | Implemented; cancellation follow-up and integration verified at `05ad9842` | Separate parsed/reused/source/cache/phase telemetry; historical immutable check passed | Existing cold regression retained; fixed corpus campaign paused for diagnosis | Not passed: performance target previously failed |
| P2.1 feasibility | Complete: pinned gopls v0.20.0 and Linux Bubblewrap execution boundary | Existing positive no-egress/read-only/descendant cancellation checks; historical Linux race passed | Hard-Go comparative quality deferred | Not passed |
| P2.2 client/capture | Complete: bounded lifecycle, source capsule/context identity, mapping and cancellation | Protocol/UTF-16/malformed reply/context/process tests; historical immutable check passed | Deferred | Not passed |
| P2.3 positive integration | Complete: direct declarations and separate implementation candidates; conversions excluded | New conversion/alias/generic fixtures and pinned live fixture; signature/workspace invalidation tests; historical Linux race passed | Independent realistic quality evaluation deferred | Not passed |
| P2.4 overlay/query contract | Complete: exact-site reconciliation, additive evidence, projection refusal; effective view in ordinary search and all impact depths | F1/F2 regressions, schema guards and new pinned ordinary-query fixture; Linux race passed; historical full repository check passed | Deferred | Not passed |
| P2.5 invalidation/evaluation | Invalidation and evaluation interfaces complete; comparative task not completed | Source/dependency/configuration identity, fallback and signature/workspace regression fixtures; Linux race passed; historical full repository check passed | Frozen earlier synthetic outcomes retained; new studies deferred | Not passed: broad adjudicated quality evidence absent |
| P3.1 policy/compatibility | Complete: documented relation directions/compositions and default depth compatibility | Go/TypeScript/Python chains, route/resource fixtures, default output tests | Adjudicated realistic changes deferred | Not passed |
| P3.2 traversal | Complete: deterministic bounded adjacency/predecessor traversal | Cycle/diamond/shuffle/hub, cancellation and independent work-limit tests; focused race passed | Total-query cost/RSS evaluation deferred | Not passed |
| P3.3 paths/tests | Complete: parallel evidence, representative alternatives, terminal tests and explicit partial counts | Reconstructed path proofs, confidence/evidence/output bounds; candidate terminal-test regression passed | Covering-test relevance study deferred | Not passed |
| P3.4 CLI/output | Complete: depth N/all, filters, budgets, additive JSON and bounded text | Depth compatibility, invalid controls, truncation-notice byte bounds; focused race passed | Deferred | Not passed |
| P3.5 compiler view | Complete: shared effective relations at every depth; candidate paths never become structural facts | Missing/disputed caller and callee fixture at depths 1/2/3/all; candidate separation; native records unchanged | Optional compiler quality ablation deferred | Not passed |
| P3.6 validation/docs | Contract/stress fixtures and propagation limits complete; comparative task not completed | Independent source fixtures and bounded stress correctness; historical immutable check passed | Realistic precision/recall/coverage/latency/RSS study deferred | Not passed |
| P4.1 baseline | Development fixture/provenance and harness interfaces complete; adjudicated release baseline pending | Existing fresh GraphMark manifests and recorded outputs preserved | New collection/adjudication and split freeze deferred | Not passed |
| P4.2 ranking core | Complete: deterministic bounded candidate-only PPR | Mass/dangling/cycle/invalid-weight/duplicate/hub/scope tests; historical immutable check passed | No quality inference from numerical correctness | Not passed |
| P4.3 query integration | Complete: explicit experimental mode, scope/render/guidance constraints and deterministic fallback | Capture/freshness/exact-match tests; compiler view used consistently; historical immutable check passed | Deferred | Not passed |
| P4.4 development ablations | Harness controls implemented; comparative task not completed | Current/uniform/weighted and expansion-control plumbing; correctness smoke checks only this phase | Existing zero-gain result retained; further ablations deferred | Not passed: no demonstrated winning configuration |
| P4.5 held-out | Protocol and required harness interfaces available; study not completed | No held-out result claimed | Deferred; requires adjudicated disjoint set and frozen candidate | Not passed |
| P4.6 downstream | Conditional protocol available; study not completed | Current remains default; no promotion | Deferred until retrieval gate and prospective power design | Not passed |

Historical P1 focused race passed (49.124s); graph-requested regression passed (0.811s). Windows admission helper compiled successfully; this is compile-only evidence.

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

The earlier `0038ef70` full `mise run check` passed in 626.23s, but does **not** verify subsequent changes. Its logs, earlier schema-golden failure/correction and source freeze remain historical evidence. The later `05ad9842` check now covers the captured-preselection and cancellation fixes.

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

Final validation at the original implementation checkpoint: source commit
`88dd1dc9` passed the complete repository check and pinned Linux correctness.
The initial Linux Git-version prerequisite failure is retained; the upgraded
run passed without product changes. The validation VM and the P1 workers were
deallocated after correctness/diagnostic work. Later source and evidence
commits are recorded separately below and do not inherit that checkpoint's
verification. No comparative campaign ran during implementation. No merge was
performed.

P1 campaign sources: the user-approved pinned chi, Kubernetes, Zod and Requests
repositories are evaluation inputs only, alongside frozen Entire source and an
independent synthetic fixture. See `corpus/corpus-manifest.json` for revisions,
licenses, selected paths and fixture origins. Official OpenAI model guidance
was consulted only for the requested cheaper subagent selection.

P1 execution monitoring: heartbeat `monitor-p1-corpus-campaign` checks every
15 minutes. Three Luna subagents prepared the corpus, harness and protocol.
The earlier `039d213b` and `dc0ddce7` checks are historical. Current
verification is summarized at the top of this ledger; no comparative gate
pass is claimed from a paused campaign.

P1 baseline freeze: `2840a152`, authoritative `frozen-baseline.json`.
Kubernetes fast/full snapshots each timed out three times at 120 seconds;
the protocol blocks those two strata and preserves 960 planned unrun cells.
The remaining 16,320 paired requests retain the original settings. The
`039d213b` check passed historically in 1126.765 seconds with source hashes
unchanged; see `p1-corpus-20260905/correctness.json`. The later `dc0ddce7` and `05ad9842`
checks supersede that historical verification. No release gate has
passed.

Historical P1 monitor (`monitor-20260905T2234.json`): all three workers were active;
66 measured requests (63 partial, 3 timeouts), 1,434 explicitly unrun, and
15,780 pending planned cells. Kubernetes syntax-only snapshot cache-on requests
also hit the 120-second limit three times; its six actual requests are retained
and its other 474 cells are unrun. The two baseline-blocked strata retain
960 unrun cells. Frozen binary, input and baseline hashes still match.
No interim performance or quality conclusion is drawn. P4.1 remains pending;
a previous ledger wording error is corrected without changing P4 status.

Historical user-directed diagnostic stop: all three services verified inactive on
2026-09-05 at approximately 22:55 UTC; recurring continuation was paused at that checkpoint.
Retained 116 measured requests (113 partial, 3 timeouts), 1,434 protocol-unrun
cells, and 15,730 remaining planned cells. Interrupted in-flight requests
are preserved separately and are not fabricated as completed observations.
No further measurement is authorized by the old continuation; diagnose and
correct the findings before defining a separately versioned evaluation.

## Current evidence and pause state

Product source and verification are recorded once in Current phase above.

The retained Kubernetes syntax-only diagnostic was run from `5a60fc8f` on
Darwin/arm64. The cache-off arm exceeded its 120-second context deadline and
returned an error after 161.18 seconds; the cache-on arm was unrun. The
ten-second stack diagnostic is retained under
`p1-corpus-20260905/retained-search-timeout-trace/` and only locates work in
listed Git-directory observation. It is not a campaign observation and does
not establish semantic equivalence or a performance result.

All campaign services remain stopped. Validation VM deallocation was requested after collecting the diagnostics;
both campaign worker VMs remain off. No
campaign has resumed. Historical campaign counts remain 116 measured requests
(113 partial, 3 timeouts), 1,434 protocol-unrun cells, and 15,730 remaining
planned cells; interrupted requests remain separate evidence. No release gate
has passed.

## Historical checkpoint record

The following entries are retained as dated or explicitly versioned historical
evidence. They do not override the current source, pause, or release status
above.

- `f96483ef` and `9bfafe17` added bounded cache-publication batches; focused
  extraction race tests passed. `4f582425` fixed parallel relation ordering and
  `f6b8a9d1` added cache/fresh selective equivalence checks. These checks did
  not explain all retained real-corpus mismatches.
- The stopgap protocol in `stopgaps-v2.md` added first-issue worker pause,
  cross-worker supervision, expiring leases, immutable run directories and
  canary admission. Stopgap evidence is preserved by `3b242da7`, `5957f604`
  and `69268ec4`; no campaign was restarted from those checks.
- At `0381ea6d`, 56 harness unit/local subprocess tests passed in 4.561s with
  mocked Azure transport. This checkpoint required successful worker exits,
  terminal-state verification and upload acknowledgement before collection.
- The captured-selection prototype `134c3752` retained a failing
  binary-attribute parity regression; oversized and locale-sensitive matching
  remained unresolved. It was not a release candidate and had no full
  repository check.
- At `5a60fc8f`, focused semantic race (35.322s) and Git utility (22.896s)
  checks passed with unchanged source hashes. The checkpoint added bounded
  oversized-file evidence and operation-bound Git-attribute decisions; it did
  not complete repository-subdirectory, locale/platform or full retained-corpus
  parity.
- The verified `dc0ddce7` checkpoint retained 55 off/on pairs with distinct
  preselection paths. Access telemetry included selected-pool hydration, and
  the combined large parity/mutation run passed in 16.860s. A reduced Git
  matcher contract still failed because a `.gitattributes` binary-classified
  file was included; this remains historical diagnosis, not current release
  evidence.

Corrective diagnostic `retained-snapshot-d793b2be/`: one pair at fully verified
source, identical semantic/partial/warning digests and unchanged corpus before,
between and after requests. OFF53.492s and ON73.462s exceed the 1.10 cold
screen. RSS is unavailable. This failed screen is retained without a statistical
claim or campaign expansion; prior failures remain untouched.

Remaining-cost diagnosis: `cold-profile-d793b2be/` retains one ON-only CPU
profile with exact semantic digest/194 partials. Publication accounts for20.86
cumulative CPU-seconds, including11.27 in quota maintenance; these samples
are not a wall-time comparison. Two collection failures and their recovery
are retained; the product request was not rerun. The next change under review
removes duplicate envelope encoding within existing bounded publication.
Quota-scan alternatives require explicit correctness review; no quota limit,
corpus, threshold or default is being tuned.
