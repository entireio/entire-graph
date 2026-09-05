# Graph advantage implementation ledger

Requirements: `/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md`, read in full; exact SHA-256 in `evidence/requirements-provenance.json`.
Baseline: fetched origin/main `3a2a715fad1948e83dc7ebe0d307377ba29e065a`.
Product branch: `codex/graph-advantage`; final product commit `0038ef70ca5e829c150a977d8021ee9fe9c7103a`.
Evaluation branch: GraphMark `codex/graph-advantage-evaluation`, commit `c22eda1`.
Primary Entire Graph checkout and its existing HEAD were preserved. No merge.

## Current checkpoint

All four opt-in implementations and cross-workstream acceptance fixtures are
frozen. The full repository check passed626.23s after a version-only correction
to29 snapshot goldens; product code is unchanged. Source identity is
`evidence/final-source-freeze-v2.json`. Final Linux combined race passed6.78s.
The dedicated Azure VM is verified deallocated; task-owned private storage/disk
remain available for reproducibility. The obsolete temporary baseline-check
worktree was removed after verifying it clean.

No additional feature work or benchmark tuning is underway. Each worker returned
bounded implementation, fixtures, exact results and remaining gates. A fresh
read-only reviewer found no substantiated blocker in capture finalization,
compiler/native distinctions, cache refusal, schema or CLI/default integration.
This review does not replace execution evidence or promote a feature.

## Tasks (current integration checkpoint)

| Task | Implementation / status | Tests and evidence | Remaining |
|---|---|---|---|
| P1.1 | Pinned baseline characterization and paired harness | baseline/phase artifacts; extraction-paired-v2: 810 equal pairs | Full-profile cost audit complete; complete pinned real corpus/RSS/scenarios still required |
| P1.2 | Explicit private payload, shared bounded capture, final operation identity and sticky storage errors | Entity field checklist, language/overload/malformed fixtures, capture mutation tests | Operation manifest/sticky-error race fixtures passed; final integration check passed626.23s |
| P1.3 | Atomic private versioned/checksummed storage, bounded decode/quota and no-follow | Concurrent same-key writers, corruption/key isolation, symlink refusal | Positive capped environment quota overrides implemented; upper bounds 1GiB/100k |
| P1.4 | Declaration reuse with fresh ordered reduction/aliases/relations | P1-A counts 3/0/1 parses; manifests/deletion/ignore and source freshness | Final combination checks; no default enablement |
| P1.5 | Format3 raw-import family for Go/TS/Python fast/full; other families absent | 450 cost observations; exact relation parity; focused raw-import race32.187s | Calls/scopes remain uncached; raw imports modest3-4% isolated relation benefit, not end-to-end claim |
| P1.6 | Separate extraction/cache/read telemetry and reproducible development evidence | 810 paired comparisons; frozen29520508 full check passed | Performance gate fails: large full cold +22%; required release corpus/scenarios/RSS incomplete |
| P2.1 | Pinned gopls v0.20.0 Linux Bubblewrap live feasibility | Grandchild network denial/source EROFS; linux-gopls-feasibility and Linux race logs | Other platforms explicitly unavailable |
| P2.2 | Bounded stdio lifecycle, cancellation/process cleanup and immutable capsule | Protocol failures/UTF16/tags/workspaces/local replacement, live race | External ambient dependency roots intentionally unavailable |
| P2.3 | Exact direct declarations and separate implementation candidates | Promoted/generic/alias/interface/closure/build-context fixtures | Dynamic runtime targets intentionally unresolved |
| P2.4 | Additive native overlay, exact-site reconciliation, projection refusal | Native/static retention, candidate namespace, schema1.2 checks | Linux cross-workstream race passed; final repository check passed626.23s |
| P2.5 | Source/context invalidation, explicit fallback and frozen fixture quality evaluation | Static4/6 vs compiler6/6 direct; zero false confirmations; candidates2/2 | Synthetic compiler-checked labels do not establish independently adjudicated real-world recall gate |
| P3.1 | Explicit direction/composition policy; default depth unchanged | Go/TS/Python chains; Go routes/HCL resources; existing depth gold tests | Realistic independent label adjudication |
| P3.2 | Bounded deterministic predecessor traversal and adjacency construction | Cycles/diamonds/hubs/shuffle/cancellation/all budgets | Final repository check passed626.23s |
| P3.3 | Evidence alternatives, terminal tests, counts/coverage/stop codes | Seeded path reconstruction; confidence and evidence byte/output-step bounds | No runtime safety inference |
| P3.4 | DepthN/all CLI, filters/budgets, optional JSON and bounded text | Focused race suite; default/invalid-control/byte notice checks | Help/schema integration implemented; final repository check passed626.23s |
| P3.5 | Separate compiler-candidate paths and effective direct view | Distinct path category and per-category alternatives | Live combined query verification passed6.78s; final repository check passed626.23s |
| P3.6 | Contract/stress tests, provenance, propagation-limit docs | 1000-dependent traversal ~1.21–1.50ms/op; not end-to-end/RSS | Realistic adjudicated precision/recall/coverage and total-query/RSS gate unestablished |
| P4.1 | Frozen fresh GraphMark development fixtures/labels/outputs/provenance | GraphMarkc22eda1, six author-labeled repository families | Independent adjudication and disjoint holdout missing |
| P4.2 | Deterministic bounded candidate-only PPR | Mass/dangling/cycle/invalid/duplicate/hub/scope tests | No quality inference from arithmetic |
| P4.3 | Explicit ranking mode, current/exact/deep fallback, rendering/guidance retained | Capture mutation and next-query scope/guidance tests | Final combined Linux race passed |
| P4.4 | Fixed development CLI current/weighted and API current/uniform/weighted ablations | 120 CLI plus180 API observations; all three arms recall/coverage1; relative gain0% | Full retrieval uniform arm complete; no winning configuration; compiler-winner arm conditional |
| P4.5 | Held-out protocol recorded; no outcomes invented | Development gate not passed | Fresh disjoint independently adjudicated holdout absent; no tuning against holdout |
| P4.6 | Conditional study/power planning recorded; current stays default | No unsupported promotion | Agent study not run because retrieval prerequisite has not passed |

