#!/usr/bin/env python3
"""ATC — Agent Traffic Control: semantic collision detection between two
parallel work streams (branches / agent sessions), powered by Entire Graph.

    collide.py --repo <path> <refA> <refB> [--json]

Pipeline (see ATC_PLAN.md):
  COLLECTOR   merge-base + temp worktrees per side (a side's live tree)
  DIFFER      `entire graph diff`  -> changed entities per side
  IMPACTOR    `entire graph neighbors --direction in` on the OTHER side's tree
  INTERSECTOR WRITE-WRITE / READ-WRITE / PROXIMITY / UNKNOWN
  ADJUDICATOR severity + landing order
  REPORTER    verdict card, --json, exit code (0 clear / 1 advisory / 2 red)

Severity policy (false positives are fatal):
  RED   READ-WRITE where the changed side altered a SIGNATURE (or removed/
        renamed the entity) and the other side has new/changed dependents.
  RED   WRITE-WRITE: both sides changed the same entity.
  ADVISORY  body-only change with new dependents on the other side
            ("behavior drift risk"), and same-file PROXIMITY.
  UNKNOWN   anything the graph could not resolve is listed, never dropped.
"""

import argparse
import json
import subprocess
import sys
import tempfile
import shutil
import os

RED_CHANGE_TYPES = {"signature_changed", "removed", "renamed"}


def sh(cmd, cwd=None):
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if r.returncode != 0:
        raise RuntimeError(f"$ {' '.join(cmd)}\n{r.stderr.strip()}")
    return r.stdout


def graph_json(args_list, repo):
    out = sh(["entire", "graph"] + args_list + ["--repo", repo])
    return json.loads(out)


def graph_diff(repo, base, head):
    """Changed entities for one side: {(path, name): change-dict}."""
    data = graph_json(["diff", "--base", base, "--head", head, "--json"], repo)
    changes, warnings = {}, []
    # the graph emits JSON null (not []) for an empty section — `or []` is
    # required; .get(k, []) returns None when the key exists with a null value
    for f in (data.get("files") or []):
        for ch in (f.get("changes") or []):
            key = (f["path"], ch.get("name") or "?")
            ch = dict(ch, path=f["path"], language=f.get("language"))
            changes[key] = ch
    for w in data.get("warnings", []) or []:
        warnings.append(str(w))
    return changes, warnings


def callers_on_tree(worktree, name, define_file, define_line=None):
    """Incoming CALLS for one definition on a side's tree.

    Resolution is by <file>:<line> whenever the diff gave us a location, then
    by name+file, and only then by bare name. Bare names are ambiguous across a
    large polyglot repo (our own semantic-diff review showed common names such
    as `main` and `execute` attracting hundreds of unrelated dependents), and a
    false red is worse than a missed one.
    """
    attempts = []
    if define_line:
        attempts.append(["--symbol", f"{define_file}:{define_line}"])
    attempts.append(["--symbol", name, "--file", define_file])
    attempts.append(["--symbol", name])
    data, unknowns = None, []
    for sel in attempts:
        try:
            data = graph_json(["neighbors", *sel, "--direction", "in",
                               "--format", "json"], worktree)
        except RuntimeError as e:
            unknowns.append(f"neighbors({' '.join(sel)}): {e}")
            continue
        if not data.get("disambiguation_required"):
            break
        unknowns.append(f"neighbors({' '.join(sel)}): ambiguous")
        data = None
    if data is None:
        return [], unknowns
    callers = []
    for m in (data.get("matches") or []):
        for edge in (m.get("incoming") or []):
            ep = edge.get("endpoint", {})
            callers.append({
                "name": ep.get("name"),
                "path": ep.get("file_path"),
                "line": (edge.get("call_site") or {}).get("line"),
                "confidence": edge.get("confidence"),
                "resolution": edge.get("resolution"),
            })
    for pf in data.get("partial_failures", []) or []:
        unknowns.append(f"neighbors({name}): partial failure: {pf}")
    return callers, unknowns


