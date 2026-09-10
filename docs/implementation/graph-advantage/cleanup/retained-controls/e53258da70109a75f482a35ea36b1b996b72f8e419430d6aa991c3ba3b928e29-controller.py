#!/usr/bin/env python3
"""Prepare one post-fix full-profile Kubernetes diagnostic."""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import os
import re
import shlex
import subprocess
import tarfile

PREP = Path(__file__).resolve().parent
PACKAGE_ROOT = PREP.parent
REPOSITORY_ROOT = PACKAGE_ROOT.parents[4]
MANIFEST_PATH = PREP / "manifest.json"
CLAIM_PATH = PREP / "dispatch-claim.json"
PENDING = "<PENDING_"
DISPATCH_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,95}$")
HASH_RE = re.compile(r"^[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
CONTROL_MEMBERS = (
    "collector/run_remote.py", "collector/batch_budget.py", "collector/campaign_gate.py",
    "collector/cloud.py", "corpus/p1_scenario.py", "corpus/corpus-manifest.json",
    "build-manifest.json", "batch-manifest.json", "control-hashes.txt",
)
SCOPED_EQUAL_PATHS = (
    "internal", "cmd/entire-graph", "cmd/graph-bench/main.go", "scripts", "go.mod", "go.sum",
)
SCOPED_ALLOWED_DIFFERENCES = ("cmd/graph-bench/main_test.go",)
GIT_TIMEOUT_SECONDS = 10


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


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
    if gate.get("version") != 1:
        raise RuntimeError("unsupported scoped full-check gate version")
    if gate.get("evaluator_source_commit") != document.get("source_commit"):
        raise RuntimeError("scoped gate evaluator source does not match prepared source")
    if not COMMIT_RE.fullmatch(str(gate.get("full_check_source_commit", ""))):
        raise RuntimeError("scoped gate full-check source is not a commit")
    if gate.get("evaluator_binary_sha256") != document.get("binary_sha256"):
        raise RuntimeError("scoped gate binary identity does not match prepared binary")
    if gate.get("evaluator_build_manifest_sha256") != document.get("build_manifest_sha256"):
        raise RuntimeError("scoped gate build identity does not match prepared build")
    if sha256(build_path) != document.get("build_manifest_sha256"):
        raise RuntimeError("prepared build manifest hash changed")
    equal_paths = gate.get("equal_paths")
    if not isinstance(equal_paths, list) or [item.get("path") for item in equal_paths] != list(SCOPED_EQUAL_PATHS):
        raise RuntimeError("scoped gate equal-path coverage changed")
    allowed = gate.get("allowed_difference_paths")
    if not isinstance(allowed, list) or [item.get("path") for item in allowed] != list(SCOPED_ALLOWED_DIFFERENCES):
        raise RuntimeError("scoped gate allowed-difference coverage changed")
    if gate.get("expected_changed_paths") != list(SCOPED_ALLOWED_DIFFERENCES):
        raise RuntimeError("scoped gate expected source diff changed")
    evaluator_revision = gate["evaluator_source_commit"]
    full_check_revision = gate["full_check_source_commit"]
    for item in equal_paths + allowed:
        path = item["path"]
        expected_evaluator = item.get("evaluator_git_object")
        expected_full_check = item.get("full_check_git_object")
        if not re.fullmatch(r"^[0-9a-f]{40}", str(expected_evaluator)) or not re.fullmatch(r"^[0-9a-f]{40}", str(expected_full_check)):
            raise RuntimeError(f"scoped gate object identity is malformed: {path}")
        if _git_object(evaluator_revision, path) != expected_evaluator or _git_object(full_check_revision, path) != expected_full_check:
            raise RuntimeError(f"scoped source object drift: {path}")
    changed_paths = _git_changed_paths(evaluator_revision, full_check_revision)
    if changed_paths != list(SCOPED_ALLOWED_DIFFERENCES):
        raise RuntimeError("scoped source diff has unexpected paths")
    if gate.get("status") != "passed":
        raise RuntimeError(f"scoped full-check gate is not passed: {gate.get('status')}")
    result_hash = str(gate.get("full_check_result_sha256", ""))
    log_hash = str(gate.get("full_check_log_sha256", ""))
    integrity_hash = str(gate.get("full_check_integrity_sha256", ""))
    result_name = gate.get("full_check_result_path")
    log_name = gate.get("full_check_log_path")
    integrity_name = gate.get("full_check_integrity_path")
    if not HASH_RE.fullmatch(result_hash) or not HASH_RE.fullmatch(log_hash):
        raise RuntimeError("scoped full-check result or log hash is missing")
    if not isinstance(result_name, str) or not result_name or Path(result_name).is_absolute():
        raise RuntimeError("scoped full-check result path is missing or not relative")
    if not isinstance(log_name, str) or not log_name or Path(log_name).is_absolute():
        raise RuntimeError("scoped full-check log path is missing or not relative")
    if not HASH_RE.fullmatch(integrity_hash):
        raise RuntimeError("scoped full-check integrity hash is missing")
    if not isinstance(integrity_name, str) or not integrity_name or Path(integrity_name).is_absolute():
        raise RuntimeError("scoped full-check integrity path is missing or not relative")
    result_path = check_artifact_member(result_name)
    log_path = check_artifact_member(log_name)
    integrity_path = check_artifact_member(integrity_name)
    if not result_path.is_file() or sha256(result_path) != result_hash:
        raise RuntimeError("scoped full-check result artifact is missing or changed")
    if not log_path.is_file() or sha256(log_path) != log_hash:
        raise RuntimeError("scoped full-check log artifact is missing or changed")
    if not integrity_path.is_file() or sha256(integrity_path) != integrity_hash:
        raise RuntimeError("scoped full-check integrity artifact is missing or changed")
    result = _load_json(result_path, "scoped full-check result")
    if result.get("expected_head") != full_check_revision:
        raise RuntimeError("scoped full-check result expected head does not match gate")
    if result.get("command") != "MISE_JOBS=1 GOFLAGS=-p=1 GOMAXPROCS=2 GIT_TERMINAL_PROMPT=0 mise run check":
        raise RuntimeError("scoped full-check result command is not mise run check")
    if result.get("exit_code") != 0:
        raise RuntimeError("scoped full-check result did not exit successfully")
    if result.get("exception") not in (None, ""):
        raise RuntimeError("scoped full-check result contains an exception")
    if result.get("timed_out") is not False:
        raise RuntimeError("scoped full-check result is incomplete or timed out")
    if not result.get("started_epoch") or not result.get("finished_epoch"):
        raise RuntimeError("scoped full-check result lacks terminal timestamps")
    integrity = _load_json(integrity_path, "scoped full-check source integrity")
    if integrity.get("before_head") != full_check_revision or integrity.get("after_head") != full_check_revision:
        raise RuntimeError("scoped full-check source head changed")
    if integrity.get("before_status_bytes") != 0 or integrity.get("after_status_bytes") != 0:
        raise RuntimeError("scoped full-check source worktree is dirty")
    if integrity.get("tracked_source_manifest_equal") is not True:
        raise RuntimeError("scoped full-check source manifest changed")
    raw_log = result.get("raw_log")
    if raw_log is not None:
        raw_log_path = Path(raw_log)
        if not raw_log_path.is_absolute():
            raw_log_path = result_path.parent / raw_log_path
        if raw_log_path.resolve() != log_path.resolve():
            raise RuntimeError("scoped full-check result log path does not match gate")
    else:
        raise RuntimeError("scoped full-check result log path does not match gate")
    if not gate.get("full_check_evidence"):
        raise RuntimeError("scoped full-check evidence path is missing")
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


def _validate_control_archive(document: dict, archive_path: Path, batch_path: Path, build_path: Path) -> None:
    members = _archive_members(archive_path)
    missing = sorted(set(CONTROL_MEMBERS) - set(members))
    if missing:
        raise RuntimeError("control archive missing members: " + ", ".join(missing))
    extra = sorted(set(members) - set(CONTROL_MEMBERS))
    if extra:
        raise RuntimeError("control archive contains unexpected members: " + ", ".join(extra))
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


def read_manifest() -> dict:
    document = _load_json(MANIFEST_PATH, "prepared dispatch manifest")
    if document.get("status") == "execution blocked pending immutable check":
        check = document.get("immutable_check")
        if not isinstance(check, dict) or check.get("status") != "passed":
            raise RuntimeError("execution blocked pending immutable check")
    if document.get("status") != "prepared only; no VM, cloud, collector, product, corpus, or benchmark execution":
        raise RuntimeError("dispatch is no longer in prepared-only state")
    if CLAIM_PATH.exists():
        raise RuntimeError(f"durable dispatch claim already exists: {CLAIM_PATH}")
    required = {"total_attempts": 1, "derived_invocations": 1, "preparatory_invocations": 0,
                "worker": 1, "cache": "off", "profile": "full", "verb": "snapshot",
                "scenario": "diagnostic", "no_on_arm": True, "no_comparison": True,
                "admission_eligible": False}
    for key, expected in required.items():
        if document.get(key) != expected:
            raise RuntimeError(f"prepared manifest mismatch: {key}")
    for key in ("source_commit", "binary_sha256", "control_archive_sha256", "build_manifest_sha256", "batch_manifest_sha256", "cell_id"):
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
                        (batch_path, "batch manifest"), (gate_path, "scoped full-check gate")):
        if not path.is_file():
            raise RuntimeError(f"prepared {label} is missing: {path}")
    if sha256(control_path) != document["control_archive_sha256"]:
        raise RuntimeError("control archive hash changed")
    if sha256(build_path) != document["build_manifest_sha256"]:
        raise RuntimeError("build manifest hash changed")
    if sha256(batch_path) != document["batch_manifest_sha256"]:
        raise RuntimeError("batch manifest hash changed")
    build = _load_json(build_path, "build manifest")
    if build.get("source_commit") != document["source_commit"] or build.get("binary_sha256") != document["binary_sha256"] or build.get("exit_code") != 0:
        raise RuntimeError("build manifest is not bound to the prepared source/binary")
    build_root = build.get("remote_root")
    if not isinstance(build_root, str) or not build_root:
        command = build.get("test_command", "")
        match = re.search(r"(?:^|\s)cd\s+([^\s]+)\s+&&", command)
        build_root = match.group(1) if match else None
    if build_root != document.get("remote_source_root"):
        raise RuntimeError("prepared remote source root does not match build manifest")
    _validate_scoped_full_check_gate(document, gate_path, build_path)
    batch = _validate_batch(document, batch_path, build_path)
    if sha256(prepared_member("corpus/corpus-manifest.json")) != document["input_manifest_sha256"]:
        raise RuntimeError("corpus manifest hash does not match prepared input identity")
    _validate_control_archive(document, control_path, batch_path, build_path)
    document["validated_batch"] = batch
    return document


