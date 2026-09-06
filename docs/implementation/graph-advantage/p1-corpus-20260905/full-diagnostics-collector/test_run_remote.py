import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import subprocess

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("collector", HERE / "run_remote.py")
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class FakeProcess:
    def __init__(self, exit_code=0):
        self.pid = 4242
        self.exit_code = exit_code

    def wait(self, timeout=None):
        return self.exit_code


class TimeoutProcess(FakeProcess):
    def __init__(self):
        super().__init__(exit_code=137)
        self.waits = 0

    def wait(self, timeout=None):
        self.waits += 1
        if self.waits == 1:
            raise subprocess.TimeoutExpired("fake", timeout)
        return self.exit_code


def fixture_artifacts(manifest, observation_path, binary_sha256, partial_count=194):
    failures = [
        {"code": f"E_{i:03d}", "severity": "warning", "file_path": f"f-{i:03d}.go",
         "effect_on_semantic_completeness": "partial", "detail": f"detail {i}"}
        for i in range(partial_count)
    ]
    warnings = [{"code": "W_WORKTREE_SNAPSHOT", "severity": "warning",
                 "effect_on_semantic_completeness": "none", "detail": "fixture"}]
    common = {
        "format_version": 1, "manifest_version": 1,
        "manifest_path": str(Path(manifest["_path"]).resolve()), "repository": collector.EXPECTED_REPOSITORY,
        "repository_path": collector.EXPECTED_REPO_PATH, "operation": "snapshot",
        "mode": "measure", "cache_mode": "off", "profile": collector.EXPECTED_PROFILE,
        "provider_version": collector.EXPECTED_PROVIDER, "mutation_id": collector.EXPECTED_MUTATION_ID,
        "source_digest": collector.EXPECTED_SOURCE_DIGEST, "binary_sha256": binary_sha256,
        "scenario": collector.EXPECTED_SCENARIO, "trial": 0, "reuse": False,
        "verb": "snapshot", "status": "partial", "semantic_sha256": collector.EXPECTED_SEMANTIC_SHA256,
        "semantic_digest": collector.EXPECTED_SEMANTIC_SHA256,
        "partial_failures_count": collector.EXPECTED_PARTIAL_FAILURES_COUNT,
        "partial_failures_sha256": collector.EXPECTED_PARTIAL_FAILURES_SHA256,
        "warnings_count": collector.EXPECTED_WARNINGS_COUNT,
        "warnings_sha256": collector.EXPECTED_WARNINGS_SHA256,
    }
    observation = dict(common)
    observation.update({
        "partial_failures": failures[:32], "warnings": warnings,
    })
    diagnostics = dict(common)
    diagnostics.update({
        "observation_path": str(Path(observation_path).resolve()), "partial_failures": failures,
        "warnings": warnings,
    })
    return observation, diagnostics