def resolve_side(repo, spec):
    """A side may be a ref, or a WORKTREE PATH with uncommitted work.

    Agents collide before anybody commits, so a tool that only reads commits
    is looking too late. For a worktree path we snapshot the live working tree
    with `git stash create` — it writes a commit object into the shared object
    store and does NOT touch the agent's files, index, or stash list. If the
    tree is clean, its HEAD is used.

    Returns (ref-or-sha, human label, is_live).
    """
    if not os.path.isdir(spec):
        return spec, spec, False
    wt = os.path.abspath(spec)
    try:
        sh(["git", "-C", wt, "rev-parse", "--git-dir"])
    except RuntimeError:
        return spec, spec, False
    label = os.path.basename(wt.rstrip("/"))
    snap = sh(["git", "-C", wt, "stash", "create"]).strip()
    if snap:
        # make the snapshot reachable from the analysing repo
        sh(["git", "-C", repo, "update-ref", f"refs/atc/live/{label}", snap])
        return snap, f"{label} (uncommitted)", True
    head = sh(["git", "-C", wt, "rev-parse", "HEAD"]).strip()
    branch = sh(["git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD"]).strip()
    return head, f"{label} ({branch}, clean)", False


def side_intent(repo, base, ref):
    """What was this side TRYING to do? Tiered, and the source is always labelled.

    1. Entire Checkpoint intent for the head commit (richest: the agent session's
       own stated intent).
    2. Commit subjects between merge-base and head (always available).
    Never guesses: if neither yields anything, returns source "none".
    """
    try:
        out = sh(["entire", "checkpoint", "explain", "--commit", ref, "--json"], cwd=repo)
        env = json.loads(out)
        cp = env if isinstance(env, dict) else {}
        for key in ("intent", "summary", "title"):
            val = (cp.get(key) or cp.get("checkpoint", {}).get(key)) if cp else None
            if isinstance(val, str) and val.strip():
                return {"source": "checkpoint", "text": val.strip(),
                        "checkpoint_id": cp.get("id") or cp.get("checkpoint", {}).get("id")}
    except (RuntimeError, json.JSONDecodeError, AttributeError):
        pass
    try:
        subjects = sh(["git", "-C", repo, "log", "--format=%s", f"{base}..{ref}"]).strip()
        # drop git's own stash-snapshot subjects; they describe the snapshot,
        # not what the session is trying to do
        lines = [s for s in subjects.splitlines()
                 if not s.startswith(("WIP on ", "index on ", "untracked files on "))]
        if lines:
            return {"source": "commit-message", "text": "; ".join(lines[:3])}
    except RuntimeError:
        pass
    return {"source": "none", "text": None}


class TempWorktree:
    def __init__(self, repo, ref):
        self.repo, self.ref = repo, ref
        self.path = tempfile.mkdtemp(prefix=f"atc-{ref.replace('/', '_')}-")

    def __enter__(self):
        os.rmdir(self.path)
        sh(["git", "-C", self.repo, "worktree", "add", "--detach", self.path, self.ref])
        return self.path

    def __exit__(self, *a):
        subprocess.run(["git", "-C", self.repo, "worktree", "remove", "--force", self.path],
                       capture_output=True)
        shutil.rmtree(self.path, ignore_errors=True)


