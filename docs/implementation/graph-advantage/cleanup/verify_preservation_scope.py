#!/usr/bin/env python3
"""Verify cleanup stays inside the admitted Graph Advantage addition boundary."""
from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import stat
from pathlib import Path


def output(repo: Path, *args: str) -> str:
    return subprocess.check_output(args, cwd=repo, text=True)


def tree(repo: Path, ref: str) -> dict[str, dict]:
    records = {}
    for line in output(repo, "git", "ls-tree", "-rl", "--full-tree", ref).splitlines():
        metadata, path = line.split("\t", 1)
        mode, kind, blob, size = metadata.split()
        if kind == "blob":
            records[path] = {"path": path, "mode": mode, "git_blob": blob, "bytes": int(size)}
    return records


def digest(records: list[dict]) -> str:
    payload = json.dumps(records, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()

def current_mode(path: Path) -> str:
    value=path.lstat().st_mode
    if stat.S_ISLNK(value): return "120000"
    return "100755" if value & 0o111 else "100644"


def admitted(repo: Path, baseline: str, precleanup: str) -> tuple[list[dict], list[dict]]:
    baseline_tree, precleanup_tree = tree(repo, baseline), tree(repo, precleanup)
    added = [precleanup_tree[path] for path in sorted(precleanup_tree.keys() - baseline_tree.keys())]
    modified = [precleanup_tree[path] for path in sorted(precleanup_tree.keys() & baseline_tree.keys()) if precleanup_tree[path]["git_blob"] != baseline_tree[path]["git_blob"] or precleanup_tree[path]["mode"] != baseline_tree[path]["mode"]]
    return added, modified


def verify(repo: Path, scope_path: Path, external_history: bool = False) -> list[str]:
    scope = json.loads(scope_path.read_text())
    errors = []
    fixture = scope.get("boundary_fixture", {})
    fixture_path = repo / fixture.get("path", "")
    if not fixture_path.is_file(): return ["missing preservation boundary fixture"]
    if hashlib.sha256(fixture_path.read_bytes()).hexdigest() != fixture.get("sha256"):
        return ["preservation boundary fixture hash"]
    boundary = json.loads(fixture_path.read_text())
    if boundary.get("schema") != "graph-advantage-preservation-boundary-v1" or boundary.get("baseline_commit") != scope["baseline_commit"] or boundary.get("precleanup_commit") != scope["precleanup_commit"]:
        return ["preservation boundary fixture identity"]
    added, modified = boundary["added"], boundary["modified"]
    if external_history:
        actual_added, actual_modified = admitted(repo, scope["baseline_commit"], scope["precleanup_commit"])
        if actual_added != added: errors.append("external added boundary mismatch")
        if actual_modified != modified: errors.append("external modified boundary mismatch")
    for name, actual in (("added", added), ("modified", modified)):
        expected = scope[f"{name}_boundary"]
        if len(actual) != expected["count"]: errors.append(f"{name} boundary count")
        if digest(actual) != expected["records_sha256"]: errors.append(f"{name} boundary digest")

    for record in modified:
        path = repo / record["path"]
        if not path.is_file():
            errors.append(f"preserved modified missing: {record['path']}")
        elif output(repo, "git", "hash-object", "--", record["path"]).strip() != record["git_blob"] or current_mode(path) != record["mode"]:
            errors.append(f"preserved modified changed: {record['path']}")

    admitted_paths = {x["path"] for x in added}
    added_by_path = {x["path"]: x for x in added}
    approved_post = {x["path"]: x for x in scope["approved_post_precleanup_files"]}
    for path, record in approved_post.items():
        current = repo / path
        if not current.is_file() or output(repo, "git", "hash-object", "--", path).strip() != record["git_blob"] or current_mode(current) != record["mode"]:
            errors.append(f"approved post-precleanup file changed: {path}")
    removal_path = repo / "docs/implementation/graph-advantage/cleanup/removal-map.json"
    removed = set()
    for candidate in json.loads(removal_path.read_text())["candidates"]:
        if candidate.get("disposition") == "applied" and candidate["path"] not in admitted_paths:
            errors.append(f"removed path outside admitted boundary: {candidate['path']}")
        if candidate.get("disposition") == "applied": removed.add(candidate["path"])
    approved_changes = set(scope["approved_cleanup_paths"]) | set(approved_post)
    approved_retained = {x["path"]: x for x in scope["approved_retained_changes"]}
    changed = output(repo, "git", "diff", "--no-renames", "--name-only", scope["baseline_commit"], "--").splitlines()
    changed += output(repo, "git", "ls-files", "--others", "--exclude-standard").splitlines()
    optional_paths = {x["path"] for x in scope["optional_local_artifacts"]}
    for path in changed:
        if path not in admitted_paths and path not in {x["path"] for x in modified} and path not in approved_changes and path not in optional_paths:
            errors.append(f"change outside admitted boundary: {path}")
    for path, record in added_by_path.items():
        current = repo / path
        if path in removed:
            if current.exists(): errors.append(f"applied removal still present: {path}")
        elif path in approved_retained:
            expected=approved_retained[path]
            if not current.is_file() or output(repo,"git","hash-object","--",path).strip()!=expected["git_blob"] or current_mode(current)!=expected["mode"]:
                errors.append(f"approved retained change drifted: {path}")
        elif path not in approved_changes:
            if not current.is_file(): errors.append(f"unapproved admitted deletion: {path}")
            elif output(repo,"git","hash-object","--",path).strip() != record["git_blob"] or current_mode(current)!=record["mode"]:
                errors.append(f"unapproved admitted change: {path}")

    for record in scope["optional_local_artifacts"]:
        path = repo / record["path"]
        if path.exists():
            if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != record["sha256"]:
                errors.append(f"optional local artifact hash: {record['path']}")
            tracked = subprocess.run(["git", "ls-files", "--error-unmatch", "--", record["path"]], cwd=repo, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
            if tracked:
                errors.append(f"optional local artifact became tracked: {record['path']}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--scope", type=Path, default=Path("docs/implementation/graph-advantage/cleanup/preservation-scope.json"))
    parser.add_argument("--external-history", action="store_true", help="also recompute boundaries from the original Git objects")
    args = parser.parse_args(); errors = verify(args.repo.resolve(), args.scope.resolve(), args.external_history)
    if errors:
        print("\n".join(errors)); return 1
    print("preservation scope verified"); return 0


if __name__ == "__main__":
    raise SystemExit(main())
