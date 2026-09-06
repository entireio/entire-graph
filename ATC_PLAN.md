# ATC — Agent Traffic Control

**Track 2 — Build with Graph Intelligence** · BTW Buildathon 2026 · team: omi (@ombhojane)

> **One-liner:** Git sees overlapping lines. The Graph sees overlapping blast radii. ATC lands your parallel agents in the right order.

This file is the canonical build reference. Any agent session working this repo: read this first, follow the ladder, commit + checkpoint after every rung.

---

## 1. Problem (evidence-backed)

Running 2–4 coding agents in parallel (worktrees/branches/background sessions) is the 2026 norm. The standard practice — git worktree isolation — **defers conflicts to merge time, it doesn't remove them** (Augment Code, MindStudio guides). Harnesses punt: Claude Code docs state parallel sessions "are independent and don't communicate." The failure class that hurts most: **changes that merge cleanly and disagree at runtime** — semantic conflicts git structurally cannot see. Academic prior art (Crystal 2011, Palantír) proved early conflict warning works for human teams, then died pre-AI; semantic-conflict detection is active 2025 research (RefFilter, Springer ASE) with no shipping product. Mergiraf resolves conflicts *after* the crash; nothing predicts them *during flight*.

**User:** the fleet developer babysitting N agent terminals, guessing merge order, discovering collisions at rebase time or in production.

## 2. What ATC is

