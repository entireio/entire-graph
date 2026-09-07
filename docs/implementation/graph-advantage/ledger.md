# Graph advantage implementation ledger

Requirements: `entire-plan/entire-graph-advantage-implementation-plan.md` and the explicitly requested interim review `entire-plan/graph-advantage-progress-review-2026-09-05.md`.
Baseline: fetched main `3a2a715fad1948e83dc7ebe0d307377ba29e065a`.
Branch: `codex/graph-advantage`, isolated worktree; primary checkout preserved. No merge.

## Current phase

**Resumption policy revised 2026-09-07:** use batches capped at 100 total
product invocations, review and fix issues between batches, and establish
stability before proposing a full run. Full-campaign execution requires the
user’s explicit approval; none is granted. The cap, durable-claim, duplicate
dispatch, first-issue stop, and approval-boundary controls are implemented at
`58f03a22`; see `resumption-plan-20260907.md`. The user has resumed the
revised controlled sequence; no campaign is currently running.

**Current bounded-resumption status:** the user’s resume authorizes controlled diagnostics, fixes and sampled batches once the documented prerequisites and controls pass. The resumed sequence has consumed five controlled product invocations: the completed cache-off Kubernetes syntax-only snapshot in `diagnostic-dispatch-1f20f694` and four full-profile Kubernetes snapshot timeouts in `diagnostic-dispatch-12574522-r2`, `diagnostic-dispatch-90ac3e16`, `diagnostic-dispatch-450bede9`, and `diagnostic-dispatch-8689fc3d`. The latest request at source `8689fc3d790e4290ce2b62500d270c96c02087b3` used the accepted focused-only `diagnostic-validation-v1` policy and timed out at 120 seconds (`process=-9`, `collector=1`) with no observation or diagnostics, while retaining a complete diagnostic-only 20.027-second relations CPU profile. Its input and control identities remained unchanged, no retry was made, and the validation VM was deallocated. A separate earlier full-profile dispatch failed in the collector before product startup (`reserved=1`, `consumed=0`, `transport_attempts=1`). None is a stability batch or campaign result. No stability batch has run, and all four product timeouts remain unresolved. Offline analysis of the retained `90ac3e16` profile identified unconditional Go route-regex scans as a CPU hotspot, and source `450bede9970e05c994d80986cf105dd068e9fc29` added a route-candidate prefilter. Its pinned Linux evidence passed 28 compiler tests including 10 live tests with zero compiler skips or failures, the focused route normal/race and profiler checks passed, and the evaluator build produced binary SHA-256 `b4d7e1a5ef72d4e09d045e562ce08cb38ff63a9c3912aef0e002bc70bb5f0674`; evidence was committed at `6e050656` under `evidence/diagnostics-linux-450bede9/`. Source `8689fc3d790e4290ce2b62500d270c96c02087b3` adds a route-token boundary correction; its pinned Linux affected normal/race checks each passed 10 tests, compiler correctness passed 28 tests including 10 live tests with zero skips or failures, and the evaluator build produced binary SHA-256 `259a56073fb54e74f2d1d60a3a6a98d87a7eec1c992f86a0c665a35c3cdb9559`. That evidence is retained under `evidence/diagnostics-linux-8689fc3d-r1/`. These are focused correctness and build results only; no immutable full check exists for `8689fc3d`, and the latest completed immutable full check remains the historical `450bede9` run. The diagnostic preparation was committed and pushed at `ad41af6fb21c0b046d3f11716683990954d2ef3d`. Offline analysis of the latest profile found that route-regex work remains a CPU hotspot; source `78c8b496` implements the route refactor and its focused correctness evidence is retained, but controlled runtime verification remains pending, so no further sampling is authorized before that run. No timeout resolution, stability, release or performance claim follows. No full campaign is approved.

**Mandatory model routing, 2026-09-07:** all substantive implementation,
diagnosis, source/data inspection, fixture or harness work, execution/testing,
result summaries, documentation, and Git, VM, or automation/control-plane
execution must be performed by cheaper-model subagents: Luna for routine work
and Sol for complex work. The main Astra agent is limited to decomposition,
assignment, coordination, evidence/diff review, adjudication, next-step
selection, and user communication. No Astra subagents or silent fallback to
Astra execution are allowed; report an unavailable cheaper worker and leave
the step unexecuted. Worker commands or VM tasks remain subject to existing
authorization, the shared 100-run batch cap, first-issue stops, and explicit
approval before a full campaign. No product or corpus work is currently
running; controlled work remains subject to the revised prerequisites and
stops.

