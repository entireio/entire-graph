# PACT for Entire

PACT turns confirmed developer intent into Graph-selected checks, executes them against pinned commits, and produces evidence a reviewer can replay.

**Track:** E2 — Build with Graph Intelligence. **Status:** working local/remote product with implemented Noon Curveball; not yet a complete submission. Post-Curveball cloud verification is complete and the implementation milestones are published. Authentic final-verification context is captured; submission remains open. Full scope is defined in [PACT_IMPLEMENTATION_GUIDE.md](PACT_IMPLEMENTATION_GUIDE.md).

## Problem, intended user and why it matters

A change can satisfy its new feature while breaking an unchanged caller. Reviewers need more than an agent's claim that a change is safe. PACT connects an explicitly approved permission policy to a real Entire Graph dependency path, runs the relevant cases, and distinguishes a regression from a pre-existing violation or an intentional policy revision.

The first supported domain is a **team-owned, synthetic permission application**, not a production security assessment. Its 24 cases vary role, operation, workspace relationship and visibility. Expected outcomes come from confirmed requirements, independently of the candidate implementation.

## Selected Entire track and why Entire is essential

Entire Graph resolves the changed `can_access` helper and the unchanged `export_document` caller. PACT traverses actual version-specific `CALLS` relations to select requirements bound to public entrypoints. Paths include source locations, edge confidence, snapshot hashes and exact commit identities. Without that relationship analysis, the demonstrated changed-file strategy misses both seeded regressions.

The Checkpoint adapter reads `entire checkpoint explain --json` plus the original session transcript, retaining exact excerpt hashes and locators. Real setup Checkpoint `7c02a621a7d5` is readable through `pact-B0`. **Its excerpts do not contain the later four-policy approval.** That approval was explicitly supplied by Shaurya during implementation and recorded in immutable requirement revisions. The missing original policy Checkpoint association remains visible as partial provenance; no unrelated source has been linked to make the report appear complete.

## Architecture and main workflow

1. An agent prepares four bounded permission predicates; a human inspects, confirms or revises them.
2. Git resolves baseline and candidate references to immutable SHAs. Only the registered fixture is exported.
3. Entire Graph produces a semantic diff and two independent snapshots. Reverse traversal selects affected registered requirements without mixing versions.
4. A subprocess executes each pinned fixture. The shared evaluator compares observed decisions with confirmed expectations.
5. The Databricks backend uploads a content-hashed runtime and input, submits a serverless notebook job, writes five Delta tables, reads rows back and returns a verified receipt.
6. The local FastAPI workbench shows failures, passed checks, partial evidence, historical runs and a correction workflow. A sealed JSON bundle replays without the original working tree or cloud access.

Implementation: `pact/src/btw_pact/`. Fixture: `pact/demo/workspace_app/`. Package/runtime upload is a ZIP with a SHA-256 hash; it contains the same evaluator modules used locally. It is an implementation choice permitted by the frozen plan's packaging flexibility.

## Entire Graph findings and verification

| Pilot version | Intended evaluation | Observed result |
| --- | --- | --- |
| `pact-B0` | Original policies | 8 applicable baseline assertions pass |
| `pact-H1` | Seeded over-broad guest/public allowance | 2 guest-export failures; guest preview works |
| `pact-H2` | Preview-only correction | All 10 applicable candidate assertions pass locally and remotely |
| `pact-H3` | Harmless refactor | All 10 candidate assertions pass |
| `pact-H4` | Alternative policy fixture | Pre-existing failures under original policy; intentional-change classification only with an explicitly revised test policy |

H3/H4 revision tests use actors explicitly labelled **synthetic test actor**. They do not pretend that Shaurya approved the alternative H4 policy in the live registry.

Measured B0 → H1 comparison, one local run per strategy, cache disabled:

| Strategy | Candidate scenarios executed | Failed assertions | Observed total |
| --- | ---: | ---: | ---: |
| Changed entrypoint file | 2 / 24 | 0 — misses the regression | 807 ms |
| Graph-selected requirements | 10 / 24 | 2 | 828 ms |
| All registered requirements | 10 / 24 | 2 | 828 ms |
| Independent full-matrix reference | 24 / 24 | 2 | 816 ms |

This pilot demonstrates a detection advantage over changed-file selection. It does **not** show a speed advantage, or fewer cases than all registered requirements. Full measurements and identities: [selector-comparison.json](pact/docs/evidence/selector-comparison.json). Graph evidence is structural influence, not proof of runtime reachability or complete program coverage.