## Release decisions and genuine limitations

No advantage release gate is declared passed. P1's earlier frozen development
comparison has a cold regression and does not cover the required final real-corpus,
edit-scenario/RSS matrix. P2 and P3 have executable correctness evidence but lack
broad independently adjudicated realistic quality evidence. P4's fixed development
set shows0% recall improvement; independent held-out collection/adjudication is
incomplete and the conditional powered agent study was not run. These remain
unfulfilled evidence requirements, not achieved advantages.

Defaults stay extraction off, compiler off, impact depth2 and current ranking.
Compiler support is tested Linux only; external dependency roots that cannot be
captured safely are explicitly unavailable. Dynamic runtime targets remain
unresolved/candidates. Capture memory limits cover retained store payload, not
all process RSS. Policy provenance explicitly limits nested/vendor-rule coverage.
Concurrent cache writers can temporarily exceed per-operation quota reservations;
this is documented rather than represented as a global hard filesystem quota.

## Source and fixture provenance

No prior conversations, memory files, competitors, comparison documents or
adjacent comparative evidence were consulted. System-provided context included
general summaries; they were not implementation evidence. No legal certification
or independently certified prior-exposure history is claimed.

Consulted Entire sources: AGENTS.md and .entire/graph-agent.md; schema ADR0001;
trust/security and mise build/check instructions; provider/parser/Entity metadata,
parallel reducer, cache confinement/policy, relation emitters and search/impact
entrypoints. Every changed source/test and per-workstream review names its
implementation evidence. Current-main touchpoints were revalidated before edits;
main's parallel pipeline was retained rather than replacing it with the plan's
older baseline design.

Official sources consulted for P2 are LSP3.17, gopls navigation/settings and Go
modules, with URLs/hashes in ADR0034. Azure CLI help and normal tool distribution
channels were used only for provisioning; runtime provider execution never
fetches tools/dependencies. Installed Go1.26.1/goplsv0.20.0/launcher versions and
binary/source hashes are retained in Linux manifests.

Fixtures were independently authored from plan contracts and Entire's maintained
behavior: P1 declaration/overload/scope/malformed/read-schedule/cache cases;
P2 exact Go tokens, interface categories, module/workspace/build-context and
hostile descendant boundary probes; P3 call/type/field/route/resource chains,
cycles/hubs/diamonds and seeded proof reconstruction; P4 mathematical examples
and fresh GraphMark Go/TS/Python development repositories. Expected targets and
paths are hand-derived and/or checked through pinned gopls, never another product.
P4 labels are author expectations pending independent reviewer adjudication.

## Validation index

- Initial stageA and a70a1892 full checks passed; frozen29520508 check passed629.12s.
- `shared-integration-race-v2.txt`, `capture-contract-tests.txt`, `raw-imports-focused.txt`, `p3-focused.txt`, `p4-focused-tests.txt`: focused race checks.
- `linux-p2-v4.txt`, `compiler-quality-v1.json`, `advantage-combination-v2.json`, associated Linux source manifests: actual pinned backend correctness/quality fixtures and all-feature combinations.
- `extraction-paired-v2.ndjson` and summary:810 pairs/1620 observations from29520508; explicitly earlier development evidence, not final release evidence.
- `relation-profile-v1.ndjson`:450 bounded characterization observations and exact precomputed-import equivalence.
- GraphMark fresh evaluation:120 CLI current/weighted and180 API current/uniform/weighted observations, exact inputs/outputs/settings/scripts and provenance.
- `scoring-reproduction.txt`: retained P1/P4 summaries reproduced exactly from raw data without query reruns.
- `check-integration-final.txt`: first full integration run645.62s failed only29 schema-version goldens. `schema-goldens-v12.txt`: exact version-only correction passed all29 under race4.850s.
- `check-integration-final-v2.txt`: final complete check passed626.23s at0038ef70. No product code changed; initial failures remain retained.

## Reviewable stages

Earlier branch stages introduced the P1.1/minimalP1.2 seam first, then capture,
protocol/evidence, traversal and pure ranking groundwork. Resumed work:

- 7f123bcc: integrated opt-in captured declaration reuse.
- 29520508: executable isolated Linux feasibility evidence.
- 0c721a9e: bounded pinned compiler adapter.
- ac488773: structural traversal policy/core bounds.
- 92753f79: shared semantic capture/reuse/evidence/ranking integration and schema1.2.
- 182c2e5c: optional query/compiler CLI.
- 0038ef70: deeper impact CLI/help and cross-workstream acceptance tests.

No all-feature PR was opened. Product and GraphMark evaluation commits stay on
separate review branches; no merge or default-promotion action occurred.

## Rollback

Select `--extraction-cache off`, `--compiler off`, `--depth 2` and
`--ranking current`. Compiler/ranking state is operation-local. Extraction data
is disposable in its separate `extraction-*` cache namespace. Revert branch
commits to remove implementations. No migration, installation, MCP, generated
summary or Brain change is required. Persistent working-tree snapshot caching
remains disabled.
