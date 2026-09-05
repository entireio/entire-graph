#!/usr/bin/env python3
"""Score the preregistered P1 paired campaign.

The input is either NDJSON (one flat request record per line), a JSON array of
records, or a JSON object containing ``observations``/``rows``.  A top-level
``manifest``/``metadata`` object may carry the frozen identities and baseline
phase profile.  The optional ``--manifest`` argument supplies that object when
the raw observations are kept as a standalone NDJSON file.

This is deliberately a post-processor.  It never starts a benchmark, touches
the extraction cache, or drops failed observations.  Exit status is non-zero
only for malformed input or a measured correctness/performance gate failure;
incomplete or unavailable evidence is represented in the JSON result and is
never turned into a passing score.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import random
import statistics
import sys
from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable, Mapping, Sequence


EXPECTED_REPETITIONS = 30
DEFAULT_BOOTSTRAP_RESAMPLES = 10_000
DEFAULT_SEED = 20260905
SUCCESS_STATUSES = frozenset({"ok", "success", "passed", "complete"})
FAILURE_STATUSES = frozenset({"error", "failed", "failure", "timeout", "timed_out", "partial", "cancelled", "unavailable"})
GROUP_FIELDS = ("repository", "profile", "scenario", "verb")


class InputError(ValueError):
    """The raw artifact cannot be interpreted as observations."""


@dataclass(frozen=True)
class Observation:
    row: Mapping[str, Any]
    index: int
    group: tuple[str, str, str, str]
    trial: int
    reuse: bool
    status: str

    @property
    def successful(self) -> bool:
        partial = _number(self.row.get("partial_failures_count"))
        return self.status in SUCCESS_STATUSES and partial == 0


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


def _text(mapping: Mapping[str, Any], *keys: str) -> str | None:
    value = _first(mapping, *keys)
    return value if isinstance(value, str) and value else None


def _group_key(record: Mapping[str, Any]) -> tuple[str, str, str, str]:
    values: list[str] = []
    for field in GROUP_FIELDS:
        value = record.get(field)
        if not isinstance(value, str) or not value:
            raise InputError(f"observation is missing non-empty `{field}`")
        values.append(value)
    return tuple(values)  # type: ignore[return-value]


def _group_label(group: tuple[str, str, str, str]) -> str:
    return "/".join(group)


def _parse_bool(value: Any) -> bool | None:
    if isinstance(value, bool):
        return value
    if isinstance(value, int) and value in (0, 1):
        return bool(value)
    if isinstance(value, str) and value.lower() in {"true", "false"}:
        return value.lower() == "true"
    return None


def _parse_observation(row: Mapping[str, Any], index: int) -> Observation:
    group = _group_key(row)
    trial_value = row.get("trial")
    if isinstance(trial_value, bool) or not isinstance(trial_value, int) or trial_value < 0:
        raise InputError(f"observation {index} has invalid `trial`")
    reuse = _parse_bool(row.get("reuse"))
    if reuse is None:
        raise InputError(f"observation {index} has invalid boolean `reuse`")
    status = row.get("status", "ok")
    if not isinstance(status, str) or not status:
        raise InputError(f"observation {index} has invalid `status`")
    return Observation(row=row, index=index, group=group, trial=trial_value, reuse=reuse, status=status.lower())


def _read_json_or_ndjson(path: Path) -> tuple[list[Mapping[str, Any]], dict[str, Any]]:
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise InputError(f"cannot read {path}: {exc}") from exc
    if not raw.strip():
        raise InputError(f"{path} is empty")
    try:
        decoded = json.loads(raw)
    except json.JSONDecodeError:
        rows: list[Mapping[str, Any]] = []
        for line_number, line in enumerate(raw.splitlines(), 1):
            if not line.strip():
                continue
            try:
                item = json.loads(line)
            except json.JSONDecodeError as exc:
                raise InputError(f"invalid JSON on line {line_number}: {exc}") from exc
            if not isinstance(item, Mapping):
                raise InputError(f"NDJSON line {line_number} is not an object")
            rows.append(item)
        return rows, {}

    if isinstance(decoded, list):
        rows = decoded
        metadata: dict[str, Any] = {}
    elif isinstance(decoded, Mapping):
        candidate = decoded.get("observations", decoded.get("rows"))
        if not isinstance(candidate, list):
            raise InputError("JSON object must contain an `observations` or `rows` array")
        rows = candidate
        metadata = {}
        for key in ("manifest", "metadata"):
            value = decoded.get(key)
            if isinstance(value, Mapping):
                metadata.update(value)
        for key in ("baseline_phase_profiles", "parse_dominated_groups", "expected_repetitions"):
            if key in decoded and key not in metadata:
                metadata[key] = decoded[key]
    else:
        raise InputError("JSON root must be an array or object")
    if not all(isinstance(item, Mapping) for item in rows):
        raise InputError("every observation must be a JSON object")
    return list(rows), metadata


def _load_manifest(path: Path | None) -> dict[str, Any]:
    if path is None:
        return {}
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise InputError(f"cannot read manifest {path}: {exc}") from exc
    if not isinstance(value, Mapping):
        raise InputError("manifest must be a JSON object")
    result: dict[str, Any] = {}
    for key in ("manifest", "metadata"):
        nested = value.get(key)
        if isinstance(nested, Mapping):
            result.update(nested)
    result.update({key: value[key] for key in ("baseline_phase_profiles", "parse_dominated_groups", "expected_repetitions") if key in value})
    return result


def _phase_value(profile: Mapping[str, Any], *keys: str) -> float | None:
    value = _first(profile, *keys)
    if value is not None:
        if isinstance(value, list):
            values = [_number(item) for item in value]
            if values and all(item is not None for item in values):
                return float(statistics.median([item for item in values if item is not None]))
            return None
        return _number(value)
    phases = profile.get("phase_ns")
    if isinstance(phases, Mapping):
        nested = _first(phases, *keys)
        if isinstance(nested, list):
            values = [_number(item) for item in nested]
            if values and all(item is not None for item in values):
                return float(statistics.median([item for item in values if item is not None]))
        return _number(nested)
    return None


def _phase_membership(metadata: Mapping[str, Any]) -> tuple[dict[tuple[str, str, str, str], bool], dict[str, Any], list[str]]:
    """Return frozen group membership, phase details, and protocol issues."""
    issues: list[str] = []
    details: dict[str, Any] = {}
    membership: dict[tuple[str, str, str, str], bool] = {}
    profiles = metadata.get("baseline_phase_profiles")
    if isinstance(profiles, list):
        for index, item in enumerate(profiles):
            if not isinstance(item, Mapping):
                issues.append(f"baseline phase profile {index} is not an object")
                continue
            try:
                group = _group_key(item)
            except InputError as exc:
                issues.append(str(exc))
                continue
            extraction = _phase_value(item, "phaseParse_ns", "phase_parse_ns", "extraction_ns", "parse_ns", "extract_ns", "phaseParse", "extraction", "parse")
            total = _phase_value(item, "total_ns", "elapsed_ns", "wall_ns", "total")
            if extraction is None or total is None or total <= 0 or extraction < 0:
                issues.append(f"baseline phase profile {_group_label(group)} lacks valid phase times")
                continue
            share = extraction / total
            declared = item.get("parse_dominated")
            if declared is not None and not isinstance(declared, bool):
                issues.append(f"baseline phase profile {_group_label(group)} has non-boolean parse_dominated")
                continue
            # Search has no parser-phase hook in the current harness. Keep its
            # observations in the report, but never let an inferred share put
            # it into the P1 one-edit gate.
            selected = group[3] == "snapshot" and share >= 0.50
            if declared is not None and declared != selected:
                issues.append(f"baseline phase profile {_group_label(group)} disagrees with frozen >=50% rule")
                continue
            membership[group] = selected
            details[_group_label(group)] = {
                "extraction_share": share,
                "parse_dominated": selected,
                "parse_classification": "baseline_phase" if group[3] == "snapshot" else "unavailable",
            }

    declared_groups = metadata.get("parse_dominated_groups")
    if isinstance(declared_groups, list):
        declared_keys: set[tuple[str, str, str, str]] = set()
        for item in declared_groups:
            if isinstance(item, str):
                parts = tuple(item.split("/"))
                if len(parts) != 4 or not all(parts):
                    issues.append(f"invalid parse-dominated group {item!r}")
                    continue
                group = parts  # type: ignore[assignment]
            elif isinstance(item, Mapping):
                try:
                    group = _group_key(item)
                except InputError as exc:
                    issues.append(str(exc))
                    continue
            else:
                issues.append("parse-dominated group is not a string or object")
                continue
            declared_keys.add(group)
        if profiles is not None and declared_keys != {group for group, value in membership.items() if value}:
            issues.append("frozen parse-dominated membership disagrees with baseline phase profiles")
        if profiles is None and declared_keys:
            issues.append("parse-dominated membership was declared without baseline phase profiles")
        for group in declared_keys:
            membership[group] = True
            details.setdefault(_group_label(group), {"parse_dominated": True, "declared_without_phase": True})
    return membership, details, issues


def _duration_ns(obs: Observation) -> float | None:
    value = _first(obs.row, "elapsed_ns", "total_ns", "elapsed")
    number = _number(value)
    return number if number is not None and number > 0 else None


def _rss_bytes(obs: Observation) -> float | None:
    value = _first(obs.row, "peak_rss_bytes", "rss_peak_bytes", "rss_bytes")
    number = _number(value)
    return number if number is not None and number >= 0 else None


def _cache_bytes(obs: Observation) -> float | None:
    value = _first(obs.row, "cache_bytes", "disk_cache_bytes")
    number = _number(value)
    return number if number is not None and number >= 0 else None


def _extraction(obs: Observation) -> Mapping[str, Any]:
    value = obs.row.get("extraction")
    return value if isinstance(value, Mapping) else {}


def _semantic_digest(obs: Observation) -> str | None:
    return _text(obs.row, "semantic_digest", "output_digest", "semantic_hash")


def _source_digest(obs: Observation) -> str | None:
    return _text(obs.row, "source_digest", "input_digest", "captured_source_digest")


def _stale_source(obs: Observation) -> bool | None:
    value = _first(obs.row, "stale_source", "source_stale")
    if value is None:
        value = _first(_extraction(obs), "stale_source", "source_stale")
    return _parse_bool(value) if value is not None else None


def _unchanged_reparses(obs: Observation) -> float | None:
    extraction = _extraction(obs)
    # ``files_parsed`` is deliberately excluded: it may include files that
    # were never eligible for reuse or files counted after a transient error.
    blocked = _number(_first(extraction, "uncacheable_or_failed_files", "uncacheable_files", "eligibility_unknown_files"))
    if blocked is not None and blocked > 0:
        return None
    value = _first(extraction, "unchanged_eligible_reparses", "unchanged_reparses")
    number = _number(value)
    return number if number is not None and number >= 0 and number.is_integer() else None


def _has_required_runtime_fields(obs: Observation) -> list[str]:
    missing: list[str] = []
    for field in ("wall_ns", "peak_rss_bytes", "cache_bytes", "semantic_digest", "source_digest", "extraction", "phase_ns", "partial_failures_count"):
        if field not in obs.row:
            missing.append(field)
    return missing


def _percentile(values: Sequence[float], percentile: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, math.ceil(percentile * len(ordered)) - 1))
    return ordered[index]


def _bootstrap_pairs(
    pairs: Sequence[tuple[Observation, Observation]],
    statistic: str,
    *,
    seed: int,
    resamples: int,
) -> tuple[float, float] | None:
    if not pairs:
        return None
    rng = random.Random(seed)
    estimates: list[float] = []
    for _ in range(resamples):
        selected = [pairs[rng.randrange(len(pairs))] for _ in pairs]
        baseline = [_duration_ns(item[0]) for item in selected]
        treatment = [_duration_ns(item[1]) for item in selected]
        if any(value is None for value in baseline + treatment):
            continue
        b = _percentile([value for value in baseline if value is not None], 0.50 if statistic == "p50" else 0.95)
        t = _percentile([value for value in treatment if value is not None], 0.50 if statistic == "p50" else 0.95)
        if b is not None and t is not None and b > 0:
            estimates.append(1.0 - t / b)
    if not estimates:
        return None
    return (_percentile(estimates, 0.025) or 0.0, _percentile(estimates, 0.975) or 0.0)


def _gate(status: str, reason: str, **extra: Any) -> dict[str, Any]:
    result = {"status": status, "reason": reason}
    result.update(extra)
    return result


def summarize(
    rows: Iterable[Mapping[str, Any]],
    metadata: Mapping[str, Any] | None = None,
    *,
    expected_repetitions: int = EXPECTED_REPETITIONS,
    seed: int = DEFAULT_SEED,
    bootstrap_resamples: int = DEFAULT_BOOTSTRAP_RESAMPLES,
) -> dict[str, Any]:
    if expected_repetitions <= 0 or bootstrap_resamples <= 0:
        raise InputError("expected repetitions and bootstrap resamples must be positive")
    metadata = metadata or {}
    observations = [_parse_observation(row, index) for index, row in enumerate(rows)]
    membership, phase_details, phase_issues = _phase_membership(metadata)
    issues = list(phase_issues)
    for observation in observations:
        missing_fields = _has_required_runtime_fields(observation)
        if missing_fields:
            issues.append(f"observation {observation.index} is missing required runtime fields: {', '.join(missing_fields)}")
    groups: dict[tuple[str, str, str, str], list[Observation]] = defaultdict(list)
    for observation in observations:
        groups[observation.group].append(observation)

    by_pair: dict[tuple[tuple[str, str, str, str], int], list[Observation]] = defaultdict(list)
    for observation in observations:
        by_pair[(observation.group, observation.trial)].append(observation)

    pair_rows: dict[str, list[dict[str, Any]]] = defaultdict(list)
    valid_pairs: dict[tuple[str, str, str, str], list[tuple[Observation, Observation]]] = defaultdict(list)
    errors: list[dict[str, Any]] = []
    group_summaries: list[dict[str, Any]] = []
    all_semantic_equal = True
    any_semantic_evidence = False
    any_stale_evidence = False
    stale_found = False
    unchanged_reparse_missing = False
    unchanged_reparse_nonzero = False

    for group in sorted(groups):
        group_label = _group_label(group)
        group_observations = groups[group]
        for (pair_group, trial), members in sorted(by_pair.items(), key=lambda item: item[0][1]):
            if pair_group != group:
                continue
            expected_order = [False, True] if trial % 2 == 0 else [True, False]
            actual_order = [item.reuse for item in members]
            if actual_order != expected_order:
                issues.append(f"{group_label} trial {trial} violates alternating arm order")
            arms = {item.reuse: item for item in members}
            if len(members) != 2 or set(arms) != {False, True}:
                errors.append({"group": group_label, "trial": trial, "kind": "unpaired", "statuses": [item.status for item in members]})
                continue
            baseline, treatment = arms[False], arms[True]
            for item in (baseline, treatment):
                if not item.successful:
                    errors.append({"group": group_label, "trial": trial, "kind": "request_failure", "reuse": item.reuse, "status": item.status, "error": item.row.get("error")})
            semantic = _semantic_digest(baseline)
            treatment_semantic = _semantic_digest(treatment)
            source = _source_digest(baseline)
            treatment_source = _source_digest(treatment)
            semantic_equal = None
            if baseline.successful and treatment.successful:
                semantic_equal = semantic is not None and treatment_semantic is not None and semantic == treatment_semantic
                any_semantic_evidence |= semantic is not None and treatment_semantic is not None
                all_semantic_equal &= semantic_equal
                if not semantic_equal:
                    errors.append({"group": group_label, "trial": trial, "kind": "semantic_mismatch_or_missing", "baseline": semantic, "treatment": treatment_semantic})
                if source is None or treatment_source is None or source != treatment_source:
                    errors.append({"group": group_label, "trial": trial, "kind": "source_identity_mismatch_or_missing", "baseline": source, "treatment": treatment_source})
            for item in (baseline, treatment):
                stale = _stale_source(item) if item.successful else None
                any_stale_evidence |= stale is not None
                if stale is True:
                    stale_found = True
                if group[2] == "unchanged" and item.successful:
                    reparses = _unchanged_reparses(item)
                    if reparses is None:
                        unchanged_reparse_missing = True
                    elif reparses != 0:
                        unchanged_reparse_nonzero = True
            if baseline.successful and treatment.successful and _duration_ns(baseline) is not None and _duration_ns(treatment) is not None and source is not None and source == treatment_source:
                valid_pairs[group].append((baseline, treatment))
            pair_rows[group_label].append({
                "trial": trial,
                "baseline_status": baseline.status,
                "treatment_status": treatment.status,
                "semantic_equal": semantic_equal,
                "valid_latency_pair": (baseline, treatment) in valid_pairs[group],
            })
        trial_count = len({item.trial for item in group_observations})
        if trial_count != expected_repetitions:
            issues.append(f"{group_label} has {trial_count} trials; expected {expected_repetitions}")
        metrics: dict[str, Any] = {}
        for reuse, arm_name in ((False, "baseline"), (True, "treatment")):
            values = [_duration_ns(item) / 1_000_000 for item in group_observations if item.reuse == reuse and item.successful and _duration_ns(item) is not None]
            rss = [_rss_bytes(item) for item in group_observations if item.reuse == reuse and item.successful and _rss_bytes(item) is not None]
            cache = [_cache_bytes(item) for item in group_observations if item.reuse == reuse and item.successful and _cache_bytes(item) is not None]
            metrics[arm_name] = {
                "n_rows": sum(1 for item in group_observations if item.reuse == reuse),
                "n_success": len(values),
                "n_failures": sum(1 for item in group_observations if item.reuse == reuse and not item.successful),
                "total_ms": {"p50": _percentile(values, 0.50), "p95": _percentile(values, 0.95)},
                "peak_rss_bytes": {"p50": _percentile([value for value in rss if value is not None], 0.50), "p95": _percentile([value for value in rss if value is not None], 0.95)},
                "cache_bytes": {"p50": _percentile([value for value in cache if value is not None], 0.50), "p95": _percentile([value for value in cache if value is not None], 0.95)},
            }
            for metric_name in ("total_ms", "peak_rss_bytes", "cache_bytes"):
                if metrics[arm_name][metric_name]["p50"] is None:
                    metrics[arm_name][metric_name]["status"] = "N/A"
        pairs = valid_pairs[group]
        metrics["paired"] = {"n": len(pairs), "status": "ok" if pairs else "N/A"}
        for statistic in ("p50", "p95"):
            baseline_values = [_duration_ns(pair[0]) / 1_000_000 for pair in pairs]
            treatment_values = [_duration_ns(pair[1]) / 1_000_000 for pair in pairs]
            baseline_stat = _percentile(baseline_values, 0.50 if statistic == "p50" else 0.95)
            treatment_stat = _percentile(treatment_values, 0.50 if statistic == "p50" else 0.95)
            benefit = None if baseline_stat in (None, 0) or treatment_stat is None else 1 - treatment_stat / baseline_stat
            metrics["paired"][statistic] = {"baseline_ms": baseline_stat, "treatment_ms": treatment_stat, "benefit": benefit, "ci95": _bootstrap_pairs(pairs, statistic, seed=seed + (0 if statistic == "p50" else 1), resamples=bootstrap_resamples)}
        parse_profile = phase_details.get(group_label)
        if parse_profile is None and group[3] == "search":
            parse_profile = {"parse_dominated": False, "parse_classification": "unavailable"}
        if parse_profile is None:
            parse_profile = {"parse_dominated": membership.get(group)}
        group_summaries.append({"group": dict(zip(GROUP_FIELDS, group)), "parse_profile": parse_profile, "metrics": metrics, "pairs": pair_rows[group_label]})

    gate_results: dict[str, dict[str, Any]] = {}
    if not any_semantic_evidence:
        gate_results["semantic_equivalence"] = _gate("evidence_incomplete", "no paired semantic digests were supplied")
    elif all_semantic_equal and not any(item["kind"] == "semantic_mismatch_or_missing" for item in errors):
        gate_results["semantic_equivalence"] = _gate("pass", "all paired successful digests match")
    else:
        gate_results["semantic_equivalence"] = _gate("fail", "at least one pair has missing or mismatched semantic output")
    if not any_stale_evidence:
        gate_results["no_stale_source"] = _gate("evidence_incomplete", "no explicit stale-source fields were supplied")
    elif stale_found:
        gate_results["no_stale_source"] = _gate("fail", "at least one observation reports stale source")
    else:
        gate_results["no_stale_source"] = _gate("pass", "all supplied stale-source fields are false")
    if unchanged_reparse_nonzero:
        gate_results["zero_unchanged_reparses"] = _gate("fail", "a valid unchanged observation reports a nonzero reparse count")
    elif unchanged_reparse_missing:
        gate_results["zero_unchanged_reparses"] = _gate("evidence_incomplete", "an unchanged observation lacks unchanged_reparses")
    elif any(group["group"]["scenario"] == "unchanged" for group in group_summaries):
        gate_results["zero_unchanged_reparses"] = _gate("pass", "all supplied unchanged reparse counts are zero")
    else:
        gate_results["zero_unchanged_reparses"] = _gate("not_applicable", "no unchanged groups were supplied")

    one_edit = [group for group in group_summaries if group["group"]["verb"] == "snapshot" and group["group"]["scenario"] == "one-edit" and group["parse_profile"].get("parse_dominated") is True]
    one_edit_pairs = [pair for group in one_edit for pair in valid_pairs[tuple(group["group"][field] for field in GROUP_FIELDS)]]
    expected_one_edit_pairs = len(one_edit) * expected_repetitions
    if not one_edit or not one_edit_pairs or len(one_edit_pairs) != expected_one_edit_pairs:
        gate_results["one_edit_median_benefit"] = _gate("evidence_incomplete", "no complete set of valid preregistered parse-dominated one-edit pairs", n_pairs=len(one_edit_pairs), expected_pairs=expected_one_edit_pairs)
    else:
        baseline = [_duration_ns(pair[0]) / 1_000_000 for pair in one_edit_pairs]
        treatment = [_duration_ns(pair[1]) / 1_000_000 for pair in one_edit_pairs]
        baseline_p50 = _percentile(baseline, 0.50)
        treatment_p50 = _percentile(treatment, 0.50)
        benefit = None if baseline_p50 in (None, 0) or treatment_p50 is None else 1 - treatment_p50 / baseline_p50
        ci = _bootstrap_pairs(one_edit_pairs, "p50", seed=seed, resamples=bootstrap_resamples)
        gate_results["one_edit_median_benefit"] = _gate("pass" if benefit is not None and benefit >= 0.25 else "fail", "aggregate parse-dominated one-edit p50 benefit", benefit=benefit, ci95=ci, n_pairs=len(one_edit_pairs))

    def regression_gate(metric: str, accessor: Any) -> dict[str, Any]:
        cold = [group for group in group_summaries if group["group"]["scenario"] == "cold"]
        if not cold:
            return _gate("not_applicable", "no cold groups were supplied")
        regressions: list[dict[str, Any]] = []
        missing = []
        for group in cold:
            baseline = accessor(group["metrics"]["baseline"])
            treatment = accessor(group["metrics"]["treatment"])
            if baseline is None or treatment is None or baseline <= 0:
                missing.append(group["group"])
            else:
                regressions.append({"group": group["group"], "regression": treatment / baseline - 1})
        if missing:
            return _gate("evidence_incomplete", f"missing cold {metric} denominator or treatment", missing=missing, measured=regressions)
        worst = max(item["regression"] for item in regressions)
        return _gate("pass" if worst <= 0.10 else "fail", f"worst cold {metric} regression", worst_regression=worst, groups=regressions)

    regression_results = regression_gate("total_ms", lambda arm: arm["total_ms"]["p50"])
    gate_results["cold_latency_regression"] = regression_results
    gate_results["cold_rss_regression"] = regression_gate("peak_rss_bytes", lambda arm: arm["peak_rss_bytes"]["p50"])

    metadata_issues: list[str] = []
    for key, expected in (("compiler", "off"), ("compiler_mode", "off"), ("ranking", "current"), ("ranking_mode", "current")):
        if key in metadata and metadata[key] != expected:
            metadata_issues.append(f"metadata `{key}` is {metadata[key]!r}; expected {expected!r}")
    for key in ("binary_sha256", "input_manifest_sha256"):
        if key not in metadata:
            metadata_issues.append(f"metadata `{key}` is missing")
    binary_values = {
        value
        for observation in observations
        for value in [_text(observation.row, "binary_sha256", "binary_digest")]
        if value is not None
    }
    if len(binary_values) > 1:
        metadata_issues.append("observations contain more than one binary digest")
    elif binary_values and "binary_sha256" in metadata and next(iter(binary_values)) != metadata["binary_sha256"]:
        metadata_issues.append("observation binary digest differs from the frozen manifest")
    issues.extend(metadata_issues)
    unrun = metadata.get("unrun", [])
    if unrun:
        if not isinstance(unrun, list):
            issues.append("metadata `unrun` must be an array")
            unrun = []
        else:
            issues.append(f"{len(unrun)} planned cells are explicitly unrun")

    statuses = {gate["status"] for gate in gate_results.values()}
    if "fail" in statuses:
        overall = "fail"
    elif "evidence_incomplete" in statuses or "inability" in statuses or issues:
        overall = "evidence_incomplete"
    elif statuses <= {"pass", "not_applicable"}:
        overall = "pass"
    else:
        overall = "evidence_incomplete"
    return {
        "protocol": "p1-corpus-20260905",
        "overall": overall,
        "settings": {"expected_repetitions": expected_repetitions, "bootstrap_seed": seed, "bootstrap_resamples": bootstrap_resamples, "latency_statistic": "elapsed_ns", "timeout_seconds": 120, "warm_os_page_cache": True, "cold_means_empty_application_cache": True},
        "observations": {"rows": len(observations), "groups": len(groups), "successful": sum(item.successful for item in observations), "failures": sum(not item.successful for item in observations), "errors": errors},
        "phase_profiles": {"groups": phase_details, "issues": phase_issues},
        "unrun": unrun,
        "groups": group_summaries,
        "gates": gate_results,
        "issues": issues,
    }


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("raw", type=Path, help="JSON or NDJSON observation artifact")
    parser.add_argument("--manifest", type=Path, help="optional JSON run/phase manifest")
    parser.add_argument("--output", type=Path, help="write summary JSON here")
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    parser.add_argument("--bootstrap-resamples", type=int, default=DEFAULT_BOOTSTRAP_RESAMPLES)
    parser.add_argument("--expected-repetitions", type=int, default=EXPECTED_REPETITIONS)
    args = parser.parse_args(argv)
    try:
        rows, metadata = _read_json_or_ndjson(args.raw)
        metadata.update(_load_manifest(args.manifest))
        summary = summarize(rows, metadata, expected_repetitions=args.expected_repetitions, seed=args.seed, bootstrap_resamples=args.bootstrap_resamples)
    except InputError as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2
    encoded = json.dumps(summary, indent=2, sort_keys=True)
    if args.output:
        args.output.write_text(encoded + "\n", encoding="utf-8")
    else:
        print(encoded)
    return 1 if summary["overall"] == "fail" else 0


if __name__ == "__main__":
    raise SystemExit(main())