The latest completed immutable full check is `450bede9`: serialized
`mise run check` exited 0 in 1204.774 seconds at exact source
`450bede9970e05c994d80986cf105dd068e9fc29`, with 151 shell checks passed,
unchanged HEAD, clean before/after status and byte-equal tracked-source
manifests. It performed no product or corpus evaluation; evidence is in
`evidence/check-450bede9/`. The `90ac3e16`, `8c8b075d` and `1f20f694`
immutable passes remain historical evidence; the first `90ac3e16` launcher
failure on `mise` trust remains retained separately as a non-test failure.
Focused YAML/ABI checks for the post-fix
`12574522` source passed, including the seven-test ABI check reported at
1.600s and 2.983s, and the pinned Linux compiler/evaluator check and
build passed, but the local full `mise run check` at that source is incomplete:
the first run ended with Darwin compile/cgo workers killed before wrapper
finalization, the first serialized retry ended before a terminal result, and
retry 2 reported `Finished in 86.27s` followed by `[test:ci] ERROR task failed`
without a completed `test:ci` result. Its foreground controller then sent
Ctrl-C after a GitHub username prompt; this is a task/transport failure, not a
test assertion. Retry 2 retains the same source/head and tracked-hash
snapshots, but `result.json` is missing and `postcheck_completed` is null, so
those post-check files cannot be conclusively attributed to wrapper completion.
The observed before/after source snapshots match, but retry 2's capture
provenance is indeterminate; none is an immutable source-pass or release claim.
Evidence is retained in
`evidence/check-12574522-retry-2/`. The offline fixture correction and focused
normal/race proof that removed that credential prompt are retained in
`evidence/diagnostic-graph-bench-offline-fixtures-12574522/`. The earlier `6cf92c9c` check and pinned Linux
evidence are also retained historical evidence. Accepted changes after those checkpoints include
`4cd72774`, `c5971406`, `5ed08ed6`, `7c3405ae`, `84ab23aa`, and harness
`84cbb46d`. The historical `8763b0d8` check failed after 1056.44 seconds:
`TestTreeSitterParserYAMLMasksQuotedMappingKeys` in `parser_test.go:1204`
returned `E_PARSE_ERROR`, with an independent documentation-state failure
also recorded. No current-source verification or release claim follows from
that failed check.

The immutable full `mise run check` for source `6f23da0ad4fa704da8c7bac53e1065030c89a4f1` terminated failed at `2026-09-07T16:39:00.860837Z` after 2334.829470 seconds with return code 1. The source checkout was unchanged before and after; the `internal/sem` race suite failed after 1801.172 seconds, so this is not a full-check pass or release gate. Raw evidence is retained under `evidence/check-6f23da0a/`; the completed-check baseline remains `450bede9`.

A second immutable check of the same source, with `MISE_JOBS=1`, `GOFLAGS='-p=1 -v'` and `GOMAXPROCS=4`, also terminated failed: it ran from `2026-09-07T16:49:14.206582Z` to `2026-09-07T17:36:04.432858Z` (2810.177653 seconds), with the cumulative `internal/sem` race run reaching its 30-minute timeout. Raw terminal evidence is retained under `evidence/check-6f23da0a-cpu4/`; source and tracked state were unchanged. This corrected configuration is not evidence of cause, and no concurrency ladder or further retry is authorized. The profiler worker is analyzing the existing verbose log only; the 36-second and 6-second tests were the tests running when the alarm fired, not isolated reruns or results, and the prior isolated normal/race passes belong to different tests in the first failed check. Counts remain five controlled product invocations and zero clean stability batches; all VMs are off.

