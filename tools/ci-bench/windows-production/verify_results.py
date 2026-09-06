#!/usr/bin/env python3
"""Verify baseline-free coverage and integrity for production Windows test shards."""

from __future__ import annotations

import argparse
import collections
import json
import re
import sys
from pathlib import Path
from typing import Any, Iterable

import plan_shards


PACKAGE_SCHEMA = "entire-graph.windows-ci.package-inventory.v1"
PREPARE_SCHEMA = "entire-graph.windows-ci.prepare-metadata.v1"
SHARD_RUN_SCHEMA = "entire-graph.windows-ci.shard-run.v1"
OTHER_RUN_SCHEMA = "entire-graph.windows-ci.other-run.v1"
REPORT_SCHEMA = "entire-graph.windows-ci.verification.v1"
TERMINAL_ACTIONS = frozenset({"pass", "fail", "skip"})
TARGET_ENVIRONMENT = {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOOS": "windows"}


def read_json(path: Path) -> Any:
    try:
        with path.open(encoding="utf-8-sig") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read JSON {path}: {error}") from error


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
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
    if not events:
        raise ValueError(f"{path}: event stream is empty")
    return events


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as handle:
        json.dump(value, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
    temporary.replace(path)


def verify_required_zero_exit(
    payload: Any,
    key: str,
    label: str,
    location: str,
    failures: list[str],
    exit_report: list[dict[str, Any]],
) -> None:
    present = isinstance(payload, dict) and key in payload
    value = payload.get(key) if present else None
    passed = present and isinstance(value, int) and not isinstance(value, bool) and value == 0
    exit_report.append(
        {
            "input": label,
            "location": location,
            "present": present,
            "value": value,
            "passed": passed,
        }
    )
    if not present:
        failures.append(f"{label} is missing required exit field {location}")
    elif not passed:
        failures.append(f"{label} has nonzero or non-integer exit at {location}: {value!r}")


def verify_prepare_operation_exits(
    prepare: dict[str, Any],
    heavy_packages: set[str],
    label: str,
    failures: list[str],
    exit_report: list[dict[str, Any]],
) -> None:
    verify_required_zero_exit(prepare, "exitCode", label, "$.exitCode", failures, exit_report)
    operations = prepare.get("operations")
    if not isinstance(operations, list):
        failures.append(f"{label} operations are not a list")
        return
    actual: collections.Counter[tuple[Any, Any]] = collections.Counter()
    for index, operation in enumerate(operations):
        if not isinstance(operation, dict):
            failures.append(f"{label} operation {index} is not an object")
            continue
        phase = operation.get("phase")
        package = operation.get("package")
        actual[(phase, package)] += 1
        verify_required_zero_exit(
            operation,
            "exitCode",
            label,
            f"$.operations[{index}].exitCode",
            failures,
            exit_report,
        )
    expected: collections.Counter[tuple[Any, Any]] = collections.Counter(
        {
            ("worktree-clean-before", None): 1,
            ("repository-sha", None): 1,
            ("repository-sha-after", None): 1,
            ("go-version", None): 1,
            ("go-environment", None): 1,
            ("go-list-all", None): 1,
            ("worktree-clean-after", None): 1,
        }
    )
    for package in heavy_packages:
        for phase in ("go-list-package", "testmain-inventory", "compile", "list"):
            expected[(phase, package)] += 1
    if actual != expected:
        failures.append("prepare operation phase/package set differs from the required schema")


def validate_plan(
    plan: dict[str, Any],
    inventories: list[dict[str, Any]],
    failures: list[str],
) -> tuple[
    dict[tuple[str, str], int],
    dict[tuple[int, str], dict[str, Any]],
    dict[str, dict[str, str]],
]:
    inventory_by_package = {item["importPath"]: item for item in inventories}
    root_details = {
        package: {root["name"]: root["kind"] for root in inventory["roots"]}
        for package, inventory in inventory_by_package.items()
    }
    if plan.get("schema") != plan_shards.PLAN_SCHEMA:
        raise ValueError(f"unexpected shard plan schema {plan.get('schema')!r}")
    if plan.get("targetEnvironment") != TARGET_ENVIRONMENT:
        failures.append("plan target is not exactly windows/amd64 with CGO_ENABLED=1")
    settings = plan.get("settings")
    if not isinstance(settings, dict) or settings.get("shuffle") != "off":
        failures.append("plan does not preserve shuffle=off semantics")
    if plan.get("inventorySetSha256") != plan_shards._inventory_digest(inventories):
        failures.append("plan inventory digest does not match compiled inventories")
    plan_packages = plan.get("packages")
    if not isinstance(plan_packages, list):
        failures.append("plan packages are not a list")
        plan_packages = []
    normalized_plan_packages: dict[str, dict[str, Any]] = {}
    for item in plan_packages:
        if not isinstance(item, dict) or not isinstance(item.get("importPath"), str):
            failures.append("plan contains a malformed package entry")
            continue
        package = item["importPath"]
        if package in normalized_plan_packages:
            failures.append(f"plan declares package {package} more than once")
        normalized_plan_packages[package] = item
    if set(normalized_plan_packages) != set(inventory_by_package):
        failures.append("plan package set differs from compiled inventories")
    for package in sorted(set(normalized_plan_packages) & set(inventory_by_package)):
        planned, inventory = normalized_plan_packages[package], inventory_by_package[package]
        for key in (
            "packageDirectoryRelative",
            "binaryName",
            "binarySha256",
            "binarySizeBytes",
            "testMainDeclarations",
        ):
            if planned.get(key) != inventory.get(key):
                failures.append(f"plan {package} {key} differs from compiled inventory")
        if planned.get("rootCount") != len(inventory["roots"]):
            failures.append(f"plan {package} root count differs from compiled inventory")

    shards = plan.get("shards")
    shard_count = plan.get("shardCount")
    if (
        not isinstance(shard_count, int)
        or isinstance(shard_count, bool)
        or not isinstance(shards, list)
        or len(shards) != shard_count
    ):
        failures.append("plan shardCount does not match the shard array")
        shards = []
    assignment_for_root: dict[tuple[str, str], int] = {}
    process_assignments: dict[tuple[int, str], dict[str, Any]] = {}
    duplicate_roots: list[str] = []
    regex_mismatches: list[str] = []
    for position, shard in enumerate(shards):
        if not isinstance(shard, dict) or shard.get("index") != position:
            failures.append(f"plan shard at position {position} has a mismatched index")
            continue
        assignments = shard.get("assignments")
        if not isinstance(assignments, list) or not assignments:
            failures.append(f"plan shard {position} has no assignments")
            continue
        seen_packages: set[str] = set()
        counted_roots = 0
        for assignment in assignments:
            if not isinstance(assignment, dict):
                failures.append(f"plan shard {position} contains a malformed assignment")
                continue
            package = assignment.get("importPath")
            if not isinstance(package, str) or package not in inventory_by_package:
                failures.append(f"plan shard {position} references unknown package {package!r}")
                continue
            if package in seen_packages:
                failures.append(f"plan shard {position} assigns package {package} more than once")
            seen_packages.add(package)
            process_assignments[(position, package)] = assignment
            inventory = inventory_by_package[package]
            for key in ("packageDirectoryRelative", "binaryName", "binarySha256"):
                if assignment.get(key) != inventory.get(key):
                    failures.append(f"plan shard {position} {package} {key} differs from inventory")
            roots = assignment.get("roots")
            expression = assignment.get("runRegex")
            if not isinstance(roots, list) or not roots or not isinstance(expression, str):
                failures.append(f"plan shard {position} {package} has malformed roots or regex")
                continue
            names: list[str] = []
            for root in roots:
                name = root.get("name") if isinstance(root, dict) else None
                kind = root.get("kind") if isinstance(root, dict) else None
                if (
                    not isinstance(name, str)
                    or name not in root_details[package]
                    or kind != root_details[package].get(name)
                ):
                    failures.append(f"plan shard {position} {package} contains an unknown root")
                    continue
                key = (package, name)
                if key in assignment_for_root:
                    duplicate_roots.append(f"{package}::{name}")
                assignment_for_root[key] = position
                names.append(name)
            counted_roots += len(names)
            if len(names) != len(set(names)):
                failures.append(f"plan shard {position} {package} repeats a root")
            try:
                compiled = re.compile(expression)
            except re.error as error:
                failures.append(f"plan shard {position} {package} has invalid regex: {error}")
                continue
            selected = {name for name in root_details[package] if compiled.fullmatch(name)}
            if selected != set(names):
                regex_mismatches.append(f"shard {position} {package}")
        if shard.get("rootCount") != counted_roots:
            failures.append(f"plan shard {position} rootCount differs from assignments")
    expected_roots = {
        (package, name) for package, names in root_details.items() for name in names
    }
    missing_roots = expected_roots - set(assignment_for_root)
    unexpected_roots = set(assignment_for_root) - expected_roots
    if missing_roots:
        failures.append(f"plan omits {len(missing_roots)} compiled runnable roots")
    if unexpected_roots:
        failures.append(f"plan adds {len(unexpected_roots)} unknown runnable roots")
    if duplicate_roots:
        failures.append(f"plan assigns {len(duplicate_roots)} runnable roots more than once")
    if regex_mismatches:
        failures.append(f"{len(regex_mismatches)} plan regexes select a different root set")
    if plan.get("rootCount") != len(expected_roots):
        failures.append("plan rootCount differs from compiled inventories")
    return assignment_for_root, process_assignments, root_details


def verify_shards(
    root: Path,
    plan: dict[str, Any],
    plan_sha256: str,
    inventories: list[dict[str, Any]],
    assignment_for_root: dict[tuple[str, str], int],
    process_assignments: dict[tuple[int, str], dict[str, Any]],
    root_details: dict[str, dict[str, str]],
    failures: list[str],
    exit_report: list[dict[str, Any]],
) -> dict[str, Any]:
    metadata_paths = sorted(root.rglob("shard-metadata.json"))
    shard_count = plan.get("shardCount")
    if len(metadata_paths) != shard_count:
        failures.append(f"found {len(metadata_paths)} shard metadata files, want {shard_count}")
    by_index: dict[int, tuple[dict[str, Any], Path]] = {}
    top_runs: collections.Counter[tuple[str, str]] = collections.Counter()
    top_terminals: collections.Counter[tuple[str, str, str]] = collections.Counter()
    package_starts: collections.Counter[tuple[int, str]] = collections.Counter()
    package_terminals: collections.Counter[tuple[int, str, str]] = collections.Counter()
    unexpected_event_roots: set[str] = set()
    event_count = 0
    for metadata_path in metadata_paths:
        metadata = read_json(metadata_path)
        if not isinstance(metadata, dict) or metadata.get("schema") != SHARD_RUN_SCHEMA:
            failures.append(f"{metadata_path}: unexpected shard metadata schema")
            continue
        verify_required_zero_exit(
            metadata,
            "exitCode",
            str(metadata_path),
            "$.exitCode",
            failures,
            exit_report,
        )
        verify_required_zero_exit(
            metadata,
            "cleanTestCacheExitCode",
            str(metadata_path),
            "$.cleanTestCacheExitCode",
            failures,
            exit_report,
        )
        index = metadata.get("shardIndex")
        if not isinstance(index, int) or isinstance(index, bool) or not 0 <= index < shard_count:
            failures.append(f"{metadata_path}: invalid shard index {index!r}")
            continue
        if index in by_index:
            failures.append(f"shard {index} has duplicate metadata artifacts")
            continue
        by_index[index] = (metadata, metadata_path)
        if metadata.get("errors") != []:
            failures.append(f"shard {index} metadata contains errors")
        if metadata.get("repositorySha") != plan.get("repositorySha"):
            failures.append(f"shard {index} repository SHA differs from plan")
        if metadata.get("repositoryShaAfter") != plan.get("repositorySha"):
            failures.append(f"shard {index} post-run repository SHA differs from plan")
        if metadata.get("goVersion") != plan.get("goVersion"):
            failures.append(f"shard {index} Go version differs from plan")
        if metadata.get("targetEnvironment") != TARGET_ENVIRONMENT:
            failures.append(f"shard {index} target is not exactly windows/amd64 with CGO_ENABLED=1")
        if metadata.get("targetEnvironment") != plan.get("targetEnvironment"):
            failures.append(f"shard {index} target differs from plan")
        if metadata.get("trackedWorktreeCleanBefore") is not True:
            failures.append(f"shard {index} did not prove a clean tracked worktree before execution")
        if metadata.get("trackedWorktreeCleanAfter") is not True:
            failures.append(f"shard {index} did not prove a clean tracked worktree after execution")
        if not isinstance(metadata.get("goToolDirectory"), str) or not metadata["goToolDirectory"]:
            failures.append(f"shard {index} omitted its resolved Go tool directory")
        if metadata.get("planSha256") != plan_sha256:
            failures.append(f"shard {index} plan hash differs from the downloaded bundle")
        if metadata.get("shardCount") != shard_count:
            failures.append(f"shard {index} metadata has the wrong shard count")
        for key in ("timeout", "shuffle", "testParallel", "goMaxProcs", "commandLineLimit"):
            if metadata.get(key) != plan.get("settings", {}).get(key):
                failures.append(f"shard {index} metadata setting {key} differs from plan")
        expected_assignments = {
            package: assignment
            for (assignment_index, package), assignment in process_assignments.items()
            if assignment_index == index
        }
        invocations = metadata.get("invocations")
        if not isinstance(invocations, list):
            failures.append(f"shard {index} invocations are not a list")
            invocations = []
        actual_invocations: dict[str, dict[str, Any]] = {}
        for invocation_index, invocation in enumerate(invocations):
            verify_required_zero_exit(
                invocation,
                "exitCode",
                str(metadata_path),
                f"$.invocations[{invocation_index}].exitCode",
                failures,
                exit_report,
            )
            package = invocation.get("package") if isinstance(invocation, dict) else None
            if not isinstance(package, str) or package in actual_invocations:
                failures.append(f"shard {index} has a malformed or duplicate invocation")
                continue
            actual_invocations[package] = invocation
            expected = expected_assignments.get(package)
            if expected is None:
                failures.append(f"shard {index} invoked unexpected package {package}")
                continue
            if invocation.get("binaryName") != expected.get("binaryName"):
                failures.append(f"shard {index} {package} binary name differs from plan")
            if invocation.get("binarySha256") != expected.get("binarySha256"):
                failures.append(f"shard {index} {package} binary hash differs from plan")
            if invocation.get("rootCount") != len(expected.get("roots", [])):
                failures.append(f"shard {index} {package} root count differs from plan")
            if invocation.get("workingDirectoryRelative") != expected.get(
                "packageDirectoryRelative"
            ):
                failures.append(f"shard {index} {package} working directory differs from plan")
            if invocation.get("paniconexit0") is not True:
                failures.append(f"shard {index} {package} omitted -test.paniconexit0")
            if invocation.get("pwdMatchesPackageDirectory") is not True:
                failures.append(f"shard {index} {package} did not set PWD to its package directory")
            if invocation.get("goToolDirectoryPrependedToPath") is not True:
                failures.append(f"shard {index} {package} did not prepend the Go tool directory to PATH")
            command_units = invocation.get("commandLineUtf16Units")
            if (
                not isinstance(command_units, int)
                or isinstance(command_units, bool)
                or not 1 <= command_units <= plan["settings"]["commandLineLimit"]
            ):
                failures.append(f"shard {index} {package} command line exceeded or omitted its budget")
        if set(actual_invocations) != set(expected_assignments):
            failures.append(f"shard {index} invocation package set differs from plan")

        events_path = metadata_path.with_name("shard-events.jsonl")
        events = read_jsonl(events_path)
        event_count += len(events)
        expected_packages = set(expected_assignments)
        for event in events:
            action = event.get("Action")
            package = event.get("Package")
            if not isinstance(action, str) or not isinstance(package, str):
                failures.append(f"shard {index} contains an event without Action or Package")
                continue
            if package not in expected_packages:
                failures.append(f"shard {index} emitted an unexpected package {package}")
                continue
            test = event.get("Test")
            if test is None:
                if action == "start":
                    package_starts[(index, package)] += 1
                elif action in TERMINAL_ACTIONS:
                    package_terminals[(index, package, action)] += 1
                    if action != "pass":
                        failures.append(f"shard {index} package {package} terminated with {action}")
                continue
            if not isinstance(test, str) or not test:
                failures.append(f"shard {index} {package} has a malformed test event")
                continue
            root = test.split("/", 1)[0]
            key = (package, root)
            if root not in root_details.get(package, {}):
                unexpected_event_roots.add(f"{package}::{root}")
                continue
            if assignment_for_root.get(key) != index:
                failures.append(f"shard {index} emitted unassigned root {package}::{root}")
            if action == "fail":
                failures.append(f"shard {index} has failed test event {package}::{test}")
            if test == root:
                if action == "run":
                    top_runs[key] += 1
                elif action in TERMINAL_ACTIONS:
                    top_terminals[(package, root, action)] += 1
    if set(by_index) != set(range(shard_count)):
        failures.append("shard result indexes are incomplete")
    if unexpected_event_roots:
        failures.append(f"shards emitted {len(unexpected_event_roots)} roots absent from compiled inventories")

    root_mismatches: list[dict[str, Any]] = []
    for key, assigned_shard in sorted(assignment_for_root.items()):
        package, name = key
        run_count = top_runs[key]
        terminals = {
            action: top_terminals[(package, name, action)] for action in sorted(TERMINAL_ACTIONS)
        }
        if run_count != 1 or sum(terminals.values()) != 1 or terminals["fail"] != 0:
            root_mismatches.append(
                {
                    "root": f"{package}::{name}",
                    "shard": assigned_shard,
                    "runCount": run_count,
                    "terminalCounts": terminals,
                }
            )
    if root_mismatches:
        failures.append(f"{len(root_mismatches)} planned roots lack exactly one run and successful terminal")

    process_mismatches: list[dict[str, Any]] = []
    for index, package in sorted(process_assignments):
        terminals = {
            action: package_terminals[(index, package, action)] for action in sorted(TERMINAL_ACTIONS)
        }
        if package_starts[(index, package)] != 1 or terminals != {"fail": 0, "pass": 1, "skip": 0}:
            process_mismatches.append(
                {
                    "shard": index,
                    "package": package,
                    "startCount": package_starts[(index, package)],
                    "terminalCounts": terminals,
                }
            )
    if process_mismatches:
        failures.append(f"{len(process_mismatches)} shard package processes have wrong start/terminal multiplicity")
    return {
        "metadataCount": len(metadata_paths),
        "eventCount": event_count,
        "expectedRootCount": len(assignment_for_root),
        "topLevelRunCount": sum(top_runs.values()),
        "topLevelTerminalCount": sum(top_terminals.values()),
        "unexpectedRoots": sorted(unexpected_event_roots)[:100],
        "rootMismatches": root_mismatches[:100],
        "processMismatches": process_mismatches[:100],
        "differencesTruncated": len(root_mismatches) > 100 or len(process_mismatches) > 100,
    }


def verify_other(
    root: Path,
    plan: dict[str, Any],
    plan_sha256: str,
    package_inventory: dict[str, Any],
    package_inventory_sha256: str,
    failures: list[str],
    exit_report: list[dict[str, Any]],
) -> dict[str, Any]:
    metadata_paths = sorted(root.rglob("other-metadata.json"))
    event_paths = sorted(root.rglob("other-events.jsonl"))
    if len(metadata_paths) != 1 or len(event_paths) != 1:
        raise ValueError(
            f"non-heavy artifact has {len(metadata_paths)} metadata and {len(event_paths)} event files, want one each"
        )
    metadata = read_json(metadata_paths[0])
    if not isinstance(metadata, dict) or metadata.get("schema") != OTHER_RUN_SCHEMA:
        raise ValueError("unexpected non-heavy metadata schema")
    for key in ("exitCode", "cleanTestCacheExitCode", "testExitCode"):
        verify_required_zero_exit(
            metadata,
            key,
            str(metadata_paths[0]),
            f"$.{key}",
            failures,
            exit_report,
        )
    if metadata.get("errors") != []:
        failures.append("non-heavy metadata contains errors")
    if metadata.get("repositorySha") != plan.get("repositorySha"):
        failures.append("non-heavy repository SHA differs from plan")
    if metadata.get("repositoryShaAfter") != plan.get("repositorySha"):
        failures.append("non-heavy post-run repository SHA differs from plan")
    if metadata.get("goVersion") != plan.get("goVersion"):
        failures.append("non-heavy Go version differs from plan")
    if metadata.get("targetEnvironment") != TARGET_ENVIRONMENT:
        failures.append("non-heavy target is not exactly windows/amd64 with CGO_ENABLED=1")
    if metadata.get("targetEnvironment") != plan.get("targetEnvironment"):
        failures.append("non-heavy target differs from plan")
    if metadata.get("trackedWorktreeCleanBefore") is not True:
        failures.append("non-heavy run did not prove a clean tracked worktree before execution")
    if metadata.get("trackedWorktreeCleanAfter") is not True:
        failures.append("non-heavy run did not prove a clean tracked worktree after execution")
    if metadata.get("planSha256") != plan_sha256:
        failures.append("non-heavy plan hash differs from the downloaded bundle")
    if metadata.get("packageInventorySha256") != package_inventory_sha256:
        failures.append("non-heavy package-inventory hash differs from the downloaded bundle")
    for key in ("timeout", "shuffle", "testParallel", "goMaxProcs", "commandLineLimit"):
        if metadata.get(key) != plan.get("settings", {}).get(key):
            failures.append(f"non-heavy metadata setting {key} differs from plan")
    command_units = metadata.get("commandLineUtf16Units")
    if (
        not isinstance(command_units, int)
        or isinstance(command_units, bool)
        or not 1 <= command_units <= plan["settings"]["commandLineLimit"]
    ):
        failures.append("non-heavy command line exceeded or omitted its budget")

    expected_packages = sorted(
        row["importPath"] for row in package_inventory["packages"] if not row["heavy"]
    )
    if metadata.get("expectedPackages") != expected_packages:
        failures.append("non-heavy metadata package list differs from target-Windows inventory")
    events = read_jsonl(event_paths[0])
    starts: collections.Counter[str] = collections.Counter()
    terminals: collections.Counter[tuple[str, str]] = collections.Counter()
    unexpected_packages: set[str] = set()
    benchmark_roots: set[str] = set()
    failed_tests: set[str] = set()
    build_output_count = 0
    for event in events:
        action, package = event.get("Action"), event.get("Package")
        if not isinstance(action, str):
            failures.append("non-heavy stream contains an event without Action or Package")
            continue
        if not isinstance(package, str):
            if (
                action == "build-output"
                and isinstance(event.get("ImportPath"), str)
                and event["ImportPath"]
                and isinstance(event.get("Output"), str)
            ):
                build_output_count += 1
                continue
            failures.append("non-heavy stream contains an event without Action or Package")
            continue
        if package not in expected_packages:
            unexpected_packages.add(package)
            continue
        test = event.get("Test")
        if test is None:
            if action == "start":
                starts[package] += 1
            elif action in TERMINAL_ACTIONS:
                terminals[(package, action)] += 1
                if action == "fail":
                    failures.append(f"non-heavy package {package} failed")
        elif isinstance(test, str):
            root = test.split("/", 1)[0]
            if root.startswith("Benchmark") and action in {"run", *TERMINAL_ACTIONS}:
                benchmark_roots.add(f"{package}::{root}")
            if action == "fail":
                failed_tests.add(f"{package}::{test}")
        else:
            failures.append("non-heavy stream contains a malformed Test field")
    if unexpected_packages:
        failures.append(f"non-heavy stream emitted {len(unexpected_packages)} unexpected packages")
    if benchmark_roots:
        failures.append(f"non-heavy default invocation ran {len(benchmark_roots)} benchmarks")
    if failed_tests:
        failures.append(f"non-heavy stream contains {len(failed_tests)} failed test events")
    package_mismatches: list[dict[str, Any]] = []
    for package in expected_packages:
        counts = {action: terminals[(package, action)] for action in sorted(TERMINAL_ACTIONS)}
        if starts[package] != 1 or sum(counts.values()) != 1 or counts["fail"] != 0:
            package_mismatches.append(
                {"package": package, "startCount": starts[package], "terminalCounts": counts}
            )
    if package_mismatches:
        failures.append(f"{len(package_mismatches)} non-heavy packages lack exactly one start and successful terminal")
    return {
        "eventCount": len(events),
        "buildOutputCount": build_output_count,
        "expectedPackageCount": len(expected_packages),
        "unexpectedPackages": sorted(unexpected_packages),
        "benchmarkRoots": sorted(benchmark_roots),
        "failedTests": sorted(failed_tests)[:100],
        "packageMismatches": package_mismatches[:100],
        "differencesTruncated": len(failed_tests) > 100 or len(package_mismatches) > 100,
    }


def verify(args: argparse.Namespace) -> dict[str, Any]:
    failures: list[str] = []
    exit_report: list[dict[str, Any]] = []
    report: dict[str, Any] = {
        "schema": REPORT_SCHEMA,
        "passed": False,
        "failures": failures,
    }
    bundle = args.bundle.resolve(strict=True)
    plan_path = bundle / "plan.json"
    package_path = bundle / "package-inventory.json"
    prepare_path = bundle / "prepare-metadata.json"
    settings_path = bundle / "settings.json"
    weights_path = bundle / "historical-weights.json"
    inventory_paths = sorted((bundle / "inventories").glob("*.inventory.json"))
    if not inventory_paths:
        raise ValueError("bundle contains no compiled inventories")
    inventories = plan_shards.load_inventories(inventory_paths)
    plan = read_json(plan_path)
    if not isinstance(plan, dict):
        raise ValueError("plan is not an object")
    settings = plan_shards.load_settings(settings_path)
    if plan.get("shardCount") != settings.get("shardCount"):
        failures.append("bundled settings shard count differs from plan")
    for key in (
        "timeout",
        "commandLineLimit",
        "shuffle",
        "testParallel",
        "goMaxProcs",
        "defaultWeightSeconds",
    ):
        if plan.get("settings", {}).get(key) != settings.get(key):
            failures.append(f"bundled setting {key} differs from plan")
    if plan.get("historicalWeights", {}).get("sha256") != plan_shards.sha256_file(weights_path):
        failures.append("bundled historical-weight hash differs from plan")

    prepare = read_json(prepare_path)
    if not isinstance(prepare, dict) or prepare.get("schema") != PREPARE_SCHEMA:
        failures.append("unexpected prepare metadata schema")
    else:
        verify_prepare_operation_exits(
            prepare,
            {inventory["importPath"] for inventory in inventories},
            str(prepare_path),
            failures,
            exit_report,
        )
        if prepare.get("repositorySha") != plan.get("repositorySha"):
            failures.append("prepare repository SHA differs from plan")
        if prepare.get("repositoryShaAfter") != plan.get("repositorySha"):
            failures.append("prepare post-run repository SHA differs from plan")
        if prepare.get("goVersion") != plan.get("goVersion"):
            failures.append("prepare Go version differs from plan")
        if prepare.get("targetEnvironment") != TARGET_ENVIRONMENT:
            failures.append("prepare target is not exactly windows/amd64 with CGO_ENABLED=1")
        if prepare.get("targetEnvironment") != plan.get("targetEnvironment"):
            failures.append("prepare target differs from plan")
        if prepare.get("trackedWorktreeCleanBefore") is not True:
            failures.append("prepare did not prove a clean tracked worktree before execution")
        if prepare.get("trackedWorktreeCleanAfter") is not True:
            failures.append("prepare did not prove a clean tracked worktree after execution")
        if not isinstance(prepare.get("goToolDirectory"), str) or not prepare["goToolDirectory"]:
            failures.append("prepare omitted its resolved Go tool directory")

    for inventory in inventories:
        binary = bundle / "binaries" / inventory["binaryName"]
        if not binary.is_file():
            failures.append(f"compiled binary is missing: {inventory['binaryName']}")
            continue
        if binary.stat().st_size != inventory["binarySizeBytes"]:
            failures.append(f"compiled binary size differs from inventory: {inventory['binaryName']}")
        if plan_shards.sha256_file(binary) != inventory["binarySha256"]:
            failures.append(f"compiled binary hash differs from inventory: {inventory['binaryName']}")

    assignment_for_root, process_assignments, root_details = validate_plan(
        plan, inventories, failures
    )
    package_inventory = read_json(package_path)
    if not isinstance(package_inventory, dict) or package_inventory.get("schema") != PACKAGE_SCHEMA:
        raise ValueError("unexpected target-Windows package inventory schema")
    if package_inventory.get("repositorySha") != plan.get("repositorySha"):
        failures.append("package inventory repository SHA differs from plan")
    if package_inventory.get("goVersion") != plan.get("goVersion"):
        failures.append("package inventory Go version differs from plan")
    package_target = {
        "GOOS": package_inventory.get("goos"),
        "GOARCH": package_inventory.get("goarch"),
        "CGO_ENABLED": package_inventory.get("cgoEnabled"),
    }
    if package_target != TARGET_ENVIRONMENT:
        failures.append("package inventory target is not exactly windows/amd64 with CGO_ENABLED=1")
    if package_target != plan.get("targetEnvironment"):
        failures.append("package inventory target differs from plan")
    package_rows = package_inventory.get("packages")
    if not isinstance(package_rows, list) or not package_rows:
        raise ValueError("target-Windows package inventory is empty")
    package_names: set[str] = set()
    heavy_names: set[str] = set()
    for row in package_rows:
        if not isinstance(row, dict) or not isinstance(row.get("heavy"), bool):
            raise ValueError("target-Windows package inventory contains a malformed row")
        package = row.get("importPath")
        if not isinstance(package, str) or not package or package in package_names:
            raise ValueError("target-Windows package inventory contains a missing or duplicate package")
        package_names.add(package)
        if row["heavy"]:
            heavy_names.add(package)
    if heavy_names != set(root_details):
        failures.append("target-Windows heavy package declarations differ from compiled inventories")
    configured_test_mains = {
        item["importPath"]: item["expectedTestMainDeclarations"]
        for item in settings["heavyPackages"]
    }
    inventory_test_mains = {
        item["importPath"]: item["testMainDeclarations"] for item in inventories
    }
    if configured_test_mains != inventory_test_mains:
        failures.append("compiled TestMain declarations differ from bundled settings")

    report["shards"] = verify_shards(
        args.shard_results.resolve(strict=True),
        plan,
        plan_shards.sha256_file(plan_path),
        inventories,
        assignment_for_root,
        process_assignments,
        root_details,
        failures,
        exit_report,
    )
    report["otherPackages"] = verify_other(
        args.other_results.resolve(strict=True),
        plan,
        plan_shards.sha256_file(plan_path),
        package_inventory,
        plan_shards.sha256_file(package_path),
        failures,
        exit_report,
    )
    report["inventory"] = {
        "repositorySha": plan.get("repositorySha"),
        "goVersion": plan.get("goVersion"),
        "targetEnvironment": plan.get("targetEnvironment"),
        "packageCount": len(package_names),
        "heavyPackageCount": len(heavy_names),
        "runnableRootCount": len(assignment_for_root),
        "excludedBenchmarkCount": sum(len(item["excludedBenchmarks"]) for item in inventories),
        "historicallyWeightedCount": plan.get("historicalWeights", {}).get("usedCount"),
        "defaultWeightedCount": plan.get("historicalWeights", {}).get("defaultedCount"),
    }
    report["exitCodes"] = exit_report
    report["passed"] = not failures
    return report


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--shard-results", type=Path, required=True)
    parser.add_argument("--other-results", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        report = verify(args)
    except (ValueError, OSError, KeyError, TypeError) as error:
        report = {"schema": REPORT_SCHEMA, "passed": False, "failures": [str(error)]}
    try:
        write_json(args.output, report)
    except OSError as error:
        print(f"error: cannot write report {args.output}: {error}", file=sys.stderr)
        return 2
    if not report.get("passed"):
        for failure in report.get("failures", []):
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1
    print("Windows shard coverage and integrity verification passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
