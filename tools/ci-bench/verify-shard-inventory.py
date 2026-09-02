#!/usr/bin/env python3
"""Fail-closed coverage-equivalence checks for compile-once test shards."""

from __future__ import annotations

import argparse
import collections
import json
import re
import sys
from pathlib import Path
from typing import Any, Iterable, Iterator


PLAN_SCHEMA = "ci-bench.test-shard-plan.v1"
INVENTORY_SCHEMA = "ci-bench.compiled-test-inventory.v1"
PACKAGE_SCHEMA = "ci-bench.windows-package-inventory.v1"
REPORT_SCHEMA = "ci-bench.shard-equivalence.v1"
_ACTIONS = frozenset({"run", "pass", "fail", "skip"})
_TERMINAL_ACTIONS = frozenset({"pass", "fail", "skip"})


def read_json(path: Path) -> Any:
    try:
        with path.open(encoding="utf-8-sig") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read JSON {path}: {error}") from error


def read_events(paths: Iterable[Path]) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    for path in paths:
        try:
            handle = path.open(encoding="utf-8-sig")
        except OSError as error:
            raise ValueError(f"cannot read JSONL {path}: {error}") from error
        with handle:
            for line_number, line in enumerate(handle, 1):
                if not line.strip():
                    continue
                try:
                    value = json.loads(line)
                except json.JSONDecodeError as error:
                    raise ValueError(f"{path}:{line_number}: invalid JSON: {error}") from error
                if not isinstance(value, dict):
                    raise ValueError(f"{path}:{line_number}: event is not an object")
                events.append(value)
    return events


def dynamic_counter(events: Iterable[dict[str, Any]]) -> collections.Counter[tuple[str, str, str]]:
    result: collections.Counter[tuple[str, str, str]] = collections.Counter()
    for event in events:
        action = event.get("Action")
        package = event.get("Package")
        test = event.get("Test")
        if action in _ACTIONS and isinstance(package, str) and isinstance(test, str):
            result[(action, package, test)] += 1
    return result


def package_terminal_counter(events: Iterable[dict[str, Any]]) -> collections.Counter[tuple[str, str]]:
    result: collections.Counter[tuple[str, str]] = collections.Counter()
    for event in events:
        action = event.get("Action")
        package = event.get("Package")
        if action in _TERMINAL_ACTIONS and isinstance(package, str) and "Test" not in event:
            result[(action, package)] += 1
    return result


def package_terminal_actions(
    counter: collections.Counter[tuple[str, str]],
) -> dict[str, set[str]]:
    result: collections.defaultdict[str, set[str]] = collections.defaultdict(set)
    for (action, package), count in counter.items():
        if count:
            result[package].add(action)
    return dict(result)


def format_counter(counter: collections.Counter[Any], limit: int = 100) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for key, count in sorted(counter.items(), key=lambda item: item[0])[:limit]:
        if len(key) == 3:
            action, package, test = key
            rows.append({"action": action, "package": package, "test": test, "count": count})
        else:
            action, package = key
            rows.append({"action": action, "package": package, "count": count})
    return rows


def walk_exit_codes(value: Any, location: str = "$") -> Iterator[tuple[str, Any]]:
    if isinstance(value, dict):
        for key, child in value.items():
            child_location = f"{location}.{key}"
            if key.lower().endswith("exitcode"):
                yield child_location, child
            yield from walk_exit_codes(child, child_location)
    elif isinstance(value, list):
        for index, child in enumerate(value):
            yield from walk_exit_codes(child, f"{location}[{index}]")


def load_expected_inventories(paths: list[Path]) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for path in paths:
        payload = read_json(path)
        if payload.get("schema") != INVENTORY_SCHEMA:
            raise ValueError(f"{path}: unexpected inventory schema {payload.get('schema')!r}")
        package = payload.get("importPath")
        tests = payload.get("tests")
        if not isinstance(package, str) or not package or not isinstance(tests, list):
            raise ValueError(f"{path}: malformed compiled-test inventory")
        if package in result:
            raise ValueError(f"duplicate inventory package: {package}")
        if any(not isinstance(test, str) for test in tests) or len(tests) != len(set(tests)):
            raise ValueError(f"{path}: tests must be unique strings")
        result[package] = {"tests": set(tests), "path": str(path), "exitCode": payload.get("exitCode")}
    return result


