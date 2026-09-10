# Laptop shutdown handoff — 2026-09-06

This document now opens with the authoritative takeover state. Later sections
retain the original operational history and measured source identities; their
paths, commit names, outcomes and instructions describe the state at the time
and must not be used to reconstruct or republish the old branch history.
Current machine-readable evidence is indexed by
[evidence/canonical-v1/index.json](evidence/canonical-v1/index.json), and
[cleanup/removal-map.json](cleanup/removal-map.json) resolves removed raw or
duplicate paths to retained replacements.

## Authoritative takeover state — 2026-09-10

All product, cloud, VM, benchmark and automation work remains **PAUSED** at the
user's request. Do not resume it automatically. No full P1 campaign has been
approved. The bounded sampling rules remain: at most 100 product/evaluator
invocations across all workers in a batch, stop the whole batch on the first
failure or other defined issue, review before another batch, and obtain the
user's explicit approval before any full campaign. An authorized future
diagnostic has no arbitrary product elapsed-time cutoff, but it still requires
the documented memory, task-count, identity, lease, process and validation
controls. See the [resumption plan](resumption-plan-20260907.md) and
[remaining release prerequisites](remaining-release-prerequisites.md).

The public history cleanup is complete. With the exact old-tip lease, remote
branch `codex/graph-advantage` moved from `bd4a4e7` to published rewrite tip
`8c23b2c`.
The final tree remained `e85998220ec9ec3f5162335946940f9f8a8540aa`. The
rewrite removed 1,385 artifact paths across 254 branch commits and substituted 150
historical path/old-blob versions while preserving the measured records'
original source IDs and outcomes. A fresh candidate-only clone passed Git
integrity checks, contained none of the 254 replaced commit objects and no
deleted path in reachable exclusive history, and passed 97 tests, validation
of all 66 canonical runs, and every cleanup evidence guard. All other remote
refs, including the checkpoint ref, were unchanged across publication.

The rewritten commits preserve the original commit messages, parent structure
through mapped parents, modes and tree diffs after the approved sanitation
transform. Existing Entire
checkpoints retain their original historical identities; this handoff does not
claim that they link to corresponding rewritten commit IDs. Do not attach or
amend checkpoints to manufacture such a link.

For takeover, fetch the latest remote branch into a fresh clone or a clean new
worktree. The cleanup validation above is tied to published rewrite tip
`8c23b2c`; later documentation commits may advance the branch without changing
that result. Do not use old commit IDs as checkout instructions and do not push an old
local copy of the branch, which could reintroduce removed history. The private
old-to-new mapping, verified rollback bundles, original archives and rewrite
review are intentionally outside Git under
`$HOME/.local/share/entire-graph-advantage/history-backups/graph-advantage-e440b626-20260910.6EJAGr/`.
They are private recovery and audit material, not public evidence to copy into
the repository or raw logs to publish.

Preserve the four protected local files: the three visible untracked files
`evidence/check-25887f69-linux-full/review/local-prep-dir.txt`,
`p1-corpus-20260905/frozen-baseline-initial.json`, and
`p1-corpus-20260905/frozen-baseline-pre-counts.json`, plus the ignored
`evidence/check-6f23da0a-linux-full/source.tar.gz`, whose recorded size alone is
107,437,529 bytes. A fresh
clone will not contain them; takeover work in the protected local checkout must
not delete, overwrite or add them to Git.

Current status must be reported in four separate parts:

- **Implementation status:** cleanup did not complete or approve P1-P4 product
  work. Use [ledger.md](ledger.md) for the task-level implementation state and
  retain its distinctions between delivered code, WIP, and incomplete work.
- **Correctness verified:** the cleanup/publication itself passed the fresh-
  clone integrity, 97-test, 66-canonical-run and evidence-guard checks above.
  Historical immutable product and focused compiler-correctness passes remain
  recorded under their original source identities.
- **Comparative work deferred:** P2, P3 and P4 comparative evaluation, P3 human
  adjudication, P4 development/holdout labels and GraphMark remain incomplete.
  The cleanup results do not establish any product quality, speed, memory,
  ranking or default-enablement claim.
- **Release gates failed or incomplete:** the retained P1 cold resource screen
  failed, the most recent controlled diagnostic timed out, zero clean stability
  batches have run, and no full P1 approval exists. The completion package's
  focused environment/configuration regression passed, but its wider copied
  package still has five expected setup errors caused by stale or absent
  prepared inputs. It remains WIP and unlaunched.

