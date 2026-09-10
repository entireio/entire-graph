#!/usr/bin/env python3
"""Validate canonical Graph Advantage evidence without third-party packages."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
from pathlib import Path
from typing import Any

SCHEMA_VERSION = "graph-advantage-evidence/v1"
CASE_VERSION = "graph-advantage-evidence-case/v1"
STATUSES = {"passed", "failed", "partial", "timeout", "not_run", "unknown"}
GATE_STATUSES = {"passed", "failed", "not_established", "not_applicable", "unknown"}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def validate_nullable(value: Any, name: str) -> None:
    require(isinstance(value, dict), f"{name} must be an object")
    require(set(value) == {"value", "unavailable_reason"}, f"{name} has unexpected fields")
    measured = value["value"]
    reason = value["unavailable_reason"]
    require(measured is None or (type(measured) in (int, float) and measured >= 0 and math.isfinite(measured)), f"{name}.value invalid")
    require(reason is None or (isinstance(reason, str) and reason), f"{name}.unavailable_reason invalid")
    require((measured is None) != (reason is None), f"{name} requires exactly one of value or unavailable_reason")


def validate_relative(path: str, name: str) -> None:
    value = Path(path)
    require(not value.is_absolute(), f"{name} must be relative")
    require(".." not in value.parts, f"{name} cannot escape evidence root")


def validate_case(case: Any, run_id: str) -> str:
    require(isinstance(case, dict), "case must be an object")
    require(set(case) == {"schema_version", "run_id", "case_id", "observed_order", "attempt", "status", "status_reason", "observed"}, "case fields do not match schema")
    require(case.get("schema_version") == CASE_VERSION, "case schema_version mismatch")
    require(case.get("run_id") == run_id, "case run_id mismatch")
    case_id = case.get("case_id")
    require(isinstance(case_id, str) and case_id, "case_id required")
    require(type(case.get("observed_order")) is int and case["observed_order"] >= 0, "observed_order invalid")
    require(type(case.get("attempt")) is int and case["attempt"] >= 1, "attempt invalid")
    require(case.get("status") in STATUSES, "case status invalid")
    require(isinstance(case.get("status_reason"), str) and case["status_reason"], "case status_reason required")
    require(isinstance(case.get("observed"), dict), "case observed must be an object")
    return case_id


def replacement_path(canonical_root: Path, evidence_root: Path, value: str) -> Path | None:
    canonical_prefix = "docs/implementation/graph-advantage/evidence/canonical-v1/"
    cleanup_prefix = "docs/implementation/graph-advantage/cleanup/"
    if value.startswith(canonical_prefix):
        return canonical_root / value.removeprefix(canonical_prefix)
    if value.startswith(cleanup_prefix):
        return evidence_root.parent / "cleanup" / value.removeprefix(cleanup_prefix)
    if value.startswith("canonical-v1/"):
        return evidence_root / value
    return None


def admitted_legacy_replacements(canonical_root: Path, evidence_root: Path) -> dict[tuple[str, str], dict[str, Any]]:
    path = evidence_root.parent / "cleanup" / "removal-map.json"
    if not path.is_file():
        return {}
    value = json.loads(path.read_text())
    require(value.get("schema") == "graph-advantage-public-artifact-removal-map-v2", "removal map schema mismatch")
    require(value.get("candidate_count") == len(value.get("candidates", [])), "removal map candidate count mismatch")
    admitted: dict[tuple[str, str], dict[str, Any]] = {}
    identities: set[tuple[str, str]] = set()
    for candidate in value.get("candidates", []):
        source_path = candidate.get("path")
        source_sha = candidate.get("source_sha256")
        replacement = candidate.get("replacement")
        require(isinstance(source_path, str) and re.fullmatch(r"[0-9a-f]{64}", source_sha or "") is not None, "removal map source identity missing")
        require(isinstance(replacement, dict), "removal map replacement missing")
        require(candidate.get("disposition") in {"applied", "approved-pending-strict-validation"}, "removal map disposition invalid")
        target = replacement_path(canonical_root, evidence_root, replacement.get("path", ""))
        require(target is not None and target.is_file(), f"removal map replacement missing: {replacement.get('path')}")
        require(replacement.get("retained_sha256") == sha256_file(target), f"removal map replacement digest mismatch: {replacement.get('path')}")
        identity = (source_path, source_sha)
        require(identity not in identities, "removal map source identities must be unique")
        identities.add(identity)
        if candidate.get("disposition") == "applied":
            admitted[identity] = replacement
    return admitted


def validate_run(path: Path, evidence_root: Path, legacy_replacements: dict[tuple[str, str], dict[str, Any]] | None = None) -> None:
    run = json.loads(path.read_text())
    required = {"schema_version", "run_id", "kind", "claim_scope", "provenance", "inputs", "workload", "execution", "semantics", "quality", "coverage", "gates", "artifacts", "limitations", "source_records"}
    allowed = required | {"cases", "redactions"}
    require(required <= set(run) and set(run) <= allowed, f"{path}: fields do not match schema")
    require(run.get("schema_version") == SCHEMA_VERSION, f"{path}: schema_version mismatch")
    run_id = run.get("run_id")
    require(isinstance(run_id, str) and run_id, f"{path}: run_id required")
    require(run.get("kind") in {"correctness", "performance", "quality", "diagnostic", "integration", "unknown"}, f"{path}: kind invalid")
    require(isinstance(run.get("claim_scope"), str) and run["claim_scope"], f"{path}: claim_scope required")
    provenance = run.get("provenance")
    require(isinstance(provenance, dict) and set(provenance) == {"source", "binary", "toolchain"}, f"{path}: provenance invalid")
    require(all(isinstance(provenance[key], dict) for key in provenance), f"{path}: provenance members invalid")
    for key in ("inputs", "workload", "semantics", "quality", "coverage"):
        require(isinstance(run.get(key), dict), f"{path}: {key} must be an object")
    execution = run.get("execution")
    require(isinstance(execution, dict), f"{path}: execution required")
    require(set(execution) == {"status", "status_reason", "exits", "timing", "peak_rss_bytes", "cache_bytes", "invocations"}, f"{path}: execution fields invalid")
    require(execution.get("status") in STATUSES, f"{path}: invalid status")
    require(isinstance(execution.get("status_reason"), str) and execution["status_reason"], f"{path}: status_reason required")
    validate_nullable(execution.get("peak_rss_bytes"), f"{path}: peak_rss_bytes")
    validate_nullable(execution.get("cache_bytes"), f"{path}: cache_bytes")
    require(isinstance(run.get("source_records"), dict), f"{path}: source_records required")
    for source_ref, record in run["source_records"].items():
        if not isinstance(record, dict) or "source_parse_error" not in record:
            continue
        error = record["source_parse_error"]
        require(isinstance(error, dict), f"{path}: malformed source_parse_error")
        require(error.get("source_ref") == source_ref, f"{path}: source_parse_error ref mismatch")
        if error.get("kind") == "json_decode_error":
            digest = error.get("source_sha256")
            require(isinstance(digest, str) and len(digest) == 64, f"{path}: parse error digest missing")
            require(type(error.get("source_size_bytes")) is int and error["source_size_bytes"] >= 0, f"{path}: parse error size missing")
        elif error.get("kind") == "read_error":
            require(error.get("source_sha256") is None, f"{path}: unread source cannot claim a digest")
            require(isinstance(error.get("source_sha256_unavailable_reason"), str), f"{path}: unread source digest reason missing")
        else:
            raise ValueError(f"{path}: unknown source_parse_error kind")
    require(isinstance(run.get("gates"), list), f"{path}: gates must be an array")
    for gate in run["gates"]:
        require(isinstance(gate, dict) and set(gate) == {"id", "status", "reason", "evidence_refs"}, f"{path}: invalid gate fields")
        require(isinstance(gate.get("id"), str) and gate["id"] and gate.get("status") in GATE_STATUSES, f"{path}: invalid gate")
        require(isinstance(gate.get("reason"), str) and isinstance(gate.get("evidence_refs"), list), f"{path}: invalid gate evidence")
    require(isinstance(run.get("artifacts"), list), f"{path}: artifacts must be an array")
    for artifact in run["artifacts"]:
        require(isinstance(artifact, dict) and set(artifact) == {"role", "path", "sha256", "media_type", "retention"}, f"{path}: artifact fields invalid")
        artifact_path = artifact.get("path")
        require(isinstance(artifact_path, str), f"{path}: artifact path required")
        validate_relative(artifact_path, f"{path}: artifact path")
        require(isinstance(artifact.get("sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", artifact["sha256"]) is not None, f"{path}: artifact sha256 invalid")
        require(artifact.get("retention") in {"canonical", "source", "legacy", "external_optional"}, f"{path}: artifact retention invalid")
        require(isinstance(artifact.get("role"), str) and isinstance(artifact.get("media_type"), str), f"{path}: artifact metadata invalid")
        source = evidence_root / artifact_path
        if source.is_file():
            require(artifact.get("sha256") == sha256_file(source), f"{path}: artifact digest mismatch {artifact_path}")
        else:
            require(artifact.get("retention") == "legacy", f"{path}: missing retained artifact {artifact_path}")
            repo_path = f"docs/implementation/graph-advantage/evidence/{artifact_path}"
            require(
                legacy_replacements is not None and (repo_path, artifact.get("sha256")) in legacy_replacements,
                f"{path}: absent legacy artifact lacks an admitted exact replacement: {artifact_path}",
            )
    cases = run.get("cases")
    if cases is None:
        return
    relative = cases.get("path")
    require(isinstance(relative, str) and relative.startswith("../cases/"), f"{path}: cases path invalid")
    case_path = (path.parent / relative).resolve()
    require(case_path.parent == (path.parent.parent / "cases").resolve(), f"{path}: cases path escapes canonical cases")
    require(case_path.is_file(), f"{path}: missing cases")
    require(cases.get("format") == CASE_VERSION, f"{path}: cases format mismatch")
    require(cases.get("sha256") == sha256_file(case_path), f"{path}: cases digest mismatch")
    ids: list[str] = []
    for line_number, line in enumerate(case_path.read_text().splitlines(), 1):
        if line:
            ids.append(validate_case(json.loads(line), run_id))
    require(ids == sorted(ids), f"{path}: cases not sorted")
    require(len(ids) == len(set(ids)), f"{path}: duplicate case_id")
    require(cases.get("count") == len(ids), f"{path}: case count mismatch")


def get_path(value: Any, *parts: str) -> Any:
    for part in parts:
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    return value


def validate_critical_observations(canonical_root: Path, required: bool = False) -> None:
    oracle_path = canonical_root / "critical-observations.json"
    if not oracle_path.is_file():
        require(not required, "critical-observations.json is required for a complete dataset")
        return
    oracle = json.loads(oracle_path.read_text())
    require(oracle.get("schema") == "cleanup-critical-observations-v1", "critical observation schema mismatch")
    mapping = {
        "fullcheck-effa358f-linux-01": "check-effa358f-linux-full-01",
        "compiler-effa358f-linux-01": "diagnostics-linux-effa358f-compiler-01",
        "diagnostic8-effa358f": "diagnostic-dispatch-effa358f",
        "fullcheck-6f23da0a-linux-r1": "check-6f23da0a-linux-full",
    }
    for expected in oracle.get("runs", []):
        run_id = mapping.get(expected.get("id"))
        require(run_id is not None, f"unknown critical observation {expected.get('id')}")
        run = json.loads((canonical_root / "runs" / f"{run_id}.json").read_text())
        expected_status = expected["authoritative_overall"]["status"]
        if expected.get("kind") == "product_diagnostic_timeout" and expected_status == "issue":
            expected_status = "timeout"
        require(get_path(run, "execution", "status") == expected_status, f"{run_id}: critical status mismatch")
        require(get_path(run, "provenance", "source", "commit") == expected["source_commit"], f"{run_id}: critical source mismatch")
        authoritative = expected["authoritative_overall"]
        exit_names = {"transport_exit": "transport", "remote_overall_exit": "product", "mise_check_exit": "test"}
        for source_name, canonical_name in exit_names.items():
            if source_name in authoritative:
                require(get_path(run, "execution", "exits", canonical_name) == authoritative[source_name], f"{run_id}: {canonical_name} exit mismatch")
        stages = expected.get("stage_outcomes", {})
        if "exit_code" in stages:
            require(get_path(run, "execution", "exits", "product") == stages["exit_code"], f"{run_id}: product exit mismatch")
        if "build_exit" in stages:
            require(get_path(run, "execution", "exits", "build") == stages["build_exit"], f"{run_id}: build exit mismatch")
        if "compiler_test_exit" in stages:
            require(get_path(run, "execution", "exits", "test") == stages["compiler_test_exit"], f"{run_id}: compiler test exit mismatch")
        if "go_test_skips" in stages:
            actual_skips = get_path(run, "coverage", "skips")
            expected_skips = stages["go_test_skips"]
            require(isinstance(actual_skips, dict), f"{run_id}: Go skip coverage missing")
            for key in ("total", "live_compiler", "other_categories"):
                require(actual_skips.get(key) == expected_skips.get(key), f"{run_id}: Go skip {key} mismatch")
        if "required_live_named_passed" in stages:
            require(get_path(run, "coverage", "required_live_tests") == stages["required_live_named_passed"], f"{run_id}: live compiler coverage mismatch")
        if "top_level_passed" in stages:
            require(get_path(run, "quality", "compiler_test_top_level_passes") == stages["top_level_passed"], f"{run_id}: compiler top-level count mismatch")
        if "product_invocations" in stages:
            require(get_path(run, "execution", "invocations", "product") == stages["product_invocations"], f"{run_id}: product invocation mismatch")
        if "peak_rss" in stages:
            require(get_path(run, "execution", "peak_rss_bytes", "value") == stages["peak_rss"], f"{run_id}: peak RSS mismatch")
            require(get_path(run, "execution", "peak_rss_bytes", "unavailable_reason") == stages["peak_rss_reason"], f"{run_id}: peak RSS reason mismatch")


def validate_source_reconstruction(canonical_root: Path, required: bool = False) -> None:
    path = canonical_root / "sources.json"
    if not path.is_file():
        require(not required, "sources.json is required for a complete dataset")
        return
    value = json.loads(path.read_text())
    require(value.get("schema") == "graph-advantage-source-archive-reconstruction-compact-v1", "source reconstruction schema mismatch")
    aliases: list[str] = []
    for subset in value.get("unique_subsets", []):
        require(subset.get("status") == "EXACT_SOURCE_SUBSET", "source subset not proven exact")
        for key in ("unsafe_member_count", "link_member_count", "extra_member_count", "mode_mismatch_count", "content_mismatch_count"):
            require(subset.get(key) == 0, f"source subset {key} is nonzero")
        aliases.extend(subset.get("aliases", []))
    require(len(aliases) == 8 and len(set(aliases)) == 8, "source reconstruction must cover eight distinct archives")


def validate_profile_references(canonical_root: Path, required: bool = False) -> None:
    public_path = canonical_root.parents[1] / "cleanup" / "profile-summaries.json"
    if not public_path.is_file():
        require(not required, "profile-summaries.json is required for a complete dataset")
        return
    summary = json.loads(public_path.read_text())
    require(summary.get("schema") == "graph-advantage-profile-summary-v1", "profile summary schema mismatch")
    digest = sha256_file(public_path)
    profiles = {profile.get("run_id"): profile for profile in summary.get("profiles", [])}
    referenced = 0
    for path in sorted((canonical_root / "runs").glob("diagnostic-dispatch-*.json")):
        run = json.loads(path.read_text())
        profile_id = run["run_id"].removeprefix("diagnostic-dispatch-")
        if profile_id not in profiles:
            continue
        reference = get_path(run, "source_records", "profile-summary-ref")
        require(isinstance(reference, dict), f"{run['run_id']}: profile summary reference missing")
        require(reference.get("public_sha256") == digest, f"{run['run_id']}: profile summary digest mismatch")
        require(reference.get("raw_profile_sha256") == profiles[profile_id].get("raw_profile_sha256"), f"{run['run_id']}: raw profile digest mismatch")
        require(reference.get("pprof_rows") == get_path(profiles[profile_id], "pprof", "rows_parsed"), f"{run['run_id']}: pprof row count mismatch")
        referenced += 1
    require(referenced == 6, "six diagnostic profiles must have canonical run references")


def validate_p1_cases(canonical_root: Path, required: bool = False) -> None:
    expected = {
        "p1-corpus-baseline": ({"passed": 69, "partial": 33, "timeout": 6}, {"ok": 69, "partial": 33, "timeout": 6}),
        "p1-corpus-campaign": ({"partial": 113, "timeout": 3}, {"partial": 113, "timeout": 3, "unrun": 1434}),
    }
    for run_id, (case_statuses, source_statuses) in expected.items():
        path = canonical_root / "runs" / f"{run_id}.json"
        if not path.is_file():
            require(not required, f"{run_id} is required for a complete dataset")
            continue
        run = json.loads(path.read_text())
        rows_path = (path.parent / run["cases"]["path"]).resolve()
        statuses: dict[str, int] = {}
        for line in rows_path.read_text().splitlines():
            status = json.loads(line)["status"]
            statuses[status] = statuses.get(status, 0) + 1
        require(statuses == case_statuses, f"{run_id}: measured case status counts mismatch")
        require(get_path(run, "source_records", "p1-summary.json", "status_counts") == source_statuses, f"{run_id}: source status counts mismatch")


def validate_no_exposure(canonical_root: Path) -> None:
    forbidden = ("/Users/", "/home/", "/tmp/", "/opt/", "blob.core.windows.net", "rg-entire-graph-advantage", "graph-validation-linux", "graph-p1-worker-")
    paths = sorted(canonical_root.rglob("*.json")) + sorted((canonical_root / "cases").glob("*.ndjson"))
    local_path = re.compile(r"(?:^|[\s=\"'])(?:/(?:Users|home|tmp|opt|usr|var|private)/|[A-Za-z]:[\\/]|\\\\)")
    path_field = re.compile(r"(?:^|_)(?:path|root|dir|directory|home|binary|executable|command|environment|env|workspace|repo|repository|cwd|goroot|gopath)(?:$|_)")

    def check_fields(value: Any, field: str = "") -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                check_fields(item, key)
        elif isinstance(value, list):
            for item in value:
                check_fields(item, field)
        elif isinstance(value, str) and path_field.search(field.lower()):
            require(local_path.search(value) is None, f"local filesystem path retained in field {field}")

    for path in paths:
        text = path.read_text()
        require(not any(marker in text for marker in forbidden), f"{path}: local path or infrastructure identifier retained")
        if path.suffix == ".ndjson":
            for line in text.splitlines():
                if line:
                    check_fields(json.loads(line))
        else:
            check_fields(json.loads(text))


def validate_index(canonical_root: Path, required: bool = False) -> None:
    path = canonical_root / "index.json"
    if not path.is_file():
        require(not required, "index.json is required for a complete dataset")
        return
    value = json.loads(path.read_text())
    require(value.get("schema") == "graph-advantage-evidence-index-v1", "canonical index schema mismatch")
    indexed = value.get("runs")
    require(isinstance(indexed, list), "canonical index runs missing")
    actual_paths = sorted((canonical_root / "runs").glob("*.json"), key=lambda run_path: run_path.stem)
    require([item.get("run_id") for item in indexed] == [path.stem for path in actual_paths], "canonical index run coverage mismatch")
    for item, run_path in zip(indexed, actual_paths, strict=True):
        require(item.get("path") == f"runs/{run_path.name}", f"canonical index path mismatch: {run_path.name}")
        require(item.get("sha256") == sha256_file(run_path), f"canonical index digest mismatch: {run_path.name}")


def validate_verification(canonical_root: Path, required: bool = False) -> None:
    path = canonical_root / "verification.json"
    if not path.is_file():
        require(not required, "verification.json is required for a complete dataset")
        return
    value = json.loads(path.read_text())
    require(value.get("schema") == "graph-advantage-canonical-verification-v1", "canonical verification schema mismatch")
    runs = [json.loads(run_path.read_text()) for run_path in sorted((canonical_root / "runs").glob("*.json"))]
    statuses: dict[str, int] = {}
    for run in runs:
        status = get_path(run, "execution", "status")
        statuses[status] = statuses.get(status, 0) + 1
    case_files = sorted((canonical_root / "cases").glob("*.ndjson"))
    case_count = sum(len(case_path.read_text().splitlines()) for case_path in case_files)
    require(value.get("run_statuses") == statuses, "canonical verification status counts mismatch")
    require(value.get("case_files") == len(case_files), "canonical verification case file count mismatch")
    require(value.get("cases") == case_count, "canonical verification case count mismatch")
    commands = value.get("commands")
    require(isinstance(commands, list) and commands and all(item.get("result") == "passed" for item in commands), "canonical verification commands incomplete")


def validate_tree(canonical_root: Path, evidence_root: Path, complete: bool = False) -> int:
    paths = sorted((canonical_root / "runs").glob("*.json"))
    require(bool(paths), "no canonical run records")
    replacements = admitted_legacy_replacements(canonical_root, evidence_root)
    for path in paths:
        validate_run(path, evidence_root, replacements)
    validate_index(canonical_root, complete)
    validate_verification(canonical_root, complete)
    validate_critical_observations(canonical_root, complete)
    validate_source_reconstruction(canonical_root, complete)
    validate_profile_references(canonical_root, complete)
    validate_p1_cases(canonical_root, complete)
    validate_no_exposure(canonical_root)
    return len(paths)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--canonical-root", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    args = parser.parse_args()
    count = validate_tree(args.canonical_root.resolve(), args.evidence_root.resolve(), complete=True)
    print(json.dumps({"schema_version": SCHEMA_VERSION, "validated_runs": count}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
