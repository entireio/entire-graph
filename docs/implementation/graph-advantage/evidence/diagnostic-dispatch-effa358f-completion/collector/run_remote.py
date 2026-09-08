#!/usr/bin/env python3
"""Collect one resource-bounded completion diagnostic request.

This package is preparation-only until all immutable pins are populated. It
retains the one-request controls and records diagnostic CPU-profile status
without imposing an elapsed product deadline or making a performance or
admission claim.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import sys

CONTROL_ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(CONTROL_ROOT))
import batch_budget


EXPECTED_SOURCE_COMMIT = "effa358f2ceaca2b9accd829984272598fa78078"
EXPECTED_BINARY_SHA256 = "f2c2940a565397a010488843af99ff469c725dd54a720c90ed190311249ed1f6"
EXPECTED_REPOSITORY = "kubernetes-kubernetes"
EXPECTED_REPO_PATH = "/opt/p1/corpus/kubernetes-kubernetes"
EXPECTED_PROVIDER = "p1-corpus-20260905"
EXPECTED_SOURCE_DIGEST = "d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4"
EXPECTED_INPUT_MANIFEST_SHA256 = "d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0"
EXPECTED_MUTATION_ID = "late-relations-cpu-profile-prep"
EXPECTED_SCENARIO = "diagnostic"
EXPECTED_PROFILE = "full"
EXPECTED_SYSTEMD_UNIT = "p1-diag-effa358f-k8s-completion-off-01.service"
EXPECTED_MEMORY_MAX_BYTES = 15032385536
EXPECTED_TASKS_MAX = 512
TIME_BINARY = "/usr/bin/time"
FINGERPRINT_TIMEOUT_SECONDS = 60
SYSTEMCTL_BINARY = "/usr/bin/systemctl"
PROC_SELF_CGROUP = Path("/proc/self/cgroup")
PROC_SELF_NETNS = Path("/proc/self/ns/net")
PROC_INIT_NETNS = Path("/proc/1/ns/net")
CGROUP_ROOT = Path("/sys/fs/cgroup")
RSS_PREFIX = "Maximum resident set size (kbytes):"
RSS_VALUE_RE = re.compile(r"^[0-9]+$")
MAX_RSS_BYTES = (1 << 64) - 1
FIXED_CLAIM_ROOT = Path("/opt/p1/batch-claims")
PHASE_BREADCRUMBS_ENV = "ENTIRE_GRAPH_EXTRACTION_CORPUS_PHASE_BREADCRUMBS"
RELATIONS_CPU_PROFILE_ENV = "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE"
RELATIONS_CPU_PROFILE_START_AFTER_ENV = "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_START_AFTER_NS"
RELATIONS_CPU_PROFILE_NAME = "relations-cpu.pprof"
RELATIONS_CPU_PROFILE_STATUS_NAME = "relations-cpu.pprof.json"
RELATIONS_CPU_PROFILE_MAX_BYTES = 8 << 20
RELATIONS_CPU_PROFILE_WINDOW_NS = 20_000_000_000
RELATIONS_CPU_PROFILE_LATEST_START_NS = 90_000_000_000
RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS = 88_000_000_000
RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES = 64 << 10
RELATIONS_CPU_PROFILE_STATUSES = frozenset({
    "scheduled", "starting", "active", "complete", "incomplete",
    "missed_safe_window", "cancelled", "error",
})


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def save(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def _regular_file_size(path, label, max_bytes=None):
    """Inspect without following symlinks and enforce an optional byte bound."""
    path = Path(path)
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    except OSError as error:
        raise RuntimeError(f"{label} cannot be inspected: {error}") from error
    if stat.S_ISLNK(info.st_mode):
        raise RuntimeError(f"{label} symlink is not allowed")
    if not stat.S_ISREG(info.st_mode):
        raise RuntimeError(f"{label} is not a regular file")
    if max_bytes is not None and info.st_size > max_bytes:
        raise RuntimeError(f"{label} exceeds {max_bytes} bytes")
    return info.st_size


def _bounded_sha256(path, label, max_bytes):
    size = _regular_file_size(path, label, max_bytes)
    if size is None:
        raise FileNotFoundError(path)
    digest = hashlib.sha256()
    total = 0
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(min(1024 * 1024, max_bytes - total + 1)), b""):
            total += len(block)
            if total > max_bytes:
                raise RuntimeError(f"{label} exceeds {max_bytes} bytes")
            digest.update(block)
    return digest.hexdigest()


def existing_artifact_hashes(root):
    root = Path(root)
    names = (
        "identity.json", "budget.json", "manifest.json", "before.json", "after.json",
        "observation.ndjson", "diagnostics.json", "process.log", "process.json",
        "time.txt", "after-identity.json", "outcome.json", "runtime-boundary.json",
        RELATIONS_CPU_PROFILE_NAME, RELATIONS_CPU_PROFILE_STATUS_NAME,
    )
    hashes = {}
    for name in names:
        path = root / name
        if not path.exists() and not path.is_symlink():
            continue
        try:
            if name == RELATIONS_CPU_PROFILE_NAME:
                hashes[name] = _bounded_sha256(path, "relations CPU profile", RELATIONS_CPU_PROFILE_MAX_BYTES)
            elif name == RELATIONS_CPU_PROFILE_STATUS_NAME:
                hashes[name] = _bounded_sha256(path, "relations CPU profile status", RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES)
            else:
                hashes[name] = sha256(path)
        except (OSError, RuntimeError):
            # Profile validation records the explicit issue; never follow or hash
            # an invalid profile artifact while assembling retained evidence.
            if name not in (RELATIONS_CPU_PROFILE_NAME, RELATIONS_CPU_PROFILE_STATUS_NAME):
                raise
    return hashes


def _profile_paths(output):
    output = Path(output).resolve()
    return output / RELATIONS_CPU_PROFILE_NAME, output / RELATIONS_CPU_PROFILE_STATUS_NAME


def validate_cpu_profile_artifacts(output, require_complete=False):
    """Return bounded profile metadata; never include profile bytes in outcomes."""
    profile_path, status_path = _profile_paths(output)
    metadata = {
        "profile": RELATIONS_CPU_PROFILE_NAME,
        "status_file": RELATIONS_CPU_PROFILE_STATUS_NAME,
        "max_bytes": RELATIONS_CPU_PROFILE_MAX_BYTES,
        "window_ns": RELATIONS_CPU_PROFILE_WINDOW_NS,
        "latest_start_ns": RELATIONS_CPU_PROFILE_LATEST_START_NS,
        "status": "missing",
        "profile_present": False,
        "status_present": False,
    }
    status_size = _regular_file_size(status_path, "relations CPU profile status", RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES)
    profile_size = _regular_file_size(profile_path, "relations CPU profile", RELATIONS_CPU_PROFILE_MAX_BYTES)
    metadata["profile_present"] = profile_size is not None
    metadata["status_present"] = status_size is not None
    if status_size is None:
        if require_complete:
            raise RuntimeError("relations CPU profile status is missing")
        metadata["validation"] = "missing"
        return metadata
    try:
        with status_path.open("rb") as stream:
            raw_status = stream.read(RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES + 1)
        if len(raw_status) > RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES:
            raise RuntimeError(f"relations CPU profile status exceeds {RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES} bytes")
        status = json.loads(raw_status.decode("utf-8"))
    except RuntimeError:
        raise
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"relations CPU profile status is invalid: {error}") from error
    if not isinstance(status, dict):
        raise RuntimeError("relations CPU profile status is not an object")
    value = status.get("status")
    if not isinstance(value, str) or value not in RELATIONS_CPU_PROFILE_STATUSES:
        raise RuntimeError("relations CPU profile status is unknown")
    metadata["status"] = value
    window = status.get("window_ns")
    if isinstance(window, bool) or not isinstance(window, int) or window != RELATIONS_CPU_PROFILE_WINDOW_NS:
        raise RuntimeError("relations CPU profile window is missing or invalid")
    requested_start = status.get("requested_start_after_ns")
    if (isinstance(requested_start, bool) or not isinstance(requested_start, int)
            or requested_start != RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS):
        raise RuntimeError("relations CPU profile requested start does not match the 88-second diagnostic pin")
    for field in ("trigger_elapsed_ns", "actual_start_elapsed_ns", "actual_ns", "bytes",
                  "relations_first", "relations_latest", "relation_progress_events"):
        if field in status and (isinstance(status[field], bool) or not isinstance(status[field], int) or status[field] < 0):
            raise RuntimeError(f"relations CPU profile {field} is invalid")
    for field in ("relations_first", "relations_latest", "relation_progress_events"):
        if field not in status:
            raise RuntimeError(f"relations CPU profile {field} is missing")
    if status["relations_latest"] < status["relations_first"] or status["relation_progress_events"] < 1:
        raise RuntimeError("relations CPU profile progress metadata is inconsistent")
    trigger_value = status.get("trigger_elapsed_ns")
    if value != "missed_safe_window" and trigger_value is not None and trigger_value > RELATIONS_CPU_PROFILE_LATEST_START_NS:
        raise RuntimeError("relations CPU profile trigger is outside the 90-second safe window")
    bytes_value = status.get("bytes")
    if bytes_value is not None and bytes_value > RELATIONS_CPU_PROFILE_MAX_BYTES:
        raise RuntimeError("relations CPU profile declared size exceeds 8 MiB")
    for marker in ("diagnostic_only", "no_performance_claim", "profile_overhead_present"):
        if marker in status and not isinstance(status[marker], bool):
            raise RuntimeError(f"relations CPU profile {marker} is invalid")
    declared_sha_value = status.get("sha256")
    if declared_sha_value is not None and (not isinstance(declared_sha_value, str) or not re.fullmatch(r"[0-9a-f]{64}", declared_sha_value)):
        raise RuntimeError("relations CPU profile SHA-256 is malformed")
    metadata["diagnostic_only"] = status.get("diagnostic_only")
    metadata["no_performance_claim"] = status.get("no_performance_claim")
    metadata["profile_overhead_present"] = status.get("profile_overhead_present")
    metadata["requested_start_after_ns"] = requested_start
    metadata["trigger_elapsed_ns"] = status.get("trigger_elapsed_ns")
    metadata["actual_start_elapsed_ns"] = status.get("actual_start_elapsed_ns")
    metadata["actual_ns"] = status.get("actual_ns")
    metadata["relations_first"] = status.get("relations_first")
    metadata["relations_latest"] = status.get("relations_latest")
    metadata["relation_progress_events"] = status.get("relation_progress_events")
    metadata["declared_bytes"] = status.get("bytes")
    metadata["declared_sha256"] = status.get("sha256")
    metadata["error"] = status.get("error")
    if value != "complete":
        metadata["validation"] = "retained_incomplete"
        if require_complete:
            raise RuntimeError(f"relations CPU profile status is {value!r}, not complete")
        return metadata
    if profile_size is None:
        raise RuntimeError("complete relations CPU profile status has no profile file")
    if status.get("diagnostic_only") is not True or status.get("no_performance_claim") is not True:
        raise RuntimeError("complete relations CPU profile lacks diagnostic-only/no-performance markers")
    if status.get("profile_overhead_present") is not True:
        raise RuntimeError("complete relations CPU profile lacks overhead marker")
    trigger = status.get("trigger_elapsed_ns")
    if not isinstance(trigger, int) or trigger < 0 or trigger > RELATIONS_CPU_PROFILE_LATEST_START_NS:
        raise RuntimeError("relations CPU profile trigger is outside the 90-second safe window")
    actual_start = status.get("actual_start_elapsed_ns")
    if (isinstance(actual_start, bool) or not isinstance(actual_start, int)
            or actual_start < RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS
            or actual_start > RELATIONS_CPU_PROFILE_LATEST_START_NS):
        raise RuntimeError("complete relations CPU profile actual start is outside the requested 88-to-90-second window")
    if trigger != actual_start:
        raise RuntimeError("complete relations CPU profile trigger and actual start differ")
    actual = status.get("actual_ns")
    if not isinstance(actual, int) or isinstance(actual, bool) or actual <= 0:
        raise RuntimeError("complete relations CPU profile actual duration is invalid")
    data_size = profile_size
    if data_size <= 0 or data_size > RELATIONS_CPU_PROFILE_MAX_BYTES:
        raise RuntimeError("relations CPU profile size is outside the 8 MiB bound")
    declared_size = status.get("bytes")
    if declared_size != data_size:
        raise RuntimeError("relations CPU profile byte count does not match file")
    declared_sha = status.get("sha256")
    if not isinstance(declared_sha, str) or not re.fullmatch(r"[0-9a-f]{64}", declared_sha):
        raise RuntimeError("relations CPU profile SHA-256 is missing or malformed")
    actual_sha = _bounded_sha256(profile_path, "relations CPU profile", RELATIONS_CPU_PROFILE_MAX_BYTES)
    if _regular_file_size(profile_path, "relations CPU profile", RELATIONS_CPU_PROFILE_MAX_BYTES) != data_size:
        raise RuntimeError("relations CPU profile changed while hashing")
    if actual_sha != declared_sha:
        raise RuntimeError("relations CPU profile SHA-256 does not match file")
    metadata.update({"bytes": data_size, "sha256": actual_sha, "validation": "complete"})
    return metadata


def read_json_object_with_raw(path):
    """Decode one object while retaining exact UTF-8 member value bytes."""
    raw = Path(path).read_bytes()
    text = raw.decode("utf-8")
    decoder = json.JSONDecoder()
    offsets = [0]
    for character in text:
        offsets.append(offsets[-1] + len(character.encode("utf-8")))

    def whitespace(position):
        while position < len(text) and text[position] in " \t\r\n":
            position += 1
        return position

    position = whitespace(0)
    if position >= len(text) or text[position] != "{":
        raise RuntimeError(f"{path} root is not a JSON object")
    position = whitespace(position + 1)
    values, raw_values = {}, {}
    if position < len(text) and text[position] == "}":
        position += 1
    else:
        while True:
            position = whitespace(position)
            key, position = decoder.raw_decode(text, position)
            if not isinstance(key, str) or key in values:
                raise RuntimeError(f"{path} has an invalid or duplicate root key")
            position = whitespace(position)
            if position >= len(text) or text[position] != ":":
                raise RuntimeError(f"{path} has malformed JSON object member")
            position = whitespace(position + 1)
            value_start = position
            value, position = decoder.raw_decode(text, position)
            values[key] = value
            raw_values[key] = raw[offsets[value_start]:offsets[position]]
            position = whitespace(position)
            if position < len(text) and text[position] == "}":
                position += 1
                break
            if position >= len(text) or text[position] != ",":
                raise RuntimeError(f"{path} has malformed JSON object delimiter")
            position += 1
    if whitespace(position) != len(text):
        raise RuntimeError(f"{path} has trailing JSON data")
    return values, raw_values


def runtime_environment(corpus_root):
    return {
        "PATH": "/usr/local/go/bin:/usr/bin:/bin",
        "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "LC_CTYPE": "C.UTF-8",
        "GOMAXPROCS": "4", "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_CONFIG_SYSTEM": "/dev/null", "GOPROXY": "off", "GOSUMDB": "off",
        "GOTOOLCHAIN": "local", "GOTELEMETRY": "off",
        "P1_CORPUS_ROOT": str(corpus_root),
        PHASE_BREADCRUMBS_ENV: "1",
        RELATIONS_CPU_PROFILE_ENV: "1",
        RELATIONS_CPU_PROFILE_START_AFTER_ENV: str(RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS),
    }


def _systemd_properties(unit, run=subprocess.run):
    names = (
        "LoadState", "MemoryMax", "TasksMax", "PrivateNetwork",
        "RestrictAddressFamilies", "KillMode", "RuntimeMaxUSec", "OOMPolicy",
        "Restart", "RemainAfterExit", "ControlGroup",
    )
    command = [SYSTEMCTL_BINARY, "show", unit, "--no-pager"]
    command.extend(f"--property={name}" for name in names)
    result = run(command, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError("cannot inspect completion diagnostic systemd unit")
    properties = {}
    for line in result.stdout.splitlines():
        key, separator, value = line.partition("=")
        if not separator or key not in names or key in properties:
            raise RuntimeError("completion diagnostic systemd properties are malformed")
        properties[key] = value
    if set(properties) != set(names):
        raise RuntimeError("completion diagnostic systemd properties are incomplete")
    return properties


def _unified_cgroup_path(raw):
    matches = []
    for line in raw.splitlines():
        hierarchy, separator, path = line.partition("::")
        if separator and hierarchy == "0" and path.startswith("/"):
            matches.append(path)
    if len(matches) != 1 or ".." in Path(matches[0]).parts:
        raise RuntimeError("completion diagnostic requires one unified cgroup v2 path")
    return matches[0]


def verify_runtime_boundary(
        evidence_path, unit, *, run=subprocess.run, cgroup_file=PROC_SELF_CGROUP,
        cgroup_root=CGROUP_ROOT, self_netns=PROC_SELF_NETNS,
        init_netns=PROC_INIT_NETNS, socket_factory=socket.socket):
    """Fail closed before claim/product start on resource and no-egress controls."""
    evidence_path = Path(evidence_path)
    record = {"schema": "completion-runtime-boundary-v1", "unit": unit,
              "status": "refused", "product_started": False}
    try:
        if unit != EXPECTED_SYSTEMD_UNIT:
            raise RuntimeError("completion diagnostic systemd unit identity mismatch")
        properties = _systemd_properties(unit, run=run)
        expected = {
            "LoadState": "loaded",
            "MemoryMax": str(EXPECTED_MEMORY_MAX_BYTES),
            "TasksMax": str(EXPECTED_TASKS_MAX),
            "PrivateNetwork": "yes",
            "RestrictAddressFamilies": "AF_UNIX",
            "KillMode": "control-group",
            "RuntimeMaxUSec": "infinity",
            "OOMPolicy": "kill",
            "Restart": "no",
            "RemainAfterExit": "yes",
        }
        for name, value in expected.items():
            if properties.get(name) != value:
                raise RuntimeError(f"completion diagnostic systemd property mismatch: {name}")
        cgroup_path = _unified_cgroup_path(Path(cgroup_file).read_text())
        if properties["ControlGroup"] != cgroup_path:
            raise RuntimeError("completion diagnostic effective cgroup path mismatch")
        cgroup = Path(cgroup_root) / cgroup_path.lstrip("/")
        memory_max = (cgroup / "memory.max").read_text().strip()
        pids_max = (cgroup / "pids.max").read_text().strip()
        if memory_max != str(EXPECTED_MEMORY_MAX_BYTES):
            raise RuntimeError("completion diagnostic memory.max is not 14 GiB")
        if pids_max != str(EXPECTED_TASKS_MAX):
            raise RuntimeError("completion diagnostic pids.max is not 512")
        self_inode = Path(self_netns).stat().st_ino
        init_inode = Path(init_netns).stat().st_ino
        if self_inode == init_inode:
            raise RuntimeError("completion diagnostic network namespace is not private")
        network_probes = {}
        for family, label in ((socket.AF_INET, "AF_INET"), (socket.AF_INET6, "AF_INET6")):
            try:
                candidate = socket_factory(family, socket.SOCK_STREAM)
            except OSError as error:
                network_probes[label] = {"denied": True, "errno": error.errno}
            else:
                candidate.close()
                raise RuntimeError(f"completion diagnostic network family is available: {label}")
        record.update({
            "status": "passed", "properties": properties,
            "cgroup_path": cgroup_path, "memory_max": memory_max,
            "pids_max": pids_max, "network_namespace_inode": self_inode,
            "host_network_namespace_inode": init_inode,
            "network_namespace_isolated": True,
            "network_probes": network_probes,
        })
    except Exception as error:
        record["error"] = str(error)
        save(evidence_path, record)
        raise
    save(evidence_path, record)
    return record


def parse_peak_rss_bytes(raw_time):
    values = []
    for line in raw_time.splitlines():
        line = line.strip()
        if line.startswith(RSS_PREFIX):
            value = line[len(RSS_PREFIX):].strip()
            if not RSS_VALUE_RE.fullmatch(value):
                raise ValueError("malformed GNU time peak RSS")
            values.append(int(value))
    if len(values) != 1 or values[0] <= 0:
        raise ValueError("missing or invalid GNU time peak RSS")
    result = values[0] * 1024
    if result > MAX_RSS_BYTES:
        raise ValueError("GNU time peak RSS exceeds representable range")
    return result


def fingerprint(root, scenario_script, corpus_root, stage):
    result = subprocess.run(
        ["/usr/bin/python3", str(scenario_script), "digest", EXPECTED_REPOSITORY],
        env=runtime_environment(corpus_root), capture_output=True,
        timeout=FINGERPRINT_TIMEOUT_SECONDS,
    )
    root = Path(root)
    (root / f"{stage}.log").write_bytes(result.stderr)
    (root / f"{stage}.json").write_bytes(result.stdout)
    if result.returncode:
        raise RuntimeError(f"{stage} input fingerprint failed")
    value = json.loads(result.stdout)
    if value.get("effective_tracked_input_sha256") != EXPECTED_SOURCE_DIGEST:
        raise RuntimeError(f"{stage} input identity mismatch")
    return value


def expected_manifest(output, binary_sha256):
    output = Path(output).resolve()
    return {
        "version": 1,
        "source_commit": EXPECTED_SOURCE_COMMIT,
        "binary_sha256": binary_sha256,
        "input_manifest_sha256": EXPECTED_INPUT_MANIFEST_SHA256,
        "repository": EXPECTED_REPOSITORY,
        "repository_id": EXPECTED_REPOSITORY,
        "repo_path": EXPECTED_REPO_PATH,
        "operation": "snapshot", "mode": "measure", "cache": "off",
        "cache_path": str(output / "cache-off-unused"),
        "profile": EXPECTED_PROFILE, "provider_version": EXPECTED_PROVIDER,
        "mutation_id": EXPECTED_MUTATION_ID, "source_digest": EXPECTED_SOURCE_DIGEST,
        "scenario": EXPECTED_SCENARIO, "trial": 0,
        "diagnostics_path": "diagnostics.json",
    }


def validate_identity(value, manifest, binary_sha256, label):
    expected = {
        "repository": EXPECTED_REPOSITORY, "repository_path": EXPECTED_REPO_PATH,
        "operation": "snapshot", "mode": "measure", "cache_mode": "off",
        "profile": EXPECTED_PROFILE, "provider_version": EXPECTED_PROVIDER,
        "mutation_id": EXPECTED_MUTATION_ID, "source_digest": EXPECTED_SOURCE_DIGEST,
        "binary_sha256": binary_sha256, "scenario": EXPECTED_SCENARIO,
        "trial": 0, "reuse": False, "verb": "snapshot",
    }
    for field, wanted in expected.items():
        if value.get(field) != wanted:
            raise RuntimeError(f"{label} identity mismatch: {field}")
    if value.get("manifest_path") != str(Path(manifest["_path"]).resolve()):
        raise RuntimeError(f"{label} identity mismatch: manifest_path")


def validate_observation(observation, manifest, binary_sha256):
    validate_identity(observation, manifest, binary_sha256, "observation")
    status = observation.get("status")
    if status not in {"ok", "partial", "error"}:
        raise RuntimeError("observation status is not a supported terminal status")
    if status in {"ok", "partial"}:
        semantic = observation.get("semantic_sha256")
        if not isinstance(semantic, str) or not re.fullmatch(r"[0-9a-f]{64}", semantic):
            raise RuntimeError("successful observation semantic digest is missing")
        if observation.get("semantic_digest") != semantic:
            raise RuntimeError("observation semantic digest aliases differ")
    elif not isinstance(observation.get("error"), str) or not observation["error"].strip():
        raise RuntimeError("error observation has no explicit error")
    if not isinstance(observation.get("partial_failures_count"), int) or observation["partial_failures_count"] < 0:
        raise RuntimeError("observation partial-failure count is missing")
    if not isinstance(observation.get("warnings_count"), int) or observation["warnings_count"] < 0:
        raise RuntimeError("observation warning count is missing")
    for field in ("partial_failures_sha256", "warnings_sha256"):
        value = observation.get(field)
        if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value):
            raise RuntimeError(f"observation {field} is missing or malformed")


def validate_diagnostics(diagnostics, observation, observation_path, manifest, binary_sha256, raw_values):
    validate_identity(diagnostics, manifest, binary_sha256, "diagnostics")
    if diagnostics.get("observation_path") != str(Path(observation_path).resolve()):
        raise RuntimeError("diagnostics observation_path mismatch")
    identity_fields = (
        "manifest_path", "repository", "repository_path", "operation", "mode",
        "cache_mode", "cache_path", "profile", "query", "provider_version", "mutation_id", "source_digest", "binary_sha256",
        "scenario", "trial", "reuse", "verb", "status", "error", "started_at",
        "semantic_sha256", "semantic_digest", "semantic_bytes", "partial_failures_count",
        "partial_failures_sha256", "warnings_count", "warnings_sha256",
    )
    for field in identity_fields:
        if diagnostics.get(field) != observation.get(field):
            raise RuntimeError(f"diagnostics identity differs from observation: {field}")
    if diagnostics.get("repository_id") not in (None, EXPECTED_REPOSITORY):
        raise RuntimeError("diagnostics repository_id differs from fixed repository identity")
    if diagnostics.get("partial_failures_count") != observation.get("partial_failures_count") or diagnostics.get("warnings_count") != observation.get("warnings_count"):
        raise RuntimeError("diagnostics counts differ from observation")
    failures = diagnostics.get("partial_failures")
    warnings = diagnostics.get("warnings")
    if not isinstance(failures, list) or len(failures) != diagnostics.get("partial_failures_count"):
        raise RuntimeError("full partial-failure artifact count mismatch")
    if not isinstance(warnings, list) or len(warnings) != diagnostics.get("warnings_count"):
        raise RuntimeError("full warning artifact count mismatch")
    if not isinstance(raw_values, dict) or "partial_failures" not in raw_values or "warnings" not in raw_values:
        raise RuntimeError("diagnostic arrays are absent from raw artifact")
    if hashlib.sha256(raw_values["partial_failures"]).hexdigest() != observation.get("partial_failures_sha256"):
        raise RuntimeError("full partial-failure digest does not match raw artifact")
    if hashlib.sha256(raw_values["warnings"]).hexdigest() != observation.get("warnings_sha256"):
        raise RuntimeError("full warning digest does not match raw artifact")
    return {"partial_failures": len(failures), "warnings": len(warnings)}


def run_process(root, binary, environment, process_factory=subprocess.Popen):
    root = Path(root)
    time_path = root / "time.txt"
    process_log = root / "process.log"
    command = [TIME_BINARY, "-v", "-o", str(time_path), "--", str(binary),
               "-test.run=^TestExtractionCorpusMeasurement$", "-test.count=1", "-test.v",
               "-test.timeout=0"]
    with process_log.open("wb") as log:
        process = process_factory(command, env=environment, cwd=root, stdout=log,
                                  stderr=subprocess.STDOUT, start_new_session=True)
        exit_code = process.wait()
    raw_time = time_path.read_text() if time_path.is_file() else ""
    try:
        peak_rss_bytes = parse_peak_rss_bytes(raw_time)
        rss_error = None
    except ValueError as error:
        peak_rss_bytes, rss_error = None, str(error)
    save(root / "process.json", {"command": command, "exit_code": exit_code,
        "timed_out": False, "timeout_seconds": None,
        "product_deadline_seconds": None,
        "peak_rss_bytes": peak_rss_bytes,
        "rss_status": "measured by /usr/bin/time -v" if rss_error is None else "invalid",
        "rss_error": rss_error})
    if exit_code:
        raise RuntimeError(f"request process failed with exit {exit_code}")
    if rss_error is not None:
        raise RuntimeError("request RSS measurement invalid")
    return {"exit_code": exit_code, "peak_rss_bytes": peak_rss_bytes}


def load_build_identity(path, binary_sha256):
    path = Path(path).resolve()
    try:
        document = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise RuntimeError(f"cannot read build manifest: {error}") from error
    if not isinstance(document, dict) or document.get("binary_sha256") != binary_sha256:
        raise RuntimeError("build manifest binary identity mismatch")
    if document.get("source_commit") != EXPECTED_SOURCE_COMMIT or document.get("exit_code") != 0:
        raise RuntimeError("build manifest source/build identity mismatch")
    return {"path": path, "sha256": sha256(path), "document": document}


def post_request_identity(output, binary, scenario_script, source_root, build_manifest, batch_manifest, expected_control):
    """Record source/binary/control identities even after timeout or failure."""
    # Keep expected and observed values separate so a failed post-request check
    # itself never hides the evidence needed to diagnose drift.
    targets = {
        "binary_sha256": (Path(binary), None),
        "scenario_sha256": (Path(scenario_script), None),
        "build_manifest_sha256": (Path(build_manifest), None),
        "batch_manifest_sha256": (Path(batch_manifest), None),
        "runner_sha256": (CONTROL_ROOT / "run_remote.py", None),
        "budget_sha256": (CONTROL_ROOT / "batch_budget.py", None),
        "gate_sha256": (CONTROL_ROOT / "campaign_gate.py", None),
    }
    observed = {}
    errors = []
    for field, (path, _) in targets.items():
        try:
            observed[field] = sha256(path)
        except (OSError, ValueError) as error:
            observed[field] = None
            errors.append(f"{field}: {error}")
    expected_binary = None
    try:
        expected_binary = json.loads(Path(build_manifest).read_text()).get("binary_sha256")
    except (OSError, json.JSONDecodeError) as error:
        errors.append(f"build_manifest: {error}")
    expected_source = None
    try:
        expected_source = json.loads(Path(build_manifest).read_text()).get("source_commit")
    except (OSError, json.JSONDecodeError):
        pass
    expected = dict(expected_control)
    expected.update({
        "binary_sha256": expected_binary,
        "source_commit": EXPECTED_SOURCE_COMMIT,
        "source_root": str(Path(source_root).resolve()),
        "build_manifest_source_commit": expected_source,
    })
    mismatches = []
    if expected_binary is not None and observed["binary_sha256"] != expected_binary:
        mismatches.append("binary_sha256")
    if expected_source is not None and expected_source != EXPECTED_SOURCE_COMMIT:
        mismatches.append("build_manifest_source_commit")
    for field, wanted in expected_control.items():
        if field in observed and wanted is not None and observed[field] != wanted:
            mismatches.append(field)
    record = {"expected": expected, "observed": observed, "mismatches": sorted(set(mismatches)), "errors": errors}
    save(Path(output) / "after-identity.json", record)
    return record


def claim_diagnostic_budget(batch_manifest, build_manifest, scenario_script, binary_sha256, worker, claim_root=FIXED_CLAIM_ROOT):
    build = load_build_identity(build_manifest, binary_sha256)
    identity = {
        "binary_sha256": binary_sha256, "input_manifest_sha256": EXPECTED_INPUT_MANIFEST_SHA256,
        "runner_sha256": sha256(__file__), "scenario_sha256": sha256(scenario_script),
        "gate_sha256": sha256(CONTROL_ROOT / "campaign_gate.py"),
        "budget_sha256": sha256(CONTROL_ROOT / "batch_budget.py"),
        "build_manifest_sha256": build["sha256"],
    }
    try:
        batch = batch_budget.load(Path(batch_manifest).resolve(), identity)
    except batch_budget.BudgetError as error:
        raise RuntimeError(str(error)) from error
    if batch.get("scope") != "bounded-diagnostic" or batch.get("derived_invocations") != 1 or len(batch.get("cells", [])) != 1:
        raise RuntimeError("collector batch must contain exactly one bounded diagnostic cell")
    cell = batch["cells"][0]
    expected = {"worker": worker, "repository": EXPECTED_REPOSITORY, "profile": EXPECTED_PROFILE,
                "verb": "snapshot", "scenario": EXPECTED_SCENARIO, "trial": 0,
                "arms": [False], "preparatory_invocations": 0}
    for field, wanted in expected.items():
        if cell.get(field) != wanted:
            raise RuntimeError(f"collector batch cell mismatch: {field}")
    try:
        ledger = batch_budget.WorkerLedger(Path(claim_root) / batch["batch_id"] / f"worker-{worker}.json", batch, worker)
    except batch_budget.BudgetError as error:
        raise RuntimeError(str(error)) from error
    claim_path = Path(claim_root) / batch["batch_id"] / f"worker-{worker}.json"
    return batch, cell, ledger, claim_path, identity


def run_request(output, binary, scenario_script, corpus_root, binary_sha256, source_root,
                build_manifest, batch_manifest, batch_worker, fingerprint_fn=fingerprint,
                process_factory=subprocess.Popen, _claim_root=FIXED_CLAIM_ROOT,
                runtime_boundary=None):
    output = Path(output).resolve()
    if Path(corpus_root).resolve() != Path(EXPECTED_REPO_PATH).parent:
        raise RuntimeError("fingerprint corpus root differs from fixed product repository")
    if output.exists():
        raise RuntimeError("output directory already exists; refusing overwrite")
    binary, scenario_script, source_root = map(lambda p: Path(p).resolve(), (binary, scenario_script, source_root))
    if not binary.is_file() or not scenario_script.is_file() or not source_root.is_dir() or not binary.is_relative_to(source_root):
        raise RuntimeError("source, binary, or scenario input unavailable")
    if sha256(binary) != binary_sha256:
        raise RuntimeError("binary identity mismatch")
    scenario_sha256 = sha256(scenario_script)
    batch, cell, ledger, claim_path, budget_identity = claim_diagnostic_budget(
        batch_manifest, build_manifest, scenario_script, binary_sha256, batch_worker, _claim_root)
    output.mkdir(parents=True)
    if not isinstance(runtime_boundary, dict) or runtime_boundary.get("status") != "passed":
        raise RuntimeError("verified completion runtime boundary is required")
    save(output / "runtime-boundary.json", runtime_boundary)
    manifest = expected_manifest(output, binary_sha256)
    manifest_path = output / "manifest.json"
    manifest["_path"] = str(manifest_path)
    save(manifest_path, {key: value for key, value in manifest.items() if key != "_path"})
    save(output / "identity.json", {"source_commit": EXPECTED_SOURCE_COMMIT, "source_root": str(source_root),
        "binary": str(binary), "binary_sha256": binary_sha256, "scenario_script": str(scenario_script),
        "scenario_sha256": sha256(scenario_script), "repository": EXPECTED_REPOSITORY,
        "repository_path": EXPECTED_REPO_PATH, "source_digest": EXPECTED_SOURCE_DIGEST,
        "input_manifest_sha256": EXPECTED_INPUT_MANIFEST_SHA256, "profile": EXPECTED_PROFILE,
        "request_scope": "one cache-off full-profile late-relations snapshot; no ON arm, repeats, ratios, or admission",
        "relations_cpu_profile_start_after_ns": RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS})
    save(output / "budget.json", {"batch_id": batch["batch_id"], "batch_manifest": str(Path(batch_manifest).resolve()),
        "batch_manifest_sha256": batch["manifest_sha256"], "cell_id": cell["cell_id"],
        "phase": "arm:false", "worker": batch_worker, "claim_path": str(claim_path), "identity": budget_identity})
    environment = runtime_environment(corpus_root)
    environment.update(ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST=str(manifest_path),
                       ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT=str(output / "observation.ndjson"),
                       ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT="ndjson")
    expected_control = {
        "binary_sha256": binary_sha256,
        "scenario_sha256": scenario_sha256,
        "build_manifest_sha256": sha256(build_manifest),
        "batch_manifest_sha256": sha256(batch_manifest),
        "runner_sha256": sha256(CONTROL_ROOT / "run_remote.py"),
        "budget_sha256": sha256(CONTROL_ROOT / "batch_budget.py"),
        "gate_sha256": sha256(CONTROL_ROOT / "campaign_gate.py"),
    }
    issue, before, process, artifact_counts = None, None, None, None
    profile_metadata = None
    started = False
    try:
        before = fingerprint_fn(output, scenario_script, corpus_root, "before")
        ledger.start(cell["cell_id"], "arm:false")
        started = True
        process = run_process(output, binary, environment, process_factory)
        if sha256(binary) != binary_sha256 or sha256(scenario_script) != scenario_sha256:
            raise RuntimeError("source or binary identity changed after request")
        observation_path, diagnostics_path = output / "observation.ndjson", output / "diagnostics.json"
        observation, _ = read_json_object_with_raw(observation_path)
        diagnostics, diagnostics_raw = read_json_object_with_raw(diagnostics_path)
        validate_observation(observation, manifest, binary_sha256)
        artifact_counts = validate_diagnostics(diagnostics, observation, observation_path, manifest, binary_sha256, diagnostics_raw)
        if observation["status"] == "error":
            raise RuntimeError("observation reported an explicit request error")
        profile_metadata = validate_cpu_profile_artifacts(output, require_complete=True)
        after = fingerprint_fn(output, scenario_script, corpus_root, "after")
        if after != before:
            raise RuntimeError("input fingerprint changed during request")
    except Exception as error:
        issue = str(error)
    finally:
        if before is not None and not (output / "after.json").exists():
            try:
                after = fingerprint_fn(output, scenario_script, corpus_root, "after")
                if after != before:
                    issue = (issue + "; " if issue else "") + "input fingerprint changed during request"
            except Exception as error:
                issue = (issue + "; " if issue else "") + str(error)
        if profile_metadata is None:
            try:
                profile_metadata = validate_cpu_profile_artifacts(output, require_complete=issue is None)
                if profile_metadata.get("validation") != "complete":
                    issue = (issue + "; " if issue else "") + "relations CPU profile was not complete"
            except Exception as error:
                issue = (issue + "; " if issue else "") + str(error)
                profile_metadata = {
                    "profile": RELATIONS_CPU_PROFILE_NAME,
                    "status_file": RELATIONS_CPU_PROFILE_STATUS_NAME,
                    "validation": "error", "error": str(error),
                    "profile_present": False,
                    "status_present": False,
                }
        try:
            identity = post_request_identity(output, binary, scenario_script, source_root,
                                             build_manifest, batch_manifest, expected_control)
            if identity["mismatches"] or identity["errors"]:
                drift = ", ".join(identity["mismatches"] + identity["errors"])
                issue = (issue + "; " if issue else "") + "post-request identity check failed: " + drift
        except Exception as error:
            issue = (issue + "; " if issue else "") + f"post-request identity recording failed: {error}"
        if started:
            try:
                ledger.finish(cell["cell_id"], "arm:false", "ok" if issue is None else "error")
            except Exception as error:
                issue = (issue + "; " if issue else "") + f"budget ledger finish failed: {error}"
        if issue is not None:
            save(output / "outcome.json", {"status": "issue", "issue": issue, "admission_eligible": False,
                "review_status": "not_reviewed", "no_performance_claim": True,
                "process": process, "profile": profile_metadata,
                "artifact_sha256": existing_artifact_hashes(output)})
    if issue is not None:
        return {"status": "issue", "issue": issue}
    observation, _ = read_json_object_with_raw(output / "observation.ndjson")
    save(output / "outcome.json", {"status": "captured_unreviewed", "coverage": observation["status"],
        "admission_eligible": False, "review_status": "not_reviewed", "no_performance_claim": True,
        "process": process, "diagnostics": artifact_counts, "profile": profile_metadata,
        "artifact_sha256": existing_artifact_hashes(output)})
    return {"status": "captured_unreviewed", "coverage": observation["status"], "diagnostics": artifact_counts}


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--binary-sha256", required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--scenario-script", type=Path, required=True)
    parser.add_argument("--build-manifest", type=Path, required=True)
    parser.add_argument("--batch-manifest", type=Path, required=True)
    parser.add_argument("--batch-worker", type=int, required=True)
    parser.add_argument("--corpus-root", type=Path, default=Path("/opt/p1/corpus"))
    parser.add_argument("--input-sha256", required=True)
    parser.add_argument("--input-manifest-sha256", required=True)
    parser.add_argument("--dry-preflight", action="store_true")
    args = parser.parse_args(argv)
    if "<PENDING_" in EXPECTED_SOURCE_COMMIT or "<PENDING_" in EXPECTED_BINARY_SHA256:
        raise SystemExit("post-fix collector pins are placeholders; refuse execution")
    if args.source_commit != EXPECTED_SOURCE_COMMIT or args.input_sha256 != EXPECTED_SOURCE_DIGEST or args.input_manifest_sha256 != EXPECTED_INPUT_MANIFEST_SHA256:
        raise SystemExit("source or corpus identity does not match post-fix diagnostics contract")
    if args.binary_sha256 != EXPECTED_BINARY_SHA256 or not re.fullmatch(r"[0-9a-fA-F]{64}", args.binary_sha256):
        raise SystemExit("binary SHA-256 does not match post-fix diagnostics contract")
    if args.dry_preflight:
        return
    unit = os.environ.get("P1_COMPLETION_SYSTEMD_UNIT", "")
    boundary = verify_runtime_boundary(args.output.parent / "runtime-boundary.json", unit)
    result = run_request(args.output, args.binary, args.scenario_script, args.corpus_root,
                         args.binary_sha256, args.source_root, args.build_manifest,
                         args.batch_manifest, args.batch_worker,
                         runtime_boundary=boundary)
    if result["status"] == "issue":
        raise SystemExit(result["issue"])


if __name__ == "__main__":
    main()
