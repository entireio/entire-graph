import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest
import unittest.mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("archive_full_check_controller", HERE / "controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class CanonicalFullCheckControllerTests(unittest.TestCase):
    def setUp(self):
        self.package = HERE.parent
        self.evidence = self.package.parent
        self.document = json.loads((HERE / "manifest.json").read_text())
        self.gate = json.loads((self.package / "full-check-gate.json").read_text())
        self.build = self.package / "build-manifest.json"

    def test_real_artifacts_validate_one_unclaimed_diagnostic(self):
        self.assertFalse((HERE / "dispatch-claim.json").exists())
        value = self._validate_gate(self.package, self.gate)
        self.assertEqual(value["schema"], "full-check-canonical-v1")
        self.assertFalse(value["admission_eligible"])
        self.assertFalse((HERE / "dispatch-claim.json").exists())

    def _copied_gate(self):
        temporary = tempfile.TemporaryDirectory(dir=self.evidence)
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        gate = copy.deepcopy(self.gate)
        for field in ("canonical_run_path", "canonical_verification_path"):
            source = (self.package / gate[field]).resolve()
            target = root / source.name
            shutil.copy2(source, target)
            gate[field] = os.path.relpath(target, self.package)
        return root, gate

    def _validate_gate(self, root, gate):
        gate_path = root / "gate.json"
        gate_path.write_text(json.dumps(gate, indent=2) + "\n")
        document = copy.deepcopy(self.document)
        document["full_check_gate_sha256"] = controller.sha256(gate_path)
        return controller._validate_scoped_full_check_gate(document, gate_path, self.build)

    def _rewrite_json_artifact(self, gate, path_field, hash_field, mutation):
        path = (self.package / gate[path_field]).resolve()
        value = json.loads(path.read_text())
        mutation(value)
        path.write_text(json.dumps(value, indent=2) + "\n")
        gate[hash_field] = controller.sha256(path)

    def test_rejects_false_integer_exit_and_changed_canonical_hash(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "canonical_run_path", "canonical_run_sha256",
            lambda run: run["source_records"]["result.json"].__setitem__("remote_overall_exit", False),
        )
        with self.assertRaisesRegex(RuntimeError, "remote_overall_exit"):
            self._validate_gate(root, gate)
        root, gate = self._copied_gate()
        gate["canonical_run_sha256"] = "f" * 64
        with self.assertRaisesRegex(RuntimeError, "canonical run artifact is missing or changed"):
            self._validate_gate(root, gate)

    def test_rejects_false_statusline_failed_count(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "canonical_run_path", "canonical_run_sha256",
            lambda run: run["source_records"]["result.json"]["task_results"]["test_statusline"].__setitem__("failed", False),
        )
        with self.assertRaisesRegex(RuntimeError, "statusline result"):
            self._validate_gate(root, gate)

    def test_rejects_altered_source_identity_and_provenance(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "canonical_run_path", "canonical_run_sha256",
            lambda run: run["source_records"]["result.json"].__setitem__("before_after_identity_equal", False),
        )
        with self.assertRaisesRegex(RuntimeError, "before_after_identity_equal"):
            self._validate_gate(root, gate)
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "canonical_run_path", "canonical_run_sha256",
            lambda run: run["source_records"]["source-provenance.json"].__setitem__("source_commit", "0" * 40),
        )
        with self.assertRaisesRegex(RuntimeError, "source provenance mismatch"):
            self._validate_gate(root, gate)

    def test_rejects_missing_required_canonical_task_with_refreshed_binding(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "canonical_run_path", "canonical_run_sha256",
            lambda run: run["source_records"]["result.json"]["ordered_task_commands"].remove(
                "[build] $ go build -o entire-graph ./cmd/entire-graph"
            ),
        )
        with self.assertRaisesRegex(RuntimeError, "ordered task commands changed"):
            self._validate_gate(root, gate)

    def test_control_archive_has_only_prepared_inputs(self):
        with tarfile.open(self.package / "control-files.tar.gz", "r:gz") as archive:
            names = set(archive.getnames())
        self.assertIn("full-check-gate.json", names)
        self.assertFalse(any("claim" in name or name.startswith("raw/") or name.startswith("results") for name in names))

    def test_leaf_collector_dry_preflight_uses_exact_pins(self):
        collector_path = self.package / "collector/run_remote.py"
        collector_spec = importlib.util.spec_from_file_location("archive_full_check_collector", collector_path)
        collector = importlib.util.module_from_spec(collector_spec)
        collector_spec.loader.exec_module(collector)
        self.assertEqual(collector.EXPECTED_SOURCE_COMMIT, self.document["source_commit"])
        self.assertEqual(collector.EXPECTED_BINARY_SHA256, self.document["binary_sha256"])
        batch = controller._validate_batch(self.document, self.package / "batch-manifest.json", self.build)
        self.assertEqual(batch["workers"], {"1": 1})
        self.assertEqual(batch["cells"][0]["arms"], [False])

    def test_late_profile_delay_is_explicit_and_status_mismatch_is_rejected(self):
        controller._validate_cpu_profile_contract(self.document)
        for mismatch in (87_999_999_999, 88_000_000_000.0, True):
            with self.subTest(manifest_delay=mismatch):
                changed = copy.deepcopy(self.document)
                changed["cpu_profile"]["requested_start_after_ns"] = mismatch
                with self.assertRaisesRegex(RuntimeError, "CPU profile delay contract"):
                    controller._validate_cpu_profile_contract(changed)

        archived = controller._archive_members(self.package / "control-files.tar.gz")
        controller._validate_archived_runtime_pins(self.document, archived)
        original_pin = b"RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS = 88_000_000_000"
        for mismatch in (b"88_000_000_000.0", b"True"):
            with self.subTest(archived_delay=mismatch.decode("ascii")):
                changed = dict(archived)
                changed["collector/run_remote.py"] = changed["collector/run_remote.py"].replace(
                    original_pin,
                    b"RELATIONS_CPU_PROFILE_REQUESTED_START_AFTER_NS = " + mismatch,
                    1,
                )
                with self.assertRaisesRegex(RuntimeError, "runtime pin has the wrong type"):
                    controller._validate_archived_runtime_pins(self.document, changed)

        collector_path = self.package / "collector/run_remote.py"
        collector_spec = importlib.util.spec_from_file_location("late_profile_collector", collector_path)
        collector = importlib.util.module_from_spec(collector_spec)
        collector_spec.loader.exec_module(collector)
        with unittest.mock.patch.dict(
            os.environ,
            {collector.RELATIONS_CPU_PROFILE_START_AFTER_ENV: "1"},
        ):
            environment = collector.runtime_environment(Path("/opt/p1/corpus"))
        self.assertEqual(
            environment[collector.RELATIONS_CPU_PROFILE_START_AFTER_ENV],
            "88000000000",
        )

        with tempfile.TemporaryDirectory(dir=self.evidence) as temporary:
            output = Path(temporary)
            profile = b"profile"
            (output / collector.RELATIONS_CPU_PROFILE_NAME).write_bytes(profile)
            status = {
                "status": "complete",
                "requested_start_after_ns": 88_000_000_000,
                "actual_start_elapsed_ns": 88_000_000_000,
                "trigger_elapsed_ns": 88_000_000_000,
                "window_ns": 20_000_000_000,
                "actual_ns": 20_000_000_000,
                "bytes": len(profile),
                "sha256": hashlib.sha256(profile).hexdigest(),
                "relations_first": 100,
                "relations_latest": 200,
                "relation_progress_events": 2,
                "diagnostic_only": True,
                "no_performance_claim": True,
                "profile_overhead_present": True,
            }
            status_path = output / collector.RELATIONS_CPU_PROFILE_STATUS_NAME
            status_path.write_text(json.dumps(status) + "\n")
            metadata = collector.validate_cpu_profile_artifacts(output, require_complete=True)
            self.assertEqual(metadata["actual_start_elapsed_ns"], 88_000_000_000)
            status["actual_start_elapsed_ns"] = 87_999_999_999
            status_path.write_text(json.dumps(status) + "\n")
            with self.assertRaisesRegex(RuntimeError, "actual start"):
                collector.validate_cpu_profile_artifacts(output, require_complete=True)


if __name__ == "__main__":
    unittest.main()
