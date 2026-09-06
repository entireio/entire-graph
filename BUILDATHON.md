# ATC — Agent Traffic Control

## One-sentence summary
Git sees overlapping lines; the Entire Graph sees overlapping blast radii — ATC detects semantic collisions between parallel agent sessions *before merge* and tells you which order to land them in.

## Problem, intended user and why it matters
Running 2–4 coding agents in parallel (worktrees/branches/background sessions) is the 2026 norm. Worktree isolation — the standard practice — defers conflicts to merge time; it does not remove them. The worst class merges **textually clean and breaks at runtime** (e.g., session A changes a function's signature while session B builds new call sites on the old shape) — a class git structurally cannot see. Harnesses punt (parallel sessions "are independent and don't communicate" — Claude Code docs); prior art warned early for *human* teams (Crystal 2011, Palantír) and died pre-AI; semantic-conflict detection is active research (RefFilter 2025, Springer ASE 2025) with no shipping product. Intended user: the fleet developer babysitting N agent terminals and guessing merge order.

## Selected Entire track and why Entire is essential
**Track 2 — Build with Graph Intelligence.**
- `entire graph diff` supplies entity-level change sets per side (incl. `signature_changed` with old/new signatures) — the write-sets.
- `entire graph neighbors --direction in` on each side's tree supplies dependents with call sites, confidence and resolution evidence — the read-sets.
- The new capability — **blast-radius intersection between two work streams** — is graph intelligence end to end: remove the Graph and there is no product. Checkpoint intent enriches each side (planned S2).
Raw graph output is never displayed as the answer; ATC turns it into a verdict a developer can verify (every finding carries file:line receipts and confidence).

## Architecture and main workflow
See `ATC_PLAN.md` (canonical build reference) for the full plan, taxonomy and ladder.

```
COLLECTOR -> DIFFER -> IMPACTOR -> INTERSECTOR -> ADJUDICATOR -> REPORTER
merge-base   graph     neighbors   WW / RW /      severity +     card · JSON ·
+ temp       diff per  on other    PROX / UNK     landing        exit 0/1/2
worktrees    side      side's tree                order
```

Collision taxonomy (sessions as transactions): **WRITE–WRITE** (same entity changed both sides, red) · **READ–WRITE** (A changed an entity's signature; B has new/changed dependents on it — red; the git-invisible class) · **BEHAVIOR DRIFT** (body-only change with cross-side dependents, advisory) · **PROXIMITY** (same file, different entities, advisory) · **UNKNOWN** (unresolved edges, always listed, never dropped). Landing order: the depended-upon side lands first; the dependent side rebases and adapts before landing.

## Pre-noon state (11:45 checkpoint record)
**Done and verified (S0+S1, commit 3d8fb66):**
- `tools/atc-fixture/build_fixture.sh` — seeded fixture: two "parallel session" branches whose merge is **textually clean** (git ort, zero conflicts) yet breaks at runtime (`TypeError: validate_token() missing 'expiry'`), plus WW / PROXIMITY seeds and an independent control pair. Verified by running the merge + tests.
- `tools/atc/collide.py` — working end-to-end collider. Verified on the fixture: catches the READ–WRITE (both new call sites `quick_pay checkout.py:14`, `renew subscriptions.py:8`, with signatures old→new and confidence), the WRITE–WRITE (`parse_config`), correct advisories/proximity, correct landing order — and returns **CLEARED** on the independent control pair (precision bar).

**Also shipped and verified before noon (S2 + D1–D3 + tests):**
- **S2 — intent enrichment** (`d44016a`): every verdict card states what each side was *trying* to do, tiered and source-labelled (Entire Checkpoint intent when the head commit carries one, else commit subjects; never guessed).
- **Real-codebase verification** (`3c68645`): the same clean-merge trap reproduced in **this Go repository** — session A adds a parameter to `signatureTypeReferences`, session B adds a caller on the old shape, `git merge` reports zero conflicts. ATC flags READ–WRITE with the exact call site `internal/sem/types.go:3665` (confidence 0.92) in ~21s on a 5k-star repo. Not just a toy fixture.
- **D1–D3 — fleet telemetry + priors loop** (`8a9757a`): verdicts stream to Delta (Databricks SQL warehouse) or a local SQLite store; history feeds back as a **pre-collision warning**. Verified: after 3 recorded runs, an unrelated pair returns `CLEARED` (zero overlap) *and* surfaces `auth.py — 3 red findings across 3 prior runs (rate 1.0)`. Backend and evidence scope are always printed, so a local prior is never presented as fleet evidence.
- **Tests — 17/17 passing** (`aeab60a`, `tools/atc/test_atc.py`): recall and precision held as separate bars, the trap itself asserted, and fail-closed behaviour asserted (unknown refs exit 3 "no verdict", never 0 "clear"). Writing the tests found two real defects: the telemetry store's HOME-based isolation broke `entire` plugin discovery (now `ATC_LOCAL_DB`), and the fixture lacked a branch that made the priors capability testable.

**Unresolved / next:** S3 `--watch` + shadow-branch (uncommitted) visibility → S4 `--all` pairwise + co-change PROXIMITY → Databricks dashboard screenshots against a live workspace.

**Open risks:** static analysis misses dynamic dispatch — *closed by the Noon Curveball response (see below)*: blind spots are now detected and downgrade a clean verdict to `CLEARED_PARTIAL` (exit 4) with a verification path, so the graph's silence is never sold as safety; body-change advisories could be noisy on large repos (mitigated: advisories never page — only signature/removal/rename are red); `neighbors` symbol ambiguity on big codebases (mitigated: `--file` disambiguation retry).

## Entire Graph findings and verification
- `graph diff --base <merge-base> --head feat-auth --json` on the fixture: flagged `validate_token` `signature_changed` `def validate_token(token)` → `def validate_token(token, expiry)` with `dependents_count: 3` — verified against source (auth.py:4).
- `graph neighbors --symbol validate_token --direction in` on session B's tree: returned 5 callers incl. the two B-added ones with call sites + confidence (0.86, import_resolved) — verified: those exact lines raise TypeError after a clean merge.
- `graph search --query "format a symbol reference for output"` on this repo located `signatureTypeReferences` (`internal/sem/types.go:2299`) as the realistic subject for the Go verification — a graph lookup that directly shaped what we built and tested.
- `graph diff` on the Go pair returned `signature_changed` for `signatureTypeReferences`; `graph neighbors --direction in` returned `atcDemoTypeSummary` at `internal/sem/types.go:3665` (confidence 0.92, `resolution: import_resolved`). Verified against source: that call site passes 2 arguments to a now-3-parameter function, so the merged tree cannot build — while `git merge` reported zero conflicts.
- Graph output is treated as evidence, not oracle: every dependent is printed with its confidence and resolution reason so a reviewer can check it, and anything unresolved is surfaced as UNKNOWN rather than dropped.
- **Final semantic-diff analysis of this submission** (`graph diff --base 3a2a715 --head HEAD`): 6 files, all additions — `ATC_PLAN.md`, `BUILDATHON.md`, `tools/atc-fixture/build_fixture.sh`, `tools/atc/{collide,telemetry,test_atc}.py`. No upstream entity was modified or deleted, so the contribution is purely additive and cannot regress existing behaviour. Warnings reported and read: `E_FILE_TOO_LARGE` / `E_PARSE_ERROR` on vendored tree-sitter `grammars/*/parser.c` files — pre-existing, unrelated to our change, and acknowledged rather than ignored.
- **The self-review changed the implementation.** That same diff reported `dependents_count` of 91 for `sh`, 209 for `prefix`, 204 for `main` — implausible for functions we had just written, and on inspection they are name collisions across a large polyglot repo rather than real dependents. Since ATC's own red verdicts rest on dependent resolution, this was a live false-positive risk in the product. Fixed in `tools/atc/collide.py`: dependents are now resolved by `<file>:<line>` from the diff's `before_start_line`, falling back to name+file and only then to bare name, with every ambiguous selector recorded as UNKNOWN. Graph evidence caught a defect in the tool that consumes graph evidence.

## Noon Curveball: what changed and how we adapted

**Track 2: "the graph is evidence, not an oracle."** The constraint: a repository using
dynamic dispatch / reflection / generated code that static analysis cannot fully resolve.

**Procedure followed:** last stable state committed (`a4ad6ef`, checkpoint `782973365874`)
→ session ended → work reconstructed from checkpoint context → **`graph impact` run on
`collide` and `callers_on_tree` BEFORE any edit** (it scoped the change: `collide` →
`run_board`/`main` = render + JSON + exit codes + board; evidence produced in
`callers_on_tree`/`graph_diff`; co-change coupling named `test_atc.py`) → focused revision
→ tests → final checkpoint.

**The invalidated assumption.** ATC's `CLEARED` verdict equated "the graph found no edge"
with "no dependency exists." We proved the failure before fixing it: a probe repo where one
branch changes `handle_payment(amount)` → `(amount, currency)` and the other adds a caller
reachable only through `getattr(handlers, "handle_" + event)`. Pre-curveball ATC:
`CLEARED (0 red)`, exit 0. `git merge`: zero conflicts. Runtime: `TypeError`. A false
green from a merge-gating tool is its worst possible failure — worse than a false red.

**The design change** (commit `c13ce3c`):
- **Analysis completeness is first-class.** Every verdict carries an `analysis` block:
  `complete` plus a `gaps` list. Detectors: a blind-spot scan for dynamic-dispatch and
  generated-code sentinels in files referencing a shape-changed module on the other side;
  inventory-only languages feature-detected via `graph capabilities` (prose/data formats
  exempt — a README edit must not cry wolf); and unresolved graph unknowns.
- **Third verdict state.** Zero findings + incomplete analysis = **`CLEARED_PARTIAL`,
  exit 4** — rendered as "PARTIAL ANALYSIS — not authoritative" with a concrete VERIFY
  path (trial-merge, run both sides' suites, inspect the listed call sites). A bare
  `CLEARED` (exit 0) is only issued when analysis is complete, and says so.
- **Evidence is tiered.** Every dependent is labelled **confirmed** (structurally resolved:
  `import_resolved`/`same_file`/`type_inferred`/`exact`) or **heuristic** (`name_only` —
  "verify at source"), in both the card and JSON: confirmed structural evidence, heuristic
  evidence, and claims requiring verification are distinguishable by humans and agents.

**Why the new result is safe.** The detectors only ever *downgrade* certainty
(`CLEARED` → `CLEARED_PARTIAL`); they cannot create a red, so precision on fully resolved
code is untouched by construction — and asserted: all 24 pre-curveball checks still pass
unchanged, plus 9 new ones (runtime-broken dynamic merge, `CLEARED_PARTIAL` + exit 4,
blind spot located at `dispatch.py`, verification path present, card wording, a static
control pair **in the same repo** still red HOLD, confirmed evidence tiers, and the
precision pair still labelled analysis-complete). Fixture:
`tools/atc-fixture/build_partial_fixture.sh` — the `getattr` trap plus a static control.

## Checkpoint links and what each checkpoint proves
1. Initial understanding and intended architecture — the plan itself was authored in commit `9ebeac6` (ATC_PLAN.md: problem, taxonomy, architecture, ladder). The checkpoint attaches to **this milestone commit**, made from an in-repo session after reconstructing the full state from ATC_HANDOFF.md/ATC_PLAN.md and re-verifying the build (24/24 checks). Earlier sessions ran from a different working directory, so Entire's capture hooks could not fire for them.
2. Last stable state before the Noon Curveball — S0–S4 and D1–D3 all shipped as of `0eb376a` (ladder table in ATC_HANDOFF.md; first stable cut was `3d8fb66`, S0+S1). Re-verified from this session at 11:45: fixture rebuilds, `feat-auth × feat-checkout` → HOLD exit 2 with both planted RW call sites, control pair → CLEARED exit 0, and `tools/atc/test_atc.py` passes 24/24. The checkpoint attaches to this milestone commit. (Milestone 1 checkpoint: `4e39b887f9d5`.)
3. Response to the Noon Curveball — commit `c13ce3c`, checkpoint `89170ff08efe`. The
   commit message itself states the invalidated assumption, the design change and the
   safety argument; the session transcript shows `graph impact` running before the edit
   and the failing probe (false `CLEARED` on dynamic dispatch) demonstrated before the fix.
4. Final implementation and verification — see **Final verification (milestone 4)** below: semantic-diff review, the Flask run, the live link, and the deployment. Earlier note: docs aligned with the
   revised design, full suite re-verified at 33/33, demo cards rendered from the committed
   partial-analysis fixture (`CLEARED_PARTIAL` exit 4 on the dynamic pair; HOLD exit 2
   with confirmed-tier evidence on the static control pair in the same repo).

## Setup, run and test instructions
```bash
# prerequisites: entire CLI + graph plugin, python3, git
tools/atc-fixture/build_fixture.sh /tmp/atc-fixture       # generate seeded fixture
cd /tmp/atc-fixture

# prove the trap: clean merge, broken runtime
git checkout -b merge-demo main
git merge --no-ff --no-edit feat-auth && git merge --no-ff --no-edit feat-checkout
python3 run_tests.py          # passes
python3 tests_checkout.py     # TypeError — the collision git missed
git checkout main && git branch -D merge-demo

# ATC catches it pre-merge
python3 <fork>/tools/atc/collide.py feat-auth feat-checkout --repo .   # exit 2, HOLD
python3 <fork>/tools/atc/collide.py feat-logging feat-docs --repo .    # exit 0, CLEARED

# full test suite (33 checks: recall, precision, partial analysis, fail-closed, priors)
python3 <fork>/tools/atc/test_atc.py

# the curveball beat: dynamic dispatch the graph cannot see -> honest partial verdict
tools/atc-fixture/build_partial_fixture.sh /tmp/atc-partial
python3 <fork>/tools/atc/collide.py feat-currency feat-webhooks --repo /tmp/atc-partial
#   -> CLEARED_PARTIAL, exit 4, blind spot at dispatch.py + VERIFY path (never a false green)
python3 <fork>/tools/atc/collide.py feat-refund-sig feat-billing --repo /tmp/atc-partial
#   -> HOLD, exit 2 - same change class through a static call is still a confirmed red

# fleet memory: record verdicts, then get warned before any overlap exists
python3 <fork>/tools/atc/collide.py feat-auth feat-checkout --repo . --record
python3 <fork>/tools/atc/telemetry.py hotspots            # contention leaderboard
python3 <fork>/tools/atc/collide.py feat-audit feat-docs --repo . --priors
#   -> CLEARED (no overlap today) + "auth.py — 3 red findings across 3 prior runs"
```

Exit codes: `0` cleared (analysis complete) · `1` advisory · `2` red · `3` no verdict (error) · `4` cleared on **partial analysis only**. Neither an error nor a partial analysis ever reads as clean.

## Databricks use, data sources and limitations (if applicable)
**Capabilities used:** Databricks SQL warehouse + Delta tables (`atc.atc_runs`, `atc.atc_findings`), written and queried through `databricks-sql-connector` from `tools/atc/telemetry.py`.

**The core function Databricks performs:** ATC's per-run analysis judges one pair of branches on one machine. The Databricks layer is the *fleet's contention memory*: every verdict any developer produces lands in Delta, and the CLI queries that history back as a **pre-collision warning** — it fires on a path's track record even when the current pair has no overlap at all (verified: `feat-audit × feat-docs` → `CLEARED`, plus `auth.py — 3 red findings across 3 prior runs, rate 1.0`). Cross-developer, cross-repo, cross-time aggregation is exactly what a single local run cannot produce; remove the shared store and that early-warning capability disappears, leaving only same-moment overlap detection.

**Honest scoping (and the reason the code is backend-pluggable):** the same schema and queries run against a local SQLite store for offline development and for the test suite. That fallback is single-machine only, and the tool **always prints which backend produced a prior and its evidence scope** (`this machine only` vs `fleet-wide`) — a local prior is never presentable as fleet evidence. Only the Databricks backend delivers the fleet capability being claimed.

**Reproduction:** set `DATABRICKS_SERVER_HOSTNAME`, `DATABRICKS_HTTP_PATH`, `DATABRICKS_TOKEN` (never committed; kept in the environment), then `python3 tools/atc/telemetry.py init --backend databricks`, `... collide.py <a> <b> --record --backend databricks`, `... telemetry.py hotspots --backend databricks`.

**Data sources, provenance and limits:** synthetic data only — verdicts generated from the seeded fixture in `tools/atc-fixture/` and from this repository's own branches, produced on 6 September 2026. No customer, personal or proprietary data. Limits: the priors are frequency statistics over a small synthetic sample, so they indicate contention tendency, not probability; counts under two runs touching a path are deliberately not reported at all rather than shown as weak evidence.

## Known limitations and next steps
Static, heuristic analysis (tree-sitter): dynamic dispatch and reflection are not resolvable and surface as UNKNOWN — verify by test; dependent counts are guidance, not compiler facts. Dependent resolution prefers `<file>:<line>` selectors because bare names collide across large polyglot repos (see the self-review above); where the diff gives no location we fall back and label the weaker resolution. Landing order is a dependency-direction heuristic, not a scheduler: it names which side to land first, it does not plan a full multi-branch sequence. Priors are frequency statistics over a small synthetic sample and indicate tendency, not probability. Runtime is ~21s on a 5k-star repo, fine on demand or post-commit but not yet interactive. Watch mode is not implemented.

Next steps to production: ship as a first-class `entire graph collide` subcommand rather than a `tools/` script; read Entire **shadow branches** directly so live agent sessions are visible without the `git stash create` snapshot; event-driven watch mode; CI gate using the existing exit codes; and validation of the priors signal against real fleet data instead of synthetic runs.

## Decisions log (pre-noon)
**Chosen:** Track 2 — blast-radius intersection between parallel work streams as the core new graph capability.

**Rejected:**
- Track 3 framing — category mismatch for what ATC actually is.
- Heal-agents idea — harness auto-update trend already covers it.
- Red severity for body-only changes — false-positive risk; body-only changes with cross-side dependents stay advisory (BEHAVIOR DRIFT), only signature/removal/rename go red.

**Assumptions:**
- Tree-sitter resolution is sufficient for READ–WRITE detection (unresolved edges degrade to labeled UNKNOWN, never silent).
- Pairwise refs first; `--all` pairwise comes later (S4).

**Open risks:** static analysis misses dynamic dispatch (mitigated: UNKNOWN labeling; errors exit 3 — "no verdict ≠ clean"); body-change advisories could be noisy on large repos (mitigated: advisories never page — only signature/removal/rename are red); `neighbors` symbol ambiguity on big codebases (mitigated: `--file` disambiguation retry).

## Final verification (milestone 4)

**Live deployment:** https://atc.omisaur.app — static build of the Flask demo
(`tools/atc/public` is the bundle). Deployment protection is disabled so it opens
without a Vercel login.

**Semantic-diff review of the whole submission** (`entire graph diff --base 3a2a715
--head HEAD`, the upstream fork point): 23 files, **335 entities added, 0 modified,
0 removed**. No upstream entity was changed or deleted, so the contribution cannot
regress existing behaviour. Warnings read rather than ignored: `E_FILE_TOO_LARGE` and
`E_PARSE_ERROR` on vendored tree-sitter `grammars/*/parser.c` (pre-existing, unrelated
to this work) and `W_UNSUPPORTED_FILE` on the `interface-inspirations/*.jpg` design
references — images are not code, so being unparsed is correct rather than a defect.

**Verified end to end on pallets/flask** (`tools/atc-demo/setup_flask_demo.sh`):
`agent-ctx` IS Flask's real commit `6a64969` "pass context through dispatch methods";
`agent-timing` is a request-timing helper built on the old shape. The feature passes on
its own branch, `git merge` reports **zero conflicts**, and the merged tree raises
`TypeError: Flask.full_dispatch_request() missing 1 required positional argument: 'ctx'`.
ATC returns **HOLD (exit 2) in about 6 seconds** with receipts at `src/flask/timing.py:19`
and `:26` (`type_inferred`, confidence 0.83) — and simultaneously reports 48 labelled
blind spots, so the verdict is red *and* honest about what static analysis cannot see.
The control pair (`agent-docs` x `agent-json`) returns **CLEARED (exit 0)**: precision
holds on a real codebase, not only on the seeded fixture.

**Live link:** `tools/atc/live.py` reads each session's own Claude Code transcript for its
live prompt (tier-0 intent — it exists before any commit message does), snapshots
uncommitted work with `git stash create`, and re-runs the collider. Verified three ways:
an edit inside a worktree redrew the open page 8 seconds later with no reload; a
dependency that existed only in the working tree (`Flask.preprocess_request` ->
`timing.py:42`) was caught before any commit; and pointed at this repository mid-build,
the board showed the session writing it with the operator's own prompt as the flight plan.

**Suite:** 33/33 checks.

**Defects our own tooling found in itself** (each fixed): bare-name dependent resolution
producing implausible counts (now `<file>:<line>` selectors); the graph emitting JSON
`null` for empty sections; `/var` versus `/private/var` worktree comparison; and ATC's own
scratch worktrees being counted as agents on the live board.
