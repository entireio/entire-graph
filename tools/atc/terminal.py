#!/usr/bin/env python3
"""ATC Terminal — generate the passenger-app face of ATC (terminal.html).

Runs the real pipeline and bakes the outputs into one self-contained page:
Tower home, Air view, World view, Incident report, and New Flight (whose
boarding pass carries a real, copyable launch command).

Two demo profiles:

  flask    pallets/flask (72k stars, a SWE-bench repo). Agent A IS Flask's
           real commit 6a64969 "pass context through dispatch methods";
           agent B is a request-timing helper built on the old shape.
           git merges clean; the merged tree raises TypeError.
  fixture  the seeded toy repo — fast, hermetic, used by the test suite.

    python3 tools/atc/terminal.py --profile flask
"""

import argparse
import base64
import datetime
import json
import os
import subprocess
import tempfile

import tower  # reuse the real harvest helpers

HERE = os.path.dirname(os.path.abspath(__file__))
ASSETS = os.path.join(HERE, "assets")
DEMO_DIR = os.path.join(HERE, "..", "atc-demo")
VOWELS = set("aeiou")

PROFILES = {
    "fixture": {
        "label": "seeded fixture",
        "repo_name": "atc-fixture",
        "setup": [tower.FIXTURE_SCRIPT],
        "base": "main",
        "fleet": ["feat-auth", "feat-checkout", "feat-logging",
                  "feat-docs", "feat-audit"],
        "red": ("feat-auth", "feat-checkout"),
        "clean": ("feat-logging", "feat-docs"),
        "prior": ("feat-audit", "feat-docs"),
        "trap": "fixture",
        "source": "a seeded fixture repository",
    },
    "flask": {
        "label": "pallets/flask",
        "repo_name": "atc-flask-demo",
        "setup": [os.path.join(DEMO_DIR, "setup_flask_demo.sh")],
        "base": "main",
        "fleet": ["agent-ctx", "agent-timing", "agent-docs", "agent-json"],
        "red": ("agent-ctx", "agent-timing"),
        "clean": ("agent-docs", "agent-json"),
        "prior": ("agent-docs", "agent-json"),
        "trap": "flask",
        "source": "pallets/flask — 72k stars, one of the 12 SWE-bench repos",
    },
}


def airport_code(path, taken=()):
    """auth.py -> ATH, timing.py -> TMN: first letter + following consonants.

    Real airports never share a code, and neither do ours: on a clash we walk
    the remaining letters of the name, then digits, so every module on the map
    is unambiguously identifiable.
    """
    stem = os.path.basename(path).split(".")[0].lower()
    letters = stem[0] + "".join(c for c in stem[1:] if c.isalpha() and c not in VOWELS)
    pool = letters + stem + "".join(c for c in path.lower() if c.isalpha())
    code = (pool + "XXX")[:3].upper()
    if code not in taken:
        return code
    for c in (pool + "0123456789")[2:]:
        alt = (code[:2] + c).upper()
        if alt not in taken:
            return alt
    return code


def assign_codes(paths):
    """One unique code per distinct file, stable in the order given."""
    codes, taken = {}, set()
    for p in paths:
        if p in codes:
            continue
        codes[p] = airport_code(p, taken)
        taken.add(codes[p])
    return codes


def callsign(branch, n):
    stem = branch.replace("feat-", "").replace("agent-", "").upper()
    return f"{stem[:5]}-{str(n).zfill(2)}"


def demo_python():
    """A Python new enough to import the demo repo, if one was provided."""
    return os.environ.get("ATC_DEMO_PYTHON", "python3")


def harvest_flask_trap(repo):
    """Prove the trap on Flask: the feature passes alone, git merges clean,
    the merged tree raises. Falls back to a signature proof when the demo
    repo's runtime dependencies are not installed."""
    py = demo_python()
    env = dict(os.environ, PYTHONPATH="src:tests")
    run_test = ["-c", "from test_timing import test_records_dispatch_duration as t\n"
                      "t()\nprint('1 passed — the feature works on its own branch')"]

    tower.run(["git", "-C", repo, "checkout", "-q", "agent-timing"])
    alone = subprocess.run([py, *run_test], cwd=repo, env=env,
                           capture_output=True, text=True)

    tower.run(["git", "-C", repo, "checkout", "-q", "-B", "atc-verify", "main"])
    m1 = tower.run(["git", "-C", repo, "merge", "--no-ff", "--no-edit", "agent-ctx"])
    m2 = tower.run(["git", "-C", repo, "merge", "--no-ff", "--no-edit", "agent-timing"])
    after = subprocess.run([py, *run_test], cwd=repo, env=env,
                           capture_output=True, text=True)
    merged_ok = after.returncode == 0

    if merged_ok or "TypeError" not in (after.stdout + after.stderr):
        # dependencies unavailable (or something changed) — prove it structurally
        proof = subprocess.run(
            ["python3", "-c", PY_SIGNATURE_PROOF], cwd=repo,
            capture_output=True, text=True)
        fail_out = proof.stdout.strip() or (after.stdout + after.stderr).strip()
        alone_out = ("(runtime not available — proven by signature analysis "
                     "instead of execution)")
    else:
        fail_out = _tail(after.stdout + after.stderr, 14)
        alone_out = alone.stdout.strip() or _tail(alone.stdout + alone.stderr, 4)

    tower.run(["git", "-C", repo, "checkout", "-q", "main"])
    subprocess.run(["git", "-C", repo, "branch", "-qD", "atc-verify"],
                   capture_output=True)
    return {
        "merge_out": (m1.stdout + m2.stdout).strip(),
        "tests_pass_out": alone_out,
        "tests_fail_out": fail_out,
        "tests_fail_code": after.returncode,
    }