class CollectorTests(unittest.TestCase):
    def test_fingerprint_root_cannot_differ_from_product_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "fingerprint corpus root"):
                collector.run_request(root / "output", root / "binary", root / "scenario",
                                      root / "other-corpus", "b" * 64, root)
            self.assertFalse((root / "output").exists())

    def test_raw_json_loader_rejects_duplicate_root_and_trailing_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            duplicate = root / "duplicate.json"
            duplicate.write_text('{"partial_failures": [], "partial_failures": []}')
            with self.assertRaisesRegex(RuntimeError, "duplicate root key"):
                collector.read_json_object_with_raw(duplicate)
            trailing = root / "trailing.json"
            trailing.write_text('{"partial_failures": []} trailing')
            with self.assertRaisesRegex(RuntimeError, "trailing JSON"):
                collector.read_json_object_with_raw(trailing)

    def test_full_diagnostics_requires_exact_artifact_count(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation_path = root / "observation.ndjson"
            observation, diagnostics = fixture_artifacts(manifest, observation_path, "b" * 64)
            diagnostics["partial_failures"] = diagnostics["partial_failures"][:-1]
            with self.assertRaisesRegex(RuntimeError, "full partial-failure artifact count"):
                collector.validate_diagnostics(diagnostics, observation_path, manifest, "b" * 64)

    def test_full_diagnostics_can_check_exact_raw_go_array_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation_path = root / "observation.ndjson"
            observation, diagnostics = fixture_artifacts(manifest, observation_path, "b" * 64)
            raw_partial = json.dumps(diagnostics["partial_failures"], separators=(",", ":")).encode()
            raw_warnings = json.dumps(diagnostics["warnings"], separators=(",", ":")).encode()
            diagnostics["partial_failures_sha256"] = hashlib.sha256(raw_partial).hexdigest()
            diagnostics["warnings_sha256"] = hashlib.sha256(raw_warnings).hexdigest()
            raw_values = {"partial_failures": raw_partial, "warnings": raw_warnings}
            result = collector.validate_diagnostics(
                diagnostics, observation_path, manifest, "b" * 64, raw_values,
                diagnostics["partial_failures_sha256"], diagnostics["warnings_sha256"],
            )
            self.assertEqual(result, {"partial_failures": 194, "warnings": 1})

    def test_identity_drift_is_rejected_before_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation, _ = fixture_artifacts(manifest, root / "observation.ndjson", "b" * 64)
            observation["source_digest"] = "d" * 64
            with self.assertRaisesRegex(RuntimeError, "source_digest"):
                collector.validate_observation(observation, manifest, "b" * 64)

    def test_one_request_stops_on_unverified_full_digest_without_repetition(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            calls = []

            def fingerprints(output, script, corpus_root, stage):
                calls.append(stage)
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                (Path(output) / f"{stage}.log").write_text("")
                return value

            def process_factory(command, **kwargs):
                calls.append("process")
                output = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"])
                manifest_path = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST"])
                manifest = json.loads(manifest_path.read_text())
                manifest["_path"] = str(manifest_path)
                observation, diagnostics = fixture_artifacts(manifest, output, binary_sha)
                output.write_text(json.dumps(observation))
                (output.parent / "diagnostics.json").write_text(json.dumps(diagnostics))
                time_path = Path(command[command.index("-o") + 1])
                time_path.write_text("Maximum resident set size (kbytes): 7\n")
                return FakeProcess()

            result = collector.run_request(
                root / "result", binary, scenario, Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                fingerprint_fn=fingerprints, process_factory=process_factory,
            )
            self.assertEqual(result["status"], "issue")
            self.assertIn("full partial-failure digest", result["issue"])
            self.assertEqual(calls, ["before", "process", "after"])
            outcome = json.loads((root / "result" / "outcome.json").read_text())
            self.assertFalse(outcome["admission_eligible"])
            self.assertEqual(outcome["review_status"], "not_reviewed")

    def test_process_failure_stops_without_claiming_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            calls = []

            def fingerprints(output, script, corpus_root, stage):
                calls.append(stage)
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                return value

            def process_factory(command, **kwargs):
                calls.append("process")
                time_path = Path(command[command.index("-o") + 1])
                time_path.write_text("Maximum resident set size (kbytes): 7\n")
                return FakeProcess(exit_code=9)

            result = collector.run_request(
                root / "result", binary, scenario, Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                fingerprint_fn=fingerprints, process_factory=process_factory,
            )
            self.assertEqual(result["status"], "issue")
            self.assertIn("exit 9", result["issue"])
            self.assertEqual(calls, ["before", "process", "after"])
            self.assertFalse((root / "result" / "diagnostics.json").exists())

    def test_timeout_kills_process_group_and_stops(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "evaluator"
            binary.write_bytes(b"fake")
            killed = []
            environment = {"ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT": str(root / "observation.ndjson")}

            def process_factory(command, **kwargs):
                Path(command[command.index("-o") + 1]).write_text(
                    "Maximum resident set size (kbytes): 7\n"
                )
                return TimeoutProcess()

            with self.assertRaisesRegex(RuntimeError, "timed out"):
                collector.run_process(
                    root, binary, environment, process_factory,
                    lambda pid, signal: killed.append((pid, signal)),
                )
            self.assertEqual(killed, [(4242, collector.signal.SIGKILL)])


if __name__ == "__main__":
    unittest.main()
