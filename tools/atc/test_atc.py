#!/usr/bin/env python3
"""ATC test suite — recall, precision, and honest-failure behaviour.

Two bars are held separately (a checker that cries wolf gets deleted):
  RECALL     every planted collision must be caught.
  PRECISION  genuinely independent work must produce ZERO reds.

Also asserts the safety property that matters most: an analysis error must
exit 3 ("no verdict"), never 0 ("clear").

Run:  python3 tools/atc/test_atc.py [--keep]
"""

import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
COLLIDE = os.path.join(HERE, "collide.py")
TELEMETRY = os.path.join(HERE, "telemetry.py")
FIXTURE_SH = os.path.join(os.path.dirname(HERE), "atc-fixture", "build_fixture.sh")
PARTIAL_FIXTURE_SH = os.path.join(os.path.dirname(HERE), "atc-fixture",
                                  "build_partial_fixture.sh")

PASS, FAIL = "\033[32mPASS\033[0m", "\033[31mFAIL\033[0m"
results = []


def check(name, cond, detail=""):
    results.append((name, bool(cond), detail))
    print(f"  {PASS if cond else FAIL}  {name}" + (f"\n        {detail}" if detail and not cond else ""))


def run_collide(repo, a, b, *extra):
    p = subprocess.run([sys.executable, COLLIDE, a, b, "--repo", repo, "--json", *extra],
                       capture_output=True, text=True)
    try:
        return p.returncode, json.loads(p.stdout)
    except json.JSONDecodeError:
        return p.returncode, {"_stdout": p.stdout, "_stderr": p.stderr}