def verify_plan(
    plan: dict[str, Any], inventories: dict[str, dict[str, Any]], failures: list[str]
) -> dict[str, Any]:
    if plan.get("schema") != PLAN_SCHEMA:
        raise ValueError(f"unexpected shard plan schema {plan.get('schema')!r}")
    shards = plan.get("shards")
    if not isinstance(shards, list) or len(shards) != plan.get("shardCount"):
        failures.append("plan shard count does not match its shard array")
        shards = []

    occurrences: collections.defaultdict[tuple[str, str], list[int]] = collections.defaultdict(list)
    regex_mismatches: list[dict[str, Any]] = []
    for position, shard in enumerate(shards):
        if shard.get("index") != position:
            failures.append(f"shard at position {position} has index {shard.get('index')!r}")
        assignments = shard.get("assignments")
        if not isinstance(assignments, list):
            failures.append(f"shard {position} assignments are not a list")
            continue
        seen_package: set[str] = set()
        for assignment in assignments:
            package = assignment.get("importPath")
            tests = assignment.get("tests")
            expression = assignment.get("runRegex")
            if not isinstance(package, str) or package in seen_package:
                failures.append(f"shard {position} has a missing or duplicate package assignment")
                continue
            seen_package.add(package)
            if not isinstance(tests, list) or not isinstance(expression, str):
                failures.append(f"shard {position} package {package} has malformed tests/regex")
                continue
            names = [test.get("name") if isinstance(test, dict) else None for test in tests]
            if any(not isinstance(name, str) for name in names) or len(names) != len(set(names)):
                failures.append(f"shard {position} package {package} has malformed or duplicate tests")
                continue
            try:
                compiled = re.compile(expression)
            except re.error as error:
                failures.append(f"shard {position} package {package} regex is invalid: {error}")
                continue
            package_inventory = inventories.get(package, {}).get("tests", set())
            matched = {test for test in package_inventory if compiled.fullmatch(test)}
            planned = set(names)
            if matched != planned:
                regex_mismatches.append(
                    {
                        "shard": position,
                        "package": package,
                        "missing": sorted(planned - matched),
                        "unexpected": sorted(matched - planned),
                    }
                )
            for name in names:
                occurrences[(package, name)].append(position)

    expected = {
        (package, test)
        for package, inventory in inventories.items()
        for test in inventory["tests"]
    }
    assigned = set(occurrences)
    missing = sorted(f"{package}::{test}" for package, test in expected - assigned)
    unexpected = sorted(f"{package}::{test}" for package, test in assigned - expected)
    duplicated = sorted(
        (
            {"test": f"{package}::{test}", "shards": shards_for_test}
            for (package, test), shards_for_test in occurrences.items()
            if len(shards_for_test) != 1
        ),
        key=lambda item: item["test"],
    )
    if missing:
        failures.append(f"{len(missing)} compiled tests are missing from the plan")
    if unexpected:
        failures.append(f"{len(unexpected)} plan tests are absent from compiled inventories")
    if duplicated:
        failures.append(f"{len(duplicated)} tests are assigned more than once")
    if regex_mismatches:
        failures.append(f"{len(regex_mismatches)} shard regexes select the wrong inventory")
    return {
        "expectedTestCount": len(expected),
        "assignedTestCount": len(assigned),
        "missing": missing,
        "unexpected": unexpected,
        "duplicates": duplicated,
        "regexMismatches": regex_mismatches,
    }


def load_package_inventory(path: Path) -> dict[str, bool]:
    payload = read_json(path)
    if payload.get("schema") != PACKAGE_SCHEMA or not isinstance(payload.get("packages"), list):
        raise ValueError(f"{path}: malformed Windows package inventory")
    packages: dict[str, bool] = {}
    for item in payload["packages"]:
        if not isinstance(item, dict) or not isinstance(item.get("heavy"), bool):
            raise ValueError(f"{path}: package entries must declare boolean heavy state")
        package = item.get("importPath")
        if not isinstance(package, str) or not package or package in packages:
            raise ValueError(f"{path}: package entries must be unique import paths")
        packages[package] = item["heavy"]
    return packages


