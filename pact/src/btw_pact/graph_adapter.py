"""Entire Graph v0.4 NDJSON adapter; no invented edges or mixed-version paths."""
import json
import subprocess
import tempfile
from pathlib import Path

from .contracts import digest
from .gitutil import SCOPE, git


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
