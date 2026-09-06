#!/usr/bin/env python3
"""Collect one cache-OFF full-diagnostics snapshot observation.

This is a correctness/evidence collector, not a campaign runner.  It runs one
syntax-only snapshot request, retains raw process and artifact files, and
refuses to interpret the expected partial result as full-coverage evidence.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess

EXPECTED_SOURCE_COMMIT = "aab356ae40eabbbdca7659eac9bcb58ccd8feb18"
EXPECTED_REPOSITORY = "kubernetes-kubernetes"
EXPECTED_REPO_PATH = "/opt/p1/corpus/kubernetes-kubernetes"
EXPECTED_PROVIDER = "p1-corpus-20260905"
EXPECTED_SOURCE_DIGEST = "d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4"
EXPECTED_INPUT_MANIFEST_SHA256 = "d2fdce2a59befb3a0a02bcc7fc5a531eb8571a1788b0070b6fd2147e92e273e0"
EXPECTED_SEMANTIC_SHA256 = "fa08ae3464a63c71db89f5755062ac76b3a8960e5bccd2f536c1491d8543b4f7"
EXPECTED_PARTIAL_FAILURES_COUNT = 194
EXPECTED_PARTIAL_FAILURES_SHA256 = "846649bc1925c607b91b3f41014408938c37232d0a12d86f71569776b46819ef"
EXPECTED_WARNINGS_COUNT = 1
EXPECTED_WARNINGS_SHA256 = "e0ce85fefeba137c4e41fcfa3bc5f1d62d461bc1f4fc7eff5587bbd52cf50468"
EXPECTED_MUTATION_ID = "retained-snapshot-d793b2be-corrective"
EXPECTED_SCENARIO = "diagnostic"
EXPECTED_PROFILE = "syntax-only"
TIME_BINARY = "/usr/bin/time"
TIMEOUT_SECONDS = 120
FINGERPRINT_TIMEOUT_SECONDS = 60
GO_TEST_TIMEOUT_SECONDS = 130
RSS_PREFIX = "Maximum resident set size (kbytes):"
RSS_VALUE_RE = re.compile(r"^[0-9]+$")
MAX_RSS_BYTES = (1 << 64) - 1


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def save(path, value):
    path = Path(path)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def existing_artifact_hashes(root):
    root = Path(root)
    names = ("identity.json", "manifest.json", "before.json", "after.json", "observation.ndjson",
             "diagnostics.json", "process.log", "process.json", "time.txt")
    return {name: sha256(root / name) for name in names if (root / name).is_file()}


def read_json_object_with_raw(path):
    """Decode one JSON object and retain exact UTF-8 member value bytes."""
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
    position += 1
    values = {}
    raw_values = {}
    position = whitespace(position)
    if position < len(text) and text[position] == "}":
        position += 1
    else:
        while True:
            position = whitespace(position)
            key, position = decoder.raw_decode(text, position)
            if not isinstance(key, str):
                raise RuntimeError(f"{path} root key is not a string")
            if key in values:
                raise RuntimeError(f"{path} contains duplicate root key: {key}")
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
    }


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
        "repository_id": "kubernetes-kubernetes",
        "repo_path": EXPECTED_REPO_PATH,
        "operation": "snapshot",
        "mode": "measure",
        "cache": "off",
        "cache_path": str(output / "cache-off-unused"),
        "profile": EXPECTED_PROFILE,
        "provider_version": EXPECTED_PROVIDER,
        "mutation_id": EXPECTED_MUTATION_ID,
        "source_digest": EXPECTED_SOURCE_DIGEST,
        "scenario": EXPECTED_SCENARIO,
        "trial": 0,
        "diagnostics_path": "diagnostics.json",
    }


def validate_identity(value, manifest, binary_sha256, label):
    expected = {
        "repository": EXPECTED_REPOSITORY,
        "repository_path": EXPECTED_REPO_PATH,
        "operation": "snapshot",
        "mode": "measure",
        "cache_mode": "off",
        "profile": EXPECTED_PROFILE,
        "provider_version": EXPECTED_PROVIDER,
        "mutation_id": EXPECTED_MUTATION_ID,
        "source_digest": EXPECTED_SOURCE_DIGEST,
        "binary_sha256": binary_sha256,
        "scenario": EXPECTED_SCENARIO,
        "trial": 0,
        "reuse": False,
        "verb": "snapshot",
    }
    for field, wanted in expected.items():
        if value.get(field) != wanted:
            raise RuntimeError(f"{label} identity mismatch: {field}")
    if value.get("manifest_path") != str((Path(manifest["_path"])).resolve()):
        raise RuntimeError(f"{label} identity mismatch: manifest_path")


def validate_observation(observation, manifest, binary_sha256):
    validate_identity(observation, manifest, binary_sha256, "observation")
    if observation.get("status") != "partial":
        raise RuntimeError("observation status is not the expected partial result")
    for field, wanted in (
        ("semantic_sha256", EXPECTED_SEMANTIC_SHA256),
        ("semantic_digest", EXPECTED_SEMANTIC_SHA256),
        ("partial_failures_count", EXPECTED_PARTIAL_FAILURES_COUNT),
        ("partial_failures_sha256", EXPECTED_PARTIAL_FAILURES_SHA256),
        ("warnings_count", EXPECTED_WARNINGS_COUNT),
        ("warnings_sha256", EXPECTED_WARNINGS_SHA256),
    ):
        if observation.get(field) != wanted:
            raise RuntimeError(f"observation retained identity mismatch: {field}")
    if not isinstance(observation.get("partial_failures"), list):
        raise RuntimeError("observation partial-failure sample is missing")
    if len(observation["partial_failures"]) > EXPECTED_PARTIAL_FAILURES_COUNT:
        raise RuntimeError("observation partial-failure sample exceeds full count")
    if not isinstance(observation.get("warnings"), list) or len(observation["warnings"]) != EXPECTED_WARNINGS_COUNT:
        raise RuntimeError("observation warning records are incomplete")


def validate_diagnostics(diagnostics, observation_path, manifest, binary_sha256,
                         raw_values=None, expected_partial_digest=EXPECTED_PARTIAL_FAILURES_SHA256,
                         expected_warning_digest=EXPECTED_WARNINGS_SHA256):
    validate_identity(diagnostics, manifest, binary_sha256, "diagnostics")
    if diagnostics.get("observation_path") != str(Path(observation_path).resolve()):
        raise RuntimeError("diagnostics observation_path mismatch")
    if diagnostics.get("status") != "partial":
        raise RuntimeError("diagnostics status mismatch")
    for field, wanted in (
        ("semantic_sha256", EXPECTED_SEMANTIC_SHA256),
        ("semantic_digest", EXPECTED_SEMANTIC_SHA256),
        ("partial_failures_count", EXPECTED_PARTIAL_FAILURES_COUNT),
        ("partial_failures_sha256", expected_partial_digest),
        ("warnings_count", EXPECTED_WARNINGS_COUNT),
        ("warnings_sha256", expected_warning_digest),
    ):
        if diagnostics.get(field) != wanted:
            raise RuntimeError(f"diagnostics identity mismatch: {field}")
    failures = diagnostics.get("partial_failures")
    warnings = diagnostics.get("warnings")
    if not isinstance(failures, list) or len(failures) != EXPECTED_PARTIAL_FAILURES_COUNT:
        raise RuntimeError("full partial-failure artifact count mismatch")
    if not isinstance(warnings, list) or len(warnings) != EXPECTED_WARNINGS_COUNT:
        raise RuntimeError("full warning artifact count mismatch")
    if raw_values is not None:
        if "partial_failures" not in raw_values or "warnings" not in raw_values:
            raise RuntimeError("diagnostic arrays are absent from raw artifact")
        if hashlib.sha256(raw_values["partial_failures"]).hexdigest() != expected_partial_digest:
            raise RuntimeError("full partial-failure digest does not match raw artifact")
        if hashlib.sha256(raw_values["warnings"]).hexdigest() != expected_warning_digest:
            raise RuntimeError("full warning digest does not match raw artifact")
    # Go computes these digests over compact json.Marshal array bytes. The raw
    # member slices preserve those bytes; Python never reserializes the arrays.
    return {"partial_failures": len(failures), "warnings": len(warnings)}


def run_process(root, binary, environment, process_factory=subprocess.Popen, kill_process_group=os.killpg):
    root = Path(root)
    time_path = root / "time.txt"
    process_log = root / "process.log"
    command = [TIME_BINARY, "-v", "-o", str(time_path), "--", str(binary),
               "-test.run=^TestExtractionCorpusMeasurement$", "-test.count=1", "-test.v",
               f"-test.timeout={GO_TEST_TIMEOUT_SECONDS}s"]
    with process_log.open("wb") as log:
        process = process_factory(command, env=environment, cwd=root, stdout=log,
                                  stderr=subprocess.STDOUT, start_new_session=True)
        timed_out = False
        try:
            exit_code = process.wait(timeout=TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired:
            timed_out = True
            kill_process_group(process.pid, signal.SIGKILL)
            exit_code = process.wait()
    raw_time = time_path.read_text() if time_path.is_file() else ""
    peak_rss_bytes = None
    rss_error = None
    try:
        peak_rss_bytes = parse_peak_rss_bytes(raw_time)
    except ValueError as error:
        rss_error = str(error)
    save(root / "process.json", {
        "command": command, "exit_code": exit_code, "timed_out": timed_out,
        "timeout_seconds": TIMEOUT_SECONDS, "peak_rss_bytes": peak_rss_bytes,
        "rss_status": "measured by /usr/bin/time -v" if rss_error is None else "invalid",
        "rss_error": rss_error,
    })
    if timed_out:
        raise RuntimeError("request timed out")
    if exit_code:
        raise RuntimeError(f"request process failed with exit {exit_code}")
    if rss_error is not None:
        raise RuntimeError("request RSS measurement invalid")
    return {"exit_code": exit_code, "peak_rss_bytes": peak_rss_bytes}


def run_request(output, binary, scenario_script, corpus_root, binary_sha256,
                source_root, fingerprint_fn=fingerprint, process_factory=subprocess.Popen,
                kill_process_group=os.killpg):
    output = Path(output).resolve()
    if Path(corpus_root).resolve() != Path(EXPECTED_REPO_PATH).parent:
        raise RuntimeError("fingerprint corpus root differs from fixed product repository")
    if output.exists():
        raise RuntimeError("output directory already exists; refusing overwrite")
    output.mkdir(parents=True)
    binary = Path(binary).resolve()
    scenario_script = Path(scenario_script).resolve()
    source_root = Path(source_root).resolve()
    if not binary.is_file() or not scenario_script.is_file() or not source_root.is_dir():
        raise RuntimeError("source, binary, or scenario input unavailable")
    if not binary.is_relative_to(source_root):
        raise RuntimeError("binary is outside source root")
    if sha256(binary) != binary_sha256:
        raise RuntimeError("binary identity mismatch")
    manifest = expected_manifest(output, binary_sha256)
    manifest_path = output / "manifest.json"
    manifest["_path"] = str(manifest_path)
    save(manifest_path, {k: v for k, v in manifest.items() if k != "_path"})
    scenario_sha256 = sha256(scenario_script)
    save(output / "identity.json", {
        "source_commit": EXPECTED_SOURCE_COMMIT,
        "source_root": str(source_root),
        "source_provenance": "caller-supplied source archive; commit is recorded, not independently verified",
        "binary": str(binary), "binary_sha256": binary_sha256,
        "scenario_script": str(scenario_script), "scenario_sha256": scenario_sha256,
        "repository": EXPECTED_REPOSITORY, "repository_path": EXPECTED_REPO_PATH,
        "source_digest": EXPECTED_SOURCE_DIGEST,
        "input_manifest_sha256": EXPECTED_INPUT_MANIFEST_SHA256,
        "semantic_sha256": EXPECTED_SEMANTIC_SHA256,
        "partial_failures_count": EXPECTED_PARTIAL_FAILURES_COUNT,
        "partial_failures_sha256": EXPECTED_PARTIAL_FAILURES_SHA256,
        "warnings_count": EXPECTED_WARNINGS_COUNT,
        "warnings_sha256": EXPECTED_WARNINGS_SHA256,
        "request_scope": "one cache-off syntax-only snapshot; no ON arm, repeats, ratios, or admission",
    })
    environment = runtime_environment(corpus_root)
    environment.update(
        ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST=str(manifest_path),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT=str(output / "observation.ndjson"),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT="ndjson",
    )
    issue = None
    before = None
    process = None
    try:
        before = fingerprint_fn(output, scenario_script, corpus_root, "before")
        process = run_process(output, binary, environment, process_factory, kill_process_group)
        if sha256(binary) != binary_sha256:
            raise RuntimeError("binary identity changed after request")
        if sha256(scenario_script) != scenario_sha256:
            raise RuntimeError("scenario script identity changed after request")
        observation_path = output / "observation.ndjson"
        diagnostics_path = output / "diagnostics.json"
        observation, _ = read_json_object_with_raw(observation_path)
        diagnostics, diagnostics_raw = read_json_object_with_raw(diagnostics_path)
        validate_observation(observation, manifest, binary_sha256)
        artifact_counts = validate_diagnostics(
            diagnostics, observation_path, manifest, binary_sha256, diagnostics_raw
        )
        after = fingerprint_fn(output, scenario_script, corpus_root, "after")
        if after != before:
            raise RuntimeError("input fingerprint changed during request")
        save(output / "outcome.json", {
            "status": "captured_expected_partial",
            "admission_eligible": False,
            "review_status": "not_reviewed",
            "coverage": "partial",
            "no_performance_claim": True,
            "process": process,
            "diagnostics": artifact_counts,
            "artifact_sha256": {
                "manifest": sha256(manifest_path),
                "observation": sha256(observation_path),
                "diagnostics": sha256(diagnostics_path),
            },
        })
        return {"status": "captured_expected_partial", "process": process, "diagnostics": artifact_counts}
    except Exception as error:
        issue = str(error)
    finally:
        if before is not None and not (output / "after.json").exists():
            try:
                after = fingerprint_fn(output, scenario_script, corpus_root, "after")
                if after != before:
                    after_issue = "input fingerprint changed during request"
                    issue = after_issue if issue is None else issue + "; " + after_issue
            except Exception as error:
                after_issue = str(error)
                issue = after_issue if issue is None else issue + "; " + after_issue
        if issue is not None:
            save(output / "outcome.json", {
                "status": "issue", "issue": issue, "admission_eligible": False,
                "review_status": "not_reviewed", "no_performance_claim": True,
                "process": process,
                "artifact_sha256": existing_artifact_hashes(output),
            })
    return {"status": "issue", "issue": issue}


def parse_args(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--binary-sha256", required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--scenario-script", type=Path, required=True)
    parser.add_argument("--corpus-root", type=Path, default=Path("/opt/p1/corpus"))
    parser.add_argument("--input-sha256", required=True)
    parser.add_argument("--input-manifest-sha256", required=True)
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    if args.source_commit != EXPECTED_SOURCE_COMMIT:
        raise SystemExit("source commit does not match diagnostics collector source")
    if args.input_sha256 != EXPECTED_SOURCE_DIGEST or args.input_manifest_sha256 != EXPECTED_INPUT_MANIFEST_SHA256:
        raise SystemExit("corpus identity does not match retained diagnostics contract")
    if not re.fullmatch(r"[0-9a-fA-F]{64}", args.binary_sha256):
        raise SystemExit("binary SHA-256 must be 64 hexadecimal characters")
    result = run_request(args.output, args.binary, args.scenario_script, args.corpus_root,
                         args.binary_sha256, args.source_root)
    if result["status"] == "issue":
        raise SystemExit(result["issue"])


if __name__ == "__main__":
    main()
