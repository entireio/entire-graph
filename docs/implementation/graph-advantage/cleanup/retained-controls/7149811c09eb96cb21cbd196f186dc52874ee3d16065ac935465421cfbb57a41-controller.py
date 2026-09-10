#!/usr/bin/env python3
"""Prepare and, only with --execute, dispatch one bounded diagnostic request.

The default invocation is deliberately non-operational.  Execution creates a
durable local claim before any cloud upload, refuses a reused dispatch, and
downloads results only after the remote upload acknowledgement.  It never
starts a VM, adds an ON arm, compares arms, or admits a result.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import sys
import tarfile


PREP = Path(__file__).resolve().parent
GRAPH = PREP.parents[1]
REPO = GRAPH.parents[2]
P1 = GRAPH / "p1-corpus-20260905"
MANIFEST_PATH = PREP / "manifest.json"
CLAIM_PATH = PREP / "dispatch-claim.json"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def prepared_member(name: str) -> Path:
    """Resolve prep-local members and repository-relative provenance files."""
    path = Path(name)
    if path.is_absolute():
        return path
    if name.startswith("docs/"):
        return REPO / path
    return PREP / path


def read_manifest() -> dict:
    document = json.loads(MANIFEST_PATH.read_text())
    if document.get("status") != (
        "prepared only; no VM, cloud, collector, product, corpus, or benchmark execution"
    ):
        raise RuntimeError("dispatch is no longer in prepared-only state")
    if CLAIM_PATH.exists():
        raise RuntimeError(f"durable dispatch claim already exists: {CLAIM_PATH}")
    required = {
        "total_attempts": 1,
        "derived_invocations": 1,
        "preparatory_invocations": 0,
        "worker": 1,
        "cache": "off",
        "profile": "syntax-only",
        "verb": "snapshot",
        "scenario": "diagnostic",
        "no_on_arm": True,
        "no_comparison": True,
        "admission_eligible": False,
    }
    for key, expected in required.items():
        if document.get(key) != expected:
            raise RuntimeError(f"prepared manifest mismatch: {key}")
    for name in ("control_archive", "build_manifest", "batch_manifest"):
        path = prepared_member(document[name])
        if not path.is_file():
            raise RuntimeError(f"missing prepared member: {path}")
    if sha256(prepared_member(document["control_archive"])) != document["control_archive_sha256"]:
        raise RuntimeError("control archive hash changed")
    if sha256(prepared_member(document["build_manifest"])) != document["build_manifest_sha256"]:
        raise RuntimeError("build manifest hash changed")
    if sha256(prepared_member(document["batch_manifest"])) != document["batch_manifest_sha256"]:
        raise RuntimeError("batch manifest hash changed")
    return document


def remote_script(document: dict, control_url: str, result_url: str) -> str:
    q = shlex.quote
    batch = document["dispatch_id"]
    remote = document["remote_output_root"]
    source = document["remote_source_root"]
    claim = document["remote_claim_path"]
    control = remote + "/control"
    output = remote + "/output"
    runner = control + "/docs/implementation/graph-advantage/p1-corpus-20260905/full-diagnostics-collector/run_remote.py"
    scenario = control + "/docs/implementation/graph-advantage/corpus/p1_scenario.py"
    build = control + "/docs/implementation/graph-advantage/evidence/diagnostics-linux-1f20f694/manifest.json"
    batch_manifest = control + "/batch-manifest.json"
    binary = source + "/p1-evaluator"
    script = f"""set -eu
REMOTE={q(remote)}
SOURCE={q(source)}
CLAIM={q(claim)}
if systemctl list-units --type=service --state=active --no-legend 'p1-*' | grep -q .; then
  echo ACTIVE_CAMPAIGN_REFUSED
  exit 1
