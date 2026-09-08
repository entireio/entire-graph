#!/usr/bin/env python3
"""Read one durable completion-diagnostic service without changing it."""

import argparse
import json
from pathlib import Path
import re
import subprocess

SYSTEMCTL = "/usr/bin/systemctl"
UNIT_RE = re.compile(r"^p1-diag-[a-z0-9-]+\.service$")
PROPERTIES = (
    "LoadState", "ActiveState", "SubState", "Result", "ExecMainCode",
    "ExecMainStatus", "ExecMainStartTimestampMonotonic",
    "ExecMainExitTimestampMonotonic", "ControlGroup", "MemoryCurrent",
    "MemoryPeak", "MemoryMax", "TasksCurrent", "TasksMax", "CPUUsageNSec",
    "NRestarts", "RuntimeMaxUSec", "OOMPolicy", "KillMode",
    "PrivateNetwork", "RestrictAddressFamilies", "Restart", "RemainAfterExit",
)


def _properties(unit, run=subprocess.run):
    command = [SYSTEMCTL, "show", unit, "--no-pager"]
    command.extend(f"--property={name}" for name in PROPERTIES)
    result = run(command, capture_output=True, text=True)
    if result.returncode:
        return {"LoadState": "unknown", "error": "systemctl show failed"}
    values = {}
    for line in result.stdout.splitlines():
        key, separator, value = line.partition("=")
        if not separator or key not in PROPERTIES or key in values:
            return {"LoadState": "unknown", "error": "systemctl output malformed"}
        values[key] = value
    if set(values) != set(PROPERTIES):
        return {"LoadState": "unknown", "error": "systemctl output incomplete"}
    return values


def _read_json(path):
    try:
        value = json.loads(Path(path).read_text())
    except (OSError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def _last_phase(path, max_bytes=256 * 1024):
    path = Path(path)
    try:
        size = path.stat().st_size
        with path.open("rb") as stream:
            stream.seek(max(0, size - max_bytes))
            raw = stream.read(max_bytes)
    except OSError:
        return {"bytes": None, "last_event": None}
    lines = raw.decode("utf-8", errors="replace").splitlines()
    for line in reversed(lines):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict) and isinstance(value.get("event"), str):
            return {"bytes": size, "last_event": value}
    return {"bytes": size, "last_event": None}


def _cgroup_events(root, control_group):
    if not isinstance(control_group, str) or not control_group.startswith("/"):
        return {"status": "unavailable"}
    cgroup = Path(root) / control_group.lstrip("/")
    result = {"status": "available", "path": control_group}
    for name in ("memory.current", "memory.peak", "memory.max", "memory.events",
                 "pids.current", "pids.max", "pids.events"):
        try:
            result[name] = (cgroup / name).read_text().strip()
        except OSError:
            result[name] = None
    if all(result[name] is None for name in result if name not in {"status", "path"}):
        result["status"] = "unavailable"
    return result


def classify(properties):
    if properties.get("LoadState") in {"not-found", "unknown", None}:
        return "unknown"
    active, sub = properties.get("ActiveState"), properties.get("SubState")
    if active == "active" and sub == "exited":
        return "terminal"
    if active in {"failed", "inactive"} and properties.get("ExecMainExitTimestampMonotonic") not in {"", "0", None}:
        return "terminal"
    if active in {"activating", "active", "deactivating"}:
        return "running"
    return "unknown"


def observe(remote_root, unit, run=subprocess.run, cgroup_root=Path("/sys/fs/cgroup")):
    if not UNIT_RE.fullmatch(unit):
        raise RuntimeError("invalid completion diagnostic systemd unit")
    remote_root = Path(remote_root)
    properties = _properties(unit, run=run)
    lifecycle = classify(properties)
    claim = _read_json(remote_root / "claim-worker-1.json")
    if claim is None:
        claim = _read_json(Path("/opt/p1/batch-claims") / unit.removesuffix(".service") / "worker-1.json")
    record = {
        "schema": "completion-observation-v1",
        "unit": unit,
        "lifecycle": lifecycle,
        "properties": properties,
        "runtime_boundary": _read_json(remote_root / "runtime-boundary.json"),
        "worker_claim": claim,
        "process": _read_json(remote_root / "output/process.json"),
        "outcome": _read_json(remote_root / "output/outcome.json"),
        "profile_status": _read_json(remote_root / "output/relations-cpu.pprof.json"),
        "progress": _last_phase(remote_root / "output/process.log"),
        "collector_log_bytes": _last_phase(remote_root / "collector.log")["bytes"],
        "cgroup": _cgroup_events(cgroup_root, properties.get("ControlGroup")),
    }
    record["product_started"] = bool(claim and claim.get("started"))
    record["terminal_success"] = bool(
        lifecycle == "terminal"
        and properties.get("Result") == "success"
        and properties.get("ExecMainCode") == "exited"
        and properties.get("ExecMainStatus") == "0"
    )
    return record


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--remote-root", type=Path, required=True)
    parser.add_argument("--unit", required=True)
    args = parser.parse_args(argv)
    print(json.dumps(observe(args.remote_root, args.unit), sort_keys=True))


if __name__ == "__main__":
    main()
