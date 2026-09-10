#!/usr/bin/env python3
"""Normalize reviewed metadata and mixed check logs without retaining raw progress."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any

import log_observations as general
import special_log_observations as special

SCHEMA = "graph-advantage-metadata-log-observations-v1"
LOCAL = re.compile(r"(?<!\w)(?:/Users|/home|/private/tmp|/tmp|/opt|/var)/[^\s,;:]+|[A-Za-z]:[\\/][^\s,;:]+")
SECRET = re.compile(r"(?i)(sig|se|sv|token|password|secret)=([^&\s]+)")
INFRA = re.compile(r"\b(?:graph-validation-linux|graph-p1-worker-\d+|worker-[a-z])\b")
TASK = re.compile(r"^\[([^]]+)\]\s+\$\s+(.+)$")
TASK_FINISHED = re.compile(r"^\[([^]]+)\]\s+Finished in ([0-9.]+)(ms|s)$")
TASK_OUTPUT = re.compile(r"^\[([^]]+)\]\s+(.+)$")
FINISHED = re.compile(r"^Finished in ([0-9.]+)s$")
DOWNLOAD = re.compile(r"^go: downloading (\S+) (\S+)$")
SLOW_TEST = re.compile(r"^(Test\S+) \(([0-9.]+)s\)$")
CHECK_OK = re.compile(r"^(.+): OK$")
CHECK_DIFF = re.compile(r"^(.+) (.+) differ(?:: (.+))?$")
GNU_TIME = re.compile(r"^\s*([^:]+):\s*(.*)$")
TOOL_IS = re.compile(r"^(\S+) is (.+)$")
PACKAGE = re.compile(r"^(?:github\.com|golang\.org|gopkg\.in|cloud\.google\.com|google\.golang\.org)/\S+$")
VERSION = re.compile(r"^(?:go version |git version |go\d|golang\.org/x/tools/gopls |gopls |mise |bubblewrap |Linux |macOS-|go tool pprof )")


def canonical_bytes(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False) + "\n").encode()


def clean(value: str) -> str:
    value = re.sub(r"https?://\S+", "<url>", value)
    value = LOCAL.sub("<local-path>", value)
    value = SECRET.sub(r"\1=<redacted>", value)
    value = INFRA.sub("<infrastructure>", value)
    return re.sub(r"\s+", " ", value).strip()


def original_bytes(repo: Path, revision: str, relative: str) -> bytes:
    return subprocess.check_output(["git", "show", f"{revision}:{relative}"], cwd=repo, stderr=subprocess.DEVNULL)


def family(path: str) -> str | None:
    name = Path(path).name.lower()
    if name.startswith("time-") or name == "time.txt":
        return "gnu-time"
    if name in {"environment.txt", "baseline-environment.txt", "environment-full-r2.txt", "safe-environment.txt", "input-identity.txt", "source-identity.txt", "source-before.txt", "before-identity.txt", "after-identity.txt", "mise-trust.txt", "mise-version.txt", "toolchain.txt", "tool-version.txt", "go-version.txt", "uname.txt"}:
        return "environment-identity"
    if name in {"setup-note.txt", "collector-dry-preflight.txt", "phase-breadcrumbs-proof.txt", "session-events.txt", "session-terminal.txt", "process-observation.txt", "relation-profile-development-failure.txt", "scoring-reproduction.txt"}:
        return "bounded-note"
    if name == "development-failures.txt":
        return "bounded-note"
    if name in {"mise-check.raw.log", "mise-check.txt", "check.log", "check.txt"} or name.startswith("check-") or name.startswith("review-") or name.startswith("linux-") or name == "fixes-mise-check.txt":
        return "mixed-check"
    if name in {"commands.txt", "test-command.txt", "mise-tasks-preflight.txt"}:
        return "command-record"
    return None


def parse(path: str, raw: bytes, general_facts: dict[str, Any]) -> dict[str, Any]:
    kind = family(path)
    facts: dict[str, list[Any]] = {}
    unknown = []
    noise_count = 0

    def add(key: str, value: Any) -> None:
        facts.setdefault(key, []).append(value)

    stripped_document = raw.decode("utf-8", "replace").strip()
    if stripped_document.startswith(("{", "[")):
        try:
            facts["structured_document"] = [special.sanitize_json(json.loads(stripped_document))]
        except json.JSONDecodeError:
            pass
        else:
            return {
                "source_sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw), "family": kind,
                "facts": facts, "general_observations_content_id": general_facts["sha256"],
                "discarded_progress_line_count": 0, "unclassified_line_count": 0,
                "unclassified_examples": [], "complete_for_semantic_removal": kind is not None,
            }

    for number, raw_line in enumerate(raw.decode("utf-8", "replace").splitlines(), 1):
        line = raw_line.strip()
        if not line:
            continue
        match = TASK.match(line)
        if match:
            add("task_commands", {"task": match.group(1), "command": clean(match.group(2))}); continue
        match = TASK_FINISHED.match(line)
        if match:
            add("task_timings", {"task": match.group(1), "value": float(match.group(2)), "unit": match.group(3)}); continue
        match = TASK_OUTPUT.match(line)
        if match:
            add("task_observations", {"task": match.group(1), "value": clean(match.group(2))}); continue
        if re.match(r"^\[[^]]+\]$", line):
            add("task_stages", line[1:-1]); continue
        match = FINISHED.match(line)
        if match:
            add("elapsed_seconds", float(match.group(1))); continue
        match = DOWNLOAD.match(line)
        if match:
            add("module_downloads", {"module": match.group(1), "version": match.group(2)}); continue
        match = SLOW_TEST.match(line)
        if match:
            add("slow_tests", {"name": match.group(1), "seconds": float(match.group(2))}); continue
        match = CHECK_OK.match(line)
        if match:
            add("identity_checks", {"logical_name": Path(match.group(1)).name, "status": "ok"}); continue
        match = CHECK_DIFF.match(line)
        if match:
            add("identity_checks", {"left": Path(match.group(1)).name, "right": Path(match.group(2)).name, "status": "different", "detail": clean(match.group(3) or "")}); continue
        if line.startswith("tar: Ignoring unknown extended header keyword"):
            add("archive_warnings", "unknown extended metadata ignored"); continue
        if line.startswith("mise trusted "):
            add("mise_trust", {"status": "trusted", "path": "<local-path>"}); continue
        if line.endswith(": trusted"):
            add("mise_trust", {"status": "trusted", "path": "<local-path>"}); continue
        if line.startswith("mise WARN"):
            add("tool_warnings", clean(line)); continue
        if line.startswith("Trust them with `mise trust`"):
            add("tool_warnings", "mise configuration was untrusted"); continue
        match = TOOL_IS.match(line)
        if match:
            add("tool_locations", {"tool": match.group(1), "executable": Path(match.group(2)).name}); continue
        if VERSION.match(line):
            add("tool_versions", clean(line)); continue
        if kind == "environment-identity" and re.match(r"^\d{4}\.\d+\.\d+\s+\S+", line):
            add("tool_versions", clean(line)); continue
        if kind == "environment-identity" and line in {"go_version:", "pprof_help_version_header:", "usage:"}:
            add("tool_version_labels", line[:-1]); continue
        if kind == "environment-identity" and re.match(r"^\d{3}\s+\d+\s+\S+", line):
            mode, size, logical = line.split(None, 2)
            add("artifact_modes", {"mode": mode, "bytes": int(size), "logical_name": Path(logical).name}); continue
        if kind == "environment-identity" and "No such file or directory" in line:
            add("identity_failures", clean(line)); continue
        if kind == "environment-identity" and (line.startswith("time (GNU Time)") or line.startswith(("Copyright ", "License ", "Written by "))):
            if line.startswith("time (GNU Time)"):
                add("tool_versions", clean(line))
            else:
                noise_count += 1
            continue
        if PACKAGE.match(line):
            add("packages", line); continue
        if kind == "mixed-check" and re.match(r"^(?:ok|not ok)\s+", line):
            add("statusline_assertions", clean(line)); continue
        if kind == "mixed-check" and line.startswith("Measuring ("):
            add("report_actions", clean(line)); continue
        if kind == "mixed-check" and line.startswith("Wrote report to "):
            add("report_outputs", "report written"); continue
        if kind == "mixed-check" and re.match(r"^(?:LANGUAGE\s+REPOS|[A-Za-z+.#-]+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\S+\s+\d+|TOTAL\s+\d+)", line):
            add("report_rows", clean(line)); continue
        if kind == "gnu-time":
            match = GNU_TIME.match(raw_line)
            if match:
                add("gnu_time", {"field": clean(match.group(1)), "value": clean(match.group(2))}); continue
        if re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", line):
            key, value = line.split("=", 1)
            add("environment", {"name": key, "value": clean(value)}); continue
        if re.match(r"^[0-9a-fA-F]{64}\s+", line):
            digest, logical = line.split(None, 1)
            add("checksums", {"sha256": digest.lower(), "logical_name": Path(logical).name}); continue
        if line.startswith("{"):
            try:
                add("json_events", special.sanitize_json(json.loads(line))); continue
            except json.JSONDecodeError:
                pass
        if kind == "command-record":
            if line.startswith("#"):
                add("command_context", clean(line[1:])); continue
            if re.match(r"^(?:go|git|python3|mise|env|sh|bash|rg|sha256sum|test|cat|cp|tar)\b", line):
                add("commands", clean(line)); continue
            if re.match(r"^[a-z][a-z0-9:_-]*\s+\S", line):
                name, description = line.split(None, 1)
                add("task_inventory", {"task": name, "description": clean(description)}); continue
        if kind == "bounded-note":
            add("bounded_observations", clean(line)); continue
        if general.TEST.search(line) or general.PLAIN_SKIP.search(line) or general.PY_TEST_OUTCOME.search(line) or general.GO_TEST_START.search(line) or re.match(r"^===\s+(?:PAUSE|CONT|NAME)\b", line):
            noise_count += 1; continue
        if special.TEST_SUMMARY.match(line) or special.PACKAGE_RESULT.match(line) or line in {"OK", "FAILED", "PASS", "FAIL", "running tests:"} or re.match(r"^[.EFsx-]+$", line) or re.match(r"^\?\s+\S+\s+\[no test files\]$", line):
            noise_count += 1; continue
        if general.GO_TEST_DETAIL.match(raw_line) or any(pattern.search(line) for _, pattern in general.SCALARS) or general.UNCLASSIFIED.search(line):
            noise_count += 1; continue
        if any(word in line.lower() for word in ("source", "sha", "hash", "manifest", "binary", "commit")) and (general.HASH.search(line) or general.COMMIT.search(line)):
            noise_count += 1; continue
        unknown.append({"line": number, "value": clean(line)[:160]})
    return {
        "source_sha256": hashlib.sha256(raw).hexdigest(),
        "bytes": len(raw),
        "family": kind,
        "facts": facts,
        "general_observations_content_id": general_facts["sha256"],
        "discarded_progress_line_count": noise_count,
        "unclassified_line_count": len(unknown),
        "unclassified_examples": unknown[:5],
        "complete_for_semantic_removal": bool(kind and facts) and not unknown,
    }


def build(repo: Path, text_map_path: Path, log_index_path: Path) -> dict[str, Any]:
    text_map = json.loads(text_map_path.read_text())
    log_index = json.loads(log_index_path.read_text())
    general_by_sha = {item["sha256"]: item for item in log_index["content_records"]}
    contents: dict[str, Any] = {}
    aliases = []
    for item in text_map["files"]:
        if item["disposition"] != "retain_unclassified_nonempty":
            continue
        raw = original_bytes(repo, text_map["precleanup_head"], item["path"])
        record = parse(item["path"], raw, general_by_sha[item["source_sha256"]])
        if record["family"] is None:
            continue
        contents.setdefault(item["source_sha256"], record)
        aliases.append({"path": item["path"], "source_sha256": item["source_sha256"], "bytes": item["bytes"], "family": record["family"], "complete_for_semantic_removal": record["complete_for_semantic_removal"]})
    return {
        "schema": SCHEMA,
        "precleanup_head": text_map["precleanup_head"],
        "source_text_retention_map_sha256": hashlib.sha256(text_map_path.read_bytes()).hexdigest(),
        "source_log_index_sha256": hashlib.sha256(log_index_path.read_bytes()).hexdigest(),
        "content_count": len(contents),
        "alias_count": len(aliases),
        "complete_alias_count": sum(item["complete_for_semantic_removal"] for item in aliases),
        "content_records": [contents[key] for key in sorted(contents)],
        "aliases": sorted(aliases, key=lambda item: item["path"]),
        "limitations": ["Named test, error, skip, and terminal facts are retained by the referenced general observation record; no run-level outcome is inferred."],
    }


def build_map(summary: dict[str, Any], summary_path: str) -> dict[str, Any]:
    digest = hashlib.sha256(canonical_bytes(summary)).hexdigest()
    files = []
    for item in summary["aliases"]:
        if item["complete_for_semantic_removal"]:
            files.append({"path": item["path"], "source_sha256": item["source_sha256"], "bytes": item["bytes"], "disposition": "remove_after_metadata_semantic_extraction", "replacement": {"path": summary_path, "sha256": digest, "content_id": item["source_sha256"]}})
    return {"schema": "graph-advantage-metadata-log-retention-map-v1", "source_summary_sha256": digest, "file_count": len(files), "files": files}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--text-map", type=Path, required=True)
    parser.add_argument("--log-index", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--retention-map", type=Path, required=True)
    args = parser.parse_args()
    summary = build(args.repo.resolve(), args.text_map.resolve(), args.log_index.resolve())
    args.output.write_bytes(canonical_bytes(summary))
    public = "docs/implementation/graph-advantage/cleanup/metadata-log-observations.json"
    args.retention_map.write_bytes(canonical_bytes(build_map(summary, public)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
