# Remaining release prerequisites

This checklist reconciles the implementation plan with the authoritative
ledger. “Complete” below means code or harness delivery; it does not mean the
comparative study or release gate passed. The ledger currently defers all P2,
P3, and P4 comparative campaigns while the P1 diagnosis and post-fix
correctness evidence remain to be closed out. See the [authoritative ledger](ledger.md:7)
and the [plan's evaluation rules](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:388>).

## Global prerequisites

- Finish the current P1 diagnostic/correctness closeout and record immutable
  source, binary, environment, corpus, and harness identities. The paused P1
  observations and failed gates remain evidence; they do not authorize a new
  campaign. The stopgaps must be followed by a newly frozen evaluation
  version before any continuation.
- Keep defaults unchanged: extraction reuse off, compiler off, depth two, and
  current ranking. A failed or inconclusive study leaves an opt-in feature
  disabled or experimental.
- Create or update the required ADR decisions before any dependent release
  claim: P1/P2 payload and schema mapping, the P2 launcher/server, P3 path
  compositions, and P4 ranking/evaluation settings. Each decision needs
  alternatives, requirement evidence, and a distinguishing test.
- For every study, freeze the manifest before tuning: workload eligibility,
  inclusion/exclusion rules, labels, metric formulas, thresholds, repetitions,
  seeds, interval method, and treatment of failures, partials, and zero
  denominators. Preserve raw failures and incomplete denominators.
- Obtain a fresh immutable repository check after the post-freeze diagnostic
  changes. The ledger records the earlier source check as passed, while its
  latest diagnostic note still leaves the post-stopgap check pending; do not
  treat the 41 harness tests as a product correctness check.

## P2 — optional compiler-backed Go resolution

The ledger marks P2.1–P2.4 implementation and focused correctness evidence
complete. P2.5 has invalidation/evaluation interfaces and regression fixtures,
but its comparative task is incomplete and its release gate is not passed
([ledger task table](ledger.md:30)).

Before a P2 release study, the following must be frozen and evidenced:

- A pinned `gopls` executable and toolchain, a Linux process boundary that
  denies network access to the complete process tree, a read-only captured
  source/dependency capsule, and confined scratch/build caches. Environment
  variables alone are insufficient.
- Exact source, dependency, module/workspace, build-tag, GOOS/GOARCH, cgo,
  package-selection, server, and adapter identities for every overlay. Missing
  or blocked dependencies must produce explicit unavailable/partial status,
  never a download, source write, or fabricated target.
- Hard-Go labels that distinguish direct required targets, allowed
  implementation candidates, forbidden targets, unresolved dynamic calls, and
  coverage limitations. Evaluate direct-target precision and recall separately
  from candidate precision, recall, and coverage.
- A frozen evaluation slice and per-query budget using the plan's initial
  limits: 30 seconds total compiler work, 5 seconds per request within that
  budget, at most 500 call sites, and an 8 MiB protocol message ceiling. Count
  startup cost and report all timeouts, diagnostics, and unavailable cases.

The release gate is: no false compiler-confirmed direct target in reviewed
fixtures; improved direct-call recall on the frozen hard-Go slice with no
precision reduction; zero network access and source writes in positive and
missing-dependency tests; explicit partial/unavailable results; and enforced
time budgets. The compiler remains opt-in even if this gate passes. These
requirements are in [P2 tasks and gate](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:171)
and the [P2 execution contract](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:330).

Human or external inputs still required are reviewer-adjudicated hard-Go
labels, a provisioned and pinned local `gopls`/dependency environment, and
independent validation of the OS network/process restriction. No competitor
implementation or external product is an evaluation oracle.

## P3 — deeper impact with evidence paths

The ledger marks P3.1–P3.5 implementation and focused correctness evidence
complete, and P3.6 contract/stress fixtures complete. The realistic
comparative study, cost/RSS evidence, and covering-test relevance study remain
deferred ([ledger task table](ledger.md:35)). P2 compiler availability must not
block the static P3 release candidate.

Before the P3 gate, freeze and verify:

- Relation directions and allowed compositions for calls, type dependencies,
  data/field links, routes/events/resources, tests, historical associations,
  containment, and similarity. Keep candidate/compiler paths separate from
  confirmed structural paths.
- Contract fixtures for chains beyond two hops, cycles, diamonds, ambiguous
  names, hubs, mixed relations, external endpoints, missing/partial source,
  and multiple independent paths. Every path must reconstruct from actual
  edges and evidence; every work-limit result must report its stopping reason
  and lower-bound counts.
- A frozen depth-sensitive slice of manually adjudicated Go, TypeScript,
  Python, and configuration/route changes. Review unrelated edited files and
  unedited affected sites; changed-file lists alone are not labels.
- Identical captured inputs and budgets for baseline and treatment, with
  affected-site precision/recall, complete multi-file coverage,
  covering-test relevance, latency, memory, and payload bytes. Report macro
  and per-language/category results and mark zero denominators not applicable.

The release gate is 100% path validity on contract fixtures, full expected
reachability when budgets permit, honest partial reporting when they do not,
no default-output regression, and at least 10% relative affected-site recall
improvement on the frozen depth-sensitive slice with at most two percentage
points precision loss. See [P3 validation and gate](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:227)
and the [P3 task list](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:218).

Human input is required to adjudicate affected sites, required/allowed/
forbidden paths, route/config behavior, and covering-test relevance across the
four fixture categories. External inputs are limited to the separately pinned
repositories/toolchains used to reproduce those fixtures; they do not replace
reviewer labels.

## P4 — query-seeded graph ranking

P4.2 and P4.3 implementation/correctness work is complete. P4.1 has
development manifests and provenance interfaces but no adjudicated release
baseline; P4.4 controls exist without a completed ablation; P4.5 and P4.6
studies remain undone ([ledger task table](ledger.md:41)). The retained
zero-gain development result is not holdout or agent evidence.

Required sequence:

1. Freeze a repository-disjoint development/holdout manifest with task
   descriptions known before fixes, labels, split rules, captured inputs,
   ranking outputs, and script versions. Keep duplicate or related task
   families within one split.
2. Adjudicate relevant symbols/source regions and all required sites before
   scoring. Define recall, complete-site coverage, deduplication, fixed-byte
   limits, and repository/task-family clustered uncertainty in the manifest.
3. Run the identical-input development arms: current ranking, current plus
   identical expansion, lexical plus unweighted graph, weighted graph, and
   weighted graph plus compiler overlay. Include candidate scope, profile,
   output bytes, graph work, compiler startup, failures, and timeouts.
4. Select weights, cutoffs, expansion limits, and the decision rule using
   development data only. Freeze the winning candidate and do not tune on the
   holdout.
5. Run the disjoint holdout once per frozen candidate and explain subgroup
   failures. Any later tuning requires a new holdout or an explicit
   development reclassification.
6. Only after retrieval passes, run the paired GraphMark agent experiment with
   identical model/build, prompts, tools, budgets, verification instructions,
   and independent task oracles. Determine sample size and the noninferiority
   margin prospectively; an underpowered or inconclusive result does not pass.

The proposed retrieval gate is at least 5% relative relevant-site recall gain
at fixed bytes, no more than one percentage point loss in complete required-
site coverage, no exact-symbol/path regression, and no more than 10% p95
total-query latency increase. The proposed agent gate requires a paired
interval excluding more than two percentage points of resolution loss and no
new severity-critical regression; claim a gain only when the interval supports
it. These margins must be frozen before the holdout ([P4 evaluation and gate](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:256)).

Human or external inputs are required for GraphMark task/relevance
adjudication, split review, independent resolution oracles, prospective power
analysis, and the fixed model/build/tool configuration. No default promotion
follows from numerical PageRank tests or anecdotes.

## Release handoff

The plan requires each task to ship code, independently sourced fixtures,
focused test results, a contract delta, benchmark artifacts, and rollback
instructions. Product admission additionally requires default and opt-in
combination checks, captured-input equivalence, working-tree edit checks, and
an evidence index containing source/binary/toolchain/corpus hashes, settings,
resources, seeds, raw failures, scoring scripts, and review decisions. See
[cross-workstream handoff](</Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md:406).

Until these study prerequisites and gates pass, P2/P3/P4 remain opt-in or
deferred and the current defaults stay in force.