def collide(repo, spec_a, spec_b):
    ref_a, label_a, live_a = resolve_side(repo, spec_a)
    ref_b, label_b, live_b = resolve_side(repo, spec_b)
    mb = sh(["git", "-C", repo, "merge-base", ref_a, ref_b]).strip()
    diff_a, warn_a = graph_diff(repo, mb, ref_a)
    diff_b, warn_b = graph_diff(repo, mb, ref_b)
    unknowns = warn_a + warn_b

    findings = {"write_write": [], "read_write": [], "advisory": [], "proximity": []}

    # ---- WRITE-WRITE: same entity changed on both sides -------------------
    ww_keys = set(diff_a) & set(diff_b)
    for path, name in sorted(ww_keys):
        findings["write_write"].append({
            "entity": name, "path": path,
            "a": diff_a[(path, name)]["type"], "b": diff_b[(path, name)]["type"],
        })

    # ---- READ-WRITE: changed on one side, depended-on by the other --------
    def rw_scan(changed, other_diff, other_ref, label_changed, label_dependent):
        hits = []
        with TempWorktree(repo, other_ref) as wt:
            for (path, name), ch in sorted(changed.items()):
                if (path, name) in ww_keys:
                    continue  # already flagged WW
                if ch.get("kind") not in (None, "function", "method", "class", "type"):
                    continue
                if ch.get("type") == "added":
                    continue  # new entity: the other side cannot depend on it yet
                callers, unk = callers_on_tree(
                    wt, name, path, ch.get("before_start_line"))
                unknowns.extend(unk)
                dependents = [c for c in callers
                              if (c["path"], c["name"]) in other_diff]
                if not dependents:
                    continue
                hit = {
                    "entity": name, "path": path,
                    "changed_by": label_changed, "change_type": ch["type"],
                    "old_signature": ch.get("old_signature"),
                    "new_signature": ch.get("new_signature"),
                    "dependents_side": label_dependent,
                    "dependents": dependents,
                }
                hits.append((ch["type"] in RED_CHANGE_TYPES, hit))
        return hits

    for red, hit in rw_scan(diff_a, diff_b, ref_b, label_a, label_b):
        (findings["read_write"] if red else findings["advisory"]).append(hit)
    for red, hit in rw_scan(diff_b, diff_a, ref_a, label_b, label_a):
        (findings["read_write"] if red else findings["advisory"]).append(hit)

    # ---- PROXIMITY: same file, different entities -------------------------
    files_a = {p for (p, _n) in diff_a}
    files_b = {p for (p, _n) in diff_b}
    for path in sorted((files_a & files_b)):
        ents_a = sorted(n for (p, n) in diff_a if p == path and (p, n) not in ww_keys)
        ents_b = sorted(n for (p, n) in diff_b if p == path and (p, n) not in ww_keys)
        if ents_a and ents_b:
            findings["proximity"].append({"path": path, "a": ents_a, "b": ents_b})

    # ---- ADJUDICATOR: landing order ---------------------------------------
    landing = None
    if findings["read_write"]:
        first = findings["read_write"][0]
        changed_side, dep_side = first["changed_by"], first["dependents_side"]
        n = len(first["dependents"])
        landing = (f"land {changed_side} first -> {dep_side} rebases and must update "
                   f"{n} dependent call site(s) to the new shape BEFORE landing. "
                   f"Reverse order merges green, then {changed_side} silently breaks "
                   f"{dep_side}'s shipped code.")

    reds = len(findings["write_write"]) + len(findings["read_write"])
    advisories = len(findings["advisory"]) + len(findings["proximity"])
    verdict = "HOLD" if reds else ("CAUTION" if advisories else "CLEARED")
    return {
        "repo": repo, "ref_a": label_a, "ref_b": label_b, "merge_base": mb,
        "resolved_a": ref_a, "resolved_b": ref_b,
        "live_sides": [l for l, is_live in ((label_a, live_a), (label_b, live_b)) if is_live],
        "verdict": verdict, "reds": reds, "advisories": advisories,
        "findings": findings, "landing_order": landing, "unknowns": unknowns,
        "intent": {label_a: side_intent(repo, mb, ref_a),
                   label_b: side_intent(repo, mb, ref_b)},
    }


def attach_priors(r, backend=None):
    """Fleet memory -> pre-collision warning.

    Asks the telemetry store how often parallel work on these paths has ended
    in a red finding before. This fires on HISTORY, so it can warn about a
    contended area even when the current pair shows no overlap at all — the
    part a purely local, single-run analysis cannot do.
    """
    try:
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import telemetry
    except ImportError:
        return
    paths = sorted({p for side in ("write_write", "read_write", "advisory", "proximity")
                    for f in r["findings"][side] for p in [f.get("path")] if p}
                   | {p for (p, _n) in []})
    # also consider every path either side touched, not just colliding ones
    try:
        for ref in (r["resolved_a"], r["resolved_b"]):
            d, _w = graph_diff(r["repo"], r["merge_base"], ref)
            paths = sorted(set(paths) | {p for (p, _n) in d})
    except RuntimeError:
        pass
    if not paths:
        return
    try:
        be = telemetry.get_backend(backend)
    except Exception as e:
        r["priors_error"] = str(e)
        return
    try:
        telemetry.init(be)
        r["priors"] = telemetry.priors(be, paths)
        r["priors_backend"] = be.name
        r["priors_scope"] = be.evidence_scope
    except Exception as e:
        r["priors_error"] = str(e)
    finally:
        be.close()