Two separate Linux cache-fixture infrastructure attempts did not reach tests. The original `evidence/check-6f23da0a-linux-cache-fixtures/` attempt stopped at the URL-substitution guard before tests; exit 73 is inferred from the guard path rather than observed. The `evidence/check-6f23da0a-linux-cache-fixtures-r1/` attempt observed remote exit 74 because the source directory or the temporary source archive was absent; retained evidence does not identify which. Transport and upload both recorded exit 0. Both attempts ran zero tests and made zero product or corpus invocations; the VM reached terminal deallocated state at the end of r1. The r2 durable-archive repair/check succeeded for its scoped cache-fixture validation: exactly three named tests passed with zero skips or failures in 2.190 seconds, using Go 1.26.1 and gopls v0.20.0; source-hash and source-comparison checks passed. mise was absent, so this is not a full `mise run check`, a timeout explanation, or a performance result. All three validation VMs were deallocated.

The three retained query profile paths are verified at `1c0b8e24`: syntax-only, fast and full all have exact semantic, warning, completeness and full 11-record partial parity, with 381 indexed files per arm and unchanged inputs. These are the three distinct requests behind the 55 historical repeated mismatch pairs. Seven requests ran: two completed pairs, a full OFF stopped on a warning-oracle error, then only the corrected full pair. The warning correction came from the original full-profile baseline. Historical repetitions remain retained, not relabeled as new observations. Evidence: `p1-corpus-20260905/retained-query-correctness-1c0b8e24/summary.json`.

The P1 campaign remains paused. Baseline counts remain 108 requests (69
complete, 33 partial, 6 timeouts); this is a collected but incomplete baseline,
not a completed release baseline. Campaign counts remain 116 observed requests
plus explicit unrun accounting. The resumed sequence has consumed five
controlled diagnostic product invocations, recorded separately from campaign
and stability-batch counts: the earlier completed syntax-only snapshot and four
full-profile timeouts at `diagnostic-dispatch-12574522-r2` and
`diagnostic-dispatch-90ac3e16`, plus the post-prefilter
`diagnostic-dispatch-450bede9` and boundary-corrected
`diagnostic-dispatch-8689fc3d` (each with a 120-second bound, process exit
`-9`, collector exit `1`, and no observation or diagnostics). The `90ac3e16`,
`450bede9`, and `8689fc3d` profiled diagnostics retained complete 20-second
relations CPU profiles with unchanged
before/after corpus identity and zero control-identity mismatches. Offline
analysis of the latest profile found that route-regex work remains a CPU
hotspot. Neither route guard resolved the timeout; source `78c8b496` now
contains the route refactor and its focused correctness evidence, but no
controlled runtime verification exists yet. The latest diagnostic used
the accepted focused-only diagnostic gate; it is not a full-check or admission
substitute. The
earlier pre-product collector failure remains a separate transport attempt
with zero consumed product invocations. The r2 input identities matched and
the VM was deallocated; its first recorded relations progress event reported
512 relations after 30,866 files and 418,711 symbols, not a final relation
total. Its raw archive and compact failure manifest are retained. No retry
was made. The earlier completed diagnostic's collector completed with 194 known partials
and one warning. The lossless source-review packet has classified all 194
entries, but that classification does not close parser issues, verify every
current file, or adopt proposed ADR0049 reviewed-partial admission. No
performance or release-gate claim is made. The latest cold snapshot pair at
`6cf92c9c` preserved exact semantics, warnings and 194 known partials with
unchanged inputs. OFF 57.769s / ON 62.614s (ratio 1.084) and peak RSS
3,312,332,800 / 3,167,264,768 bytes (ratio 0.956) were within both 1.10
screens. This single pair is not statistical, causal or release evidence; the
previous failed RSS screen remains retained. Remaining baseline timeouts,
partial admission and the four unresolved diagnostic timeouts still prevent campaign
expansion. Evidence: `p1-corpus-20260905/retained-snapshot-6cf92c9c/summary.json`
and `evidence/diagnostic-dispatch-1f20f694/`. No stability batch has run, and
the retained CPU profile does not establish a stability or performance gate.

The bounded execution controls are implemented at `58f03a22`: selected manifests are capped at 100 derived product invocations, preparation and arm costs are reserved before spawn, worker claims are durable and path-stable, duplicate/retry dispatch is refused, and the first issue stops the batch. The committed controller and focused Python contracts passed 90 synthetic-only tests in 7.985225 seconds; evidence and source hashes are in `evidence/bounded-controls-58f03a22/`. The five controlled product invocations did not close partial admission, timeout or performance gates, zero clean stability batches have run, and no release gate passed.

