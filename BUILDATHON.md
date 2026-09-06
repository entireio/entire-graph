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

**Unresolved / next (post-noon ladder):** S2 checkpoint-intent enrichment → Databricks D1–D3 (telemetry → hotspot dashboard → priors loop) → S3 `--watch`/live worktrees → S4 `--all` pairwise + co-change PROXIMITY.

**Open risks:** static analysis misses dynamic dispatch (mitigated: UNKNOWN labeling; errors exit 3 "no verdict ≠ clean"); body-change advisories could be noisy on large repos (mitigated: advisories never page — only signature/removal/rename are red); `neighbors` symbol ambiguity on big codebases (mitigated: `--file` disambiguation retry).

## Entire Graph findings and verification
- `graph diff --base <merge-base> --head feat-auth --json` on the fixture: flagged `validate_token` `signature_changed` `def validate_token(token)` → `def validate_token(token, expiry)` with `dependents_count: 3` — verified against source (auth.py:4).
- `graph neighbors --symbol validate_token --direction in` on session B's tree: returned 5 callers incl. the two B-added ones with call sites + confidence (0.86, import_resolved) — verified: those exact lines raise TypeError after a clean merge.
- Final semantic-diff analysis of the submitted implementation: **to be recorded post-curveball.**

## Noon Curveball: what changed and how we adapted
_To be filled at 12:00. Procedure: close session → receive constraint → fresh session → reconstruct from checkpoint context → `graph impact` on the affected area before editing → smallest complete response → test → checkpoint._

## Checkpoint links and what each checkpoint proves
1. Initial understanding and intended architecture — commit `9ebeac6` (ATC_PLAN.md) — _link after push_
2. Last stable state before the Noon Curveball — commit `3d8fb66` + this record — _link after push_
3. Response to the Noon Curveball — _pending_
4. Final implementation and verification — _pending_

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
```

## Databricks use, data sources and limitations (if applicable)
Planned opt-in (rungs D1–D3 in ATC_PLAN.md §8): verdict telemetry → Delta via SQL warehouse; contention-hotspot dashboard; priors loop feeding historical hotspot warnings back into the verdict card (early warning before any overlap exists — impossible locally). Data: synthetic fixture telemetry + this repo's own runs; will document snapshot dates and limits here if shipped.

## Known limitations and next steps
Static, heuristic analysis (tree-sitter): dynamic dispatch/reflection edges surface as UNKNOWN — verify by test; dependent counts are guidance, not compiler facts. Pairwise refs today (`--all` planned); watch mode planned (poll, not event-driven). Next step to production: run as an `entire` plugin subcommand with shadow-branch (uncommitted work) visibility for true pre-commit radar, and CI gate mode using the existing exit codes.

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
