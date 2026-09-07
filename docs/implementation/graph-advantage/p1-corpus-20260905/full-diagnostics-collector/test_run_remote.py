import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import subprocess

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("collector", HERE / "run_remote.py")
collector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collector)


class FakeProcess:
    def __init__(self, exit_code=0):
        self.pid = 4242
        self.exit_code = exit_code

    def wait(self, timeout=None):
        return self.exit_code


class TimeoutProcess(FakeProcess):
    def __init__(self):
        super().__init__(exit_code=137)
        self.waits = 0

    def wait(self, timeout=None):
        self.waits += 1
        if self.waits == 1:
            raise subprocess.TimeoutExpired("fake", timeout)
        return self.exit_code


def fixture_artifacts(manifest, observation_path, binary_sha256, partial_count=194):
    failures = [
        {"code": f"E_{i:03d}", "severity": "warning", "file_path": f"f-{i:03d}.go",
         "effect_on_semantic_completeness": "partial", "detail": f"detail {i}"}
        for i in range(partial_count)
    ]
    warnings = [{"code": "W_WORKTREE_SNAPSHOT", "severity": "warning",
                 "effect_on_semantic_completeness": "none", "detail": "fixture"}]
    common = {
        "format_version": 1, "manifest_version": 1,
        "manifest_path": str(Path(manifest["_path"]).resolve()), "repository": collector.EXPECTED_REPOSITORY,
        "repository_path": collector.EXPECTED_REPO_PATH, "operation": "snapshot",
        "mode": "measure", "cache_mode": "off", "profile": collector.EXPECTED_PROFILE,
        "provider_version": collector.EXPECTED_PROVIDER, "mutation_id": collector.EXPECTED_MUTATION_ID,
        "source_digest": collector.EXPECTED_SOURCE_DIGEST, "binary_sha256": binary_sha256,
        "scenario": collector.EXPECTED_SCENARIO, "trial": 0, "reuse": False,
        "verb": "snapshot", "status": "partial", "semantic_sha256": collector.EXPECTED_SEMANTIC_SHA256,
        "semantic_digest": collector.EXPECTED_SEMANTIC_SHA256,
        "partial_failures_count": collector.EXPECTED_PARTIAL_FAILURES_COUNT,
        "partial_failures_sha256": collector.EXPECTED_PARTIAL_FAILURES_SHA256,
        "warnings_count": collector.EXPECTED_WARNINGS_COUNT,
        "warnings_sha256": collector.EXPECTED_WARNINGS_SHA256,
    }
    observation = dict(common)
    observation.update({
        "partial_failures": failures[:32], "warnings": warnings,
    })
    diagnostics = dict(common)
    diagnostics.update({
        "observation_path": str(Path(observation_path).resolve()), "partial_failures": failures,
        "warnings": warnings,
    })
    return observation, diagnostics


def budget_fixture(root, binary_sha256, scenario, *, scope="bounded-diagnostic",
                   cell_overrides=None, extra_cells=None, batch_id="diagnostic-test"):
    root = Path(root)
    build = root / "build-manifest.json"
    build.write_text(json.dumps({
        "source_commit": collector.EXPECTED_SOURCE_COMMIT,
        "binary_sha256": binary_sha256,
        "exit_code": 0,
    }))
    identity = {
        "binary_sha256": binary_sha256,
        "input_manifest_sha256": collector.EXPECTED_INPUT_MANIFEST_SHA256,
        "runner_sha256": collector.sha256(HERE / "run_remote.py"),
        "scenario_sha256": collector.sha256(scenario),
        "gate_sha256": collector.sha256(collector.P1_ROOT / "campaign_gate.py"),
        "budget_sha256": collector.sha256(collector.P1_ROOT / "batch_budget.py"),
        "build_manifest_sha256": collector.sha256(build),
    }
    cell = {
        "worker": 1,
        "repository": collector.EXPECTED_REPOSITORY,
        "profile": collector.EXPECTED_PROFILE,
        "verb": "snapshot",
        "scenario": collector.EXPECTED_SCENARIO,
        "trial": 0,
        "arms": [False],
        "preparatory_invocations": 0,
    }
    cell.update(cell_overrides or {})
    cell["cell_id"] = collector.batch_budget.cell_id(cell)
    cells = [cell] + list(extra_cells or [])
    workers = {}
    for item in cells:
        worker = str(item["worker"])
        workers[worker] = workers.get(worker, 0) + item.get("preparatory_invocations", 0) + len(item["arms"])
    batch = root / "batch-manifest.json"
    batch.write_text(json.dumps({
        "version": 1,
        "batch_id": batch_id,
        "scope": scope,
        "cap": collector.batch_budget.CAP,
        "identity": identity,
        "cells": cells,
        "workers": workers,
    }))
    return build, batch, cell


