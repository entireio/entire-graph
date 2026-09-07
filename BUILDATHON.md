# PACT for Entire
## Evidence-backed regression review when the dependency graph is incomplete

**Track E2 - Build with Graph Intelligence | Bengaluru Tech Week Buildathon | 6 September 2026**

**Project contact:** Shaurya ([Shaurya002800](https://github.com/Shaurya002800))

**Implementation:** [pact/implementation](https://github.com/Shaurya002800/entire-graph/tree/pact/implementation) in the genuine Entire Graph fork. PACT's event-created product lives under `pact/`; upstream Graph code is credited separately.

> A feature can work while an unchanged caller breaks. PACT turns human-confirmed intent into executable checks, uses Entire Graph to explain structural impact, and broadens verification when that evidence is incomplete.

### The result to inspect first

In our deliberately seeded permission regression, changed-file selection executes two candidate scenarios and detects **zero** failures. PACT's Graph strategy, with its Curveball fallback, executes ten and detects **both** guest-export regressions. The corrected version passes all ten. These results agree locally and in **five actual Databricks jobs**; receipts preserve the original analysis limitations.

**22 tests passed. Five cloud jobs succeeded. A clean installed replay reproduced failure and correction without the original checkout or cloud credentials.** These are measured results on a team-authored 24-case fixture, not production accuracy or performance claims.

Start with the [genuine recorded walkthrough](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-live-walkthrough.gif), then inspect the [cloud verification manifest](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-databricks-verification.json), [D1 failure report](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/d1-graph-databricks-report.json), and [D2 corrected report](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/d2-graph-databricks-report.json). Public report copies retain the results and evidence hashes while omitting personal environment paths.

Upload-ready supporting material: [six-slide pitch deck](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/submission/PACT-Pitch.pdf), [short silent MP4 recording](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/submission/PACT-Demo.mp4), and [three-minute presenter script](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/submission/DEMO-SCRIPT.md).

## 1. The problem and the working product

PACT is for a developer or reviewer evaluating a change against previously agreed behavior. A file diff alone cannot tell them which unchanged entrypoint depends on a changed helper. A static path alone cannot prove runtime behavior. A passing run alone cannot establish that the right checks were selected.

PACT connects these pieces in one review: confirmed policy, immutable baseline/candidate commits, qualified Graph evidence, observed behavior, classification, and a portable reproducer. A reviewer can inspect the claim, run it, correct the implementation and compare the evidence. The product distinguishes regression, pre-existing violation, an explicitly approved policy revision, execution error and partial analysis.

The visible workflow is implemented in a local FastAPI workbench:

1. Inspect and confirm four bounded permission requirements; revisions are append-only.
2. Choose pinned baseline/candidate versions and local or Databricks execution.
3. Inspect selected checks, structural paths, missing-source warnings and fallback reasons.
4. Review the two D1 guest-export failures; use the correction flow to run D2.
5. Compare strategies, recover saved Databricks history, or download a sealed replay bundle.

The correction flow selects a prepared corrected commit. It does not pretend to autonomously repair arbitrary software. The interface binds to localhost as a trusted single-team workbench.

## 2. What Entire Graph contributes

The original resolved-call example changes `can_access` while leaving `export_document` unchanged. Version-specific `CALLS` traversal connects the helper to registered public entrypoints, preserving file/line locations, resolution, confidence and snapshot hashes. Base and head paths are never mixed.

The Curveball makes the limitation concrete: the unchanged export caller uses dynamic lookup, so the necessary runtime relation can be absent despite parser success. PACT retains valid structural evidence but no longer treats an absent edge as proof that a policy is unaffected. Source precautions are labelled separately; they are never fabricated Graph edges.

Entire is used both **inside the product** (semantic diff, two snapshots, requirement selection and source ingestion) and **during development** (before-edit search/impact, recovery, final review). Before-edit [selector impact](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-impact-selector.json) and [adapter impact](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-impact-adapter.json) identified review/recovery/benchmark consumers; focused source inspection also traced replay, UI and cloud receipts.

The repository is a genuine fork of `entireio/entire-graph`, with its multi-language parser and semantic-analysis infrastructure. Our evaluation is intentionally bounded to the registered Python fixture. We do not claim we evaluated every upstream language or all of the repository's behavior.

## 3. Measured comparison

All rows below refer to D0 -> D1 and the same confirmed policies. Candidate observations count executed fixture scenarios; failed assertions count policy violations. The full matrix executes all 24 cases, including cases without applicable assertions.

| Strategy | Candidate observations | Failed assertions | Local/cloud parity |
| --- | ---: | ---: | --- |
| Changed entrypoint file | 2 | 0 - misses both seeded regressions | Exact match |
| Graph with conservative fallback | 10 | 2 | Exact match |
| All confirmed registered requirements | 10 | 2 | Exact match |
| Independent full-matrix reference | 24 | 2 | Exact match |
| Corrected D2, Graph with fallback | 10 | 0; all ten pass | Exact match |

This demonstrates detection beyond changed-file selection. It does **not** establish a speed advantage, fewer checks than all-registered selection, or universal program coverage. [Local measurements](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-local-comparison.json) and [cloud identities, counts and hashes](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-databricks-verification.json) make the comparison inspectable.

## 4. Noon Curveball: Graph is evidence, not an oracle

The supplied constraint required qualified incomplete relationships, visible partial analysis, a safe verification path, preservation of resolved behavior and an incomplete-analysis fixture. The user confirmed that no separate organiser fixture was available; D0/D1/D2 are explicitly **team-authored**.

**Failure reproduced before the fix:** the real-Graph dynamic-dispatch test expected two guest-export regressions but observed zero. With Graph unavailable, the old selector retained only R4 instead of all four policies.

**Implemented adaptation:**

- Qualify structurally confirmed and heuristic paths; retain diagnostic origin, location and commit side.
- Reconcile semantic-diff coverage with Git's changed-file inventory.
- Detect bounded Python runtime-lookup patterns and uncertain bindings as precautions.
- Fall back to all confirmed registered assertions when narrow selection is not justified. D1 adds R1/R3 without inventing an R1 call path.
- Preserve the complete selection context and source gaps in hashed replay bundles, Databricks receipts, Delta history and the UI. Reject missing or altered receipt context; label legacy evidence as unassessed.

D2's passing execution still displays partial analysis. Execution success does not erase missing Graph relationships or original intent-source gaps. The original H1/H2/H3/H4 behavior and policy-revision semantics remain covered by the test suite. [Full adaptation record](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/CURVEBALL.md).

## 5. Implementation and Databricks

`confirmed requirements -> immutable Git versions -> Graph evidence/selection -> local or Databricks runner -> shared evaluator -> evidence report + portable replay`

The Python package `btw_pact` contains validated Pydantic contracts, an append-only SQLite registry, real Checkpoint transcript ingestion, Graph analysis, a shared assertion evaluator, a subprocess runner, Databricks Jobs/Delta integration, integrity-checked replay, CLI and web UI. [Architecture](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/ARCHITECTURE.md) and [source](https://github.com/Shaurya002800/entire-graph/tree/pact/implementation/pact/src/btw_pact).

**Databricks is part of the execution and evidence workflow.** The backend uploads a content-hashed fixture/runtime, runs a serverless notebook, writes five Delta tables, reads results back and validates identity, cardinality, assertions and evidence hashes. Tables preserve scenarios, requirement revisions, runs, observations and assertion results. Saved remote history can be recovered independently of local SQLite. Idempotent receipt replay is verified.

Five fresh post-Curveball jobs completed on 6 September 2026:

| Workload | Real Databricks run | Candidate result |
| --- | --- | --- |
| D1 changed-file | [962538927251526](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/679652746926491/run/962538927251526) | 2 pass / 0 fail; 2 observations |
| D1 Graph fallback | [380869402049467](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/111082340501413/run/380869402049467) | 8 pass / 2 fail; 10 observations |
| D1 all registered | [591291185596050](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/814172048197535/run/591291185596050) | 8 pass / 2 fail; 10 observations |
| D1 full matrix | [357743749677995](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/120477170423413/run/357743749677995) | 8 pass / 2 fail; 24 observations |
| D2 correction | [758723055621736](https://dbc-c3d496ed-7dbd.cloud.databricks.com/?o=7474653723260152#job/693356911604309/run/758723055621736) | 10 pass / 0 fail; 10 observations |

The existing Delta statement `01f1a9ce-b798-1a0a-90c5-4d7ae2b7a3df` returned matching evidence and assertions. A separate fresh review directly checked all five job statuses as `SUCCESS`. Workspace links require Databricks permission; **public report copies and offline replay are available without workspace access**. No credentials or full private intent transcripts are uploaded with the cloud fixture.

**Data provenance:** 24 synthetic permission cases across role, operation, workspace relationship and visibility, written by the team. Four policies were explicitly confirmed by Shaurya: deny guest export; deny members another workspace's private resources; allow own-workspace admin export; allow the new guest public-preview feature on the candidate. Only registered, approved assertions determine verdicts. No external production dataset is claimed.

## 6. Checkpoints, recovery and honest provenance

Three unique authentic Checkpoints are available on the pushed `entire/checkpoints/v1` branch. Each was created through supported manual attachment of a real Codex session.

| Checkpoint | What its stored context actually establishes |
| --- | --- |
| `7c02a621a7d5` | Initial setup/research; not the later four-policy approval |
| `f899811e4ff1` | Fresh-session recovery before Curveball implementation, created 06:39:31 UTC |
| `90d4a736f01e` | Later independent adaptation/final verification review at `e3b512e`, created 08:44:54 UTC; one Checkpoint covers both review topics |

Read remotely with `entire checkpoint explain 90d4a736f01e --repo Shaurya002800/entire-graph --json`, or inspect the [published Checkpoint branch](https://github.com/Shaurya002800/entire-graph/tree/entire/checkpoints/v1). [Final capture manifest](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-final-checkpoint.json) records the actual session, reviewed SHA and stored-transcript hash. Remote identity and transcript content were verified.

**Disclosed gaps:** the original policy-approval and pre-noon milestone were not captured in distinct authentic Checkpoints. Seven native hooks awaited approval, so manual capture is not represented as continuous native recording. Later review cannot retroactively repair those historical gaps. Reports retain partial source provenance; unrelated setup excerpts are not linked to policies. The initial standalone repository was later migrated to the genuine fork/India mirror; the earlier workflow deviation is disclosed and organiser acceptance is not claimed.

## 7. Reproduce and demonstrate

The reviewer can inspect public evidence immediately or run the product locally. Python 3.11+ is supported; the verified clean installation used Python 3.12.12 and package 0.2.0.

```sh
entire repo clone entire://aws-ap-south-1.entire.io/gh/Shaurya002800/entire-graph PACT-fork
cd PACT-fork
git switch pact/implementation
python3 -m venv .venv
.venv/bin/python -m pip install './pact[test]'
entire plugin install graph
.venv/bin/btw-pact serve --port 8765
```

Open `http://127.0.0.1:8765`. For a short demonstration: inspect confirmed policies, select D1, explain the visible fallback and two guest-export failures, run corrected D2, then show the preserved partial-analysis label and saved Databricks parity.

```sh
.venv/bin/python -m pytest -q -c pact/pyproject.toml pact/tests
.venv/bin/btw-pact reproduce --bundle pact/docs/evidence/d1-reproducer.json
.venv/bin/btw-pact reproduce --bundle pact/docs/evidence/d2-reproducer.json
```

D1 replay deliberately exits **1** with two failures; D2 exits **0** with none. Review evidence remains partial in both. The standalone replay needs neither the original checkout nor cloud authentication after installation. A clean isolated wheel installation and these replays were actually verified. [Clean-install record](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-clean-install.json), [test record](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/curveball-verification.json), [demo script](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/DEMO.md), [setup](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/SETUP.md).

For fresh cloud execution, authenticate the official Databricks CLI with profile `PACT` to the [workspace](https://dbc-c3d496ed-7dbd.cloud.databricks.com/), then select Databricks in the UI. Startup/quota delays are explicit; a pending receipt preserves the existing remote run for recovery instead of silently resubmitting.

## 8. Scope, verification and continuation

The original full plan included intent ingestion/confirmation, Graph investigation/selection, local and remote execution, classifications, replay, UI, comparison and delivery. All product paths are implemented; evidence gaps are separate from execution completeness. [Frozen implementation plan](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/PACT_IMPLEMENTATION_GUIDE.md) and [handoff](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/HANDOFF.md) preserve the decisions.

Verification includes 22 passing Python tests, seven Curveball cases, actual D1-to-D2 browser operation without JavaScript errors, clean installed replay, five cloud jobs with exact local parity, receipt tamper checks, idempotence and Delta history recovery. Earlier pre-Curveball H1/H2 cloud runs remain separately labelled. [Final semantic review](https://github.com/Shaurya002800/entire-graph/blob/pact/implementation/pact/docs/evidence/submission-semantic-review.json) records the exact reviewed commit and preserves upstream generated-parser size/parse warnings; static dependent counts remain partial. Documentation-only packaging changes do not change the verified product implementation `fed20634613006b1661996de29e07d457b139fcf`.

**Prior work and attribution:** Entire Graph and its upstream history are not our event-day work. Upstream boundary is `3a2a715fad1948e83dc7ebe0d307377ba29e065a`; event product setup begins at `3884262`. The pre-event implementation guide is planning material. Open-source dependencies include FastAPI, Pydantic, the Databricks SDK and Entire tooling. PACT for Entire is independent of Pact Foundation's contract-testing product.

**Limits:** trusted synthetic fixture, registered-policy coverage, conservative static/source diagnostics, no arbitrary hostile-code isolation, no general autonomous repair and no guarantee of production safety. Databricks workspace access is permissioned; the public evidence package provides an inspectable fallback. Historical capture gaps remain disclosed.

**Next engineering steps:** expand scenario/entrypoint adapters, evaluate larger real applications with labelled expected behavior, strengthen dynamic-language diagnostics and automate reliable capture setup. These are future work, not implemented claims.

PACT's demonstrated contribution is a review workflow that connects intent, structural evidence and executed behavior while keeping uncertainty visible through correction, replay and cloud history.
