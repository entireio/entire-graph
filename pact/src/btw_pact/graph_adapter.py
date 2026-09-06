"""Entire Graph v0.4 NDJSON adapter; no invented edges or mixed-version paths."""
import ast
import json
import subprocess
import tempfile
from pathlib import Path

from .contracts import digest
from .gitutil import SCOPE, fixture_files, git


def entire(repo, *args):
    p = subprocess.run(["entire", "graph", *args], cwd=repo, capture_output=True, text=True, timeout=60)
    if p.returncode:
        raise RuntimeError(p.stderr[:2000] or "Entire Graph failed")
    return p.stdout


def snapshot(repo: Path, sha: str) -> dict:
    ignore = repo / "pact/graph-fixture.ignore"
    with tempfile.TemporaryDirectory(prefix="pact-graph-") as td:
        target = Path(td) / "tree"
        created = False
        try:
            # Committed snapshots read Git objects: no source checkout is needed.
            git(repo, "worktree", "add", "--detach", "--no-checkout", str(target), sha)
            created = True
            raw = entire(target, "snapshot", "--repo", str(target), "--format", "ndjson",
                         "--ignore-file", str(ignore))
        finally:
            if created:
                git(repo, "worktree", "remove", "--force", str(target))
    rows = [json.loads(line) for line in raw.splitlines() if line.strip()]
    if not rows or rows[0].get("provider") != "entire-graph" or rows[0].get("commit") != sha:
        raise ValueError("Graph snapshot identity mismatch")
    summaries = [r for r in rows if r.get("record_type") == "summary"]
    if len(summaries) != 1:
        raise ValueError("Incomplete Graph stream: summary missing")
    summary = summaries[0]
    symbols = {r["id"]: r for r in rows if r.get("record_type") == "symbol"}
    calls = [r for r in rows if r.get("record_type") == "relation" and r.get("type") == "CALLS"]
    partial = bool(summary.get("partial_failures")) or summary.get("stats", {}).get("completeness_level") != "ok"
    return {"commit_sha": sha, "symbols": symbols, "calls": calls, "partial": partial,
            "diagnostics": source_diagnostics(fixture_files(repo, sha)),
            "summary": summary, "provider_version": rows[0].get("provider_version"),
            "raw": raw, "hash": digest(raw)}


def analyse(repo: Path, base: str, head: str) -> dict:
    raw = entire(repo, "diff", "--repo", str(repo), "--base", base, "--head", head,
                 "--json", "--max-seconds", "20", "--", SCOPE)
    changes = json.loads(raw)
    if changes.get("base") != base or changes.get("head") != head or "files" not in changes:
        raise ValueError("Unexpected semantic diff identity/schema")
    versions = {"base": snapshot(repo, base), "head": snapshot(repo, head)}
    return {"diff": changes, "diff_raw": raw, "versions": versions,
            "partial": bool(changes.get("warnings")) or any(v["partial"] for v in versions.values()),
            "errors": []}


def source_diagnostics(files):
    """Bounded precaution for the registered Python fixture, not a completeness proof.

    Graph can omit a runtime lookup entirely without a parser failure. Record
    source-based suspicion separately; never convert it into a Graph relation.
    """
    diagnostics = []
    reflective = {"getattr", "setattr", "delattr", "eval", "exec", "__import__", "globals", "locals", "vars"}
    for path, source in sorted(files.items()):
        def flag(node, code, detail):
            diagnostics.append({"code": code, "file_path": path, "line": getattr(node, "lineno", 1),
                                "origin": "source_precaution", "detail": detail})
        try:
            tree = ast.parse(source)
        except SyntaxError as error:
            flag(error, "source_parse_failure", "Python source could not be inspected")
            continue
        for node in tree.body:
            if isinstance(node, (ast.Assign, ast.AnnAssign, ast.AugAssign)):
                flag(node, "module_configuration", "Module-level configuration or registry may affect behavior outside CALLS evidence")
        direct = {n.name for n in ast.walk(tree) if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef))}
        direct.update(a.asname or a.name for n in ast.walk(tree) if isinstance(n, ast.ImportFrom) for a in n.names)
        # Only inert builtins used by the trusted pilot are exempt from the
        # unknown-call precaution. Aliasing/rebinding is separately flagged.
        direct.update({"ValueError", "TypeError", "bool", "str", "int", "len"})
        assigned = {n.id for n in ast.walk(tree) if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Store)}
        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                if isinstance(node.func, ast.Name):
                    name = node.func.id
                    if name in reflective:
                        flag(node, "runtime_lookup", f"{name} may resolve or modify relationships at runtime")
                    elif name not in direct or name in assigned:
                        flag(node, "indirect_call", f"Call through {name} requires source/test verification")
                else:
                    flag(node, "dynamic_call_target", "Attribute, registry or computed callable needs source/test verification")
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) and node.decorator_list:
                flag(node, "decorated_definition", "Decorator can replace runtime behavior")
            if isinstance(node, ast.ImportFrom) and any(a.name == "*" for a in node.names):
                flag(node, "wildcard_import", "Wildcard import may hide runtime bindings")
    return diagnostics