class CollectorTests(unittest.TestCase):
    def test_fingerprint_root_cannot_differ_from_product_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "fingerprint corpus root"):
                collector.run_request(root / "output", root / "binary", root / "scenario",
                                      root / "other-corpus", "b" * 64, root, None, None, 1)
            self.assertFalse((root / "output").exists())

    def test_raw_json_loader_rejects_duplicate_root_and_trailing_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            duplicate = root / "duplicate.json"
            duplicate.write_text('{"partial_failures": [], "partial_failures": []}')
            with self.assertRaisesRegex(RuntimeError, "duplicate root key"):
                collector.read_json_object_with_raw(duplicate)
            trailing = root / "trailing.json"
            trailing.write_text('{"partial_failures": []} trailing')
            with self.assertRaisesRegex(RuntimeError, "trailing JSON"):
                collector.read_json_object_with_raw(trailing)

    def test_full_diagnostics_requires_exact_artifact_count(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation_path = root / "observation.ndjson"
            observation, diagnostics = fixture_artifacts(manifest, observation_path, "b" * 64)
            diagnostics["partial_failures"] = diagnostics["partial_failures"][:-1]
            with self.assertRaisesRegex(RuntimeError, "full partial-failure artifact count"):
                collector.validate_diagnostics(diagnostics, observation_path, manifest, "b" * 64)

    def test_full_diagnostics_can_check_exact_raw_go_array_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation_path = root / "observation.ndjson"
            observation, diagnostics = fixture_artifacts(manifest, observation_path, "b" * 64)
            raw_partial = json.dumps(diagnostics["partial_failures"], separators=(",", ":")).encode()
            raw_warnings = json.dumps(diagnostics["warnings"], separators=(",", ":")).encode()
            diagnostics["partial_failures_sha256"] = hashlib.sha256(raw_partial).hexdigest()
            diagnostics["warnings_sha256"] = hashlib.sha256(raw_warnings).hexdigest()
            raw_values = {"partial_failures": raw_partial, "warnings": raw_warnings}
            result = collector.validate_diagnostics(
                diagnostics, observation_path, manifest, "b" * 64, raw_values,
                diagnostics["partial_failures_sha256"], diagnostics["warnings_sha256"],
            )
            self.assertEqual(result, {"partial_failures": 194, "warnings": 1})

    def test_identity_drift_is_rejected_before_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = {"_path": str(root / "manifest.json")}
            observation, _ = fixture_artifacts(manifest, root / "observation.ndjson", "b" * 64)
            observation["source_digest"] = "d" * 64
            with self.assertRaisesRegex(RuntimeError, "source_digest"):
                collector.validate_observation(observation, manifest, "b" * 64)

    def test_one_request_stops_on_unverified_full_digest_without_repetition(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            build, batch, _ = budget_fixture(root, binary_sha, scenario)
            calls = []

            def fingerprints(output, script, corpus_root, stage):
                calls.append(stage)
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                (Path(output) / f"{stage}.log").write_text("")
                return value

            def process_factory(command, **kwargs):
                calls.append("process")
                output = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"])
                manifest_path = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST"])
                manifest = json.loads(manifest_path.read_text())
                manifest["_path"] = str(manifest_path)
                observation, diagnostics = fixture_artifacts(manifest, output, binary_sha)
                output.write_text(json.dumps(observation))
                (output.parent / "diagnostics.json").write_text(json.dumps(diagnostics))
                time_path = Path(command[command.index("-o") + 1])
                time_path.write_text("Maximum resident set size (kbytes): 7\n")
                return FakeProcess()

            result = collector.run_request(
                root / "result", binary, scenario, Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                build, batch, 1,
                fingerprint_fn=fingerprints, process_factory=process_factory,
                _claim_root=root / "claims",
            )
            self.assertEqual(result["status"], "issue")
            self.assertIn("full partial-failure digest", result["issue"])
            self.assertEqual(calls, ["before", "process", "after"])
            outcome = json.loads((root / "result" / "outcome.json").read_text())
            self.assertFalse(outcome["admission_eligible"])
            self.assertEqual(outcome["review_status"], "not_reviewed")

    def test_one_bounded_request_captures_complete_diagnostics_and_completes_budget(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = collector.sha256(binary)
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            build, batch, cell = budget_fixture(root, binary_sha, scenario, batch_id="success-path")
            calls = []

            failures = [
                {"code": f"E_{i:03d}", "severity": "warning", "file_path": f"f-{i:03d}.go",
                 "effect_on_semantic_completeness": "partial", "detail": f"detail {i}"}
                for i in range(collector.EXPECTED_PARTIAL_FAILURES_COUNT)
            ]
            warnings = [{"code": "W_WORKTREE_SNAPSHOT", "severity": "warning",
                         "effect_on_semantic_completeness": "none", "detail": "fixture"}]
            raw_failures = json.dumps(failures, separators=(",", ":")).encode()
            raw_warnings = json.dumps(warnings, separators=(",", ":")).encode()
            failure_digest = hashlib.sha256(raw_failures).hexdigest()
            warning_digest = hashlib.sha256(raw_warnings).hexdigest()

            def fingerprints(output, script, corpus_root, stage):
                calls.append(stage)
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                (Path(output) / f"{stage}.log").write_text("")
                return value

            def process_factory(command, **kwargs):
                calls.append("process")
                output = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT"])
                manifest_path = Path(kwargs["env"]["ENTIRE_GRAPH_EXTRACTION_CORPUS_MANIFEST"])
                manifest = json.loads(manifest_path.read_text())
                manifest["_path"] = str(manifest_path)
                observation, diagnostics = fixture_artifacts(manifest, output, binary_sha)
                for value in (observation, diagnostics):
                    value["partial_failures_sha256"] = failure_digest
                    value["warnings_sha256"] = warning_digest
                diagnostics["partial_failures"] = failures
                diagnostics["warnings"] = warnings
                output.write_text(json.dumps(observation, separators=(",", ":")))
                (output.parent / "diagnostics.json").write_text(
                    json.dumps(diagnostics, separators=(",", ":"))
                )
                Path(command[command.index("-o") + 1]).write_text(
                    "Maximum resident set size (kbytes): 7\n"
                )
                return FakeProcess()

            with mock.patch.object(collector, "EXPECTED_PARTIAL_FAILURES_SHA256", failure_digest), \
                    mock.patch.object(collector, "EXPECTED_WARNINGS_SHA256", warning_digest):
                result = collector.run_request(
                    root / "result", binary, scenario,
                    Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                    build, batch, 1, fingerprint_fn=fingerprints,
                    process_factory=process_factory, _claim_root=root / "claims",
                )

            self.assertEqual(result["status"], "captured_expected_partial")
            self.assertEqual(result["diagnostics"], {"partial_failures": 194, "warnings": 1})
            self.assertEqual(calls, ["before", "process", "after"])
            token = f"{cell['cell_id']}:arm:false"
            state = json.loads(
                (root / "claims" / "success-path" / "worker-1.json").read_text()
            )
            self.assertEqual(state["started"], [token])
            self.assertEqual(state["completed"], [token])
            self.assertEqual(state["failed"], [])
            outcome = json.loads((root / "result" / "outcome.json").read_text())
            self.assertFalse(outcome["admission_eligible"])
            self.assertEqual(outcome["review_status"], "not_reviewed")
            self.assertEqual(outcome["diagnostics"], {"partial_failures": 194, "warnings": 1})
            self.assertTrue((root / "result" / "observation.ndjson").is_file())
            self.assertTrue((root / "result" / "diagnostics.json").is_file())

    def test_process_failure_stops_without_claiming_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            build, batch, cell = budget_fixture(root, binary_sha, scenario)
            calls = []

            def fingerprints(output, script, corpus_root, stage):
                calls.append(stage)
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                return value

            def process_factory(command, **kwargs):
                calls.append("process")
                time_path = Path(command[command.index("-o") + 1])
                time_path.write_text("Maximum resident set size (kbytes): 7\n")
                return FakeProcess(exit_code=9)

            result = collector.run_request(
                root / "result", binary, scenario, Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                build, batch, 1,
                fingerprint_fn=fingerprints, process_factory=process_factory,
                _claim_root=root / "claims",
            )
            self.assertEqual(result["status"], "issue")
            self.assertIn("exit 9", result["issue"])
            self.assertEqual(calls, ["before", "process", "after"])
            self.assertFalse((root / "result" / "diagnostics.json").exists())
            claim = root / "claims" / "diagnostic-test" / "worker-1.json"
            state = json.loads(claim.read_text())
            token = f"{cell['cell_id']}:arm:false"
            self.assertEqual(state["started"], [token])
            self.assertEqual(state["failed"], [token])
            with self.assertRaisesRegex(RuntimeError, "existing budget ledger refuses restart"):
                collector.run_request(
                    root / "retry-result", binary, scenario,
                    Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                    build, batch, 1, fingerprint_fn=fingerprints,
                    process_factory=process_factory, _claim_root=root / "claims",
                )
            self.assertFalse((root / "retry-result").exists())

    def test_wrong_or_multiple_diagnostic_cells_refuse_before_process(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = collector.sha256(binary)
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            build, wrong_batch, _ = budget_fixture(
                root, binary_sha, scenario, cell_overrides={"profile": "fast"},
                batch_id="wrong-cell",
            )
            process_calls = []
            with self.assertRaisesRegex(RuntimeError, "cell mismatch: profile"):
                collector.run_request(
                    root / "wrong-output", binary, scenario,
                    Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                    build, wrong_batch, 1,
                    process_factory=lambda *args, **kwargs: process_calls.append(1),
                    _claim_root=root / "claims",
                )
            self.assertEqual(process_calls, [])
            self.assertFalse((root / "wrong-output").exists())

            extra = {
                "worker": 1,
                "repository": collector.EXPECTED_REPOSITORY,
                "profile": collector.EXPECTED_PROFILE,
                "verb": "snapshot",
                "scenario": collector.EXPECTED_SCENARIO,
                "trial": 1,
                "arms": [False],
                "preparatory_invocations": 0,
            }
            extra["cell_id"] = collector.batch_budget.cell_id(extra)
            build, multiple_batch, _ = budget_fixture(
                root, binary_sha, scenario, extra_cells=[extra], batch_id="multiple-cell",
            )
            with self.assertRaisesRegex(RuntimeError, "exactly one invocation cell"):
                collector.run_request(
                    root / "multiple-output", binary, scenario,
                    Path(collector.EXPECTED_REPO_PATH).parent, binary_sha, source_root,
                    build, multiple_batch, 1, _claim_root=root / "claims",
                )
            self.assertFalse((root / "multiple-output").exists())

    def test_budget_start_is_durable_before_process_factory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = root / "source"
            source_root.mkdir()
            binary = source_root / "evaluator"
            binary.write_bytes(b"fake evaluator")
            binary_sha = collector.sha256(binary)
            scenario = source_root / "p1_scenario.py"
            scenario.write_text("# fake scenario\n")
            build, batch, cell = budget_fixture(root, binary_sha, scenario, batch_id="start-order")

            def fingerprints(output, script, corpus_root, stage):
                value = {"effective_tracked_input_sha256": collector.EXPECTED_SOURCE_DIGEST}
                (Path(output) / f"{stage}.json").write_text(json.dumps(value))
                return value

            def process_factory(command, **kwargs):
                claim = root / "claims" / "start-order" / "worker-1.json"
                token = f"{cell['cell_id']}:arm:false"
                self.assertEqual(json.loads(claim.read_text())["started"], [token])
                raise OSError("synthetic spawn failure")

            result = collector.run_request(
                root / "result", binary, scenario, Path(collector.EXPECTED_REPO_PATH).parent,
                binary_sha, source_root, build, batch, 1,
                fingerprint_fn=fingerprints, process_factory=process_factory,
                _claim_root=root / "claims",
            )
            self.assertEqual(result["status"], "issue")
            self.assertIn("synthetic spawn failure", result["issue"])
            claim = root / "claims" / "start-order" / "worker-1.json"
            token = f"{cell['cell_id']}:arm:false"
            state = json.loads(claim.read_text())
            self.assertEqual(state["started"], [token])
            self.assertEqual(state["failed"], [token])
            outcome = json.loads((root / "result" / "outcome.json").read_text())
            self.assertFalse(outcome["admission_eligible"])

    def test_timeout_kills_process_group_and_stops(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "evaluator"
            binary.write_bytes(b"fake")
            killed = []
            environment = {"ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT": str(root / "observation.ndjson")}

            def process_factory(command, **kwargs):
                Path(command[command.index("-o") + 1]).write_text(
                    "Maximum resident set size (kbytes): 7\n"
                )
                return TimeoutProcess()

            with self.assertRaisesRegex(RuntimeError, "timed out"):
                collector.run_process(
                    root, binary, environment, process_factory,
                    lambda pid, signal: killed.append((pid, signal)),
                )
            self.assertEqual(killed, [(4242, collector.signal.SIGKILL)])


if __name__ == "__main__":
    unittest.main()
