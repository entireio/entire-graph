import importlib.util
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("route_boundary_controller", HERE / "controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class RouteBoundaryControllerTests(unittest.TestCase):
    def test_real_read_manifest_is_one_non_admission_diagnostic(self):
        claim = HERE / "dispatch-claim.json"
        self.assertFalse(claim.exists())
        value = controller.read_manifest()
        self.assertEqual(value["validated_batch"]["derived_invocations"], 1)
        self.assertEqual(value["validated_gate"], {
            "kind": "diagnostic-validation-v1",
            "sha256": value["validation_gate_sha256"],
            "admission_eligible": False,
        })
        self.assertFalse(claim.exists())

    def test_gate_preflight_failure_creates_no_claim_or_cloud_call(self):
        document = json.loads((HERE / "manifest.json").read_text())
        document["validation_gate_sha256"] = "f" * 64
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / "manifest.json"
            manifest.write_text(json.dumps(document))
            claim = Path(directory) / "claim.json"
            with mock.patch.object(controller, "MANIFEST_PATH", manifest), \
                 mock.patch.object(controller, "CLAIM_PATH", claim), \
                 mock.patch.object(controller, "load_cloud_transport", side_effect=AssertionError("cloud called")):
                with self.assertRaisesRegex(RuntimeError, "gate hash changed"):
                    controller.execute()
            self.assertFalse(claim.exists())

    def test_control_archive_retains_gate_kind_without_old_results(self):
        package = HERE.parent
        with tarfile.open(package / "control-files.tar.gz", "r:gz") as archive:
            names = set(archive.getnames())
            gate = json.load(archive.extractfile("diagnostic-validation-gate.json"))
        self.assertEqual(gate["schema"], "diagnostic-validation-v1")
        self.assertIn("diagnostic-validation-gate.json", names)
        self.assertNotIn("full-check-gate.json", names)
        self.assertFalse(any("claim" in name or name.startswith("raw/") or name.startswith("results") for name in names))

    def test_leaf_collector_dry_preflight_uses_exact_pins(self):
        collector_path = HERE.parent / "collector/run_remote.py"
        collector_spec = importlib.util.spec_from_file_location("route_boundary_collector", collector_path)
        collector = importlib.util.module_from_spec(collector_spec)
        collector_spec.loader.exec_module(collector)
        document = json.loads((HERE / "manifest.json").read_text())
        self.assertEqual(collector.EXPECTED_SOURCE_COMMIT, document["source_commit"])
        self.assertEqual(collector.EXPECTED_BINARY_SHA256, document["binary_sha256"])
        batch = controller._validate_batch(document, HERE.parent / "batch-manifest.json", HERE.parent / "build-manifest.json")
        self.assertEqual(batch["workers"], {"1": 1})
        self.assertEqual(batch["cells"][0]["arms"], [False])


if __name__ == "__main__":
    unittest.main()