def render(r):
    L = []
    L.append(f"🗼 ATC — Agent Traffic Control   {r['ref_a']} ✈ {r['ref_b']}")
    L.append("━" * 66)
    for ref in (r["ref_a"], r["ref_b"]):
        it = r.get("intent", {}).get(ref, {})
        if it.get("text"):
            src = {"checkpoint": "checkpoint intent",
                   "commit-message": "commit msg"}.get(it["source"], it["source"])
            L.append(f"   {ref} is trying to: {it['text']}  [{src}]")
    if any(r.get("intent", {}).get(x, {}).get("text") for x in (r["ref_a"], r["ref_b"])):
        L.append("")
    for f in r["findings"]["write_write"]:
        L.append(f"🔴 WRITE–WRITE  {f['entity']}  ({f['path']})")
        L.append(f"   {r['ref_a']}: {f['a']}   ·   {r['ref_b']}: {f['b']}")
        L.append("   Both sessions changed the same entity. Review together.")
    for f in r["findings"]["read_write"]:
        L.append(f"🔴 READ–WRITE   {f['entity']}  ({f['path']})")
        if f.get("old_signature"):
            L.append(f"   {f['changed_by']} changed signature:")
            L.append(f"     {f['old_signature']}  →  {f['new_signature']}")
        else:
            L.append(f"   {f['changed_by']}: {f['change_type']}")
        L.append(f"   {f['dependents_side']} depends on it at:")
        for d in f["dependents"]:
            conf = f"  (confidence {d['confidence']})" if d.get("confidence") else ""
            L.append(f"     · {d['name']}()  {d['path']}:{d['line']}{conf}")
        L.append("   Git merges this cleanly. The merged tree is broken "
                 "(build error in compiled languages, runtime error in dynamic ones).")
    for f in r["findings"]["advisory"]:
        deps = ", ".join(f"{d['name']}() {d['path']}:{d['line']}" for d in f["dependents"])
        L.append(f"🟡 BEHAVIOR DRIFT  {f['entity']} ({f['path']}) — "
                 f"{f['changed_by']} changed its body; {f['dependents_side']} "
                 f"has changed/new dependents: {deps}")
    for f in r["findings"]["proximity"]:
        L.append(f"🟡 PROXIMITY  {f['path']} — {r['ref_a']}: {', '.join(f['a'])}"
                 f"  ·  {r['ref_b']}: {', '.join(f['b'])}")
    for p in r.get("priors", []):
        L.append(f"📊 HOTSPOT PRIOR  {p['path']} — {p['red_findings']} red finding(s) "
                 f"across {p['runs_touching']} prior runs touching it "
                 f"(rate {p['rate']}). Sequence work here rather than parallelising it.")
    if r.get("priors"):
        L.append(f"   prior evidence scope: {r.get('priors_scope', 'unknown')} "
                 f"[backend: {r.get('priors_backend', '?')}]")
    if r.get("priors_error"):
        L.append(f"❔ UNKNOWN: priors unavailable ({r['priors_error']}) — "
                 f"no historical claim made.")
    if r["landing_order"]:
        L.append(f"✈  LANDING ORDER: {r['landing_order']}")
    for u in r["unknowns"]:
        L.append(f"❔ UNKNOWN: {u}")
    L.append("━" * 66)
    L.append(f"verdict: {r['verdict']}  ({r['reds']} red, {r['advisories']} advisory)")
    return "\n".join(L)


def discover_sides(repo, include_branches=False):
    """Everything currently in flight: linked worktrees (an agent each), and
    optionally local branches that have diverged from the default branch."""
    sides = []
    out = sh(["git", "-C", repo, "worktree", "list", "--porcelain"])
    # realpath both sides: macOS reports /private/var while callers pass /var
    main_wt = os.path.realpath(repo)
    for block in out.strip().split("\n\n"):
        path = next((l.split(" ", 1)[1] for l in block.splitlines()
                     if l.startswith("worktree ")), None)
        if path and os.path.realpath(path) != main_wt:
            sides.append(path)
    if include_branches:
        cur = sh(["git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD"]).strip()
        for b in sh(["git", "-C", repo, "branch", "--format=%(refname:short)"]).split():
            if b != cur and b not in sides:
                sides.append(b)
    return sides


