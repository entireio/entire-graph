#!/usr/bin/env python3
"""Verify cleanup stays inside the admitted Graph Advantage addition boundary."""
from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
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


def admitted(repo: Path, baseline: str, precleanup: str) -> tuple[list[dict], list[dict]]:
    baseline_tree, precleanup_tree = tree(repo, baseline), tree(repo, precleanup)
    added = [precleanup_tree[path] for path in sorted(precleanup_tree.keys() - baseline_tree.keys())]
    modified = [precleanup_tree[path] for path in sorted(precleanup_tree.keys() & baseline_tree.keys()) if precleanup_tree[path]["git_blob"] != baseline_tree[path]["git_blob"] or precleanup_tree[path]["mode"] != baseline_tree[path]["mode"]]
    return added, modified


def verify(repo: Path, scope_path: Path) -> list[str]:
    scope = json.loads(scope_path.read_text())
    added, modified = admitted(repo, scope["baseline_commit"], scope["precleanup_commit"])
    errors = []
    for name, actual in (("added", added), ("modified", modified)):
        expected = scope[f"{name}_boundary"]
        if len(actual) != expected["count"]: errors.append(f"{name} boundary count")
        if digest(actual) != expected["records_sha256"]: errors.append(f"{name} boundary digest")

    pre_tree = tree(repo, scope["precleanup_commit"])
    for record in modified:
        path = repo / record["path"]
        if not path.is_file():
            errors.append(f"preserved modified missing: {record['path']}")
        elif output(repo, "git", "hash-object", "--", record["path"]).strip() != record["git_blob"]:
            errors.append(f"preserved modified changed: {record['path']}")

    admitted_paths = {x["path"] for x in added}
    approved_post = {x["path"]: x for x in scope["approved_post_precleanup_files"]}
    for path, record in approved_post.items():
        current = repo / path
        if not current.is_file() or output(repo, "git", "hash-object", "--", path).strip() != record["git_blob"]:
            errors.append(f"approved post-precleanup file changed: {path}")
    allowed_new_prefix = "docs/implementation/graph-advantage/cleanup/"
    allowed_new_exact = {".github/workflows/graph-advantage-evidence.yml", "docs/implementation/graph-advantage/evidence/.gitignore"}
    changed = output(repo, "git", "diff", "--no-renames", "--name-only", scope["precleanup_commit"], "--").splitlines()
    for path in changed:
        if path not in admitted_paths and path not in approved_post and not path.startswith(allowed_new_prefix) and path not in allowed_new_exact:
            errors.append(f"change outside admitted boundary: {path}")

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
    args = parser.parse_args(); errors = verify(args.repo.resolve(), args.scope.resolve())
    if errors:
        print("\n".join(errors)); return 1
    print("preservation scope verified"); return 0


if __name__ == "__main__":
    raise SystemExit(main())
