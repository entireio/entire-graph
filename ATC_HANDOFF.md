# ATC — session handoff (read this first)

Written 11:40 IST, 6 Sep 2026. All work so far was done from a session whose cwd was
`/Users/omi/products/omisaur`, which is why **`entire checkpoint list` shows 0** —
Entire's capture hooks only fire for a Claude session started **inside this repo**.
The code is all committed and pushed; the transcripts are not. Everything from here
runs in `/Users/omi/products/entire-graph`.

## Where we are

- **Project:** ATC (Agent Traffic Control) · **Track 2 — Build with Graph Intelligence**
- **Repo:** this fork, branch `buildathon`, mirrored to `entire://aws-ap-south-1.entire.io/gh/ombhojane/entire-graph` (India region). `main` is push-protected — always push `buildathon`.
- **Head at handoff:** `d8480c0`. All commits pushed.
- **Canonical build reference:** `ATC_PLAN.md`. Judge-facing quick start: `tools/atc/README.md`. Submission doc: `BUILDATHON.md`.
- **Deadline: submit before 15:00 IST.** Noon Curveball at 12:00.

## What the product does (one paragraph)

Several coding agents work one repo in parallel. Git only catches conflicts where the
*text* overlaps, and only at merge time. The class it cannot see: session A changes a
function's signature while session B builds new callers on the old shape — merges with
zero conflicts, and the merged tree is broken. ATC intersects the two sides' blast radii
using Entire Graph (`graph diff` for write-sets, `graph neighbors --direction in` for
read-sets), classifies collisions, and reports a verdict with receipts plus a **landing
order**. A "side" may be a branch, a ref, or a **worktree path with uncommitted work**.

## Shipped and verified (24/24 tests green)

| Rung | What | Commit |
|---|---|---|
| S0 | Seeded fixture; clean-merge trap proven (`git merge` clean → `TypeError`) | `3d8fb66` |
| S1 | Collider: WW / RW / drift / proximity, receipts, landing order, exit codes | `3d8fb66` |
| S2 | Intent enrichment, tiered + source-labelled (checkpoint → commit msg) | `d44016a` |
| — | Verified on **this Go repo**: RW caught at `internal/sem/types.go:3665`, ~21s | `3c68645` |
| D1–D3 | Delta telemetry + hotspot leaderboard + **priors loop** (Databricks/local) | `8a9757a` |
| Tests | 17→24 checks: recall, precision, fail-closed, priors, live worktree, board | `aeab60a` |
| S3 | Collisions in **uncommitted** work via `git stash create` (non-disturbing) | `69b7591` |
| S4 | `--all` board auto-discovering worktrees in flight | `2e4f1c7` |
| Fix | Dependents resolved by `<file>:<line>`, not bare name (see below) | `d7587ea` |
| Docs | Judge README, semantic-diff self-review recorded, plan status | `d8480c0` |

Verified from a **clean clone** at `d8480c0`: fixture builds, verdict correct, 24/24 pass.

## Two defects the work found in itself (keep these in the pitch — corrections read as rigor)

1. **Our own final semantic-diff review changed the implementation.** `graph diff` on our
   submission reported `dependents=209` for `prefix`, `204` for `main` — name collisions
   across a polyglot repo, not real dependents. ATC's red verdicts *rest* on dependent
   resolution, so this was a live false-positive risk. Now resolved by `<file>:<line>`
   selectors from the diff's `before_start_line`, with ambiguity recorded as UNKNOWN.
2. **The `--all` board exposed two bugs:** graph JSON emits `null` (not `[]`) for empty
   sections, so `.get(k, [])` returned `None` and crashed the walk (`or []` everywhere
   now); and worktree discovery compared `/var` against macOS's `/private/var`, counting
   the main worktree as a side (`realpath` both sides now).

## Design rules — do not violate these

- **False positives are fatal.** Only signature/removal/rename changes with cross-side
  dependents go 🔴. Body-only changes and same-file proximity are 🟡 and never page.
- **Degrade, never die.** Unresolved edges are printed as ❔ UNKNOWN. Analysis errors
  exit **3** ("no verdict"), never 0 — a partial scan must never read as clean.
- **Never disturb the observed session.** Sampling a live worktree must not touch the
  agent's files, index, or stash stack. This is asserted by tests; keep it that way.
- **Label evidence scope.** A local telemetry prior must never be presented as fleet
  evidence — the backend and scope are printed with every prior.
- **Graph output is evidence, not oracle.** Print confidence and resolution so a human
  can verify; verify claims against source before asserting them.

## Rejected options (already decided — don't relitigate)

- Track 3 framing (category mismatch: ATC consumes captured context, doesn't capture new).
- The earlier "heal-agents" idea (harness auto-update trend erodes it).
- Red severity for body-only changes (false-positive risk).
- Watch mode / polling (not built; explicitly listed as a limitation, not claimed).

## Pending

1. **Checkpoints — the priority.** `entire checkpoint list` = 0. Milestones needed:
   (1) initial understanding/architecture, (2) pre-noon stable, (3) curveball response,
   (4) final + verification. From now on every commit must come from a session in this
   repo so transcripts attach.
2. **Noon Curveball at 12:00** — close the session, receive the constraint, start a
   **fresh** session, reconstruct from checkpoint context + `ATC_PLAN.md`, run
   `entire graph impact` on the affected area **before** editing, implement the smallest
   complete response, test, checkpoint.
3. **Databricks** (optional award) — needs a Free Edition account + PAT (omi must create
   it). Then: export `DATABRICKS_SERVER_HOSTNAME`, `DATABRICKS_HTTP_PATH`,
   `DATABRICKS_TOKEN`; `python3 tools/atc/telemetry.py init --backend databricks`;
   record a few runs; `telemetry.py hotspots --backend databricks`; **screenshot it**
   (Free Edition quota can die for the day). Never commit the token.
4. **Submission before 15:00** — track, fork URL + final SHA, Entire mirror URL,
   checkpoint links, setup/test instructions, working demo or fallback recording,
   complete `BUILDATHON.md`, Databricks fields if opted in.
5. **Fallback demo recording** — 90-second script in `ATC_PLAN.md` §10. The live-demo
   gods are cruel; record it before 14:30.

## Commands worth knowing

```bash
tools/atc-fixture/build_fixture.sh /tmp/atc-fixture     # seeded demo repo
python3 tools/atc/collide.py feat-auth feat-checkout --repo /tmp/atc-fixture
python3 tools/atc/collide.py --all --repo /tmp/atc-fixture      # board
python3 tools/atc/collide.py <a> <b> --repo . --record --priors # fleet memory
python3 tools/atc/telemetry.py hotspots                          # leaderboard
python3 tools/atc/test_atc.py                                    # 24 checks
```

Exit codes: `0` cleared · `1` advisory · `2` red · `3` no verdict.

## The demo beat that wins the room

`git merge` says clean → `python3 tests_checkout.py` explodes → ATC's card called it in
advance with the exact edge, the confidence, and the landing order. Then: a pair with
**zero** overlap returns CLEARED and still warns that `auth.py` ate 3 of 3 prior runs —
the fleet-memory capability a single local run cannot produce.
