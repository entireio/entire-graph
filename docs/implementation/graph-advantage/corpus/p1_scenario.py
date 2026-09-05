#!/usr/bin/env python3
"""Apply and reset the reversible P1 corpus scenarios.

Usage: p1_scenario.py apply|reset|digest REPOSITORY [SCENARIO]
The repository argument is an id from corpus-manifest.json or an explicit path.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
from pathlib import Path

HERE = Path(__file__).resolve().parent
MANIFEST = HERE / "corpus-manifest.json"
DEST = Path("/Users/thomi/Projects/graph-advantage-p1-corpus")


def run(*args: str, cwd: Path, check: bool = True) -> str:
    p = subprocess.run(args, cwd=cwd, check=check, text=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return p.stdout.strip()


def repo_path(value: str) -> tuple[str, Path, dict]:
    data = json.loads(MANIFEST.read_text())
    for record in data["repositories"]:
        if record["id"] == value:
            return record["id"], DEST / value, record
    p = Path(value).resolve()
    return p.name, p, next(r for r in data["repositories"] if r["id"] == p.name)


def marker(path: str, text: str) -> str:
    s = Path(path).suffix.lower()
    return (f"# {text}\n" if s in {".py", ".sh", ".yaml", ".yml", ".toml"}
            else f"<!-- {text} -->\n" if s in {".html", ".xml", ".vue"}
            else f"// {text}\n")


def append_overlay(root: Path, rel: str, text: str) -> None:
    p = root / rel
    with p.open("ab") as f:
        if p.stat().st_size and not p.read_bytes().endswith(b"\n"):
            f.write(b"\n")
        f.write(marker(rel, text).encode())


def manifest_path(root: Path) -> str | None:
    candidates = ["go.mod", "go.work", "package.json", "pyproject.toml",
                  "setup.py", "requirements.txt", "Pipfile", "Cargo.toml"]
    for c in candidates:
        if (root / c).is_file():
            return c
    return None


def reset(root: Path, record: dict) -> None:
    baseline = record.get("fixture_commit", record["commit"])
    run("git", "reset", "--hard", baseline, cwd=root)
    run("git", "clean", "-fd", cwd=root)
    branches = run("git", "for-each-ref", "--format=%(refname:short)",
                   "refs/heads/p1-scenario", cwd=root, check=False)
    if branches:
        run("git", "branch", "-D", "p1-scenario", cwd=root, check=False)
    run("git", "checkout", "--detach", baseline, cwd=root)


def apply(root: Path, record: dict, scenario: str) -> dict:
    reset(root, record)
    paths = record["selected_paths"]
    if scenario == "cold" or scenario == "unchanged":
        pass
    elif scenario in {"one-edit", "ten-edit"}:
        selected = paths[:1] if scenario == "one-edit" else paths
        for i, rel in enumerate(selected, 1):
            append_overlay(root, rel, f"P1-CORPUS {record['id']} {scenario} edit-{i}")
    elif scenario == "rename-delete":
        old = root / paths[0]
        renamed = old.with_name(old.name + ".p1-renamed")
        renamed.parent.mkdir(parents=True, exist_ok=True)
        old.rename(renamed)
        (root / paths[1]).unlink()
    elif scenario == "branch-switch":
        run("git", "switch", "-c", "p1-scenario", cwd=root)
        append_overlay(root, paths[0], f"P1-CORPUS {record['id']} branch-switch")
    elif scenario == "manifest-edit":
        rel = manifest_path(root)
        if rel:
            append_overlay(root, rel, f"P1-CORPUS {record['id']} manifest-edit")
        else:
            rel = ".p1-manifest-overlay"
            (root / rel).write_text("# P1-CORPUS manifest-edit\n")
            run("git", "add", rel, cwd=root)
    else:
        raise SystemExit(f"unknown scenario: {scenario}")
    return digest(root)


def digest(root: Path) -> dict:
    h = hashlib.sha256()
    tracked = run("git", "ls-files", "-s", "-z", cwd=root).encode()
    h.update(b"git-ls-files\0" + tracked)
    effective_paths = run("git", "ls-files", "-z", "--cached", "--others",
                          "--exclude-standard", cwd=root).encode()
    for raw in effective_paths.split(b"\0"):
        if not raw:
            continue
        rel = os.fsdecode(raw)
        p = root / rel
        if p.is_file():
            b = p.read_bytes()
            h.update(len(rel).to_bytes(8, "big") + rel.encode() + len(b).to_bytes(8, "big") + b)
    for rel in [".graphignore", ".git/info/exclude"]:
        p = root / rel
        b = p.read_bytes() if p.is_file() else b""
        h.update(b"policy\0" + rel.encode() + len(b).to_bytes(8, "big") + b)
    refs = run("git", "show-ref", cwd=root, check=False).encode()
    h.update(b"refs\0" + refs)
    return {"repository": root.name, "effective_tracked_input_sha256": h.hexdigest(),
            "tracked_manifest_sha256": hashlib.sha256(tracked).hexdigest(),
            "graphignore_present": (root / ".graphignore").is_file(),
            "git_info_exclude_present": (root / ".git/info/exclude").is_file(),
            "refs": refs.decode().splitlines()}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("command", choices=["apply", "reset", "digest"])
    ap.add_argument("repository")
    ap.add_argument("scenario", nargs="?")
    args = ap.parse_args()
    rid, root, record = repo_path(args.repository)
    if args.command == "reset":
        reset(root, record)
        result = digest(root)
    elif args.command == "digest":
        result = digest(root)
    else:
        if not args.scenario:
            raise SystemExit("apply requires a scenario")
        result = apply(root, record, args.scenario)
    result["repository_id"] = rid
    result["scenario"] = args.scenario if args.command == "apply" else "baseline" if args.command == "reset" else "observed"
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
