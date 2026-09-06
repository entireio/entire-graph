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
test_atc.py                          # 24-check suite
```

**A side can be a worktree path**, in which case ATC analyses the *uncommitted* work in it — collisions surface before anybody commits. The live tree is sampled with `git stash create`, which writes an object and touches neither the agent's files, its index, nor its stash stack (asserted by tests).

**Exit codes:** `0` cleared · `1` advisory · `2` red · `3` no verdict (error). An error never reads as "clear".

## What it reports

| Class | Meaning | Severity |
|---|---|---|
| **WRITE–WRITE** | both sides changed the same entity | 🔴 |
| **READ–WRITE** | one side changed an entity's signature; the other has new/changed dependents on it — invisible to git | 🔴 |
| **BEHAVIOR DRIFT** | body-only change with cross-side dependents | 🟡 |
| **PROXIMITY** | same file, different entities | 🟡 |
| **UNKNOWN** | an edge the graph could not resolve — always listed, never dropped | ❔ |

Only signature/removal/rename changes go red. False positives are fatal for a tool like this, so advisories never page.

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

Static, heuristic analysis: dynamic dispatch and reflection are not resolvable and surface as UNKNOWN — verify by test. Dependent resolution prefers `<file>:<line>` selectors because bare names collide across a large polyglot repo. Landing order is a dependency-direction heuristic, not a scheduler. Watch mode is not implemented; run ATC post-commit or on demand.