Impact-aware collision detection for concurrent agent sessions, **built into Entire Graph** (Track 2's "improve Graph itself" lane). ATC computes each session's changed entities, intersects their blast radii, and emits verdicts: collision class, severity, receipts (file:line + edges), and a **landing order** (which branch merges first and why).

## 3. Collision taxonomy (DB-concurrency framing — sessions are transactions)

| Class | Detection | Severity |
|---|---|---|
| **WRITE–WRITE** | same entity modified on both sides | 🔴 alert |
| **READ–WRITE** | side A modified entity E; side B added/changed code that *depends on* E (calls, types). Zero textual overlap — git merges it silently. **The crown jewel.** | 🔴 alert |
| **PROXIMITY** | both sides changed entities in one blast radius / co-change cluster | 🟡 advisory (never pages) |
| **UNKNOWN** | edges the graph can't resolve (dynamic dispatch, reflection) | ❔ labeled, never silent |

Landing order = serializability: the more-depended-upon side lands first; the other rebases and re-verifies. Disjoint impact sets → "cleared, any order."

**Iron rules:** false positives are fatal — only 🔴 classes alert. Degrade, never die — parse failures and unresolved edges become labeled UNKNOWNs, never silent passes, and a partial scan must never present as a clean one.

## 4. Architecture

```
 refs/worktrees ─▶ COLLECTOR ─▶ DIFFER ─▶ IMPACTOR ─▶ INTERSECTOR ─▶ ADJUDICATOR ─▶ REPORTER
 (+shadow refs)    discover     graph     impact +     set algebra:   severity +     card · JSON
 checkpoint        sides        diff vs   neighbors    WW / RW /      landing        · exit code
 intent ──────────────────────▶ merge-    per entity   PROX / UNK     order          0/1/2
                                base      (read/write
                                          sets)
```

- **Differ:** `entire graph diff <merge-base>..<side>` → changed-entity set per side.
- **Impactor:** per entity, `entire graph impact` + `neighbors` → write-set (dependents) and read-set (dependencies) + co-change clusters.
- **Intersector:** `writeA ∩ writeB` → WW; `writeA ∩ readB` (and mirror) → RW; cluster overlap → PROX.
- Implementation lives in this fork (new command/tool wrapping graph internals or shelling its CLI — whichever is faster; wrapper-over-own-commands is fine, the *intersection logic* is the new capability).

## 5. CLI surface

```
entire graph collide <refA> <refB>    # one-shot verdict between two refs
entire graph collide --all            # all local branches/worktrees pairwise vs merge-base
entire graph collide --watch          # poll; print only NEW verdicts (silence is default)
entire graph collide --json           # machine-readable; exit 0=clear 1=advisory 2=red
```

Verdict card = the whole product surface: ≤15-second read, every claim with file:line receipts, `--explain N` for the evidence chain.

## 6. Build ladder (commit + checkpoint after EVERY rung)

| Rung | What | Proof | Time box |
|---|---|---|---|
| **S0** | This plan + seeded fixture repo (§7) | fixture's clean-merge RW conflict demonstrated with plain git | pre-noon |
| **S1** | Two committed refs → verdict card (WW + RW detection) | catches ≥2 planted seeds; zero reds on independent branches | first build block |
| **S2** | Checkpoint-intent enrichment (one line per side: what the session *tried* to do, from checkpoint metadata) | card shows intent lines | after S1 |
| **S3** | `--watch` + shadow refs (live pre-commit radar) | live demo: two sessions, collision flagged before either commits | stretch |
| **S4** | Landing-order refinement, `--all`, PROXIMITY via co-change | fixture seeds 4–5 caught | stretch |

**S1 alone is a complete Track 2 submission.** Do not start S3 until S1+S2 are tested and checkpointed.

## 7. Seeded fixture (build FIRST — it is both test-bed and demo)

Small Go or Python repo, `main` + two branches simulating parallel agent sessions:

1. **RW seed (the demo):** branch A changes `validate_token()` signature/behavior; branch B adds two new call sites to it in different files. `git merge` = clean; runtime/test = broken. ATC must flag RW with the exact edge.
2. **WW seed:** both branches modify `parse_config()`.
3. **PROX seed:** both touch entities in the same module/cluster.
4. **Clean pair:** two genuinely disjoint branches → ATC must report CLEARED (precision proof).
5. **UNKNOWN seed (optional):** dynamic call ATC must label unresolved.

Recall bar: catch all planted seeds. Precision bar: zero reds on the clean pair.

## 8. Databricks — Best Use opt-in (ladder, only after S2 is checkpointed)

Thesis: a local CLI can judge *this* moment; only a data platform can learn from *history and fleet*. ATC streams every verdict (`--json`) to Delta; Databricks turns telemetry into **contention intelligence that feeds back into the product**:

| Rung | What | Why it's essential (not bolted on) |
|---|---|---|
| **D1** | `tools/atc_export.py` — push run telemetry (runs, sides, entities, classes, verdicts) via `databricks-sql-connector` (SQL warehouse, PAT auth) into Delta tables | provenance-documented telemetry store |
| **D2** | SQL dashboard: contention-hotspot leaderboard (which entities/modules collide most), collision classes over time, landing-order compliance | fleet observability impossible locally |
| **D3** | **Priors loop:** CLI queries Delta for historical hotspot rate → verdict card gains a pre-collision warning: "payments/ is a contention hotspot — 68% of parallel sessions touching it collided." Warns *before any overlap exists.* | removing Databricks removes an early-warning capability the product cannot have otherwise → "materially reduces functionality" bar met |
| D4 | Genie space over the tables (stretch) | NL fleet queries |

Data: synthetic + our own repo's telemetry; snapshot dates + limits documented in BUILDATHON.md. Never commit tokens. Screenshot every working Databricks step immediately (quota can die for the day).

## 9. Required checkpoint milestones (guide-mandated)

1. **Initial understanding & architecture** — this plan (S0 commit).
2. **Pre-noon stable @11:45** — last runnable state + intent/architecture/open-risks recorded.
3. **Curveball response** — fresh session, reconstruct from checkpoints, `graph impact` before touching affected area, smallest complete response, tested.
4. **Final + verification** — semantic-diff review of the whole submission recorded.

Also show during dev (15 pts): a `graph search`/`def` lookup, an `impact` analysis before a risky change, a final `graph diff` semantic review.

## 10. Demo script (90s) + submission

1. "You run four agents in parallel. Git only catches colliding *lines* — at merge time." (10s)
2. Fixture: `git merge --no-commit` → **clean**. Run tests → **broken**. (25s)
3. `entire graph collide A B` → 🔴 READ–WRITE card with the exact edge + landing order. Re-merge in ATC's order → green. (35s)
4. Checkpoint/graph evidence + (if D3 done) the hotspot prior line. Close: known limits — static analysis heuristics, UNKNOWN labeling. (20s)

Submission by **15:00 IST**: fork URL + final SHA, Entire mirror URL, checkpoint links, setup/test instructions, BUILDATHON.md complete (outline in guide), fallback recording of the demo. Databricks fields if D≥2 shipped.

## 11. Known limits (state them; honesty reads as rigor)

Tree-sitter static analysis misses dynamic dispatch/reflection → UNKNOWN-labeled, verify by test. Dependent counts are guidance, not compiler facts. Co-change clusters are heuristic. Watch mode polls; it is not event-driven yet.
