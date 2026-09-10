import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("relations_cpu_controller", HERE / "controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class RelationsCPUControllerTests(unittest.TestCase):
    def test_prepared_manifest_fails_closed_on_pending_pin_without_claim_or_cloud(self):
        with mock.patch.object(controller, "CLAIM_PATH", Path("/tmp/relations-cpu-no-claim")), \
             mock.patch.object(controller, "_validate_scoped_full_check_gate", side_effect=RuntimeError("scoped full-check gate is not passed")):
            with self.assertRaisesRegex(RuntimeError, "not passed"):
                controller.read_manifest()
            self.assertFalse(Path("/tmp/relations-cpu-no-claim").exists())

    def test_pending_manifest_never_loads_or_calls_cloud_transport(self):
        with mock.patch.object(controller, "load_cloud_transport", side_effect=AssertionError("cloud must not load")), \
             mock.patch.object(controller, "_validate_scoped_full_check_gate", side_effect=RuntimeError("scoped full-check gate is not passed")):
            with self.assertRaisesRegex(RuntimeError, "not passed"):
                controller.execute()

    def test_manifest_requires_dedicated_profiler_opt_in(self):
        document = json.loads((HERE / "manifest.json").read_text())
        self.assertIs(document["profiler_enabled"], True)
        with tempfile.TemporaryDirectory() as directory:
            pending = dict(document, profiler_enabled=False)
            path = Path(directory) / "manifest.json"
            path.write_text(json.dumps(pending))
            with mock.patch.object(controller, "MANIFEST_PATH", path):
                with self.assertRaisesRegex(RuntimeError, "profiler_enabled"):
                    controller.read_manifest()

    def test_remote_script_preserves_one_request_and_no_precreated_output(self):
        document = {
            "dispatch_id": "relations-cpu-test",
            "remote_output_root": "/opt/p1/diagnostics/relations-cpu-test",
            "remote_source_root": "/opt/graph-validation/relations-cpu",
            "remote_claim_path": "/opt/p1/batch-claims/relations-cpu-test/worker-1.json",
            "binary_sha256": "a" * 64,
            "source_commit": "b" * 40,
            "source_digest": "c" * 64,
            "input_manifest_sha256": "d" * 64,
            "control_archive_sha256": "e" * 64,
            "remote_control_timeout_seconds": 360,
        }
        script = controller.remote_script(document, "https://control", "https://result")
        self.assertIn("--batch-worker 1", script)
        collector_source = (HERE.parent / "collector/run_remote.py").read_text()
        self.assertIn("ENTIRE_GRAPH_EXTRACTION_CORPUS_RELATIONS_CPU_PROFILE", collector_source)
        self.assertIn("TIMEOUT_SECONDS = 120", collector_source)
        self.assertIn("timeout --signal=TERM --kill-after=10s 360s", script)
        self.assertIn('test ! -e "$REMOTE"', script)
        self.assertIn('test ! -e "$CLAIM"', script)
        self.assertIn('if test -e "$REMOTE/output"; then', script)
        self.assertNotIn('mkdir -p "$REMOTE" "$CONTROL" "$OUTPUT"', script)

    def test_preparation_contains_no_retained_result_or_claim_members(self):
        package = HERE.parent
        names = {path.relative_to(package).as_posix() for path in package.rglob("*") if path.is_file()}
        self.assertFalse(any(name.startswith("raw/") for name in names))
        self.assertFalse(any(name.startswith("results") for name in names))
        self.assertNotIn("dispatch-claim.json", names)
        self.assertIn("control-files.tar.gz", names)


if __name__ == "__main__":
    unittest.main()