Source `78c8b496` implements the bounded Go HTTP route-parser refactor motivated by the retained relations profile. Focused normal, race, compatibility-oracle and resource-bound checks are recorded in `evidence/route-parser-profile-8689fc3d/`; this is implementation/correctness evidence only. The controlled runtime timeout remains unresolved pending focused diagnosis, and diagnostic packaging/runtime execution is blocked; no speedup, stability or release claim follows.

All 77 campaign-control tests passed; a live fake-service smoke verified that all three active workers stopped after an injected pause. The validation VM is confirmed deallocated after correctness and corrective evidence collection; the two campaign workers remain deallocated. No campaign is running. P2/P3/P4 comparative studies remain deferred, and no complete workstream release gate has passed. Defaults remain extraction reuse off, compiler off, impact depth two and current ranking.

The test-only corpus harness now has an optional `diagnostics_path` artifact containing every failure and warning. Eight focused tests, including tiny-repository subprocess plumbing and race, passed. The `aab356ae` check command passed in 705.514s, but its immutable-state gate failed because gofmt changed six original reviewer input files; that failure and formatting diff remain retained. Those inputs now use `.go.txt` storage with unchanged original hashes and logical-path mapping. The packaging fix was verified by the pinned immutable `mise run check` at `1f20f694`: exit 0 in 42.970406 seconds with unchanged HEAD and clean status; see `evidence/check-1f20f694/`. Existing campaign admission remains unchanged; no old sampled observation is relabeled fully reviewed. See `p1-corpus-20260905/admission-audit-1058c133/README.md`.

## Authoritative task status

This table supersedes earlier checkpoint/status statements. “Complete” in the
implementation column describes code or harness delivery. The latest
immutable full check is the current `450bede9` checkpoint described above.
The three retained query profile paths are historical exact
partial-output parity evidence. Broader evaluation remains paused. Evaluation
tasks cannot be declared complete merely because their harness exists;
correctness checks do not establish comparative release gates.

| Task | Implementation | Correctness evidence | Comparative evaluation | Release gate |
|---|---|---|---|---|
| P1.1 characterize | Complete: phase/capture characterization and reproducible paired harness | Existing pinned phase artifacts and controlled-reader fixtures | Fixed six-repository campaign prepared; baseline collected but incomplete (69 complete, 33 partial, 6 timeouts); paired campaign paused for diagnosis | Not passed: full fixed corpus/RSS matrix incomplete |
| P1.2 pure extraction | Complete: explicit metadata, shared bounded source/policy capture, manifests and sticky errors | Entity field checklist, malformed/overload/language round trips, reader mutation tests; historical immutable check passed | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.3 storage | Implemented through `6cf92c9c` and later accepted source changes: encoded publication, capability-bound admission, session-owned compression and operation-wide batch gate; historical `450bede9` full check passed, while `6f23da0a` full check failed in `internal/sem` | Independent-operation/subprocess contention, corruption and no-follow regressions passed race; focused Linux source-`6f23da0a` evidence passed 32 normal, 32 race and 28 compiler tests including 10 live tests; full-check failure remains retained | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.4 entity integration | Implemented; all three retained query profile paths and the diagnostic snapshot have exact partial-output parity; full corpus evaluation incomplete | P1-A parse counts; manifest/rename/delete/ignore and selective-scope freshness fixtures; historical immutable check passed; this does not establish the full fixed-corpus gate | Fixed corpus campaign paused for diagnosis | Not passed |
| P1.5 relation inputs | Complete for measured raw-import family: Go/TypeScript/Python fast/full; explicit family presence; `450bede9` and `78c8b496` add correctness-tested Go route candidate filtering/refactoring | Exact relation parity and reuse tests; focused route normal/race/oracle/resource checks passed; other families deliberately absent | The refactor targets a profiled hotspot, but no controlled speedup or timeout resolution is established | Not passed |
| P1.6 diagnostics/gates | Implemented; local first-issue stops verified, live fake-service smoke passed; ADR 0046 verified and ADR 0047 verified; ADR 0048 focused and pinned checks passed; historical `450bede9` full check passed, while `6f23da0a` full check failed in `internal/sem` | Separate parsed/reused/source/cache/phase telemetry; focused Linux source-`6f23da0a` evidence passed 32 normal, 32 race and 28 compiler tests including 10 live tests; no full-check gate follows | Existing cold regression retained; fixed corpus campaign paused for diagnosis | Not passed: performance and full-check correctness remain unresolved |
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
| P4.1 baseline | Development fixture/provenance and the bounded isolated candidate-expansion ablation interface are available | Existing fresh GraphMark manifests and recorded outputs preserved; focused expansion contract evidence retained | New collection/adjudication and split freeze deferred | Not passed |
| P4.2 ranking core | Complete: deterministic bounded candidate-only PPR | Mass/dangling/cycle/invalid-weight/duplicate/hub/scope tests; historical immutable check passed | No quality inference from numerical correctness | Not passed |
| P4.3 query integration | Complete: explicit experimental mode, scope/render/guidance constraints and deterministic fallback | Capture/freshness/exact-match tests; compiler view used consistently; historical immutable check passed | Deferred | Not passed |
| P4.4 development ablations | Implemented at `6f23da0a`: one bounded candidate-expansion policy is available to all four evaluation arms; `current-expansion`, `uniform` and `weighted` have identical no-compiler candidate pools while `weighted-compiler` may differ when compiler evidence changes eligible relations; default/product behavior is unchanged | Focused normal/race expansion, scope, determinism, fallback, cancellation and source-read checks passed; evidence: `evidence/p4-candidate-expansion-control-20260906.md` | No new retrieval measurement or tuning; existing zero-gain result retained and comparative task deferred | Not passed: no demonstrated winning configuration |
| P4.5 held-out | Protocol and expansion interface available; study not run | No held-out result claimed | Deferred; requires adjudicated disjoint set and frozen candidate | Not passed |
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