The required final semantic review of the final submission SHA is still pending. Development search and recovery impact evidence are saved as [search](pact/docs/evidence/graph-search-recovery.json) and [impact](pact/docs/evidence/graph-impact-recovery.json); these are explicitly working-tree snapshots, not the final submitted tree. Current runs preserve real fixture diff/snapshot artifacts under local `pact/runs/<run-id>/` and paths in the published reports.

## Noon Curveball: what changed and how we adapted

**Track 2 — Graph is evidence, not an oracle.** The user supplied the official screenshots and confirmed they are the complete supplied information. Track 2 permits any suitably complex open-source repository; we retained the existing Entire Graph fork. The dynamic-dispatch fixture is transparently team-authored.

The fresh actual session recovered the original setup Checkpoint and handoff before edits. Before-edit Graph impact identified selector/adapter consumers including review, recovery and benchmarking. The invalidated assumption was that parser success and internal endpoints justified excluding unselected policies. The initial dynamic-dispatch test demonstrated the defect: zero detected failures despite two guest-export regressions.

PACT now qualifies bound structural versus heuristic evidence, retains diagnostic origin/file/line/version, reconciles semantic changes with Git's file inventory, and checks bounded Python runtime-lookup patterns. Potentially incomplete Graph selection falls back to every confirmed registered assertion. The fallback adds R1 and R3 for the unchanged dynamic export caller without inventing an R1 path. D1 finds two regressions; D2 passes ten candidate assertions while retaining incomplete analysis.

Selection quality and source gaps travel in hashed replay/cloud bundles. New Delta receipts preserve them independently of execution completion; the client rejects changed or missing receipt context. Legacy reports remain immutable and are labelled as unassessed rather than upgraded.

Observed verification: **22 tests passed in 13.08 seconds**, including the original H1/H2/H3/H4 cases and seven Curveball tests. The real D1→D2 browser flow had no JavaScript errors. Before-edit evidence and design: [CURVEBALL.md](pact/docs/CURVEBALL.md). Local report: [D1](pact/docs/evidence/d1-local-report.json), [D2](pact/docs/evidence/d2-local-report.json). [Recorded live walkthrough](pact/docs/evidence/curveball-live-walkthrough.gif) is genuine sampled browser footage, not generated UI.

A new measured D1 comparison reports 2 candidate scenarios/0 failures for changed-file, 10/2 for Graph with fallback, 10/2 for all-registered, and 24/2 for the separate full-matrix reference. [Full measurement](pact/docs/evidence/curveball-local-comparison.json). No speed advantage or universal coverage is claimed.

All five post-Curveball Databricks jobs completed successfully: changed-file, Graph fallback, all registered, independent full matrix, and corrected D2. Every cloud assertion matches its local counterpart; evidence context and idempotent replay are verified. D1 finds two regressions through fallback; D2 passes all ten candidate assertions. D2 evidence context was recovered from Delta history. See [cloud verification](pact/docs/evidence/curveball-databricks-verification.json). Earlier H1/H2 runs remain separately identified.

## Checkpoint links and what each checkpoint proves

| Evidence | Actual state |
| --- | --- |
| Initial setup | `7c02a621a7d5`, manually attached from the authentic Codex session; `entire checkpoint explain 7c02a621a7d5` |
| Four-policy approval | Explicit user approval recorded locally; original approval excerpt has not yet been captured into a readable Checkpoint |
| Pre-noon stable state | Pending a distinct authentic Checkpoint; the repeated setup ID does not count as a new milestone |
| Fresh-session recovery | `f899811e4ff1`, supported manual attachment of actual new session; creation `2026-09-06T06:39:31.244113Z`, trailer in recovery commit `e9e3634` |
| Implemented adaptation | `90d4a736f01e`, supported manual attachment of actual fresh final-verification session `01a075de-fbc5-7b60-b38c-b14cb13816a0`; reviews adaptation and five completed Databricks runs at `e3b512e`. This is authentic later verification/reconstruction, not retroactive capture of the original implementation session. |
| Final verification | Same final-review Checkpoint `90d4a736f01e`; this is one unique Checkpoint covering both review topics, not two separate milestones |

Initial source locator: `entire://checkpoint/7c02a621a7d5?session=0`. This is an internal evidence locator, not a verified public judge-access URL. Public/organiser-accessible Checkpoint links must be filled in and opened before submission.

The installed CLI reuses LastCheckpointID on repeated attachment of the same session. The fresh noon transcript produced a new genuine recovery Checkpoint through supported manual attachment. Native capture remains unverified (seven hooks require approval); fresh final-review Checkpoint `90d4a736f01e` is now readable through supported manual attachment. No IDs, hooks, timestamps or transcripts were fabricated, and no published commits were amended.