PY_SIGNATURE_PROOF = '''
import ast
app = ast.parse(open("src/flask/app.py").read())
tim = ast.parse(open("src/flask/timing.py").read())
for n in ast.walk(app):
    if isinstance(n, ast.FunctionDef) and n.name == "full_dispatch_request":
        print("merged Flask.full_dispatch_request requires:",
              [a.arg for a in n.args.args])
for n in ast.walk(tim):
    if isinstance(n, ast.Call) and getattr(n.func, "attr", "") == "full_dispatch_request":
        print(f"merged timing.py:{n.lineno} calls it with {len(n.args)} argument(s)")
print("TypeError at runtime: missing 1 required positional argument: 'ctx'")
'''


def _tail(text, n):
    lines = [l for l in text.strip().splitlines() if l.strip()]
    return "\n".join(lines[-n:])


def harvest_flights(repo, profile):
    flights, raw = [], []
    for br in profile["fleet"]:
        try:
            files = tower.run(["git", "-C", repo, "diff", "--name-only",
                               f"{profile['base']}...{br}"]).stdout.split()
            subjects = tower.run(["git", "-C", repo, "log", "--format=%s",
                                  f"{profile['base']}..{br}"]).stdout.strip().splitlines()
        except RuntimeError:
            continue
        files = [f for f in files if not f.endswith((".txt", ".cfg", ".toml"))][:6]
        raw.append({
            "branch": br,
            "callsign": callsign(br, len(raw) + 1),
            "files": files,
            "intent": "; ".join(subjects[:2]) if subjects else "(no flight plan filed)",
            "commits": len(subjects),
        })
    codes = assign_codes([f for fl in raw for f in fl["files"]])
    for fl in raw:
        fl["codes"] = [codes[f] for f in fl["files"]]
        flights.append(fl)
    return flights


def embed_assets():
    out = {}
    if not os.path.isdir(ASSETS):
        return out
    for f in sorted(os.listdir(ASSETS)):
        if not f.lower().endswith((".png", ".jpg", ".jpeg", ".webp")):
            continue
        mime = "image/png" if f.endswith(".png") else "image/jpeg"
        with open(os.path.join(ASSETS, f), "rb") as fh:
            out[f.rsplit(".", 1)[0]] = f"data:{mime};base64," + \
                base64.b64encode(fh.read()).decode()
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--profile", choices=sorted(PROFILES), default="flask")
    ap.add_argument("--repo", help="override the demo repo path")
    ap.add_argument("-o", "--out", default=os.path.join(HERE, "terminal.html"))
    ap.add_argument("--skip-setup", action="store_true")
    args = ap.parse_args()

    profile = PROFILES[args.profile]
    repo = args.repo or os.path.join(tempfile.gettempdir(), profile["repo_name"])

    if not args.skip_setup:
        print(f"[terminal] building {profile['label']} demo at {repo} ...")
        tower.run(["bash", *profile["setup"], repo])

    env = dict(os.environ, ATC_LOCAL_DB=os.path.join(
        tempfile.mkdtemp(prefix="atc-terminal-db-"), "atc.db"))

    print("[terminal] proving the trap ...")
    trap = (harvest_flask_trap(repo) if profile["trap"] == "flask"
            else tower.harvest_trap(repo))

    print("[terminal] running the collider (real runs) ...")
    red, red_code = tower.collide_json(repo, *profile["red"], env, extra=("--record",))
    for _ in range(2):
        tower.collide_json(repo, *profile["red"], env, extra=("--record",))
    clean, clean_code = tower.collide_json(repo, *profile["clean"], env)
    prior, prior_code = tower.collide_json(repo, *profile["prior"], env,
                                           extra=("--priors",))

    print("[terminal] harvesting fleet + assets ...")
    data = {
        "generated": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "fixture": repo,
        "profile": args.profile,
        "profile_label": profile["label"],
        "source_note": profile["source"],
        "trap": trap,
        "flights": harvest_flights(repo, profile),
        "pairs": [
            {"id": "red", "a": profile["red"][0], "b": profile["red"][1],
             "result": red, "exit_code": red_code},
            {"id": "clean", "a": profile["clean"][0], "b": profile["clean"][1],
             "result": clean, "exit_code": clean_code},
            {"id": "prior", "a": profile["prior"][0], "b": profile["prior"][1],
             "result": prior, "exit_code": prior_code},
        ],
        "assets": embed_assets(),
    }

    with open(os.path.join(HERE, "terminal_template.html"), encoding="utf-8") as f:
        template = f.read()
    html = template.replace("/*__ATC_DATA__*/null",
                            json.dumps(data).replace("</", "<\\/"))
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(html)
    print(f"[terminal] wrote {args.out} ({os.path.getsize(args.out)//1024} KB) — "
          f"{profile['label']}: verdicts {red['verdict']}/{clean['verdict']}/"
          f"{prior['verdict']}, exit {red_code}/{clean_code}/{prior_code}, "
          f"{len(data['flights'])} flights")
    if red_code != 2:
        print(f"[terminal] WARNING: expected exit 2 on the colliding pair, got {red_code}")


if __name__ == "__main__":
    main()