The P1 sequence below is superseded by `resumption-plan-20260907.md`: bounded100-run sampling first, explicit user approval before any full campaign. No canary or sample result authorizes automatic expansion.

1. After final correctness passes, prospectively freeze the binary, source, environments, workload matrix, labels, metrics and trial rules. Preserve earlier failed outcomes.
2. P1: execute the fixed small/medium/large and synthetic cold/edit/rename/delete/branch/manifest scenarios with at least 30 paired repetitions, identical inputs, documented page-cache state and RSS/disk measurements.
3. P2/P3: independently adjudicate exact required/allowed/forbidden targets and affected sites, keeping candidates separate. Run the hard-Go and depth-sensitive slices with total cost and coverage accounting.
4. P4 development: run current, identical-expansion baseline, uniform graph and weighted graph under identical captured inputs and byte budgets. Run the compiler arm only under the preregistered dependency and include startup cost. Freeze any selected configuration without tuning on held-out outcomes.
5. Run a disjoint adjudicated holdout once per frozen candidate. Only after retrieval clears its gate, perform the prospectively powered paired agent experiment. Failed or inconclusive gates retain experimental/default-off status.

## Consulted sources and fixture origins

Implementation sources are the plan, the user-authorized interim review, applicable AGENTS/generated graph instructions, Entire source/tests, schema ADR 0001, trust contract and repository checks. The review is now explicitly a consulted source; older blanket statements excluding every review/comparison document are superseded. No competitor implementation, prior conversation, memory file or adjacent comparative corpus was used as implementation evidence.

Official P2 references remain LSP 3.17, gopls navigation/settings and Go modules, recorded in the existing ADR/source manifests. Tool distribution/Azure provisioning is separate from runtime provider behavior. Runtime analysis does not fetch tools or dependencies.

Fixtures derive from the plan and Entire's maintained behavior, with hand-derived expectations or pinned official compiler checks. New review fixtures cover type conversions/aliases/generic conversions, exact-site redirects and ordinary search/impact propagation. The terminal-test fixture combines P2-B candidate distinction with P3-B terminal test semantics. The pinned ordinary-query fixture uses a promoted method through an alias and records observed static coverage without assuming a static gap. No external product supplies expected answers.

Historical checkpoint notes and superseded status text are retained in
[ledger-checkpoints-through-6ebbb046.md](ledger-checkpoints-through-6ebbb046.md).

`reviewer-packet-v1/` contains all six existing synthetic P4 development
fixtures with verified source hashes and unanswered reviewer fields. It
withholds author labels and scores. It is not a realistic P3 change set or a
held-out split; those human-adjudicated inputs remain required.