def package_terminal_multiplicity(
    counter: collections.Counter[tuple[str, str]],
) -> collections.Counter[str]:
    result: collections.Counter[str] = collections.Counter()
    for (_, package), count in counter.items():
        result[package] += count
    return result


def expected_candidate_package_multiplicity(
    plan: dict[str, Any], package_inventory: dict[str, bool]
) -> collections.Counter[str]:
    result: collections.Counter[str] = collections.Counter(
        {package: 1 for package, heavy in package_inventory.items() if not heavy}
    )
    for shard in plan.get("shards", []):
        for assignment in shard.get("assignments", []):
            package = assignment.get("importPath")
            tests = assignment.get("tests")
            if package_inventory.get(package) is True and isinstance(tests, list) and tests:
                result[package] += 1
    return result


def format_multiplicity_mismatches(
    expected: collections.Counter[str], actual: collections.Counter[str]
) -> list[dict[str, Any]]:
    return [
        {"package": package, "expected": expected[package], "actual": actual[package]}
        for package in sorted(set(expected) | set(actual))
        if expected[package] != actual[package]
    ]


def write_report(path: Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as handle:
        json.dump(report, handle, indent=2)
        handle.write("\n")
    temporary.replace(path)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-events", type=Path, required=True)
    parser.add_argument("--candidate-events", type=Path, action="append", required=True)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--inventory", type=Path, action="append", required=True)
    parser.add_argument("--package-inventory", type=Path, required=True)
    parser.add_argument("--metadata", type=Path, action="append", required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    failures: list[str] = []
    report: dict[str, Any] = {
        "schema": REPORT_SCHEMA,
        "equivalent": False,
        "failures": failures,
        "inputs": {
            "baselineEvents": str(args.baseline_events),
            "candidateEvents": [str(path) for path in args.candidate_events],
            "plan": str(args.plan),
            "inventories": [str(path) for path in args.inventory],
            "packageInventory": str(args.package_inventory),
            "metadata": [str(path) for path in args.metadata],
        },
    }
    try:
        inventories = load_expected_inventories(args.inventory)
        for package, inventory in inventories.items():
            if inventory["exitCode"] != 0:
                failures.append(f"compiled inventory listing failed for {package}: {inventory['exitCode']!r}")
        plan = read_json(args.plan)
        report["topLevelInventory"] = verify_plan(plan, inventories, failures)

        baseline_events = read_events([args.baseline_events])
        candidate_events = read_events(args.candidate_events)
        baseline_dynamic = dynamic_counter(baseline_events)
        candidate_dynamic = dynamic_counter(candidate_events)
        missing_dynamic = baseline_dynamic - candidate_dynamic
        extra_dynamic = candidate_dynamic - baseline_dynamic
        if missing_dynamic or extra_dynamic:
            failures.append("fully qualified dynamic run/pass/fail/skip event multisets differ")
        report["dynamicEvents"] = {
            "baselineCount": sum(baseline_dynamic.values()),
            "candidateCount": sum(candidate_dynamic.values()),
            "missing": format_counter(missing_dynamic),
            "extra": format_counter(extra_dynamic),
            "differencesTruncated": len(missing_dynamic) > 100 or len(extra_dynamic) > 100,
        }

        package_inventory = load_package_inventory(args.package_inventory)
        expected_packages = set(package_inventory)
        declared_heavy_packages = {
            package for package, heavy in package_inventory.items() if heavy
        }
        if declared_heavy_packages != set(inventories):
            failures.append("compiled heavy-package inventories differ from target-Windows heavy declarations")
        baseline_packages = {package for _, package in package_terminal_counter(baseline_events)}
        candidate_packages = {package for _, package in package_terminal_counter(candidate_events)}
        for label, actual in (("baseline", baseline_packages), ("candidate", candidate_packages)):
            if actual != expected_packages:
                failures.append(f"{label} package result inventory differs from target-Windows go list inventory")
        baseline_package_results = package_terminal_counter(baseline_events)
        candidate_package_results = package_terminal_counter(candidate_events)
        baseline_multiplicity = package_terminal_multiplicity(baseline_package_results)
        candidate_multiplicity = package_terminal_multiplicity(candidate_package_results)
        expected_baseline_multiplicity = collections.Counter(
            {package: 1 for package in expected_packages}
        )
        expected_candidate_multiplicity = expected_candidate_package_multiplicity(
            plan, package_inventory
        )
        baseline_multiplicity_mismatches = format_multiplicity_mismatches(
            expected_baseline_multiplicity, baseline_multiplicity
        )
        candidate_multiplicity_mismatches = format_multiplicity_mismatches(
            expected_candidate_multiplicity, candidate_multiplicity
        )
        if baseline_multiplicity_mismatches:
            failures.append("baseline package terminal multiplicity is not exactly once per package")
        if candidate_multiplicity_mismatches:
            failures.append("candidate package terminal multiplicity differs from planned process assignments")
        baseline_outcomes = package_terminal_actions(baseline_package_results)
        candidate_outcomes = package_terminal_actions(candidate_package_results)
        outcome_mismatches = [
            {
                "package": package,
                "baseline": sorted(baseline_outcomes.get(package, set())),
                "candidate": sorted(candidate_outcomes.get(package, set())),
            }
            for package in sorted(expected_packages)
            if baseline_outcomes.get(package, set()) != candidate_outcomes.get(package, set())
        ]
        if outcome_mismatches:
            failures.append("package terminal outcome sets differ between baseline and candidate")
        # `go test -json` reports packages without test files as package-level
        # skips with a successful process exit. Preserve and compare those
        # skips; only a package-level fail is intrinsically bad.
        baseline_bad_results = collections.Counter(
            {key: count for key, count in baseline_package_results.items() if key[0] == "fail"}
        )
        candidate_bad_results = collections.Counter(
            {key: count for key, count in candidate_package_results.items() if key[0] == "fail"}
        )
        if baseline_bad_results:
            failures.append("baseline contains non-passing package terminal results")
        if candidate_bad_results:
            failures.append("candidate contains non-passing package terminal results")
        report["packages"] = {
            "expected": sorted(expected_packages),
            "baselineMissing": sorted(expected_packages - baseline_packages),
            "baselineUnexpected": sorted(baseline_packages - expected_packages),
            "candidateMissing": sorted(expected_packages - candidate_packages),
            "candidateUnexpected": sorted(candidate_packages - expected_packages),
            "baselineTerminalResults": format_counter(baseline_package_results),
            "candidateTerminalResults": format_counter(candidate_package_results),
            "baselineMultiplicityMismatches": baseline_multiplicity_mismatches,
            "candidateMultiplicityMismatches": candidate_multiplicity_mismatches,
            "outcomeMismatches": outcome_mismatches,
            "baselineFailedResults": format_counter(baseline_bad_results),
            "candidateFailedResults": format_counter(candidate_bad_results),
        }

        exit_checks: list[dict[str, Any]] = []
        for path in args.metadata:
            payload = read_json(path)
            codes = list(walk_exit_codes(payload))
            if not codes:
                failures.append(f"metadata has no exit-code fields: {path}")
            for location, value in codes:
                passed = isinstance(value, int) and not isinstance(value, bool) and value == 0
                exit_checks.append(
                    {"path": str(path), "location": location, "value": value, "passed": passed}
                )
                if not passed:
                    failures.append(f"nonzero, null, or non-integer exit code at {path}:{location}: {value!r}")
        report["exitCodes"] = exit_checks
        report["equivalent"] = not failures
    except (ValueError, OSError) as error:
        failures.append(str(error))
    try:
        write_report(args.output, report)
    except OSError as error:
        print(f"error: cannot write report {args.output}: {error}", file=sys.stderr)
        return 2
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1
    print("coverage-equivalence verification passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
