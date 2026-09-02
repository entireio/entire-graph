#!/usr/bin/env python3
"""Build a compact, sanitized A1 result summary from downloaded raw artifacts."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import statistics
from pathlib import Path
from typing import Any, Iterable


ACCEPTED_RUNS = (
    ("accepted-screen-03-d4", 4),
    ("accepted-screen-04-d8", 8),
    ("accepted-screen-05-d16", 16),
    ("accepted-screen-02-d32", 32),
    ("accepted-repeat-01-d32", 32),
    ("accepted-repeat-02-d32", 32),
)


def read_json(path: Path) -> Any:
    with path.open("r", encoding="utf-8-sig") as handle:
        return json.load(handle)


def read_json_lines(path: Path) -> Iterable[dict[str, Any]]:
    with path.open("r", encoding="utf-8-sig") as handle:
        for line in handle:
            if line.strip():
                yield json.loads(line)


def percentile(values: list[float], percent: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    position = (len(ordered) - 1) * percent
    lower = math.floor(position)
    upper = math.ceil(position)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] * (upper - position) + ordered[upper] * (position - lower)


def distribution(values: list[float]) -> dict[str, float | int | None]:
    return {
        "count": len(values),
        "mean": round(statistics.fmean(values), 6) if values else None,
        "p50": round(percentile(values, 0.50), 6) if values else None,
        "p95": round(percentile(values, 0.95), 6) if values else None,
        "max": round(max(values), 6) if values else None,
    }


def step_duration(metadata: dict[str, Any], command_line: str) -> float:
    matches = [
        step
        for step in metadata["steps"]
        if step.get("commandLine") == command_line
    ]
    if len(matches) != 1:
        raise ValueError(f"expected one step {command_line!r}, got {len(matches)}")
    return float(matches[0]["durationSeconds"])


def summarize_run(root: Path, run_id: str, vcpus: int) -> dict[str, Any]:
    run_root = root / run_id
    suite_root = run_root / "suite"
    treatment = read_json(run_root / "treatment-metadata.json")
    suite = read_json(suite_root / "run-metadata.json")
    compile_metrics = read_json(suite_root / "compile-metrics.json")
    transport = read_json(run_root / "transport.json")

    required_zeros = {
        "goTest": treatment["goTestExitCode"],
        "suiteMetadata": treatment["suiteMetadataExitCode"],
        "wrapperProcess": treatment["wrapperProcessExitCode"],
        "driver": treatment["exitCode"],
    }
    if any(value != 0 for value in required_zeros.values()):
        raise ValueError(f"{run_id}: nonzero acceptance exit: {required_zeros}")
    if treatment["state"] != "completed" or suite["state"] != "completed":
        raise ValueError(f"{run_id}: incomplete accepted metadata")
    if suite["commandLine"] != "go test -json -timeout 30m ./...":
        raise ValueError(f"{run_id}: unexpected command {suite['commandLine']!r}")
    if treatment["actualVcpus"] != vcpus:
        raise ValueError(f"{run_id}: expected {vcpus} vCPU")

    cpu_percent: list[float] = []
    disk_percent: list[float] = []
    disk_bytes_per_second: list[float] = []
    disk_queue: list[float] = []
    available_memory: list[float] = []
    monitor_end: dict[str, Any] | None = None
    for record in read_json_lines(suite_root / "resource-samples.jsonl"):
        if record.get("type") == "resource-monitor-end":
            monitor_end = record
        if record.get("type") != "resource-sample":
            continue
        cpu = record.get("cpu") or {}
        if cpu.get("percentProcessorTime") is not None:
            cpu_percent.append(float(cpu["percentProcessorTime"]))
        memory = record.get("memory") or {}
        if memory.get("availableBytes") is not None:
            available_memory.append(float(memory["availableBytes"]))
        total_disk = next(
            (disk for disk in (record.get("disks") or []) if disk.get("name") == "_Total"),
            None,
        )
        if total_disk:
            disk_percent.append(float(total_disk.get("percentDiskTime") or 0))
            disk_bytes_per_second.append(float(total_disk.get("diskBytesPerSecond") or 0))
            disk_queue.append(float(total_disk.get("currentDiskQueueLength") or 0))

    test_inventory: set[str] = set()
    packages: dict[str, dict[str, Any]] = {}
    top_level_tests: list[dict[str, Any]] = []
    for event in read_json_lines(suite_root / "go-test.jsonl"):
        package = event.get("Package")
        test = event.get("Test")
        action = event.get("Action")
        if action == "run" and package and test:
            test_inventory.add(f"{package}\0{test}")
        if package and not test and action in {"pass", "fail", "skip"}:
            packages[package] = {
                "package": package,
                "action": action,
                "elapsedSeconds": event.get("Elapsed"),
            }
        if package and test and "/" not in test and action in {"pass", "fail", "skip"}:
            top_level_tests.append(
                {
                    "package": package,
                    "test": test,
                    "action": action,
                    "elapsedSeconds": event.get("Elapsed"),
                }
            )

    inventory_lines = sorted(test_inventory)
    inventory_hash = hashlib.sha256("\n".join(inventory_lines).encode()).hexdigest()
    package_rows = sorted(
        packages.values(),
        key=lambda row: float(row["elapsedSeconds"] or 0),
        reverse=True,
    )
    top_level_tests.sort(
        key=lambda row: float(row["elapsedSeconds"] or 0),
        reverse=True,
    )

    compile_rows = [
        {
            "name": row["name"],
            "wallTimeSeconds": row["wallTimeSeconds"],
            "binarySizeBytes": row["binarySizeBytes"],
            "exitCode": row["exitCode"],
        }
        for row in compile_metrics
    ]

    return {
        "runId": run_id,
        "runLabel": treatment["runLabel"],
        "vmSize": treatment["actualVmSize"],
        "vcpus": vcpus,
        "seedArchiveSha256": treatment["seedArchiveSha256"],
        "repositorySha": treatment["productCommit"],
        "harnessSha": treatment["harnessCommit"],
        "commandLine": suite["commandLine"],
        "kernelSeconds": round(
            step_duration(suite, "go test -json -timeout 30m ./..."), 6
        ),
        "suiteWrapperSeconds": suite["durationSeconds"],
        "treatmentSeconds": treatment["durationSeconds"],
        "cacheRestoreSeconds": next(
            phase["durationSeconds"]
            for phase in treatment["phases"]
            if phase["name"] == "verify-and-restore-seed-cache"
        ),
        "packageSeconds": transport["packageDurationSeconds"],
        "uploadSeconds": transport["uploadDurationSeconds"],
        "artifactBytes": transport["artifactBytes"],
        "artifactSha256": transport["artifactSha256"],
        "acceptance": {
            "suiteState": suite["state"],
            "suitePhase": suite["phase"],
            "exitCodes": required_zeros,
            "exportStatus": transport["exportStatus"],
        },
        "warmCacheCompileDiagnostics": compile_rows,
        "resourceMonitor": {
            "sampleCount": monitor_end.get("sampleCount") if monitor_end else None,
            "samplesWithErrors": monitor_end.get("samplesWithErrors") if monitor_end else None,
            "cpuPercent": distribution(cpu_percent),
            "diskPercent": distribution(disk_percent),
            "diskBytesPerSecond": distribution(disk_bytes_per_second),
            "diskQueueLength": distribution(disk_queue),
            "minimumAvailableMemoryBytes": int(min(available_memory)) if available_memory else None,
        },
        "testInventory": {
            "runEventCount": len(test_inventory),
            "sha256": inventory_hash,
            "packageCount": len(packages),
            "failedPackages": sorted(
                row["package"] for row in packages.values() if row["action"] == "fail"
            ),
        },
        "slowestPackages": package_rows[:5],
        "slowestTopLevelTests": top_level_tests[:5],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-root", type=Path, default=Path("results/windows-ci/a1"))
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    runs = [summarize_run(args.results_root, run_id, vcpus) for run_id, vcpus in ACCEPTED_RUNS]
    inventory_hashes = sorted({run["testInventory"]["sha256"] for run in runs})
    seed_hashes = sorted({run["seedArchiveSha256"] for run in runs})
    d32_kernel = [run["kernelSeconds"] for run in runs if run["vcpus"] == 32]
    d32_treatment = [run["treatmentSeconds"] for run in runs if run["vcpus"] == 32]
    summary = {
        "schemaVersion": 1,
        "acceptedRunCount": len(runs),
        "coverageEquivalence": {
            "allRunsPassed": all(not run["testInventory"]["failedPackages"] for run in runs),
            "identicalTestInventoryAcrossRuns": len(inventory_hashes) == 1,
            "inventorySha256Values": inventory_hashes,
            "identicalSeedAcrossRuns": len(seed_hashes) == 1,
            "seedArchiveSha256Values": seed_hashes,
        },
        "d32Variance": {
            "runCount": len(d32_kernel),
            "completeTreatment": {
                "seconds": d32_treatment,
                "medianSeconds": round(statistics.median(d32_treatment), 6),
                "minimumSeconds": round(min(d32_treatment), 6),
                "maximumSeconds": round(max(d32_treatment), 6),
                "rangeSeconds": round(max(d32_treatment) - min(d32_treatment), 6),
                "sampleStandardDeviationSeconds": round(statistics.stdev(d32_treatment), 6),
            },
            "kernel": {
                "seconds": d32_kernel,
                "medianSeconds": round(statistics.median(d32_kernel), 6),
                "minimumSeconds": round(min(d32_kernel), 6),
                "maximumSeconds": round(max(d32_kernel), 6),
                "rangeSeconds": round(max(d32_kernel) - min(d32_kernel), 6),
                "sampleStandardDeviationSeconds": round(statistics.stdev(d32_kernel), 6),
            },
        },
        "runs": runs,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