def main():
    fixture = tempfile.mkdtemp(prefix="atc-test-")
    os.rmdir(fixture)
    subprocess.run(["bash", FIXTURE_SH, fixture], check=True, capture_output=True)
    print(f"\nfixture: {fixture}\n")

    # ---------- the trap: git merges clean, the tree is broken -------------
    print("TRAP (the class git cannot see)")
    subprocess.run(["git", "-C", fixture, "checkout", "-qb", "merge-demo", "main"], check=True)
    m1 = subprocess.run(["git", "-C", fixture, "merge", "--no-ff", "--no-edit", "feat-auth"],
                        capture_output=True, text=True)
    m2 = subprocess.run(["git", "-C", fixture, "merge", "--no-ff", "--no-edit", "feat-checkout"],
                        capture_output=True, text=True)
    check("git merges both sessions with zero conflicts",
          m1.returncode == 0 and m2.returncode == 0, m1.stderr + m2.stderr)
    broken = subprocess.run([sys.executable, "tests_checkout.py"], cwd=fixture,
                            capture_output=True, text=True)
    check("merged tree is nonetheless broken at runtime",
          broken.returncode != 0 and "validate_token" in broken.stderr,
          broken.stderr[-200:])
    subprocess.run(["git", "-C", fixture, "checkout", "-q", "main"], check=True)
    subprocess.run(["git", "-C", fixture, "branch", "-qD", "merge-demo"], check=True)

    # ---------- RECALL -----------------------------------------------------
    print("\nRECALL (seeded pair feat-auth x feat-checkout)")
    code, r = run_collide(fixture, "feat-auth", "feat-checkout")
    rw = {f["entity"] for f in r.get("findings", {}).get("read_write", [])}
    ww = {f["entity"] for f in r.get("findings", {}).get("write_write", [])}
    prox = {f["path"] for f in r.get("findings", {}).get("proximity", [])}
    check("READ-WRITE seed caught (validate_token)", "validate_token" in rw, str(rw))
    check("WRITE-WRITE seed caught (parse_config)", "parse_config" in ww, str(ww))
    check("PROXIMITY seed caught (payments.py)", "payments.py" in prox, str(prox))
    rwf = next((f for f in r.get("findings", {}).get("read_write", [])
                if f["entity"] == "validate_token"), None)
    dep_paths = {d["path"] for d in (rwf or {}).get("dependents", [])}
    check("both new call sites reported (checkout.py, subscriptions.py)",
          {"checkout.py", "subscriptions.py"} <= dep_paths, str(dep_paths))
    check("signature transition recorded old -> new",
          rwf and "expiry" in (rwf.get("new_signature") or "")
          and "expiry" not in (rwf.get("old_signature") or ""),
          str(rwf and (rwf.get("old_signature"), rwf.get("new_signature"))))
    check("landing order names the depended-upon side first",
          (r.get("landing_order") or "").startswith("land feat-auth first"),
          str(r.get("landing_order"))[:120])
    check("verdict HOLD with exit code 2", r.get("verdict") == "HOLD" and code == 2,
          f"verdict={r.get('verdict')} exit={code}")

    # ---------- PRECISION --------------------------------------------------
    print("\nPRECISION (independent pair feat-logging x feat-docs)")
    code, r = run_collide(fixture, "feat-logging", "feat-docs")
    check("zero reds on independent work", r.get("reds") == 0, json.dumps(r.get("findings")))
    check("zero advisories on independent work", r.get("advisories") == 0, "")
    check("verdict CLEARED with exit code 0", r.get("verdict") == "CLEARED" and code == 0,
          f"verdict={r.get('verdict')} exit={code}")
    check("complete analysis is labelled complete (still authoritative)",
          r.get("analysis", {}).get("complete") is True
          and r.get("analysis", {}).get("gaps") == [],
          json.dumps(r.get("analysis"))[:200])

    # ---------- PARTIAL ANALYSIS (the graph is evidence, not an oracle) ----
    print("\nPARTIAL ANALYSIS (dynamic dispatch the graph cannot resolve)")
    pfx = tempfile.mkdtemp(prefix="atc-partial-")
    os.rmdir(pfx)
    subprocess.run(["bash", PARTIAL_FIXTURE_SH, pfx], check=True, capture_output=True)

    # the trap exists: git merges clean, runtime breaks through the router
    subprocess.run(["git", "-C", pfx, "checkout", "-qb", "merge-demo", "main"], check=True)
    subprocess.run(["git", "-C", pfx, "merge", "-q", "--no-ff", "--no-edit",
                    "feat-currency"], check=True, capture_output=True)
    subprocess.run(["git", "-C", pfx, "merge", "-q", "--no-ff", "--no-edit",
                    "feat-webhooks"], check=True, capture_output=True)
    broken = subprocess.run(
        [sys.executable, "-c",
         "from webhooks import on_stripe_event; on_stripe_event({'amount': 1})"],
        cwd=pfx, capture_output=True, text=True)
    check("dynamic-dispatch merge is broken at runtime (the invisible class)",
          broken.returncode != 0 and "handle_payment" in broken.stderr,
          broken.stderr[-200:])
    subprocess.run(["git", "-C", pfx, "checkout", "-q", "main"], check=True)
    subprocess.run(["git", "-C", pfx, "branch", "-qD", "merge-demo"], check=True)

    code, r = run_collide(pfx, "feat-currency", "feat-webhooks")
    check("dynamic pair is NOT presented as a bare CLEARED",
          r.get("verdict") != "CLEARED",
          f"verdict={r.get('verdict')}")
    check("verdict CLEARED_PARTIAL with its own exit code 4",
          r.get("verdict") == "CLEARED_PARTIAL" and code == 4,
          f"verdict={r.get('verdict')} exit={code}")
    gaps = r.get("analysis", {}).get("gaps", [])
    dyn = [g for g in gaps if g.get("kind") == "dynamic_dispatch"]
    check("blind spot identifies the dynamic call site (dispatch.py, module handlers)",
          any(g.get("file") == "dispatch.py" and g.get("module") == "handlers"
              for g in dyn), json.dumps(gaps)[:300])
    check("analysis marked incomplete with a verification path",
          r.get("analysis", {}).get("complete") is False
          and "test" in (r.get("analysis", {}).get("verification") or ""),
          json.dumps(r.get("analysis", {}))[:200])
    p = subprocess.run([sys.executable, COLLIDE, "feat-currency", "feat-webhooks",
                        "--repo", pfx], capture_output=True, text=True)
    check("card says PARTIAL ANALYSIS / not authoritative, and shows the blind spot",
          "PARTIAL ANALYSIS" in p.stdout and "not authoritative" in p.stdout
          and "BLIND SPOT" in p.stdout, p.stdout[-400:])

    # control: fully resolved code in the SAME repo keeps existing behaviour
    code, r = run_collide(pfx, "feat-refund-sig", "feat-billing")
    check("static control pair in the same repo is still a red HOLD (exit 2)",
          r.get("verdict") == "HOLD" and code == 2
          and any(f["entity"] == "handle_refund"
                  for f in r.get("findings", {}).get("read_write", [])),
          f"verdict={r.get('verdict')} exit={code}")
    deps = [d for f in r.get("findings", {}).get("read_write", [])
            for d in f.get("dependents", [])]
    check("resolved dependents carry a confirmed evidence tier",
          deps and all(d.get("evidence") == "confirmed" for d in deps),
          json.dumps(deps)[:200])

    # ---------- FAIL-CLOSED -------------------------------------------------
    print("\nFAIL-CLOSED (errors must never read as 'clear')")
    p = subprocess.run([sys.executable, COLLIDE, "no-such-ref-x", "no-such-ref-y",
                        "--repo", fixture], capture_output=True, text=True)
    check("unknown refs exit 3 (no verdict), not 0", p.returncode == 3,
          f"exit={p.returncode}")
    check("error message refuses a clean reading",
          "do NOT treat as clean" in p.stderr, p.stderr[:160])

    # ---------- TELEMETRY + PRIORS -----------------------------------------
    print("\nTELEMETRY & PRIORS (fleet memory feeds back)")
    # isolated telemetry store; HOME is left alone so `entire` plugins resolve
    env = dict(os.environ, ATC_TELEMETRY_BACKEND="local",
               ATC_LOCAL_DB=os.path.join(fixture, ".atc-test", "telemetry.db"))
    for _ in range(2):
        subprocess.run([sys.executable, COLLIDE, "feat-auth", "feat-checkout",
                        "--repo", fixture, "--json", "--record", "--backend", "local"],
                       capture_output=True, text=True, env=env)
    hs = subprocess.run([sys.executable, TELEMETRY, "hotspots", "--backend", "local"],
                        capture_output=True, text=True, env=env)
    hot = json.loads(hs.stdout).get("hotspots", []) if hs.returncode == 0 else []
    check("recorded runs produce a contention leaderboard",
          any(h["path"] == "auth.py" and h["reds"] >= 1 for h in hot), hs.stdout[:200])
    p = subprocess.run([sys.executable, COLLIDE, "feat-audit", "feat-docs", "--repo", fixture,
                        "--json", "--priors", "--backend", "local"],
                       capture_output=True, text=True, env=env)
    pr = json.loads(p.stdout) if p.stdout.startswith("{") else {}
    check("hotspot prior fires on a pair with NO current collision",
          pr.get("verdict") == "CLEARED"
          and any(x["path"] == "auth.py" for x in pr.get("priors", [])),
          f"verdict={pr.get('verdict')} priors={pr.get('priors')}")
    check("prior evidence scope is labelled (never passed off as fleet)",
          pr.get("priors_backend") in ("local", "databricks"), str(pr.get("priors_backend")))

    # ---------- LIVE WORKTREE (pre-commit radar) ---------------------------
    print("\nLIVE WORKTREE (collisions before anyone commits)")
    wt = fixture + "-wt-checkout"
    if os.path.isdir(wt):
        with open(os.path.join(wt, "subscriptions.py"), "a") as fh:
            fh.write('\n\ndef renew_bulk(tokens, amt):\n'
                     '    """live uncommitted work"""\n'
                     '    return [renew(t, amt) for t in tokens]\n')
        before = subprocess.run(["git", "-C", wt, "status", "--short"],
                                capture_output=True, text=True).stdout
        code, r = run_collide(fixture, "feat-auth", wt)
        rw = {f["entity"] for f in r.get("findings", {}).get("read_write", [])}
        check("collision detected against UNCOMMITTED work", "validate_token" in rw, str(rw))
        check("side is labelled as uncommitted",
              any("uncommitted" in s for s in r.get("live_sides", [])),
              str(r.get("live_sides")))
        after = subprocess.run(["git", "-C", wt, "status", "--short"],
                               capture_output=True, text=True).stdout
        stash = subprocess.run(["git", "-C", wt, "stash", "list"],
                               capture_output=True, text=True).stdout.strip()
        check("agent's working tree is left untouched", before == after,
              f"before={before!r} after={after!r}")
        check("no stash entry is pushed onto the agent's stack", stash == "", stash)
        check("intent skips git's stash-snapshot wording",
              "WIP on" not in json.dumps(r.get("intent", {})), json.dumps(r.get("intent", {}))[:160])
    else:
        check("live worktree present in fixture", False, f"missing {wt}")

    # ---------- BOARD (--all) ----------------------------------------------
    print("\nBOARD (auto-discovered sides in flight)")
    p = subprocess.run([sys.executable, COLLIDE, "--all", "--repo", fixture],
                       capture_output=True, text=True)
    check("board discovers both agent worktrees", "2 sides in flight" in p.stdout,
          p.stdout[:160] + p.stderr[:160])
    check("board reports the red pair and exits 2",
          "READ–WRITE on validate_token" in p.stdout and p.returncode == 2,
          f"exit={p.returncode}")

    # ---------- summary ----------------------------------------------------
    passed = sum(1 for _n, ok, _d in results if ok)
    print(f"\n{'='*58}\n{passed}/{len(results)} checks passed")
    if "--keep" in sys.argv:
        print(f"fixture kept at {fixture}")
    sys.exit(0 if passed == len(results) else 1)


if __name__ == "__main__":
    main()