def remote_script(document: dict, control_url: str, result_url: str) -> str:
    q = shlex.quote
    remote = document["remote_output_root"]
    source = document["remote_source_root"]
    claim = document["remote_claim_path"]
    control = remote + "/control"
    output = remote + "/output"
    runner = control + "/collector/run_remote.py"
    scenario = control + "/corpus/p1_scenario.py"
    build = control + "/build-manifest.json"
    batch_manifest = control + "/batch-manifest.json"
    binary = source + "/p1-evaluator"
    return f"""set -eu
REMOTE={q(remote)}
SOURCE={q(source)}
CLAIM={q(claim)}
if systemctl list-units --type=service --state=active --no-legend 'p1-*' | grep -q .; then
  echo ACTIVE_CAMPAIGN_REFUSED
  exit 1
fi
test ! -e \"$REMOTE\"
test ! -e \"$CLAIM\"
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
(uname -a; /usr/local/go/bin/go version; /usr/bin/git --version) > \"$REMOTE/environment.txt\"
set +e
timeout --signal=TERM --kill-after=10s {document['remote_control_timeout_seconds']}s \\
  runuser -u graphcheck -- env PATH=/usr/local/go/bin:/usr/bin:/bin \\
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GIT_CONFIG_GLOBAL=/dev/null \\
  GIT_CONFIG_SYSTEM=/dev/null GOMAXPROCS=4 \\
  /usr/bin/python3 {q(runner)} --output {q(output)} --binary {q(binary)} \\
  --binary-sha256 {q(document['binary_sha256'])} --source-root {q(source)} \\
  --source-commit {q(document['source_commit'])} --scenario-script {q(scenario)} \\
  --build-manifest {q(build)} --batch-manifest {q(batch_manifest)} \\
  --batch-worker 1 --corpus-root /opt/p1/corpus \\
  --input-sha256 {q(document['source_digest'])} \\
  --input-manifest-sha256 {q(document['input_manifest_sha256'])} \\
  > \"$REMOTE/collector.log\" 2>&1
collector_status=$?
set -e
printf '%s\\n' \"$collector_status\" > \"$REMOTE/collector-exit.txt\"
if test -e \"$CLAIM\"; then cp \"$CLAIM\" \"$REMOTE/claim-worker-1.json\"; else printf '%s\\n' CLAIM_MISSING > \"$REMOTE/claim-worker-1.json\"; fi
if test -e \"$REMOTE/output\"; then
  tar czf \"$REMOTE/results.tar.gz\" -C \"$REMOTE\" control output environment.txt collector.log collector-exit.txt claim-worker-1.json
else
  tar czf \"$REMOTE/results.tar.gz\" -C \"$REMOTE\" control environment.txt collector.log collector-exit.txt claim-worker-1.json
fi
curl -fsS -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file \"$REMOTE/results.tar.gz\" {q(result_url)} >/dev/null
echo POST_FIX_FULL_PROFILE_DIAGNOSTIC_UPLOAD_ACK
exit 0
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


def execute() -> None:
    document = read_manifest()
    cloud = load_cloud_transport()
    claim = {"dispatch_id": document["dispatch_id"], "batch_manifest_sha256": document["batch_manifest_sha256"],
             "control_archive_sha256": document["control_archive_sha256"], "source_commit": document["source_commit"],
             "profile": "full", "state": "claimed_before_cloud_upload"}
    fd = os.open(CLAIM_PATH, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(claim, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    env = cloud.environment()
    control_blob = document["dispatch_id"] + "-control.tar.gz"
    result_blob = document["dispatch_id"] + "-results.tar.gz"
    cloud.upload(prepared_member(document["control_archive"]), control_blob, env)
    response = cloud.run("graph-validation-linux", remote_script(document, cloud.url(control_blob, "r", env), cloud.url(result_blob, "cw", env)))
    (PREP / "transport.json").write_text(response + "\n")
    if "POST_FIX_FULL_PROFILE_DIAGNOSTIC_UPLOAD_ACK" not in response:
        raise RuntimeError("missing unique post-fix upload acknowledgement; do not retry")
    cloud.download(result_blob, PREP / "results.tar.gz", env)
    with tarfile.open(PREP / "results.tar.gz") as archive:
        archive.extractall(PREP / "raw", filter="data")


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--execute", action="store_true")
    args = parser.parse_args(argv)
    if not args.execute:
        raise SystemExit("preparation only; pass --execute only after independent review and VM preflight")
    execute()


if __name__ == "__main__":
    main()
