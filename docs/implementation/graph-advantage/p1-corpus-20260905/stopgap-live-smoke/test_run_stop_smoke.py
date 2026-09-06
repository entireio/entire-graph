import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
spec = importlib.util.spec_from_file_location("smoke", HERE / "run_stop_smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class StopSmokeTests(unittest.TestCase):
    def test_setup_is_fake_service_only_and_bounded(self):
        script = smoke.setup_script("smoke-1")
        self.assertIn("RuntimeMaxSec=300", script)
        self.assertIn("p1-campaign-smoke-1", script)
        self.assertIn("fake-worker.sh", script)
        self.assertNotIn("p1-evaluator", script)
        self.assertNotIn("/opt/p1/corpus", script)
        self.assertNotIn("graph query", script)

    def test_transport_ack_is_required_and_redacted(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "raw.json"
            with self.assertRaises(RuntimeError):
                smoke.decode(json.dumps(["P1_SMOKE_READY wrong"]), path, "P1_SMOKE_READY smoke-1")
            path.unlink()
            decoded = smoke.decode(
                json.dumps(["P1_SMOKE_READY smoke-1 https://blob/x?sig=secret"]),
                path,
                "P1_SMOKE_READY smoke-1 https://blob/x?sig=secret",
            )
            self.assertEqual(len(decoded), 1)
            self.assertNotIn("secret", path.read_text())

    def test_stop_all_uses_exact_units_and_preserves_responses(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            smoke.cloud, "run", return_value=json.dumps(["stopped"])
        ) as run:
            smoke.stop_all(pathlib.Path(directory), "campaign", "smoke-1")
            self.assertEqual(run.call_count, len(smoke.VMS))
            calls = [call.args[1] for call in run.call_args_list]
            self.assertTrue(all("p1-campaign-smoke-1" in call for call in calls))
            self.assertEqual(
                len(list(pathlib.Path(directory).glob("stop-*.json"))),
                len(smoke.VMS),
            )


if __name__ == "__main__":
    unittest.main()
