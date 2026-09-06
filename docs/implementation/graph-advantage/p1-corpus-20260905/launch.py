#!/usr/bin/env python3
"""Launch one versioned P1 evaluation run on the frozen workers.

The launcher deliberately performs all local identity checks before contacting Azure.
A build manifest selects both the evaluator blob and its source inventory; omitting
--build-manifest retains the historical build.json default.
"""
import argparse
import concurrent.futures
import hashlib
import json
import pathlib
import re
import shlex
import subprocess
import tarfile
import tempfile

import canary_admission
import cloud
import supervise

HERE = pathlib.Path(__file__).resolve().parent
VMS = ["graph-validation-linux", "graph-p1-worker-2", "graph-p1-worker-3"]
_DIGEST_RE = re.compile(r"^[0-9a-fA-F]{64}$")
_BLOB_RE = re.compile(r"^[A-Za-z0-9._-]+$")
_RUN_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")


def sha(path):
    h = hashlib.sha256()
    with pathlib.Path(path).open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def _inside(base, candidate, label):
    base = pathlib.Path(base).resolve()
    candidate = pathlib.Path(candidate).resolve()
    try:
        candidate.relative_to(base)
    except ValueError as exc:
        raise ValueError(f"{label} escapes its manifest boundary: {candidate}") from exc
    return candidate


def _relative_path(value, label):
    if not isinstance(value, str) or not value or pathlib.PurePosixPath(value).is_absolute():
        raise ValueError(f"{label} must be a relative path")
    path = pathlib.Path(value)
    if any(part == ".." for part in path.parts):
        raise ValueError(f"{label} may not contain '..'")
    return path


def _digest(value, label):
    if not isinstance(value, str) or not _DIGEST_RE.fullmatch(value):
        raise ValueError(f"{label} must be a lowercase or uppercase SHA-256 digest")
    return value.lower()


def _blob(value, label):
    if not isinstance(value, str) or not _BLOB_RE.fullmatch(value):
        raise ValueError(f"{label} must be a simple blob name")
    return value


def current_source_files(repo_root):
    output = subprocess.check_output(
        [
            "git",
            "ls-files",
            "--cached",
            "--others",
            "--exclude-standard",
            "--",
            "internal",
            "cmd",
            "go.mod",
            "go.sum",
        ],
        cwd=repo_root,
        text=True,
    )
    return [line for line in output.splitlines() if line]


def resolve_manifest_path(value, default=None):
    """Resolve the selected build manifest without reading or contacting Azure."""
    return pathlib.Path(value if value is not None else default).expanduser().resolve()