The next agent's starting points are the current branch, applicable `AGENTS.md`,
this handoff, the [resumption plan](resumption-plan-20260907.md), the
[remaining prerequisites](remaining-release-prerequisites.md), the
[cleanup README](cleanup/README.md), the
[WIP package README](evidence/diagnostic-dispatch-effa358f-completion/README.md),
the original requirements plan at
`entire-plan/entire-graph-advantage-implementation-plan.md`, and the current
source, manifests and protected local files. Report any drift. Unless the new
session already includes an explicit user request to resume, stop after the
takeover review and wait for one. For authorized resumed work, use cheaper-model workers for every implementation, inspection,
test, documentation, Git, VM, cloud or automation step; the main Astra agent
only orchestrates and reviews. Before any product invocation, finish and verify
the WIP controls and synthetic tests, freeze exact identities and the selected
request list, and enforce the shared 100-run/first-issue-stop budget. These are
prerequisites, not automatic authorization.

## Historical handoff entries

The sections below are preserved as dated records. Their source IDs and outcomes
remain authoritative historical evidence, but their checkout paths, branch tips,
automation states and next-step wording are superseded by the takeover state
above.

### Pause recorded on 2026-09-08

The user paused all work. The active worktree was
`<REPO_ROOT>`, branch `codex/graph-advantage`,
pre-handover tip `ef1df4ed`. Eight controlled product invocations had been consumed and
zero clean stability batches had run. The latest immutable full-check and
compiler-correctness evidence was at source `effa358f`; the latest diagnostic
timed out. One completion diagnostic without an arbitrary elapsed product
deadline was authorized in principle, but no execution could begin until the user
resumed and the 14 GiB/`TasksMax=512` controls were implemented and verified.
Full-campaign approval remained absent. The documentation checkpoint was
committed; the prepared partial package was tracked with this handoff, remained
unfrozen and behaviorally unvalidated, and had not been launched. Exact identities and protected
untracked files were to be preserved. Heartbeat `monitor-p1-corpus-campaign`
was paused via the automation control plane.

The WIP package was at
`docs/implementation/graph-advantage/evidence/diagnostic-dispatch-effa358f-completion/`.
Owned moving files were `collector/run_remote.py`,
`collector/observe_remote.py`, `collector/cloud.py` and
`controller/controller.py`; `controller/test_completion_controls.py` had only
passed syntax and diff checks. The integrated suite and CLI preflight did not
run. The package was never frozen, behaviorally validated or launched; its
`controller/manifest.json` was stale and `batch-manifest.json`,
`control-hashes.txt` and `control-files.tar.gz` had not been created. Its README
was at `evidence/diagnostic-dispatch-effa358f-completion/README.md`. No cloud, VM
or product launch occurred; all three VMs were last verified deallocated.
The package was tracked with this handoff for takeover; its internal control
artifacts remained incomplete and required owner review.

At that time, another Codex instance was expected to fetch
`codex/graph-advantage` and use its own checkout path, read the resumption plan
and this handoff, confirm the committed partial package and owner report, then
wait for the user to resume. No implementation, runtime, VM, cloud or
automation work resumed from that pause state.

Known follow-up issues then were: in the rolling UTC window
2026-09-06T07:13:01Z to 2026-09-08T07:13:01Z, 147 of 196 branch-reachable
commits were trailerless. A missing trailer was not proof that no checkpoint
existed; the historical cause remained unproven, and no history had yet been
rewritten. The raw mise/full-manifest/archive material added at `2bd309eb` was
excessive; storage cleanup was then undone. No repair or execution was
requested while that pause remained active.

## Historical status after explicit resume — 2026-09-07

The user has resumed the revised controlled sequence. The heartbeat
`monitor-p1-corpus-campaign` is ACTIVE for implementation monitoring. One
separately identified cache-off Kubernetes syntax-only snapshot diagnostic has
completed under the bounded controls; it is recorded in
`evidence/diagnostic-dispatch-1f20f694/` and is not a stability batch or
campaign result. No other product or corpus call has been made in this resumed
sequence. Controlled diagnostics, fixes and sampled batches may proceed once
prerequisites and controls pass. No full-campaign approval has been granted.
The cheaper-model routing, 100-run shared cap, first-issue stops and explicit
full-run approval boundary remain in force. The validation VM is deallocated
after collection.


The one authorized immutable verification completed on pinned commit
`1f20f694775c3d8fd616eee4b22d9b13c30ff0fe`: `mise run check` exited 0 in
42.970406 seconds, with unchanged HEAD and clean status before and after. Its
raw evidence is retained under `evidence/check-1f20f694/`. The earlier
`aab356ae` immutable failure remains retained and is not relabeled; the
reviewer fixture packaging fix is the change verified by the new pass.

The shutdown stopping point and resume sequence below are historical guidance
from before this explicit resume. At the time of handoff no product or corpus
run had occurred; the later bounded diagnostic is recorded above. Release
gates remain unchanged.

