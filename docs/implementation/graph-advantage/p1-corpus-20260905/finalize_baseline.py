#!/usr/bin/env python3
"""Freeze the P1 baseline phase profile before comparative observations.

This command only reads three worker baseline artifacts.  It does not start a
worker, inspect a repository, or select a subset from treatment outcomes.
Each worker must provide three cache-off baseline rows for each assigned
repository/profile/verb cell.  Snapshot cells are eligible when the median
``phaseParse_ns / total_ns`` share is at least 0.50.  Search cells are retained
as explicitly unavailable because the search harness has no parser phase hook.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import statistics
import sys
from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable, Mapping, Sequence


PROTOCOL = "p1-corpus-20260905"
BASELINE_REPETITIONS = 3
VERBS = ("snapshot", "search")
SUCCESS_STATUSES = frozenset({"ok", "success", "passed", "complete"})


class InputError(ValueError):
    """An input manifest or observation artifact is not usable."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as exc:
        raise InputError(f"cannot hash {path}: {exc}") from exc
    return digest.hexdigest()


def _number(value: Any) -> float | None:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    try:
        number = float(value)
    except (TypeError, ValueError, OverflowError):
        return None
    return number if math.isfinite(number) else None


def _first(mapping: Mapping[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in mapping:
            return mapping[key]
    return None


def _read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise InputError(f"cannot read JSON {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise InputError(f"{path} must contain a JSON object")
    return value


def _read_ndjson(path: Path) -> list[dict[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise InputError(f"cannot read observations {path}: {exc}") from exc
    rows: list[dict[str, Any]] = []
    for line_number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise InputError(f"invalid JSON in {path} line {line_number}: {exc}") from exc
        if not isinstance(value, dict):
            raise InputError(f"{path} line {line_number} is not an object")
        rows.append(value)
    return rows


def _resolve_worker(path: Path) -> tuple[Path, Path]:
    path = path.resolve()
    if path.is_dir():
        manifest = path / "baseline-manifest.json"
        observations = path / "baseline.ndjson"
    elif path.suffix == ".ndjson":
        observations = path
        manifest = path.with_name("baseline-manifest.json")
    else:
        manifest = path
        metadata = _read_json(manifest)
        named = _first(metadata, "observations", "observations_path", "baseline_ndjson", "ndjson")
        observations = manifest.parent / str(named) if isinstance(named, str) and named else manifest.with_name("baseline.ndjson")
    if not manifest.is_file():
        raise InputError(f"worker manifest does not exist: {manifest}")
    if not observations.is_file():
        raise InputError(f"worker baseline NDJSON does not exist: {observations}")
    return manifest, observations


@dataclass(frozen=True)
class Worker:
    manifest_path: Path
    observations_path: Path
    manifest: dict[str, Any]
    rows: tuple[dict[str, Any], ...]
    blocked_strata: tuple[dict[str, Any], ...]


def _load_worker(path: Path) -> Worker:
    manifest_path, observations_path = _resolve_worker(path)
    manifest = _read_json(manifest_path)
    rows = tuple(_read_ndjson(observations_path))
    blocked = manifest.get("blocked_strata", [])
    if not isinstance(blocked, list):
        raise InputError(f"{manifest_path}: blocked_strata must be an array")
    blocked_objects = tuple(item for item in blocked if isinstance(item, dict))
    progress_path = manifest_path.with_name("progress.json")
    if progress_path.is_file():
        progress = _read_json(progress_path)
        progress_blocked = progress.get("blocked", [])
        if isinstance(progress_blocked, list):
            blocked_objects += tuple(item for item in progress_blocked if isinstance(item, dict))
    return Worker(manifest_path, observations_path, manifest, rows, blocked_objects)


def _assignment(manifest: Mapping[str, Any], rows: Iterable[Mapping[str, Any]]) -> set[tuple[str, str]]:
    result: set[tuple[str, str]] = set()
    assignment = manifest.get("assignment", [])
    if isinstance(assignment, list):
        for item in assignment:
            if not isinstance(item, Mapping):
                continue
            repository = _first(item, "repository", "repo", "id")
            profile = item.get("profile")
            if isinstance(repository, str) and repository and isinstance(profile, str) and profile:
                result.add((repository, profile))
    if result:
        return result
    for row in rows:
        repository, profile = row.get("repository"), row.get("profile")
        if isinstance(repository, str) and isinstance(profile, str):
            result.add((repository, profile))
    return result


def _row_group(row: Mapping[str, Any]) -> tuple[str, str, str] | None:
    repository, profile, verb = row.get("repository"), row.get("profile"), row.get("verb", row.get("operation"))
    if not all(isinstance(value, str) and value for value in (repository, profile, verb)):
        return None
    return repository, profile, verb


def _phase(row: Mapping[str, Any], *keys: str) -> float | None:
    phases = row.get("phase_ns")
    if not isinstance(phases, Mapping):
        return None
    value = _first(phases, *keys)
    return _number(value)


def _status(row: Mapping[str, Any]) -> str:
    value = row.get("status", "ok")
    return value.lower() if isinstance(value, str) else "invalid"


def _eligible_rows(rows: Sequence[Mapping[str, Any]]) -> tuple[list[Mapping[str, Any]], list[str]]:
    reasons: list[str] = []
    if len(rows) != BASELINE_REPETITIONS:
        reasons.append(f"expected {BASELINE_REPETITIONS} rows, got {len(rows)}")
    if len({row.get("trial") for row in rows}) != len(rows):
        reasons.append("duplicate trial number")
    usable: list[Mapping[str, Any]] = []
    for row in rows:
        status = _status(row)
        partial = _number(row.get("partial_failures_count"))
        if row.get("scenario") not in (None, "baseline"):
            reasons.append("row is not from the baseline scenario")
        if row.get("cache_mode") not in (None, "off", False):
            reasons.append("baseline row is not cache-off")
        if status not in SUCCESS_STATUSES:
            reasons.append(f"status={status}")
            continue
        if partial is None or partial != 0:
            reasons.append("partial_failures_count is missing or nonzero")
            continue
        if row.get("reuse") not in (False, 0):
            reasons.append("baseline row is not reuse=false")
            continue
        usable.append(row)
    return usable, sorted(set(reasons))


def _phase_entry(repository: str, profile: str, verb: str, rows: Sequence[Mapping[str, Any]], reasons: list[str]) -> dict[str, Any]:
    base: dict[str, Any] = {"repository": repository, "profile": profile, "scenario": "one-edit", "verb": verb, "baseline_scenario": "baseline", "expected_repetitions": BASELINE_REPETITIONS}
    if verb == "search":
        base.update({"eligible": False, "parse_dominated": False, "parse_classification": "unavailable", "reason_code": "search_phase_unavailable"})
        if reasons:
            base["baseline_observation_issues"] = reasons
        return base
    parse_values = [_phase(row, "phaseParse_ns", "phase_parse_ns", "phaseParse", "parse_ns", "parse", "extraction_ns", "extraction") for row in rows]
    total_values = [_phase(row, "total_ns", "total", "elapsed_ns") for row in rows]
    if len(parse_values) != BASELINE_REPETITIONS or any(value is None or value < 0 for value in parse_values):
        reasons.append("missing or invalid phaseParse_ns")
    if len(total_values) != BASELINE_REPETITIONS or any(value is None or value <= 0 for value in total_values):
        reasons.append("missing or invalid total_ns")
    if reasons:
        base.update({"eligible": False, "parse_dominated": False, "parse_classification": "baseline_phase", "reason_code": "baseline_incomplete", "baseline_observation_issues": sorted(set(reasons))})
        return base
    parse_numbers = [value for value in parse_values if value is not None]
    total_numbers = [value for value in total_values if value is not None]
    median_parse = statistics.median(parse_numbers)
    median_total = statistics.median(total_numbers)
    share = median_parse / median_total
    base.update({"eligible": True, "parse_dominated": share >= 0.50, "parse_classification": "baseline_phase", "phaseParse_ns": parse_numbers, "total_ns": total_numbers, "median_phaseParse_ns": median_parse, "median_total_ns": median_total, "extraction_share": share, "threshold": 0.50})
    return base


def _partial_reason_summary(rows: Sequence[Mapping[str, Any]]) -> dict[str, Any] | None:
    reasons: dict[tuple[str, str, str], int] = defaultdict(int)
    partial_rows = 0
    failure_count = 0
    for row in rows:
        count = _number(row.get("partial_failures_count"))
        failures = row.get("partial_failures")
        if not ((isinstance(count, (int, float)) and count > 0) or _status(row) == "partial" or isinstance(failures, list) and failures):
            continue
        partial_rows += 1
        if isinstance(count, (int, float)) and count > 0:
            failure_count += int(count)
        if isinstance(failures, list):
            for failure in failures:
                if not isinstance(failure, Mapping):
                    continue
                code = failure.get("code", "unknown")
                severity = failure.get("severity", "unknown")
                effect = failure.get("effect_on_semantic_completeness", "unknown")
                key = (str(code), str(severity), str(effect))
                reasons[key] += 1
    if not partial_rows:
        return None
    return {
        "partial_rows": partial_rows,
        "failure_count": failure_count,
        "reasons": [
            {"code": code, "severity": severity, "effect_on_semantic_completeness": effect, "rows": count}
            for (code, severity, effect), count in sorted(reasons.items())
        ],
    }


def finalize(worker_paths: Sequence[Path]) -> dict[str, Any]:
    if len(worker_paths) != 3:
        raise InputError(f"expected exactly 3 worker baseline artifacts, got {len(worker_paths)}")
    workers = [_load_worker(path) for path in worker_paths]
    issues: list[str] = []
    all_rows: list[dict[str, Any]] = []
    expected: set[tuple[str, str]] = set()
    source_workers: list[dict[str, Any]] = []
    blocked: list[dict[str, Any]] = []
    binary_digests: set[str] = set()
    input_digests: set[str] = set()
    for worker in workers:
        if worker.manifest.get("stage") not in (None, "baseline"):
            issues.append(f"{worker.manifest_path}: stage is not baseline")
        all_rows.extend(worker.rows)
        expected.update(_assignment(worker.manifest, worker.rows))
        blocked.extend(worker.blocked_strata)
        for key in ("binary_sha256", "binary_digest"):
            value = worker.manifest.get(key)
            if isinstance(value, str) and value:
                binary_digests.add(value)
        for key in ("input_manifest_sha256", "manifest_sha256"):
            value = worker.manifest.get(key)
            if isinstance(value, str) and value:
                input_digests.add(value)
        source_workers.append({"manifest": str(worker.manifest_path), "manifest_sha256": sha256_file(worker.manifest_path), "observations": str(worker.observations_path), "observations_sha256": sha256_file(worker.observations_path), "assignment": sorted(_assignment(worker.manifest, worker.rows)), "blocked_strata": list(worker.blocked_strata)})
        if worker.manifest.get("compiler", "off") != "off":
            issues.append(f"{worker.manifest_path}: compiler is not off")
        if worker.manifest.get("ranking", "current") != "current":
            issues.append(f"{worker.manifest_path}: ranking is not current")
    if len(binary_digests) != 1:
        issues.append("worker manifests do not provide one shared binary_sha256")
    if len(input_digests) != 1:
        issues.append("worker manifests do not provide one shared input_manifest_sha256")

    grouped: dict[tuple[str, str, str], list[dict[str, Any]]] = defaultdict(list)
    for row in all_rows:
        group = _row_group(row)
        if group is not None:
            grouped[group].append(row)
    snapshot_profiles: list[dict[str, Any]] = []
    search_profiles: list[dict[str, Any]] = []
    blocked_keys = {
        (item.get("repository"), item.get("profile"), item.get("verb", item.get("operation")))
        for item in blocked
        if isinstance(item.get("repository"), str) and isinstance(item.get("profile"), str) and isinstance(item.get("verb", item.get("operation")), str)
    }
    for repository, profile in sorted(expected):
        for verb in VERBS:
            rows = grouped[(repository, profile, verb)]
            usable, reasons = _eligible_rows(rows)
            if (repository, profile, verb) in blocked_keys or any(item.get("blocked_stratum") for item in rows):
                reasons.append("blocked stratum")
            entry = _phase_entry(repository, profile, verb, usable, sorted(set(reasons)))
            partial_summary = _partial_reason_summary(rows)
            if partial_summary is not None:
                entry["partial_reasons"] = partial_summary
            if verb == "snapshot" and entry.get("eligible") and not entry.get("parse_dominated"):
                entry["reason_code"] = "below_parse_majority_threshold"
            if verb == "snapshot" and not entry.get("eligible"):
                issues.append(f"{repository}/{profile}/snapshot baseline is ineligible")
            if verb == "search":
                entry["eligible"] = False
            # Duplicate assignments or observations from multiple workers are
            # retained as provenance but never silently merged into n=3.
            if len(rows) > BASELINE_REPETITIONS:
                entry["eligible"] = False
                entry["parse_dominated"] = False
                entry["reason_code"] = "duplicate_worker_rows"
                entry["baseline_observation_issues"] = sorted(set(reasons + ["more than three baseline rows"] ))
                issues.append(f"{repository}/{profile}/{verb} has duplicate baseline rows")
            if verb == "snapshot":
                snapshot_profiles.append(entry)
            else:
                search_profiles.append(entry)

    parse_dominated_groups = [
        {key: entry[key] for key in ("repository", "profile", "scenario", "verb")}
        for entry in snapshot_profiles
        if entry.get("eligible") and entry.get("parse_dominated")
    ]
    partial_baselines = [entry for entry in snapshot_profiles + search_profiles if "partial_reasons" in entry]
    missing_baselines = [entry for entry in snapshot_profiles + search_profiles if entry.get("reason_code") == "baseline_incomplete" and "partial_reasons" not in entry]
    counts = {"workers": len(workers), "snapshot_profiles": len(snapshot_profiles), "snapshot_eligible": sum(entry.get("eligible") is True for entry in snapshot_profiles), "parse_dominated": len(parse_dominated_groups), "search_profiles": len(search_profiles), "search_phase_unavailable": sum(entry.get("parse_classification") == "unavailable" for entry in search_profiles), "blocked_strata": len(blocked), "partial_baselines": len(partial_baselines), "missing_baselines": len(missing_baselines)}
    status = "ready" if not issues and all(entry.get("eligible") for entry in snapshot_profiles) else "evidence_incomplete"
    return {"format_version": 1, "protocol": PROTOCOL, "stage": "baseline-finalized", "status": status, "counts": counts, "compiler": "off", "ranking": "current", "binary_sha256": next(iter(binary_digests), None), "input_manifest_sha256": next(iter(input_digests), None), "baseline_repetitions": BASELINE_REPETITIONS, "phase_rule": {"name": "phaseParse_share", "numerator": "median(phaseParse_ns)", "denominator": "median(total_ns)", "threshold": 0.50, "inclusive": True, "scope": "snapshot only"}, "baseline_phase_profiles": snapshot_profiles, "search_phase_profiles": search_profiles, "parse_dominated_groups": parse_dominated_groups, "blocked_strata": blocked, "partial_baselines": partial_baselines, "missing_baselines": missing_baselines, "source_workers": source_workers, "issues": sorted(set(issues))}


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker", action="append", required=True, help="worker directory, manifest, or baseline NDJSON; repeat exactly three times")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        result = finalize([Path(value) for value in args.worker])
        if args.output.exists():
            raise InputError(f"refusing to overwrite existing output {args.output}")
        args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    except InputError as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps({"status": result["status"], "output": str(args.output)}, sort_keys=True))
    return 0 if result["status"] == "ready" else 1


if __name__ == "__main__":
    raise SystemExit(main())
