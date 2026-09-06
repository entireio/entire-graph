# ATC — Agent Traffic Control

**Git sees overlapping lines. The Entire Graph sees overlapping blast radii.**

When several coding agents work one repo in parallel, git only catches conflicts where the *text* overlaps, and only at merge time. The dangerous class is the one it cannot see: session A changes a function's shape while session B builds new callers on the old shape. That merges with zero conflicts and the merged tree is broken.

ATC intersects the two sides' blast radii using Entire Graph, and tells you what will break and **which order to land the work in**.

## 60-second demo

```bash
# 1. build the seeded fixture (two "parallel agent sessions" + a control pair)
tools/atc-fixture/build_fixture.sh /tmp/atc-fixture
cd /tmp/atc-fixture

# 2. watch git approve a broken merge
git checkout -qb merge-demo main
git merge --no-ff --no-edit feat-auth && git merge --no-ff --no-edit feat-checkout
#   -> "Merge made by the 'ort' strategy" — zero conflicts
python3 tests_checkout.py
#   -> TypeError: validate_token() missing 1 required positional argument: 'expiry'
git checkout -q main && git branch -qD merge-demo

# 3. ATC catches it before the merge
python3 <fork>/tools/atc/collide.py feat-auth feat-checkout --repo .
```

```
🗼 ATC — Agent Traffic Control   feat-auth ✈ feat-checkout
   feat-auth is trying to: require token expiry …            [commit msg]
   feat-checkout is trying to: quick_pay + subscriptions …   [commit msg]

🔴 READ–WRITE   validate_token  (auth.py)
   feat-auth changed signature:
     def validate_token(token)  →  def validate_token(token, expiry)
   feat-checkout depends on it at:
     · quick_pay()  checkout.py:14  (confidence 0.86)
     · renew()      subscriptions.py:8  (confidence 0.86)
   Git merges this cleanly. The merged tree is broken.

✈  LANDING ORDER: land feat-auth first -> feat-checkout rebases and must
   update 2 dependent call site(s) BEFORE landing. Reverse order merges
   green, then feat-auth silently breaks feat-checkout's shipped code.

verdict: HOLD  (2 red, 4 advisory)
```

## Commands

```bash
collide.py <a> <b> --repo .          # two branches, refs, or worktree paths
collide.py --all --repo .            # board across every worktree in flight
collide.py --all --branches          # ...also include local branches
collide.py <a> <b> --json            # machine-readable
collide.py <a> <b> --record          # send the verdict to the telemetry store
collide.py <a> <b> --priors          # add historical contention warnings
telemetry.py hotspots                # contention leaderboard
test_atc.py                          # 33-check suite

terminal.py --profile flask          # build the UI from a real 72k-star repo
live.py --repo . --serve             # watch the agents flying right now
```

## Live mode

```bash
python3 tools/atc/live.py --repo <path> --serve      # then open :8787
```

Everything else in ATC reasons about commits. `live.py` watches work that has
not been committed yet, which is when a collision is still cheap to avoid.
Every few seconds it sweeps each worktree (and the main checkout, when that is
in flight too) and answers three questions:

| question | source |
|---|---|
| what is this agent **trying** to do | the live prompt from that session's Claude Code transcript |
| what has it **changed** so far | `git stash create` snapshot of the uncommitted tree |
| will it **collide** | pairwise `collide` over those snapshots |

The prompt is the strongest intent signal ATC has, because it exists before any
commit message does. Transcripts are read from `~/.claude/projects/<slug>/`,
read-only, and never leave the machine. The page keeps whatever view you are on
and redraws only the data, so a collision appears while you are looking at it —
typically within one sweep of the edit landing.

Point it at this repository and you will see the session that is editing it,
with your own prompt as the flight plan.

**A side can be a worktree path**, in which case ATC analyses the *uncommitted* work in it — collisions surface before anybody commits. The live tree is sampled with `git stash create`, which writes an object and touches neither the agent's files, its index, nor its stash stack (asserted by tests).

**Exit codes:** `0` cleared (analysis complete) · `1` advisory · `2` red · `3` no verdict (error) · `4` cleared **on partial analysis only**. Neither an error nor a partial analysis ever reads as "clear".

## What it reports

| Class | Meaning | Severity |
|---|---|---|
| **WRITE–WRITE** | both sides changed the same entity | 🔴 |
| **READ–WRITE** | one side changed an entity's signature; the other has new/changed dependents on it — invisible to git | 🔴 |
| **BEHAVIOR DRIFT** | body-only change with cross-side dependents | 🟡 |
| **PROXIMITY** | same file, different entities | 🟡 |
| **UNKNOWN** | an edge the graph could not resolve — always listed, never dropped | ❔ |
| **BLIND SPOT** | a place the graph is structurally blind: dynamic dispatch (`getattr`/reflection) or generated code referencing a shape-changed module, or an inventory-only language | 🕳️ |

Only signature/removal/rename changes go red. False positives are fatal for a tool like this, so advisories never page.

## The graph is evidence, not an oracle

A static graph cannot see through dynamic dispatch, reflection, or generated
code — on such repos **"no edge" stops proving "no dependency"**. ATC therefore
never sells silence as safety:

- Every verdict carries an `analysis` block: `complete` plus a `gaps` list with
  each blind spot's location. A pair with zero findings is only `CLEARED`
  (exit 0) when analysis is complete; otherwise the verdict is
  **`CLEARED_PARTIAL`** (exit 4) with a concrete verification path
  (trial-merge, run both sides' suites, inspect the listed call sites).
- Every reported dependent is tiered: **confirmed** (structurally resolved
  edge: `import_resolved`, `same_file`, `type_inferred`, `exact`) vs
  **heuristic** (`name_only` match — the card says "verify at source").
- Inventory-only languages are feature-detected via
  `entire graph capabilities`; executable code in them can't be cleared
  structurally and is flagged (prose/data formats are exempt — a README edit
  is not a blind spot).

The blind-spot scan is itself textual and heuristic, which is safe by
construction: a hit only ever *downgrades* certainty, it never creates a red.
`tools/atc-fixture/build_partial_fixture.sh` seeds the trap: a `getattr`
router, a signature change on one branch, a new dynamic caller on the other —
git merges clean, the tree breaks at runtime, and ATC answers
`CLEARED_PARTIAL` pointing at `dispatch.py` instead of a false green.

## Fleet memory (optional, Databricks)

Verdicts can be recorded to Delta tables through a Databricks SQL warehouse. History then feeds back as a **pre-collision warning** — it fires on a path's track record even when the current pair has no overlap at all:

```
📊 HOTSPOT PRIOR  auth.py — 3 red finding(s) across 3 prior runs (rate 1.0).
   Sequence work here rather than parallelising it.
   prior evidence scope: fleet-wide  [backend: databricks]

verdict: CLEARED  (0 red, 0 advisory)
```

Configure with `DATABRICKS_SERVER_HOSTNAME`, `DATABRICKS_HTTP_PATH`, `DATABRICKS_TOKEN`. Without them the same schema runs on local SQLite for development — and the backend and evidence scope are always printed, so a local prior is never mistaken for fleet evidence.

## Limits

Static, heuristic analysis: dynamic dispatch and reflection are not resolvable; they surface as 🕳️ BLIND SPOT lines and downgrade a clean verdict to CLEARED_PARTIAL — verify by test. Dependent resolution prefers `<file>:<line>` selectors because bare names collide across a large polyglot repo. Landing order is a dependency-direction heuristic, not a scheduler. Watch mode is not implemented; run ATC post-commit or on demand.
