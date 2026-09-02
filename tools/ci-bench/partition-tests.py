#!/usr/bin/env python3
"""Deterministically partition compiled Go tests into weighted execution shards."""

from __future__ import annotations

import argparse
import json
import math
import re
import statistics
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, Iterable


SCHEMA = "ci-bench.test-shard-plan.v1"
INVENTORY_SCHEMA = "ci-bench.compiled-test-inventory.v1"
_TERMINAL_ACTIONS = frozenset({"pass", "fail", "skip"})


def _read_json(path: Path) -> Any:
    try:
        with path.open(encoding="utf-8-sig") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read JSON {path}: {error}") from error


def load_inventories(paths: Iterable[Path]) -> list[dict[str, Any]]:
    inventories: list[dict[str, Any]] = []
    seen_packages: set[str] = set()
    for path in paths:
        payload = _read_json(path)
        if payload.get("schema") != INVENTORY_SCHEMA:
            raise ValueError(
                f"{path}: schema is {payload.get('schema')!r}, want {INVENTORY_SCHEMA!r}"
            )
        package = payload.get("importPath")
        tests = payload.get("tests")
        if not isinstance(package, str) or not package:
            raise ValueError(f"{path}: importPath must be a non-empty string")
        if package in seen_packages:
            raise ValueError(f"duplicate package inventory: {package}")
        if not isinstance(tests, list) or not tests:
            raise ValueError(f"{path}: tests must be a non-empty list")
        if any(not isinstance(name, str) or not re.fullmatch(r"Test\w+", name) for name in tests):
            raise ValueError(f"{path}: every test must be a top-level Go Test identifier")
        if len(tests) != len(set(tests)):
            raise ValueError(f"{path}: duplicate test names")
        binary_name = payload.get("binaryName")
        package_relative = payload.get("packageDirectoryRelative")
        if not isinstance(binary_name, str) or Path(binary_name).name != binary_name:
            raise ValueError(f"{path}: binaryName must be a file name")
        if not isinstance(package_relative, str) or not package_relative:
            raise ValueError(f"{path}: packageDirectoryRelative must be non-empty")
        if Path(package_relative).is_absolute() or ".." in Path(package_relative).parts:
            raise ValueError(f"{path}: unsafe packageDirectoryRelative")
        seen_packages.add(package)
        inventories.append(
            {
                "importPath": package,
                "packageDirectoryRelative": package_relative.replace("\\", "/"),
                "binaryName": binary_name,
                "tests": sorted(tests),
                "sourceInventory": str(path),
                "listExitCode": payload.get("exitCode"),
            }
        )
    return sorted(inventories, key=lambda item: item["importPath"])


def load_timings(paths: Iterable[Path]) -> dict[tuple[str, str], list[float]]:
    observations: dict[tuple[str, str], list[float]] = defaultdict(list)
    for path in paths:
        try:
            handle = path.open(encoding="utf-8-sig")
        except OSError as error:
            raise ValueError(f"cannot read timing JSONL {path}: {error}") from error
        with handle:
            for line_number, line in enumerate(handle, 1):
                if not line.strip():
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError as error:
                    raise ValueError(f"{path}:{line_number}: invalid JSON: {error}") from error
                action = event.get("Action")
                package = event.get("Package")
                test = event.get("Test")
                elapsed = event.get("Elapsed")
                if (
                    action in _TERMINAL_ACTIONS
                    and isinstance(package, str)
                    and isinstance(test, str)
                    and "/" not in test
                    and isinstance(elapsed, (int, float))
                    and math.isfinite(float(elapsed))
                    and float(elapsed) >= 0
                ):
                    observations[(package, test)].append(float(elapsed))
    return observations


class _TrieNode:
    __slots__ = ("children", "terminal")

    def __init__(self) -> None:
        self.children: dict[str, _TrieNode] = {}
        self.terminal = False


def _render_trie(node: _TrieNode) -> str:
    alternatives = [re.escape(char) + _render_trie(child) for char, child in sorted(node.children.items())]
    if not alternatives:
        return ""
    body = alternatives[0] if len(alternatives) == 1 else "(" + "|".join(alternatives) + ")"
    if node.terminal:
        return "(" + body + ")?"
    return body


def compressed_run_regex(names: Iterable[str]) -> str:
    ordered = sorted(set(names))
    if not ordered:
        raise ValueError("cannot build a run expression for no tests")
    root = _TrieNode()
    for name in ordered:
        node = root
        for char in name:
            node = node.children.setdefault(char, _TrieNode())
        node.terminal = True
    return "^" + _render_trie(root) + "$"


