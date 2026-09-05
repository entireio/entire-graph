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
import re
import shutil
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[4]
DEFAULT_DEST = Path(os.environ.get("P1_CORPUS_ROOT", "/Users/thomi/Projects/graph-advantage-p1-corpus"))
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
    "build", "generated", "gen", "bazel-out", "_output", "coverage", "docs",
    "doc", "documentation", "evidence", "examples", "benchmark", "benchmarks",
}
PARSABLE_EXTENSIONS = {".go", ".ts", ".tsx", ".py"}


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


def source_inventory(repo: Path) -> tuple[list[str], int, int]:
    paths: list[str] = []
    total = 0
    raw_paths = [raw for raw in run("git", "ls-files", "-z", cwd=repo).encode().split(b"\0") if raw]
    for raw in raw_paths:
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
    return paths, total, len(raw_paths)


def chosen_paths(paths: list[str], root: Path | None = None) -> list[str]:
    # Hash ordering makes the ten-file sample stable without privileging a
    # repository's top-level directory layout.
    eligible = [p for p in paths if Path(p).suffix.lower() in PARSABLE_EXTENSIONS]
    if root is not None:
        eligible = [p for p in eligible if re.search(
            r"\b(?:func|function|(?:async\s+)?def)\b", (root / p).read_text(errors="ignore"))]
    return [p for _, p in sorted((hashlib.sha256(p.encode()).hexdigest(), p)
                                 for p in eligible)[:10]]


def query_for(repo: Path, selected: list[str]) -> str:
    path = selected[0]
    ignored = {"package", "import", "from", "def", "class", "func", "export", "const",
               "return", "test", "tests", "init", "input", "number", "string", "copyright",
               "expect", "testing", "assert", "require", "should", "file"}
    path_tokens = [x for x in re.split(r"[^A-Za-z0-9]+", Path(path).stem)
                   if len(x) >= 4 and x.lower() not in ignored]
    token = path_tokens[0] if path_tokens else None
    text = (repo / path).read_text(errors="ignore")[:65536]
    identifiers = re.findall(r"[A-Za-z_][A-Za-z0-9_]{3,}", text)
    token = token or next((x for x in identifiers if x.lower() not in ignored), None)
    if not token:
        token = Path(path).stem.replace("-", " ").replace("_", " ")
    return f"find the {token} symbol or entrypoint in {path}"


def write_marker(repo: Path, repo_id: str) -> None:
    marker = repo / ".git" / "p1-corpus-fixture.json"
    marker.write_text(json.dumps({"id": repo_id, "purpose": "P1 evaluation fixture"}) + "\n")


def prepare_branch_variant(repo: Path, record: dict) -> None:
    branch = "p1-branch-variant"
    run("git", "switch", "-C", branch, cwd=repo)
    rel = record["selected_paths"][0]
    p = repo / rel
    source = p.read_text()
    comment = comment_for(rel, f"P1-CORPUS {record['id']} committed branch variant").rstrip("\n")
    if p.suffix.lower() == ".py":
        match = re.search(r"(?m)^(\s*)(?:async\s+)?def\s+[^\n]+:\s*$", source)
        if not match:
            raise SystemExit(f"no Python function body for branch variant: {rel}")
        source = source[:match.end()] + "\n" + match.group(1) + "    " + comment + source[match.end():]
    else:
        match = re.search(r"(?:func\s+\w+[^\{]*|function\s+\w+[^\{]*|=>\s*)\{", source)
        if not match:
            raise SystemExit(f"no Go/TypeScript function body for branch variant: {rel}")
        source = source[:match.end()] + "\n\t" + comment + source[match.end():]
    p.write_text(source)
    run("git", "add", rel, cwd=repo)
    run("git", "-c", "user.name=P1 corpus", "-c", "user.email=p1-corpus@example.invalid",
        "commit", "-q", "-m", "P1 committed branch variant", cwd=repo)
    record["branch_variant_commit"] = run("git", "rev-parse", "HEAD", cwd=repo)
    run("git", "checkout", "--detach", record.get("fixture_commit", record["commit"]), cwd=repo)


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
    write_marker(root, "synthetic-2000")
    paths, total, raw_count = source_inventory(root)
    return {
        "id": "synthetic-2000", "kind": "synthetic", "commit": run("git", "rev-parse", "HEAD", cwd=root),
        "raw_tracked_count": raw_count, "all_indexed_count": len(paths), "all_indexed_bytes": total,
        "language_source_counts": {k: v for k, v in counts.items()},
        "language_source_bytes": {ext: sum((root / p).stat().st_size for p in paths
                                            if Path(p).suffix.lower() == f".{ext}")
                                  for ext in counts},
        "selected_paths": chosen_paths(paths, root), "query": query_for(root, chosen_paths(paths, root)),
        "license": {"path": None, "sha256": None}, "generation_seed": SEED,
        "generation": "2000 independent source files: Go 667, TypeScript 667, Python 666",
    }


def repo_record(repo_id: str, repo: Path, source: str, commit: str) -> dict:
    paths, total, raw_count = source_inventory(repo)
    selected = chosen_paths(paths, repo)
    overlays = []
    for idx, path in enumerate(selected):
        overlays.append({
            "path": path, "marker": f"P1-CORPUS {repo_id} edit-{idx + 1}",
            "edit": "insert the exact marker inside the first function body",
            "reversible": "restore the recorded fixture commit",
        })
    return {
        "id": repo_id, "kind": "repository", "source": source, "commit": commit,
        "license": license_info(repo), "eligible_ruleset": "p1-source-v1",
        "raw_tracked_count": raw_count, "all_indexed_count": len(paths), "all_indexed_bytes": total,
        "language_source_counts": {ext.lstrip("."): sum(1 for p in paths if Path(p).suffix.lower() == ext)
                                    for ext in sorted(PARSABLE_EXTENSIONS)},
        "language_source_bytes": {ext.lstrip("."): sum((repo / p).stat().st_size for p in paths
                                                        if Path(p).suffix.lower() == ext)
                                   for ext in sorted(PARSABLE_EXTENSIONS)},
        "selected_paths": selected, "query": query_for(repo, selected), "overlays": overlays,
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
        write_marker(target, repo_id)
        commit = run("git", "rev-parse", "HEAD", cwd=target)
        record = repo_record(repo_id, target, remote, commit)
        prepare_branch_variant(target, record)
        records.append(record)

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
    write_marker(entire, "entire-graph-frozen-88dd1dc9")
    archived = repo_record("entire-graph-frozen-88dd1dc9", entire,
                           "git archive from approved Entire Graph checkout", ENTIRE_COMMIT)
    archived["fixture_commit"] = run("git", "rev-parse", "HEAD", cwd=entire)
    prepare_branch_variant(entire, archived)
    records.append(archived)
    synthetic = make_synthetic(dest)
    prepare_branch_variant(dest / "synthetic-2000", synthetic)
    records.append(synthetic)

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
            {"id": "rename", "operation": "rename selected_paths[0] to the same-extension *_p1 path, query, then restore"},
            {"id": "delete", "operation": "delete selected_paths[1], query, then restore"},
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
                      "repositories": [(r["id"], r.get("all_indexed_count"), r.get("all_indexed_bytes"), r.get("language_source_counts")) for r in records]}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
