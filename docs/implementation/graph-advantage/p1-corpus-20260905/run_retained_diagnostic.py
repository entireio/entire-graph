#!/usr/bin/env python3
"""Run exactly one retained Kubernetes syntax-only diagnostic on one VM.

This is deliberately independent of the P1 campaign runner.  It stages a
frozen source archive, builds one Linux test binary, runs the fail-fast
OFF/ON diagnostic once, verifies the fixture before and after, and uploads the
raw result archive before downloading it locally.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import pathlib
import re
import shlex
import subprocess
import tarfile
import tempfile
from typing import Any, Callable

import cloud
from collect import download_immutable, require_upload_ack

HERE = pathlib.Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[3]
CORPUS_ROOT = HERE.parent / "corpus"
EXPECTED_INPUTS = HERE / "expected-inputs.json"
DIAGNOSTIC_SOURCE = HERE / "retained-search-diagnostic.go.txt"
SCENARIO_SOURCE = CORPUS_ROOT / "p1_scenario.py"
MANIFEST_SOURCE = CORPUS_ROOT / "corpus-manifest.json"
DEFAULT_VM = "graph-validation-linux"
REPOSITORY = "kubernetes-kubernetes"
REMOTE_PARENT = "/opt/p1/retained-diagnostics"
RUN_ID_RE = re.compile(r"[a-z0-9][a-z0-9-]{0,63}\Z")
HARD_TIMEOUT_SECONDS = 300


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def resolve_commit(source_root: pathlib.Path, requested: str) -> str:
    if not requested or requested.startswith("-"):
        raise ValueError("--source-commit must name a frozen commit")
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--verify", "--end-of-options", requested + "^{commit}"],
            cwd=source_root, check=True, capture_output=True, text=True,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise ValueError(f"cannot resolve frozen source commit: {requested}") from exc
    return result.stdout.strip()


def expected_corpus_digest() -> str:
    data = json.loads(EXPECTED_INPUTS.read_text())
    try:
        return data[REPOSITORY]["effective_tracked_input_sha256"]
    except (KeyError, TypeError) as exc:
        raise ValueError("expected-inputs.json lacks the Kubernetes fingerprint") from exc


def _add_path(archive: tarfile.TarFile, path: pathlib.Path, arcname: str) -> None:
    info = archive.gettarinfo(str(path), arcname=arcname)
    if info.isfile():
        with path.open("rb") as stream:
            archive.addfile(info, stream)
    else:
        archive.addfile(info)


def build_source_archive(source_root: pathlib.Path, commit: str, destination: pathlib.Path,
                         metadata: dict[str, Any]) -> None:
    """Copy only committed product inputs plus the retained harness/helpers."""
    with tempfile.TemporaryDirectory(prefix="retained-diagnostic-archive-") as temporary:
        source_tar = pathlib.Path(temporary) / "source.tar"
        subprocess.run(
            ["git", "archive", "--format=tar", "--output", str(source_tar), commit,
             "internal", "cmd", "go.mod", "go.sum"],
            cwd=source_root, check=True,
        )
        with tarfile.open(source_tar, "r:") as source, tarfile.open(destination, "w:gz") as output:
            for member in source:
                stream = source.extractfile(member) if member.isfile() else None
                output.addfile(member, stream)
                if stream is not None:
                    stream.close()
            _add_path(output, DIAGNOSTIC_SOURCE,
                      "internal/sem/retained_search_diagnostic_test.go")
            _add_path(output, SCENARIO_SOURCE, "corpus-tools/p1_scenario.py")
            _add_path(output, MANIFEST_SOURCE, "corpus-tools/corpus-manifest.json")
            payload = json.dumps(metadata, indent=2, sort_keys=True).encode() + b"\n"
            info = tarfile.TarInfo("request-metadata.json")
            info.size = len(payload)
            info.mode = 0o600
            output.addfile(info, __import__("io").BytesIO(payload))


def _q(value: str | pathlib.Path) -> str:
    return shlex.quote(str(value))


def remote_script(*, source_url: str, archive_sha256: str, expected_digest: str,
                  artifact_url: str, artifact_blob: str, remote_root: str,
                  run_id: str) -> str:
    """Return one fail-closed VM script; it never starts a VM or campaign unit."""
    root = _q(remote_root)
    archive = _q(remote_root + "/source.tgz")
    source_url_q = _q(source_url)
    artifact_url_q = _q(artifact_url)
    expected_archive_q = _q(archive_sha256)
    expected_digest_q = _q(expected_digest)
    run_id_q = _q(run_id)
    return f'''set -eu
root={root}
if [ -e "$root" ]; then
  echo "retained diagnostic run directory already exists: $root" >&2
  exit 73
fi
if systemctl list-units --state=active --no-legend 'p1-*.service' | grep -q .; then
  echo "refusing diagnostic while a P1 service is active" >&2
  exit 74
fi
mkdir -p "$root"
curl --fail --silent --show-error {source_url_q} -o {archive}
echo {_q(archive_sha256 + '  ' + remote_root + '/source.tgz')} | sha256sum -c -
tar -xzf {archive} -C "$root"
chown -R graphcheck:graphcheck "$root"

set +e
runuser -u graphcheck -- env P1_CORPUS_ROOT=/opt/p1/corpus \\
  /usr/bin/python3 "$root/corpus-tools/p1_scenario.py" digest {_q(REPOSITORY)} \\
  > "$root/before-corpus.json" 2>&1
before_digest_status=$?
set -e
if [ "$before_digest_status" -eq 0 ]; then
  set +e
  runuser -u graphcheck -- /usr/bin/python3 - "$root/before-corpus.json" {expected_digest_q} <<'P1_EXPECTED'
import json, pathlib, sys
payload = json.loads(pathlib.Path(sys.argv[1]).read_text())
expected = sys.argv[2]
actual = payload.get("effective_tracked_input_sha256")
if actual != expected:
    raise SystemExit("corpus fingerprint mismatch: " + str(actual))
P1_EXPECTED
  before_identity_status=$?
  set -e
else
  before_identity_status=1
fi

if [ "$before_digest_status" -ne 0 ] || [ "$before_identity_status" -ne 0 ]; then
  printf '%s\\n' '{{"outcome":"precondition_failed","run_id":{json.dumps(run_id)},"before_digest_status":'"$before_digest_status"',"before_identity_status":'"$before_identity_status"'}}' > "$root/outcome.json"
else
  set +e
  runuser -u graphcheck -- env PATH=/usr/local/go/bin:/usr/bin:/bin \\
    GOPATH=/opt/graph-validation/gopath GOCACHE=/opt/graph-validation/cache \\
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \\
    GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOTELEMETRY=off \\
    sh -c 'cd "$1" && go test -c -o diagnostic ./internal/sem' sh "$root"
  build_status=$?
  set -e
  if [ "$build_status" -eq 0 ]; then
    sha256sum "$root/diagnostic" > "$root/binary.sha256"
    runuser -u graphcheck -- /usr/bin/python3 - "$root" <<'P1_RUN_ONCE'
import json, os, pathlib, signal, subprocess, sys, time
root = pathlib.Path(sys.argv[1])
log = (root / "diagnostic.log").open("wb")
env = os.environ.copy()
env.update({{
    "P1_DIAGNOSTIC_REPO": "/opt/p1/corpus/kubernetes-kubernetes",
    "P1_DIAGNOSTIC_OUTPUT": str(root / "raw"),
    "GOMAXPROCS": "4", "GIT_CONFIG_GLOBAL": "/dev/null",
    "GIT_CONFIG_SYSTEM": "/dev/null", "GOPROXY": "off",
    "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off",
}})
(root / "raw").mkdir(mode=0o700)
start = time.monotonic()
process = subprocess.Popen(
    [str(root / "diagnostic"), "-test.run=^TestRetainedSearchDiagnostic$",
     "-test.v", "-test.count=1", "-test.timeout=5m"],
    cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT,
    start_new_session=True,
)
timed_out = False
while process.poll() is None:
    if time.monotonic() - start >= {HARD_TIMEOUT_SECONDS}:
        timed_out = True
        os.killpg(process.pid, signal.SIGKILL)
        break
    time.sleep(0.05)
returncode = process.wait()
log.close()
(root / "process.json").write_text(json.dumps({{
    "run_id": {json.dumps(run_id)}, "returncode": returncode,
    "timed_out": timed_out, "hard_timeout_seconds": {HARD_TIMEOUT_SECONDS},
    "command": ["diagnostic", "-test.run=^TestRetainedSearchDiagnostic$",
                 "-test.v", "-test.count=1", "-test.timeout=5m"],
}}, indent=2) + "\\n")
P1_RUN_ONCE
    runner_status=$?
    set -e
    set +e
    runuser -u graphcheck -- env P1_CORPUS_ROOT=/opt/p1/corpus \\
      /usr/bin/python3 "$root/corpus-tools/p1_scenario.py" digest {_q(REPOSITORY)} \\
      > "$root/after-corpus.json" 2>&1
    after_digest_status=$?
    set -e
    if [ "$after_digest_status" -eq 0 ]; then
      set +e
      runuser -u graphcheck -- /usr/bin/python3 - "$root/after-corpus.json" {expected_digest_q} <<'P1_AFTER_EXPECTED'
import json, pathlib, sys
payload = json.loads(pathlib.Path(sys.argv[1]).read_text())
expected = sys.argv[2]
actual = payload.get("effective_tracked_input_sha256")
if actual != expected:
    raise SystemExit("post-run corpus fingerprint mismatch: " + str(actual))
P1_AFTER_EXPECTED
      after_identity_status=$?
      set -e
    else
      after_identity_status=1
    fi
    if [ "$runner_status" -ne 0 ]; then
      printf '%s\\n' '{{"outcome":"unknown","reason":"diagnostic process wrapper failed","runner_status":'"$runner_status"'}}' > "$root/outcome.json"
    elif [ "$after_digest_status" -ne 0 ] || [ "$after_identity_status" -ne 0 ]; then
      printf '%s\\n' '{{"outcome":"unknown","reason":"post-run corpus fingerprint changed or unavailable","after_digest_status":'"$after_digest_status"',"after_identity_status":'"$after_identity_status"'}}' > "$root/outcome.json"
    else
      printf '%s\\n' '{{"outcome":"diagnostic_completed","build_status":'"$build_status"',"run_id":{json.dumps(run_id)}}}' > "$root/outcome.json"
    fi
  else
    printf '%s\\n' '{{"outcome":"build_failed","build_status":'"$build_status"'}}' > "$root/outcome.json"
  fi
fi

printf '%s\\n' '{{"run_id":{json.dumps(run_id)},"source_archive_sha256":{json.dumps(archive_sha256)},"expected_corpus_digest":{json.dumps(expected_digest)}}}' > "$root/identity.json"
uname -a > "$root/uname.txt"
/usr/local/go/bin/go version > "$root/go-version.txt" 2>&1 || true
archive_out="$root/retained-diagnostic-{run_id}.tar.gz"
tar -czf "$archive_out" -C "$root" .
curl --fail --silent --show-error -X PUT -H "x-ms-blob-type: BlockBlob" \\
  --upload-file "$archive_out" {artifact_url_q}
printf 'P1_UPLOAD_OK %s\\n' {_q(artifact_blob)}
'''


def parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--source-commit", required=True)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--output", type=pathlib.Path, required=True)
    ap.add_argument("--source-root", type=pathlib.Path, default=REPO_ROOT)
    ap.add_argument("--vm", default=DEFAULT_VM)
    return ap


def _unknown(output: pathlib.Path, metadata: dict[str, Any], reason: str) -> None:
    output.mkdir(parents=True, exist_ok=True)
    payload = dict(metadata)
    payload.update({"outcome": "unknown", "reason": reason})
    (output / "unknown.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")


def run(args: argparse.Namespace, *, transport: Any = cloud,
        downloader: Callable[..., Any] = download_immutable) -> dict[str, Any]:
    if not RUN_ID_RE.fullmatch(args.run_id):
        raise ValueError("invalid run id")
    output = args.output.resolve()
    if output.exists():
        raise FileExistsError("refusing to reuse diagnostic output directory: " + str(output))
    source_root = args.source_root.resolve()
    commit = resolve_commit(source_root, args.source_commit)
    expected_digest = expected_corpus_digest()
    harness_digest = sha256_file(DIAGNOSTIC_SOURCE)
    metadata: dict[str, Any] = {
        "format_version": 1, "run_id": args.run_id, "vm": args.vm,
        "repository": REPOSITORY, "source_commit_requested": args.source_commit,
        "source_commit": commit, "diagnostic_harness_sha256": harness_digest,
        "expected_corpus_digest": expected_digest,
        "profile": "syntax-only", "compiler": "off", "ranking": "current",
        "test_count": 1, "arm_order": ["off", "on"],
        "arm_timeout_seconds": 120, "hard_timeout_seconds": HARD_TIMEOUT_SECONDS,
        "no_campaign": True,
    }
    output.mkdir(parents=True)
    try:
        with tempfile.TemporaryDirectory(prefix="retained-diagnostic-") as temporary:
            archive = pathlib.Path(temporary) / "source.tgz"
            build_source_archive(source_root, commit, archive, metadata)
            archive_digest = sha256_file(archive)
            metadata["source_archive_sha256"] = archive_digest
            (output / "request-metadata.json").write_text(
                json.dumps(metadata, indent=2, sort_keys=True) + "\n")
            env = transport.environment()
            source_blob = f"p1-retained-diagnostic-source-{args.run_id}-{archive_digest}.tgz"
            artifact_blob = f"p1-retained-diagnostic-result-{args.run_id}-{archive_digest}.tar.gz"
            transport.upload(archive, source_blob, env)
            source_url = transport.url(source_blob, "r", env)
            artifact_url = transport.url(artifact_blob, "cw", env)
            script = remote_script(
                source_url=source_url, archive_sha256=archive_digest,
                expected_digest=expected_digest, artifact_url=artifact_url,
                artifact_blob=artifact_blob,
                remote_root=f"{REMOTE_PARENT}/{args.run_id}", run_id=args.run_id,
            )
            (output / "remote-script.sh").write_text(script)
            raw = transport.run(args.vm, script)
            (output / "run-command-response.json").write_text(str(raw) + "\n")
            require_upload_ack(raw, artifact_blob)
            downloader(artifact_blob, output / artifact_blob, env)
            return {"outcome": "artifact_collected", "artifact_blob": artifact_blob,
                    "source_blob": source_blob, "output": str(output)}
    except Exception as exc:
        _unknown(output, metadata, str(exc))
        raise


def main() -> int:
    args = parser().parse_args()
    try:
        result = run(args)
    except Exception as exc:
        raise SystemExit(str(exc)) from exc
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
