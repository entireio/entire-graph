#!/usr/bin/env python3
"""Normalize and compare the evidence used by the A3 cross-compile experiment.

The tool intentionally compares dynamic test events as a multiset. A set would
hide accidental duplicate execution, while a simple list would be sensitive to
the harmless interleaving produced by ``go test -json``.
"""

from __future__ import annotations

import argparse
import collections
import json
import pathlib
import sys
from typing import Any, Iterable


TEST_ACTIONS = frozenset({"run", "pass", "fail", "skip"})
FILE_FIELDS = (
    "GoFiles",
    "CgoFiles",
    "CFiles",
    "CXXFiles",
    "MFiles",
    "HFiles",
    "FFiles",
    "SFiles",
    "SwigFiles",
    "SwigCXXFiles",
    "SysoFiles",
    "TestGoFiles",
    "XTestGoFiles",
)


def load_json(path: pathlib.Path) -> Any:
    with path.open("r", encoding="utf-8-sig") as stream:
        return json.load(stream)


def write_json(path: pathlib.Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
    temporary.replace(path)


def decode_json_stream(path: pathlib.Path) -> Iterable[dict[str, Any]]:
    text = path.read_text(encoding="utf-8-sig")
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            break
        value, offset = decoder.raw_decode(text, offset)
        if isinstance(value, dict):
            yield value


def normalize_target(args: argparse.Namespace) -> int:
    packages: dict[str, Any] = {}
    for package in decode_json_stream(args.input):
        import_path = package.get("ImportPath")
        if not import_path or package.get("Standard"):
            continue
        packages[import_path] = {
            "name": package.get("Name"),
            "files": {
                field: sorted(package.get(field) or []) for field in FILE_FIELDS
            },
        }
    write_json(
        args.output,
        {
            "schema": "entire-graph.windows-ci.a3.target-files.v1",
            "target": {"goos": "windows", "goarch": "amd64", "cgoEnabled": "1"},
            "packages": dict(sorted(packages.items())),
        },
    )
    return 0


def compare_target(args: argparse.Namespace) -> int:
    baseline = load_json(args.baseline)
    candidate = load_json(args.candidate)
    baseline_packages = baseline.get("packages", {})
    candidate_packages = candidate.get("packages", {})
    all_packages = sorted(set(baseline_packages) | set(candidate_packages))
    differences = []
    for package in all_packages:
        if baseline_packages.get(package) != candidate_packages.get(package):
            differences.append(
                {
                    "package": package,
                    "baseline": baseline_packages.get(package),
                    "candidate": candidate_packages.get(package),
                }
            )
    result = {
        "schema": "entire-graph.windows-ci.a3.target-files-compare.v1",
        "equivalent": not differences,
        "baselinePackageCount": len(baseline_packages),
        "candidatePackageCount": len(candidate_packages),
        "differences": differences,
    }
    write_json(args.output, result)
    return 0 if result["equivalent"] else 1


def normalize_test_list(value: Any) -> dict[str, list[str]]:
    if not isinstance(value, dict):
        raise ValueError("test-list JSON must be an object keyed by import path")
    return {
        str(package): sorted(str(name) for name in (tests or []))
        for package, tests in value.items()
    }


def compare_test_lists(args: argparse.Namespace) -> int:
    baseline = normalize_test_list(load_json(args.baseline))
    candidate = normalize_test_list(load_json(args.candidate))
    all_packages = sorted(set(baseline) | set(candidate))
    differences = []
    for package in all_packages:
        baseline_counter = collections.Counter(baseline.get(package, []))
        candidate_counter = collections.Counter(candidate.get(package, []))
        if baseline_counter != candidate_counter:
            differences.append(
                {
                    "package": package,
                    "missing": sorted((baseline_counter - candidate_counter).elements()),
                    "unexpected": sorted((candidate_counter - baseline_counter).elements()),
                }
            )
    result = {
        "schema": "entire-graph.windows-ci.a3.test-list-compare.v1",
        "equivalent": not differences,
        "baselinePackageCount": len(baseline),
        "candidatePackageCount": len(candidate),
        "baselineTopLevelTestCount": sum(map(len, baseline.values())),
        "candidateTopLevelTestCount": sum(map(len, candidate.values())),
        "differences": differences,
    }
    write_json(args.output, result)
    return 0 if result["equivalent"] else 1


def dynamic_events(paths: list[pathlib.Path]) -> tuple[collections.Counter, list[dict[str, Any]]]:
    events: collections.Counter[tuple[str, str, str]] = collections.Counter()
    diagnostics: list[dict[str, Any]] = []
    for path in paths:
        with path.open("r", encoding="utf-8-sig", errors="replace") as stream:
            for line_number, line in enumerate(stream, 1):
                if not line.strip():
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError as error:
                    diagnostics.append(
                        {"path": str(path), "line": line_number, "error": str(error)}
                    )
                    continue
                action = event.get("Action")
                test = event.get("Test")
                package = event.get("Package")
                if action in TEST_ACTIONS and test and package:
                    events[(str(package), str(test), str(action))] += 1
    return events, diagnostics


def serialize_counter(counter: collections.Counter) -> list[dict[str, Any]]:
    return [
        {"package": package, "test": test, "action": action, "count": count}
        for (package, test, action), count in sorted(counter.items())
    ]


def compare_dynamic(args: argparse.Namespace) -> int:
    baseline, baseline_diagnostics = dynamic_events(args.baseline)
    candidate, candidate_diagnostics = dynamic_events(args.candidate)
    missing = baseline - candidate
    unexpected = candidate - baseline
    result = {
        "schema": "entire-graph.windows-ci.a3.dynamic-events-compare.v1",
        "equivalent": not missing and not unexpected and not baseline_diagnostics and not candidate_diagnostics,
        "baselineEventCount": sum(baseline.values()),
        "candidateEventCount": sum(candidate.values()),
        "missing": serialize_counter(missing),
        "unexpected": serialize_counter(unexpected),
        "baselineDiagnostics": baseline_diagnostics,
        "candidateDiagnostics": candidate_diagnostics,
    }
    write_json(args.output, result)
    return 0 if result["equivalent"] else 1


def assert_dynamic_passes(args: argparse.Namespace) -> int:
    events, diagnostics = dynamic_events(args.input)
    checks = []
    accepted = not diagnostics
    for requirement in args.require_pass:
        try:
            package, test = requirement.split("::", 1)
        except ValueError as error:
            raise ValueError(
                f"--require-pass must be PACKAGE::TEST, got {requirement!r}"
            ) from error
        count = events[(package, test, "pass")]
        checks.append({"package": package, "test": test, "passCount": count})
        accepted = accepted and count == 1
    result = {
        "schema": "entire-graph.windows-ci.a3.dynamic-assertions.v1",
        "accepted": accepted,
        "checks": checks,
        "diagnostics": diagnostics,
    }
    write_json(args.output, result)
    return 0 if accepted else 1


def path(value: str) -> pathlib.Path:
    return pathlib.Path(value)


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)

    normalize = commands.add_parser("normalize-target")
    normalize.add_argument("--input", type=path, required=True)
    normalize.add_argument("--output", type=path, required=True)
    normalize.set_defaults(func=normalize_target)

    target = commands.add_parser("compare-target")
    target.add_argument("--baseline", type=path, required=True)
    target.add_argument("--candidate", type=path, required=True)
    target.add_argument("--output", type=path, required=True)
    target.set_defaults(func=compare_target)

    lists = commands.add_parser("compare-test-lists")
    lists.add_argument("--baseline", type=path, required=True)
    lists.add_argument("--candidate", type=path, required=True)
    lists.add_argument("--output", type=path, required=True)
    lists.set_defaults(func=compare_test_lists)

    dynamic = commands.add_parser("compare-dynamic")
    dynamic.add_argument("--baseline", type=path, action="append", required=True)
    dynamic.add_argument("--candidate", type=path, action="append", required=True)
    dynamic.add_argument("--output", type=path, required=True)
    dynamic.set_defaults(func=compare_dynamic)

    assertions = commands.add_parser("assert-dynamic-passes")
    assertions.add_argument("--input", type=path, action="append", required=True)
    assertions.add_argument("--require-pass", action="append", required=True)
    assertions.add_argument("--output", type=path, required=True)
    assertions.set_defaults(func=assert_dynamic_passes)
    return root


def main() -> int:
    args = parser().parse_args()
    try:
        return args.func(args)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"compare-evidence: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
