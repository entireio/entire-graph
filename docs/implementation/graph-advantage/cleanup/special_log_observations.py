#!/usr/bin/env python3
"""Extract complete structured facts from five reviewed legacy text formats."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any

SCHEMA = "graph-advantage-special-log-observations-v1"
BENCHMARK = re.compile(r"^(Benchmark\S+)\s+(\d+)\s+(.+)$")
PPROF_ROW = re.compile(r"^\s*(\S+)\s+(\S+)%\s+(\S+)%\s+(\S+)\s+(\S+)%\s+(.+)$")
TEST_SUMMARY = re.compile(r"^Ran\s+(\d+)\s+tests?\s+in\s+([0-9.]+)s$")
PACKAGE_RESULT = re.compile(r"^(ok|FAIL)\s+(\S+)(?:\s+([0-9.]+)s)?$")
GO_TEST_START = re.compile(r"^\s*===\s+RUN\s+(.+)$")
GO_TEST_PROGRESS = re.compile(r"^\s*===\s+(?:PAUSE|CONT|NAME)\s+(.+)$")
GO_TEST_OUTCOME = re.compile(r"^\s*---\s+(PASS|FAIL|SKIP):\s+([^\s(]+)", re.I)
GO_TEST_DETAIL = re.compile(r"^\s+[^:\s]+_test\.go:\d+:\s*(.+)$")
PY_TEST_OUTCOME = re.compile(r"^\s*(test\S*)\s+\(([^)]+)\)\s+\.\.\.\s+(ok|FAIL|ERROR|skipped)(?:\s+(.+))?$", re.I)
KEY_VALUE = re.compile(r"^([A-Za-z][A-Za-z0-9_.-]*)=(.*)$")
CHECKSUM = re.compile(r"^([0-9a-fA-F]{64})\s+[* ]?(.+)$")
LOCAL = re.compile(r"(?<!\w)(?:/Users|/home|/private/tmp|/tmp|/opt|/var)/[^\s,;]+|[A-Za-z]:[\\/][^\s,;]+")
SECRET = re.compile(r"(?i)(sig|se|sv|token|password|secret)=([^&\s]+)")
INFRA = re.compile(r"\b(?:graph-validation-linux|graph-p1-worker-\d+|worker-[a-z])\b")


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode()


def clean(value: str) -> str:
    value = re.sub(r"https?://\S+", "<url>", value)
    value = LOCAL.sub("<local-path>", value)
    value = SECRET.sub(r"\1=<redacted>", value)
    value = INFRA.sub("<infrastructure>", value)
    return re.sub(r"\s+", " ", value).strip()


def sanitize_json(value: Any, key: str = "") -> Any:
    if isinstance(value, dict):
        return {name: sanitize_json(item, name) for name, item in value.items()}
    if isinstance(value, list):
        return [sanitize_json(item, key) for item in value]
    if isinstance(value, str):
        return clean(value)
    return value


def scalar(value: str) -> Any:
    stripped = value.strip()
    if stripped.lower() in {"true", "false"}:
        return stripped.lower() == "true"
    if re.fullmatch(r"-?\d+", stripped):
        return int(stripped)
    return clean(stripped)


def parse_measurements(text: str) -> list[dict[str, Any]] | None:
    tokens = text.split()
    if len(tokens) % 2:
        return None
    result = []
    for index in range(0, len(tokens), 2):
        try:
            value = float(tokens[index])
        except ValueError:
            return None
        result.append({"value": value, "unit": tokens[index + 1]})
    return result


def extract(path: Path, raw: bytes | None = None) -> dict[str, Any]:
    raw = path.read_bytes() if raw is None else raw
    lines = raw.decode("utf-8", "replace").splitlines()
    consumed: set[int] = set()
    facts: dict[str, Any] = {}

    benchmarks = []
    benchmark_environment: dict[str, str] = {}
    for index, line in enumerate(lines):
        if ": " in line and line.split(":", 1)[0] in {"goos", "goarch", "pkg", "cpu"}:
            key, value = line.split(":", 1)
            benchmark_environment[key] = clean(value)
            consumed.add(index)
        match = BENCHMARK.match(line)
        if match:
            measurements = parse_measurements(match.group(3))
            if measurements is not None:
                benchmarks.append({"name": match.group(1), "iterations": int(match.group(2)), "measurements": measurements})
                consumed.add(index)
    if benchmarks:
        facts["benchmark"] = {"environment": benchmark_environment, "rows": benchmarks}

    pprof: dict[str, Any] = {}
    pprof_rows = []
    in_table = False
    for index, line in enumerate(lines):
        if line.startswith(("File: ", "Type: ", "Time: ")):
            key, value = line.split(":", 1)
            pprof[key.lower()] = clean(value)
            consumed.add(index)
        elif line.startswith("Duration: "):
            match = re.match(r"Duration:\s*([^,]+),\s*Total samples\s*=\s*([^ ]+)(?:\s+\(([^)]+)\))?", line)
            if match:
                pprof.update({"duration": match.group(1), "total_samples": match.group(2), "sampling_percent": match.group(3)})
                consumed.add(index)
        elif line.startswith(("Showing nodes accounting for ", "Dropped ", "Showing top ")):
            pprof.setdefault("notes", []).append(clean(line))
            consumed.add(index)
        elif re.match(r"^\s*flat\s+flat%\s+sum%\s+cum\s+cum%", line):
            in_table = True
            consumed.add(index)
        elif in_table:
            match = PPROF_ROW.match(line)
            if match:
                pprof_rows.append({"flat": match.group(1), "flat_percent": match.group(2) + "%", "sum_percent": match.group(3) + "%", "cumulative": match.group(4), "cumulative_percent": match.group(5) + "%", "function": clean(match.group(6))})
                consumed.add(index)
    if pprof_rows:
        pprof["rows"] = pprof_rows
        facts["pprof_text"] = pprof

    build_info: dict[str, Any] = {}
    modules = []
    settings = []
    for index, line in enumerate(lines):
        if index == 0 and ": go" in line:
            binary, go = line.split(":", 1)
            build_info.update({"binary": clean(binary), "go_version": clean(go)})
            consumed.add(index)
        elif line.startswith("\tpath\t"):
            build_info["package_path"] = clean(line.split("\t", 2)[2]); consumed.add(index)
        elif line.startswith("\tmod\t") or line.startswith("\tdep\t"):
            parts = line.strip().split("\t")
            modules.append({"kind": parts[0], "path": parts[1] if len(parts) > 1 else None, "version": parts[2] if len(parts) > 2 else None, "sum": parts[3] if len(parts) > 3 else None})
            consumed.add(index)
        elif line.startswith("\tbuild\t"):
            setting = line.strip().split("\t", 1)[1]
            key, _, value = setting.partition("=")
            settings.append({"key": key, "value": clean(value)})
            consumed.add(index)
    if build_info:
        build_info.update({"modules": modules, "settings": settings})
        facts["go_build_info"] = build_info

    if "process-memory-observation" in path.name:
        observations = []
        for index, line in enumerate(lines):
            stripped = line.strip()
            if not stripped:
                consumed.add(index)
            elif stripped.endswith(":"):
                facts["process_memory_heading"] = clean(stripped[:-1]); consumed.add(index)
            elif stripped.startswith("-"):
                observations.append(clean(stripped[1:])); consumed.add(index)
        if observations:
            facts["process_memory_observations"] = observations

    events = []
    suite: dict[str, Any] = {}
    named_outcomes = []
    pending_details = []
    for index, line in enumerate(lines):
        stripped = line.strip()
        match = GO_TEST_START.match(line)
        if match:
            pending_details = []
            consumed.add(index)
            continue
        if GO_TEST_PROGRESS.match(line):
            consumed.add(index)
            continue
        match = GO_TEST_DETAIL.match(line)
        if match:
            pending_details.append(clean(match.group(1)))
            consumed.add(index)
            continue
        match = GO_TEST_OUTCOME.match(line)
        if match:
            item = {"outcome": match.group(1).lower(), "name": clean(match.group(2))}
            if item["outcome"] in {"fail", "skip"} and pending_details:
                item["details"] = pending_details[:]
            named_outcomes.append(item)
            pending_details = []
            consumed.add(index)
            continue
        match = PY_TEST_OUTCOME.match(line)
        if match:
            marker = match.group(3).lower()
            item = {"outcome": "pass" if marker == "ok" else "skip" if marker == "skipped" else "fail", "name": clean(match.group(1)), "container": clean(match.group(2)), "observed_marker": marker}
            if match.group(4):
                item["details"] = [clean(match.group(4))]
            named_outcomes.append(item)
            consumed.add(index)
            continue
        if stripped.startswith("{"):
            try:
                event = json.loads(stripped)
            except json.JSONDecodeError:
                pass
            else:
                events.append(sanitize_json(event)); consumed.add(index); continue
        match = TEST_SUMMARY.match(stripped)
        if match:
            suite.update({"tests": int(match.group(1)), "elapsed_seconds": float(match.group(2))}); consumed.add(index)
        elif stripped in {"OK", "FAILED", "PASS", "FAIL"}:
            suite["observed_terminal_marker"] = stripped.lower(); consumed.add(index)
        else:
            match = PACKAGE_RESULT.match(stripped)
            if match:
                suite.setdefault("packages", []).append({"marker": match.group(1).lower(), "package": clean(match.group(2)), "elapsed_seconds": float(match.group(3)) if match.group(3) else None})
                consumed.add(index)
            elif stripped and set(stripped) <= {".", "E", "F", "s", "x"}:
                suite["progress_characters"] = len(stripped); consumed.add(index)
            elif stripped and set(stripped) == {"-"}:
                consumed.add(index)
            elif re.match(r"^\?\s+\S+\s+\[no test files\]$", stripped):
                suite.setdefault("packages", []).append({"marker": "skip", "package": clean(stripped.split()[1]), "reason": "no test files"})
                consumed.add(index)
            elif not stripped:
                consumed.add(index)
    if events or suite or named_outcomes:
        facts["test_or_status_output"] = {"events": events, "suite": suite, "named_outcomes": named_outcomes}

    remaining = [(index, line.strip()) for index, line in enumerate(lines) if line.strip() and index not in consumed]
    if remaining and all(KEY_VALUE.match(line) for _, line in remaining):
        facts["key_value_markers"] = [{"key": KEY_VALUE.match(line).group(1), "value": scalar(KEY_VALUE.match(line).group(2))} for _, line in remaining]
        consumed.update(index for index, _ in remaining)
    elif remaining and all(CHECKSUM.match(line) for _, line in remaining):
        facts["checksum_manifest"] = [{"sha256": CHECKSUM.match(line).group(1).lower(), "logical_name": clean(CHECKSUM.match(line).group(2))} for _, line in remaining]
        consumed.update(index for index, _ in remaining)
    elif len(remaining) == 1:
        index, value = remaining[0]
        logical_name = path.name.lower()
        if re.fullmatch(r"-?\d+|true|false|[0-9a-fA-F]{40}|[0-9a-fA-F]{64}|\d{4}-\d\d-\d\dT\S+", value, re.I):
            facts["single_marker"] = {"logical_name": logical_name, "value": scalar(value)}
            consumed.add(index)

    unparsed = [index + 1 for index, line in enumerate(lines) if line.strip() and index not in consumed]
    formats = sorted(facts)
    return {
        "source_sha256": hashlib.sha256(raw).hexdigest(),
        "bytes": len(raw),
        "lines": len(lines),
        "formats": formats,
        "facts": facts,
        "unparsed_nonempty_line_count": len(unparsed),
        "complete_for_removal": bool(formats) and not unparsed,
    }


def git_blob(repo: Path, relative: str, revision: str) -> str | None:
    try:
        return subprocess.check_output(["git", "rev-parse", f"{revision}:{relative}"], cwd=repo, text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        return None


def build(repo: Path, log_index: Path) -> dict[str, Any]:
    index = json.loads(log_index.read_text())
    revision = index["precleanup_head"]
    contents: dict[str, dict[str, Any]] = {}
    aliases = []
    for item in index["files"]:
        path = repo / item["path"]
        try:
            raw = subprocess.check_output(["git", "show", f"{revision}:{item['path']}"], cwd=repo, stderr=subprocess.DEVNULL)
        except subprocess.CalledProcessError:
            if not path.is_file():
                continue
            raw = path.read_bytes()
        if not raw.strip():
            continue
        record = extract(path, raw)
        if not record["formats"]:
            continue
        content_id = record["source_sha256"]
        contents.setdefault(content_id, record)
        aliases.append({"path": item["path"], "git_blob": git_blob(repo, item["path"], revision), "content_id": content_id, "formats": record["formats"], "complete_for_removal": record["complete_for_removal"]})
    return {
        "schema": SCHEMA,
        "precleanup_head": revision,
        "source_log_index_sha256": hashlib.sha256(log_index.read_bytes()).hexdigest(),
        "content_count": len(contents),
        "alias_count": len(aliases),
        "complete_alias_count": sum(item["complete_for_removal"] for item in aliases),
        "content_records": [contents[key] for key in sorted(contents)],
        "aliases": sorted(aliases, key=lambda item: item["path"]),
        "limitations": ["Observed markers are preserved as facts; no run-level pass or failure is inferred."],
    }


def build_retention_map(log_index: Path, special: dict[str, Any], special_path: str) -> dict[str, Any]:
    index = json.loads(log_index.read_text())
    general = {record["sha256"]: record for record in index["content_records"]}
    special_aliases = {item["path"]: item for item in special["aliases"]}
    special_contents = {record["source_sha256"]: record for record in special["content_records"]}
    special_sha = hashlib.sha256(canonical_bytes(special)).hexdigest()
    log_sha = hashlib.sha256(log_index.read_bytes()).hexdigest()
    log_path = "docs/implementation/graph-advantage/cleanup/log-observations.json"
    files = []
    for item in index["files"]:
        facts = general[item["content_id"]]
        decision: dict[str, Any]
        alias = special_aliases.get(item["path"])
        if item["bytes"] == 0:
            decision = {"disposition": "remove_empty", "reason": "zero-byte legacy file has no observation"}
        elif alias and alias["complete_for_removal"]:
            decision = {
                "disposition": "remove_after_structured_extraction",
                "reason": "every nonempty line belongs to a reviewed special format",
                "replacements": [{"path": special_path, "sha256": special_sha, "content_id": item["content_id"]}],
            }
        elif alias:
            special_record = special_contents[item["content_id"]]
            if facts["scalar_observations_truncated"]:
                decision = {"disposition": "retain_truncated_observations", "reason": "the bounded general observation record omitted scalar observations", "unparsed_nonempty_line_count": special_record["unparsed_nonempty_line_count"]}
            else:
                decision = {
                    "disposition": "retain_unclassified_nonempty",
                    "reason": "recognized facts coexist with unparsed nonempty lines; no format-specific removal approval applies",
                    "unparsed_nonempty_line_count": special_record["unparsed_nonempty_line_count"],
                }
        elif facts["named_tests"] or facts["scalar_observations"] or facts["source_hashes"] or facts["source_commits"]:
            fact_counts = {"named_tests": len(facts["named_tests"]), "scalar_observations": facts["scalar_observation_count"], "source_hashes": len(facts["source_hashes"]), "source_commits": len(facts["source_commits"])}
            if facts["scalar_observations_truncated"]:
                decision = {"disposition": "retain_truncated_observations", "reason": "the bounded observation record omitted scalar observations", "fact_counts": fact_counts}
            else:
                decision = {
                    "disposition": "retain_unclassified_nonempty",
                    "reason": "general observations alone do not establish that every meaningful source fact has a structured replacement",
                    "fact_counts": fact_counts,
                }
        else:
            decision = {
                "disposition": "retain_unclassified_nonempty",
                "reason": "nonempty content has no reviewed lossless structured replacement",
            }
        files.append({
            "path": item["path"],
            "source_sha256": item["content_id"],
            "bytes": item["bytes"],
            "git_blob": item["git_blob"],
            **decision,
        })
    counts: dict[str, int] = {}
    for item in files:
        counts[item["disposition"]] = counts.get(item["disposition"], 0) + 1
    return {
        "schema": "graph-advantage-text-retention-map-v1",
        "precleanup_head": index["precleanup_head"],
        "source_log_index_sha256": special["source_log_index_sha256"],
        "file_count": len(files),
        "disposition_counts": counts,
        "files": files,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--log-index", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--retention-map", type=Path)
    args = parser.parse_args()
    result = build(args.repo.resolve(), args.log_index.resolve())
    args.output.write_bytes(canonical_bytes(result))
    if args.retention_map:
        public_path = "docs/implementation/graph-advantage/cleanup/special-log-observations.json"
        args.retention_map.write_bytes(canonical_bytes(build_retention_map(args.log_index.resolve(), result, public_path)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
