#!/usr/bin/env python3
"""Compatibility reader for normalized Graph Advantage evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from typing import Any

SCHEMA_VERSION = "graph-advantage-evidence/v1"
CASE_VERSION = "graph-advantage-evidence-case/v1"


def load_run(run_path: Path) -> dict[str, Any]:
    run = json.loads(run_path.read_text())
    if run.get("schema_version") != SCHEMA_VERSION:
        raise ValueError(f"unsupported canonical evidence schema: {run.get('schema_version')!r}")
    return run


def load_legacy_rows(run_path: Path) -> list[dict[str, Any]]:
    """Return observed source rows in their original order."""
    run = load_run(run_path)
    cases = run.get("cases")
    if not isinstance(cases, dict):
        return []
    if cases.get("format") != CASE_VERSION:
        raise ValueError("unsupported canonical case schema")
    case_path = (run_path.parent / cases["path"]).resolve()
    expected_parent = (run_path.parent.parent / "cases").resolve()
    if case_path.parent != expected_parent:
        raise ValueError("canonical case path escapes cases directory")
    payload = case_path.read_bytes()
    if hashlib.sha256(payload).hexdigest() != cases.get("sha256"):
        raise ValueError("canonical case digest mismatch")
    decoded = [json.loads(line) for line in payload.decode().splitlines() if line]
    if len(decoded) != cases.get("count"):
        raise ValueError("canonical case count mismatch")
    decoded.sort(key=lambda case: case["observed_order"])
    return [case["observed"] for case in decoded]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run", type=Path, required=True)
    args = parser.parse_args()
    for row in load_legacy_rows(args.run.resolve()):
        print(json.dumps(row, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
