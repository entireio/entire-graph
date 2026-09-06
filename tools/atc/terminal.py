#!/usr/bin/env python3
"""ATC Terminal — generate the passenger-app face of ATC (terminal.html).

Same live data as tower.py (which it imports), rendered as a light,
minimal, flight-app experience: Tower home, Air view (flight cards),
World view (route map), Incident report, and New Flight (booking flow
whose boarding pass carries a real, copyable launch command).

    python3 tools/atc/terminal.py [--fixture DIR] [-o terminal.html]
"""

import argparse
import base64
import datetime
import json
import os
import tempfile

import tower  # same directory: reuse the real harvest, no re-invention

HERE = os.path.dirname(os.path.abspath(__file__))
ASSETS = os.path.join(HERE, "assets")
VOWELS = set("aeiou")

FLEET = ["feat-auth", "feat-checkout", "feat-logging", "feat-docs", "feat-audit"]


def airport_code(path):
    """auth.py -> ATH, config.py -> CNF: first letter + following consonants."""
    stem = os.path.basename(path).split(".")[0].lower()
    letters = stem[0] + "".join(c for c in stem[1:] if c.isalpha() and c not in VOWELS)
    return (letters + stem)[:3].upper()


def harvest_flights(fixture):
    flights = []
    for br in FLEET:
        try:
            files = tower.run(["git", "-C", fixture, "diff", "--name-only",
                               f"main...{br}"]).stdout.split()
            subjects = tower.run(["git", "-C", fixture, "log", "--format=%s",
                                  f"main..{br}"]).stdout.strip().splitlines()
        except RuntimeError:
            continue
        flights.append({
            "branch": br,
            "callsign": br.replace("feat-", "").upper()[:5] + "-" + str(len(flights) + 1).zfill(2),
            "files": files,
            "codes": [airport_code(f) for f in files],
            "intent": "; ".join(subjects[:2]) if subjects else "(no flight plan filed)",
            "commits": len(subjects),
        })
    return flights


def embed_assets():
    out = {}
    if not os.path.isdir(ASSETS):
        return out
    for f in sorted(os.listdir(ASSETS)):
        p = os.path.join(ASSETS, f)
        if not f.lower().endswith((".png", ".jpg", ".jpeg", ".webp")):
            continue
        mime = "image/png" if f.endswith(".png") else "image/jpeg"
        with open(p, "rb") as fh:
            out[f.rsplit(".", 1)[0]] = f"data:{mime};base64," + \
                base64.b64encode(fh.read()).decode()
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixture", default=os.path.join(tempfile.gettempdir(), "atc-fixture"))
    ap.add_argument("-o", "--out", default=os.path.join(HERE, "terminal.html"))
    ap.add_argument("--skip-fixture", action="store_true",
                    help="reuse an already-built fixture")
    args = ap.parse_args()

    if not args.skip_fixture:
        print("[terminal] rebuilding fixture ...")
        tower.run(["bash", tower.FIXTURE_SCRIPT, args.fixture])

    env = dict(os.environ, ATC_LOCAL_DB=os.path.join(
        tempfile.mkdtemp(prefix="atc-terminal-db-"), "atc.db"))

    print("[terminal] harvesting trap + verdicts (real runs) ...")
    trap = tower.harvest_trap(args.fixture)
    red, red_code = tower.collide_json(args.fixture, "feat-auth", "feat-checkout",
                                       env, extra=("--record",))
    for _ in range(2):
        tower.collide_json(args.fixture, "feat-auth", "feat-checkout", env,
                           extra=("--record",))
    clean, clean_code = tower.collide_json(args.fixture, "feat-logging", "feat-docs", env)
    prior, prior_code = tower.collide_json(args.fixture, "feat-audit", "feat-docs", env,
                                           extra=("--priors",))
    print("[terminal] harvesting fleet + assets ...")
    data = {
        "generated": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "fixture": args.fixture,
        "trap": trap,
        "flights": harvest_flights(args.fixture),
        "pairs": [
            {"id": "red", "a": "feat-auth", "b": "feat-checkout",
             "result": red, "exit_code": red_code},
            {"id": "clean", "a": "feat-logging", "b": "feat-docs",
             "result": clean, "exit_code": clean_code},
            {"id": "prior", "a": "feat-audit", "b": "feat-docs",
             "result": prior, "exit_code": prior_code},
        ],
        "assets": embed_assets(),
    }

    with open(os.path.join(HERE, "terminal_template.html"), encoding="utf-8") as f:
        template = f.read()
    payload = json.dumps(data).replace("</", "<\\/")
    html = template.replace("/*__ATC_DATA__*/null", payload)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(html)
    size_kb = os.path.getsize(args.out) // 1024
    print(f"[terminal] wrote {args.out} ({size_kb} KB) — "
          f"verdicts {red['verdict']}/{clean['verdict']}/{prior['verdict']}, "
          f"exit {red_code}/{clean_code}/{prior_code}, "
          f"{len(data['flights'])} flights, {len(data['assets'])} assets")


if __name__ == "__main__":
    main()
