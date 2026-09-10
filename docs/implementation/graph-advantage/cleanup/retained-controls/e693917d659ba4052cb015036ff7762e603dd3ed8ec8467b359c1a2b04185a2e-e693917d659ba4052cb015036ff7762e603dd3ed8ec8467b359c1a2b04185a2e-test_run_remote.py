import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("postfix_collector", HERE / "run_remote.py")
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class FakeProcess:
    pid = 4242

    def __init__(self, exit_code=0):
        self.exit_code = exit_code
        self.waits = 0

    def wait(self, timeout=None):
        self.waits += 1
        if self.exit_code == 137 and self.waits == 1:
            raise subprocess.TimeoutExpired("fake", timeout)
        return self.exit_code


class FakeLedger:
    def __init__(self):
        self.calls = []

    def start(self, cell_id, phase):
        self.calls.append(("start", cell_id, phase))

    def finish(self, cell_id, phase, status):
        self.calls.append(("finish", cell_id, phase, status))


def common_identity(manifest_path, binary_sha, status="partial"):
    return {
        "manifest_path": str(Path(manifest_path).resolve()),
        "repository": collector.EXPECTED_REPOSITORY,
        "repository_path": collector.EXPECTED_REPO_PATH,
        "operation": "snapshot", "mode": "measure", "cache_mode": "off",
        "profile": "full", "provider_version": collector.EXPECTED_PROVIDER,
        "mutation_id": collector.EXPECTED_MUTATION_ID,
        "source_digest": collector.EXPECTED_SOURCE_DIGEST,
        "binary_sha256": binary_sha, "scenario": collector.EXPECTED_SCENARIO,
        "trial": 0, "reuse": False, "verb": "snapshot", "status": status,
    }


