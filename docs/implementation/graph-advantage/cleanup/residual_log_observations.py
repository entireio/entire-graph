#!/usr/bin/env python3
"""Retain compact causal facts from reviewed residual diagnostic text logs."""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any

import metadata_log_observations as metadata

SCHEMA = "graph-advantage-residual-log-observations-v1"
FULL_CHECK_RUNS = {
    "check-25887f69-linux-full/raw/mise-check.raw.log": "check-25887f69-linux-full.json",
    "check-6f23da0a-cpu4/mise-check.raw.log": "check-6f23da0a-cpu4.json",
    "check-6f23da0a-linux-full/attempt-r1/raw/mise-check.raw.log": "check-6f23da0a-linux-full.json",
    "check-950567a3-linux-full-01/raw/mise-check.raw.log": "check-950567a3-linux-full-01.json",
    "check-effa358f-linux-full-01/raw/mise-check.raw.log": "check-effa358f-linux-full-01.json",
}


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode()


def original(repo: Path, revision: str, path: str) -> bytes:
    return subprocess.check_output(["git", "show", f"{revision}:{path}"], cwd=repo, stderr=subprocess.DEVNULL)


def goroutines(raw: bytes) -> dict[str, Any]:
    lines = raw.decode("utf-8", "replace").splitlines()
    states: collections.Counter[str] = collections.Counter()
    functions: collections.Counter[str] = collections.Counter()
    top_frames = []
    current_state = None
    awaiting_top = False
    for index, line in enumerate(lines):
        match = re.match(r"^goroutine \d+ \[([^]]+)\]:$", line)
        if match:
            current_state = match.group(1)
            states[current_state] += 1
            awaiting_top = True
            continue
        if not line or line[0].isspace() or line.startswith(("created by ", "panic:", "running tests:", "=== ", "--- ", "FAIL", "ok\t", "?\t", "Finished ", "[")):
            continue
        if index + 1 < len(lines) and lines[index + 1][:1].isspace() and ("(" in line or line.endswith("...")):
            name = re.sub(r"\([^)]*\)$", "", line).rstrip(".")
            functions[name] += 1
            if awaiting_top:
                top_frames.append({"state": current_state, "function": name})
                awaiting_top = False
    return {
        "goroutine_state_counts": dict(sorted(states.items())),
        "top_frames": top_frames,
        "function_occurrences": [{"function": name, "count": count} for name, count in sorted(functions.items())],
    }


def process_tree(raw: bytes) -> dict[str, Any]:
    elapsed = []
    commands: collections.Counter[str] = collections.Counter()
    snapshot_counts = []
    current = 0
    numeric_candidate_rows = 0
    parsed_rows = 0
    for line in raw.decode("utf-8", "replace").splitlines():
        match = re.match(r"^=== elapsed=([0-9.]+)s", line)
        if match:
            if elapsed:
                snapshot_counts.append(current)
            elapsed.append(float(match.group(1)))
            current = 0
            continue
        if re.match(r"^\d+\s+\d+\s+\d+\s+", line):
            numeric_candidate_rows += 1
        match = re.match(r"^\d+\s+\d+\s+\d+\s+(?:\S+\s+)?(?:\d+-)?(?:\d{2}:)?\d{2}:\d{2}\s+(.+)$", line)
        if match:
            parsed_rows += 1
            current += 1
            command = match.group(1).split()[0].strip("()")
            commands[Path(command).name] += 1
    if elapsed or parsed_rows:
        snapshot_counts.append(current)
    return {
        "snapshot_count": len(elapsed),
        "first_elapsed_seconds": elapsed[0] if elapsed else None,
        "last_elapsed_seconds": elapsed[-1] if elapsed else None,
        "max_processes_observed": max(snapshot_counts) if parsed_rows else None,
        "max_processes_unavailable_reason": None if parsed_rows else "no recognized process rows",
        "numeric_candidate_rows": numeric_candidate_rows,
        "parsed_process_rows": parsed_rows,
        "unparsed_numeric_rows": numeric_candidate_rows - parsed_rows,
        "executable_observation_counts": dict(sorted(commands.items())),
    }


def apple_sample(raw: bytes) -> dict[str, Any]:
    text = raw.decode("utf-8", "replace")
    fields: dict[str, str] = {}
    for label in ("Identifier", "Code Type", "Platform", "Target Type", "OS Version", "Report Version"):
        match = re.search(rf"^{re.escape(label)}:\s*(.+)$", text, re.M)
        if match:
            fields[label.lower().replace(" ", "_")] = metadata.clean(match.group(1))
    memory = {}
    for label, key in (("Physical footprint", "observed"), ("Physical footprint (peak)", "peak")):
        match = re.search(rf"^{re.escape(label)}:\s*([0-9.]+)([KMG])$", text, re.M)
        if match:
            memory[key] = {"value": float(match.group(1)), "unit": match.group(2) + "iB"}
    thread_headers = re.findall(r"^\s*\d+\s+Thread_\d+", text, re.M)
    symbols: collections.Counter[str] = collections.Counter()
    unknown_weight = 0
    for line in text.splitlines():
        match = re.match(r"^\s*[+!:| ]*\s*(\d+)\s+(.+?)(?:\s+\(in\s+|\s+\[0x)", line)
        if not match:
            continue
        weight = int(match.group(1))
        symbol = match.group(2).strip()
        if symbol == "???":
            unknown_weight += weight
        else:
            symbols[symbol] += weight
    return {
        "observed_fields": fields,
        "memory_footprint": memory,
        "thread_header_count": len(thread_headers),
        "unknown_symbol_sample_weight": unknown_weight,
        "known_symbol_sample_weights": [{"symbol": name, "weight": weight} for name, weight in sorted(symbols.items())],
        "limitations": ["Sampling weights are observations from one diagnostic sample, not benchmark timings or end-to-end attribution."],
    }


