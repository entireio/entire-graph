# Proposed first stability batch (planning only)

This note is a future selection rule, not a launch manifest. It applies only
after the current source, evaluator, corpus, and immutable-check blockers are
closed. The P1 plan caps one batch at 100 total product/evaluator invocations;
the selected request list must be frozen before dispatch, and the full campaign
still needs the user’s explicit approval.

Use the frozen six-repository workload from
`docs/implementation/graph-advantage/corpus/corpus-manifest.json`:
`go-chi-chi`, `kubernetes-kubernetes`, `colinhacks-zod`, `psf-requests`,
`entire-graph-frozen-88dd1dc9`, and `synthetic-2000`. Candidate dimensions are
profiles `syntax-only`, `fast`, and `full`; verbs `snapshot` and `search`; and
the frozen scenarios `cold`, `unchanged`, `one-edit`, `ten-edit`, `rename`,
`delete`, `branch-switch`, and `manifest-edit`.

Use the normal non-profiled evaluator settings for this stability batch:
leave the CPU-profile opt-in unset and require the ordinary observation and
bounded diagnostics artifacts. CPU profiles belong to the current targeted
diagnostic, not stability sampling overhead.

Select 30 cells with a reproducible backtracking rule. Form the eligible
profile/verb/scenario/trial-0 candidates, compute
`sha256("stability-1|" + repository + "|" + profile + "|" + verb + "|" + scenario)`,
and sort by `(digest, repository, profile, verb, scenario, trial)`. Walk that
ordered list depth-first, accepting the first complete set that satisfies
exactly five cells per repository, at least one cell for every profile and
verb within each repository, and at least one cell for each of the eight
scenarios; prune a branch when any quota cannot be met by its remaining
candidates. The resulting ordered cell list and digest are the frozen
selection record. This gives all six repositories, all three profiles, both
verbs, and all mutation categories without scheduling the full Cartesian
matrix. Historical timeout, partial, and semantic-mismatch rows may enter
only when their retained source-grounded identity is explicitly attached to
the candidate set; labels alone do not admit a row.

Each selected cell reserves one preparatory product invocation and two paired
arms, OFF and ON. Thus the declared cost is `30 * (1 + 2) = 90` total
product/evaluator invocations, leaving ten invocations of the shared 100-run
cap unused. The preparation is counted in the cell’s
`preparatory_invocations`; it is never treated as free metadata. Pair arms use
identical source/configuration/input identities and alternate arm order by the
cell’s canonical rank. A failure or interruption consumes its reserved attempt;
there is no automatic retry. The existing `batch_budget.py` cap is 100, so
“one batch” means one selected manifest with durable worker allocations and no
second queue hidden behind it.

Before this batch can be formed, all of the following must be true: the
450bede9 source-specific evaluator build and binary hash are recorded; the
source archive, corpus digest/manifest, remote root, and control archive agree;
the fresh immutable full-check result is passed against the exact evaluator
source with clean source-integrity evidence; the route-prefilter correctness
fix and its affected checks are reviewed; known full/fast timeout and partial
admission blockers have a source-grounded disposition; and no worker, VM,
claim, or prior batch remains active. Any source drift, missing identity,
novel or unreviewed partial, semantic/warning mismatch, timeout, control or
lease failure, missing observation or required diagnostics, or resource-limit violation pauses
the whole batch at the first issue and leaves later cells explicitly unrun.

A known or source-reviewed partial remains partial and does not become a
complete observation; the governing complete-only admission rule is unchanged,
and proposed ADR0049 is not adopted by this plan. Three consecutive clean
batches are representable by the existing selected manifest, durable-claim,
arm-pair, preparation-cost, and first-issue-stop interfaces without changing
the batch accounting code. That is only a mechanical protocol observation: it
does not establish that any batch will be clean, does not waive the unresolved
gates above, and does not authorize a full campaign.