def create_plan(
    inventories: list[dict[str, Any]],
    timing_observations: dict[tuple[str, str], list[float]],
    shard_count: int,
    default_weight: float,
    timing_paths: list[Path],
) -> dict[str, Any]:
    if shard_count <= 0:
        raise ValueError("shard count must be positive")
    if not math.isfinite(default_weight) or default_weight <= 0:
        raise ValueError("default weight must be finite and positive")

    work: list[dict[str, Any]] = []
    timing_keys_used: set[tuple[str, str]] = set()
    for inventory in inventories:
        for test in inventory["tests"]:
            key = (inventory["importPath"], test)
            samples = timing_observations.get(key, [])
            if samples:
                weight = statistics.median(samples)
                source = "median-observed-elapsed"
                timing_keys_used.add(key)
            else:
                weight = default_weight
                source = "default"
            work.append(
                {
                    "importPath": inventory["importPath"],
                    "packageDirectoryRelative": inventory["packageDirectoryRelative"],
                    "binaryName": inventory["binaryName"],
                    "name": test,
                    "weightSeconds": round(weight, 6),
                    "weightSource": source,
                    "observationCount": len(samples),
                }
            )

    # Longest-processing-time first, with complete lexical tie breaking.
    work.sort(key=lambda item: (-item["weightSeconds"], item["importPath"], item["name"]))
    bins: list[list[dict[str, Any]]] = [[] for _ in range(shard_count)]
    totals = [0.0] * shard_count
    for item in work:
        if item["weightSeconds"] == 0:
            # A Windows `go test -json` terminal duration is rounded and many
            # fast or subtest-owning parents therefore report zero. Every
            # placement is weight-equivalent for those entries, so use shard
            # test count first to prevent a single enormous -test.run argv.
            destination = min(
                range(shard_count),
                key=lambda index: (len(bins[index]), totals[index], index),
            )
        else:
            destination = min(
                range(shard_count),
                key=lambda index: (totals[index], len(bins[index]), index),
            )
        bins[destination].append(item)
        totals[destination] += item["weightSeconds"]

    shards: list[dict[str, Any]] = []
    for index, items in enumerate(bins):
        by_package: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for item in items:
            by_package[item["importPath"]].append(item)
        assignments: list[dict[str, Any]] = []
        for package in sorted(by_package):
            package_items = sorted(by_package[package], key=lambda item: item["name"])
            assignments.append(
                {
                    "importPath": package,
                    "packageDirectoryRelative": package_items[0]["packageDirectoryRelative"],
                    "binaryName": package_items[0]["binaryName"],
                    "runRegex": compressed_run_regex(item["name"] for item in package_items),
                    "tests": [
                        {
                            "name": item["name"],
                            "weightSeconds": item["weightSeconds"],
                            "weightSource": item["weightSource"],
                            "observationCount": item["observationCount"],
                        }
                        for item in package_items
                    ],
                }
            )
        shards.append(
            {
                "index": index,
                "estimatedWeightSeconds": round(totals[index], 6),
                "testCount": len(items),
                "assignments": assignments,
            }
        )

    unused = sorted(
        f"{package}::{test}"
        for package, test in set(timing_observations) - timing_keys_used
    )
    return {
        "schema": SCHEMA,
        "algorithm": "deterministic-longest-processing-time-first-v2-zero-count-tiebreak",
        "shardCount": shard_count,
        "defaultWeightSeconds": default_weight,
        "timingInputs": [str(path) for path in timing_paths],
        "packageCount": len(inventories),
        "testCount": len(work),
        "packages": inventories,
        "shards": shards,
        "diagnostics": {
            "observedTestCount": len(timing_keys_used),
            "defaultedTestCount": len(work) - len(timing_keys_used),
            "unusedTimingKeys": unused,
        },
    }


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", action="append", type=Path, required=True)
    parser.add_argument("--timings", action="append", type=Path, default=[])
    parser.add_argument("--shards", type=int, required=True)
    parser.add_argument("--default-weight", type=float, default=1.0)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        inventories = load_inventories(args.inventory)
        timings = load_timings(args.timings)
        plan = create_plan(inventories, timings, args.shards, args.default_weight, args.timings)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        temporary = args.output.with_name(args.output.name + ".tmp")
        with temporary.open("w", encoding="utf-8", newline="\n") as handle:
            json.dump(plan, handle, indent=2, sort_keys=False)
            handle.write("\n")
        temporary.replace(args.output)
    except (ValueError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