def load_and_validate_build_manifest(manifest_path, repo_root):
    """Load a build manifest and validate its exact source inventory locally.

    ``source_inventory_root`` is an explicit repository-relative boundary. The
    historical manifest omits it and therefore uses the repository root. Source
    inventory entries remain repository-relative because that is the frozen
    ``source-files.sha256`` contract consumed by canary_admission.
    """
    manifest_path = pathlib.Path(manifest_path).resolve()
    repo_root = pathlib.Path(repo_root).resolve()
    if not manifest_path.is_file():
        raise ValueError(f"build manifest is not a regular file: {manifest_path}")
    try:
        document = json.loads(manifest_path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read build manifest {manifest_path}: {exc}") from exc
    if not isinstance(document, dict):
        raise ValueError("build manifest must be a JSON object")

    binary_sha256 = _digest(document.get("binary_sha256"), "binary_sha256")
    binary_blob = _blob(document.get("binary_blob"), "binary_blob")
    inventory_hash = _digest(
        document.get("source_file_hash_manifest_sha256"),
        "source_file_hash_manifest_sha256",
    )
    boundary_rel = _relative_path(document.get("source_inventory_root", "."), "source_inventory_root")
    boundary = _inside(repo_root, repo_root / boundary_rel, "source_inventory_root")
    inventory_rel = _relative_path(
        document.get("source_file_hash_manifest"), "source_file_hash_manifest"
    )
    inventory_path = _inside(boundary, boundary / inventory_rel, "source_file_hash_manifest")
    if not inventory_path.is_file():
        raise ValueError(f"source inventory is not a regular file: {inventory_path}")

    files = current_source_files(repo_root)
    canary_admission.verify_build_sources(
        repo_root,
        inventory_path,
        inventory_hash,
        files,
    )
    if "source_file_count" in document and document["source_file_count"] != len(files):
        raise ValueError(
            f"source_file_count={document['source_file_count']} does not match {len(files)}"
        )

    return {
        "path": manifest_path,
        "document": document,
        "manifest_sha256": sha(manifest_path),
        "binary_sha256": binary_sha256,
        "binary_blob": binary_blob,
        "inventory_path": inventory_path,
        "inventory_sha256": inventory_hash,
        "source_files": files,
    }


def _redact_transport(value):
    text = value if isinstance(value, str) else json.dumps(value, sort_keys=True)
    # Azure command output must not put signed URLs or query credentials in local
    # evidence. Keep the URL origin/path and replace the complete query string.
    return re.sub(r"(https?://[^\s\"'?]+)\?[^\s\"']*", r"\1?<redacted-sas>", text)


def persist_transport_response(path, raw):
    """Persist redacted transport bytes before attempting JSON decoding."""
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    text = raw if isinstance(raw, str) else json.dumps(raw, sort_keys=True)
    payload = {
        "raw_response_sha256": hashlib.sha256(text.encode()).hexdigest(),
        "raw_response_redacted": _redact_transport(text),
    }
    with path.open("x") as f:
        json.dump(payload, f, sort_keys=True)
        f.write("\n")


def decode_transport_response(raw, evidence_path):
    """Retain the response first, then decode the Azure JSON envelope."""
    persist_transport_response(evidence_path, raw)
    if isinstance(raw, str):
        return json.loads(raw)
    return raw


def prepare_supervisor_output(path):
    """Create an exclusive local evidence directory before cloud mutation."""
    path = pathlib.Path(path).resolve()
    if path.exists():
        raise ValueError(f"supervisor output already exists: {path}")
    path.mkdir(parents=True)
    for index in range(1, len(VMS) + 1):
        if (path / f"launch-worker-{index}-run-command.json").exists():
            raise ValueError("launch transport evidence already exists")
    return path


def _scripts_archive(build_manifest_path, frozen_baseline):
    files = [
        (HERE / "run_campaign.py", "run_campaign.py"),
        (HERE / "verify_inputs.py", "verify_inputs.py"),
        (HERE / "expected-inputs.json", "expected-inputs.json"),
        (HERE / "campaign_gate.py", "campaign_gate.py"),
        (HERE / "worker-1.json", "worker-1.json"),
        (HERE / "worker-2.json", "worker-2.json"),
        (HERE / "worker-3.json", "worker-3.json"),
        (HERE.parent / "corpus" / "p1_scenario.py", "corpus-tools/p1_scenario.py"),
        (HERE.parent / "corpus" / "corpus-manifest.json", "corpus-tools/corpus-manifest.json"),
        (pathlib.Path(build_manifest_path), "build-manifest.json"),
    ]
    if frozen_baseline is not None:
        files.append((pathlib.Path(frozen_baseline), "frozen-baseline.json"))
    temp = tempfile.NamedTemporaryFile(prefix="p1-scripts-", suffix=".tar.gz", delete=False)
    temp.close()
    archive_path = pathlib.Path(temp.name)
    with tarfile.open(archive_path, "w:gz") as archive:
        for source, arcname in files:
            archive.add(source, arcname=arcname, recursive=False)
    return archive_path


def worker_script(
    *,
    stage,
    run_id,
    worker_index,
    script_url,
    binary_url,
    binary_sha256,
    trials,
    frozen_baseline,
):
    run_dir = f"/opt/p1/runs/{run_id}"
    scripts_dir = f"{run_dir}/scripts"
    archive_path = f"{run_dir}/scripts.tar.gz"
    binary_path = f"{run_dir}/p1-evaluator"
    unit = f"p1-{stage}-{run_id}"
    command = [
        "python3",
        f"{scripts_dir}/run_campaign.py",
        "--root",
        "/opt/p1/corpus",
        "--binary",
        binary_path,
        "--manifest",
        f"{scripts_dir}/corpus-tools/corpus-manifest.json",
        "--scenario-script",
        f"{scripts_dir}/corpus-tools/p1_scenario.py",
        "--assignment",
        f"{scripts_dir}/worker-{worker_index}.json",
        "--output",
        run_dir,
        "--stage",
        stage,
        "--require-supervisor",
        "--trials",
        str(trials),
        "--build-manifest",
        f"{scripts_dir}/build-manifest.json",
    ]
    if frozen_baseline:
        command.extend(["--frozen-baseline", f"{scripts_dir}/frozen-baseline.json"])
    command_text = " ".join(shlex.quote(str(part)) for part in command)
    return "\n".join(
        [
            "set -eu",
            f"test ! -e {shlex.quote(run_dir)}",
            "if systemctl list-units --state=active --no-legend 'p1-*.service' | grep -q .; then exit 1; fi",
            f"mkdir -p {shlex.quote(scripts_dir)}",
            f"curl --fail --silent --show-error {shlex.quote(script_url)} -o {shlex.quote(archive_path)}",
            f"tar -xzf {shlex.quote(archive_path)} -C {shlex.quote(scripts_dir)}",
            f"curl --fail --silent --show-error {shlex.quote(binary_url)} -o {shlex.quote(binary_path)}",
            f"chmod 0755 {shlex.quote(binary_path)}",
            f"chown -R graphcheck:graphcheck {shlex.quote(run_dir)}",
            f"printf '%s  %s\\n' {shlex.quote(binary_sha256)} {shlex.quote(binary_path)} | sha256sum -c -",
            f"runuser -u graphcheck -- python3 {shlex.quote(scripts_dir + '/verify_inputs.py')} --output {shlex.quote(run_dir + '/input-verification.json')}",
            supervise.lease_script(run_dir),
            f"systemd-run --unit={shlex.quote(unit)} --uid=graphcheck --collect --property=WorkingDirectory=/opt/p1 --property=MemoryMax=14G --property=TasksMax=512 --property=StandardOutput=append:{shlex.quote(run_dir + '/' + stage + '.log')} --property=StandardError=append:{shlex.quote(run_dir + '/' + stage + '.log')} -- {command_text}",
        ]
    ) + "\n"


def _identity(context, frozen_baseline=None):
    document = context["document"]
    identity = {
        "binary_sha256": context["binary_sha256"],
        "input_manifest_sha256": canary_admission.sha(HERE.parent / "corpus" / "corpus-manifest.json"),
        "runner_sha256": canary_admission.sha(HERE / "run_campaign.py"),
        "scenario_sha256": canary_admission.sha(HERE.parent / "corpus" / "p1_scenario.py"),
        "gate_sha256": canary_admission.sha(HERE / "campaign_gate.py"),
        "build_manifest_sha256": context["manifest_sha256"],
        "source_file_hash_manifest_sha256": context["inventory_sha256"],
    }
    if document.get("frozen_source_commit"):
        identity["frozen_source_commit"] = document["frozen_source_commit"]
    if frozen_baseline is not None:
        identity["frozen_baseline_sha256"] = canary_admission.sha(frozen_baseline)
    return identity


def stop_workers(stage, run_id, output):
    """Stop every worker after a startup/transport failure.

    Stop responses are retained before any interpretation, just like startup
    responses. A failed stop request is recorded and does not prevent attempts
    against the remaining workers.
    """
    output = pathlib.Path(output)
    results_dir = f"/opt/p1/runs/{run_id}"
    unit = f"p1-{stage}-{run_id}"
    reason = "launcher startup or transport failure; diagnose before retry"

    def stop(item):
        index, vm = item
        path = output / f"launch-worker-{index}-stop.json"
        try:
            raw = cloud.run(vm, supervise.stop_script(stage, reason, results_dir, unit))
            persist_transport_response(path, raw)
            return None
        except BaseException as exc:  # preserve attempts to all workers
            return f"{index}:{type(exc).__name__}"

    with concurrent.futures.ThreadPoolExecutor(max_workers=len(VMS)) as pool:
        failures = list(pool.map(stop, enumerate(VMS, 1)))
    (output / "launch-failure.json").write_text(
        json.dumps(
            {
                "reason": reason,
                "stop_failures": [failure for failure in failures if failure],
            },
            indent=2,
        )
        + "\n"
    )


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("stage", choices=["baseline", "campaign"])
    parser.add_argument("--build-manifest", type=pathlib.Path)
    parser.add_argument("--frozen-baseline", type=pathlib.Path)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--canary", action="store_true")
    parser.add_argument("--canary-results", nargs=3, type=pathlib.Path)
    parser.add_argument("--supervisor-output", type=pathlib.Path, required=True)
    args = parser.parse_args(argv)
    if not _RUN_ID_RE.fullmatch(args.run_id):
        raise ValueError("run-id must be a simple path-safe identifier")
    if args.canary and args.stage != "campaign":
        raise ValueError("canary is a campaign stage")
    if args.stage == "campaign" and not args.frozen_baseline:
        raise ValueError("campaign requires a frozen baseline manifest")

    repo_root = HERE.parents[3]
    manifest_path = resolve_manifest_path(args.build_manifest, HERE / "build.json")
    context = load_and_validate_build_manifest(manifest_path, repo_root)

    if args.stage == "campaign" and not args.canary:
        if not args.canary_results:
            raise ValueError("campaign requires --canary-results unless --canary is set")
        assignments = [
            json.loads((HERE / f"worker-{i}.json").read_text())
            for i in range(1, 4)
        ]
        admitted = canary_admission.validate(
            args.canary_results, assignments, _identity(context, args.frozen_baseline)
        )
        supervisor_output = prepare_supervisor_output(args.supervisor_output)
        (supervisor_output / "canary-admission.json").write_text(
            json.dumps(admitted, indent=2) + "\n"
        )
    else:
        supervisor_output = prepare_supervisor_output(args.supervisor_output)

    env = cloud.environment()
    archive = _scripts_archive(manifest_path, args.frozen_baseline)
    archive_digest = sha(archive)
    script_blob = f"p1-20260905-scripts-{args.run_id}-{archive_digest}.tar.gz"
    cloud.upload(archive, script_blob, env)
    script_url = cloud.url(script_blob, "r", env)
    binary_url = cloud.url(context["binary_blob"], "r", env)
    trials = 1 if args.canary else 30
    remote_run_dir = f"/opt/p1/runs/{args.run_id}"

    def start(item):
        index, vm = item
        script = worker_script(
            stage=args.stage,
            run_id=args.run_id,
            worker_index=index,
            script_url=script_url,
            binary_url=binary_url,
            binary_sha256=context["binary_sha256"],
            trials=trials,
            frozen_baseline=args.frozen_baseline,
        )
        raw = cloud.run(vm, script)
        return index, decode_transport_response(
            raw,
            supervisor_output / f"launch-worker-{index}-run-command.json",
        )

    pool = concurrent.futures.ThreadPoolExecutor(max_workers=len(VMS))
    futures = [pool.submit(start, item) for item in enumerate(VMS, 1)]
    try:
        for future in concurrent.futures.as_completed(futures):
            future.result()
    except BaseException:
        pool.shutdown(wait=False, cancel_futures=True)
        stop_workers(args.stage, args.run_id, supervisor_output)
        raise
    else:
        pool.shutdown(wait=True)
    unit = f"p1-{args.stage}-{args.run_id}"
    if not supervise.supervise(
        VMS,
        args.stage,
        supervisor_output,
        results_dir=remote_run_dir,
        unit_name=unit,
    ):
        raise SystemExit("Campaign paused; fix findings before a new run")
    if args.stage == "campaign" and args.canary:
        (supervisor_output / "launch-identities.json").write_text(
            json.dumps(_identity(context, args.frozen_baseline), indent=2, sort_keys=True) + "\n"
        )


if __name__ == "__main__":
    main()
