#!/usr/bin/env python3
"""Deterministically partition compiled Windows tests using native baseline time.

Heavy-package top-level tests are independently weighted. Other test packages
stay monolithic and are placed as indivisible items, which preserves their
package-global lifetime and guarantees one execution per candidate.
"""

from __future__ import annotations

import argparse
import collections
import json
import pathlib
import re
from typing import Any, Iterable


TEST_NAME = re.compile(r"^Test")
SUPPLEMENTAL_NAME = re.compile(r"^(?:Fuzz|Example)")
TERMINAL = frozenset({"pass", "fail", "skip"})


def json_lines(path: pathlib.Path) -> Iterable[dict[str, Any]]:
    with path.open("r", encoding="utf-8-sig", errors="strict") as stream:
        for line_number, line in enumerate(stream, 1):
            if not line.strip():
                continue
            value = json.loads(line)
            if not isinstance(value, dict):
                raise ValueError(f"{path}:{line_number}: expected a JSON object")
            yield value


def load(path: pathlib.Path) -> Any:
    with path.open("r", encoding="utf-8-sig") as stream:
        return json.load(stream)


def write(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
    temporary.replace(path)


def build_plan(args: argparse.Namespace) -> int:
    listed = load(args.inventory)
    full_listed = load(args.full_inventory)
    if not isinstance(listed, dict) or not isinstance(full_listed, dict):
        raise ValueError("inventory must be an object keyed by import path")
    test_inventory = {
        str(package): sorted(
            str(name) for name in (names or []) if TEST_NAME.match(str(name))
        )
        for package, names in listed.items()
    }
    full_inventory = {
        str(package): sorted(str(name) for name in (names or []))
        for package, names in full_listed.items()
    }
    if set(test_inventory) != set(full_inventory):
        raise ValueError("primary and full compiled inventories have different packages")
    heavy = frozenset(args.heavy_package)
    unknown_heavy = sorted(heavy - set(test_inventory))
    if unknown_heavy:
        raise ValueError(f"heavy packages absent from compiled inventory: {unknown_heavy}")

    baseline_roots: dict[str, set[str]] = collections.defaultdict(set)
    weights: dict[tuple[str, str], float] = {}
    package_elapsed: dict[str, float] = {}
    for event in json_lines(args.baseline):
        package = event.get("Package")
        action = event.get("Action")
        test = event.get("Test")
        if package in test_inventory and not test and action == "pass":
            package_elapsed[str(package)] = max(
                package_elapsed.get(str(package), 0.0), float(event.get("Elapsed") or 0.0)
            )
        if package not in test_inventory or not test or "/" in str(test):
            continue
        key = (str(package), str(test))
        if action == "run":
            baseline_roots[str(package)].add(str(test))
        if action in TERMINAL:
            baseline_roots[str(package)].add(str(test))
            weights[key] = max(weights.get(key, 0.0), float(event.get("Elapsed") or 0.0))

    inventory: dict[str, list[str]] = {}
    inventory_audit: dict[str, Any] = {}
    differences = []
    for package in sorted(test_inventory):
        full = set(full_inventory[package])
        primary = set(test_inventory[package])
        observed = baseline_roots.get(package, set())
        supplemental = {name for name in full if SUPPLEMENTAL_NAME.match(name) and name in observed}
        expected = primary | supplemental
        inventory[package] = sorted(expected)
        outside_test_scope = sorted(full - primary)
        inventory_audit[package] = {
            "primaryTests": sorted(primary),
            "benchmarksExcludedFromDefaultExecution": sorted(
                name for name in outside_test_scope if name.startswith("Benchmark")
            ),
            "examplesOutsidePrimaryTestScope": sorted(
                name for name in outside_test_scope if name.startswith("Example")
            ),
            "fuzzTargetsOutsidePrimaryTestScope": sorted(
                name for name in outside_test_scope if name.startswith("Fuzz")
            ),
            "supplementalDefaultRunnableRoots": sorted(supplemental),
            "otherOutsidePrimaryTestScope": sorted(
                name
                for name in outside_test_scope
                if not name.startswith(("Benchmark", "Example", "Fuzz"))
            ),
        }
        if expected != observed:
            differences.append(
                {
                    "package": package,
                    "missingFromBaseline": sorted(expected - observed),
                    "unexpectedInBaseline": sorted(observed - expected),
                }
            )
    if differences:
        raise ValueError(f"compiled inventory/native baseline mismatch: {differences}")

    items: list[dict[str, Any]] = []
    missing_weights = []
    for package in sorted(inventory):
        if package not in heavy:
            weight = package_elapsed.get(package) or sum(
                max(weights.get((package, test), 0.0), 0.001)
                for test in inventory[package]
            )
            items.append(
                {
                    "kind": "package",
                    "package": package,
                    "tests": [],
                    "weightSeconds": max(weight, 0.001),
                }
            )
            continue
        for test in inventory[package]:
            weight = weights.get((package, test), 0.0)
            if weight <= 0:
                missing_weights.append({"package": package, "test": test})
                weight = 0.001
            items.append(
                {
                    "kind": "test",
                    "package": package,
                    "tests": [test],
                    "weightSeconds": weight,
                }
            )

    bins: list[dict[str, Any]] = [
        {"index": index + 1, "estimatedSeconds": 0.0, "items": []}
        for index in range(args.shards)
    ]
    for item in sorted(
        items,
        key=lambda value: (
            -value["weightSeconds"],
            value["package"],
            value["tests"][0] if value["tests"] else "",
        ),
    ):
        target = min(bins, key=lambda value: (value["estimatedSeconds"], value["index"]))
        target["items"].append(item)
        target["estimatedSeconds"] += item["weightSeconds"]

    assignment_counts: collections.Counter[tuple[str, str]] = collections.Counter()
    monolithic_counts: collections.Counter[str] = collections.Counter()
    rendered_bins = []
    for target in bins:
        grouped: dict[str, dict[str, Any]] = {}
        for item in target["items"]:
            package = item["package"]
            entry = grouped.setdefault(
                package,
                {"package": package, "mode": "selected", "tests": [], "estimatedSeconds": 0.0},
            )
            entry["estimatedSeconds"] += item["weightSeconds"]
            if item["kind"] == "package":
                entry["mode"] = "full"
                monolithic_counts[package] += 1
            else:
                test = item["tests"][0]
                entry["tests"].append(test)
                assignment_counts[(package, test)] += 1
        packages = []
        for package in sorted(grouped):
            entry = grouped[package]
            entry["tests"].sort()
            entry["estimatedSeconds"] = round(entry["estimatedSeconds"], 6)
            packages.append(entry)
        rendered_bins.append(
            {
                "index": target["index"],
                "estimatedSeconds": round(target["estimatedSeconds"], 6),
                "packages": packages,
            }
        )

    coverage_errors = []
    for package in sorted(inventory):
        if package in heavy:
            for test in inventory[package]:
                count = assignment_counts[(package, test)]
                if count != 1:
                    coverage_errors.append(
                        {"package": package, "test": test, "assignmentCount": count}
                    )
        elif monolithic_counts[package] != 1:
            coverage_errors.append(
                {"package": package, "assignmentCount": monolithic_counts[package]}
            )
    if coverage_errors or any(not target["packages"] for target in rendered_bins):
        raise ValueError(
            f"invalid shard coverage: errors={coverage_errors}, "
            f"empty={[x['index'] for x in rendered_bins if not x['packages']]}"
        )

    write(
        args.output,
        {
            "schema": "entire-graph.windows-ci.a3.weighted-shards.v1",
            "shardCount": args.shards,
            "weightsSource": str(args.baseline),
            "heavyPackages": sorted(heavy),
            "inventory": inventory,
            "compiledInventoryAudit": inventory_audit,
            "topLevelRunnableCount": sum(map(len, inventory.values())),
            "missingOrZeroDurationWeights": missing_weights,
            "coverageEquivalent": True,
            "bins": rendered_bins,
        },
    )
    return 0


def path(value: str) -> pathlib.Path:
    return pathlib.Path(value)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline", type=path, required=True)
    parser.add_argument("--inventory", type=path, required=True)
    parser.add_argument("--full-inventory", type=path, required=True)
    parser.add_argument("--output", type=path, required=True)
    parser.add_argument("--shards", type=int, choices=(4, 6, 8), required=True)
    parser.add_argument("--heavy-package", action="append", required=True)
    args = parser.parse_args()
    try:
        return build_plan(args)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"partition-tests: {error}", file=__import__("sys").stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
