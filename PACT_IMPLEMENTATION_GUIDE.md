# PACT Implementation and Event Execution Guide

**Version:** 1.1 — full scope retained; schedule corrected to the organiser's participant guide received on 6 September 2026.  
**Event:** BTW Buildathon 2026, Bengaluru.  
**Selected track:** E2 — Build with Graph Intelligence, confirmed by the team.  
**Deliverable:** The complete PACT project, including Databricks integration and a polished demonstration.  
**Audience:** The team, an implementing AI with no previous conversation context, and the fresh AI session used at noon.  
**Status:** Approved direction and execution specification; implementation and environment feasibility are not yet verified. This document does not claim a working product exists.  
**Time zone:** Asia/Kolkata, IST, UTC+05:30. Every time below refers to Sunday 6 September 2026.

## Read this first

Build PACT, a developer tool that connects a confirmed requirement from an Entire Checkpoint to code affected by a change, selects relevant checks using Entire Graph, executes them, and produces reproducible evidence of violations. The first supported domain is permission behaviour in a small Python application. Databricks executes a versioned scenario workload and supplies real comparison results to the product. A local runner provides the same evaluation semantics and supports development and reproducibility.

The user explicitly requested the **complete project**, not the smaller local-only version discussed earlier. This guide supersedes earlier brainstorming, the teammate's outdated Track 1 selection, and suggestions to omit Databricks. All capability rows in Section 3 are required for full completion. If a dependency fails, keep the scope visible, resolve the dependency, and report unfinished work honestly. Do not silently redefine success.

Research, planning, and generic tool practice may happen before the event. Do not implement competition-specific source code, generate a feature branch, or manufacture development evidence before the official start. This guide is planning material. Runtime setup commands, implementation tasks, UI designs, and sample behaviours described below are to be enacted within the authorised competition window.

**Execution reality:** Six hours is the official delivery window, including setup and submission. This schedule assumes three parallel implementation lanes with AI assistance and one coordinating human who may also own a lane. One sequential worker has a materially higher delivery risk. This plan is not a promise of a prize or of guaranteed completion.

## Contents

