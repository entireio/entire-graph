import copy
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("archive_full_check_controller", HERE / "controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class ArchiveFullCheckControllerTests(unittest.TestCase):
    def setUp(self):
        self.package = HERE.parent
        self.evidence = self.package.parent
        self.document = json.loads((HERE / "manifest.json").read_text())
        self.gate = json.loads((self.package / "full-check-gate.json").read_text())
        self.build = self.package / "build-manifest.json"

    def test_real_artifacts_validate_one_unclaimed_diagnostic(self):
        self.assertFalse((HERE / "dispatch-claim.json").exists())
        value = controller.read_manifest()
        self.assertEqual(value["validated_batch"]["derived_invocations"], 1)
        self.assertEqual(value["validated_gate"], {
            "kind": "full-check-archive-v1",
            "sha256": value["full_check_gate_sha256"],
            "admission_eligible": False,
        })
        self.assertEqual(value["reserved_product_invocations"], 0)
        self.assertFalse((HERE / "dispatch-claim.json").exists())

    def _copied_gate(self):
        temporary = tempfile.TemporaryDirectory(dir=self.evidence)
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        gate = copy.deepcopy(self.gate)
        for field in (
            "full_check_result_path", "full_check_log_path", "before_tracked_manifest_path",
            "after_tracked_manifest_path", "source_provenance_path", "overall_exit_path",
            "check_exit_path", "producer_path",
        ):
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

    def test_rejects_false_integer_exit_and_changed_artifact_hash(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(gate, "full_check_result_path", "full_check_result_sha256",
                                    lambda result: result.__setitem__("remote_overall_exit", False))
        with self.assertRaisesRegex(RuntimeError, "remote_overall_exit"):
            self._validate_gate(root, gate)
        root, gate = self._copied_gate()
        gate["full_check_result_sha256"] = "f" * 64
        with self.assertRaisesRegex(RuntimeError, "result artifact is missing or changed"):
            self._validate_gate(root, gate)

    def test_rejects_false_statusline_failed_count(self):
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(
            gate, "full_check_result_path", "full_check_result_sha256",
            lambda result: result["task_results"]["test_statusline"].__setitem__("failed", False),
        )
        with self.assertRaisesRegex(RuntimeError, "statusline result"):
            self._validate_gate(root, gate)

    def test_rejects_altered_manifest_and_source_provenance(self):
        root, gate = self._copied_gate()
        before = (self.package / gate["before_tracked_manifest_path"]).resolve()
        before.write_text(before.read_text().replace("100644", "100755", 1))
        gate["before_tracked_manifest_sha256"] = controller.sha256(before)
        with self.assertRaisesRegex(RuntimeError, "tracked manifest hash changed"):
            self._validate_gate(root, gate)
        root, gate = self._copied_gate()
        self._rewrite_json_artifact(gate, "source_provenance_path", "source_provenance_sha256",
                                    lambda provenance: provenance.__setitem__("source_commit", "0" * 40))
        with self.assertRaisesRegex(RuntimeError, "source provenance mismatch"):
            self._validate_gate(root, gate)

    def test_rejects_missing_required_raw_task_with_refreshed_bindings(self):
        root, gate = self._copied_gate()
        log = (self.package / gate["full_check_log_path"]).resolve()
        log.write_text("".join(line for line in log.read_text().splitlines(keepends=True)
                               if line != "[build] $ go build -o entire-graph ./cmd/entire-graph\n"))
        gate["full_check_log_sha256"] = controller.sha256(log)
        result = (self.package / gate["full_check_result_path"]).resolve()
        value = json.loads(result.read_text())
        value["raw_log_sha256"] = gate["full_check_log_sha256"]
        result.write_text(json.dumps(value, indent=2) + "\n")
        gate["full_check_result_sha256"] = controller.sha256(result)
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


if __name__ == "__main__":
    unittest.main()