## Setup, run and test instructions

Use the active genuine fork/mirror below. Current implementation branch is `pact/implementation`. Preserve fixture tags `pact-B0`, `pact-H1`, `pact-H2`, `pact-H3`, `pact-H4`.

```sh
entire repo clone entire://aws-ap-south-1.entire.io/gh/Shaurya002800/entire-graph PACT-fork
cd PACT-fork
git switch pact/implementation
python3 -m venv .venv
.venv/bin/python -m pip install './pact[test]'
entire plugin install graph
entire graph version
.venv/bin/btw-pact serve --port 8765
```

Open http://127.0.0.1:8765. Inspect and confirm the four proposals, select H1 and run a local review. Inspect the unchanged export caller, then use **Review corrected H2**. Requirement revisions are append-only; approving an explicitly head-only policy revision retains the prior policy for baseline evaluation.

The UI binds only to localhost and is a single-team development workbench, not a public hosted service. The package supports Python 3.11+; local verification used 3.12.12, and the observed Databricks runtime used Python 3.11. Authentication stays in the official CLI credential store.

```sh
.venv/bin/python -m pytest -q -c pact/pyproject.toml pact/tests
.venv/bin/btw-pact review --request pact/demo/request-h1.json
.venv/bin/btw-pact benchmark --request pact/demo/request-h1.json
.venv/bin/btw-pact sources --repo . --commit pact-B0
.venv/bin/btw-pact reproduce --bundle pact/docs/evidence/h1-reproducer.json
```

The example requests preserve the four policies actually confirmed by Shaurya; they have explicit source gaps. Review exits `2` for partial evidence, `1` for a complete review with failed candidate assertions, and `0` for a complete passing review. The standalone reproducer reports execution only: H1 exits `1`, H2 exits `0`, inconclusive execution exits `2`. It does not certify Checkpoint provenance.

**Observed validation:** 15 focused tests passed in the final 8.39-second suite; package build/install succeeded; installed CLI replay from a clean temporary directory reproduced H1's two failures and H2's zero failures. Only PACT code changed; unrelated upstream Go suites were not rerun.

## Databricks use, data sources and limitations

