import hashlib
import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
spec = importlib.util.spec_from_file_location("launch", HERE / "launch.py")
launch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launch)


class LaunchContracts(unittest.TestCase):
    def fixture(self):
        root = pathlib.Path(tempfile.mkdtemp())
        for name, contents in {
            "internal/a.go": "package a\n",
            "cmd/tool/main.go": "package main\n",
            "go.mod": "module example\n",
            "go.sum": "",
        }.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(contents)
        source_files = ["internal/a.go", "cmd/tool/main.go", "go.mod", "go.sum"]
        inventory = root / "source-files.sha256"
        inventory.write_text(
            "".join(
                f"{hashlib.sha256((root / name).read_bytes()).hexdigest()}  {name}\n"
                for name in source_files
            )
        )
        subprocess.run(["git", "init", "-q"], cwd=root, check=True)
        subprocess.run(["git", "config", "user.email", "test@example.com"], cwd=root, check=True)
        subprocess.run(["git", "config", "user.name", "Test"], cwd=root, check=True)
        subprocess.run(["git", "add", "internal", "cmd", "go.mod", "go.sum"], cwd=root, check=True)
        common = {
            "source_file_hash_manifest": inventory.name,
            "source_file_hash_manifest_sha256": launch.sha(inventory),
            "source_file_count": len(source_files),
            "source_inventory_root": ".",
        }
        first = root / "build-a.json"
        second = root / "build-b.json"
        first.write_text(json.dumps(dict(common, binary_sha256="a" * 64, binary_blob="evaluator-a")))
        second.write_text(json.dumps(dict(common, binary_sha256="b" * 64, binary_blob="evaluator-b")))
        return root, first, second

    def test_two_manifests_pin_distinct_blob_and_run_local_paths(self):
        root, first, second = self.fixture()
        a = launch.load_and_validate_build_manifest(first, root)
        b = launch.load_and_validate_build_manifest(second, root)
        self.assertNotEqual(a["binary_blob"], b["binary_blob"])
        script = launch.worker_script(
            stage="campaign", run_id="run-a", worker_index=1,
            script_url="https://storage/scripts-a?sig=secret",
            binary_url="https://storage/evaluator-a?sig=secret",
            binary_sha256=a["binary_sha256"], trials=1, frozen_baseline=False,
        )
        self.assertIn("/opt/p1/runs/run-a/p1-evaluator", script)
        self.assertIn("/opt/p1/runs/run-a/scripts", script)
        self.assertIn("test ! -e /opt/p1/runs/run-a", script)
        self.assertIn("P1_LAUNCH_OK %s", script)
        self.assertIn("/opt/p1/runs/run-a/STOP", script)
        self.assertNotIn("/opt/p1/p1-evaluator", script)

    def test_default_manifest_resolution_keeps_historical_path(self):
        selected = launch.resolve_manifest_path(None, HERE / "build.json")
        self.assertEqual(selected, (HERE / "build.json").resolve())

    def test_source_inventory_mismatch_fails_in_local_preflight(self):
        root, first, _ = self.fixture()
        inventory = root / "source-files.sha256"
        inventory.write_text(inventory.read_text().replace("internal/a.go", "internal/missing.go"))
        document = json.loads(first.read_text())
        document["source_file_hash_manifest_sha256"] = launch.sha(inventory)
        first.write_text(json.dumps(document))
        with self.assertRaises(ValueError):
            launch.load_and_validate_build_manifest(first, root)

    def test_existing_supervisor_run_directory_is_rejected_by_caller_contract(self):
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / "run"
            path.mkdir()
            with self.assertRaises(ValueError):
                launch.prepare_supervisor_output(path)

    def test_malformed_reply_is_retained_before_decode_without_sas(self):
        with tempfile.TemporaryDirectory() as d:
            evidence = pathlib.Path(d) / "transport.json"
            raw = 'not-json https://storage/blob?sig=secret&sp=r'
            with self.assertRaises(json.JSONDecodeError):
                launch.decode_transport_response(raw, evidence)
            record = json.loads(evidence.read_text())
            self.assertIn("not-json", record["raw_response_redacted"])
            self.assertNotIn("secret", record["raw_response_redacted"])
            self.assertTrue(record["raw_response_sha256"])

    def test_redaction_starts_at_first_query_delimiter(self):
        redacted = launch._redact_transport(
            "https://storage/blob?sig=secret?secondary=also-secret"
        )
        self.assertEqual(redacted, "https://storage/blob?<redacted-sas>")

    def test_valid_azure_envelope_without_launch_ack_is_failure(self):
        with tempfile.TemporaryDirectory() as d:
            evidence = pathlib.Path(d) / "transport.json"
            with self.assertRaises(RuntimeError):
                launch.decode_transport_response(
                    json.dumps(["status only"]), evidence, expected_ack="P1_LAUNCH_OK unit"
                )
            self.assertTrue(evidence.exists())

    def test_launch_ack_is_required_after_remote_script(self):
        with tempfile.TemporaryDirectory() as d:
            evidence = pathlib.Path(d) / "transport.json"
            decoded = launch.decode_transport_response(
                json.dumps(["P1_LEASE renewed\nP1_LAUNCH_OK unit"]),
                evidence,
                expected_ack="P1_LAUNCH_OK unit",
            )
            self.assertEqual(decoded[0].splitlines()[-1], "P1_LAUNCH_OK unit")

    def test_frozen_baseline_identity_changes_when_baseline_changes(self):
        root, first, _ = self.fixture()
        context = launch.load_and_validate_build_manifest(first, root)
        baseline_a = root / "baseline-a.json"
        baseline_b = root / "baseline-b.json"
        baseline_a.write_text("{\"binary_sha256\":\"a\"}\n")
        baseline_b.write_text("{\"binary_sha256\":\"b\"}\n")
        identity_a = launch._identity(context, baseline_a)
        identity_b = launch._identity(context, baseline_b)
        self.assertNotEqual(
            identity_a["frozen_baseline_sha256"],
            identity_b["frozen_baseline_sha256"],
        )

    def test_failure_handler_attempts_stop_on_every_worker(self):
        with tempfile.TemporaryDirectory() as d, mock.patch.object(
            launch.cloud, "run", return_value=json.dumps(["status"])
        ) as run:
            launch.stop_workers("campaign", "run-a", pathlib.Path(d))
            self.assertEqual(run.call_count, len(launch.VMS))
            self.assertEqual(
                len(list(pathlib.Path(d).glob("launch-worker-*-stop.json"))),
                len(launch.VMS),
            )
            self.assertTrue((pathlib.Path(d) / "launch-failure.json").exists())


if __name__ == "__main__":
    unittest.main()
