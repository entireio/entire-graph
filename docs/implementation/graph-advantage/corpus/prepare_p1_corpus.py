#!/usr/bin/env python3
"""Prepare the pinned, source-only P1 evaluation corpus.

This script owns corpus acquisition and manifest generation.  It never writes
the cloned repositories into this product checkout; the default destination is
an adjacent corpus directory.  It does not run graph queries or benchmarks.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[4]
DEFAULT_DEST = Path("/Users/thomi/Projects/graph-advantage-p1-corpus")
ENTIRE_COMMIT = "88dd1dc95a996999ae4e456879b6dd86d8027f71"
QUERY = "trace request routing and graph traversal"
SEED = 20260905

REPOS = {
    "go-chi-chi": "https://github.com/go-chi/chi.git",
    "kubernetes-kubernetes": "https://github.com/kubernetes/kubernetes.git",
    "colinhacks-zod": "https://github.com/colinhacks/zod.git",
    "psf-requests": "https://github.com/psf/requests.git",
}

SOURCE_EXTENSIONS = {
    ".c", ".cc", ".cpp", ".css", ".go", ".h", ".hpp", ".html", ".java",
    ".js", ".jsx", ".kt", ".m", ".md", ".php", ".py", ".rb", ".rs",
    ".scala", ".sh", ".sql", ".swift", ".ts", ".tsx", ".vue", ".xml",
    ".yaml", ".yml",
}
EXCLUDED_PARTS = {
    ".git", ".hg", ".svn", "vendor", "node_modules", "third_party", "dist",
    "build", "generated", "gen", "bazel-out", "_output", "coverage",
}


def run(*args: str, cwd: Path | None = None, capture: bool = True) -> str:
    p = subprocess.run(args, cwd=cwd, check=True, text=True,
                       stdout=subprocess.PIPE if capture else None,
                       stderr=subprocess.PIPE if capture else None)
    return p.stdout.strip() if capture else ""


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def eligible(path: Path) -> bool:
    parts = set(path.parts)
    if parts & EXCLUDED_PARTS or path.name.startswith("."):
        return False
    if ".generated." in path.name or path.name.endswith("_generated.go"):
        return False
    return path.suffix.lower() in SOURCE_EXTENSIONS


def source_inventory(repo: Path) -> tuple[list[str], int]:
    paths: list[str] = []
    total = 0
    for raw in run("git", "ls-files", "-z", cwd=repo).encode().split(b"\0"):
        if not raw:
            continue
        rel = Path(os.fsdecode(raw))
        if not eligible(rel):
            continue
        file = repo / rel
        if file.is_file() and not file.is_symlink():
            paths.append(rel.as_posix())
            total += file.stat().st_size
    paths.sort()
    return paths, total


def chosen_paths(paths: list[str]) -> list[str]:
    # Hash ordering makes the ten-file sample stable without privileging a
    # repository's top-level directory layout.
    return [p for _, p in sorted((hashlib.sha256(p.encode()).hexdigest(), p)
                                 for p in paths)[:10]]


def license_info(repo: Path) -> dict[str, str | None]:
    candidates = [p for p in repo.iterdir() if p.name.lower().startswith("license")]
    if not candidates:
        return {"path": None, "sha256": None}
    p = sorted(candidates)[0]
    return {"path": p.name, "sha256": sha256(p)}


def comment_for(path: str, marker: str) -> str:
    suffix = Path(path).suffix.lower()
    if suffix in {".py", ".sh", ".yaml", ".yml", ".toml"}:
        return f"# {marker}\n"
    if suffix in {".html", ".xml", ".vue"}:
        return f"<!-- {marker} -->\n"
    if suffix == ".md":
        return f"\n<!-- {marker} -->\n"
    return f"// {marker}\n"


def make_synthetic(dest: Path) -> dict:
    root = dest / "synthetic-2000"
    if root.exists():
        shutil.rmtree(root)
    root.mkdir(parents=True)
    counts = {"go": 667, "ts": 667, "py": 666}
    n = 0
    for ext, count in counts.items():
        for i in range(count):
            n += 1
            p = root / f"src/{ext}/file-{i:04d}.{ext}"
            p.parent.mkdir(parents=True, exist_ok=True)
            if ext == "go":
                body = f"package p{i % 17}\n\nfunc Function{i}(input int) int {{ return input + {i} }}\n"
            elif ext == "ts":
                body = f"export function function{i}(input: number): number {{ return input + {i}; }}\n"
            else:
                body = f"def function_{i}(input: int) -> int:\n    return input + {i}\n"
            p.write_text(body)
    run("git", "init", "-q", cwd=root)
    run("git", "add", ".", cwd=root)
    run("git", "-c", "user.name=P1 corpus", "-c", "user.email=p1-corpus@example.invalid",
        "commit", "-q", "-m", "synthetic P1 seed", cwd=root)
    paths, total = source_inventory(root)
    return {
        "id": "synthetic-2000", "kind": "synthetic", "commit": run("git", "rev-parse", "HEAD", cwd=root),
        "source_counts": {"total": len(paths), "by_extension": {k: v for k, v in counts.items()}},
        "eligible_bytes": total, "selected_paths": chosen_paths(paths),
        "license": {"path": None, "sha256": None}, "generation_seed": SEED,
        "generation": "2000 independent source files: Go 667, TypeScript 667, Python 666",
    }


def repo_record(repo_id: str, repo: Path, source: str, commit: str) -> dict:
    paths, total = source_inventory(repo)
    selected = chosen_paths(paths)
    overlays = []
    for idx, path in enumerate(selected):
        overlays.append({
            "path": path, "marker": f"P1-CORPUS {repo_id} edit-{idx + 1}",
            "append": comment_for(path, f"P1-CORPUS {repo_id} edit-{idx + 1}"),
            "reversible": "remove the exact appended marker line",
        })
    return {
        "id": repo_id, "kind": "repository", "source": source, "commit": commit,
        "license": license_info(repo), "eligible_ruleset": "p1-source-v1",
        "eligible_count": len(paths), "eligible_bytes": total,
        "selected_paths": selected, "overlays": overlays,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--destination", type=Path, default=DEFAULT_DEST)
    ap.add_argument("--entire-source", type=Path, default=ROOT)
    args = ap.parse_args()
    dest = args.destination.resolve()
    dest.mkdir(parents=True, exist_ok=True)
    records = []
    for repo_id, remote in REPOS.items():
        target = dest / repo_id
        if not target.exists():
            run("git", "clone", "--depth", "1", "--no-tags", remote, str(target), capture=False)
        commit = run("git", "rev-parse", "HEAD", cwd=target)
        records.append(repo_record(repo_id, target, remote, commit))

    # The requested Entire Graph fixture is made from git archive, in a
    # separate non-product repository.  The source checkout is never modified.
    entire = dest / "entire-graph-frozen-88dd1dc9"
    if entire.exists():
        shutil.rmtree(entire)
    archive = dest / "entire-graph-frozen-88dd1dc9.tar"
    with archive.open("wb") as f:
        subprocess.run(("git", "archive", "--format=tar", ENTIRE_COMMIT), cwd=args.entire_source,
                       check=True, stdout=f)
    entire.mkdir()
    run("tar", "-xf", str(archive), "-C", str(entire))
    archive.unlink()
    run("git", "init", "-q", cwd=entire)
    run("git", "add", ".", cwd=entire)
    run("git", "-c", "user.name=P1 corpus", "-c", "user.email=p1-corpus@example.invalid",
        "commit", "-q", "-m", f"archive Entire Graph {ENTIRE_COMMIT[:8]}", cwd=entire)
    archived = repo_record("entire-graph-frozen-88dd1dc9", entire,
                           "git archive from approved Entire Graph checkout", ENTIRE_COMMIT)
    archived["fixture_commit"] = run("git", "rev-parse", "HEAD", cwd=entire)
    records.append(archived)
    records.append(make_synthetic(dest))

    manifest = {
        "manifest": "p1-corpus-v1", "prepared": "2026-09-05", "query": QUERY,
        "generation_seed": SEED,
        "source_policy": {
            "extensions": sorted(SOURCE_EXTENSIONS), "excluded_path_parts": sorted(EXCLUDED_PARTS),
            "excluded_names": [".*", "*.generated.*", "*_generated.go"],
            "symlinks": "excluded", "tracked_files_only": True,
        },
        "scenarios": [
            {"id": "cold", "operation": "fresh checkout and query"},
            {"id": "unchanged", "operation": "repeat query without source changes"},
            {"id": "one-edit", "operation": "apply overlays[0] and query"},
            {"id": "ten-edit", "operation": "apply all ten overlays and query"},
            {"id": "rename-delete", "operation": "rename selected_paths[0], delete selected_paths[1], query, then restore"},
            {"id": "branch-switch", "operation": "switch seed branch to a reversible edit branch and back"},
            {"id": "manifest-edit", "operation": "apply a reversible package-manifest/config edit and query"},
        ],
        "repositories": records,
        "provenance": {
            "plan": "/Users/thomi/Projects/entire-plan/entire-graph-advantage-implementation-plan.md",
            "product_checkout": str(args.entire_source.resolve()),
            "competitor_implementations": "not consulted",
            "benchmarks": "not run",
        },
    }
    out = Path(__file__).with_name("corpus-manifest.json")
    out.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    print(json.dumps({"manifest": str(out), "destination": str(dest),
                      "repositories": [(r["id"], r.get("eligible_count", r.get("source_counts", {}).get("total")), r.get("eligible_bytes")) for r in records]}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
