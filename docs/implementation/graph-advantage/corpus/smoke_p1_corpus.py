#!/usr/bin/env python3
"""Fast, non-benchmark validation of all P1 corpus scenarios."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
MANIFEST = json.loads((HERE / "corpus-manifest.json").read_text())
SCENARIOS = [s["id"] for s in MANIFEST["scenarios"]]
RUNNER = HERE / "p1_scenario.py"


def invoke(repo: str, command: str, scenario: str | None = None, fast: bool = True) -> dict:
    args = [sys.executable, str(RUNNER), command, repo]
    if scenario:
        args.append(scenario)
    env = os.environ.copy()
    if fast and command in {"apply", "reset"}:
        env["P1_SCENARIO_SKIP_DIGEST"] = "1"
    return json.loads(subprocess.check_output(args, text=True, env=env))


def main() -> int:
    checked = 0
    for record in MANIFEST["repositories"]:
        repo = record["id"]
        paths = record["selected_paths"]
        assert len(paths) == 10, (repo, len(paths))
        assert all(Path(p).suffix.lower() in {".go", ".ts", ".tsx", ".py"} for p in paths)
        baseline = invoke(repo, "reset", fast=False)["effective_tracked_input_sha256"]
        for scenario in SCENARIOS:
            result = invoke(repo, "apply", scenario)
            assert result["scenario"] == scenario
            if scenario == "manifest-edit":
                root = Path(os.environ.get("P1_CORPUS_ROOT", "/Users/thomi/Projects/graph-advantage-p1-corpus")) / repo
                if (root / "package.json").is_file():
                    json.loads((root / "package.json").read_text())
            invoke(repo, "reset")
            checked += 1
        restored = invoke(repo, "digest")["effective_tracked_input_sha256"]
        assert restored == baseline, (repo, baseline, restored)
    print(json.dumps({"repositories": len(MANIFEST["repositories"]), "scenarios": len(SCENARIOS),
                      "checks": checked, "status": "ok"}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