def build(repo: Path, text_map_path: Path, general_path: Path, metadata_path: Path) -> dict[str, Any]:
    text_map = json.loads(text_map_path.read_text())
    general = json.loads(general_path.read_text())
    meta = json.loads(metadata_path.read_text())
    general_ids = {item["sha256"] for item in general["content_records"]}
    meta_by_path = {item["path"]: item["source_sha256"] for item in meta["aliases"]}
    records = []
    targets = []
    for item in text_map["files"]:
        if item["disposition"] != "retain_unclassified_nonempty":
            continue
        path = item["path"]
        short = path.split("docs/implementation/graph-advantage/evidence/", 1)[-1]
        name = Path(path).name
        kind = None
        if short in FULL_CHECK_RUNS:
            kind = "full-check-log"
        elif path.endswith("check-6f23da0a/mise-check.raw.log") or name == "goroutines.txt":
            kind = "goroutine-timeout"
        elif name in {"process-tree.log", "process-trees.txt"}:
            kind = "process-tree-samples"
        elif name == "live-sample-pid43766.txt":
            kind = "apple-process-sample"
        elif name == "review-linux-correctness-initial.txt":
            kind = "git-usage-test-failure"
        elif name == "review-mise-check.txt":
            kind = "review-check-log"
        elif name == "setup-failure.log":
            kind = "setup-failure"
        if kind is None:
            continue
        raw = original(repo, text_map["precleanup_head"], path)
        record: dict[str, Any] = {"path": path, "source_sha256": item["source_sha256"], "bytes": item["bytes"], "kind": kind, "general_observations_content_id": item["source_sha256"], "eligible_for_removal": True}
        if item["source_sha256"] not in general_ids:
            raise ValueError(f"missing general facts for {path}")
        if path in meta_by_path:
            record["metadata_observations_content_id"] = meta_by_path[path]
        if kind == "full-check-log":
            run_name = FULL_CHECK_RUNS[short]
            run_path = repo / "docs/implementation/graph-advantage/evidence/canonical-v1/runs" / run_name
            if not run_path.is_file():
                raise ValueError(f"missing canonical full-check run {run_name}")
            record["canonical_run"] = {"path": run_path.relative_to(repo).as_posix(), "sha256": hashlib.sha256(run_path.read_bytes()).hexdigest()}
            record["discarded_detail"] = "duplicated raw progress, package inventory, and human report formatting"
        elif kind == "goroutine-timeout":
            record["stack_summary"] = goroutines(raw)
            record["discarded_detail"] = "addresses, source-machine paths, and repeated full stack layout"
        elif kind == "process-tree-samples":
            record["process_summary"] = process_tree(raw)
            record["discarded_detail"] = "process ids, local paths, arguments, and repeated snapshots"
            if record["process_summary"]["parsed_process_rows"] == 0:
                record["eligible_for_removal"] = False
                record["retention_reason"] = "source format was not recognized as process-table rows"
        elif kind == "apple-process-sample":
            record["sample_summary"] = apple_sample(raw)
            record["discarded_detail"] = "addresses, local paths, and repeated call-tree indentation"
        elif kind == "git-usage-test-failure":
            record["failure"] = {"test": "TestCompilerIndexEvidenceDoesNotEnterStaticCache", "observed_subcommand": "git cat-file", "discarded_detail": "generic git usage help"}
        elif kind == "review-check-log":
            record["review_context"] = {"command": "mise run check", "comparative_evaluation": "disabled"}
        elif kind == "setup-failure":
            record["setup_failure_lines"] = [metadata.clean(line) for line in raw.decode("utf-8", "replace").splitlines() if line.strip()]
        records.append(record)
        targets.append({"path": path, "source_sha256": item["source_sha256"], "bytes": item["bytes"]})
    result = {
        "schema": SCHEMA,
        "precleanup_head": text_map["precleanup_head"],
        "source_text_retention_map_sha256": hashlib.sha256(text_map_path.read_bytes()).hexdigest(),
        "source_general_observations_sha256": hashlib.sha256(general_path.read_bytes()).hexdigest(),
        "source_metadata_observations_sha256": hashlib.sha256(metadata_path.read_bytes()).hexdigest(),
        "record_count": len(records),
        "removal_eligible_count": sum(item["eligible_for_removal"] for item in records),
        "records": sorted(records, key=lambda item: item["path"]),
        "limitations": ["Summaries preserve observed state and causal diagnostics; they do not infer run-level outcomes beyond linked canonical run records."],
    }
    return result


def removal_map(summary: dict[str, Any], path: str) -> dict[str, Any]:
    digest = hashlib.sha256(canonical_bytes(summary)).hexdigest()
    eligible = [item for item in summary["records"] if item["eligible_for_removal"]]
    return {"schema": "graph-advantage-residual-log-retention-map-v1", "source_summary_sha256": digest, "file_count": len(eligible), "files": [{"path": item["path"], "source_sha256": item["source_sha256"], "bytes": item["bytes"], "disposition": "remove_after_residual_summary", "replacement": {"path": path, "sha256": digest, "content_id": item["source_sha256"]}} for item in eligible]}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--text-map", type=Path, required=True)
    parser.add_argument("--general", type=Path, required=True)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--retention-map", type=Path, required=True)
    args = parser.parse_args()
    result = build(args.repo.resolve(), args.text_map.resolve(), args.general.resolve(), args.metadata.resolve())
    args.output.write_bytes(canonical_bytes(result))
    public = "docs/implementation/graph-advantage/cleanup/residual-log-observations.json"
    args.retention_map.write_bytes(canonical_bytes(removal_map(result, public)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