1. [Event brief and obligations](#1-event-brief-and-obligations)
2. [Product brief and competitive position](#2-product-brief-and-competitive-position)
3. [Frozen scope and completion contract](#3-frozen-scope-and-completion-contract)
4. [Demonstration application and requirement history](#4-demonstration-application-and-requirement-history)
5. [Architecture and repository layout](#5-architecture-and-repository-layout)
6. [Shared data contracts](#6-shared-data-contracts)
7. [Requirement and evidence rules](#7-requirement-and-evidence-rules)
8. [Entire integration and Graph selection](#8-entire-integration-and-graph-selection)
9. [Execution and Databricks implementation](#9-execution-and-databricks-implementation)
10. [Product interface and polished demo](#10-product-interface-and-polished-demo)
11. [Work allocation and timeline](#11-work-allocation-and-timeline)
12. [Implementation work packages](#12-implementation-work-packages)
13. [Checkpoint and noon handoff protocol](#13-checkpoint-and-noon-handoff-protocol)
14. [Acceptance tests and evaluation](#14-acceptance-tests-and-evaluation)
15. [Recovery decisions and scope control](#15-recovery-decisions-and-scope-control)
16. [Judging presentation and submission](#16-judging-presentation-and-submission)
17. [Instructions to the implementing AI](#17-instructions-to-the-implementing-ai)
18. [Completion and source register](#18-completion-and-source-register)

## 1 Event brief and obligations

The event is at Scaler School of Technology, Electronic City, Bengaluru. Arrival/check-in begins at 08:00. Building starts at 09:00; code freeze and submission are at 15:00; demonstrations and judging run until 17:00.

At 12:00, a mandatory track-specific constraint is revealed. Preserve a stable commit with a valid Checkpoint beforehand. Close the current coding-agent session, start a fresh one, recover context through Checkpoints, use Graph before editing, and adapt the actual project.

Implementation must start in a new fork of the selected Entire repository. Mirror it on Entire using the India region and work from the mirror clone. Enable Checkpoints before agent-assisted implementation. Use Graph during development and for the final semantic-diff review. Deliver working behaviour, tests, reproducible setup, and disclosed limitations.

The main rubric is: problem/innovation 20; implementation 25; curveball 15; Checkpoints 15; Graph 15; demonstration/future potential 10.

Databricks is an optional event award but a required feature of this frozen project. Its rubric is: meaningful use 30; reliability 25; user value 20; data provenance 15; curveball 10. It is scored separately; the same project may win both awards.

Source: [official event brief and rules](https://build.bengalurutechweek.com/). Check private participation rules for eligibility, team size, submission access, and judge-access arrangements; these were not accessible in the signed-out research session. Do not infer private details from this document. Confirm any newly announced official changes before acting.

## 2 Product brief and competitive position

### User and problem

The first user is a developer reviewing an AI-generated permission change before accepting it. The latest feature can work while an older access rule is accidentally violated elsewhere. A normal diff shows changed lines; PACT identifies relevant preserved requirements, explains why they are affected, and supplies an executable counterexample when one is found.

**Product statement:** PACT uses Entire Graph to investigate whether a code change violates a confirmed requirement from Entire Checkpoints, then produces executable evidence.

**Opening pitch:** “Your AI agent added the feature. PACT checks which earlier promises it may have broken.”

### What is distinctive and what is already available

Entire already offers intent-aware review. Entire Graph already supplies code relationships, impact analysis, semantic change information, and a command for running a supplied verification command. PACT must reuse these capabilities and contribute the missing application workflow: confirmed requirement state, requirement-to-entrypoint registration, graph-based selection, execution across versions, evidence classification, and reproducibility.

GraphDev's GitLab award supports the relevance of change-impact workflows; VeriGraph's HackwithBay result supports presenting executable evidence. These are design precedents, not proof that PACT will win. Do not repeat “nobody has this” or “the first such tool.”

An established product named Pact already performs integration contract testing. Keep **PACT** as the team's project name, use the qualified display name **PACT for Entire**, and package our implementation as **btw_pact**. Do not imply affiliation with Pact Foundation or publish under its package names.

### Pilot boundaries

The complete first release supports one registered Python demonstration application, real commit comparisons, confirmed permission assertions, a local runner, a Databricks runner, and a reusable review workflow. The pilot is intentionally limited to deterministic functions with JSON input/output. Broader languages, arbitrary customer repositories, autonomous fixes, automatic merging, and production deployment are future work, not promised components of this frozen release.

Permission errors are the first domain. The core registry and evidence format should not depend on permission-specific function names. A later domain can add approved assertion templates and a new fixture adapter without replacing the graph selector.

## 3 Frozen scope and completion contract

| ID | Required capability | Evidence required for completion |
| --- | --- | --- |
| F01 | Real Checkpoint ingestion | The product reads an actual event-created Checkpoint, displays its source excerpt and links it to the correct commit/session |
| F02 | Assisted requirement preparation and human confirmation | Suggestions retain provenance; a developer can confirm, amend, supersede, or leave a requirement unresolved |
| F03 | Real Graph investigation | Changed symbols and relevant dependency paths come from Entire output for identified versions |
| F04 | Requirement-based check selection | A change in a shared helper selects a registered requirement on an unchanged caller, with a source-linked explanation |
| F05 | Local execution | Approved assertions execute reproducibly against immutable baseline and candidate snapshots |
| F06 | Databricks execution and comparison | A real job executes the same registered workload, stores versioned data/results, and returns results consumed by the interface |
| F07 | Correct verdicts | Violations, checked cases, unresolved evidence, intentional policy changes, pre-existing violations, and execution failures are distinguished |
| F08 | Evidence and reproduction | Each confirmed violation includes source intent, path, scenario, expected/actual values, version identifiers, and a working reproducer |
| F09 | Interactive product experience | Commit selection, requirement confirmation, execution status, result filtering, evidence inspection, and rerun after a fix work |
| F10 | Comparative evaluation | Changed-file, Graph-selected, and all-registered-check strategies are compared on identical inputs with measured outcomes |
| F11 | Authentic development history | Initial plan, baseline, pre-noon state, fresh-session adaptation, and final verification have real Checkpoint evidence |
| F12 | Delivery | A fresh teammate setup succeeds; live demo, recorded backup, BUILDATHON.md, source links, and submission fields are complete |

F01–F12 form the definition of the complete PACT project. Temporary stubs may unblock parallel development, but must be visibly marked, excluded from final evidence, and replaced before a capability is marked done. A local run cannot satisfy F06. A screenshot cannot substitute for an executable path.

### Priority without scope reduction

Implement the dependency-critical workflow first, then complete all other rows. If delayed, reduce redundant animation, custom branding, repeated rework, or extra examples beyond the agreed acceptance suite. Do not remove a frozen capability and call the project complete. At the event freeze, submit the working state with explicit unfinished items if necessary; never continue implementation past the deadline to preserve a completion claim.

## 4 Demonstration application and requirement history

### Application shape

Create an event-owned fixture package with plain Python functions:

- `permissions.can_access(request)` makes the shared access decision.
- `preview.preview_document(request)` calls the permission helper for preview.
- `export.export_document(request)` calls the same helper for export.
- `app.dispatch(request)` dispatches the operation and returns a normalised decision.

Imports are explicit. Avoid reflection and framework routing in the fixture so Graph can inspect the relevant call relationships. The fixture is a test target; the tool implementation remains in the designated Entire Graph fork.

### Scenario definition

Every request has:

| Field | Allowed values |
| --- | --- |
| `role` | `guest`, `member`, `admin` |
| `operation` | `preview`, `export` |
| `same_workspace` | `true`, `false` |
| `visibility` | `public`, `private` |

The complete scenario matrix has **24 requests**: 3 × 2 × 2 × 2. These are synthetic evaluation cases, not user records. Do not inflate the matrix to claim scale. Normalised output includes `allowed: boolean`; explanatory text is displayed but is not the assertion oracle.

### Baseline behaviour

For the initial fixture, guests are denied both operations. Members and admins are allowed within their own workspace and denied across workspaces. Preserve this deterministic baseline and its authentic Checkpoint before implementing the feature patch.

### Confirmed requirements

| ID | Status and applicability | Independent assertion | Registered entrypoint |
| --- | --- | --- | --- |
| R1 | Active on baseline and candidate | Any guest export is denied, regardless of ownership or visibility | `export.export_document` |
| R2 | Active on baseline and candidate | A member's operation on another workspace's private resource is denied | Both public operations |
| R3 | Active on baseline and candidate | An admin export inside its own workspace succeeds | `export.export_document` |
| R4 | New feature; candidate only | A guest preview of public content succeeds, including another workspace's public content | `preview.preview_document` |

R1 applies to 4 scenarios, R2 to 2, R3 to 2, and R4 to 2. The primary candidate has **10 applicable assertion-scenario pairs**. The original baseline has **8** because R4 is not yet applicable. Executing every scenario does not mean every result has an approved requirement oracle; report both counts.

### Version sequence

1. **B0 baseline:** old behaviour and R1–R3 captured and confirmed.
2. **H1 faulty feature:** add guest public preview by broadening the shared permission rule to allow guests on any public resource. This intentionally also permits guest public exports and violates R1. This is a disclosed seeded regression, not an independently discovered production bug.
3. **H2 corrected feature:** restrict the exception to `guest AND public AND preview`; preserve all previous rules. R1–R4 pass for the defined matrix.
4. **H3 harmless refactor:** change internal organisation or names while preserving H2 behaviour; all applicable requirements remain satisfied. Ensure renamed symbol mappings are resolved or marked unresolved.
5. **H4 intentional policy revision:** in a separate comparison case, the user explicitly permits guest export of public resources. R1 is superseded by R1b, which denies guest exports only for private resources. The product must not enforce obsolete R1 against H4. Preserve the original history and revision provenance.

Give versions real commit identifiers created during the event. Record the resolved SHAs in the run manifest rather than inventing identifiers in this plan. The B0/H1/H2 labels are semantic labels, not fake commit SHAs.

### Primary demo success condition

The H1 change to the shared helper produces a Graph path to the unchanged export entrypoint. PACT selects R1, executes an applicable scenario, and shows `expected allowed=false` versus `actual allowed=true`. The same scenario passes on B0 and H2. H2 also satisfies R4, demonstrating that the repair preserves the new feature.

## 5 Architecture and repository layout

### Data flow

```text
Immutable commits + actual Checkpoint records
                  |
        Checkpoint and Graph adapters
                  |
Confirmed requirement registry + source-linked impact paths
                  |
         SelectionPlan with explicit gaps
                  |
       Approved fixture execution package
          /                         \
 Local subprocess runner       Databricks job runner
          \                         /
         Normalised observations and assertions
                  |
      Verdict classification and comparison
                  |
 Interactive evidence report + reproducer + run history
```

### Stack

- Python **3.11 or 3.12**, choosing one that works both locally and in the available Databricks environment; pin the minor version after the first successful round trip.
- Entire CLI and Entire Graph: select a released compatible pair, inspect installed help, then record exact versions. Documentation currently specifies Graph needs Entire CLI 0.10.0+ and Git 2.36+; verify local compatibility rather than relying on an old install.
- Python standard library for the fixture, scenario enumeration, hashes, subprocess orchestration, JSON, and HTTP where sufficient.
- SQLite through Python's standard library for confirmed requirement revisions, local run indexes and UI history; use transactions and immutable revision rows. Store raw execution evidence in hashed files referenced by the database.
- FastAPI, Uvicorn, Pydantic, and Jinja2 for a single local web application. Use server-rendered HTML plus small local JavaScript; no separate frontend build chain.
- pytest for meaningful tests of the product and fixture.
- Databricks SDK for Python, the available serverless Jobs capability, and Delta tables for scenario/result history. Verify SDK operations against the installed version before coding adapters.
- One already-authorised model provider for optional automated suggestions. Structured suggestions must be reviewed. The product also accepts AI-prepared proposals through the same provenance-checked confirmation flow; never hide which mode supplied them.

Do not install a conflicting package named `pact`. The implementation package is `btw_pact`. Keep fixture execution independent of the web server's mutable process state.

### Planned files in the event fork

Paths below are relative to the new Entire Graph event fork. They are proposed implementation paths, not files created by this guide. If the fork has a conflicting layout, choose a single equivalent integration directory during Task T01 and record that mapping; do not spread files across unrelated repositories.

```text
pact/
  pyproject.toml
  src/btw_pact/
    contracts.py             shared validated records and state enums
    storage.py               transactional registry and local run index
    cli.py                   developer entrypoint and planned CLI commands
    checkpoints.py           source ingestion and provenance
    requirements.py          proposals, confirmation, revision history
    graph_adapter.py         Entire commands and output normalisation
    selector.py              reverse reachability and check selection
    scenarios.py             24-case matrix and applicability
    assertions.py            independent approved assertion templates
    evaluator.py             observation evaluation and verdict semantics
    runners/local.py         subprocess execution of trusted snapshots
    runners/databricks.py    remote upload, submit, poll, retrieval
    packaging.py             deterministic execution bundle and hashes
    evidence.py              reports, fingerprints, counterexamples
    benchmark.py             strategy comparison on identical inputs
    web.py                   local UI/API orchestration
    templates/               one application shell and evidence views
    static/                  local CSS and JavaScript
  demo/workspace_app/         registered fixture and no external data
  databricks/
    execute_review.py        notebook or job entrypoint using shared evaluator
    setup.sql                schema/table setup verified for the workspace
  tests/
    test_contracts.py
    test_requirements.py
    test_selector.py
    test_evaluator.py
    test_local_runner.py
    test_databricks_contract.py
    test_review_flow.py
    test_reproducer.py
    test_benchmark.py
  docs/
    SETUP.md
    DEMO.md
    ARCHITECTURE.md
    LIMITATIONS.md
    HANDOFF.md
    CURVEBALL.md
    EXECUTION_BOARD.md
  examples/                  clearly labelled synthetic input examples
  .env.example               names only, never populated credentials
BUILDATHON.md                event-facing submission narrative
PACT_IMPLEMENTATION_GUIDE.md  this planning guide, copied at event kickoff
```

Runtime output and `pact/runs/pact.sqlite3` belong in an ignored `pact/runs/` directory. Public evidence selected for submission can be copied into a reviewed `pact/evidence/` directory. A replay bundle contains its frozen approved requirement snapshot, so it does not depend on the presenter's SQLite database. Keep credentials, unrestricted transcripts, virtual environments, caches, and transient execution bundles out of commits.

## 6 Shared data contracts

Freeze these contracts before parallel workers implement modules. Use Pydantic records or equivalent explicit validation. All external payloads include `schema_version="1.0"`. Empty evidence is not fabricated evidence. Use explicit status and error fields when data is unavailable.

### Core records

| Record | Required fields and semantics |
| --- | --- |
| `ReviewRequest` | `repo_path`, `scope_prefix`, `base_sha`, `head_sha`, `requirement_set_id`, `scenario_set_id`, `runner`, `strategy`; resolve refs to immutable SHAs before execution |
| `CheckpointSource` | `checkpoint_id`, `session_id`, `associated_commit`, `association_status`, `message_role`, `excerpt`, `excerpt_locator`, `excerpt_hash`, `source_uri`; distinguish verified association from an imported or ambiguous record |
| `Requirement` | `requirement_id`, `revision`, `text`, `status`, `source_refs`, `entrypoints`, `assertion_template`, `scenario_filter`, `applies_to`, `confirmed_by`, `confirmed_at`, `supersedes`; no executable model-generated expression strings |
| `SymbolRef` | `commit_sha`, `graph_symbol_id`, `relative_file`, `qualified_name`, `definition_line`; identities are version-scoped |
| `ImpactPath` | `path_id`, `commit_sha`, `changed_symbol`, `entrypoint`, ordered `symbols`, typed `edges`, `edge_source_refs`, `status`; label a structural path, not a runtime trace |
| `SelectionPlan` | `request_hash`, `selected_requirement_ids`, `selection_reasons`, `path_ids`, `not_selected_ids`, `unresolved_ids`, `partial_analysis`, `traversal_limit`, `graph_version`, `snapshot_hashes` |
| `Scenario` | `scenario_id`, `role`, `operation`, `same_workspace`, `visibility`; use stable IDs derived from canonical field values |
| `Observation` | `run_id`, `side`, `commit_sha`, `scenario_id`, `allowed`, `status`, `duration_ms`, `error_kind`, `error_message`, `execution_backend`; `allowed` is null when execution did not produce a valid decision |
| `AssertionResult` | `run_id`, `requirement_id`, `requirement_revision`, `scenario_id`, `side`, `expected_allowed`, `actual_allowed`, `status`, `applicability_reason`, `observation_ref` |
| `Finding` | `finding_id`, `requirement_ref`, `path_refs`, `scenario_ref`, `classification`, `base_result_ref`, `head_result_ref`, `reproducer_ref`, `evidence_hash`; the ID derives from evidence, not UI ordering |
| `RunManifest` | `run_id`, timestamps, tool/runtime versions, repo and artifact hashes, SHAs, requirement/scenario hashes, selection strategy, `execution_scope`, `comparison_id`, `cache_mode`, backend, job reference, counts, `completion_state`, `errors`, output locations |

`applies_to` is resolved into explicit baseline/candidate applicability in the immutable request manifest, with the applicable requirement revision on each side. A moving branch name or the registry's current state cannot change a saved run. For H4, retain R1 for comparisons against the earlier policy and apply R1b to H4; do not compare different policy revisions as though they were one unchanged assertion. New/replacement revisions can have `not_applicable` on the earlier side.

### Enumerations

- Requirement status: `proposed`, `confirmed_active`, `superseded`, `unresolved`.
- Observation status: `ok`, `execution_error`, `timeout`, `invalid_output`, `cancelled`.
- Assertion status: `pass`, `fail`, `not_applicable`, `unresolved`, `not_run`.
- Finding classification: `confirmed_regression`, `candidate_violation`, `pre_existing_violation`, `intentional_change`, `inconclusive`.
- Review completion: `complete`, `partial`, `failed`. This is independent of whether completed checks pass or fail.
- Runner: `local`, `databricks`. A replayed stored report also carries `presentation_mode="recorded"`; do not relabel it as a live backend execution.

### Module interfaces

These signatures describe the implementation contract; they are not a supplied starter implementation.

| Interface | Input → output |
| --- | --- |
| `read_checkpoint_sources` | repository + immutable commit → sources + association warnings |
| `propose_requirements` | sources + registered assertion templates → proposals with cited excerpts |
| `confirm_requirement_revision` | proposal + human confirmation + applicability → immutable requirement revision |
| `analyse_change` | repository + base/head + scope → changed symbols + versioned graphs + partial-analysis metadata |
| `select_checks` | change analysis + confirmed registry + strategy → SelectionPlan |
| `build_execution_bundle` | request + snapshots + scenarios + selected assertions → bundle path + checksum + manifest |
| `execute_bundle` | verified bundle + runner configuration → observations + backend metadata |
| `evaluate_observations` | observations + confirmed assertions → AssertionResults |
| `classify_findings` | base/head results + requirement applicability + evidence gaps → Findings |
| `build_reproducer` | finding + manifest → self-contained reproduction package + instructions |
| `compare_strategies` | identical versions, registry, matrix and runner → measured comparison table |

### Planned user commands and routes

Implement the command name `btw-pact`. Commands are **planned PACT commands**, not existing Entire commands:

- `btw-pact doctor`: validate tool versions, fixture registration, backend configuration and source visibility without exposing secrets.
- `btw-pact serve`: open the local product interface.
- `btw-pact review --request PATH`: execute a validated request file and write a run manifest.
- `btw-pact reproduce --bundle PATH`: replay an approved reproduction bundle locally.
- `btw-pact benchmark --request PATH`: compare all three selection strategies using the same inputs.

Expose local routes `GET /`, `GET /api/reviews/{run_id}`, `POST /api/reviews`, `POST /api/requirements/{id}/confirm`, `GET /api/reviews/{run_id}/reproducer/{finding_id}`, and `POST /api/reviews/{run_id}/rerun`. A rerun creates a new immutable run; it does not overwrite evidence. Validate every request server-side. Bind the demo server to loopback by default. A static recorded report supports demonstration backup; public hosting is not required for the local product path.

## 7 Requirement and evidence rules

1. Preserve the original user excerpt. Agent claims such as “all tests passed” or “no behaviour change” are claims to inspect, not authoritative specifications or proof.
2. Restrict executable assertions to registered templates with typed parameters. Model output cannot supply arbitrary shell commands or executable expressions.
3. Confirmation records a human identity label and time. It is not automatic when the proposal is generated or when a timeout expires.
4. Existing requirements remain active until an explicit confirmed revision changes applicability. “Latest message wins” is not a valid general rule.
5. If sources conflict, leave the requirement unresolved and show the conflict. Do not silently choose an interpretation to get a green report.
6. Lock a requirement-set hash before evaluating a candidate. Any later alteration creates a new revision and a new run.
7. A failing candidate with a passing applicable baseline is a confirmed regression. If both fail, classify a pre-existing violation. A new requirement failing on the candidate is a candidate violation; baseline is not applicable. A missing baseline run does not support a regression attribution.
8. Passing observations with unresolved Graph coverage do not imply comprehensive verification. Show both executed-check outcomes and selection gaps.
9. Source links include version identifiers. If a hyperlink is unavailable, display verifiable file/line information and the artifact locator rather than inventing a link.
10. A clean rename is not itself a contradiction of “no behaviour change.” A dependency is not a proven runtime call. An executed failed assertion demonstrates only the applicable observed case.
11. Fixture source and scenario data are synthetic and event-owned. Preserve provenance and disclose seeded regressions. Do not upload unrelated private repositories, full private conversation histories, credentials, or personal data.
12. Keep the requirement oracle independent from the candidate implementation. Do not derive expected values by calling the candidate's permission helper or copying its conditional logic. Evaluate the explicit R1–R4 predicates against normalised observations.

## 8 Entire integration and Graph selection

### Repository and tool setup

At or after the official start, inspect the event instructions and the installed tool help. Fork the designated E2 repository, expected to be [entireio/entire-graph](https://github.com/entireio/entire-graph), verify the event's designated target, mirror in the required region, and clone the mirror. Do not implement in the current research folder as a substitute for that workflow.

Before code generation, enable Entire for the actual coding-agent client and verify capture with a small legitimate event setup change. Preserve the initial user plan in the captured session. If a particular agent client does not capture correctly, use a supported client whose capture has been verified; do not forge Checkpoints after development.

Record installed `entire version`, `entire graph version`, relevant help output, and Graph capabilities. The exact installed help is authoritative for flags and schemas. The adapter must have a single version-specific parser instead of scattering guesses throughout the product.

### Commit and snapshot discipline

- The PACT implementation commit and the evaluated fixture commits are different concepts. Record both. Do not assume the latest HEAD is the desired test target.
- Use read-only snapshot extraction or detached execution directories for base and candidate. Never change the developer's active branch to run a review.
- Keep Graph data for both versions. Deleted symbols need baseline relationships; newly introduced symbols need candidate relationships.
- Qualify symbols by file and name. Resolve renames only when source evidence supports the mapping; otherwise show an unresolved mapping. Do not connect unrelated same-named symbols.
- Narrow selection to the registered fixture prefix after ingesting the appropriate graph. Do not globally ignore the PACT implementation to make the product demo look clean; development Graph review still covers changed implementation code.
- A demo snapshot's raw evidence is saved with SHA and graph version. An example or mocked parser response cannot count as real graph evidence.

### Selection algorithm

1. Get the semantic change list between immutable versions.
2. Match changed definitions to their version-scoped graph symbols.
3. Build reverse adjacency for verified call relations. A recorded `caller → callee` edge is traversed in reverse from a changed callee to find potentially affected callers.
4. Traverse to registered requirement entrypoints, preserving a path explanation. Include a directly changed entrypoint itself.
5. Use baseline and candidate evidence for removed or changed symbols. Keep paths version-specific; never splice edges from different versions into one alleged path.
6. Select the confirmed requirements bound to those entrypoints. Add explicitly new or revised requirements such as R4 regardless of graph reachability, because their new intent itself requires verification. Label that reason separately.
7. Mark unresolved bindings and truncated traversal. A depth boundary is an evidence limitation, not proof that deeper callers are unaffected.
8. Use stable ordering for selected requirements and scenarios so reruns are comparable.

BFS is sufficient for reachability; Dijkstra/A* is not required. Reuse Entire's existing impact output where adequate. If deeper reachability is necessary, use exported typed relationships and state the configured limit. The documented `impact` depth is bounded; do not advertise unlimited analysis.

### Three strategies for evaluation

- **Changed-file baseline:** select registered entrypoints whose definition files changed, plus all explicitly new/revised requirements.
- **Graph-selected:** use the algorithm above, plus new/revised requirements.
- **All registered checks:** execute every active applicable assertion. This is the reference within the registered pilot domain, not universal program coverage.

Keep the oracle and scenario set identical across strategies. The shared-helper fixture may select nearly all requirements through Graph. That is acceptable: demonstrate recovered relevant checks and evidence; do not invent a speed advantage. A clearly labelled simple changed-file baseline is not a claim to outperform sophisticated test-selection products.

## 9 Execution and Databricks implementation

### Shared evaluator

Create one evaluator used by both backends. It enumerates scenario IDs, applies requirement applicability, validates observations, and computes results with the same contract. Do not implement two separate interpretations of the permission rules.

For each selection strategy, execute the union of scenario IDs needed by its selected, applicable assertions on each side. Deduplicate observations within that side; two assertions on one request reuse one observation. All three strategies use this same execution rule. A side with no applicable selected assertions has zero requested observations, not an invented pass.

Also execute the complete 24-scenario matrix for each fixture version as a separately identified reference run, including through Databricks for the comparison view. Use `execution_scope="selected_applicable_scenarios"` for a strategy run and `execution_scope="full_scenario_matrix"` for that reference. Store selection membership separately from reference observations. The reference run contains 24 observations per side but only the approved applicable assertion pairs. It must not supply hidden results or unreported work to a timed selected run.

The three strategy runs and the full-matrix reference share a `comparison_id` and identical version, requirement and scenario hashes. Benchmark with independent executions and `cache_mode="disabled"`; ordinary UI reruns may reuse correctly keyed evidence if the cache mode is visible. Report full-reference execution cost separately. A shared-helper change may cause Graph and all-registered selection to execute the same scenarios, and that is an honest outcome.

### Local runner

Package only the registered, trusted fixture for each commit. Launch a separate Python process per version so module imports and caches cannot leak between base and candidate. Inputs are structured JSON. Capture structured output, exit status, stderr and duration. Limit process duration and output size; use a configurable 30-second execution timeout for the small fixture and classify expiry as inconclusive.

Run with only the environment needed for the fixture, excluding cloud credentials. This is a bounded execution harness for event-owned code, not a sandbox for arbitrary hostile repositories. State that limitation.

### Databricks workflow

The remote implementation must perform actual computation. Its working data flow is:

```text
Approved execution bundle from PACT
    → upload through authenticated supported workspace API
    → serverless job executes fixture versions
    → shared evaluator produces assertion results
    → versioned scenario and result tables are written
    → query groups violations by requirement and role
    → manifest and evidence return to PACT
```

During the first cloud spike, resolve a writable workspace location, an available catalog/schema, an execution method, and a readable result location. Do not invent credentials, a catalog name, a job ID, or judge access. Register the resolved values in local configuration and put only non-secret identifiers in public evidence.

**Preferred implementation:** a Python wheel containing the shared evaluator and job entrypoint, with separately hashed baseline/candidate fixture bundles. A short Databricks notebook can orchestrate execution, write Delta tables, and return a compact result manifest. Use supported uploaded files/volumes rather than relying on the remote job to clone GitHub or call an unverified domain.

If wheel installation is the isolated blocker, run the same evaluator as uploaded Python/notebook code after verifying imports and environment parity. This changes packaging, not scope. If an SDK API is missing, verify and use the official equivalent REST or CLI operation. Record the chosen method and exact successful invocation in SETUP.md.

Do not assume Python-wheel stdout is the Jobs result API. Explicitly choose and verify a return path: a notebook's documented return value for a small manifest, or a persisted result file/table read through a supported authenticated API. Validate this before integrating the UI.

### Required data tables

Use a granted catalog with a project schema resolved during setup. The following are logical table names; qualify them using inspected workspace configuration:

| Table | Purpose and key fields |
| --- | --- |
| `pact_scenarios` | `scenario_set_id`, `scenario_id`, scenario fields, provenance and creation time |
| `pact_requirement_revisions` | requirement set/hash, revision, status, applicability, source hashes and approved assertion parameters; exclude full private transcripts |
| `pact_runs` | run ID, SHAs, tool/runtime versions, bundle hash, timestamps, backend/job reference, completion state |
| `pact_observations` | run/side/scenario identity, decision, status, duration and bounded errors |
| `pact_assertion_results` | run/side/requirement-revision/scenario identity, expected/actual values, applicability and verdict |

Preserve immutable logical records. Use idempotent writes keyed by the full identity so retries do not double counts. A new input hash means a new run. Validate duplicate-key counts explicitly; do not assume unenforced table constraints guarantee uniqueness. Mark a run complete only after expected result cardinalities and manifest hashes match.

### Remote integration acceptance

- Submit a real job from PACT and show its job/run reference.
- Read actual results back; modifying an approved scenario or candidate produces a new run and appropriate changed results.
- Match local and cloud assertion results on the same bundle. Compare semantic outputs, not job-start latency.
- Show a query grouping candidate violations by requirement and role, linked to the rows it counts.
- Restart the local UI and recover a prior remote run through the stored Databricks records. The remote history and grouped comparison must actually use these records. Removing that backend loses these remote capabilities, while the documented local execution/reproduction path still works.
- Demonstrate that a partial upload, remote timeout, unavailable result or missing row yields an explicit incomplete state.
- Use reasonable polling with visible queued/running states; do not imply instant remote compute. Apply a configurable 180-second UI wait budget, preserving the job reference if it continues remotely.
- Record the data source as synthetic and the fixture ownership as the team. No claim about production risk rates or large-scale performance follows from 24 scenarios.

### Demonstration continuity

Keep the most recent genuine Databricks report available for a clearly labelled recorded view. If connectivity fails during presentation, show that report and its identifiers, then demonstrate local reproduction separately. This preserves an honest demo; it does not count as a successful live remote run or permit hiding an unfinished F06.

## 10 Product interface and polished demo

### Visual direction

Use one restrained developer-tool interface with strong typography, generous spacing, and compact evidence cards. Use navy/charcoal text, a pale neutral background, blue for actions, amber for potential impact, red for a demonstrated violation, and green only for passed executed checks. Pair colour with text and icons. The primary screen must be legible on a projected laptop at 1280 × 720 and 1440 × 900.

Keep source text escaped. Collapse long transcripts and technical metadata behind evidence controls. Use local assets and fonts available offline. Avoid decorative graph motion that competes with the result.

### Screens and interactions

**A. Review setup**

- Repository fixture, baseline and candidate selectors resolved to SHAs.
- Checkpoint source preview and source-association warnings.
- Requirement cards showing proposed/confirmed/replaced/unresolved status.
- Assertion preview in plain language before confirmation.
- Backend selector with local/Databricks availability and truthful readiness.
- Primary action: **Review change**. Show the reason if execution is blocked by missing inputs.

**B. Review results**

- Top summary: confirmed violations, checked assertions, untested/unresolved items, execution backend and run state.
- Show the new feature alongside preserved requirements, so judges can see both outcomes.
- Compact graph path for a selected finding. Use an SVG or HTML node chain from actual saved path data; no need for an entire 3D codebase.
- Evidence panel: original requirement, changed helper, unchanged caller, concrete scenario, expected/actual values, baseline/candidate comparison.
- Actions: **Inspect source**, **Download reproducer**, **Review corrected commit**.
- Filters: violations, passed checks, intentional changes, inconclusive items.

**C. Compare and history**

- Compare Graph selection with changed-file selection and all registered checks.
- Show Databricks comparison by requirement/role with drill-down to exact rows.
- Show immutable prior runs; a correction creates a new result, preserving the failed run.
- Clear **Recorded run** badge when replaying saved evidence.

### Required interface states

Implement empty configuration, source loading, missing Checkpoint, proposed requirements, execution queued/running, complete failure report, passed selected checks, partial analysis, remote failure, recorded backup, and successful rerun. A spinner cannot be the only explanation for a long job. Show what stage is running and the job reference where available.

### Reproducer behaviour

The download contains approved scenario data, requirement revision and hash, immutable target identifiers, fixture bundle/hash or exact retrieval instructions, expected outcome, and a single documented replay command. It must work from a clean temporary directory. It must not include cloud credentials or depend on the presenter's working tree. It returns a non-zero status when the demonstrated requirement fails and a distinct error status when execution is inconclusive.

## 11 Work allocation and timeline

### Lanes

| Lane | Main responsibility | Owned areas |
| --- | --- | --- |
| A — Entire and integration | Repository setup, Checkpoint/Graph adapters, selector, integration ownership | CLI, adapters, selector, main branch integration, Checkpoint register |
| B — Execution and data | Fixture, assertions, local runner, Databricks, evaluation tests | Fixture, evaluator, runners, tables, scenario/result correctness |
| C — Product and demonstration | Interface, evidence export, reproduction workflow, docs and rehearsal | Web layer, visual states, evidence rendering, demo and submission pack |

A coordinating human resolves requirement meaning, accepts revisions, confirms official instructions, and owns final submission. The coordinator can also work in one lane. Assign human names at kickoff in the execution board; this is a required runtime assignment, not an assumption about team size.

If AI workers are available, give each one an owned file set and shared contracts. A lead worker maintains contracts.py and approves interface changes. Parallel workers must not overwrite each other's files or mutate the shared requirement registry simultaneously. Use supported Entire-captured sessions; every integration commit preserves its development context. If branches/worktrees are used, derive them from the verified mirror clone and verify Checkpoint capture there.

### Full-day timetable

| Time IST | Lane A | Lane B | Lane C | Exit evidence |
| --- | --- | --- | --- | --- |
| Before 09:00 | Read guide and generic tool practice only | Verify account access using unrelated practice material | Read pitch and event requirements | No competition implementation created |
| 09:00–09:20 | Fork/mirror/clone, capture enabled, version/help record | Inspect cloud permissions and runtime | Confirm team roles, submission access and UI structure | CP00 setup and initial plan |
| 09:20–09:45 | Freeze shared records; read a real Checkpoint | Create legitimate baseline fixture and assertions; start cloud smoke execution | Create application shell against explicit development fixtures | CP01 baseline/source excerpt; first Graph path and local assertion |
| 09:45–10:30 | Graph adapter and versioned selection | Local evaluator and cloud round trip | Confirmation flow, evidence layout and API wiring | G1 real sources + execution; G2 remote result retrieval |
| 10:30–11:15 | Integrate actual selection and report payloads | B0/H1/H2 scenario results; persist cloud records | Replace UI fixtures with actual runs; download reproducer | One complete real local and remote workflow in progress |
| 11:15–11:40 | Resolve version/mapping defects | Local/cloud parity and negative cases | Live correction flow and recorded report support | CP02 first complete review and evidence |
| 11:40–11:45 | Commit stable state and handoff | Finish bounded running work; record exact status | Snapshot demo/run links and unfinished items | CP03 pre-noon stable state |
| 11:45–12:00 | Verify Checkpoint availability | Verify reproducibility of stable run | Prepare fresh-session prompt | No unrecorded critical assumptions |
| 12:00–12:20 | Fresh-session recovery and Graph impact analysis | Assess runner/data implications | Assess UX/demo implications | CP04 actual constraint and adaptation plan |
| 12:20–13:15 | Implement required adapter/selection changes | Implement required execution/data changes | Implement required UI changes | CP05 adaptation with focused tests |
| 13:15–13:40 | Close full-scope integration gaps | Complete Databricks history and benchmark | Complete compare/history interface | Every F01–F12 row has evidence or explicit open owner |
| 13:40–14:10 | Final reliability and Graph review preparation | Full acceptance suite and measured evaluation | Clean-machine reproduction; visual/accessibility check | CP06 verified product and evaluation |
| 14:10–14:25 | Resolve demonstrated failures only | Confirm data/cardinality/backend status | Rehearse, capture genuine backup demo | CP07 demo-ready state |
| 14:25–14:35 | Audit source/evidence consistency | Audit provenance and secret exclusion | Finish BUILDATHON.md and judge-access check | Submission pack ready |
| 14:35–14:45 | Final commit and final semantic review | Save final test and cloud run evidence | Submit and verify receipt with coordinator | CP08 final submission candidate + receipt |
| 14:45–15:00 | Buffer for submission correction only | Verify no pending execution mislabelled complete | Confirm final SHA and demo links | Code freeze respected |
| 15:00–17:00 | Explain Graph/source decisions | Explain evaluation/Databricks | Present demo and handle questions | Demonstration only; no new implementation |

The timetable allocates goals, not permission to fake a milestone. If it is already after 09:00 when execution starts, inspect what exists and produce a revised remaining-time schedule that retains every F-row and protects noon/freeze obligations. Do not start work before 09:00 to compensate for an optimistic estimate.

### Live progress board

At kickoff, create `pact/docs/EXECUTION_BOARD.md` in the event repository with each T-task, owner, state (`not_started`, `in_progress`, `blocked`, `verified`), evidence reference, next action, and next checkpoint time. Update at integration points and before session handoff. A task becomes verified only after its acceptance condition has been observed. Do not mark a task complete merely because code was written.

## 12 Implementation work packages

Each task has a testable output. Write the smallest meaningful failing test for the specified behaviour, implement the change, run that test, then run the task's related suite. Preserve a Checkpoint-backed commit at the indicated milestones. UI styling needs visual inspection; do not add tests that merely mirror static markup.

### T01 Establish the official environment and capture

**Window:** 09:00–09:20. **Owner:** A, with human coordinator. **Files:** execution board and runtime configuration; generated Entire activation files only through supported setup.

- [ ] Confirm E2 target repository, private event participation obligations and any current announcements.
- [ ] Create the new event fork, Entire India mirror and mirror clone after the start.
- [ ] Enable and verify Checkpoint capture for the real agent client before competition code generation.
- [ ] Record tool versions, interpreter, supported Graph languages/relations, fork/mirror URLs and capture verification.
- [ ] Confirm the initial plan appears in authentic captured context; preserve CP00.
- [ ] Assign lanes, register actual runtime values and check the submission form can be accessed.
- [ ] Copy this planning guide to the event repository root and create the execution board at its planned path; disclose the guide as pre-event planning.

**Acceptance:** another teammate can locate CP00 and identify the correct mirror clone. If capture or required mirror region is unavailable, contact the event mentor/coordinator and record the blocker; do not assume a fake or retrospective replacement is acceptable.

### T02 Freeze shared records and establish the baseline

**Window:** 09:20–09:45. **Owners:** A for records, B for fixture. **Files:** contracts.py, scenarios.py, assertions.py, demo/workspace_app, tests/test_contracts.py, tests/test_evaluator.py.

- [ ] Define the records/enums in Section 6 and reject unknown critical fields or invalid enum values.
- [ ] Implement the four-field scenario schema; enumerate 24 stable unique IDs.
- [ ] Have the human supply/confirm R1–R3 as an actual event requirement message; implement B0 and commit it with source context.
- [ ] Encode independent R1–R3 predicates, register public entrypoints and verify the 8 applicable baseline assertion pairs.
- [ ] Record the baseline SHA and requirement-set hash before creating H1.

**Acceptance:** baseline results and original Checkpoint excerpt are available, 24 scenario IDs are unique, and no expected value is computed using the fixture's can_access implementation. Preserve CP01.

### T03 Read Checkpoints and analyse code versions

**Window:** 09:45–10:30. **Owner:** A. **Files:** checkpoints.py, graph_adapter.py, requirements.py, storage.py; tests/test_requirements.py and adapter cases in test_review_flow.py.

- [ ] Verify actual installed CLI/API formats using one real Checkpoint and Graph snapshot; save small sanitised examples for parser tests.
- [ ] Return multiple sessions without silently selecting one. Flag unavailable/imported/ambiguous source association.
- [ ] Extract proposals with exact excerpts and allowed assertion templates. Expose confirmation/revision operations.
- [ ] Persist revision history and run indexes transactionally; verify application restart retains confirmed meaning and immutable prior results.
- [ ] Resolve baseline/candidate refs before Graph calls; capture raw versioned evidence and tool versions.
- [ ] Handle missing definitions, file-qualified names, deletions and ambiguous renames explicitly.

**Acceptance:** a displayed requirement is traceable to a real source and a displayed path to raw Graph evidence at the stated commit. Missing source produces an unresolved state. No candidate code is modified by review.

### T04 Select and execute requirements locally

**Window:** 09:45–11:15; split ownership between A and B. **Files:** selector.py, evaluator.py, packaging.py, runners/local.py, cli.py; tests/test_selector.py, test_local_runner.py and test_evaluator.py.

- [ ] Build reverse call reachability with version-specific paths and explicit limits.
- [ ] Select unchanged entrypoints affected by the helper; add new/revised requirements separately.
- [ ] Package immutable trusted fixture versions with hashes; execute base/candidate in isolated processes.
- [ ] Capture observations, evaluate applicability, classify failures, and distinguish pre-existing from introduced violations.
- [ ] Create H1 and H2 during the event with authentic requirement messages and commits.
- [ ] Verify H1 violates R1 while H2 satisfies R1–R4; confirm a failed check does not become an execution error or vice versa.

**Acceptance:** a real Graph-selected unchanged caller leads to a runnable failing assertion. Local module caches cannot leak across sides. An execution timeout yields inconclusive. Target G1 by 10:30.

### T05 Implement the real Databricks backend

**Window:** start permission/runtime probe at 09:20; round trip by 10:30; full integration by 13:30. **Owner:** B, delegating the remote adapter if a separate captured worker is available. **Files:** runners/databricks.py, databricks/execute_review.py, databricks/setup.sql, tests/test_databricks_contract.py.

- [ ] Inspect workspace access and verify an execution/result round trip using event-created material after the start.
- [ ] Resolve upload, job and result APIs from the installed SDK/docs. Record their working use.
- [ ] Upload hashed bundles through the approved backend; avoid remote network fetch dependencies.
- [ ] Execute the shared evaluator; write versioned scenario, requirement, run, observation and assertion records.
- [ ] Validate output identity/cardinality and idempotent retries before marking remote completion.
- [ ] Return results and a job reference to PACT; display grouped comparison data from actual stored records.
- [ ] Compare local/cloud semantic outputs for the same bundle and test remote error handling.

**Acceptance:** F06's real computation and result retrieval are inspectable. Local observations cannot be relabelled as remote. Target G2 by 10:30. Failure of the early round trip triggers immediate targeted debugging with a mentor, not silent scope removal.

### T06 Build the confirmation and evidence interface

**Window:** 09:20–11:40, then adaptation/completion 12:20–14:00. **Owner:** C. **Files:** web.py, templates, static; integration cases in tests/test_review_flow.py.

- [ ] Build the three views and state labels from Section 10 against the frozen schema.
- [ ] Wire requirement confirmations to persisted immutable revisions, not local cosmetic toggles.
- [ ] Submit reviews and poll explicit states; show backend/job identity and unresolved items.
- [ ] Replace all development fixtures with live backend data before claiming a completed path.
- [ ] Render source-linked path evidence, assertion comparisons and rerun controls.
- [ ] Test keyboard operation, readability at both target resolutions, long excerpts, empty results and remote failure states.

**Acceptance:** the full review can be driven from the UI and the evidence corresponds to the selected commits. User-supplied text cannot inject HTML or executable code. A failed live run cannot display a previous success without a recorded-run label.

### T07 Package evidence and reproduction

**Window:** 10:30–11:40, finish by 13:40. **Owner:** C with B. **Files:** evidence.py, docs/SETUP.md, docs/DEMO.md, tests/test_reproducer.py.

- [ ] Create stable finding IDs and bounded source/evidence attachments.
- [ ] Generate a replay bundle containing only approved fixture code/data and verified hashes.
- [ ] Run the replay in a clean temporary directory against the intended immutable version.
- [ ] Confirm H1 fails the selected assertion and H2 passes when a new reproduction package targets H2.
- [ ] Preserve immutable run history and a clearly labelled recorded report.

**Acceptance:** a teammate can reproduce the result without the presenter's working tree or cloud credentials. Preserve CP02 when the first complete end-to-end path works.

### T08 Complete the actual noon adaptation

**Window:** mandatory handoff at noon, core adaptation 12:00–13:15. **Owner:** all lanes, coordinated by A/human. **Files:** whichever components the real constraint affects; docs/HANDOFF.md and docs/CURVEBALL.md.

- [ ] Complete the pre-noon preservation checklist in Section 13.
- [ ] Record the exact official constraint and its source/time; do not invent or pre-fill it.
- [ ] Start a fresh agent session, reconstruct state from real Checkpoints and this guide, and use Graph before editing.
- [ ] State the required observable behaviour, affected components, preserved invariants, and test that will demonstrate compliance.
- [ ] Implement the change; verify both the new constraint and the stable pre-noon behaviour.
- [ ] Explain effects on Databricks. If no code change is necessary there, demonstrate compatibility and explain why; do not invent an unrelated remote change.

**Acceptance:** actual curveball compliance is demonstrated with before/after evidence and authentic fresh-session context. Preserve CP04 and CP05.

### T09 Complete evaluation and reliability

**Window:** 13:15–14:10. **Owners:** A/B, with C inspecting UI output. **Files:** benchmark.py and all meaningful acceptance tests.

- [ ] Evaluate H1/H2/H3/H4 with fixed inputs and requirement revisions.
- [ ] Execute all cases in Section 14 and resolve classification/identity failures.
- [ ] Compare the three selection strategies using the same oracle and scenarios.
- [ ] Measure elapsed time by stage and actual assertion counts; disclose graph startup overhead.
- [ ] Verify local/cloud parity and re-read an actual cloud report through the user interface.
- [ ] Run repository-required checks appropriate to the changed code, including upstream build/tests if core Go source was changed. Record pre-existing unrelated failures separately with evidence.

**Acceptance:** reports and claims match observed results. Preserve CP06. Do not continue broad repeated testing without a new change or unresolved concern once required checks pass.

### T10 Prepare the polished demo and submit

**Window:** 14:10–15:00. **Owners:** C/human, with A/B providing technical evidence. **Files:** root BUILDATHON.md; pact/docs/DEMO.md, SETUP.md, ARCHITECTURE.md and LIMITATIONS.md; selected evidence.

- [ ] Run the clean teammate setup and the three-minute demonstration twice; fix observed failures.
- [ ] Capture a genuine short backup recording with visible run/backend/version context.
- [ ] Complete the rubric/evidence mapping and submission pack in Section 16.
- [ ] Preserve the final implementation commit, run final Graph review, record tests and submit the verified SHA.
- [ ] Verify submission receipt and judge access. Stop implementation by 15:00.

**Acceptance:** F01–F12 are independently auditable. If anything remains unfinished, submission accurately says so. Preserve CP07 and CP08; follow the final-SHA protocol below to avoid recursive evidence commits.

### Feature-to-delivery crosswalk

These are the final target deadlines; the earlier integration gates still apply. Acceptance IDs refer to Section 14. A code commit alone does not satisfy a row.

| Feature | Lead lane and tasks | Final target | Required acceptance/evidence |
| --- | --- | --- | --- |
| F01 Checkpoint ingestion | A; T01, T03 | 11:40 | Real source/commit/session association; A06–A07 |
| F02 Requirement preparation | A/C; T03, T06 | 13:40 | Genuine AI proposal with source, visible human confirmation, replacement and unresolved states; A05–A07, A17 |
| F03 Graph investigation | A; T03, T09 | 14:10 | Saved versioned Graph output and final development review; A08–A10 |
| F04 Check selection | A; T04, T09 | 14:10 | Unchanged caller selected through real Graph evidence; A08–A10 and measured strategy comparison |
| F05 Local runner | B; T02, T04 | 11:40 | Independent snapshots and oracle; A01–A04, A11–A12 |
| F06 Databricks | B/C; T05, T06, T09 | 14:10 | Complete integration by 13:30, then real job/table/query/UI evidence and A13–A15 |
| F07 Verdict semantics | B/A; T04, T09 | 14:10 | A01–A07, A10–A13, A17; an inconclusive run never appears verified |
| F08 Reproduction | C/B; T07 | 13:40 | Source/path/scenario/version evidence and A16 |
| F09 Interactive experience | C; T06, T09 | 14:10 | Confirmation, review, evidence, history and corrected rerun demonstrated; A17, A19 |
| F10 Comparative evaluation | A/B; T09 | 14:10 | Same-input strategy measurements with separate reference cost; A08 plus Section 14 protocol |
| F11 Authentic history | A/all lanes; T01–T10 | 14:45 | CP00–CP08, actual session restart and A18; pre-noon preservation by 11:45 |
| F12 Polished delivery | C/human; T07, T10 | 14:45 | A20, two rehearsals, genuine recording, complete submission and receipt |

## 13 Checkpoint and noon handoff protocol

### Checkpoint register

CP labels below are logical milestones. The real Checkpoint ID/URI and associated commit are captured from Entire at execution time. Never use these labels as invented evidence identifiers.

| Label | Required content |
| --- | --- |
| CP00 | Initial plan, E2/full-scope decision, environment and required setup |
| CP01 | Baseline requirements, source excerpts, baseline implementation, registry confirmation |
| CP02 | First working review, Graph path, actual failure, execution evidence and known gaps |
| CP03 | Last stable pre-noon commit and complete handoff context |
| CP04 | Fresh-session recovery, official constraint, Graph findings and adaptation plan |
| CP05 | Implemented adaptation and tests preserving old behaviour |
| CP06 | Full acceptance/evaluation results and limitations |
| CP07 | Demo-ready build, rehearsal findings, genuine backup evidence |
| CP08 | Final implementation/submission candidate and verification context |

The register lives in the event execution board or a small checkpoint record linked from BUILDATHON.md. Each entry records the real ID, commit, time, source URL or locator, purpose, test evidence and verification that a fresh session can read it. A normal Git commit is not by itself a valid Entire Checkpoint.

### Before noon

- [ ] Finish a bounded change; do not leave the only working product in uncommitted state.
- [ ] Commit the stable implementation and verify CP03 is valid and readable.
- [ ] Record the actual state of every F-row and T-task, including remote jobs still running.
- [ ] Record chosen tool versions, adapter formats, schemas, paths and working commands.
- [ ] Record baseline/candidate/implementation SHAs and requirement/scenario hashes separately.
- [ ] Save setup status, current test results, demo steps, limitations and the next actions.
- [ ] Confirm a teammate can locate the mirror, Checkpoints, product entrypoint and cloud run.
- [ ] Stop implementation and close the current agent session at the official reveal. Apply this to every active coding lane; do not leave an old coding worker implementing through the restart. Fresh workers receive the recovered context and real constraint before resuming.

### Handoff contents

HANDOFF.md must contain: product one-liner; full frozen scope; repository/mirror; implementation and fixture commits; actual Checkpoint links; completed and open tasks by owner; current failing checks; Graph evidence locators; cloud configuration references without secrets; exact start/test/reproduction commands; known assumptions; runtime limits; next work; deadlines. This is a concise current-state record, not a copy of the entire transcript.

### Fresh-session acceptance

Before edits, the fresh agent must state: what PACT currently does; which frozen capabilities remain; where the stable evidence is; what the actual curveball requires; which code Graph identifies as affected; and which tests will show both adaptation and preserved behaviour. The human verifies any ambiguous policy meaning. The agent must use the earlier real Checkpoint context, not rely only on an agent-written handoff summary.

### Final SHA protocol

Do not create an endless cycle of commits whose documentation contains their own final SHA. Commit implementation and final documentation, record CP08, then capture the resulting SHA in the submission form and external run/verification manifest. Run the final review against that SHA. If a defect requires another permitted pre-freeze commit, repeat the relevant checks and submit the new SHA. Evidence documents may refer to named version labels resolved in the manifest.

## 14 Acceptance tests and evaluation

### Mandatory test matrix

| ID | Input or condition | Expected observation |
| --- | --- | --- |
| A01 | B0 with R1–R3 | 24 observations; 8 applicable assertions pass |
| A02 | H1 with R1–R4 | Guest public export violates R1; guest public preview satisfies R4 |
| A03 | H2 with R1–R4 | All 10 applicable assertions pass |
| A04 | H3 harmless refactor | No demonstrated behaviour violation; ambiguity, if any, is disclosed |
| A05 | H4 with confirmed R1b | Superseded R1 is not enforced; the approved replacement is evaluated |
| A06 | Active requirement without verified source association | Unresolved provenance, never claimed as verified intent |
| A07 | Agent text contradicts a confirmed user requirement | Agent claim cannot override the confirmed requirement |
| A08 | Changed helper with export file unchanged | Graph selects R1 through its real dependency path |
| A09 | Deleted/renamed symbol or duplicate names | Correct version/file-qualified match or explicit unresolved mapping |
| A10 | Truncated or unsupported Graph evidence | Partial analysis remains visible; no global safe label |
| A11 | Candidate fails and applicable baseline also fails | Pre-existing violation, not introduced regression |
| A12 | Runner timeout, crash or malformed JSON | Inconclusive execution; no fabricated assertion pass |
| A13 | Cloud result has a wrong SHA/hash or missing records | Reject result as incomplete/mismatched |
| A14 | Same cloud request retried | No duplicate logical result rows or inflated counts |
| A15 | Local and remote run same verified bundle | Applicable assertion outcomes match |
| A16 | Reproducer in clean directory | Same failing assertion on its pinned candidate |
| A17 | Requirement revision changed after a run | Old run remains immutable; new revision requires a new run |
| A18 | Actual noon constraint | Newly required behaviour demonstrated and stable pre-noon path still works |
| A19 | UI long text, keyboard, errors, recorded view | Readable, escaped, explicit state; no stale-success substitution |
| A20 | Fresh teammate setup | Documented install/start/review/reproduce path succeeds |

A01–A05 describe full-matrix reference evaluation of the planned fixture and independent assertions, not claimed test results. A01's 24 observations are for B0; a two-sided full reference produces 24 observations per side. Selected-strategy runs may request fewer observations according to Section 9. Record measured counts after implementation. If a legitimate curveball changes the schema, explicitly revise the matrix with the human and retain the original baseline evidence.

### Measurement protocol

For each strategy report: total eligible requirements; selected requirements; applicable assertion-scenario pairs; executed observations; confirmed failures; unresolved cases; graph analysis time; execution time; end-to-end time; backend/runtime; and input hashes. Run one consistent comparison, then repeat only to assess variability or after changes that affect the measurement. Show graph overhead and remote startup cost separately.

Compare detection against all registered checks. A matching result means agreement on the registered test domain only. If using seeded defects, report detected seeded cases out of the disclosed set. Do not label this real-world accuracy, production coverage, guaranteed security, or formal proof.

### Product completion gate

- [ ] Every F-row has actual evidence.
- [ ] All mandatory applicable acceptance cases pass or have a disclosed unresolved issue; unresolved required cases prevent a full-completion claim.
- [ ] The official curveball has a recorded test and observed result.
- [ ] Graph and Checkpoint evidence are real and bound to the evaluated commits.
- [ ] Databricks performs actual execution and result comparison.
- [ ] Clean setup, local reproduction and polished live review are verified.
- [ ] Final documentation and submission point to the same implementation state.

## 15 Recovery decisions and scope control

### Early gates

| Gate | Deadline | If not met |
| --- | --- | --- |
| G0 valid fork/mirror/capture | 09:20 | Escalate to an event mentor immediately; preserve facts, do not fake compliance |
| G1 real Checkpoint + Graph path + executed assertion | 10:30 | Lead pairs on the failing boundary; keep UI work on the frozen schema but do not present mocked evidence |
| G2 real Databricks round trip | 10:30 | Isolate auth/upload/compute/return failure; use a verified equivalent packaging/API path; seek mentor assistance |
| G3 pre-noon stable product state | 11:40 | Stop starting new work, preserve the working state and incomplete capabilities for the mandatory handoff |
| G4 actual curveball path | 13:15 | Reallocate lanes to the real constraint and its regression test; preserve complete scope in the board |
| G5 full-scope evidence | 14:10 | Coordinator prioritises unresolved required capabilities and demo blockers; document deadline risk explicitly |

### Bounded technical recovery

- **Checkpoint format differs:** read supported installed help or source, update one adapter, test on the real record. A copied handoff summary does not become a genuine Checkpoint.
- **Graph path absent:** inspect source and raw snapshot to distinguish unsupported extraction from wrong direction/mapping. Use a simpler explicit call structure only if it remains an honest supported fixture design. Label the limitation; never draw a relationship as Graph evidence when Graph did not produce it.
- **Model service unavailable:** preserve proposals already produced with provenance, support manual preparation through the same confirmation flow, and record the automation mode. F02 requires assisted preparation with genuine model/agent proposals demonstrated at least once; manual-only operation is a degraded state, not full automation.
- **Remote packaging fails:** use the same shared evaluator through a verified notebook/Python entrypoint. Preserve the data/execution/result workload.
- **Remote quota or outage:** show explicit backend failure, continue independent work, retain genuine completed remote evidence, and seek event help. Do not purchase credits or change accounts without team authorisation. Full F06 remains open if it never worked.
- **Reproducer mismatch:** stop claiming the finding is reproducible until its pinned artifact, oracle and environment are corrected.
- **UI instability:** simplify presentation within the same screens and actions; preserve confirmation, evidence, comparison and rerun functionality.
- **Late start:** re-plan remaining hours openly with all F-rows retained. No extension is assumed.

### Change control

Frozen: the user problem, E2 orientation, F01–F12, pilot domain, truthful verdict semantics and authentic evidence. Flexible: packaging method, exact compatible versions, UI styling details, branch/worktree coordination, and changes necessary to satisfy the actual curveball. The human approves changes to requirement meaning or any proposed removal of a frozen capability. An executing AI must not spend an hour redesigning the project because another idea sounds more novel.

## 16 Judging presentation and submission

### Three-minute core demonstration

| Time | Presenter action | What it establishes |
| --- | --- | --- |
| 00:00–00:20 | Explain the reviewer and the guest-preview request | Concrete user and problem |
| 00:20–00:40 | Open original R1 Checkpoint excerpt and confirmed policy | Authentic intent and human-confirmed oracle |
| 00:40–01:10 | Review H1; show changed helper and unchanged export caller | Essential Graph contribution |
| 01:10–01:40 | Open failing guest-export case with baseline/candidate outcomes | Executed contradiction |
| 01:40–02:00 | Replay the downloaded reproducer or show the verified command/result | Inspectable evidence |
| 02:00–02:25 | Review H2; guest preview works while guest export is denied | Repair preserves new and old requirements |
| 02:25–02:45 | Open real Databricks job and requirement/role comparison | Meaningful working backend |
| 02:45–03:00 | Show actual noon adaptation evidence and state one limitation | Event-specific adaptation and honest scope |

This is a rehearsal script, not an assumed official pitch duration. Adapt to the assigned slot. Do not spend the live pitch waiting for cold remote compute: queue the genuine job before the slot or show the most recent run with its timestamp and label. Be ready to trigger a fresh job during questions.

### Five-minute extension for questions

Show H3 and H4, the strategy comparison, the exact selected-check counts, the graph coverage limit, and local/cloud parity. Let a judge vary a role/visibility/ownership input using the supported form. If the changed input lacks an approved assertion, show that limitation rather than improvising an expected outcome.

### Answers the team must understand

- **Why Graph?** It links a changed shared helper to registered requirements on unchanged callers; show the measured selection comparison.
- **Why Checkpoints?** They preserve the original user context and requirement revision history; show an actual excerpt and commit association.
- **Why Databricks?** It executes the reproducible scenario workload, maintains versioned comparison data and supplies results used by the product. Do not claim 24 cases require distributed compute.
- **What is new?** The confirmed requirement-to-path-to-executable-evidence workflow is our implementation contribution; existing impact analysis and intent-aware review are acknowledged.
- **Can it prove safety?** No. It demonstrates violations and reports outcomes for an explicit tested domain with visible gaps.
- **Is the bug staged?** Yes, deliberately seeded and disclosed to evaluate detection; the engine also handles safe and intentional changes.
- **What happened at noon?** State the real constraint, source, changed assumption, Graph analysis, adaptation and preservation test.
- **What would come next?** More fixture adapters, richer approved assertions, stronger symbol mapping and measured evaluation on independently selected repositories.

### Evidence map for the rubric

| Criterion | Evidence to present |
| --- | --- |
| Problem and innovation | User story, clear comparison with existing tools, focused differentiated workflow |
| Implementation | Real adapters, correct classifications, reproducible package and reliability tests |
| Curveball | CP03–CP05, exact requirement, before/after verification |
| Checkpoints | Actual source excerpts, confirmed revision history, captured development and fresh-session recovery |
| Graph | Raw source-backed paths, unchanged caller selection, version discipline and measured comparison |
| Demonstration and continuation | Complete live journey, readable evidence, honest limitations and plausible next work |
| Databricks category | Actual job, data provenance, reliable result retrieval, product-connected comparison and curveball compatibility |

### BUILDATHON.md contents

1. Project name, E2 track, team and one-sentence problem.
2. Working user journey and concise architecture.
3. Original plan, full scope and actual completed capability status.
4. Fork/mirror reference and setup instructions.
5. Prior materials and dependencies, identifying this pre-event plan as planning and competition implementation as event-created.
6. Authentic initial and pre-noon state, with Checkpoint links.
7. Actual curveball, fresh-session recovery, change and verification.
8. Graph findings and selection comparison, with raw evidence locators.
9. Test commands, measured results, seed disclosures and reproducibility.
10. Databricks capability, core workload, job/demo access, source paths, data provenance and limitations.
11. Demo access, recording and reproduction instructions.
12. Limitations, remaining work, future direction and relevant sources.

### Submission checklist

- [ ] Project name, confirmed E2 selection and team details.
- [ ] Correct repository, final commit SHA, Entire mirror/project URL.
- [ ] Demo access and documented setup/test commands.
- [ ] BUILDATHON.md and required Checkpoint/Graph evidence.
- [ ] Databricks opt-in and actual capabilities, role, job/workspace/demo reference, relevant code paths and reproduction instructions.
- [ ] Synthetic data provenance, permissions, snapshot dates, limitations and actual curveball effect.
- [ ] Approved judge-access mechanism; no credentials embedded in code, public reports, recording or submission.
- [ ] Submission receipt checked and recorded before freeze.

Respect the event's upstream-contribution policy; submit the fork. Do not open an upstream PR unless an Entire maintainer invites or approves it. Demonstrate the team's understanding without asking the coding agent to answer every judge question.

## 17 Instructions to the implementing AI

### Start prompt

Copy the following prompt together with this entire guide into the implementation session:

> Implement PACT for the BTW Buildathon according to PACT_IMPLEMENTATION_GUIDE.md. Read the whole guide before editing. The selected track is E2. The user froze the complete F01–F12 scope, including Databricks and the interactive demo. This is not authorisation to silently deliver the earlier local-only reduction. Treat the permission fixture as the first supported domain and retain all stated evidence semantics.
>
> First check the current IST time and the actual repository/environment. Before 09:00 on 6 September 2026, perform only permitted research and generic practice; do not generate competition implementation. At/after the start, execute T01 and verify the required new fork, Entire India mirror, mirror clone and Checkpoint capture before code generation. Read the repository's applicable instructions and installed tool documentation. Reuse authentic existing progress; do not reset a teammate's work.
>
> Create the execution board, assign owned work packages, freeze the shared records, and execute the timetable with parallel workers where available and authorised. Use real tool outputs for Graph and Checkpoint adapters. Record exact versions and immutable commit/artifact identities. Do not invent source links, Checkpoint IDs, successful runs, benchmark results or user confirmations.
>
> Give concise progress updates with verified results, current blockers, the next gate and deadline. Resolve routine implementation choices autonomously. Ask the human only for missing credentials/access, ambiguous requirement meaning, official-rule clarification, necessary consequential approvals, or a proposed change to frozen scope. Do not change confirmed policy meaning yourself.
>
> Preserve real Checkpoints at CP00–CP08. Save and verify CP03 and the stable handoff by 11:45. At noon stop implementation, close every active coding session, and have the team start fresh sessions using the handoff prompt. Implement the actual revealed constraint after reconstructing the earlier Checkpoints and using Graph. Protect the 15:00 freeze and submission buffer.
>
> Finish the product, Databricks path, acceptance/evaluation suite, polished live demo, genuine recorded backup and submission pack. Mark full completion only when every required capability has observed evidence. If time or infrastructure prevents completion, retain the full scope record, identify the exact unfinished items, and submit an honest working state by the deadline. Do not compensate by faking results or continuing past freeze.

### Fresh-session prompt at noon

The human supplies the exact official curveball text separately. Do not invent it in advance.

> This is the mandatory fresh noon session for PACT in BTW Buildathon E2. Read PACT_IMPLEMENTATION_GUIDE.md, pact/docs/EXECUTION_BOARD.md, pact/docs/HANDOFF.md and the real CP03 Checkpoint context. The full frozen scope remains F01–F12. Verify the pre-noon commit, requirement/scenario versions, Graph evidence and current test/run state. Preserve teammates' work.
>
> Read the organiser's actual curveball supplied with this prompt. Before editing, use Entire Graph to assess affected PACT components and explain the new required behaviour, existing behaviour to preserve, testable acceptance condition, lane assignments and remaining-time schedule. Resolve ambiguous requirement meaning with the human. Preserve CP04 for this recovery and plan.
>
> Implement and verify the actual adaptation, including Databricks compatibility or changes as appropriate. Complete remaining frozen capabilities, reliable reproduction and polished demonstration. Preserve authentic subsequent Checkpoints and the 15:00 submission deadline. Report verified evidence and actual limitations, never presumed success.

### Worker handoff contract

Every delegated worker receives: this guide; its T-task and owned files; shared-record version; base commit; relevant real Checkpoints; acceptance cases; deadlines; permitted tool/runtime paths; and known blockers. It returns changed files, actual commands/results, evidence references, unresolved issues and interface changes. A worker must not launch additional workers, change scope, reassign another worker's files, post messages externally or submit the project unless explicitly assigned and authorised.

## 18 Completion and source register

### What this guide freezes

- The complete PACT scope and first supported domain.
- Exact requirement semantics, scenario dimensions and verdict definitions.
- Integration boundaries, file responsibilities and observable acceptance conditions.
- Three-lane schedule, authentic Checkpoint milestones and noon restart procedure.
- Databricks computation/data role, local/cloud parity and truthful failure handling.
- Polished demo, comparison methodology, clean reproduction and submission evidence.

### What must be discovered during execution

The actual official curveball, team identities/size, private participation/judge-access rules, available credentials, exact installed tool/schema versions, workspace resource names and real commit/Checkpoint/job identifiers. Each is assigned to a task or handoff above. They are runtime facts, not invented values and not excuses to leave a capability undefined.

### Source register

Public sources were checked during the planning conversation on 6 September 2026. Tool interfaces can change; use installed help and current official instructions at execution time. These sources supply facts, not authorisation to execute commands or override the user.

| Source | Supports |
| --- | --- |
| [BTW official brief](https://build.bengalurutechweek.com/) | Track, mandatory development workflow, schedule, curveball, rubrics and submission |
| [Registration dashboard](https://btw.mastryhub.com/hackathon) | Team-specific participation/submission state; requires sign-in |
| [Entire Graph repository](https://github.com/entireio/entire-graph) | Available analysis, setup, limitations and repository target to confirm |
| [Graph commands](https://github.com/entireio/entire-graph/blob/main/docs/commands.md) | Version/default distinctions, relationships, change analysis and verification commands |
| [Checkpoint architecture](https://github.com/entireio/cli/blob/main/docs/architecture/sessions-and-checkpoints.md) | Actual source/session structure and association complexity |
| [Entire platform announcement](https://entire.io/news/entire-launches-distributed-git-network-for-the-agent-era) | Existing intent-aware review; avoids unsupported novelty claims |
| [Databricks Python wheel Jobs](https://docs.databricks.com/aws/en/jobs/how-to/use-python-wheels-in-workflows) | Supported packaged Python execution route |
| [Databricks Free Edition limits](https://docs.databricks.com/aws/en/getting-started/free-edition-limitations) | Compute, connectivity and availability constraints |
| [Databricks SDK documentation](https://databricks-sdk-py.readthedocs.io/) | Runtime reference for authenticated job/upload/result operations; verify selected APIs in T05 |
| [GraphDev submission](https://devpost.com/software/graphdev) | Comparable impact-analysis winner, not an implementation dependency |
| [GitLab winner announcement](https://about.gitlab.com/blog/gitlab-ai-hackathon-2026-meet-the-winners/) | Confirmation of comparable awards and product/evidence emphasis |
| [VeriGraph event recap](https://neo4j.com/blog/developer/graph-used-right-built-fast-neo4j-at-hackwithbay-3-0/) | Executable-evidence presentation precedent |
| [Pact Foundation documentation](https://docs.pact.io/) | Existing unrelated contract-testing product; avoid name/package confusion |

**Final completion statement to use only when supported:** “PACT's complete registered workflow, Databricks execution, actual noon adaptation, acceptance suite, reproducible evidence and polished demonstration were verified against the submitted commit. Remaining limitations are documented.” If that sentence is not true, state precisely which parts worked and which remained unfinished.