def run_board(repo, sides, args):
    """Pairwise board across every in-flight side."""
    print(f"🗼 ATC — {len(sides)} sides in flight: {', '.join(os.path.basename(s.rstrip('/')) for s in sides)}")
    print("━" * 66)
    worst, rows = 0, []
    for i in range(len(sides)):
        for j in range(i + 1, len(sides)):
            try:
                r = collide(repo, sides[i], sides[j])
            except RuntimeError as e:
                rows.append((f"{sides[i]} ✈ {sides[j]}", "NO VERDICT", str(e)[:60]))
                worst = max(worst, 3)
                continue
            if args.priors:
                attach_priors(r, args.backend)
            icon = {"HOLD": "🔴", "CAUTION": "🟡", "CLEARED": "🟢"}[r["verdict"]]
            detail = ""
            if r["findings"]["read_write"]:
                f = r["findings"]["read_write"][0]
                detail = f"READ–WRITE on {f['entity']} ({len(f['dependents'])} call site(s))"
            elif r["findings"]["write_write"]:
                detail = f"WRITE–WRITE on {r['findings']['write_write'][0]['entity']}"
            elif r["advisories"]:
                detail = f"{r['advisories']} advisory"
            rows.append((f"{icon} {r['ref_a']} ✈ {r['ref_b']}", r["verdict"], detail))
            worst = max(worst, 2 if r["reds"] else (1 if r["advisories"] else 0))
            if r["reds"] and r["landing_order"]:
                rows.append(("   ✈ landing order", "", r["landing_order"][:110]))
    for a, b, c in rows:
        print(f"{a:<46} {b:<9} {c}")
    print("━" * 66)
    print({0: "🟢 all clear", 1: "🟡 advisories only", 2: "🔴 hold — see reds above",
           3: "❔ incomplete board"}[worst])
    return worst


def main():
    ap = argparse.ArgumentParser(description="ATC semantic collision detection")
    ap.add_argument("side_a", nargs="?",
                    help="branch/ref, or a worktree path for live uncommitted work")
    ap.add_argument("side_b", nargs="?",
                    help="branch/ref, or a worktree path for live uncommitted work")
    ap.add_argument("--all", action="store_true",
                    help="board across every in-flight worktree (pairwise)")
    ap.add_argument("--branches", action="store_true",
                    help="with --all, also include local branches")
    ap.add_argument("--repo", default=".")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--priors", action="store_true",
                    help="enrich with historical contention priors from telemetry")
    ap.add_argument("--record", action="store_true",
                    help="record this verdict to the telemetry store")
    ap.add_argument("--backend", choices=["local", "databricks"], default=None)
    args = ap.parse_args()
    repo = os.path.abspath(args.repo)

    if args.all:
        sides = discover_sides(repo, args.branches)
        if len(sides) < 2:
            print("ATC: fewer than two sides in flight — nothing to sequence "
                  f"(found: {sides or 'none'}). Use --branches to include local branches.")
            sys.exit(0)
        sys.exit(run_board(repo, sides, args))
    if not (args.side_a and args.side_b):
        ap.error("give two sides, or --all")

    try:
        r = collide(repo, args.side_a, args.side_b)
    except RuntimeError as e:
        print(f"ATC error (no verdict — do NOT treat as clean): {e}", file=sys.stderr)
        sys.exit(3)
    if args.priors:
        attach_priors(r, args.backend)
    if args.record:
        try:
            sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
            import telemetry
            be = telemetry.get_backend(args.backend)
            telemetry.init(be)
            r["recorded"] = telemetry.record(be, r)
            be.close()
        except Exception as e:
            r["record_error"] = str(e)
            print(f"[atc] telemetry record failed: {e}", file=sys.stderr)
    print(json.dumps(r, indent=2) if args.json else render(r))
    sys.exit(2 if r["reds"] else (1 if r["advisories"] else 0))


if __name__ == "__main__":
    main()