## Mandatory model routing

For any future authorized work, cheaper-model subagents perform all
implementation, diagnosis, source/data inspection, fixture and harness work,
execution/testing, result summaries, documentation, and Git, VM, or
automation/control-plane execution: Luna handles routine work and Sol handles
complex work. The main Astra agent only decomposes, assigns, coordinates,
reviews evidence/diffs, adjudicates, selects the next step, and communicates.
Do not use Astra subagents or silently fall back to Astra execution; report an
unavailable cheaper worker and leave the step unexecuted. Worker commands and
VM tasks remain subject to the shared 100-run batch cap, first-issue stops, and
explicit user approval for a full campaign. This routing grants no new
authorization. Controlled work remains subject to the revised plan,
prerequisites and stops; do not start a full campaign without explicit user
approval.

## Exact stopping point

- All three task Azure VMs confirmed deallocated. Heartbeat
  `monitor-p1-corpus-campaign` is PAUSED. All delegated work has returned.
- No local verification or remote collector remains active. Integration handle
  27301 completed; never re-poll or restart it as though still live.
- Product source `6cf92c9c` passed the immutable full check and pinned Linux
  76 top-level/10 live compiler tests. Its one corrective cold pair preserved
  exact semantics/194 known partials and stayed within the single latency/RSS
  screens. No statistical or release gate passed; old failures remain.
- Harness `aab356ae` adds explicit full diagnostic artifacts. Eight focused
  tests and race passed. Its full `mise run check` exited0 in705.514s, but
  immutable=false: repository gofmt modified six reviewer Go input files.
  Evidence and exact diff: `evidence/check-aab356ae/`. This is not relabeled
  a clean immutable pass.
- The packaging fix stores those original Go bytes as `.go.txt`; `tasks.json`
  maps original logical paths to stored paths. All16 original hashes match.
  No source input, label coordinate, or fixture origin changed.
- `full-diagnostics-collector/` remains prepared and reviewed as a reusable
  control. One newer dispatch executed exactly one OFF request under it;
  complete raw arrays, the claim ledger and source-review packet are retained
  under `evidence/diagnostic-dispatch-1f20f694/`. The result never admits a
  campaign.
- ADR0049 is PROPOSED only. It describes prospective reviewed-partial coverage
  strata while preserving corpus, thresholds, original parse-dominated set,
  explicit coverage and first-issue stops. Adoption tests and complete review
  payloads remain prerequisites. No eligibility rule changed.

## Resume sequence

**Updated user instruction, 2026-09-07:** follow `resumption-plan-20260907.md`. Sample in batches capped at100 total runs, stop/fix on issues, and obtain explicit user approval before any full P1 campaign. The steps below are prerequisites, not permission to expand automatically.

1. Re-read current plan/review and this ledger; inspect status. Preserve the
   unrelated untracked `frozen-baseline-initial.json` and
   `frozen-baseline-pre-counts.json`. No memory/competitor/prior-session research.
2. Pin the new integration commit after the packaging fix. Reuse the clean
   verification checkout at
   `/var/folders/7g/r0pg1n495tb1snh2zvk9y0_r0000gn/T/graph-integration-0c9e80f5-61zo1a1q/source`
   if it survives; otherwise create a fresh isolated checkout. The six known
   formatter mutations there were restored only after retaining their diff.
   Run required checks on that immutable commit; do not call the old run clean.
3. Re-pin the prepared Linux source/build/controller and collector before
   execution. `evidence/diagnostics-linux-aab356ae/` contains PREPARATION ONLY,
   and its guard deliberately requires an immutable aab check that did not
   pass. Do not run it unchanged or bypass that guard. Its source archive was
   `/tmp/graph-aab356ae-source.tar.gz`; recreate from recorded recipe if lost.
4. Once correctness is verified, one separately identified OFF-only full
   diagnostics collection can supply the missing194-record review payload.
   Preserve all evidence; stop on an issue. It is not a new benchmark campaign.
5. Review every partial with source evidence. Resolve outstanding fast/full
   snapshot baseline timeouts separately. Only then consider ADR0049 adoption
   with tests and a prospectively frozen run. Never resume the old campaign.
6. Required P3/P4 human review is still pending. `reviewer-packet-v1` is neutral
   development material with blank answers, not realistic P3 changes or a
   held-out set. No performance/quality claim or default enablement is allowed.

## Rollback

Keep extraction reuse off, compiler off, depth2 and ranking current. The
optional full artifact is disabled by omitting `diagnostics_path`. Do not
re-enable working-tree snapshot caching. No installation, MCP, summaries or
Brain changes are part of this work.