Final workspace: [PACT Databricks workspace](https://dbc-c3d496ed-7dbd.cloud.databricks.com/). Profile `PACT`, catalog/schema `workspace.pact`. The application uses serverless Jobs, workspace artifacts, Delta tables and SQL history queries.

```sh
databricks auth login --host https://dbc-c3d496ed-7dbd.cloud.databricks.com --profile PACT
```

Then select **Databricks** in the UI. Optional environment variables: `PACT_DATABRICKS_PROFILE`, `PACT_DATABRICKS_SCHEMA` (`catalog.schema` only), `PACT_DATABRICKS_WAREHOUSE`, and `PACT_PENDING_DIR`. No token belongs in a request, screenshot, Checkpoint or Git file.

The five tables are `pact_scenarios`, `pact_requirement_revisions`, `pact_runs`, `pact_observations`, and `pact_assertion_results`. They preserve immutable JSON payloads with queryable identities, roles, sides and verdicts. Complete runs are replayed from saved receipts; conflicting identities are rejected. No full private transcript excerpts are uploaded; source hashes and locators are retained.

| Real execution | Remote run | Result |
| --- | --- | --- |
| H1 | [761800479613647](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/775465677544334/run/761800479613647) | 18 observations, 20 assertion rows, 2 grouped guest-export violations |
| H2 | [130512173112459](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/236751144068058/run/130512173112459) | 18 observations, 20 assertion rows, 0 failures; completed-receipt replay verified |

Both contain 8 baseline and 10 candidate observations. The 20 assertion rows include two baseline `not_applicable` rows for the new feature. PACT validates result identities/cardinality and recomputes remote assertions with its local evaluator. Browser fallback screenshots: [H1](pact/docs/evidence/h1-review.png), [H2](pact/docs/evidence/h2-review.png). Saved evidence: [H1 report](pact/docs/evidence/h1-databricks-report.json), [H2 report](pact/docs/evidence/h2-databricks-report.json).

Remote history was actually recovered from Delta with SQL statement `01f1a9b4-c3a9-1b77-8e3a-89cf4610a087`. The UI's **Recover Databricks history** uses this remote ledger independently of SQLite. Removing Databricks removes durable remote execution history, grouped SQL analysis and the remote execution path; local reproduction remains available.

An initial notebook failed because an older `typing_extensions` module was already loaded. The targeted correction was the documented Python restart after `%pip install`; subsequent H1/H2 jobs succeeded. Source: [Databricks notebook-scoped libraries](https://docs.databricks.com/aws/en/libraries/notebooks-python-libraries).

Remote startup is not presented as instant. The client waits up to 180 seconds, preserving the remote ID in `pact/runs/pending/` if the job continues. Inspect that run before resubmitting. Free Edition quotas apply; saved results are clearly recorded evidence, never relabelled as a fresh cloud execution. Run `.venv/bin/btw-pact recover --run-id <PACT_RUN_ID>` to finish a preserved pending request. Recovery verifies the original input/runtime hashes and reuses its remote job. The completed H2 remote job was successfully recovered without a new submission. Requests created before this recovery feature may have only a job receipt, without the full recoverable request.

## Repository lineage and event-day work

The initial repository was created at **09:24:48 IST on 6 September 2026**, after kickoff, but GitHub's API reports `fork: false`. It was populated with upstream Entire Graph history rather than created with GitHub's Fork operation. That does **not** satisfy the guide's literal fork instruction without organiser acceptance.

The user created the genuine fork `Shaurya002800/entire-graph`. It has now been cloned through its India mirror into `PACT-fork`; existing commits, seven milestone/fixture tags, authentic Checkpoint branch and runtime evidence were transferred without rewriting history. Do not delete the current repository or force-push. The current India mirror clone was created using `entire repo clone` before application code was generated there.

Upstream boundary: `3a2a715fad1948e83dc7ebe0d307377ba29e065a`. Earlier commits belong to Entire Graph, not this team's event-day implementation. PACT setup starts at `3884262` (10:19 IST); the baseline is `d2ade70`, H1 `67ee0cf`, H2 `6ba0eca`, H3 `b3e7497`, H4 `f438c37`. Fixture identities are distinct from the final implementation SHA. Record the final resulting SHA in the submission form after the last commit; do not create an endless self-referential documentation commit.

## Known limitations and next steps

- Destination fork lineage is corrected; disclose the earlier workflow deviation and provide accessible Checkpoint links before claiming event compliance.
- Capture/link the real policy approval and the remaining milestone contexts; current reports intentionally retain partial provenance.
- Curveball implementation, local preservation tests and all five post-Curveball cloud checks are verified; final review context is captured in `90d4a736f01e`.
- Finish final semantic review, judge-access check and submission receipt before **15:00 IST**. Two local D1→D2 walkthroughs were performed; the second was genuinely recorded.
- This is a bounded trusted fixture, not hostile-code isolation, broad-language support, autonomous repair, formal verification or a guarantee that code is safe.

Submission owner, demo owner and Databricks deployment owner must be confirmed by the team. No submission has been made.


## Full frozen-scope status after noon

| Capability | Actual evidence/state |
| --- | --- |
| F01 Checkpoint ingestion | Real setup source ingestion works; recovery Checkpoint is readable. Original policy source association remains partial. |
| F02 Human confirmation | Immutable confirmed policies/revisions and live UI; policies were not changed for the Curveball. |
| F03 Graph investigation | Versioned snapshots, original structural paths, and saved before-edit impact. Final submitted-SHA review pending. |
| F04 Selection | Resolved unchanged callers preserved; dynamic-dispatch fallback demonstrated without invented edges. |
| F05 Local execution | Pinned subprocess execution, 22 passing tests, D1/D2 reports and portable bundles. |
| F06 Databricks | Original H1/H2 and five post-Curveball jobs verified, with local parity, preserved evidence, idempotent replay and Delta history. |
| F07 Verdicts | Baseline/candidate, intentional revisions, execution errors and incomplete analysis remain separate. |
| F08 Evidence/replay | New bundles retain evidence quality and source gaps; legacy evidence is not upgraded. |
| F09 UI | Real D1→D2 flow verified, diagnostics and correction visible, zero JS errors. |
| F10 Comparison | Local and real Databricks strategy comparisons plus independent full matrix verified; identical assertions. |
| F11 Authentic history | Setup and manually attached fresh recovery available; final review context is captured; missing original/pre-noon capture remains disclosed. |
| F12 Delivery | Setup/architecture/limitations/demo docs and genuine backup recording published; final cloud/capture evidence included in this milestone. Judge access and submission receipt remain. |

Following explicit user authorization, implementation through `21aabb9c3d5262bfa46b32fdf03ef95d0e940ce6`, D0/D1/D2 tags and the existing authentic Checkpoint branch were pushed to the India mirror and remotely verified. No force-push or history rewrite was used. See [setup](pact/docs/SETUP.md), [architecture](pact/docs/ARCHITECTURE.md), and [limitations](pact/docs/LIMITATIONS.md).
