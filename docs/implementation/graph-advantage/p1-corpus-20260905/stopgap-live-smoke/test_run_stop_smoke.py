import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest
import threading
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

    def test_main_runs_actual_supervisor_with_mocked_worker_transport(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory)
            state_lock = threading.Lock()
            calls = []
            paused = False
            stopped = set()

            def fake_run(vm, script):
                nonlocal paused
                with state_lock:
                    calls.append((vm, script))
                    if "P1_SMOKE_READY" in script:
                        return json.dumps(["P1_SMOKE_READY smoke-e2e"])
                    if "P1_SMOKE_PAUSED" in script:
                        paused = True
                        return json.dumps(["P1_SMOKE_PAUSED"])
                    if "supervisor-stop.json" in script:
                        stopped.add(vm)
                        return json.dumps(["stopped"])
                    if "P1_STATUS " in script:
                        active = vm not in stopped
                        payload = {
                            "state": "active" if active else "inactive",
                            "exit_code": 0,
                            "progress": {"stage": "campaign", "done": False},
                            "paused": paused if active else False,
                            "stop": vm in stopped,
                        }
                        return json.dumps(["P1_STATUS " + json.dumps(payload)])
                raise AssertionError("unexpected worker command")

            with mock.patch.object(smoke.cloud, "environment", return_value={}), mock.patch.object(
                smoke.cloud, "run", side_effect=fake_run
            ):
                smoke.main(["--run-id", "smoke-e2e", "--output", str(output)])

            summary = json.loads((output / "summary.json").read_text())
            self.assertEqual(summary["status"], "pass")
            self.assertIs(summary["supervisor_result"], False)
            self.assertEqual(len(stopped), 3)
            self.assertEqual(
                sum("P1_SMOKE_READY" in script for _, script in calls),
                3,
            )
            self.assertEqual(
                sum("supervisor-stop.json" in script for _, script in calls),
                3,
            )


if __name__ == "__main__":
    unittest.main()
