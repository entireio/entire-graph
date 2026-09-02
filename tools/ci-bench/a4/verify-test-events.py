#!/usr/bin/env python3
"""Build weights and prove exact dynamic/list coverage for A4 shards."""

from __future__ import annotations

import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import sys
from typing import Iterable


DYNAMIC_ACTIONS = {"run", "pass", "fail", "skip"}


def events(paths: Iterable[Path]):
    for path in paths:
        with path.open("r", encoding="utf-8-sig") as stream:
            for line_number, line in enumerate(stream, start=1):
                if not line.strip():
                    continue
                try:
                    yield json.loads(line)
                except json.JSONDecodeError as error:
                    raise ValueError(f"{path}:{line_number}: invalid JSON: {error}") from error


def dynamic_counter(paths: Iterable[Path]) -> Counter[tuple[str, str, str]]:
    result: Counter[tuple[str, str, str]] = Counter()
    for event in events(paths):
        package = event.get("Package")
        test = event.get("Test")
        action = event.get("Action")
        if package and test and action in DYNAMIC_ACTIONS:
            result[(package, test, action)] += 1
    return result


def package_inventory(paths: Iterable[Path]) -> set[str]:
    return {event["Package"] for event in events(paths) if event.get("Package")}


def package_terminals(paths: Iterable[Path]) -> Counter[tuple[str, str]]:
    result: Counter[tuple[str, str]] = Counter()
    for event in events(paths):
        if (
            event.get("Package")
            and not event.get("Test")
            and event.get("Action") in {"pass", "fail", "skip"}
        ):
            result[(event["Package"], event["Action"])] += 1
    return result


def counter_signature(counter: Counter[tuple[str, ...]]) -> str:
    canonical = "".join(
        "\0".join(key) + f"\0{count}\n" for key, count in sorted(counter.items())
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def counter_difference(
    left: Counter[tuple[str, ...]], right: Counter[tuple[str, ...]], limit: int = 50
):
    result = []
    for key in sorted(set(left) | set(right)):
        if left[key] != right[key]:
            result.append(
                {"key": list(key), "baselineCount": left[key], "candidateCount": right[key]}
            )
            if len(result) >= limit:
                break
    return result


def write_json(path: Path, value) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def compare_events(args: argparse.Namespace) -> int:
    baseline_paths = [args.baseline]
    candidate_paths = args.candidate
    baseline = dynamic_counter(baseline_paths)
    candidate = dynamic_counter(candidate_paths)
    baseline_packages = package_inventory(baseline_paths)
    candidate_packages = package_inventory(candidate_paths)
    baseline_terminals = package_terminals(baseline_paths)
    candidate_terminals = package_terminals(candidate_paths)
    exact = baseline == candidate
    payload = {
        "schema": "ci-bench.a4-event-equivalence.v1",
        "exactDynamicEventMultiset": exact,
        "actions": sorted(DYNAMIC_ACTIONS),
        "baseline": {
            "files": [str(path) for path in baseline_paths],
            "eventCount": sum(baseline.values()),
            "distinctEventKeys": len(baseline),
            "signatureSHA256": counter_signature(baseline),
            "packageInventory": sorted(baseline_packages),
            "packageTerminals": [
                {"package": key[0], "action": key[1], "count": count}
                for key, count in sorted(baseline_terminals.items())
            ],
        },
        "candidate": {
            "files": [str(path) for path in candidate_paths],
            "eventCount": sum(candidate.values()),
            "distinctEventKeys": len(candidate),
            "signatureSHA256": counter_signature(candidate),
            "packageInventory": sorted(candidate_packages),
            "packageTerminals": [
                {"package": key[0], "action": key[1], "count": count}
                for key, count in sorted(candidate_terminals.items())
            ],
        },
        "packageInventoryExact": baseline_packages == candidate_packages,
        "missingPackages": sorted(baseline_packages - candidate_packages),
        "extraPackages": sorted(candidate_packages - baseline_packages),
        "differenceCount": sum(
            1 for key in set(baseline) | set(candidate) if baseline[key] != candidate[key]
        ),
        "differences": counter_difference(baseline, candidate),
    }
    write_json(args.output, payload)
    return 0 if exact and baseline_packages == candidate_packages else 1


def build_weights(args: argparse.Namespace) -> int:
    weights: dict[str, float] = {}
    terminal_actions: dict[str, str] = {}
    for event in events([args.input]):
        name = event.get("Test")
        if (
            event.get("Package") == args.package
            and name
            and "/" not in name
            and event.get("Action") in {"pass", "fail", "skip"}
        ):
            weights[name] = float(event.get("Elapsed", 0.0))
            terminal_actions[name] = event["Action"]
    write_json(
        args.output,
        {
            "schema": "ci-bench.a4-test-weights.v1",
            "package": args.package,
            "tests": weights,
            "terminalActions": terminal_actions,
            "testCount": len(weights),
            "totalElapsedSeconds": round(sum(weights.values()), 9),
        },
    )
    return 0


def list_names(path: Path) -> list[str]:
    return [
        line.strip()
        for line in path.read_text(encoding="utf-8-sig").splitlines()
        if line.strip() and line.strip() not in {"PASS", "FAIL"}
    ]


def compare_lists(args: argparse.Namespace) -> int:
    baseline = Counter(list_names(args.baseline))
    candidate: Counter[str] = Counter()
    for path in args.candidate:
        candidate.update(list_names(path))
    exact = baseline == candidate
    payload = {
        "schema": "ci-bench.a4-list-equivalence.v1",
        "exactListMultiset": exact,
        "baselineCount": sum(baseline.values()),
        "candidateCount": sum(candidate.values()),
        "baselineUnique": len(baseline),
        "candidateUnique": len(candidate),
        "baselineSignatureSHA256": counter_signature(
            Counter({(key,): value for key, value in baseline.items()})
        ),
        "candidateSignatureSHA256": counter_signature(
            Counter({(key,): value for key, value in candidate.items()})
        ),
        "differenceCount": sum(
            1 for key in set(baseline) | set(candidate) if baseline[key] != candidate[key]
        ),
        "differences": [
            {"name": key, "baselineCount": baseline[key], "candidateCount": candidate[key]}
            for key in sorted(set(baseline) | set(candidate))
            if baseline[key] != candidate[key]
        ][:50],
    }
    write_json(args.output, payload)
    return 0 if exact else 1


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)

    compare = commands.add_parser("compare-events")
    compare.add_argument("--baseline", type=Path, required=True)
    compare.add_argument("--candidate", type=Path, action="append", required=True)
    compare.add_argument("--output", type=Path, required=True)
    compare.set_defaults(handler=compare_events)

    weights = commands.add_parser("weights")
    weights.add_argument("--input", type=Path, required=True)
    weights.add_argument("--package", required=True)
    weights.add_argument("--output", type=Path, required=True)
    weights.set_defaults(handler=build_weights)

    lists = commands.add_parser("compare-lists")
    lists.add_argument("--baseline", type=Path, required=True)
    lists.add_argument("--candidate", type=Path, action="append", required=True)
    lists.add_argument("--output", type=Path, required=True)
    lists.set_defaults(handler=compare_lists)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        return args.handler(args)
    except (OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
