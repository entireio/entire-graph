#!/usr/bin/env python3
"""ATC Tower — generate the visual simulation demo (tower.html).

Runs the REAL pipeline end to end and bakes the outputs into a single
self-contained HTML page:

  1. rebuilds the seeded fixture (tools/atc-fixture/build_fixture.sh)
  2. reproduces the trap in a scratch clone: `git merge` reports CLEAN,
     the test suite then explodes — both outputs captured verbatim
  3. runs collide.py --json on the red pair, the control pair, and a
     no-overlap pair with historical priors (telemetry isolated to a
     temp store, three runs recorded first)
  4. captures the --all board and the hotspot leaderboard as terminal text

Nothing on the page is mocked; every number, path, confidence and
traceback is harvested from the run that generated it.

    python3 tools/atc/tower.py [--fixture DIR] [-o tower.html]
"""

import argparse
import datetime
import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
FIXTURE_SCRIPT = os.path.join(HERE, "..", "atc-fixture", "build_fixture.sh")


def run(cmd, cwd=None, env=None, check=True):
    r = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise RuntimeError(f"$ {' '.join(cmd)}\n{r.stderr.strip()}")
    return r


def harvest_trap(fixture):
    """Clean merge, broken runtime — captured from a scratch clone so the
    fixture itself is never disturbed."""
    scratch = tempfile.mkdtemp(prefix="atc-trap-")
    run(["git", "clone", "--quiet", fixture, scratch])
    run(["git", "-C", scratch, "checkout", "-q", "-b", "merge-demo", "origin/main"])
    m1 = run(["git", "-C", scratch, "merge", "--no-ff", "--no-edit", "origin/feat-auth"])
    m2 = run(["git", "-C", scratch, "merge", "--no-ff", "--no-edit", "origin/feat-checkout"])
    t_ok = run(["python3", "run_tests.py"], cwd=scratch)
    t_boom = run(["python3", "tests_checkout.py"], cwd=scratch, check=False)
    if t_boom.returncode == 0:
        raise RuntimeError("trap did not fire: tests_checkout.py passed after merge")
    return {
        "merge_out": (m1.stdout + m2.stdout).strip(),
        "tests_pass_out": t_ok.stdout.strip(),
        "tests_fail_out": (t_boom.stdout + t_boom.stderr).strip(),
        "tests_fail_code": t_boom.returncode,
    }


def collide_json(fixture, a, b, env, extra=()):
    r = run(["python3", os.path.join(HERE, "collide.py"), a, b,
             "--repo", fixture, "--json", *extra], env=env, check=False)
    if r.returncode == 3 or not r.stdout.strip():
        raise RuntimeError(f"collide {a} {b}: no verdict\n{r.stderr}")
    return json.loads(r.stdout), r.returncode


def collide_text(fixture, a, b, env):
    r = run(["python3", os.path.join(HERE, "collide.py"), a, b,
             "--repo", fixture], env=env, check=False)
    return r.stdout.strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixture", default=os.path.join(tempfile.gettempdir(), "atc-fixture"))
    ap.add_argument("-o", "--out", default=os.path.join(HERE, "tower.html"))
    args = ap.parse_args()

    print("[tower] rebuilding fixture ...")
    run(["bash", FIXTURE_SCRIPT, args.fixture])

    # isolate telemetry so the demo's fleet memory is reproducible
    env = dict(os.environ, ATC_LOCAL_DB=os.path.join(
        tempfile.mkdtemp(prefix="atc-tower-db-"), "atc.db"))

    print("[tower] reproducing the trap (clean merge -> broken tests) ...")
    trap = harvest_trap(args.fixture)

    print("[tower] running collider: red pair (recorded 3x for priors) ...")
    red, red_code = collide_json(args.fixture, "feat-auth", "feat-checkout", env,
                                 extra=("--record",))
    for _ in range(2):
        collide_json(args.fixture, "feat-auth", "feat-checkout", env, extra=("--record",))
    red_text = collide_text(args.fixture, "feat-auth", "feat-checkout", env)

    print("[tower] running collider: control pair ...")
    clean, clean_code = collide_json(args.fixture, "feat-logging", "feat-docs", env)

    print("[tower] running collider: no-overlap pair with priors ...")
    prior, prior_code = collide_json(args.fixture, "feat-audit", "feat-docs", env,
                                     extra=("--priors",))

    print("[tower] capturing board + hotspot leaderboard ...")
    board = run(["python3", os.path.join(HERE, "collide.py"), "--all",
                 "--repo", args.fixture], env=env, check=False).stdout.strip()
    hotspots = run(["python3", os.path.join(HERE, "telemetry.py"), "hotspots"],
                   env=env, check=False).stdout.strip()

    data = {
        "generated": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "fixture": args.fixture,
        "trap": trap,
        "pairs": [
            {"id": "red", "a": "feat-auth", "b": "feat-checkout",
             "result": red, "exit_code": red_code, "terminal": red_text},
            {"id": "clean", "a": "feat-logging", "b": "feat-docs",
             "result": clean, "exit_code": clean_code},
            {"id": "prior", "a": "feat-audit", "b": "feat-docs",
             "result": prior, "exit_code": prior_code},
        ],
        "board_text": board,
        "hotspots_text": hotspots,
    }

    with open(os.path.join(HERE, "tower_template.html"), encoding="utf-8") as f:
        template = f.read()
    payload = json.dumps(data).replace("</", "<\\/")
    html = template.replace("/*__ATC_DATA__*/null", payload)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(html)
    print(f"[tower] wrote {args.out}  "
          f"(verdicts: {red['verdict']}/{clean['verdict']}/{prior['verdict']}, "
          f"exit {red_code}/{clean_code}/{prior_code})")
    if not (red_code == 2 and clean_code == 0):
        print("[tower] WARNING: expected exit 2 on red pair and 0 on control pair",
              file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
