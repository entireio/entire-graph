#!/usr/bin/env python3
"""Regenerate a target's historical weights from downloaded CI shard evidence.

Weights only balance shards; they never decide what runs. This reads the
`*-test-shard-N` artifacts of one green run (each holds `shard-metadata.json`
and `shard-events.jsonl`), takes every top-level root's terminal elapsed time
as measured on the real runner, and writes the roots at or above a threshold.
Roots below it keep the settings' default weight.

The evidence must be one complete, successful run of one target: every shard
index present once, every exit zero, one repository commit, and exactly one
terminal event per root. Anything else is refused rather than averaged.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path
from typing import Any

import plan_shards
import targets


SHARD_RUN_SCHEMA = "entire-graph.windows-ci.shard-run.v1"
TERMINAL_ACTIONS = frozenset({"pass", "fail", "skip"})


def read_json(path: Path) -> Any:
    return plan_shards.read_json(path)


def collect(evidence: Path, target: targets.Target) -> tuple[dict[tuple[str, str], float], str, int]:
    """Return per-root elapsed seconds, the commit, and the shard count of one run."""
    metadata_paths = sorted(evidence.rglob("shard-metadata.json"))
    if not metadata_paths:
        raise ValueError(f"{evidence}: no shard-metadata.json found")
    elapsed: dict[tuple[str, str], float] = {}
    commits: set[Any] = set()
    indexes: list[Any] = []
    shard_counts: set[Any] = set()
    for metadata_path in metadata_paths:
        metadata = read_json(metadata_path)
        if not isinstance(metadata, dict) or metadata.get("schema") != SHARD_RUN_SCHEMA:
            raise ValueError(f"{metadata_path}: unexpected shard metadata schema")
        if metadata.get("targetEnvironment") != target.environment:
            raise ValueError(f"{metadata_path}: evidence is not {target.description}")
        if metadata.get("exitCode") != 0 or metadata.get("errors") != []:
            raise ValueError(f"{metadata_path}: shard did not succeed; refusing to learn from it")
        commits.add(metadata.get("repositorySha"))
        indexes.append(metadata.get("shardIndex"))
        shard_counts.add(metadata.get("shardCount"))
        events_path = metadata_path.with_name("shard-events.jsonl")
        try:
            handle = events_path.open(encoding="utf-8-sig")
        except OSError as error:
            raise ValueError(f"cannot read {events_path}: {error}") from error
        with handle:
            for line_number, line in enumerate(handle, 1):
                if not line.strip():
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError as error:
                    raise ValueError(f"{events_path}:{line_number}: invalid JSON: {error}") from error
                if not isinstance(event, dict):
                    raise ValueError(f"{events_path}:{line_number}: event is not an object")
                test, package = event.get("Test"), event.get("Package")
                if (
                    not isinstance(test, str)
                    or "/" in test
                    or not isinstance(package, str)
                    or event.get("Action") not in TERMINAL_ACTIONS
                ):
                    continue
                if event["Action"] == "fail":
                    raise ValueError(f"{events_path}:{line_number}: failed root {package}::{test}")
                seconds = event.get("Elapsed", 0.0)
                if (
                    not isinstance(seconds, (int, float))
                    or isinstance(seconds, bool)
                    or not math.isfinite(float(seconds))
                    or float(seconds) < 0
                ):
                    raise ValueError(f"{events_path}:{line_number}: invalid Elapsed for {package}::{test}")
                key = (package, test)
                if key in elapsed:
                    raise ValueError(f"root {package}::{test} has more than one terminal event")
                elapsed[key] = float(seconds)
    if len(commits) != 1 or not isinstance(next(iter(commits)), str):
        raise ValueError(f"evidence spans repository commits {sorted(map(str, commits))!r}")
    if len(shard_counts) != 1 or not isinstance(next(iter(shard_counts)), int):
        raise ValueError("evidence disagrees on the shard count")
    shard_count = next(iter(shard_counts))
    if sorted(indexes, key=str) != sorted(range(shard_count), key=str):
        raise ValueError(f"evidence holds shard indexes {indexes!r}, want every index of {shard_count} once")
    if not elapsed:
        raise ValueError("evidence contains no top-level terminal events")
    return elapsed, next(iter(commits)), shard_count


def render(
    elapsed: dict[tuple[str, str], float], minimum_seconds: float, source_commit: str, source: str
) -> str:
    rows = sorted(
        (package, name, round(seconds, 2))
        for (package, name), seconds in elapsed.items()
        if seconds >= minimum_seconds
    )
    lines = [
        "{",
        f'  "schema": {json.dumps(plan_shards.WEIGHTS_SCHEMA)},',
        f'  "sourceCommit": {json.dumps(source_commit)},',
        f'  "source": {json.dumps(source, ensure_ascii=False)},',
        '  "weights": [',
    ]
    for position, (package, name, seconds) in enumerate(rows):
        row = json.dumps({"package": package, "name": name, "seconds": seconds}, ensure_ascii=False)
        lines.append("    " + row + ("," if position < len(rows) - 1 else ""))
    lines += ["  ]", "}"]
    return "\n".join(lines) + "\n"


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, required=True, help="directory of downloaded shard artifacts")
    parser.add_argument("--output", type=Path, required=True, help="historical-weights file to write")
    parser.add_argument("--target", default=targets.DEFAULT.name, choices=sorted(targets.TARGETS))
    parser.add_argument("--minimum-seconds", type=float, default=0.5)
    parser.add_argument("--run", default=None, help="CI run identifier to record in the source note")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        target = targets.resolve(args.target)
        if not math.isfinite(args.minimum_seconds) or args.minimum_seconds < 0:
            raise ValueError("--minimum-seconds must be finite and non-negative")
        elapsed, commit, shard_count = collect(args.evidence.resolve(strict=True), target)
        run = f" of CI run {args.run}" if args.run else ""
        source = (
            f"Top-level terminal elapsed from the {shard_count} {target.name} shard evidence artifacts"
            f"{run}; roots below {args.minimum_seconds:g} seconds intentionally use the configured default."
        )
        text = render(elapsed, args.minimum_seconds, commit, source)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(text, encoding="utf-8", newline="\n")
        plan_shards.load_weights(args.output)
    except (ValueError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2
    kept = sum(1 for seconds in elapsed.values() if seconds >= args.minimum_seconds)
    print(f"wrote {kept} of {len(elapsed)} roots from commit {commit} to {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
