#!/usr/bin/env python3
"""Prepare, start and observe one completion diagnostic without a deadline."""
from __future__ import annotations

import argparse
import ast
import hashlib
import importlib.util
import json
from pathlib import Path
import os
import re
import shlex
import subprocess
import tarfile
import time

PREP = Path(__file__).resolve().parent
PACKAGE_ROOT = PREP.parent
REPOSITORY_ROOT = PACKAGE_ROOT.parents[4]
MANIFEST_PATH = PREP / "manifest.json"
CLAIM_PATH = PREP / "dispatch-claim.json"
HANDLE_PATH = PREP / "runtime-handle.json"
OBSERVATIONS_PATH = PREP / "observations"
PENDING = "<PENDING_"
DISPATCH_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,95}$")
HASH_RE = re.compile(r"^[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
CONTROL_MEMBERS = (
    "collector/run_remote.py", "collector/batch_budget.py", "collector/campaign_gate.py",
    "collector/cloud.py", "collector/observe_remote.py", "corpus/p1_scenario.py", "corpus/corpus-manifest.json",
    "build-manifest.json", "batch-manifest.json", "full-check-gate.json",
    "control-hashes.txt",
)
SCOPED_EQUAL_PATHS = (
    "internal", "cmd/entire-graph", "cmd/graph-bench/main.go", "scripts", "go.mod", "go.sum",
)
SCOPED_ALLOWED_DIFFERENCES = ("cmd/graph-bench/main_test.go",)
GIT_TIMEOUT_SECONDS = 10
EXPECTED_SYSTEMD_UNIT = "p1-diag-effa358f-k8s-completion-off-01.service"
EXPECTED_MEMORY_MAX_BYTES = 15032385536
EXPECTED_TASKS_MAX = 512
VM_ENV_NAMES = (
    "GRAPH_ADVANTAGE_VALIDATION_VM",
    "GRAPH_ADVANTAGE_WORKER_2_VM",
    "GRAPH_ADVANTAGE_WORKER_3_VM",
)
EXPECTED_FULL_CHECK_COMMANDS = (
    "[fmt] $ gofmt -s -w .",
    "[vet] $ go vet ./...",
    "[test:ci] $ go test -race -timeout 30m ./...",
    "[build] $ go build -o entire-graph ./cmd/entire-graph",
    "[test:statusline] $ sh scripts/entire-graph-statusline_test.sh",
)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def configured_vms() -> tuple[str, str, str]:
    values = tuple(os.environ.get(name, "").strip() for name in VM_ENV_NAMES)
    missing = [name for name, value in zip(VM_ENV_NAMES, values) if not value]
    if missing:
        raise RuntimeError("missing required completion diagnostic configuration: " + ", ".join(missing))
    return values


def prepared_member(name: str) -> Path:
    raw = Path(name)
    resolved = (raw if raw.is_absolute() else PACKAGE_ROOT / raw).resolve()
    if not resolved.is_relative_to(PACKAGE_ROOT.resolve()):
        raise RuntimeError(f"prepared member escapes package: {name}")
    return resolved


def check_artifact_member(name: str) -> Path:
    raw = Path(name)
    resolved = (raw if raw.is_absolute() else PACKAGE_ROOT / raw).resolve()
    evidence_root = PACKAGE_ROOT.parent.resolve()
    if not resolved.is_relative_to(evidence_root):
        raise RuntimeError(f"full-check artifact escapes evidence package: {name}")
    return resolved


def _load_json(path: Path, label: str) -> dict:
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise RuntimeError(f"cannot read {label}: {error}") from error
    if not isinstance(value, dict):
        raise RuntimeError(f"{label} must be a JSON object")
    return value


def _load_local_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load local control module: {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _git_object(revision: str, path: str) -> str:
    environment = os.environ.copy()
    environment["GIT_TERMINAL_PROMPT"] = "0"
    try:
        result = subprocess.run(
            ["git", "rev-parse", f"{revision}:{path}"],
            cwd=REPOSITORY_ROOT,
            check=True,
            capture_output=True,
            text=True,
            env=environment,
            timeout=GIT_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise RuntimeError(f"cannot resolve scoped source object {revision}:{path}: {error}") from error
    value = result.stdout.strip()
    if not re.fullmatch(r"^[0-9a-f]{40}$", value):
        raise RuntimeError(f"scoped source object is not a Git object: {revision}:{path}")
    return value


def _git_changed_paths(evaluator_revision: str, full_check_revision: str) -> list[str]:
    environment = os.environ.copy()
    environment["GIT_TERMINAL_PROMPT"] = "0"
    try:
        result = subprocess.run(
            ["git", "diff", "--name-only", evaluator_revision, full_check_revision, "--",
             "internal", "cmd", "scripts", "go.mod", "go.sum"],
            cwd=REPOSITORY_ROOT,
            check=True,
            capture_output=True,
            text=True,
            env=environment,
            timeout=GIT_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise RuntimeError(f"cannot resolve scoped source diff: {error}") from error
    return sorted(path for path in result.stdout.splitlines() if path)


def _validate_scoped_full_check_gate(document: dict, gate_path: Path, build_path: Path) -> dict:
    if sha256(gate_path) != document.get("full_check_gate_sha256"):
        raise RuntimeError("scoped full-check gate artifact hash changed")
    gate = _load_json(gate_path, "scoped full-check gate")
    if gate.get("version") != 1 or gate.get("schema") != "full-check-canonical-v1":
        raise RuntimeError("unsupported scoped full-check gate version")
    if gate.get("evaluator_source_commit") != document.get("source_commit"):
        raise RuntimeError("scoped gate evaluator source does not match prepared source")
    if not COMMIT_RE.fullmatch(str(gate.get("full_check_source_commit", ""))):
        raise RuntimeError("scoped gate full-check source is not a commit")
    if gate.get("evaluator_binary_sha256") != document.get("binary_sha256"):
        raise RuntimeError("scoped gate binary identity does not match prepared binary")
    if gate.get("evaluator_build_manifest_sha256") != document.get("build_manifest_sha256"):
        raise RuntimeError("scoped gate build identity does not match prepared build")
    if gate.get("source_archive_sha256") != document.get("source_archive_sha256") or gate.get("tracked_manifest_sha256") != document.get("tracked_manifest_sha256"):
        raise RuntimeError("scoped gate source capture identity does not match prepared source")
    if gate.get("admission_eligible") is not False or gate.get("required_for_execution") is not True:
        raise RuntimeError("scoped full-check gate execution boundary changed")
    if sha256(build_path) != document.get("build_manifest_sha256"):
        raise RuntimeError("prepared build manifest hash changed")
    equal_paths = gate.get("equal_paths")
    if not isinstance(equal_paths, list) or [item.get("path") for item in equal_paths] != list(SCOPED_EQUAL_PATHS):
        raise RuntimeError("scoped gate equal-path coverage changed")
    evaluator_revision = gate["evaluator_source_commit"]
    full_check_revision = gate["full_check_source_commit"]
    # The current evaluator and immutable full check are the same revision.  A
    # historical synthetic fixture still exercises the older scoped-difference
    # path, but a real same-revision gate must bind an empty diff explicitly.
    expected_allowed = () if evaluator_revision == full_check_revision else SCOPED_ALLOWED_DIFFERENCES
    allowed = gate.get("allowed_difference_paths")
    if not isinstance(allowed, list) or [item.get("path") for item in allowed] != list(expected_allowed):
        raise RuntimeError("scoped gate allowed-difference coverage changed")
    if gate.get("expected_changed_paths") != list(expected_allowed):
        raise RuntimeError("scoped gate expected source diff changed")
    for item in equal_paths + allowed:
        path = item["path"]
        expected_evaluator = item.get("evaluator_git_object")
        expected_full_check = item.get("full_check_git_object")
        if not re.fullmatch(r"^[0-9a-f]{40}", str(expected_evaluator)) or not re.fullmatch(r"^[0-9a-f]{40}", str(expected_full_check)):
            raise RuntimeError(f"scoped gate object identity is malformed: {path}")
        if _git_object(evaluator_revision, path) != expected_evaluator or _git_object(full_check_revision, path) != expected_full_check:
            raise RuntimeError(f"scoped source object drift: {path}")
    changed_paths = _git_changed_paths(evaluator_revision, full_check_revision)
    if changed_paths != list(expected_allowed):
        raise RuntimeError("scoped source diff has unexpected paths")
    if gate.get("status") != "passed":
        raise RuntimeError(f"scoped full-check gate is not passed: {gate.get('status')}")
    artifact_fields = {
        "canonical run": ("canonical_run_path", "canonical_run_sha256"),
        "canonical verification": ("canonical_verification_path", "canonical_verification_sha256"),
    }
    artifacts = {}
    for label, (path_field, hash_field) in artifact_fields.items():
        name, expected_hash = gate.get(path_field), str(gate.get(hash_field, ""))
        if not isinstance(name, str) or not name or Path(name).is_absolute():
            raise RuntimeError(f"scoped full-check {label} path is missing or not relative")
        if not HASH_RE.fullmatch(expected_hash):
            raise RuntimeError(f"scoped full-check {label} hash is missing")
        path = check_artifact_member(name)
        if not path.is_file() or sha256(path) != expected_hash:
            raise RuntimeError(f"scoped full-check {label} artifact is missing or changed")
        artifacts[label] = path
    run = _load_json(artifacts["canonical run"], "canonical full-check run")
    if run.get("schema_version") != "graph-advantage-evidence/v1" or run.get("run_id") != "check-effa358f-linux-full-01" or run.get("kind") != "integration":
        raise RuntimeError("canonical full-check run identity changed")
    if run.get("execution", {}).get("status") != "passed":
        raise RuntimeError("canonical full-check run is not passed")
    result = run.get("source_records", {}).get("result.json")
    provenance = run.get("source_records", {}).get("source-provenance.json")
    if not isinstance(result, dict) or not isinstance(provenance, dict):
        raise RuntimeError("canonical full-check source records are missing")
    required_result = {
        "source_commit": full_check_revision, "transport_exit": 0,
        "remote_overall_exit": 0, "mise_check_exit": 0, "full_check_pass": True,
        "tracked_file_count": 2512,
        "source_archive_sha256": gate.get("source_archive_sha256"),
        "tracked_manifest_sha256": gate.get("tracked_manifest_sha256"),
        "before_after_identity_equal": True, "retry_performed": False,
        "product_or_corpus_invocations": 0, "all_vms_deallocated": True,
        "raw_log_sha256": gate.get("full_check_log_sha256"),
    }
    for key, expected in required_result.items():
        observed = result.get(key)
        if isinstance(expected, bool):
            matches = observed is expected
        elif isinstance(expected, int):
            matches = type(observed) is int and observed == expected
        else:
            matches = observed == expected
        if not matches:
            raise RuntimeError(f"scoped full-check result mismatch: {key}")
    tasks = result.get("task_results")
    expected_tasks = {"fmt": "passed", "vet": "passed", "test_ci": "passed", "build": "passed"}
    if not isinstance(tasks, dict) or any(tasks.get(key) != value for key, value in expected_tasks.items()):
        raise RuntimeError("scoped full-check result is missing a required passed task")
    statusline = tasks.get("test_statusline")
    if not isinstance(statusline, dict) or any(type(statusline.get(field)) is not int for field in ("passed", "failed", "skipped")) or statusline != {
        "passed": 145, "failed": 0, "skipped": 3,
        "skipped_names": [
            "platform has no chflags; chmod(1) mode-match elision unasserted",
            "platform cannot make chmod fail; foreign cache directory unverified",
            "no foreign-owned writable directory available; ownership gate unverified",
        ],
    }:
        raise RuntimeError("scoped full-check statusline result or disclosed skips changed")
    if provenance.get("source_commit") != full_check_revision or provenance.get("source_archive_sha256") != gate.get("source_archive_sha256") or provenance.get("tracked_file_count") != 2512:
        raise RuntimeError("scoped full-check source provenance mismatch")
    if gate.get("ordered_task_commands") != list(EXPECTED_FULL_CHECK_COMMANDS):
        raise RuntimeError("scoped full-check gate task commands changed")
    if result.get("ordered_task_commands") != list(EXPECTED_FULL_CHECK_COMMANDS):
        raise RuntimeError("scoped full-check ordered task commands changed")
    if run.get("coverage", {}).get("task_results") != tasks:
        raise RuntimeError("canonical full-check task projection changed")
    if run.get("semantics", {}).get("equal") is not True:
        raise RuntimeError("canonical full-check source equality changed")
    verification = _load_json(artifacts["canonical verification"], "canonical verification")
    commands = verification.get("commands")
    if verification.get("schema") != "graph-advantage-canonical-verification-v1" or not isinstance(commands, list) or not any(
        item.get("command", "").startswith("python3 validate.py ") and item.get("result") == "passed" and item.get("validated_runs") == 66
        for item in commands if isinstance(item, dict)
    ):
        raise RuntimeError("canonical evidence verification is not passed")
    if gate.get("disclosed_limitations") != {"statusline_platform_skips": 3, "full_coverage_claimed": False}:
        raise RuntimeError("scoped full-check disclosed limitations changed")
    return gate


def _validate_batch(document: dict, batch_path: Path, build_path: Path) -> dict:
    budget = _load_local_module("postfix_batch_budget", prepared_member("collector/batch_budget.py"))
    scenario = prepared_member("corpus/p1_scenario.py")
    expected_identity = {
        "binary_sha256": document["binary_sha256"],
        "input_manifest_sha256": document["input_manifest_sha256"],
        "runner_sha256": sha256(prepared_member("collector/run_remote.py")),
        "scenario_sha256": sha256(scenario),
        "gate_sha256": sha256(prepared_member("collector/campaign_gate.py")),
        "budget_sha256": sha256(prepared_member("collector/batch_budget.py")),
        "build_manifest_sha256": sha256(build_path),
    }
    try:
        batch = budget.load(batch_path, expected_identity)
    except Exception as error:
        raise RuntimeError(f"batch manifest validation failed: {error}") from error
    if batch["batch_id"] != document["dispatch_id"]:
        raise RuntimeError("batch id does not match dispatch id")
    if batch["scope"] != "bounded-diagnostic" or batch["derived_invocations"] != 1 or len(batch["cells"]) != 1:
        raise RuntimeError("batch must contain exactly one derived invocation")
    cell = batch["cells"][0]
    expected_cell = {"worker": 1, "repository": document["repository"], "profile": "full",
                     "verb": "snapshot", "scenario": "diagnostic", "trial": 0,
                     "arms": [False], "preparatory_invocations": 0}
    for key, expected in expected_cell.items():
        if cell.get(key) != expected:
            raise RuntimeError(f"batch cell mismatch: {key}")
    if document.get("cell_id") != cell["cell_id"]:
        raise RuntimeError("prepared cell_id does not match canonical batch cell")
    if batch["workers"] != {"1": 1}:
        raise RuntimeError("batch worker allocation is not exactly one invocation")
    return batch


def _archive_members(path: Path) -> dict[str, bytes]:
    try:
        with tarfile.open(path, "r:gz") as archive:
            members = archive.getmembers()
            names = [member.name for member in members]
            if len(names) != len(set(names)):
                raise RuntimeError("control archive contains duplicate members")
            result = {}
            for member in members:
                member_path = Path(member.name)
                if member_path.is_absolute() or ".." in member_path.parts:
                    raise RuntimeError(f"control archive path escapes package: {member.name}")
                if not member.isfile():
                    raise RuntimeError(f"control archive member is not a regular file: {member.name}")
                stream = archive.extractfile(member)
                if stream is None:
                    raise RuntimeError(f"control archive member cannot be read: {member.name}")
                result[member.name] = stream.read()
            return result
    except (OSError, tarfile.TarError) as error:
        raise RuntimeError(f"cannot read control archive: {error}") from error


def _validate_archived_runtime_pins(document: dict, members: dict[str, bytes]) -> None:
    """Bind the code that will execute remotely to the prepared identities."""
    try:
        tree = ast.parse(members["collector/run_remote.py"].decode("utf-8"))
    except (KeyError, UnicodeDecodeError, SyntaxError) as error:
        raise RuntimeError(f"cannot inspect archived collector runtime pins: {error}") from error
    pins = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            name = node.targets[0].id
            if name in {
                "EXPECTED_SOURCE_COMMIT", "EXPECTED_BINARY_SHA256",
                "RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS",
                "EXPECTED_SYSTEMD_UNIT", "EXPECTED_MEMORY_MAX_BYTES",
                "EXPECTED_TASKS_MAX",
            }:
                try:
                    pins[name] = ast.literal_eval(node.value)
                except (ValueError, TypeError) as error:
                    raise RuntimeError(f"archived collector runtime pin is not a literal: {name}") from error
    expected = {
        "EXPECTED_SOURCE_COMMIT": document["source_commit"],
        "EXPECTED_BINARY_SHA256": document["binary_sha256"],
        "RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS":
            document["cpu_profile"]["requested_start_after_ns"],
        "EXPECTED_SYSTEMD_UNIT": document["systemd_service"]["unit"],
        "EXPECTED_MEMORY_MAX_BYTES": document["systemd_service"]["memory_max_bytes"],
        "EXPECTED_TASKS_MAX": document["systemd_service"]["tasks_max"],
    }
    for name, value in expected.items():
        actual = pins.get(name)
        if type(actual) is not type(value):
            raise RuntimeError(f"archived collector runtime pin has the wrong type: {name}")
        if isinstance(actual, str) and "<PENDING_" in actual:
            raise RuntimeError(f"archived collector runtime pin is missing or placeholder: {name}")
        if actual != value:
            raise RuntimeError(f"archived collector runtime pin does not match prepared identity: {name}")
    runner = members["collector/run_remote.py"].decode("utf-8")
    if "TIMEOUT_SECONDS =" in runner or "GO_TEST_TIMEOUT_SECONDS" in runner:
        raise RuntimeError("archived collector retains an elapsed product deadline")
    if '"-test.timeout=0"' not in runner or "process.wait()" not in runner:
        raise RuntimeError("archived collector does not explicitly disable the Go test deadline")


def _validate_control_archive(document: dict, archive_path: Path, batch_path: Path, build_path: Path) -> None:
    members = _archive_members(archive_path)
    missing = sorted(set(CONTROL_MEMBERS) - set(members))
    if missing:
        raise RuntimeError("control archive missing members: " + ", ".join(missing))
    extra = sorted(set(members) - set(CONTROL_MEMBERS))
    if extra:
        raise RuntimeError("control archive contains unexpected members: " + ", ".join(extra))
    _validate_archived_runtime_pins(document, members)
    listed = {}
    for line in members["control-hashes.txt"].decode("ascii").splitlines():
        fields = line.split()
        if len(fields) != 2 or not HASH_RE.fullmatch(fields[0]):
            raise RuntimeError("malformed control-hashes.txt")
        if fields[1] in listed:
            raise RuntimeError(f"duplicate control hash entry: {fields[1]}")
        listed[fields[1]] = fields[0]
    if set(listed) != set(CONTROL_MEMBERS) - {"control-hashes.txt"}:
        raise RuntimeError("control-hashes.txt does not enumerate exactly the control members")
    for name, expected in listed.items():
        if hashlib.sha256(members[name]).hexdigest() != expected:
            raise RuntimeError(f"control member hash mismatch: {name}")
    for name in CONTROL_MEMBERS:
        if name == "control-hashes.txt":
            continue
        local = prepared_member(name)
        if not local.is_file() or local.read_bytes() != members[name]:
            raise RuntimeError(f"control archive differs from prepared member: {name}")
    if hashlib.sha256(members["build-manifest.json"]).hexdigest() != document["build_manifest_sha256"]:
        raise RuntimeError("archived build manifest hash mismatch")
    if hashlib.sha256(members["batch-manifest.json"]).hexdigest() != document["batch_manifest_sha256"]:
        raise RuntimeError("archived batch manifest hash mismatch")


def _validate_cpu_profile_contract(document: dict) -> None:
    expected = {
        "enabled_env": "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE=1",
        "start_after_env":
            "ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE_START_AFTER_NS=88000000000",
        "requested_start_after_ns": 88_000_000_000,
        "window_ns": 20_000_000_000,
        "latest_start_ns": 90_000_000_000,
        "max_bytes": 8_388_608,
        "profile_name": "relations-cpu.pprof",
        "status_name": "relations-cpu.pprof.json",
    }
    observed = document.get("cpu_profile")
    if not isinstance(observed, dict) or set(observed) != set(expected):
        raise RuntimeError("prepared manifest CPU profile delay contract changed")
    for key, expected_value in expected.items():
        actual = observed.get(key)
        if type(actual) is not type(expected_value) or actual != expected_value:
            raise RuntimeError("prepared manifest CPU profile delay contract changed")


def _validate_systemd_contract(document: dict) -> None:
    expected = {
        "unit": EXPECTED_SYSTEMD_UNIT,
        "memory_max_bytes": EXPECTED_MEMORY_MAX_BYTES,
        "tasks_max": EXPECTED_TASKS_MAX,
        "private_network": True,
        "restrict_address_families": ["AF_UNIX"],
        "kill_mode": "control-group",
        "oom_policy": "kill",
        "runtime_max_usec": "infinity",
        "restart": "no",
        "remain_after_exit": True,
    }
    observed = document.get("systemd_service")
    if not isinstance(observed, dict) or set(observed) != set(expected):
        raise RuntimeError("prepared manifest systemd service contract changed")
    for key, expected_value in expected.items():
        actual = observed.get(key)
        if type(actual) is not type(expected_value) or actual != expected_value:
            raise RuntimeError(f"prepared manifest systemd service contract changed: {key}")


def read_manifest(*, require_unclaimed=True) -> dict:
    document = _load_json(MANIFEST_PATH, "prepared dispatch manifest")
    if document.get("status") != "prepared only; no VM, cloud, collector, product, corpus, or benchmark execution":
        raise RuntimeError("dispatch is no longer in prepared-only state")
    if require_unclaimed and CLAIM_PATH.exists():
        raise RuntimeError(f"durable dispatch claim already exists: {CLAIM_PATH}")
    required = {"total_attempts": 1, "derived_invocations": 1, "preparatory_invocations": 0,
                "worker": 1, "cache": "off", "profile": "full", "verb": "snapshot",
                "scenario": "diagnostic", "no_on_arm": True, "no_comparison": True,
                "admission_eligible": False, "profiler_enabled": True,
                "product_invocations": 0, "reserved_product_invocations": 0,
                "retry_limit": 0, "product_deadline_seconds": None,
                "product_timeout_mode": "none-user-authorized-completion"}
    for key, expected in required.items():
        if document.get(key) != expected:
            raise RuntimeError(f"prepared manifest mismatch: {key}")
    _validate_cpu_profile_contract(document)
    _validate_systemd_contract(document)
    for key in ("source_commit", "binary_sha256", "control_archive_sha256", "build_manifest_sha256", "batch_manifest_sha256", "cell_id", "full_check_gate_sha256"):
        if PENDING in str(document.get(key, "")):
            raise RuntimeError(f"post-fix manifest still has a placeholder: {key}")
    if not isinstance(document.get("dispatch_id"), str) or not DISPATCH_ID_RE.fullmatch(document["dispatch_id"]):
        raise RuntimeError("dispatch_id is not path-safe")
    if not HASH_RE.fullmatch(document["binary_sha256"]):
        raise RuntimeError("binary_sha256 is not a SHA-256")
    control_path = prepared_member(document["control_archive"])
    build_path = prepared_member(document["build_manifest"])
    batch_path = prepared_member(document["batch_manifest"])
    gate_path = prepared_member(document.get("full_check_gate", ""))
    for path, label in ((control_path, "control archive"), (build_path, "build manifest"),
                        (batch_path, "batch manifest"), (gate_path, "archive-native full-check gate")):
        if not path.is_file():
            raise RuntimeError(f"prepared {label} is missing: {path}")
    if sha256(control_path) != document["control_archive_sha256"]:
        raise RuntimeError("control archive hash changed")
    if sha256(build_path) != document["build_manifest_sha256"]:
        raise RuntimeError("build manifest hash changed")
    if sha256(batch_path) != document["batch_manifest_sha256"]:
        raise RuntimeError("batch manifest hash changed")
    build = _load_json(build_path, "build manifest")
    if build.get("source_commit") != document["source_commit"] or build.get("binary_sha256") != document["binary_sha256"] or type(build.get("exit_code")) is not int or build.get("exit_code") != 0:
        raise RuntimeError("build manifest is not bound to the prepared source/binary")
    expected_build = {
        "source_archive_sha256": document["source_archive_sha256"],
        "tracked_manifest_sha256": document["tracked_manifest_sha256"],
        "tracked_file_count": 2512,
        "product_invocations": 0,
        "corpus_invocations": 0,
        "retry_performed": False,
        "all_vms_deallocated": True,
    }
    for key, expected in expected_build.items():
        observed = build.get(key)
        if isinstance(expected, bool):
            matches = observed is expected
        elif isinstance(expected, int):
            matches = type(observed) is int and observed == expected
        else:
            matches = observed == expected
        if not matches:
            raise RuntimeError(f"build manifest source binding mismatch: {key}")
    build_root = build.get("remote_root")
    if not isinstance(build_root, str) or not build_root:
        command = build.get("test_command", "")
        match = re.search(r"(?:^|\s)cd\s+([^\s]+)\s+&&", command)
        build_root = match.group(1) if match else None
    if build_root != document.get("remote_source_root"):
        raise RuntimeError("prepared remote source root does not match build manifest")
    if document.get("validation_gate_kind") != "full-check-canonical-v1":
        raise RuntimeError("unsupported validation gate kind")
    _validate_scoped_full_check_gate(document, gate_path, build_path)
    batch = _validate_batch(document, batch_path, build_path)
    if sha256(prepared_member("corpus/corpus-manifest.json")) != document["input_manifest_sha256"]:
        raise RuntimeError("corpus manifest hash does not match prepared input identity")
    _validate_control_archive(document, control_path, batch_path, build_path)
    document["validated_batch"] = batch
    document["validated_gate"] = {"kind": document["validation_gate_kind"],
                                  "sha256": document["full_check_gate_sha256"],
                                  "admission_eligible": False}
    return document


def remote_start_script(document: dict, control_url: str) -> str:
    q = shlex.quote
    remote = document["remote_output_root"]
    source = document["remote_source_root"]
    claim = document["remote_claim_path"]
    control = remote + "/control"
    runner = control + "/collector/run_remote.py"
    scenario = control + "/corpus/p1_scenario.py"
    build = control + "/build-manifest.json"
    batch_manifest = control + "/batch-manifest.json"
    binary = source + "/p1-evaluator"
    service = document["systemd_service"]
    unit = service["unit"]
    return f"""set -eu
REMOTE={q(remote)}
SOURCE={q(source)}
CLAIM={q(claim)}
UNIT={q(unit)}
if systemctl list-units --type=service --state=activating,active,deactivating --no-legend 'p1-*' | grep -q .; then
  echo ACTIVE_CAMPAIGN_REFUSED
  exit 1
fi
test ! -e \"$REMOTE\"
test ! -e \"$CLAIM\"
test \"$(systemctl show \"$UNIT\" --property=LoadState --value 2>/dev/null || true)\" = not-found
test -x {q(binary)}
mkdir -p \"$REMOTE\" {q(control)}
curl -fsS {q(control_url)} -o \"$REMOTE/control-files.tar.gz\"
echo {q(document['control_archive_sha256'] + '  ' + remote + '/control-files.tar.gz')} | sha256sum -c - >/dev/null
tar xzf \"$REMOTE/control-files.tar.gz\" -C {q(control)}
(cd {q(control)} && sha256sum -c control-hashes.txt)
echo {q(document['binary_sha256'] + '  ' + binary)} | sha256sum -c - >/dev/null
CLAIM_PARENT=$(dirname \"$CLAIM\")
test -d /opt/p1/batch-claims
test -w /opt/p1/batch-claims
test ! -e \"$CLAIM_PARENT\"
mkdir \"$CLAIM_PARENT\"
chown graphcheck:graphcheck \"$CLAIM_PARENT\"
chown -R graphcheck:graphcheck \"$REMOTE\"
(uname -a; /usr/local/go/bin/go version; /usr/bin/git --version; systemd --version) > \"$REMOTE/environment.txt\"
systemd-run --unit=\"$UNIT\" --no-block --service-type=exec \\
  --uid=graphcheck --gid=graphcheck \\
  --property=MemoryAccounting=yes \\
  --property=MemoryMax={service['memory_max_bytes']} \\
  --property=TasksAccounting=yes \\
  --property=TasksMax={service['tasks_max']} \\
  --property=PrivateNetwork=yes \\
  --property=RestrictAddressFamilies=AF_UNIX \\
  --property=NoNewPrivileges=yes \\
  --property=KillMode=control-group \\
  --property=OOMPolicy=kill \\
  --property=RuntimeMaxSec=infinity \\
  --property=Restart=no \\
  --property=RemainAfterExit=yes \\
  --property=StandardOutput=append:{q(remote + '/collector.log')} \\
  --property=StandardError=append:{q(remote + '/collector.log')} \\
  --property=WorkingDirectory={q(remote)} \\
  /usr/bin/env PATH=/usr/local/go/bin:/usr/bin:/bin \\
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off GIT_CONFIG_GLOBAL=/dev/null \\
  GIT_CONFIG_SYSTEM=/dev/null GOMAXPROCS=4 \\
  P1_COMPLETION_SYSTEMD_UNIT=\"$UNIT\" \\
  /usr/bin/python3 {q(runner)} --output {q(remote + '/output')} --binary {q(binary)} \\
  --binary-sha256 {q(document['binary_sha256'])} --source-root {q(source)} \\
  --source-commit {q(document['source_commit'])} --scenario-script {q(scenario)} \\
  --build-manifest {q(build)} --batch-manifest {q(batch_manifest)} \\
  --batch-worker 1 --corpus-root /opt/p1/corpus \\
  --input-sha256 {q(document['source_digest'])} \\
  --input-manifest-sha256 {q(document['input_manifest_sha256'])}
systemctl show \"$UNIT\" --no-pager > \"$REMOTE/start-unit.properties\"
echo COMPLETION_DIAGNOSTIC_START_ACK:\"$UNIT\"
"""


def remote_observe_script(document: dict, result_url: str) -> str:
    q = shlex.quote
    remote = document["remote_output_root"]
    claim = document["remote_claim_path"]
    unit = document["systemd_service"]["unit"]
    observer = remote + "/control/collector/observe_remote.py"
    return f"""set -eu
REMOTE={q(remote)}
CLAIM={q(claim)}
UNIT={q(unit)}
test -d \"$REMOTE/control\"
/usr/bin/python3 {q(observer)} --remote-root \"$REMOTE\" --unit \"$UNIT\" > \"$REMOTE/unit-status-latest.json\"
echo COMPLETION_STATUS_JSON_BEGIN
cat \"$REMOTE/unit-status-latest.json\"
echo COMPLETION_STATUS_JSON_END
LIFECYCLE=$(/usr/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["lifecycle"])' \"$REMOTE/unit-status-latest.json\")
if test \"$LIFECYCLE\" = terminal; then
  if test -e \"$CLAIM\"; then cp \"$CLAIM\" \"$REMOTE/claim-worker-1.json\"; else printf '%s\\n' CLAIM_MISSING > \"$REMOTE/claim-worker-1.json\"; fi
  /usr/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("properties",{{}}).get("ExecMainStatus","unknown"))' \"$REMOTE/unit-status-latest.json\" > \"$REMOTE/collector-exit.txt\"
  set -- control environment.txt start-unit.properties unit-status-latest.json collector-exit.txt claim-worker-1.json
  test ! -e \"$REMOTE/collector.log\" || set -- \"$@\" collector.log
  test ! -e \"$REMOTE/runtime-boundary.json\" || set -- \"$@\" runtime-boundary.json
  test ! -d \"$REMOTE/output\" || set -- \"$@\" output
  tar czf \"$REMOTE/results.tar.gz\" -C \"$REMOTE\" \"$@\"
  curl -fsS -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file \"$REMOTE/results.tar.gz\" {q(result_url)} >/dev/null
  echo COMPLETION_DIAGNOSTIC_TERMINAL_UPLOAD_ACK:\"$UNIT\"
elif test \"$LIFECYCLE\" = running; then
  echo COMPLETION_DIAGNOSTIC_RUNNING_ACK:\"$UNIT\"
else
  echo COMPLETION_DIAGNOSTIC_UNKNOWN_ACK:\"$UNIT\"
fi
"""


def load_cloud_transport(path: Path | None = None):
    path = Path(path or (PACKAGE_ROOT / "collector/cloud.py")).resolve()
    if not path.is_file():
        raise RuntimeError(f"packaged cloud transport is missing: {path}")
    spec = importlib.util.spec_from_file_location("p1_postfix_cloud", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load packaged cloud transport: {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _write_json(path: Path, value: dict) -> None:
    path = Path(path)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("w") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def _create_dispatch_claim(document: dict) -> dict:
    claim = {
        "dispatch_id": document["dispatch_id"],
        "batch_manifest_sha256": document["batch_manifest_sha256"],
        "control_archive_sha256": document["control_archive_sha256"],
        "source_commit": document["source_commit"],
        "profile": "full",
        "state": "claimed_before_cloud_upload",
        "validation_gate_kind": document["validation_gate_kind"],
        "validation_gate_sha256": document["full_check_gate_sha256"],
        "admission_eligible": False,
        "maximum_product_invocations": 1,
        "retry_limit": 0,
        "product_deadline_seconds": None,
    }
    fd = os.open(CLAIM_PATH, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(claim, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    return claim


def _deallocate_all(cloud, env) -> dict:
    all_vms = configured_vms()
    errors = []
    for vm in all_vms:
        try:
            cloud.deallocate(vm, env)
        except Exception as error:
            errors.append(f"{vm}: {error}")
    states = cloud.states(env)
    record = {"states": states, "errors": errors}
    _write_json(PREP / "vm-terminal-final.json", record)
    if errors or set(states) != set(all_vms) or any(value != "VM deallocated" for value in states.values()):
        raise RuntimeError("not all completion diagnostic VMs are deallocated")
    return record


def _read_handle(document: dict) -> dict:
    handle = _load_json(HANDLE_PATH, "completion runtime handle")
    expected = {
        "dispatch_id": document["dispatch_id"],
        "unit": document["systemd_service"]["unit"],
        "remote_output_root": document["remote_output_root"],
        "source_commit": document["source_commit"],
        "binary_sha256": document["binary_sha256"],
        "batch_manifest_sha256": document["batch_manifest_sha256"],
        "control_archive_sha256": document["control_archive_sha256"],
    }
    for key, value in expected.items():
        if handle.get(key) != value:
            raise RuntimeError(f"completion runtime handle mismatch: {key}")
    return handle


def _status_from_response(response: str) -> dict:
    begin, end = "COMPLETION_STATUS_JSON_BEGIN", "COMPLETION_STATUS_JSON_END"
    if response.count(begin) != 1 or response.count(end) != 1:
        raise RuntimeError("completion observation has no unique status payload")
    payload = response.split(begin, 1)[1].split(end, 1)[0].strip()
    try:
        value = json.loads(payload)
    except json.JSONDecodeError as error:
        raise RuntimeError("completion observation status is not JSON") from error
    if not isinstance(value, dict) or value.get("schema") != "completion-observation-v1":
        raise RuntimeError("completion observation status has the wrong schema")
    return value


def _extract_results(archive_path: Path, destination: Path) -> None:
    if destination.exists():
        raise RuntimeError("completion result extraction directory already exists")
    with tarfile.open(archive_path, "r:gz") as archive:
        members = archive.getmembers()
        names = [member.name for member in members]
        if len(names) != len(set(names)):
            raise RuntimeError("completion result archive has duplicate members")
        for member in members:
            name = Path(member.name)
            if name.is_absolute() or ".." in name.parts or member.issym() or member.islnk():
                raise RuntimeError("completion result archive has unsafe members")
        archive.extractall(destination, filter="data")


def execute(cloud_module=None) -> dict:
    document = read_manifest()
    cloud = cloud_module or load_cloud_transport()
    _create_dispatch_claim(document)
    handle = {
        "schema": "completion-runtime-handle-v1",
        "dispatch_id": document["dispatch_id"],
        "unit": document["systemd_service"]["unit"],
        "remote_output_root": document["remote_output_root"],
        "source_commit": document["source_commit"],
        "binary_sha256": document["binary_sha256"],
        "batch_manifest_sha256": document["batch_manifest_sha256"],
        "control_archive_sha256": document["control_archive_sha256"],
        "state": "claimed",
        "product_deadline_seconds": None,
        "retry_permitted": False,
    }
    _write_json(HANDLE_PATH, handle)
    env = cloud.environment()
    all_vms = configured_vms()
    validation_vm = all_vms[0]
    states = cloud.states(env)
    if set(states) != set(all_vms) or any(value != "VM deallocated" for value in states.values()):
        raise RuntimeError("completion diagnostic requires all three VMs initially deallocated")
    remote_start_maybe = False
    try:
        cloud.start(validation_vm, env)
        handle["state"] = "vm_started"
        _write_json(HANDLE_PATH, handle)
        control_blob = document["dispatch_id"] + "-control.tar.gz"
        cloud.upload(prepared_member(document["control_archive"]), control_blob, env)
        handle["state"] = "start_call_issued"
        _write_json(HANDLE_PATH, handle)
        remote_start_maybe = True
        response = cloud.run(
            validation_vm,
            remote_start_script(document, cloud.url(control_blob, "r", env)),
        )
        (PREP / "start-transport.json").write_text(response + "\n")
        acknowledgement = "COMPLETION_DIAGNOSTIC_START_ACK:" + document["systemd_service"]["unit"]
        if acknowledgement not in response:
            handle["state"] = "start_unknown"
            _write_json(HANDLE_PATH, handle)
            raise RuntimeError("completion service start acknowledgement missing; observe exact handle and never replay")
        handle["state"] = "start_acknowledged"
        _write_json(HANDLE_PATH, handle)
        return handle
    except Exception:
        if not remote_start_maybe:
            _deallocate_all(cloud, env)
        raise


def observe(cloud_module=None) -> dict:
    document = read_manifest(require_unclaimed=False)
    if not CLAIM_PATH.is_file():
        raise RuntimeError("completion dispatch claim is missing; observation cannot start work")
    handle = _read_handle(document)
    cloud = cloud_module or load_cloud_transport()
    env = cloud.environment()
    validation_vm = configured_vms()[0]
    result_blob = document["dispatch_id"] + "-results.tar.gz"
    response = cloud.run(
        validation_vm,
        remote_observe_script(document, cloud.url(result_blob, "cw", env)),
    )
    OBSERVATIONS_PATH.mkdir(exist_ok=True)
    sequence = len(list(OBSERVATIONS_PATH.glob("observation-*.transport.json"))) + 1
    transport_path = OBSERVATIONS_PATH / f"observation-{sequence:04d}.transport.json"
    transport_path.write_text(response + "\n")
    status = _status_from_response(response)
    _write_json(OBSERVATIONS_PATH / f"observation-{sequence:04d}.json", status)
    lifecycle = status.get("lifecycle")
    if lifecycle == "running":
        handle["state"] = "running"
        handle["last_observation"] = sequence
        _write_json(HANDLE_PATH, handle)
        return status
    if lifecycle != "terminal":
        handle["state"] = "unknown_never_replay"
        handle["last_observation"] = sequence
        _write_json(HANDLE_PATH, handle)
        return status
    acknowledgement = "COMPLETION_DIAGNOSTIC_TERMINAL_UPLOAD_ACK:" + document["systemd_service"]["unit"]
    if acknowledgement not in response:
        handle["state"] = "terminal_collection_pending"
        handle["last_observation"] = sequence
        _write_json(HANDLE_PATH, handle)
        raise RuntimeError("completion service is terminal but result upload is not acknowledged")
    archive_path = PREP / "results.tar.gz"
    try:
        cloud.download(result_blob, archive_path, env)
        _extract_results(archive_path, PREP / "raw")
    finally:
        _deallocate_all(cloud, env)
    handle["state"] = "terminal_collected"
    handle["last_observation"] = sequence
    handle["terminal_success"] = status.get("terminal_success") is True
    _write_json(HANDLE_PATH, handle)
    return status


def main(argv=None):
    parser = argparse.ArgumentParser()
    actions = parser.add_mutually_exclusive_group()
    actions.add_argument("--execute", action="store_true")
    actions.add_argument("--observe", action="store_true")
    args = parser.parse_args(argv)
    if args.execute:
        print(json.dumps(execute(), sort_keys=True))
        return
    if args.observe:
        print(json.dumps(observe(), sort_keys=True))
        return
    document = read_manifest()
    print(json.dumps({
        "status": "preflight passed; no claim created",
        "dispatch_id": document["dispatch_id"],
        "reserved_product_invocations": document["reserved_product_invocations"],
        "product_deadline_seconds": None,
        "systemd_unit": document["systemd_service"]["unit"],
    }, sort_keys=True))


if __name__ == "__main__":
    main()