fi
test ! -e "$REMOTE"
test ! -e "$CLAIM"
test -x {q(binary)}
mkdir -p "$REMOTE" {q(control)}
curl -fsS {q(control_url)} -o "$REMOTE/control-files.tar.gz"
echo {q(document['control_archive_sha256'] + '  ' + remote + '/control-files.tar.gz')} | sha256sum -c - >/dev/null
tar xzf "$REMOTE/control-files.tar.gz" -C {q(control)}
(cd {q(control)} && sha256sum -c control-hashes.txt)
echo {q(document['binary_sha256'] + '  ' + binary)} | sha256sum -c - >/dev/null
CLAIM_PARENT=$(dirname "$CLAIM")
test -d /opt/p1/batch-claims
test -w /opt/p1/batch-claims
test ! -e "$CLAIM_PARENT"
mkdir "$CLAIM_PARENT"
chown graphcheck:graphcheck "$CLAIM_PARENT"
test ! -e "$CLAIM"
chown -R graphcheck:graphcheck "$REMOTE"
(uname -a; /usr/local/go/bin/go version; /usr/bin/git --version) > "$REMOTE/environment.txt"
set +e
timeout --signal=TERM --kill-after=10s {document['remote_control_timeout_seconds']}s \\
  runuser -u graphcheck -- env PATH=/usr/local/go/bin:/usr/bin:/bin \\
  GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off \\
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GOMAXPROCS=4 \\
  /usr/bin/python3 {q(runner)} \\
  --output {q(output)} --binary {q(binary)} \\
  --binary-sha256 {q(document['binary_sha256'])} \\
  --source-root {q(source)} --source-commit {q(document['source_commit'])} \\
  --scenario-script {q(scenario)} --build-manifest {q(build)} \\
  --batch-manifest {q(batch_manifest)} --batch-worker 1 \\
  --corpus-root /opt/p1/corpus \\
  --input-sha256 {q(document['source_digest'])} \\
  --input-manifest-sha256 {q(document['input_manifest_sha256'])} \\
  > "$REMOTE/collector.log" 2>&1
collector_status=$?
set -e
printf '%s\\n' "$collector_status" > "$REMOTE/collector-exit.txt"
if test -e "$CLAIM"; then cp "$CLAIM" "$REMOTE/claim-worker-1.json"; else printf '%s\\n' CLAIM_MISSING > "$REMOTE/claim-worker-1.json"; fi
    # A rejected preflight may leave the collector output absent.  Preserve
    # the log, exit status, and claim evidence instead of losing the upload to
    # tar's missing-member failure.
    if test -e "$REMOTE/output"; then
      tar czf "$REMOTE/results.tar.gz" -C "$REMOTE" control output environment.txt collector.log collector-exit.txt claim-worker-1.json
    else
      tar czf "$REMOTE/results.tar.gz" -C "$REMOTE" control environment.txt collector.log collector-exit.txt claim-worker-1.json
    fi
curl -fsS -X PUT -H 'x-ms-blob-type: BlockBlob' --upload-file "$REMOTE/results.tar.gz" {q(result_url)} >/dev/null
echo ONE_REQUEST_DIAGNOSTIC_UPLOAD_ACK
exit 0
"""
    return script


def execute() -> None:
    document = read_manifest()
    # Importing the cloud transport is delayed until the explicit execution
    # path, so preparation and local inspection can never call Azure.
    sys.path.insert(0, str(P1))
    import cloud

    claim = {
        "dispatch_id": document["dispatch_id"],
        "batch_manifest_sha256": document["batch_manifest_sha256"],
        "control_archive_sha256": document["control_archive_sha256"],
        "source_commit": document["source_commit"],
        "state": "claimed_before_cloud_upload",
    }
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
    control_url = cloud.url(control_blob, "r", env)
    result_url = cloud.url(result_blob, "cw", env)
    script = remote_script(document, control_url, result_url)
    (PREP / "dispatch-script.sha256").write_text(hashlib.sha256(script.encode()).hexdigest() + "\n")
    response = cloud.run("graph-validation-linux", script)
    (PREP / "transport.json").write_text(response + "\n")
    if "ONE_REQUEST_DIAGNOSTIC_UPLOAD_ACK" not in response:
        raise RuntimeError("missing unique result upload acknowledgement; do not retry")
    cloud.download(result_blob, PREP / "results.tar.gz", env)
    with tarfile.open(PREP / "results.tar.gz") as archive:
        archive.extractall(PREP / "raw", filter="data")
    status = int((PREP / "raw/collector-exit.txt").read_text().strip())
    document["status"] = "collected" if status == 0 else "collected_with_issue"
    document["collector_exit_code"] = status
    document["result_archive_sha256"] = sha256(PREP / "results.tar.gz")
    (PREP / "manifest.json").write_text(json.dumps(document, indent=2, sort_keys=True) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--execute", action="store_true")
    args = parser.parse_args()
    if not args.execute:
        raise SystemExit("preparation only; pass --execute only after independent review and VM preflight")
    execute()


if __name__ == "__main__":
    main()
