#!/usr/bin/env python3
"""Check real Graph health output through a supplied Brain checkout's reader.

Run with the Go toolchain on PATH, e.g.:
  mise exec -- python3 scripts/test-brain-health.py /path/to/entire-brain

Builds Graph and creates synthetic fixtures in a temporary directory. Go's
overlay adds the consumer test without modifying the Brain checkout.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("brain", type=Path, help="local Entire Brain checkout")
    args = parser.parse_args()
    graph = Path(__file__).resolve().parent.parent
    brain = args.brain.resolve()
    if not (brain / "internal/cli/semantic_stream.go").is_file():
        parser.error("the supplied checkout has no Brain semantic stream reader")
    virtual_test = brain / "internal/cli/graph_health_consumer_integration_test.go"
    if virtual_test.exists():
        parser.error(f"refusing to overlay an existing file: {virtual_test}")

    env = dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_SYSTEM="/dev/null")
    for label, repo in (("Graph", graph), ("Brain", brain)):
        revision = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=repo, env=env, text=True
        ).strip()
        dirty = subprocess.check_output(
            ["git", "status", "--porcelain"], cwd=repo, env=env, text=True
        ).strip()
        print(f"{label}: {revision}{' (working-tree changes)' if dirty else ''}", flush=True)

    with tempfile.TemporaryDirectory(prefix="graph-brain-health-") as temporary:
        root = Path(temporary)
        binary = root / "entire-graph"
        subprocess.run(
            ["go", "build", "-o", str(binary), "./cmd/entire-graph"],
            cwd=graph, env=env, check=True,
        )
        for name, total, flagged in (
            ("healthy", 20, 0), ("below", 21, 1),
            ("boundary", 20, 1), ("unsafe", 3, 1),
        ):
            fixture = root / name
            fixture.mkdir()
            for index in range(total):
                content = "def retained():\n    return 1\n"
                if index < flagged:
                    content += "def broken(:\n"
                (fixture / f"source{index}.py").write_text(content)
            (fixture / "README.md").write_text("# documentation\n")
            with (root / f"{name}.ndjson").open("w") as output:
                subprocess.run(
                    [str(binary), "snapshot", "--repo", str(fixture),
                     "--worktree", "--format", "ndjson"],
                    cwd=graph, env=env, stdout=output, check=True,
                )
        overlay = root / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {
            str(virtual_test): str(graph / "scripts/testdata/brain_health_consumer_test.go")
        }}))
        env["GRAPH_HEALTH_FIXTURES"] = str(root)
        subprocess.run(
            ["go", "test", "-overlay", str(overlay), "./internal/cli",
             "-run", "^TestGraphHealthConsumerCompatibility$", "-count=1", "-v"],
            cwd=brain, env=env, check=True,
        )


if __name__ == "__main__":
    main()
