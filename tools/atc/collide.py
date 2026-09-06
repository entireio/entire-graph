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
    for f in data.get("files", []):
        for ch in f.get("changes", []):
            key = (f["path"], ch.get("name") or "?")
            ch = dict(ch, path=f["path"], language=f.get("language"))
            changes[key] = ch
    for w in data.get("warnings", []) or []:
        warnings.append(str(w))
    return changes, warnings


def callers_on_tree(worktree, name, define_file):
    """Incoming CALLS for `name` on a side's tree. Returns (callers, unknowns)."""
    try:
        data = graph_json(
            ["neighbors", "--symbol", name, "--direction", "in", "--format", "json"],
            worktree,
        )
    except RuntimeError as e:
        return [], [f"neighbors({name}): {e}"]
    if data.get("disambiguation_required"):
        try:
            data = graph_json(
                ["neighbors", "--symbol", name, "--file", define_file,
                 "--direction", "in", "--format", "json"], worktree)
        except RuntimeError as e:
            return [], [f"neighbors({name}, {define_file}): ambiguous, retry failed: {e}"]
    callers, unknowns = [], []
    for m in data.get("matches", []):
        for edge in m.get("incoming", []):
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
        if subjects:
            return {"source": "commit-message",
                    "text": "; ".join(subjects.splitlines()[:3])}
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


def collide(repo, ref_a, ref_b):
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
                callers, unk = callers_on_tree(wt, name, path)
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

    for red, hit in rw_scan(diff_a, diff_b, ref_b, ref_a, ref_b):
        (findings["read_write"] if red else findings["advisory"]).append(hit)
    for red, hit in rw_scan(diff_b, diff_a, ref_a, ref_b, ref_a):
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
        "repo": repo, "ref_a": ref_a, "ref_b": ref_b, "merge_base": mb,
        "verdict": verdict, "reds": reds, "advisories": advisories,
        "findings": findings, "landing_order": landing, "unknowns": unknowns,
        "intent": {ref_a: side_intent(repo, mb, ref_a),
                   ref_b: side_intent(repo, mb, ref_b)},
    }


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
    if r["landing_order"]:
        L.append(f"✈  LANDING ORDER: {r['landing_order']}")
    for u in r["unknowns"]:
        L.append(f"❔ UNKNOWN: {u}")
    L.append("━" * 66)
    L.append(f"verdict: {r['verdict']}  ({r['reds']} red, {r['advisories']} advisory)")
    return "\n".join(L)


def main():
    ap = argparse.ArgumentParser(description="ATC semantic collision detection")
    ap.add_argument("ref_a")
    ap.add_argument("ref_b")
    ap.add_argument("--repo", default=".")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    try:
        r = collide(os.path.abspath(args.repo), args.ref_a, args.ref_b)
    except RuntimeError as e:
        print(f"ATC error (no verdict — do NOT treat as clean): {e}", file=sys.stderr)
        sys.exit(3)
    print(json.dumps(r, indent=2) if args.json else render(r))
    sys.exit(2 if r["reds"] else (1 if r["advisories"] else 0))


if __name__ == "__main__":
    main()