class PostFixCollectorTests(unittest.TestCase):
    def test_runtime_enables_phase_breadcrumbs(self):
        environment = collector.runtime_environment("/opt/p1/corpus")
        self.assertEqual(environment[collector.PHASE_BREADCRUMBS_ENV], "1")

    def test_full_payload_is_unreviewed_without_old_result_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest_path = root / "manifest.json"
            manifest_path.write_text("{}")
            manifest = {"_path": str(manifest_path)}
            binary_sha = "a" * 64
            failures = [{"code": "E_PARSE_ERROR", "severity": "error"}]
            warnings = [{"code": "W_WORKTREE_SNAPSHOT", "severity": "warning"}]
            raw_failures = json.dumps(failures, separators=(",", ":")).encode()
            raw_warnings = json.dumps(warnings, separators=(",", ":")).encode()
            common = common_identity(manifest_path, binary_sha)
            failure_digest = hashlib.sha256(raw_failures).hexdigest()
            warning_digest = hashlib.sha256(raw_warnings).hexdigest()
            observation = dict(common, partial_failures_count=1, warnings_count=1,
                               semantic_sha256="b" * 64, semantic_digest="b" * 64,
                               partial_failures_sha256=failure_digest,
                               warnings_sha256=warning_digest)
            diagnostics = dict(common, observation_path=str((root / "observation.ndjson").resolve()),
                               partial_failures_count=1, warnings_count=1,
                               partial_failures_sha256=failure_digest,
                               warnings_sha256=warning_digest,
                               semantic_sha256="b" * 64, semantic_digest="b" * 64,
                               partial_failures=failures, warnings=warnings)
            collector.validate_observation(observation, manifest, binary_sha)
            result = collector.validate_diagnostics(
                diagnostics, observation, root / "observation.ndjson", manifest,
                binary_sha, {"partial_failures": raw_failures, "warnings": raw_warnings},
            )
            self.assertEqual(result, {"partial_failures": 1, "warnings": 1})

    def test_error_without_complete_diagnostics_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest_path = root / "manifest.json"
            manifest_path.write_text("{}")
            manifest = {"_path": str(manifest_path)}
            binary_sha = "a" * 64
            observation = dict(common_identity(manifest_path, binary_sha, status="error"),
                               partial_failures_count=1, warnings_count=0,
                               partial_failures_sha256="a" * 64,
                               warnings_sha256="b" * 64, error="request failed")
            collector.validate_observation(observation, manifest, binary_sha)
            with self.assertRaisesRegex(RuntimeError, "full partial-failure artifact count"):
                collector.validate_diagnostics(
                    dict(observation, observation_path=str((root / "observation.ndjson").resolve())),
                    observation, root / "observation.ndjson", manifest, binary_sha, {},
                )

    def test_timeout_retains_captured_process_log(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "evaluator"
            binary.write_bytes(b"fake")
            killed = []

            def process_factory(command, **kwargs):
                kwargs["stdout"].write(b"ordinary output\n{\"event\":\"snapshot_phase_start\"}\n")
                Path(command[command.index("-o") + 1]).write_text("Maximum resident set size (kbytes): 7\n")
                return FakeProcess(exit_code=137)

            with self.assertRaisesRegex(RuntimeError, "timed out"):
                collector.run_process(
                    root, binary, {}, process_factory,
                    lambda pid, signal: killed.append((pid, signal)),
                )
            self.assertEqual(len(killed), 1)
            self.assertIn("snapshot_phase_start", (root / "process.log").read_text())
            self.assertTrue((root / "process.json").is_file())

    def test_cross_artifact_digest_mismatch_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest_path = root / "manifest.json"
            manifest_path.write_text("{}")
            manifest = {"_path": str(manifest_path)}
            binary_sha = "a" * 64
            raw_failures = b"[]"
            raw_warnings = b"[]"
            observation = dict(common_identity(manifest_path, binary_sha),
                               partial_failures_count=0, warnings_count=0,
                               partial_failures_sha256=hashlib.sha256(raw_failures).hexdigest(),
                               warnings_sha256=hashlib.sha256(raw_warnings).hexdigest(),
                               semantic_sha256="b" * 64, semantic_digest="b" * 64)
            diagnostics = dict(observation, observation_path=str((root / "observation.ndjson").resolve()),
                               partial_failures=[], warnings=[],
                               semantic_digest="c" * 64)
            with self.assertRaisesRegex(RuntimeError, "identity differs"):
                collector.validate_diagnostics(diagnostics, observation,
                                               root / "observation.ndjson", manifest,
                                               binary_sha, {"partial_failures": raw_failures,
                                                            "warnings": raw_warnings})

    def test_error_observation_is_issue_and_finishes_failed_ledger(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            source.mkdir()
            binary = source / "p1-evaluator"
            binary.write_bytes(b"binary")
            scenario = root / "p1_scenario.py"
            scenario.write_text("# scenario\n")
            build = root / "build.json"
            build.write_text(json.dumps({"source_commit": collector.EXPECTED_SOURCE_COMMIT,
                                         "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                                         "exit_code": 0}))
            batch = root / "batch.json"
            batch.write_text("{}")
            ledger = FakeLedger()
            binary_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            cell = {"cell_id": "cell"}
            batch_document = {"batch_id": "batch", "manifest_sha256": "c" * 64}
            raw_failures = b"[]"
            raw_warnings = b"[]"

            def fake_process(output, *_args):
                manifest_path = Path(output) / "manifest.json"
                observation = dict(common_identity(manifest_path, binary_sha, status="error"),
                                   partial_failures_count=0, warnings_count=0,
                                   partial_failures_sha256=hashlib.sha256(raw_failures).hexdigest(),
                                   warnings_sha256=hashlib.sha256(raw_warnings).hexdigest(),
                                   error="product request failed")
                diagnostics = dict(observation,
                                   observation_path=str((Path(output) / "observation.ndjson").resolve()),
                                   partial_failures=[], warnings=[])
                (Path(output) / "observation.ndjson").write_text(json.dumps(observation))
                (Path(output) / "diagnostics.json").write_text(json.dumps(diagnostics))
                (Path(output) / "process.log").write_text("request failed\n")
                return {"exit_code": 0, "peak_rss_bytes": 1}

            with mock.patch.object(collector, "claim_diagnostic_budget",
                                   return_value=(batch_document, cell, ledger,
                                                 root / "claim.json", {})), \
                 mock.patch.object(collector, "run_process", side_effect=fake_process):
                result = collector.run_request(
                    root / "output", binary, scenario, Path("/opt/p1/corpus"), binary_sha,
                    source, build, batch, 1,
                    fingerprint_fn=lambda *args: {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST},
                    _claim_root=root / "claims",
                )
            self.assertEqual(result["status"], "issue")
            self.assertIn("explicit request error", result["issue"])
            self.assertEqual(ledger.calls[-1][-1], "error")
            self.assertEqual(json.loads((root / "output" / "outcome.json").read_text())["status"], "issue")

    def test_post_request_identity_retains_binary_drift_for_timeout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            source.mkdir()
            binary = source / "p1-evaluator"
            binary.write_bytes(b"before")
            scenario = root / "scenario.py"
            scenario.write_text("# scenario\n")
            build = root / "build.json"
            build.write_text(json.dumps({"source_commit": collector.EXPECTED_SOURCE_COMMIT,
                                         "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                                         "exit_code": 0}))
            batch = root / "batch.json"
            batch.write_text("{}")
            expected = {
                "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                "scenario_sha256": collector.sha256(scenario),
                "build_manifest_sha256": collector.sha256(build),
                "batch_manifest_sha256": collector.sha256(batch),
                "runner_sha256": collector.sha256(Path(collector.__file__)),
                "budget_sha256": collector.sha256(Path(collector.__file__).with_name("batch_budget.py")),
                "gate_sha256": collector.sha256(Path(collector.__file__).with_name("campaign_gate.py")),
            }
            binary.write_bytes(b"after-timeout")
            record = collector.post_request_identity(root, binary, scenario, source, build, batch, expected)
            self.assertIn("binary_sha256", record["mismatches"])
            self.assertTrue((root / "after-identity.json").is_file())

    def test_placeholder_execution_refuses_before_cloud_or_process(self):
        with mock.patch.object(collector, "EXPECTED_SOURCE_COMMIT", "<PENDING_INTEGRATION_SHA>"), \
             mock.patch.object(collector, "run_request") as run_request, \
             self.assertRaisesRegex(SystemExit, "placeholders"):
            collector.main([
                "--output", "/tmp/unused", "--binary", "/tmp/unused", "--binary-sha256", "a" * 64,
                "--source-root", "/tmp", "--source-commit", "<PENDING_INTEGRATION_SHA>",
                "--scenario-script", "/tmp/unused", "--build-manifest", "/tmp/unused",
                "--batch-manifest", "/tmp/unused", "--batch-worker", "1",
                "--input-sha256", collector.EXPECTED_SOURCE_DIGEST,
                "--input-manifest-sha256", collector.EXPECTED_INPUT_MANIFEST_SHA256,
            ])
        run_request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
