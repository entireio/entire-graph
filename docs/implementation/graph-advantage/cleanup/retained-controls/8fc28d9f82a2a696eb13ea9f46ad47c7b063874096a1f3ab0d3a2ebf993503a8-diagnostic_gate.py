"""Strict validator for one non-admission diagnostic-validation-v1 gate."""
from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path
import re

HASH_RE = re.compile(r"^[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
REQUIRED_STAGES = ("affected_normal", "affected_race", "correctness", "build")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_object(path: Path, label: str) -> dict:
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise RuntimeError(f"cannot read {label}: {error}") from error
    if not isinstance(value, dict):
        raise RuntimeError(f"{label} must be a JSON object")
    return value


def evidence_member(root: Path, name: object, base: Path | None = None) -> Path:
    if not isinstance(name, str) or not name or Path(name).is_absolute():
        raise RuntimeError("diagnostic evidence path must be non-empty and relative")
    root = root.resolve()
    candidate = (base or root).resolve() / name
    try:
        resolved = candidate.resolve(strict=True)
    except OSError as error:
        raise RuntimeError(f"diagnostic evidence is missing: {name}") from error
    if not resolved.is_relative_to(root):
        raise RuntimeError(f"diagnostic evidence escapes evidence root: {name}")
    if candidate.is_symlink() or not resolved.is_file():
        raise RuntimeError(f"diagnostic evidence is not a regular non-symlink file: {name}")
    return resolved


def checked_artifact(root: Path, path_value: object, hash_value: object, label: str,
                     base: Path | None = None) -> Path:
    if not isinstance(hash_value, str) or not HASH_RE.fullmatch(hash_value):
        raise RuntimeError(f"{label} hash is malformed")
    path = evidence_member(root, path_value, base)
    if sha256(path) != hash_value:
        raise RuntimeError(f"{label} hash changed")
    return path


def _nonnegative_int(value: object, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise RuntimeError(f"{label} must be a nonnegative integer")
    return value


def _zero_int(value: object, label: str) -> None:
    if isinstance(value, bool) or not isinstance(value, int) or value != 0:
        raise RuntimeError(f"{label} must be integer zero")


def validate(manifest: dict, gate_path: Path, evidence_root: Path) -> dict:
    if manifest.get("validation_gate_kind") != "diagnostic-validation-v1":
        raise RuntimeError("unsupported validation gate kind")
    if "full_check_gate" in manifest or "full_check_gate_sha256" in manifest:
        raise RuntimeError("diagnostic and full-check gates cannot coexist")
    if manifest.get("admission_eligible") is not False or manifest.get("no_comparison") is not True:
        raise RuntimeError("diagnostic gate cannot be admission eligible or comparative")
    expected_scope = {
        "total_attempts": 1, "derived_invocations": 1,
        "preparatory_invocations": 0, "worker": 1, "cache": "off",
        "profile": "full", "verb": "snapshot", "scenario": "diagnostic",
        "no_on_arm": True,
    }
    for key, expected in expected_scope.items():
        actual = manifest.get(key)
        if isinstance(expected, int) and not isinstance(expected, bool) and isinstance(actual, bool):
            raise RuntimeError(f"diagnostic manifest scope mismatch: {key}")
        if actual != expected:
            raise RuntimeError(f"diagnostic manifest scope mismatch: {key}")
    gate_hash = manifest.get("validation_gate_sha256")
    if not isinstance(gate_hash, str) or not HASH_RE.fullmatch(gate_hash):
        raise RuntimeError("diagnostic gate hash is malformed")
    if sha256(gate_path) != gate_hash:
        raise RuntimeError("diagnostic gate hash changed")
    gate = load_object(gate_path, "diagnostic validation gate")
    constants = {
        "schema": "diagnostic-validation-v1",
        "status": "passed",
        "purpose": "single-non-admission-timeout-diagnostic",
        "required_for_execution": True,
        "admission_eligible": False,
        "maximum_product_invocations": 1,
    }
    for key, expected in constants.items():
        if gate.get(key) != expected:
            raise RuntimeError(f"diagnostic gate mismatch: {key}")
    allowed = gate.get("allowed_cell")
    expected_cell = {"cache": "off", "profile": "full", "verb": "snapshot",
                     "scenario": "diagnostic", "arms": [False],
                     "preparatory_invocations": 0, "retry_limit": 0,
                     "comparison": False}
    if allowed != expected_cell:
        raise RuntimeError("diagnostic gate cell scope changed")
    source = gate.get("evaluator_source_commit")
    if not isinstance(source, str) or not COMMIT_RE.fullmatch(source) or source != manifest.get("source_commit"):
        raise RuntimeError("diagnostic gate source mismatch")
    for gate_key, manifest_key in (
        ("source_archive_sha256", "source_archive_sha256"),
        ("evaluator_binary_sha256", "binary_sha256"),
        ("evaluator_build_manifest_sha256", "build_manifest_sha256"),
        ("input_manifest_sha256", "input_manifest_sha256"),
    ):
        value = gate.get(gate_key)
        if not isinstance(value, str) or not HASH_RE.fullmatch(value) or value != manifest.get(manifest_key):
            raise RuntimeError(f"diagnostic gate identity mismatch: {gate_key}")
    linux = gate.get("pinned_linux")
    if not isinstance(linux, dict):
        raise RuntimeError("pinned Linux evidence is missing")
    if linux.get("source_commit") != source or linux.get("source_archive_sha256") != gate["source_archive_sha256"]:
        raise RuntimeError("pinned Linux source identity mismatch")
    if linux.get("remote_root") != manifest.get("remote_source_root"):
        raise RuntimeError("pinned Linux remote root mismatch")
    linux_manifest_path = checked_artifact(evidence_root, linux.get("manifest_path"), linux.get("manifest_sha256"), "pinned Linux manifest")
    linux_manifest = load_object(linux_manifest_path, "pinned Linux manifest")
    if linux_manifest.get("source_commit") != source or linux_manifest.get("remote_root") != linux.get("remote_root"):
        raise RuntimeError("pinned Linux manifest source mismatch")
    if linux_manifest.get("source_archive_sha256") != gate["source_archive_sha256"] or linux_manifest.get("binary_sha256") != gate["evaluator_binary_sha256"]:
        raise RuntimeError("pinned Linux manifest archive or binary mismatch")
    build_manifest_path = checked_artifact(evidence_root, linux.get("build_manifest_path"),
                                           gate["evaluator_build_manifest_sha256"], "pinned Linux build manifest")
    build_manifest = load_object(build_manifest_path, "pinned Linux build manifest")
    if (build_manifest.get("source_commit") != source
            or build_manifest.get("source_archive_sha256") != gate["source_archive_sha256"]
            or build_manifest.get("remote_root") != linux.get("remote_root")
            or build_manifest.get("binary_sha256") != gate["evaluator_binary_sha256"]):
        raise RuntimeError("pinned Linux build-manifest identity mismatch")
    _zero_int(build_manifest.get("exit_code"), "pinned Linux build-manifest exit")
    required = linux.get("required_stage_ids")
    if required != list(REQUIRED_STAGES):
        raise RuntimeError("pinned Linux required-stage set changed")
    expected_stages = linux.get("expected_stages")
    if (not isinstance(expected_stages, list) or not all(isinstance(stage, dict) for stage in expected_stages)
            or [stage.get("id") for stage in expected_stages] != list(REQUIRED_STAGES)):
        raise RuntimeError("pinned Linux expected stages are missing, duplicated, or reordered")
    expected_commands = {stage["id"]: stage.get("command") for stage in expected_stages}
    if build_manifest.get("commands") != expected_commands:
        raise RuntimeError("pinned Linux build-manifest commands differ from gate")
    build_exits = build_manifest.get("stage_exit_codes")
    if not isinstance(build_exits, dict):
        raise RuntimeError("pinned Linux build-manifest stage exits are missing")
    for stage_id in REQUIRED_STAGES:
        _zero_int(build_exits.get(stage_id), f"pinned Linux build-manifest {stage_id} exit")
    results_path = checked_artifact(evidence_root, linux.get("stage_results_path"), linux.get("stage_results_sha256"), "pinned Linux stage results")
    results = load_object(results_path, "pinned Linux stage results")
    if (results.get("source_commit") != source or results.get("source_archive_sha256") != gate["source_archive_sha256"]
            or results.get("remote_root") != linux.get("remote_root") or results.get("binary_sha256") != gate["evaluator_binary_sha256"]):
        raise RuntimeError("pinned Linux stage-result identity mismatch")
    stages = results.get("stages")
    if (not isinstance(stages, list) or not all(isinstance(stage, dict) for stage in stages)
            or [stage.get("id") for stage in stages] != list(REQUIRED_STAGES)):
        raise RuntimeError("pinned Linux stage results are missing, duplicated, or reordered")
    for expected, stage in zip(expected_stages, stages):
        stage_id = stage["id"]
        command = expected.get("command")
        if not isinstance(command, str) or not command.strip() or stage.get("command") != command:
            raise RuntimeError(f"pinned Linux stage command is empty: {stage_id}")
        _zero_int(stage.get("exit_code"), f"pinned Linux {stage_id} exit")
        checked_artifact(evidence_root, stage.get("log_path"), stage.get("log_sha256"),
                         f"pinned Linux {stage_id} log", results_path.parent)
        exit_path = checked_artifact(evidence_root, stage.get("exit_path"), stage.get("exit_sha256"),
                                     f"pinned Linux {stage_id} raw exit", results_path.parent)
        if exit_path.read_bytes() != b"0\n":
            raise RuntimeError(f"pinned Linux raw exit is not zero: {stage_id}")
        matched = _nonnegative_int(stage.get("matched_tests"), f"{stage_id} matched_tests")
        passed = _nonnegative_int(stage.get("passed_tests"), f"{stage_id} passed_tests")
        named_passes = stage.get("named_passes")
        actual_skips = stage.get("actual_skips")
        allowed_skips = expected.get("allowed_skips")
        failures = stage.get("failures")
        if not isinstance(named_passes, list) or not all(isinstance(item, str) and item for item in named_passes) or len(named_passes) != passed:
            raise RuntimeError(f"pinned Linux named passes malformed: {stage_id}")
        if len(named_passes) != len(set(named_passes)):
            raise RuntimeError(f"pinned Linux named passes contain duplicates: {stage_id}")
        if not isinstance(actual_skips, list) or not all(isinstance(item, str) and item for item in actual_skips):
            raise RuntimeError(f"pinned Linux actual skips malformed: {stage_id}")
        if not isinstance(allowed_skips, list) or actual_skips != allowed_skips:
            raise RuntimeError(f"pinned Linux skips differ from allowed set: {stage_id}")
        required_tests = expected.get("required_tests")
        if not isinstance(required_tests, list) or not all(isinstance(item, str) and item for item in required_tests) or len(required_tests) != len(set(required_tests)):
            raise RuntimeError(f"pinned Linux required test names malformed: {stage_id}")
        if failures != []:
            raise RuntimeError(f"pinned Linux failures present: {stage_id}")
        if stage_id != "build" and (matched < 1 or passed < 1 or passed + len(actual_skips) != matched):
            raise RuntimeError(f"pinned Linux stage lacks required executed tests: {stage_id}")
        if stage_id != "build" and not set(required_tests).issubset(set(named_passes) | set(actual_skips)):
            raise RuntimeError(f"pinned Linux stage is missing a required test: {stage_id}")
        if stage_id == "build" and (matched != 0 or passed != 0 or named_passes or required_tests):
            raise RuntimeError("build stage must not claim test counts")
    _zero_int(linux.get("build_exit_code"), "pinned Linux build exit")
    if linux.get("binary_sha256") != gate["evaluator_binary_sha256"]:
        raise RuntimeError("pinned Linux build identity mismatch")
    required_boundaries = ["implementation-admission", "stability-sampling", "merge", "release", "quantitative-publication"]
    if gate.get("final_full_check_required_before") != required_boundaries:
        raise RuntimeError("final full-check boundary changed")
    expected_claims = {"correctness": "focused-exact-source-only", "performance": "none",
                       "stability": "none", "release": "none"}
    if gate.get("claims") != expected_claims:
        raise RuntimeError("diagnostic claims changed")
    return gate
