import importlib.util
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("postfix_controller", HERE / "controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)


class PostFixControllerTests(unittest.TestCase):
    def test_archived_collector_runtime_pins_fail_closed(self):
        document = json.loads((HERE / "manifest.json").read_text())
        runner = (HERE.parent / "collector/run_remote.py").read_bytes()
        controller._validate_archived_runtime_pins(document, {"collector/run_remote.py": runner})
        placeholder = runner.replace(document["source_commit"].encode(), b"<PENDING_TEST_SOURCE_COMMIT>")
        with self.assertRaisesRegex(RuntimeError, "placeholder"):
            controller._validate_archived_runtime_pins(document, {"collector/run_remote.py": placeholder})
        mismatch = runner.replace(document["binary_sha256"].encode(), ("f" * 64).encode())
        with self.assertRaisesRegex(RuntimeError, "does not match"):
            controller._validate_archived_runtime_pins(document, {"collector/run_remote.py": mismatch})

    def test_final_packaged_collector_leaf_dry_preflight(self):
        document = json.loads((HERE / "manifest.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with tarfile.open(HERE.parent / document["control_archive"], "r:gz") as archive:
                archive.extractall(root, filter="data")
            command = [
                sys.executable, str(root / "collector/run_remote.py"),
                "--output", str(root / "unused-output"),
                "--binary", str(root / "unused-binary"),
                "--binary-sha256", document["binary_sha256"],
                "--source-root", str(root / "unused-source"),
                "--source-commit", document["source_commit"],
                "--scenario-script", str(root / "corpus/p1_scenario.py"),
                "--build-manifest", str(root / "build-manifest.json"),
                "--batch-manifest", str(root / "batch-manifest.json"),
                "--batch-worker", "1", "--input-sha256", document["source_digest"],
                "--input-manifest-sha256", document["input_manifest_sha256"],
                "--dry-preflight",
            ]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((root / "unused-output").exists())

    def test_pending_or_placeholder_manifest_refuses_before_members_or_cloud(self):
        with tempfile.TemporaryDirectory() as directory:
            pending = json.loads((HERE / "manifest.json").read_text())
            pending["status"] = "execution blocked pending immutable check"
            pending["immutable_check"] = {"status": "pending"}
            manifest_path = Path(directory) / "pending.json"
            manifest_path.write_text(json.dumps(pending))
            with mock.patch.object(controller, "MANIFEST_PATH", manifest_path):
                with self.assertRaisesRegex(RuntimeError, "immutable check"):
                    controller.read_manifest()
            pending["status"] = "prepared only; no VM, cloud, collector, product, corpus, or benchmark execution"
            pending["immutable_check"] = {"status": "passed"}
            pending["source_commit"] = "<PENDING_INTEGRATION_SHA>"
            manifest_path = Path(directory) / "manifest.json"
            manifest_path.write_text(json.dumps(pending))
            with mock.patch.object(controller, "MANIFEST_PATH", manifest_path):
                with self.assertRaisesRegex(RuntimeError, "placeholder"):
                    controller.read_manifest()

    def test_remote_script_is_full_profile_single_off_diagnostic(self):
        manifest = {
            "dispatch_id": "postfix-test",
            "remote_output_root": "/opt/p1/diagnostics/postfix-test",
            "remote_source_root": "/opt/graph-validation/diagnostics-postfix",
            "remote_claim_path": "/opt/p1/batch-claims/postfix-test/worker-1.json",
            "binary_sha256": "a" * 64,
            "source_commit": "b" * 40,
            "source_digest": "c" * 64,
            "input_manifest_sha256": "d" * 64,
            "control_archive_sha256": "e" * 64,
            "remote_control_timeout_seconds": 360,
        }
        script = controller.remote_script(manifest, "https://control", "https://result")
        self.assertIn("collector/run_remote.py", script)
        self.assertIn("--batch-worker 1", script)
        self.assertIn("POST_FIX_FULL_PROFILE_DIAGNOSTIC_UPLOAD_ACK", script)
        self.assertIn('test ! -e "$REMOTE"', script)
        self.assertIn('test ! -e "$CLAIM"', script)
        self.assertIn('if test -e "$REMOTE/output"; then', script)
        self.assertNotIn("mkdir -p \"$REMOTE\" \"$CONTROL\" \"$OUTPUT\"", script)

    def test_manifest_contract_is_exactly_one_full_off_cell(self):
        document = json.loads((HERE / "manifest.json").read_text())
        self.assertEqual(document["profile"], "full")
        self.assertEqual(document["cache"], "off")
        self.assertEqual(document["total_attempts"], 1)
        self.assertFalse(document["no_on_arm"] is False)
        self.assertFalse(document["no_comparison"] is False)
        self.assertFalse(document["admission_eligible"])

    def test_manifest_rejects_remote_source_root_drift_from_build(self):
        document = json.loads((HERE / "manifest.json").read_text())
        document["remote_source_root"] = "/opt/graph-validation/wrong-build-root"
        with tempfile.TemporaryDirectory() as directory:
            manifest_path = Path(directory) / "manifest.json"
            manifest_path.write_text(json.dumps(document))
            with mock.patch.object(controller, "MANIFEST_PATH", manifest_path):
                with self.assertRaisesRegex(RuntimeError, "remote source root"):
                    controller.read_manifest()

    def _passed_scoped_gate_fixture(self, directory):
        directory = Path(directory)
        document = json.loads((HERE / "manifest.json").read_text())
        evaluator_revision = document["source_commit"]
        full_revision = "e" * 40
        gate = {
            "version": 1, "status": "passed",
            "evaluator_source_commit": evaluator_revision,
            "full_check_source_commit": full_revision,
            "evaluator_binary_sha256": document["binary_sha256"],
            "evaluator_build_manifest_sha256": controller.sha256(HERE.parent / "build-manifest.json"),
            "equal_paths": [],
            "allowed_difference_paths": [],
            "expected_changed_paths": list(controller.SCOPED_ALLOWED_DIFFERENCES),
        }
        for path_name in controller.SCOPED_EQUAL_PATHS:
            gate["equal_paths"].append({"path": path_name, "evaluator_git_object": "a" * 40,
                                        "full_check_git_object": "a" * 40})
        for path_name in controller.SCOPED_ALLOWED_DIFFERENCES:
            gate["allowed_difference_paths"].append({"path": path_name, "evaluator_git_object": "b" * 40,
                                                      "full_check_git_object": "c" * 40})
        result = {"expected_head": full_revision, "exit_code": 0, "exception": None,
                  "timed_out": False, "started_epoch": 1, "finished_epoch": 2,
                  "command": "MISE_JOBS=1 GOFLAGS=-p=1 GOMAXPROCS=2 GIT_TERMINAL_PROMPT=0 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null mise run check"}
        result_path = directory / "full-check-result.json"
        log_path = directory / "full-check.log"
        log_path.write_text("mise run check\nPASS\n")
        result["raw_log"] = log_path.name
        result_path.write_text(json.dumps(result, sort_keys=True) + "\n")
        integrity_path = directory / "source-integrity.json"
        integrity_path.write_text(json.dumps({"before_head": full_revision, "after_head": full_revision,
            "before_status_bytes": 0, "after_status_bytes": 0,
            "tracked_source_manifest_equal": True}, sort_keys=True) + "\n")
        gate.update({"full_check_result_sha256": hashlib.sha256(result_path.read_bytes()).hexdigest(),
                     "full_check_log_sha256": hashlib.sha256(log_path.read_bytes()).hexdigest(),
                     "full_check_integrity_sha256": hashlib.sha256(integrity_path.read_bytes()).hexdigest(),
                     "full_check_result_path": result_path.name, "full_check_log_path": log_path.name,
                     "full_check_integrity_path": integrity_path.name,
                     "full_check_evidence": "synthetic test fixture"})
        path = directory / "full-check-gate.json"
        path.write_text(json.dumps(gate))
        document["full_check_gate_sha256"] = controller.sha256(path)
        return document, path, gate

    def _validate_scoped_gate_fixture(self, document, path, gate, directory):
        object_map = {
            (revision, item["path"]): item[key]
            for revision, key in ((gate["evaluator_source_commit"], "evaluator_git_object"),
                                  (gate["full_check_source_commit"], "full_check_git_object"))
            for item in gate["equal_paths"] + gate["allowed_difference_paths"]
        }
        with mock.patch.object(controller, "PACKAGE_ROOT", Path(directory)), \
             mock.patch.object(controller, "_git_object", side_effect=lambda revision, source_path: object_map[(revision, source_path)]), \
             mock.patch.object(controller, "_git_changed_paths", return_value=list(controller.SCOPED_ALLOWED_DIFFERENCES)):
            return controller._validate_scoped_full_check_gate(
                document, path, HERE.parent / "build-manifest.json"
            )

    def _new_schema_scoped_gate_fixture(self, directory):
        directory = Path(directory)
        package = directory / "package"
        evidence = directory / "check-450bede9"
        package.mkdir()
        evidence.mkdir()
        for name in ("full-check-gate.json", "build-manifest.json"):
            shutil.copy2(HERE.parent / name, package / name)
        for name in ("subprocess-result.json", "mise-check.raw.log", "source-integrity.json", "schema-normalization.json"):
            shutil.copy2(HERE.parent.parent / "check-450bede9" / name, evidence / name)
        gate_path = package / "full-check-gate.json"
        gate = json.loads(gate_path.read_text())
        document = json.loads((HERE / "manifest.json").read_text())
        document["full_check_gate_sha256"] = controller.sha256(gate_path)
        return document, package, evidence, gate_path, gate

    def _validate_new_schema_gate_fixture(self, document, package, gate_path):
        gate = json.loads(gate_path.read_text())
        object_map = {
            (revision, item["path"]): item[key]
            for revision, key in ((gate["evaluator_source_commit"], "evaluator_git_object"),
                                  (gate["full_check_source_commit"], "full_check_git_object"))
            for item in gate["equal_paths"] + gate["allowed_difference_paths"]
        }
        with mock.patch.object(controller, "PACKAGE_ROOT", package), \
             mock.patch.object(controller, "_git_object", side_effect=lambda revision, source_path: object_map[(revision, source_path)]), \
             mock.patch.object(controller, "_git_changed_paths", return_value=[]):
            return controller._validate_scoped_full_check_gate(
                document, gate_path, package / "build-manifest.json"
            )

    def _refresh_new_schema_gate(self, document, gate_path, evidence, artifact_name):
        gate = json.loads(gate_path.read_text())
        artifact_path = evidence / artifact_name
        gate_key = {
            "subprocess-result.json": "full_check_result_sha256",
            "mise-check.raw.log": "full_check_log_sha256",
            "source-integrity.json": "full_check_integrity_sha256",
            "schema-normalization.json": "full_check_normalization_sha256",
        }[artifact_name]
        gate[gate_key] = controller.sha256(artifact_path)
        gate_path.write_text(json.dumps(gate, sort_keys=True) + "\n")
        document["full_check_gate_sha256"] = controller.sha256(gate_path)

    def test_scoped_gate_accepts_exact_covered_source_and_allowed_difference(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            result = self._validate_scoped_gate_fixture(document, path, gate, directory)
            self.assertEqual(result["status"], "passed")

    def test_new_schema_gate_rejects_equal_nonempty_status_hashes(self):
        with tempfile.TemporaryDirectory() as directory:
            document, package, evidence, gate_path, _ = self._new_schema_scoped_gate_fixture(directory)
            integrity_path = evidence / "source-integrity.json"
            integrity = json.loads(integrity_path.read_text())
            integrity["before_status_sha256"] = "1" * 64
            integrity["after_status_sha256"] = "1" * 64
            integrity_path.write_text(json.dumps(integrity, sort_keys=True) + "\n")
            self._refresh_new_schema_gate(document, gate_path, evidence, "source-integrity.json")
            with self.assertRaisesRegex(RuntimeError, "status is not clean"):
                self._validate_new_schema_gate_fixture(document, package, gate_path)

    def test_new_schema_gate_rejects_integrity_tracked_hash_drift_from_result(self):
        with tempfile.TemporaryDirectory() as directory:
            document, package, evidence, gate_path, _ = self._new_schema_scoped_gate_fixture(directory)
            integrity_path = evidence / "source-integrity.json"
            integrity = json.loads(integrity_path.read_text())
            integrity["before_tracked_sha256"] = "1" * 64
            integrity["after_tracked_sha256"] = "1" * 64
            integrity_path.write_text(json.dumps(integrity, sort_keys=True) + "\n")
            self._refresh_new_schema_gate(document, gate_path, evidence, "source-integrity.json")
            with self.assertRaisesRegex(RuntimeError, "tracked source hashes disagree"):
                self._validate_new_schema_gate_fixture(document, package, gate_path)

    def test_scoped_gate_rejects_unexpected_source_difference(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            object_map = {
                (revision, item["path"]): item[key]
                for revision, key in ((gate["evaluator_source_commit"], "evaluator_git_object"),
                                      (gate["full_check_source_commit"], "full_check_git_object"))
                for item in gate["equal_paths"] + gate["allowed_difference_paths"]
            }
            with mock.patch.object(controller, "PACKAGE_ROOT", Path(directory)), \
                 mock.patch.object(controller, "_git_object", side_effect=lambda revision, source_path: object_map[(revision, source_path)]), \
                 mock.patch.object(controller, "_git_changed_paths", return_value=[*controller.SCOPED_ALLOWED_DIFFERENCES, "internal/extra.go"]):
                with self.assertRaisesRegex(RuntimeError, "unexpected paths"):
                    controller._validate_scoped_full_check_gate(
                        document, path, HERE.parent / "build-manifest.json"
                    )

    def test_scoped_gate_rejects_binary_or_build_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, _ = self._passed_scoped_gate_fixture(directory)
            document["binary_sha256"] = "c" * 64
            with self.assertRaisesRegex(RuntimeError, "binary identity"):
                with mock.patch.object(controller, "PACKAGE_ROOT", Path(directory)):
                    controller._validate_scoped_full_check_gate(
                        document, path, HERE.parent / "build-manifest.json"
                    )

    def test_scoped_gate_rejects_missing_result_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            (Path(directory) / gate["full_check_result_path"]).unlink()
            with self.assertRaisesRegex(RuntimeError, "result artifact"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)

    def test_scoped_gate_rejects_tampered_log_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            (Path(directory) / gate["full_check_log_path"]).write_text("tampered\n")
            with self.assertRaisesRegex(RuntimeError, "log artifact"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)

    def test_scoped_gate_rejects_failed_result_even_when_gate_says_passed(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            result_path = Path(directory) / gate["full_check_result_path"]
            result = json.loads(result_path.read_text())
            result["exit_code"] = 1
            result_path.write_text(json.dumps(result, sort_keys=True) + "\n")
            gate["full_check_result_sha256"] = controller.sha256(result_path)
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            with self.assertRaisesRegex(RuntimeError, "did not exit successfully"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)

    def test_scoped_gate_rejects_different_successful_command(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            result_path = Path(directory) / gate["full_check_result_path"]
            result = json.loads(result_path.read_text())
            result["command"] = "go test ./..."
            result_path.write_text(json.dumps(result, sort_keys=True) + "\n")
            gate["full_check_result_sha256"] = controller.sha256(result_path)
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            with self.assertRaisesRegex(RuntimeError, "command"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)

    def test_scoped_gate_rejects_wrong_result_source_head(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            result_path = Path(directory) / gate["full_check_result_path"]
            result = json.loads(result_path.read_text())
            result["expected_head"] = "f" * 40
            result_path.write_text(json.dumps(result, sort_keys=True) + "\n")
            gate["full_check_result_sha256"] = controller.sha256(result_path)
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            with self.assertRaisesRegex(RuntimeError, "expected head"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)

    def test_scoped_gate_rejects_dirty_or_changed_source_integrity(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            integrity_path = Path(directory) / gate["full_check_integrity_path"]
            integrity = json.loads(integrity_path.read_text())
            integrity["tracked_source_manifest_equal"] = False
            integrity_path.write_text(json.dumps(integrity, sort_keys=True) + "\n")
            gate["full_check_integrity_sha256"] = controller.sha256(integrity_path)
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            with self.assertRaisesRegex(RuntimeError, "source manifest"):
                self._validate_scoped_gate_fixture(document, path, gate, directory)
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            gate["evaluator_build_manifest_sha256"] = "d" * 64
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            with self.assertRaisesRegex(RuntimeError, "build identity"):
                with mock.patch.object(controller, "PACKAGE_ROOT", Path(directory)):
                    controller._validate_scoped_full_check_gate(
                        document, path, HERE.parent / "build-manifest.json"
                    )

    def test_scoped_gate_rejects_pending_or_changed_gate_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            document, path, gate = self._passed_scoped_gate_fixture(directory)
            document["full_check_gate_sha256"] = "e" * 64
            with self.assertRaisesRegex(RuntimeError, "gate artifact hash changed"):
                controller._validate_scoped_full_check_gate(document, path, HERE.parent / "build-manifest.json")
            gate["status"] = "pending"
            path.write_text(json.dumps(gate))
            document["full_check_gate_sha256"] = controller.sha256(path)
            object_map = {
                (revision, item["path"]): item[key]
                for revision, key in ((gate["evaluator_source_commit"], "evaluator_git_object"),
                                      (gate["full_check_source_commit"], "full_check_git_object"))
                for item in gate["equal_paths"] + gate["allowed_difference_paths"]
            }
            with mock.patch.object(controller, "PACKAGE_ROOT", Path(directory)), \
                 mock.patch.object(controller, "_git_object", side_effect=lambda revision, source_path: object_map[(revision, source_path)]), \
                 mock.patch.object(controller, "_git_changed_paths", return_value=list(controller.SCOPED_ALLOWED_DIFFERENCES)):
                with self.assertRaisesRegex(RuntimeError, "not passed"):
                    controller._validate_scoped_full_check_gate(document, path, HERE.parent / "build-manifest.json")

    def test_packaged_cloud_transport_imports_in_executable_smoke(self):
        command = [sys.executable, "-c",
                   "import controller; m=controller.load_cloud_transport(); print(m.__file__)"]
        result = subprocess.run(command, cwd=HERE, capture_output=True, text=True, check=True)
        self.assertEqual(Path(result.stdout.strip()).resolve(), (HERE.parent / "collector/cloud.py").resolve())

    def test_batch_allocation_and_cell_identity_are_revalidated(self):
        budget = controller._load_local_module("test_batch_budget", HERE.parent / "collector/batch_budget.py")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            build = root / "build.json"
            build.write_text(json.dumps({"source_commit": "a" * 40, "binary_sha256": "b" * 64, "exit_code": 0}))
            document = {"dispatch_id": "p1-test", "binary_sha256": "b" * 64,
                        "input_manifest_sha256": "c" * 64, "repository": "kubernetes-kubernetes"}
            cell = {"worker": 1, "repository": "kubernetes-kubernetes", "profile": "full",
                    "verb": "snapshot", "scenario": "diagnostic", "trial": 0,
                    "arms": [False], "preparatory_invocations": 0}
            cell["cell_id"] = budget.cell_id(cell)
            document["cell_id"] = cell["cell_id"]
            batch = {"version": 1, "batch_id": "p1-test", "scope": "bounded-diagnostic",
                     "cap": 100, "total_attempts": 1, "derived_invocations": 1,
                     "preparatory_invocations": 0, "cells": [cell], "workers": {"1": 1},
                     "identity": {
                         "binary_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
                         "runner_sha256": controller.sha256(HERE.parent / "collector/run_remote.py"),
                         "scenario_sha256": controller.sha256(HERE.parent / "corpus/p1_scenario.py"),
                         "gate_sha256": controller.sha256(HERE.parent / "collector/campaign_gate.py"),
                         "budget_sha256": controller.sha256(HERE.parent / "collector/batch_budget.py"),
                         "build_manifest_sha256": controller.sha256(build)}}
            batch_path = root / "batch.json"
            batch_path.write_text(json.dumps(batch))
            self.assertEqual(controller._validate_batch(document, batch_path, build)["cells"][0]["cell_id"], cell["cell_id"])
            batch["workers"] = {"1": 2}
            batch_path.write_text(json.dumps(batch))
            with self.assertRaisesRegex(RuntimeError, "worker 1 allocation"):
                controller._validate_batch(document, batch_path, build)

    def test_control_archive_membership_and_hash_list_are_checked(self):
        package = HERE.parent
        names = controller.CONTROL_MEMBERS
        with tempfile.TemporaryDirectory() as directory:
            archive_path = Path(directory) / "control.tar.gz"
            with tarfile.open(archive_path, "w:gz") as archive:
                for name in names:
                    archive.add(package / name, arcname=name)
            prepared = json.loads((HERE / "manifest.json").read_text())
            document = {"source_commit": prepared["source_commit"],
                        "binary_sha256": prepared["binary_sha256"],
                        "build_manifest_sha256": controller.sha256(package / "build-manifest.json"),
                        "batch_manifest_sha256": controller.sha256(package / "batch-manifest.json")}
            with mock.patch.object(controller, "_validate_archived_runtime_pins"):
                controller._validate_control_archive(document, archive_path,
                                                      package / "batch-manifest.json",
                                                      package / "build-manifest.json")
            broken = Path(directory) / "broken.tar.gz"
            with tarfile.open(broken, "w:gz") as archive:
                for name in names:
                    if name != "collector/cloud.py":
                        archive.add(package / name, arcname=name)
            with self.assertRaisesRegex(RuntimeError, "missing members"):
                with mock.patch.object(controller, "_validate_archived_runtime_pins"):
                    controller._validate_control_archive(document, broken,
                                                          package / "batch-manifest.json",
                                                          package / "build-manifest.json")

            extra = Path(directory) / "extra.tar.gz"
            with tarfile.open(extra, "w:gz") as archive:
                for name in names:
                    archive.add(package / name, arcname=name)
                extra_file = Path(directory) / "extra.txt"
                extra_file.write_text("unexpected\n")
                archive.add(extra_file, arcname="unexpected.txt")
            with self.assertRaisesRegex(RuntimeError, "unexpected members"):
                with mock.patch.object(controller, "_validate_archived_runtime_pins"):
                    controller._validate_control_archive(document, extra,
                                                          package / "batch-manifest.json",
                                                          package / "build-manifest.json")

            duplicate = Path(directory) / "duplicate-hash.tar.gz"
            hashes = (package / "control-hashes.txt").read_text()
            duplicate_hashes = Path(directory) / "control-hashes.txt"
            duplicate_hashes.write_text(hashes + hashes.splitlines()[0] + "\n")
            with tarfile.open(duplicate, "w:gz") as archive:
                for name in names:
                    archive.add(duplicate_hashes if name == "control-hashes.txt" else package / name, arcname=name)
            with self.assertRaisesRegex(RuntimeError, "duplicate control hash"):
                with mock.patch.object(controller, "_validate_archived_runtime_pins"):
                    controller._validate_control_archive(document, duplicate,
                                                          package / "batch-manifest.json",
                                                          package / "build-manifest.json")

    def test_execute_smoke_uses_fake_transport_and_retains_result_archive(self):
        package = HERE.parent
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "control-files.tar.gz").write_bytes(b"control")
            document = {
                "dispatch_id": "p1-test-execute", "batch_manifest_sha256": "a" * 64,
                "control_archive_sha256": "b" * 64, "source_commit": "c" * 40,
                "binary_sha256": "d" * 64, "profile": "full",
                "control_archive": "control-files.tar.gz",
                "remote_output_root": "/opt/p1/test-output",
                "remote_source_root": "/opt/graph-validation/test",
                "remote_claim_path": "/opt/p1/batch-claims/p1-test-execute/worker-1.json",
                "source_digest": "e" * 64, "input_manifest_sha256": "f" * 64,
                "remote_control_timeout_seconds": 360,
            }

            class FakeCloud:
                def __init__(self):
                    self.calls = []

                def environment(self):
                    self.calls.append("environment")
                    return {"FAKE_CLOUD": "1"}

                def url(self, name, permissions, env):
                    self.calls.append(("url", name, permissions))
                    return "fake://" + name

                def upload(self, path, name, env):
                    self.calls.append(("upload", Path(path).name, name))

                def run(self, vm, script):
                    self.calls.append(("run", vm))
                    self.script = script
                    return "POST_FIX_FULL_PROFILE_DIAGNOSTIC_UPLOAD_ACK\n"

                def download(self, name, path, env):
                    self.calls.append(("download", name))
                    payload = root / "payload.txt"
                    payload.write_text("retained\n")
                    with tarfile.open(path, "w:gz") as archive:
                        archive.add(payload, arcname="payload.txt")

            fake = FakeCloud()
            with mock.patch.object(controller, "PREP", root), \
                 mock.patch.object(controller, "PACKAGE_ROOT", root), \
                 mock.patch.object(controller, "CLAIM_PATH", root / "dispatch-claim.json"), \
                 mock.patch.object(controller, "read_manifest", return_value=document), \
                 mock.patch.object(controller, "load_cloud_transport", return_value=fake):
                controller.execute()
            self.assertEqual([call[0] for call in fake.calls if isinstance(call, tuple)],
                             ["upload", "url", "url", "run", "download"])
            self.assertIn("collector/run_remote.py", fake.script)
            self.assertIn("--batch-worker 1", fake.script)
            self.assertTrue((root / "raw" / "payload.txt").is_file())
            self.assertEqual(json.loads((root / "dispatch-claim.json").read_text())["profile"], "full")

    def test_retained_real_observation_and_diagnostics_schema_smoke(self):
        observation_path = Path("${GRAPH_ADVANTAGE_REPO_ROOT}/docs/implementation/graph-advantage/evidence/diagnostic-dispatch-1f20f694/raw/output/observation.ndjson")
        diagnostics_path = observation_path.with_name("diagnostics.json")
        if not observation_path.is_file() or not diagnostics_path.is_file():
            self.skipTest("retained diagnostic artifacts are unavailable")
        collector_spec = importlib.util.spec_from_file_location("postfix_collector_schema", HERE.parent / "collector/run_remote.py")
        collector = importlib.util.module_from_spec(collector_spec)
        collector_spec.loader.exec_module(collector)
        observation, _ = collector.read_json_object_with_raw(observation_path)
        diagnostics, raw_values = collector.read_json_object_with_raw(diagnostics_path)
        # The retained artifact is syntax-only; repin only this in-memory field
        # to exercise the post-fix full-profile schema without a product call.
        observation["profile"] = diagnostics["profile"] = "full"
        manifest = {"_path": observation["manifest_path"]}
        diagnostics["observation_path"] = str(observation_path.resolve())
        with mock.patch.object(collector, "EXPECTED_MUTATION_ID", observation["mutation_id"]), \
             mock.patch.object(collector, "EXPECTED_SOURCE_DIGEST", observation["source_digest"]):
            collector.validate_observation(observation, manifest, observation["binary_sha256"])
            counts = collector.validate_diagnostics(diagnostics, observation, observation_path,
                                                    manifest, observation["binary_sha256"], raw_values)
        self.assertEqual(counts, {"partial_failures": observation["partial_failures_count"],
                                  "warnings": observation["warnings_count"]})


if __name__ == "__main__":
    unittest.main()
