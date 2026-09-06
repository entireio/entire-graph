#!/usr/bin/env python3
"""One retained query-correctness replay: syntax-only, fast, then full.

The six request files are frozen inputs. The runner executes one OFF/ON pair
per profile, stops at the first issue, and makes no performance claim.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess

EXPECTED_SOURCE_COMMIT = "1c0b8e24f3b6a5bb880e7b306c1c74d818614782"
EXPECTED_BINARY_SHA256 = "b51728ed2c0840081c0921e6ec29931d2b10dce749802fb4f6fb341a60852e37"
EXPECTED_REPOSITORY = "kubernetes-kubernetes"
EXPECTED_REPO_PATH = "/opt/p1/corpus/kubernetes-kubernetes"
EXPECTED_INPUT_SHA256 = "d7a25ec35c9720efead0ac3f3dccc493385f6f4bc8c42d2f0313e2afbc9e4db4"
EXPECTED_PROVIDER = "p1-corpus-20260905"
EXPECTED_QUERY = "find the values symbol or entrypoint in staging/src/k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/values_test.go"
EXPECTED_PROFILES = ("syntax-only", "fast", "full")
EXPECTED_CACHE_DIRS = {
    ("syntax-only", "off"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-off-syntax-only",
    ("syntax-only", "on"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-on-syntax-only",
    ("fast", "off"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-off-fast",
    ("fast", "on"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-on-fast",
    ("full", "off"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-off-full",
    ("full", "on"): "/opt/graph-validation/retained-query-correctness-1c0b8e24/cache-on-full",
}
TIME_BINARY = "/usr/bin/time"
TIMEOUT_SECONDS = 120
FINGERPRINT_TIMEOUT_SECONDS = 60
GO_TEST_TIMEOUT_SECONDS = 130
RSS_PREFIX = "Maximum resident set size (kbytes):"
RSS_VALUE_RE = re.compile(r"^[0-9]+$")
MAX_RSS_BYTES = (1 << 64) - 1
ARM_ORDER = tuple((profile, arm) for profile in EXPECTED_PROFILES for arm in ("off", "on"))
VOLATILE_FIELDS = (
    "started_at", "elapsed_ns", "wall_ns", "product_ns", "serialization_ns",
    "phase_ns", "peak_rss_bytes", "cache_bytes", "cache_path", "cache_dir",
    "manifest_path", "process_log", "process_exit", "timed_out", "prime",
    "stats", "extraction.unchanged_reparses",
)
PARITY_FIELDS = (
    "format_version", "manifest_version", "mode", "mutation_id", "operation",
    "partial_failures", "partial_failures_count", "partial_failures_sha256",
    "completeness", "profile", "provider_version", "query", "repository",
    "repository_path", "scenario", "semantic_bytes", "semantic_digest",
    "semantic_sha256", "source_digest", "status",
    "trial", "verb", "warnings", "warnings_count", "warnings_sha256",
)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def save(root, name, value):
    (Path(root) / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def runtime_environment(corpus_root):
    return {
        "PATH": "/usr/local/go/bin:/usr/bin:/bin",
        "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "LC_CTYPE": "C.UTF-8",
        "GOMAXPROCS": "4", "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_CONFIG_SYSTEM": "/dev/null", "GOPROXY": "off", "GOSUMDB": "off",
        "GOTOOLCHAIN": "local", "GOTELEMETRY": "off",
        "P1_CORPUS_ROOT": str(corpus_root),
    }


def fingerprint(root, scenario_script, corpus_root, stage):
    result = subprocess.run(
        ["/usr/bin/python3", str(scenario_script), "digest", EXPECTED_REPOSITORY],
        env=runtime_environment(corpus_root), capture_output=True,
        timeout=FINGERPRINT_TIMEOUT_SECONDS,
    )
    (Path(root) / (stage + ".log")).write_bytes(result.stderr)
    (Path(root) / (stage + ".json")).write_bytes(result.stdout)
    if result.returncode:
        raise RuntimeError(stage + " input fingerprint failed")
    value = json.loads(result.stdout)
    if value.get("effective_tracked_input_sha256") != EXPECTED_INPUT_SHA256:
        raise RuntimeError(stage + " input identity mismatch")
    return value


def parse_peak_rss_bytes(raw_time):
    values = []
    for line in raw_time.splitlines():
        stripped = line.strip()
        if stripped.startswith(RSS_PREFIX):
            value = stripped[len(RSS_PREFIX):].strip()
            if not RSS_VALUE_RE.fullmatch(value):
                raise ValueError("malformed GNU time peak RSS")
            values.append(int(value))
    if len(values) != 1 or values[0] <= 0:
        raise ValueError("missing or invalid GNU time peak RSS")
    result = values[0] * 1024
    if result > MAX_RSS_BYTES:
        raise ValueError("GNU time peak RSS exceeds representable range")
    return result


def _manifest_contract(manifest):
    expected = {
        "source_commit": EXPECTED_SOURCE_COMMIT,
        "binary_sha256": EXPECTED_BINARY_SHA256,
        "repository": EXPECTED_REPOSITORY,
        "repo_path": EXPECTED_REPO_PATH,
        "provider_version": EXPECTED_PROVIDER,
        "source_digest": EXPECTED_INPUT_SHA256,
        "operation": "search",
        "scenario": "cold",
        "mutation_id": "cold",
        "trial": 0,
        "query": EXPECTED_QUERY,
        "top_k": 8,
        "max_indexed_files": 0,
        "compiler": "off",
        "ranking": "current",
    }
    for field, value in expected.items():
        if manifest.get(field) != value:
            raise ValueError("frozen query manifest mismatch: " + field)


def load_configs(config_dir):
    config_dir = Path(config_dir).resolve()
    manifest_path = config_dir / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    _manifest_contract(manifest)
    pairs = manifest.get("pairs")
    expected_pairs = [
        {"id": f"kubernetes-search-{profile}-trial-0", "profile": profile,
         "off_request": f"request-{profile}-off.json", "on_request": f"request-{profile}-on.json"}
        for profile in EXPECTED_PROFILES
    ]
    if pairs != expected_pairs:
        raise ValueError("frozen query pair manifest mismatch")
    configs = {}
    for profile, arm in ARM_ORDER:
        path = config_dir / f"request-{profile}-{arm}.json"
        config = json.loads(path.read_text())
        expected = {
            "version": 1, "repository": EXPECTED_REPOSITORY, "repo_path": EXPECTED_REPO_PATH,
            "operation": "search", "mode": "measure", "cache": arm,
            "cache_dir": EXPECTED_CACHE_DIRS[(profile, arm)], "profile": profile,
            "query": EXPECTED_QUERY, "provider_version": EXPECTED_PROVIDER,
            "top_k": 8, "max_indexed_files": 0, "source_digest": EXPECTED_INPUT_SHA256,
            "mutation_id": "cold", "scenario": "cold", "trial": 0,
        }
        if config != expected:
            raise ValueError(f"frozen request mismatch: {profile} {arm}")
        configs[(profile, arm)] = (path, config)
    return manifest_path, manifest, configs


def validate_observation(observation, config, binary_sha256):
    required = {
        "repository": EXPECTED_REPOSITORY, "repository_path": EXPECTED_REPO_PATH,
        "operation": "search", "profile": config["profile"], "query": EXPECTED_QUERY,
        "provider_version": EXPECTED_PROVIDER, "binary_sha256": binary_sha256,
        "source_digest": EXPECTED_INPUT_SHA256, "scenario": "cold",
        "mutation_id": "cold", "trial": 0, "cache_mode": config["cache"],
        "reuse": config["cache"] == "on",
    }
    for field, value in required.items():
        if observation.get(field) != value:
            raise RuntimeError("observation identity mismatch: " + field)
    if observation.get("status") not in ("ok", "partial"):
        raise RuntimeError("unexpected observation status")
    if observation.get("semantic_digest") != observation.get("semantic_sha256"):
        raise RuntimeError("semantic digest mismatch")
    if not observation.get("semantic_digest"):
        raise RuntimeError("missing semantic digest")
    partial = observation.get("partial_failures")
    count = observation.get("partial_failures_count")
    if not isinstance(partial, list) or not isinstance(count, int) or count < 0:
        raise RuntimeError("missing full partial-failure membership")
    if count > 20:
        raise RuntimeError("partial-failure membership exceeds retained observation bound")
    if len(partial) != count:
        raise RuntimeError("partial-failure membership/count mismatch")
    if not isinstance(observation.get("partial_failures_sha256"), str):
        raise RuntimeError("missing partial-failure digest")
    warnings = observation.get("warnings")
    warning_count = observation.get("warnings_count")
    if not isinstance(warnings, list) or len(warnings) != warning_count:
        raise RuntimeError("warning membership/count mismatch")
    if not isinstance(observation.get("warnings_sha256"), str):
        raise RuntimeError("missing warning digest")
    if not isinstance(observation.get("completeness"), dict):
        raise RuntimeError("missing completeness fields")
    extraction = observation.get("extraction")
    if not isinstance(extraction, dict):
        raise RuntimeError("missing extraction stale indicators")
    if "unchanged_reparses" not in extraction:
        raise RuntimeError("missing unchanged-reparse stale indicator")
    if extraction.get("stale_source") is True:
        raise RuntimeError("stale source indicator is true")
    return observation


def parity_view(observation):
    view = {field: observation.get(field) for field in PARITY_FIELDS}
    view["stale_indicators"] = {
        "source_digest": observation.get("source_digest"),
        # unchanged_reparses is retained diagnostic/gate telemetry. It is
        # expected to differ between cache-off and cache-on and is not a
        # correctness parity field.
        "extraction.stale_source": observation.get("extraction", {}).get("stale_source"),
    }
    return view


def assert_pair_parity(off, on):
    if parity_view(off) != parity_view(on):
        raise RuntimeError("OFF/ON correctness parity mismatch")


def run_arm(root, binary, config_path, config, binary_sha256, profile, arm, started, corpus_root):
    root = Path(root)
    copied_config = root / f"request-{profile}-{arm}.json"
    copied_config.write_bytes(Path(config_path).read_bytes())
    environment = runtime_environment(corpus_root)
    environment.update(
        ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG=str(copied_config),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT=str(root / f"observation-{profile}-{arm}.ndjson"),
        ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT_FORMAT="ndjson",
    )
    time_path = root / f"time-{profile}-{arm}.txt"
    command = [TIME_BINARY, "-v", "-o", str(time_path), "--", str(binary),
               "-test.run=^TestExtractionCorpusMeasurement$", "-test.count=1", "-test.v",
               f"-test.timeout={GO_TEST_TIMEOUT_SECONDS}s"]
    process_log = root / f"process-{profile}-{arm}.log"
    with process_log.open("wb") as log:
        process = subprocess.Popen(command, env=environment, cwd=root, stdout=log,
                                    stderr=subprocess.STDOUT, start_new_session=True)
        started.append(f"{profile}-{arm}")
        timed_out = False
        try:
            exit_code = process.wait(timeout=TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(process.pid, signal.SIGKILL)
            exit_code = process.wait()
    raw_time = time_path.read_text() if time_path.is_file() else ""
    rss_error = None
    peak_rss_bytes = None
    try:
        peak_rss_bytes = parse_peak_rss_bytes(raw_time)
    except ValueError as error:
        rss_error = str(error)
    save(root, f"process-{profile}-{arm}.json", {
        "command": command, "exit_code": exit_code, "timed_out": timed_out,
        "timeout_seconds": TIMEOUT_SECONDS, "peak_rss_bytes": peak_rss_bytes,
        "rss_status": "measured by /usr/bin/time -v" if rss_error is None else "invalid",
        "rss_error": rss_error, "time_output_path": str(time_path),
        "config_path": str(copied_config),
    })
    if timed_out or exit_code:
        raise RuntimeError(f"{profile} {arm} process failed or timed out")
    if rss_error is not None:
        raise RuntimeError(f"{profile} {arm} RSS measurement invalid; stop before next arm")
    observation_path = root / f"observation-{profile}-{arm}.ndjson"
    observation = json.loads(observation_path.read_text())
    return validate_observation(observation, config, binary_sha256)


def run_pair(profile, execute, before_on):
    off = execute(profile, "off")
    before_on()
    on = execute(profile, "on")
    assert_pair_parity(off, on)
    return off, on


def parse_args(argv):
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--config-dir", type=Path, required=True)
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
    if args.source_commit != EXPECTED_SOURCE_COMMIT or args.binary_sha256 != EXPECTED_BINARY_SHA256 or args.input_sha256 != EXPECTED_INPUT_SHA256:
        raise SystemExit("CLI identity does not match frozen correctness protocol")
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    if any(root.iterdir()):
        raise SystemExit("output directory must be empty; refusing overwrite")
    manifest_path, manifest, configs = load_configs(args.config_dir)
    binary = args.binary.resolve()
    source_root = args.source_root.resolve()
    scenario_script = args.scenario_script.resolve()
    corpus_root = args.corpus_root.resolve()
    identity = {
        "source_commit": EXPECTED_SOURCE_COMMIT, "binary_sha256": EXPECTED_BINARY_SHA256,
        "repository": EXPECTED_REPOSITORY, "repo_path": EXPECTED_REPO_PATH,
        "source_digest": EXPECTED_INPUT_SHA256, "provider_version": EXPECTED_PROVIDER,
        "manifest_sha256": sha256(manifest_path), "scenario_sha256": sha256(scenario_script),
        "binary": str(binary), "source_root": str(source_root),
        "source_commit_provenance": "caller-supplied source archive identity",
        "config_dir": str(Path(args.config_dir).resolve()), "config_files": {
            f"{profile}-{arm}": sha256(path) for (profile, arm), (path, _) in configs.items()
        },
        "rss_tool": TIME_BINARY, "rss_units": "kbytes converted to bytes",
        "timeout_seconds": TIMEOUT_SECONDS, "go_test_timeout_seconds": GO_TEST_TIMEOUT_SECONDS,
        "volatile_fields_excluded_from_pair_parity": list(VOLATILE_FIELDS),
    }
    save(root, "identity.json", identity)
    save(root, "environment.json", runtime_environment(corpus_root))
    started = []
    completed_profiles = []
    observations = {}
    issue = None
    baseline_fingerprint = None
    config_hashes = {key: sha256(path) for key, (path, _) in configs.items()}
    scenario_hash = sha256(scenario_script) if scenario_script.is_file() else None
    cache_checked = set()
    try:
        if not source_root.is_dir() or not binary.is_file() or not scenario_script.is_file():
            raise RuntimeError("source, binary, or scenario input unavailable")
        if not binary.is_relative_to(source_root):
            raise RuntimeError("binary is outside source root")
        if sha256(binary) != EXPECTED_BINARY_SHA256:
            raise RuntimeError("binary identity mismatch")
        if not Path(TIME_BINARY).is_file():
            raise RuntimeError("/usr/bin/time unavailable")
        for profile in EXPECTED_PROFILES:
            if sha256(binary) != EXPECTED_BINARY_SHA256:
                raise RuntimeError("binary changed before pair")
            pair_fingerprint = fingerprint(root, scenario_script, corpus_root, f"before-{profile}")
            if baseline_fingerprint is None:
                baseline_fingerprint = pair_fingerprint
            elif pair_fingerprint != baseline_fingerprint:
                raise RuntimeError(f"input changed before {profile} pair")

            def before_on():
                if sha256(binary) != EXPECTED_BINARY_SHA256:
                    raise RuntimeError(f"binary changed before {profile} ON")
                between = fingerprint(root, scenario_script, corpus_root, f"between-{profile}")
                if between != pair_fingerprint:
                    raise RuntimeError(f"input changed before {profile} ON")

            def execute(current_profile, arm):
                path, config = configs[(current_profile, arm)]
                if sha256(path) != config_hashes[(current_profile, arm)]:
                    raise RuntimeError(f"request config changed before {current_profile} {arm}")
                if sha256(scenario_script) != scenario_hash:
                    raise RuntimeError(f"scenario script changed before {current_profile} {arm}")
                cache_path = Path(config["cache_dir"])
                if (current_profile, arm) not in cache_checked:
                    if cache_path.exists():
                        raise RuntimeError(f"cache path was not fresh before {current_profile} {arm}")
                    cache_checked.add((current_profile, arm))
                return run_arm(root, binary, path, config, EXPECTED_BINARY_SHA256,
                               current_profile, arm, started, corpus_root)

            # Store the validated OFF row before attempting ON so a failed ON
            # retains the useful first-arm evidence in the outcome.
            off = execute(profile, "off")
            observations[f"{profile}-off"] = off
            before_on()
            on = execute(profile, "on")
            observations[f"{profile}-on"] = on
            assert_pair_parity(off, on)
            completed_profiles.append(profile)
    except Exception as error:
        issue = str(error)
    finally:
        if baseline_fingerprint is not None:
            try:
                if sha256(binary) != EXPECTED_BINARY_SHA256:
                    raise RuntimeError("binary changed after diagnostic")
                after = fingerprint(root, scenario_script, corpus_root, "after")
                if after != baseline_fingerprint:
                    after_issue = "input changed after diagnostic"
                    issue = after_issue if issue is None else issue + "; " + after_issue
            except Exception as error:
                after_issue = str(error)
                issue = after_issue if issue is None else issue + "; " + after_issue
        save(root, "outcome.json", {
            "status": "issue" if issue else "all_three_pairs_semantically_equal",
            "issue": issue, "processes_started": started,
            "completed_pairs": completed_profiles,
            "unrun": [f"{profile}-{arm}" for profile, arm in ARM_ORDER if f"{profile}-{arm}" not in started],
            "observations": observations,
            "scope": "one retained correctness pair per profile; no performance score or campaign",
        })
    if issue:
        raise SystemExit(issue)


if __name__ == "__main__":
    main()
