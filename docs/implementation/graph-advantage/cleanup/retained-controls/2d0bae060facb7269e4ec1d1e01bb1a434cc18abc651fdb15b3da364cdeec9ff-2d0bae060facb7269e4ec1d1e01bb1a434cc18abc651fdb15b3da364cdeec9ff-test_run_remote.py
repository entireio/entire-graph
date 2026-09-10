import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("relations_cpu_collector", HERE / "run_remote.py")
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class FakeProcess:
    pid = 9123

    def __init__(self, timeout=False):
        self.timeout = timeout
        self.calls = 0

    def wait(self, timeout=None):
        self.calls += 1
        if self.timeout and self.calls == 1:
            raise subprocess.TimeoutExpired("fake evaluator", timeout)
        return 137 if self.timeout else 0


class RelationsCPUProfileTests(unittest.TestCase):
    def _complete(self, root, payload=b"profile"):
        profile = root / collector.RELATIONS_CPU_PROFILE_NAME
        profile.write_bytes(payload)
        status = {
            "status": "complete",
            "trigger_elapsed_ns": 3_000_000_000,
            "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS,
            "actual_ns": 20_000_000_000,
            "bytes": len(payload),
            "sha256": hashlib.sha256(payload).hexdigest(),
            "diagnostic_only": True,
            "no_performance_claim": True,
            "profile_overhead_present": True,
        }
        (root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME).write_text(json.dumps(status))
        return status

    def test_runtime_enables_opt_in_and_preserves_offline_controls(self):
        environment = collector.runtime_environment("/opt/p1/corpus")
        self.assertEqual(environment[collector.RELATIONS_CPU_PROFILE_ENV], "1")
        self.assertEqual(environment[collector.PHASE_BREADCRUMBS_ENV], "1")
        self.assertEqual(environment["GOPROXY"], "off")

    def test_complete_profile_requires_fixed_window_markers_and_bounded_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._complete(root)
            metadata = collector.validate_cpu_profile_artifacts(root, require_complete=True)
            self.assertEqual(metadata["validation"], "complete")
            self.assertEqual(metadata["bytes"], len(b"profile"))
            self.assertEqual(metadata["sha256"], hashlib.sha256(b"profile").hexdigest())
            self.assertIn(collector.RELATIONS_CPU_PROFILE_NAME, collector.existing_artifact_hashes(root))
            self.assertIn(collector.RELATIONS_CPU_PROFILE_STATUS_NAME, collector.existing_artifact_hashes(root))

    def test_profile_and_status_symlinks_are_rejected_without_following(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "outside"
            target.write_bytes(b"secret")
            (root / collector.RELATIONS_CPU_PROFILE_NAME).symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, "symlink"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)
            (root / collector.RELATIONS_CPU_PROFILE_NAME).unlink()
            (root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME).symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, "symlink"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)
            self.assertNotIn(collector.RELATIONS_CPU_PROFILE_NAME, collector.existing_artifact_hashes(root))
            self.assertNotIn(collector.RELATIONS_CPU_PROFILE_STATUS_NAME, collector.existing_artifact_hashes(root))

    def test_incomplete_artifacts_are_bounded_and_status_types_are_strict(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            profile = root / collector.RELATIONS_CPU_PROFILE_NAME
            profile.write_bytes(b"x" * (collector.RELATIONS_CPU_PROFILE_MAX_BYTES + 1))
            status = root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME
            status.write_text(json.dumps({"status": "active", "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS}))
            with self.assertRaisesRegex(RuntimeError, "profile exceeds"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)
            profile.unlink()
            status.write_bytes(b"{" + b"x" * collector.RELATIONS_CPU_PROFILE_STATUS_MAX_BYTES)
            with self.assertRaisesRegex(RuntimeError, "status exceeds"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)
            status.write_text(json.dumps({"status": "active", "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS, "bytes": True}))
            with self.assertRaisesRegex(RuntimeError, "bytes is invalid"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)
            status.write_text(json.dumps({"status": "unknown", "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS}))
            with self.assertRaisesRegex(RuntimeError, "unknown"):
                collector.validate_cpu_profile_artifacts(root, require_complete=False)

    def test_complete_profile_requires_positive_actual_duration(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._complete(root)
            status = root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME
            value = json.loads(status.read_text())
            value["actual_ns"] = 0
            status.write_text(json.dumps(value))
            with self.assertRaisesRegex(RuntimeError, "actual duration"):
                collector.validate_cpu_profile_artifacts(root, require_complete=True)

    def test_profile_hash_size_and_window_mismatch_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._complete(root)
            status_path = root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME
            status = json.loads(status_path.read_text())
            status["sha256"] = "0" * 64
            status_path.write_text(json.dumps(status))
            with self.assertRaisesRegex(RuntimeError, "SHA-256"):
                collector.validate_cpu_profile_artifacts(root, require_complete=True)
            status["sha256"] = hashlib.sha256(b"profile").hexdigest()
            status["window_ns"] = collector.RELATIONS_CPU_PROFILE_WINDOW_NS + 1
            status_path.write_text(json.dumps(status))
            with self.assertRaisesRegex(RuntimeError, "window"):
                collector.validate_cpu_profile_artifacts(root, require_complete=True)

    def test_timeout_retains_incomplete_profile_status_without_promoting(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            status = {"status": "active", "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS,
                      "diagnostic_only": True, "no_performance_claim": True,
                      "profile_overhead_present": True, "trigger_elapsed_ns": 89_000_000_000}
            (root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME).write_text(json.dumps(status))
            killed = []

            def process_factory(command, **kwargs):
                kwargs["stdout"].write(b"relations phase\n")
                Path(command[command.index("-o") + 1]).write_text("Maximum resident set size (kbytes): 9\n")
                return FakeProcess(timeout=True)

            with self.assertRaisesRegex(RuntimeError, "timed out"):
                collector.run_process(root, root / "evaluator", {}, process_factory,
                                      lambda pid, signal: killed.append((pid, signal)))
            retained = collector.validate_cpu_profile_artifacts(root, require_complete=False)
            self.assertEqual(retained["status"], "active")
            self.assertEqual(retained["validation"], "retained_incomplete")
            self.assertEqual(len(killed), 1)
            self.assertTrue((root / "process.json").is_file())

    def test_timeout_can_retain_complete_profile_metadata_without_promotion(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self._complete(root, payload=b"complete-before-timeout")
            killed = []

            def process_factory(command, **kwargs):
                Path(command[command.index("-o") + 1]).write_text("Maximum resident set size (kbytes): 9\n")
                return FakeProcess(timeout=True)

            with self.assertRaisesRegex(RuntimeError, "timed out"):
                collector.run_process(root, root / "evaluator", {}, process_factory,
                                      lambda pid, signal: killed.append((pid, signal)))
            metadata = collector.validate_cpu_profile_artifacts(root, require_complete=False)
            self.assertEqual(metadata["validation"], "complete")
            self.assertEqual(metadata["bytes"], len(b"complete-before-timeout"))
            self.assertEqual(len(killed), 1)

    def test_missed_safe_window_allows_late_trigger_and_is_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            status = {"status": "missed_safe_window", "trigger_elapsed_ns": 91_000_000_000,
                      "window_ns": collector.RELATIONS_CPU_PROFILE_WINDOW_NS,
                      "diagnostic_only": True, "no_performance_claim": True,
                      "profile_overhead_present": True}
            (root / collector.RELATIONS_CPU_PROFILE_STATUS_NAME).write_text(json.dumps(status))
            metadata = collector.validate_cpu_profile_artifacts(root, require_complete=False)
            self.assertEqual(metadata["status"], "missed_safe_window")
            self.assertEqual(metadata["validation"], "retained_incomplete")

    def test_missing_profile_is_rejected_for_completed_request_but_described_on_timeout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "status is missing"):
                collector.validate_cpu_profile_artifacts(root, require_complete=True)
            metadata = collector.validate_cpu_profile_artifacts(root, require_complete=False)
            self.assertEqual(metadata["validation"], "missing")

    def test_main_refuses_pending_source_and_binary_before_any_request(self):
        with mock.patch.object(collector, "EXPECTED_SOURCE_COMMIT", "<PENDING_TEST_SOURCE_COMMIT>"), \
             mock.patch.object(collector, "run_request") as request:
            with self.assertRaisesRegex(SystemExit, "placeholders"):
                collector.main([
                    "--output", "/tmp/relations-cpu-unused",
                    "--binary", "/tmp/relations-cpu-binary",
                    "--binary-sha256", "a" * 64,
                    "--source-root", "/tmp/relations-cpu-source",
                    "--source-commit", "b" * 40,
                    "--scenario-script", "/tmp/relations-cpu-scenario.py",
                    "--build-manifest", "/tmp/relations-cpu-build.json",
                    "--batch-manifest", "/tmp/relations-cpu-batch.json",
                    "--batch-worker", "1", "--dry-preflight",
                    "--input-sha256", "c" * 64,
                    "--input-manifest-sha256", "d" * 64,
                ])
            request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
