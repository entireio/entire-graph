#!/usr/bin/env python3
"""Convert reviewed Graph Advantage evidence into compact canonical records.

The converter copies structured facts only. It never parses prose or command
logs to infer a pass, metric, identity, or missing value.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import tarfile
from pathlib import Path, PurePosixPath
from typing import Any, Iterable

SCHEMA_VERSION = "graph-advantage-evidence/v1"
CASE_VERSION = "graph-advantage-evidence-case/v1"

RUN_PREFIXES = (
    "check-",
    "correctness-",
    "diagnostic-",
    "diagnostic-dispatch-",
    "diagnostic-graph-",
    "diagnostics-linux-",
    "statusline-linux-",
)
STRUCTURED_NAMES = (
    "result.json",
    "runtime-manifest.json",
    "verification.json",
    "build-manifest.json",
    "stage-results.json",
    "subprocess-result.json",
    "manifest.json",
    "source-provenance.json",
    "provenance.json",
    "test-counts.json",
    "full-check-gate.json",
)
RAW_STRUCTURED_NAMES = (
    "outcome.json",
    "process.json",
    "identity.json",
    "manifest.json",
    "profile-status.json",
    "diagnostics.json",
)
ABSOLUTE_PATH_PREFIXES = ("/Users/", "/home/", "/tmp/", "/opt/")
OPERATIONAL_MARKERS = ("blob.core.windows.net", "rg-entire-graph-advantage", "graph-validation-linux", "graph-p1-worker-")
LOCAL_PATH_RE = re.compile(r"(?:^|[\s=\"'])(?:/(?:Users|home|tmp|opt|usr|var|private)/|[A-Za-z]:[\\/]|\\\\)")
PATH_FIELD_RE = re.compile(r"(?:^|_)(?:path|root|dir|directory|home|binary|executable|command|environment|env|workspace|repo|repository|cwd|goroot|gopath)(?:$|_)")


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False) + "\n").encode()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_json(path: Path) -> Any:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def sanitize(value: Any, pointer: str = "") -> tuple[Any, list[dict[str, str]]]:
    """Remove local filesystem and signed-query values without hiding omission."""
    redactions: list[dict[str, str]] = []
    if isinstance(value, dict):
        clean: dict[str, Any] = {}
        for key, item in value.items():
            child = f"{pointer}/{key}"
            if any(marker in key for marker in OPERATIONAL_MARKERS):
                redactions.append({"path": pointer or "/", "reason": "operational infrastructure identifier key omitted"})
                continue
            cleaned, found = sanitize(item, child)
            clean[key] = cleaned
            redactions.extend(found)
        return clean, redactions
    if isinstance(value, list):
        clean_list = []
        for index, item in enumerate(value):
            cleaned, found = sanitize(item, f"{pointer}/{index}")
            clean_list.append(cleaned)
            redactions.extend(found)
        return clean_list, redactions
    if isinstance(value, str):
        path_bearing = any(PATH_FIELD_RE.search(part.lower()) for part in pointer.split("/") if part)
        if path_bearing and re.fullmatch(r"/usr/(?:local/)?bin/[A-Za-z0-9._+-]+", value):
            return Path(value).name, [{"path": pointer or "/", "reason": "local executable path normalized to basename"}]
        if any(prefix in value for prefix in ABSOLUTE_PATH_PREFIXES) or (path_bearing and LOCAL_PATH_RE.search(value)):
            return None, [{"path": pointer or "/", "reason": "local filesystem path omitted"}]
        if value.startswith(("http://", "https://")) or any(marker in value for marker in OPERATIONAL_MARKERS):
            return None, [{"path": pointer or "/", "reason": "operational network or infrastructure identifier omitted"}]
        if "?" in value and any(token in value.split("?", 1)[1] for token in ("sig=", "sv=", "se=")):
            return None, [{"path": pointer or "/", "reason": "signed query value omitted"}]
    return value, redactions


def first(mapping: dict[str, Any], *names: str) -> Any:
    for name in names:
        if name in mapping:
            return mapping[name]
    return None


def find_nested(records: dict[str, Any], *names: str) -> Any:
    for record in records.values():
        if not isinstance(record, dict):
            continue
        value = first(record, *names)
        if value is not None:
            return value
    return None


def classify_kind(run_id: str) -> str:
    if run_id.startswith("correctness-") or run_id.startswith("check-") or run_id.startswith("statusline-"):
        return "correctness" if run_id.startswith("correctness-") else "integration"
    if run_id.startswith("diagnostic-"):
        return "diagnostic"
    if run_id.startswith("extraction-") or run_id.startswith("relation-"):
        return "performance"
    if run_id.startswith("compiler-quality-"):
        return "quality"
    return "unknown"


def record_named(records: dict[str, Any], name: str) -> tuple[str, dict[str, Any]] | None:
    for path, record in records.items():
        if PurePosixPath(path).name == name and isinstance(record, dict):
            return path, record
    return None


def normalized_status(raw: Any) -> str | None:
    if not isinstance(raw, str):
        return None
    lowered = raw.lower()
    if lowered.startswith("prepared"):
        return "not_run"
    return {
        "pass": "passed", "passed": "passed", "success": "passed", "ok": "passed", "complete": "passed", "completed": "passed", "collected": "passed",
        "fail": "failed", "failed": "failed", "error": "failed",
        "partial": "partial", "incomplete": "partial",
        "timeout": "timeout", "timed_out": "timeout",
        "not_run": "not_run", "skipped": "not_run",
    }.get(lowered)


def explicit_status(run_id: str, records: dict[str, Any]) -> tuple[str, str]:
    """Read only authoritative run-level outcomes for the run kind."""
    signals: list[tuple[str, str]] = []
    preparation_signals: list[tuple[str, str]] = []

    def add_status(label: str, value: Any) -> None:
        status = normalized_status(value)
        if status:
            target = preparation_signals if status == "not_run" else signals
            target.append((status, f"explicit {label} status={value}"))

    result_match = record_named(records, "result.json")
    if result_match:
        label, record = result_match
        add_status(label, record.get("status"))
        if record.get("full_check_pass") is True:
            signals.append(("passed", f"explicit {label} full_check_pass=true"))
        elif record.get("full_check_pass") is False:
            signals.append(("failed", f"explicit {label} full_check_pass=false"))

    runtime_match = record_named(records, "runtime-manifest.json")
    if runtime_match:
        label, record = runtime_match
        add_status(label, record.get("status"))
        add_status(label, record.get("outcome_status"))
        if record.get("timed_out") is True or record.get("deadline_exceeded") is True:
            signals.append(("timeout", f"explicit {label} timeout marker"))
        process = record.get("process")
        if isinstance(process, dict):
            if process.get("timed_out") is True or process.get("deadline_exceeded") is True:
                signals.append(("timeout", f"explicit {label} process timeout marker"))
            add_status(f"{label} process", process.get("status"))
            if type(process.get("exit_code")) is int:
                signals.append(("passed" if process["exit_code"] == 0 else "failed", f"explicit {label} process.exit_code={process['exit_code']}"))
        if type(record.get("collector_exit_code")) is int and record["collector_exit_code"] != 0:
            signals.append(("failed", f"explicit {label} collector_exit_code={record['collector_exit_code']}"))
        request = record.get("request")
        if isinstance(request, dict):
            add_status(f"{label} request", request.get("status"))
            if request.get("timed_out") is True or request.get("deadline_exceeded") is True:
                signals.append(("timeout", f"explicit {label} request timeout marker"))
            if type(request.get("process_exit_code")) is int:
                signals.append(("passed" if request["process_exit_code"] == 0 else "failed", f"explicit {label} request.process_exit_code={request['process_exit_code']}"))
        if type(record.get("partial_failures_count")) is int and record["partial_failures_count"] > 0:
            signals.append(("partial", f"explicit {label} partial_failures_count={record['partial_failures_count']}"))

    outcome_match = record_named(records, "outcome.json")
    if outcome_match:
        label, record = outcome_match
        add_status(label, record.get("status"))
        issue = record.get("issue")
        if isinstance(issue, str) and issue:
            status = "timeout" if "timed out" in issue.lower() or "timeout" in issue.lower() else "failed"
            signals.append((status, f"explicit {label} issue={issue}"))

    process_match = record_named(records, "process.json")
    if process_match:
        label, record = process_match
        if record.get("timed_out") is True:
            signals.append(("timeout", f"explicit {label} timed_out=true"))
        if type(record.get("exit_code")) is int:
            signals.append(("passed" if record["exit_code"] == 0 else "failed", f"explicit {label} exit_code={record['exit_code']}"))

    diagnostics_match = record_named(records, "diagnostics.json")
    if diagnostics_match:
        label, record = diagnostics_match
        add_status(label, record.get("status"))
        if type(record.get("partial_failures_count")) is int and record["partial_failures_count"] > 0:
            signals.append(("partial", f"explicit {label} partial_failures_count={record['partial_failures_count']}"))

    verification_match = record_named(records, "verification.json")
    if verification_match:
        label, record = verification_match
        add_status(label, record.get("status"))
        if type(record.get("exit_code")) is int:
            signals.append(("passed" if record["exit_code"] == 0 else "failed", f"explicit {label} exit_code={record['exit_code']}"))

    subprocess_match = record_named(records, "subprocess-result.json")
    if subprocess_match:
        label, record = subprocess_match
        add_status(label, record.get("status"))
        if type(record.get("exit_code")) is int:
            signals.append(("passed" if record["exit_code"] == 0 else "failed", f"explicit {label} exit_code={record['exit_code']}"))

    manifest_match = record_named(records, "manifest.json")
    if manifest_match:
        label, record = manifest_match
        add_status(label, record.get("status"))
        if run_id.startswith("diagnostic-dispatch-") and type(record.get("collector_exit_code")) is int:
            signals.append(("passed" if record["collector_exit_code"] == 0 else "failed", f"explicit {label} collector_exit_code={record['collector_exit_code']}"))

    p1_match = record_named(records, "p1-summary.json")
    if p1_match:
        add_status(p1_match[0], p1_match[1].get("status"))

    if run_id.startswith("diagnostics-linux-"):
        build_match = record_named(records, "build-manifest.json")
        if build_match:
            label, record = build_match
            add_status(label, record.get("status"))
            for key in ("exit_code", "build_exit_code"):
                if type(record.get(key)) is int:
                    signals.append(("passed" if record[key] == 0 else "failed", f"explicit {label} {key}={record[key]}"))

    if run_id.startswith("diagnostic-dispatch-") and not runtime_match:
        product = find_nested(records, "product_invocations")
        corpus = find_nested(records, "corpus_invocations")
        diagnostic = find_nested(records, "diagnostic_invocations")
        known = [value for value in (product, corpus, diagnostic) if type(value) is int]
        if known and all(value == 0 for value in known):
            preparation_signals.append(("not_run", "explicit zero product/corpus/diagnostic invocations and no runtime manifest"))

    for desired in ("timeout", "failed", "partial", "not_run", "passed"):
        for status, reason in signals:
            if status == desired:
                return status, reason
    if preparation_signals:
        return preparation_signals[0]
    return "unknown", "no explicit structured terminal outcome"


def extract_gates(records: dict[str, Any]) -> list[dict[str, Any]]:
    gates: list[dict[str, Any]] = []
    for path, record in records.items():
        if not isinstance(record, dict):
            continue
        for key in ("before_after_identity_equal", "source_before_after_equal"):
            value = record.get(key)
            if type(value) is bool:
                gates.append({
                    "id": f"source-identity:{path}:{key}",
                    "status": "passed" if value else "failed",
                    "reason": f"explicit {key}={str(value).lower()}",
                    "evidence_refs": [path],
                })
        mismatches = record.get("post_identity_mismatches")
        if isinstance(mismatches, list):
            gates.append({
                "id": f"source-identity:{path}:post_identity_mismatches",
                "status": "passed" if not mismatches else "failed",
                "reason": "explicit mismatch list is empty" if not mismatches else f"explicit mismatch count={len(mismatches)}",
                "evidence_refs": [path],
            })
        full_check = record.get("full_check_pass")
        if type(full_check) is bool:
            gates.append({
                "id": f"full-check:{path}",
                "status": "passed" if full_check else "failed",
                "reason": f"explicit full_check_pass={str(full_check).lower()}",
                "evidence_refs": [path],
            })
        compiler_contract = record.get("compiler_contract_pass")
        if type(compiler_contract) is bool:
            gates.append({
                "id": f"compiler-contract:{path}",
                "status": "passed" if compiler_contract else "failed",
                "reason": f"explicit compiler_contract_pass={str(compiler_contract).lower()}",
                "evidence_refs": [path],
            })
    return sorted(gates, key=lambda gate: gate["id"])


def extract_exits(records: dict[str, Any]) -> dict[str, Any]:
    exits = {"transport": None, "controller": None, "collector": None, "product": None, "test": None, "build": None}
    result_match = record_named(records, "result.json")
    if result_match:
        record = result_match[1]
        exits["transport"] = first(record, "transport_exit")
        exits["test"] = first(record, "mise_check_exit", "check_exit")
        exits["product"] = first(record, "remote_overall_exit", "product_exit")
    runtime_match = record_named(records, "runtime-manifest.json")
    if runtime_match:
        record = runtime_match[1]
        exits["controller"] = first(record, "controller_exit_code", "controller_exit")
        exits["collector"] = first(record, "collector_exit_code", "collector_exit")
        exits["product"] = first(record, "process_exit_code", "process_exit")
        if isinstance(record.get("transport"), dict):
            exits["controller"] = first(record["transport"], "controller_exit_code", "controller_exit")
            exits["collector"] = first(record["transport"], "collector_exit_code", "collector_exit")
        if isinstance(record.get("request"), dict):
            exits["product"] = first(record["request"], "process_exit_code", "process_exit")
        if isinstance(record.get("process"), dict):
            exits["product"] = first(record["process"], "exit_code", "process_exit_code")
            controller = first(record["process"], "controller_exit_code", "controller_exit")
            collector = first(record["process"], "collector_exit_code", "collector_exit")
            if controller is not None:
                exits["controller"] = controller
            if collector is not None:
                exits["collector"] = collector
    build_match = record_named(records, "build-manifest.json")
    if build_match:
        exits["build"] = first(build_match[1], "build_exit_code", "exit_code")
        compiler_test_exit = first(build_match[1], "compiler_test_exit_code")
        if compiler_test_exit is not None:
            exits["test"] = compiler_test_exit
    verification_match = record_named(records, "verification.json")
    if verification_match:
        exits["test"] = first(verification_match[1], "exit_code", "test_exit_code")
    subprocess_match = record_named(records, "subprocess-result.json")
    if subprocess_match:
        exits["test"] = first(subprocess_match[1], "exit_code")
    return exits


def nullable_measurement(value: Any, reason: str) -> dict[str, Any]:
    if type(value) in (int, float) and value >= 0 and math.isfinite(value):
        return {"value": value, "unavailable_reason": None}
    return {"value": None, "unavailable_reason": reason}


def named_counts(records: dict[str, Any]) -> dict[str, Any]:
    counts = {"reserved": None, "preclaim": None, "product": None, "corpus": None, "retry": None}
    aliases = {
        "reserved": ("reserved", "reservations", "effective_attempts"),
        "preclaim": ("preclaim", "pre_claim", "preparation_invocations"),
        "product": ("product", "product_invocations", "product_or_corpus_invocations"),
        "corpus": ("corpus", "corpus_invocations"),
        "retry": ("retry", "retries", "retry_performed"),
    }
    for record in records.values():
        if not isinstance(record, dict):
            continue
        candidates = [record]
        if isinstance(record.get("claim_counts"), dict):
            candidates.insert(0, record["claim_counts"])
        for target, names in aliases.items():
            if counts[target] is not None:
                continue
            for candidate in candidates:
                value = first(candidate, *names)
                if type(value) is bool:
                    counts[target] = int(value)
                    break
                if type(value) is int and value >= 0:
                    counts[target] = value
                    break
        before = first(record, "product_diagnostic_invocations_before", "historical_product_invocations_before", "resumed_product_invocations_before")
        after = first(record, "product_diagnostic_invocations_after", "historical_product_invocations_after", "resumed_product_invocations_after")
        if counts["product"] is None and type(before) is int and type(after) is int and after >= before:
            counts["product"] = after - before
        claim_counts = record.get("claim_counts")
        if isinstance(claim_counts, dict):
            before = first(claim_counts, "historical_product_invocations_before", "resumed_product_invocations_before")
            after = first(claim_counts, "historical_product_invocations_after", "resumed_product_invocations_after")
            if counts["product"] is None and type(before) is int and type(after) is int and after >= before:
                counts["product"] = after - before
    return counts


def collect_records(run_dir: Path) -> tuple[dict[str, Any], list[dict[str, str]]]:
    records: dict[str, Any] = {}
    redactions: list[dict[str, str]] = []

    def collect(path: Path) -> None:
        relative_name = path.relative_to(run_dir).as_posix()
        try:
            payload = path.read_bytes()
        except OSError:
            records[relative_name] = {
                "source_parse_error": {
                    "kind": "read_error",
                    "source_ref": relative_name,
                    "source_sha256": None,
                    "source_sha256_unavailable_reason": "source bytes could not be read",
                }
            }
            return
        try:
            value = json.loads(payload)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            detail: dict[str, Any] = {
                "kind": "json_decode_error",
                "source_ref": relative_name,
                "source_sha256": hashlib.sha256(payload).hexdigest(),
                "source_size_bytes": len(payload),
            }
            if isinstance(error, json.JSONDecodeError):
                detail.update({"line": error.lineno, "column": error.colno})
            records[relative_name] = {"source_parse_error": detail}
            return
        clean, removed = sanitize(value, f"/source_records/{relative_name}")
        records[relative_name] = clean
        redactions.extend(removed)

    for name in STRUCTURED_NAMES:
        paths = [run_dir / name]
        if name == "runtime-manifest.json":
            paths.append(run_dir / "controller" / name)
        for attempt in sorted(run_dir.glob("attempt-*")):
            paths.append(attempt / name)
        for path in paths:
            if not path.is_file():
                continue
            collect(path)
    for name in RAW_STRUCTURED_NAMES:
        for path in (run_dir / "raw" / "output" / name, run_dir / "controller" / "raw" / "output" / name):
            if not path.is_file():
                continue
            collect(path)
    return records, redactions


def artifact_for(path: Path, evidence_root: Path, role: str = "structured-source") -> dict[str, str]:
    relative = path.relative_to(evidence_root).as_posix()
    media = "application/x-ndjson" if path.suffix == ".ndjson" else "application/json"
    return {"role": role, "path": relative, "sha256": sha256_file(path), "media_type": media, "retention": "legacy"}


def profile_references(evidence_root: Path) -> dict[str, dict[str, Any]]:
    path = evidence_root.parent / "cleanup" / "profile-summaries.json"
    if not path.is_file():
        return {}
    summary = load_json(path)
    if summary.get("schema") != "graph-advantage-profile-summary-v1":
        raise ValueError("profile summary schema mismatch")
    digest = sha256_file(path)
    references: dict[str, dict[str, Any]] = {}
    for profile in summary.get("profiles", []):
        profile_id = profile.get("run_id")
        if not isinstance(profile_id, str) or not profile_id:
            raise ValueError("profile summary run_id missing")
        references[profile_id] = {
            "schema": summary["schema"],
            "public_path": "docs/implementation/graph-advantage/cleanup/profile-summaries.json",
            "public_sha256": digest,
            "profile_run_id": profile_id,
            "raw_profile_sha256": profile.get("raw_profile_sha256"),
            "raw_profile_git_blob": profile.get("raw_profile_git_blob"),
            "raw_profile_bytes": profile.get("raw_profile_bytes"),
            "pprof_rows": profile.get("pprof", {}).get("rows_parsed"),
        }
    return references


def make_run(run_id: str, records: dict[str, Any], redactions: list[dict[str, str]]) -> dict[str, Any]:
    status, status_reason = explicit_status(run_id, records)
    source_commit = find_nested(records, "source_commit", "frozen_source_commit")
    source_tree = find_nested(records, "source_tree", "tree_sha256", "source_digest")
    binary_sha = find_nested(records, "binary_sha256", "evaluator_binary_sha256")
    build_manifest_sha = find_nested(records, "build_manifest_sha256")
    go_version = find_nested(records, "go_version")
    mise_version = find_nested(records, "mise_version")
    operation = find_nested(records, "operation", "verb")
    profile = find_nested(records, "profile")
    mode = find_nested(records, "mode")
    cache_mode = find_nested(records, "cache_mode")
    elapsed = find_nested(records, "elapsed_ns", "product_ns", "wall_ns")
    elapsed_seconds = find_nested(records, "full_check_elapsed_seconds", "elapsed_seconds")
    peak_rss = find_nested(records, "peak_rss_bytes", "max_rss_bytes")
    peak_rss_reason = find_nested(records, "rss_error", "peak_rss_status") or "not present in structured source evidence"
    cache_bytes = find_nested(records, "cache_bytes")
    partial_count = find_nested(records, "partial_failures_count")
    warnings_count = find_nested(records, "warnings_count")
    skips = find_nested(records, "go_test_skips", "compiler_test_skips", "skips")
    return {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "kind": classify_kind(run_id),
        "claim_scope": "historical evidence normalization; no new product or release claim",
        "provenance": {
            "source": {"commit": source_commit, "tree_or_digest": source_tree},
            "binary": {"sha256": binary_sha, "build_manifest_sha256": build_manifest_sha},
            "toolchain": {"go": go_version, "mise": mise_version},
        },
        "inputs": {
            "manifest_sha256": find_nested(records, "input_manifest_sha256", "tracked_manifest_sha256"),
            "effective_sha256": find_nested(records, "effective_input_sha256", "corpus_sha256"),
            "origin": find_nested(records, "fixture_origin", "label_origin"),
        },
        "workload": {"operation": operation, "profile": profile, "mode": mode, "cache_mode": cache_mode},
        "execution": {
            "status": status,
            "status_reason": status_reason,
            "exits": extract_exits(records),
            "timing": {"elapsed_ns": elapsed, "elapsed_seconds": elapsed_seconds},
            "peak_rss_bytes": nullable_measurement(peak_rss, str(peak_rss_reason)),
            "cache_bytes": nullable_measurement(cache_bytes, "not present in structured source evidence"),
            "invocations": named_counts(records),
        },
        "semantics": {
            "equal": find_nested(records, "semantic_equal", "before_after_identity_equal", "source_before_after_equal"),
            "digest": find_nested(records, "semantic_sha256", "semantic_digest"),
            "completeness": find_nested(records, "completeness"),
            "partial_failures": {"count": partial_count, "sha256": find_nested(records, "partial_failures_sha256")},
            "warnings": {"count": warnings_count, "sha256": find_nested(records, "warnings_sha256")},
        },
        "quality": {
            "compiler_test_top_level_passes": find_nested(records, "compiler_test_top_level_passes"),
            "compiler_test_failures": find_nested(records, "compiler_test_failures"),
        },
        "coverage": {
            "skips": skips,
            "required_live_tests": find_nested(records, "compiler_test_required_live_passes"),
            "task_results": find_nested(records, "task_results"),
        },
        "gates": extract_gates(records),
        "artifacts": [],
        "limitations": ["Fields absent from structured source evidence remain null; logs and prose were not parsed."],
        "source_records": records,
        "redactions": redactions,
    }


def stable_case_id(run_id: str, row: dict[str, Any], index: int) -> str:
    parts: list[str] = [run_id]
    for key in ("case_id", "task", "id", "category", "repository", "worker", "size", "language", "phase", "operation", "mode", "cache_mode", "profile", "scenario", "verb", "arm", "reuse", "trial", "repetition", "attempt", "mutation_id"):
        if key in row:
            parts.append(f"{key}={row[key]}")
    if len(parts) == 1:
        parts.append(f"observed_order={index}")
    return "/".join(str(part).replace("/", "_") for part in parts)


def normalize_rows(run_id: str, rows: Iterable[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, str]]]:
    result: list[dict[str, Any]] = []
    redactions: list[dict[str, str]] = []
    source_rows = list(rows)
    base_ids = [stable_case_id(run_id, row, index) for index, row in enumerate(source_rows)]
    id_counts = {case_id: base_ids.count(case_id) for case_id in set(base_ids)}
    occurrences: dict[str, int] = {}
    for index, row in enumerate(source_rows):
        clean, removed = sanitize(row, f"/cases/{index}/observed")
        status = "timeout" if row.get("timed_out") is True else normalized_status(row.get("status")) or "unknown"
        case_id = base_ids[index]
        if id_counts[case_id] > 1:
            occurrences[case_id] = occurrences.get(case_id, 0) + 1
            case_id = f"{case_id}/occurrence={occurrences[case_id]}"
        case = {
            "schema_version": CASE_VERSION,
            "run_id": run_id,
            "case_id": case_id,
            "observed_order": index,
            "attempt": row.get("attempt", 1),
            "status": status,
            "status_reason": f"explicit observed status={row.get('status')}" if row.get("status") is not None else "no explicit case status",
            "observed": clean,
        }
        result.append(case)
        redactions.extend(removed)
    result.sort(key=lambda item: item["case_id"])
    return result, redactions


def read_ndjson(path: Path) -> list[dict[str, Any]]:
    rows = []
    with path.open(encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, 1):
            if not line.strip():
                continue
            value = json.loads(line)
            if not isinstance(value, dict):
                raise ValueError(f"{path}:{line_number}: expected object")
            rows.append(value)
    return rows


def write_cases(run_id: str, rows: list[dict[str, Any]], output: Path) -> tuple[dict[str, Any], list[dict[str, str]]]:
    normalized, redactions = normalize_rows(run_id, rows)
    payload = b"".join(canonical_bytes(row) for row in normalized)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(payload)
    return {"path": f"../cases/{output.name}", "sha256": sha256_bytes(payload), "count": len(normalized), "format": CASE_VERSION}, redactions


def dataset_rows(evidence_root: Path, run_id: str) -> tuple[list[dict[str, Any]], list[Path]]:
    if run_id.startswith("extraction-paired-") or run_id == "relation-profile-v1":
        path = evidence_root / f"{run_id}.ndjson"
        return read_ndjson(path), [path]
    if run_id == "compiler-quality-v1":
        path = evidence_root / "compiler-quality-v1.json"
        data = load_json(path)
        return list(data["rows"]), [path]
    raise ValueError(run_id)


def query_rows(run_dir: Path) -> tuple[list[dict[str, Any]], list[Path]]:
    paths = [path for path in (run_dir / "queries.json", run_dir / "raw" / "queries.json") if path.is_file()]
    if not paths:
        return [], []
    path = paths[0]
    data = load_json(path)
    rows = []
    for order, (case_id, response) in enumerate(data.get("responses", {}).items()):
        rows.append({
            "case_id": case_id,
            "fixture_origin": data.get("fixture_origin"),
            "failed": data.get("failed"),
            "source_sha256": data.get("source_sha256"),
            "response": response,
            "response_order": order,
        })
    return rows, [path]


def response_rows(path: Path) -> list[dict[str, Any]]:
    data = load_json(path)
    rows = []
    for order, (case_id, response) in enumerate(data.get("responses", {}).items()):
        rows.append({
            "case_id": case_id,
            "fixture_origin": data.get("fixture_origin"),
            "failed": data.get("failed"),
            "response": response,
            "response_order": order,
        })
    return rows


def observation_rows(run_dir: Path) -> tuple[list[dict[str, Any]], list[Path]]:
    for path in (run_dir / "raw" / "output" / "observation.ndjson", run_dir / "controller" / "raw" / "output" / "observation.ndjson"):
        if path.is_file():
            return read_ndjson(path), [path]
    return [], []


def p1_rows(p1_root: Path, run_id: str) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    sources: list[dict[str, Any]] = []
    status_counts: dict[str, int] = {}
    if run_id == "p1-corpus-baseline":
        paths = sorted((p1_root / "baseline-raw").glob("worker-*/baseline.ndjson"))
        for path in paths:
            worker = path.parent.name
            source_rows = read_ndjson(path)
            for row in source_rows:
                row = dict(row)
                row["worker"] = worker
                rows.append(row)
                raw_status = str(row.get("status", "unknown"))
                status_counts[raw_status] = status_counts.get(raw_status, 0) + 1
            sources.append({"path": path.relative_to(p1_root.parent).as_posix(), "sha256": sha256_file(path), "rows": len(source_rows)})
    elif run_id == "p1-corpus-campaign":
        for path in sorted((p1_root / "paused-raw").glob("worker-*.tar.gz")):
            worker = path.stem.removesuffix(".tar")
            with tarfile.open(path, "r:gz") as archive:
                member = archive.extractfile("results/campaign.ndjson")
                if member is None:
                    raise ValueError(f"{path}: missing results/campaign.ndjson")
                source_rows = [json.loads(line) for line in member if line.strip()]
            for row in source_rows:
                raw_status = str(row.get("status", "unknown"))
                status_counts[raw_status] = status_counts.get(raw_status, 0) + 1
                if raw_status == "unrun":
                    continue
                row = dict(row)
                row["worker"] = worker
                rows.append(row)
            sources.append({
                "path": path.relative_to(p1_root.parent).as_posix(),
                "sha256": sha256_file(path),
                "member": "results/campaign.ndjson",
                "rows": len(source_rows),
            })
    else:
        raise ValueError(run_id)
    return rows, {"status": "partial", "status_counts": status_counts, "measured_rows": len(rows), "sources": sources}


def write_run(run: dict[str, Any], output: Path) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(canonical_bytes(run))


def convert(evidence_root: Path, output_root: Path) -> list[str]:
    run_ids = [path.name for path in evidence_root.iterdir() if path.is_dir() and path.name.startswith(RUN_PREFIXES)]
    possible_datasets = ["extraction-paired-v1", "extraction-paired-v2", "relation-profile-v1", "compiler-quality-v1"]
    datasets = [run_id for run_id in possible_datasets if (evidence_root / f"{run_id}.ndjson").is_file() or (run_id == "compiler-quality-v1" and (evidence_root / "compiler-quality-v1.json").is_file())]
    response_datasets = [
        path.stem for path in (
            evidence_root / "advantage-combination-v1.json",
            evidence_root / "advantage-combination-v2.json",
            evidence_root / "review-linux-combination-initial.json",
            evidence_root / "review-linux-combination.json",
            evidence_root / "review-linux-queries-initial.json",
            evidence_root / "review-linux-queries.json",
        ) if path.is_file()
    ]
    p1_root = evidence_root.parent / "p1-corpus-20260905"
    p1_datasets = ["p1-corpus-baseline", "p1-corpus-campaign"] if p1_root.is_dir() else []
    profiles = profile_references(evidence_root)
    converted: list[str] = []
    for run_id in sorted(set(run_ids + datasets + response_datasets + p1_datasets)):
        run_dir = evidence_root / run_id
        records, redactions = collect_records(run_dir) if run_dir.is_dir() else ({}, [])
        if run_id == "compiler-quality-v1":
            source = load_json(evidence_root / "compiler-quality-v1.json")
            clean, removed = sanitize({key: source.get(key) for key in ("manifest", "label_origin", "compiler_contract_pass", "source_sha256")}, "/source_records/compiler-quality-v1.json")
            records["compiler-quality-v1.json"] = clean
            redactions.extend(removed)
        if run_id in response_datasets:
            source_path = evidence_root / f"{run_id}.json"
            source = load_json(source_path)
            clean, removed = sanitize({key: source.get(key) for key in ("failed", "fixture_origin")}, f"/source_records/{source_path.name}")
            records[source_path.name] = clean
            redactions.extend(removed)
        p1_case_rows: list[dict[str, Any]] = []
        if run_id in p1_datasets:
            p1_case_rows, summary = p1_rows(p1_root, run_id)
            clean, removed = sanitize(summary, "/source_records/p1-summary.json")
            records["p1-summary.json"] = clean
            redactions.extend(removed)
        if run_id.startswith("diagnostic-dispatch-"):
            profile_id = run_id.removeprefix("diagnostic-dispatch-")
            if profile_id in profiles:
                records["profile-summary-ref"] = profiles[profile_id]
        run = make_run(run_id, records, redactions)
        rows: list[dict[str, Any]] = []
        sources: list[Path] = []
        if run_id in datasets:
            rows, sources = dataset_rows(evidence_root, run_id)
        elif run_id in response_datasets:
            source_path = evidence_root / f"{run_id}.json"
            rows, sources = response_rows(source_path), [source_path]
        elif run_id in p1_datasets:
            rows = p1_case_rows
        elif run_dir.is_dir():
            rows, sources = query_rows(run_dir)
            if not rows:
                rows, sources = observation_rows(run_dir)
        if rows:
            case_info, case_redactions = write_cases(run_id, rows, output_root / "cases" / f"{run_id}.ndjson")
            run["cases"] = case_info
            run["redactions"].extend(case_redactions)
        for source in sources:
            run["artifacts"].append(artifact_for(source, evidence_root, "case-source"))
        for name in records:
            path = run_dir / name if run_dir.is_dir() else evidence_root / name
            if path.is_file():
                try:
                    run["artifacts"].append(artifact_for(path, evidence_root))
                except OSError:
                    # The source record already carries an explicit read error.
                    pass
        if run_id == "compiler-quality-v1":
            source = load_json(evidence_root / "compiler-quality-v1.json")
            run["quality"] = {
                "label_origin": source.get("label_origin"),
                "compiler_contract_pass": source.get("compiler_contract_pass"),
            }
        run["redactions"].sort(key=lambda item: (item["path"], item["reason"]))
        run["artifacts"].sort(key=lambda item: item["path"])
        write_run(run, output_root / "runs" / f"{run_id}.json")
        converted.append(run_id)
    index = {
        "schema": "graph-advantage-evidence-index-v1",
        "runs": [
            {
                "run_id": run_id,
                "path": f"runs/{run_id}.json",
                "sha256": sha256_file(output_root / "runs" / f"{run_id}.json"),
            }
            for run_id in converted
        ],
    }
    (output_root / "index.json").write_bytes(canonical_bytes(index))
    return converted


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--output-root", type=Path, required=True)
    args = parser.parse_args()
    converted = convert(args.evidence_root.resolve(), args.output_root.resolve())
    print(json.dumps({"schema_version": SCHEMA_VERSION, "runs": len(converted)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
