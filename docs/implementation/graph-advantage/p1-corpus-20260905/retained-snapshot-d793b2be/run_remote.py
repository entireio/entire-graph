#!/usr/bin/env python3
"""One bounded corrective snapshot pair using a caller-pinned binary.

This file is prepared for a later remote invocation. It performs exactly one
OFF request and, only after that observation passes the retained identity
checks, one ON request. It never retries or starts a campaign.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

EXPECTED_REPOSITORY = "kubernetes-kubernetes"
EXPECTED_PROFILE = "syntax-only"
EXPECTED_OPERATION = "snapshot"
EXPECTED_PROVIDER = "p1-corpus-20260905"
EXPECTED_PARTIAL_FAILURES_COUNT = 194
EXPECTED_PARTIAL_FAILURES_SHA256 = "846649bc1925c607b91b3f41014408938c37232d0a12d86f71569776b46819ef"
EXPECTED_WARNINGS_COUNT = 1
EXPECTED_WARNINGS_SHA256 = "e0ce85fefeba137c4e41fcfa3bc5f1d62d461bc1f4fc7eff5587bbd52cf50468"
EXPECTED_SEMANTIC_SHA256 = "fa08ae3464a63c71db89f5755062ac76b3a8960e5bccd2f536c1491d8543b4f7"
TIMEOUT_SECONDS = 120
FINGERPRINT_TIMEOUT_SECONDS = 60
GO_TEST_TIMEOUT_SECONDS = 130


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def save(root, name, value):
    path = Path(root) / name
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def fingerprint(root, scenario_script, corpus_root, input_sha256, stage):
    env = runtime_environment(corpus_root)
    result = subprocess.run(
        ["/usr/bin/python3", str(scenario_script), "digest", EXPECTED_REPOSITORY],
        env=env,
        capture_output=True,
        timeout=FINGERPRINT_TIMEOUT_SECONDS,
    )
    (Path(root) / (stage + ".log")).write_bytes(result.stderr)
    (Path(root) / (stage + ".json")).write_bytes(result.stdout)
    if result.returncode:
        raise RuntimeError(stage + " input fingerprint failed")
    value = json.loads(result.stdout)
    if value.get("effective_tracked_input_sha256") != input_sha256:
        raise RuntimeError(stage + " input identity mismatch")
    return value


def runtime_environment(corpus_root):
    environment = dict(
        PATH="/usr/local/go/bin:/usr/bin:/bin",
        LANG="C.UTF-8",
        LC_ALL="C.UTF-8",
        LC_CTYPE="C.UTF-8",
        GOMAXPROCS="4",
        GIT_CONFIG_GLOBAL="/dev/null",
        GIT_CONFIG_SYSTEM="/dev/null",
        GOPROXY="off",
        GOSUMDB="off",
        GOTOOLCHAIN="local",
        GOTELEMETRY="off",
        P1_CORPUS_ROOT=str(corpus_root),
    )
    return environment


def validate_observation(observation, arm, binary_sha256, input_sha256):
    expected = {
        "repository": EXPECTED_REPOSITORY,
        "operation": EXPECTED_OPERATION,
        "profile": EXPECTED_PROFILE,
        "provider_version": EXPECTED_PROVIDER,
        "status": "partial",
        "source_digest": input_sha256,
        "binary_sha256": binary_sha256,
        "semantic_sha256": EXPECTED_SEMANTIC_SHA256,
        "semantic_digest": EXPECTED_SEMANTIC_SHA256,
        "partial_failures_count": EXPECTED_PARTIAL_FAILURES_COUNT,
        "partial_failures_sha256": EXPECTED_PARTIAL_FAILURES_SHA256,
        "warnings_count": EXPECTED_WARNINGS_COUNT,
        "warnings_sha256": EXPECTED_WARNINGS_SHA256,
    }
    for field, wanted in expected.items():
        if observation.get(field) != wanted:
            raise RuntimeError(f"{arm} observation identity mismatch: {field}")
    if observation.get("cache_mode") != arm:
        raise RuntimeError(f"{arm} cache mode mismatch")
    if observation.get("reuse") != (arm == "on"):
        raise RuntimeError(f"{arm} reuse mismatch")
    return observation


def run_arm(root, binary, scenario_script, corpus_root, binary_sha256, input_sha256, arm, started=None):
    config = dict(
        version=1,
        repository=EXPECTED_REPOSITORY,
        repo_path=str(Path(corpus_root) / EXPECTED_REPOSITORY),
        operation=EXPECTED_OPERATION,
        mode="measure",
        cache=arm,
        cache_path=str(Path(root) / ("cache-" + arm)),
        profile=EXPECTED_PROFILE,
        provider_version=EXPECTED_PROVIDER,
        mutation_id="retained-snapshot-d793b2be-corrective",
        scenario="diagnostic",
        trial=0,
        source_digest=input_sha256,
        input_manifest_sha256="d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0",
    )
    save(root, "request-" + arm + ".json", config)
    environment = runtime_environment(corpus_root)
    environment.update(
        ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG=str(Path(root) / ("request-" + arm + ".json")),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT=str(Path(root) / ("observation-" + arm + ".ndjson")),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT="ndjson",
    )
    command = [
        str(binary),
        "-test.run=^TestExtractionCorpusMeasurement$",
        "-test.count=1",
        "-test.v",
        f"-test.timeout={GO_TEST_TIMEOUT_SECONDS}s",
    ]
    process_path = Path(root) / ("process-" + arm + ".log")
    timed_out = False
    with process_path.open("wb") as log:
        process = subprocess.Popen(
            command,
            env=environment,
            cwd=root,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        if started is not None:
            started.append(arm)
        try:
            exit_code = process.wait(timeout=TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(process.pid, signal.SIGKILL)
            exit_code = process.wait()
    save(
        root,
        "process-" + arm + ".json",
        dict(
            command=command,
            exit_code=exit_code,
            timed_out=timed_out,
            timeout_seconds=TIMEOUT_SECONDS,
            peak_rss_bytes=None,
            rss_status="unavailable; no isolated wait4 measurement claimed",
        ),
    )
    if timed_out or exit_code:
        raise RuntimeError(f"{arm} process failed or timed out; stop")
    observation_path = Path(root) / ("observation-" + arm + ".ndjson")
    observation = json.loads(observation_path.read_text())
    return validate_observation(observation, arm, binary_sha256, input_sha256)


def parse_args(argv):
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--binary-sha256", required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--scenario-script", type=Path, required=True)
    parser.add_argument("--corpus-root", type=Path, default=Path("/opt/p1/corpus"))
    parser.add_argument("--input-sha256", required=True)
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    root = args.output.resolve()
    binary = args.binary.resolve()
    source_root = args.source_root.resolve()
    scenario_script = args.scenario_script.resolve()
    corpus_root = args.corpus_root.resolve()
    root.mkdir(parents=True, exist_ok=True)
    if any(root.iterdir()):
        raise SystemExit("output directory must be empty; refusing overwrite")
    identity = dict(
        source_root=str(source_root),
        source_commit_asserted=args.source_commit,
        source_provenance="caller-supplied build archive; commit is recorded, not independently verified",
        binary=str(binary),
        expected_binary_sha256=args.binary_sha256,
        scenario_script=str(scenario_script),
        scenario_sha256=sha256(scenario_script),
        corpus_root=str(corpus_root),
        repository=EXPECTED_REPOSITORY,
        expected_input_sha256=args.input_sha256,
        expected_partial_failures_count=EXPECTED_PARTIAL_FAILURES_COUNT,
        expected_partial_failures_sha256=EXPECTED_PARTIAL_FAILURES_SHA256,
        expected_warnings_count=EXPECTED_WARNINGS_COUNT,
        expected_warnings_sha256=EXPECTED_WARNINGS_SHA256,
        expected_semantic_sha256=EXPECTED_SEMANTIC_SHA256,
        timeout_seconds=TIMEOUT_SECONDS,
        fingerprint_timeout_seconds=FINGERPRINT_TIMEOUT_SECONDS,
        go_test_timeout_seconds=GO_TEST_TIMEOUT_SECONDS,
        rss_status="unavailable; no isolated wait4 measurement claimed",
    )
    save(root, "environment.json", runtime_environment(corpus_root))
    save(root, "identity.json", identity)
    completed = []
    issue = None
    before = None
    observations = {}
    try:
        if not source_root.is_dir():
            raise RuntimeError("source root is unavailable")
        if not binary.is_file():
            raise RuntimeError("pinned binary is unavailable")
        if not binary.is_relative_to(source_root):
            raise RuntimeError("binary is outside the pinned source root")
        actual_binary_sha256 = sha256(binary)
        save(root, "binary.json", dict(sha256=actual_binary_sha256))
        if actual_binary_sha256 != args.binary_sha256:
            raise RuntimeError("binary identity mismatch")
        if not scenario_script.is_file():
            raise RuntimeError("pinned scenario script is unavailable")
        before = fingerprint(root, scenario_script, corpus_root, args.input_sha256, "before")
        started = []
        for arm in ("off", "on"):
            observation = run_arm(
                root,
                binary,
                scenario_script,
                corpus_root,
                args.binary_sha256,
                args.input_sha256,
                arm,
                started,
            )
            observations[arm] = observation
            completed.append(arm)
            if arm == "off" and observation["semantic_sha256"] != EXPECTED_SEMANTIC_SHA256:
                raise RuntimeError("OFF semantic identity mismatch; stop before ON")
            if arm == "off":
                if not source_root.is_dir() or not binary.is_file():
                    raise RuntimeError("source or binary unavailable before ON")
                if sha256(binary) != args.binary_sha256:
                    raise RuntimeError("binary changed before ON")
                if sha256(scenario_script) != identity["scenario_sha256"]:
                    raise RuntimeError("scenario script changed before ON")
                before_on = fingerprint(root, scenario_script, corpus_root, args.input_sha256, "before-on")
                if before_on != before:
                    raise RuntimeError("input changed before ON")
        if observations["off"]["semantic_sha256"] != observations["on"]["semantic_sha256"]:
            raise RuntimeError("semantic mismatch; stop")
        if observations["off"]["source_digest"] != observations["on"]["source_digest"]:
            raise RuntimeError("source digest mismatch; stop")
    except Exception as error:
        issue = str(error)
    finally:
        if before is not None:
            try:
                if fingerprint(root, scenario_script, corpus_root, args.input_sha256, "after") != before:
                    issue = "input changed during diagnostic"
            except Exception as error:
                issue = str(error)
        save(
            root,
            "outcome.json",
            dict(
                status="issue" if issue else "pair_semantics_equal",
                issue=issue,
                processes_started=started,
                unrun=[arm for arm in ("off", "on") if arm not in started],
                observations={arm: observations[arm] for arm in completed},
                scope="one isolated corrective diagnostic; no campaign or release gate",
            ),
        )
    if issue:
        raise SystemExit(issue)


if __name__ == "__main__":
    main()
