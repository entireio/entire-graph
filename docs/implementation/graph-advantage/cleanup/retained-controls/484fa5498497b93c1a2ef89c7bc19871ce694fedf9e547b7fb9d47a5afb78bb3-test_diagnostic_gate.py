import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("diagnostic_gate", HERE / "diagnostic_gate.py")
diagnostic_gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnostic_gate)

SOURCE = "8" * 40
HASH = "a" * 64


class DiagnosticGateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.result_index = 0
        linux_manifest = self.root / "linux-manifest.json"
        linux_manifest.write_text(json.dumps({"source_commit": SOURCE, "remote_root": "/opt/source", "source_archive_sha256": HASH, "binary_sha256": HASH}))
        self.stages = []
        for stage_id in diagnostic_gate.REQUIRED_STAGES:
            log = self.root / f"{stage_id}.log"
            log.write_text(f"PASS {stage_id}\n")
            exit_path = self.root / f"{stage_id}.exit.txt"
            exit_path.write_text("0\n")
            is_build = stage_id == "build"
            self.stages.append({
                "id": stage_id,
                "command": "go test -v ./internal/sem -run '^TestNamed$'" if not is_build else "go test -c -o p1-evaluator ./internal/sem",
                "exit_code": 0,
                "log_path": log.name,
                "log_sha256": diagnostic_gate.sha256(log),
                "exit_path": exit_path.name,
                "exit_sha256": diagnostic_gate.sha256(exit_path),
                "matched_tests": 0 if is_build else 1,
                "passed_tests": 0 if is_build else 1,
                "named_passes": [] if is_build else [f"Test{stage_id.title().replace('_', '')}"],
                "actual_skips": [],
                "failures": [],
            })
        stage_results = self.root / "stage-results.json"
        stage_results.write_text(json.dumps({"source_commit": SOURCE, "source_archive_sha256": HASH,
                                              "remote_root": "/opt/source", "binary_sha256": HASH,
                                              "stages": self.stages}, sort_keys=True))
        build_manifest = self.root / "build-manifest.json"
        build_manifest.write_text(json.dumps({"source_commit": SOURCE, "source_archive_sha256": HASH,
                                               "remote_root": "/opt/source", "binary_sha256": HASH,
                                               "exit_code": 0,
                                               "commands": {item["id"]: item["command"] for item in self.stages},
                                               "stage_exit_codes": {item["id"]: 0 for item in self.stages}}, sort_keys=True))
        self.gate = {
            "schema": "diagnostic-validation-v1",
            "status": "passed",
            "purpose": "single-non-admission-timeout-diagnostic",
            "required_for_execution": True,
            "admission_eligible": False,
            "maximum_product_invocations": 1,
            "allowed_cell": {"cache": "off", "profile": "full", "verb": "snapshot", "scenario": "diagnostic", "arms": [False], "preparatory_invocations": 0, "retry_limit": 0, "comparison": False},
            "evaluator_source_commit": SOURCE,
            "source_archive_sha256": HASH,
            "evaluator_binary_sha256": HASH,
            "evaluator_build_manifest_sha256": diagnostic_gate.sha256(build_manifest),
            "input_manifest_sha256": HASH,
            "pinned_linux": {
                "manifest_path": linux_manifest.name,
                "manifest_sha256": diagnostic_gate.sha256(linux_manifest),
                "build_manifest_path": build_manifest.name,
                "source_commit": SOURCE,
                "source_archive_sha256": HASH,
                "remote_root": "/opt/source",
                "stage_results_path": stage_results.name,
                "stage_results_sha256": diagnostic_gate.sha256(stage_results),
                "required_stage_ids": list(diagnostic_gate.REQUIRED_STAGES),
                "expected_stages": [{"id": item["id"], "command": item["command"],
                                     "required_tests": list(item["named_passes"]), "allowed_skips": []}
                                    for item in self.stages],
                "build_exit_code": 0,
                "binary_sha256": HASH,
            },
            "final_full_check_required_before": ["implementation-admission", "stability-sampling", "merge", "release", "quantitative-publication"],
            "claims": {"correctness": "focused-exact-source-only", "performance": "none", "stability": "none", "release": "none"},
        }
        self.manifest = {
            "validation_gate_kind": "diagnostic-validation-v1",
            "admission_eligible": False, "no_comparison": True, "no_on_arm": True,
            "total_attempts": 1, "derived_invocations": 1, "preparatory_invocations": 0,
            "worker": 1, "cache": "off", "profile": "full", "verb": "snapshot", "scenario": "diagnostic",
            "source_commit": SOURCE, "source_archive_sha256": HASH, "binary_sha256": HASH,
            "build_manifest_sha256": diagnostic_gate.sha256(build_manifest), "input_manifest_sha256": HASH,
            "remote_source_root": "/opt/source",
        }

    def tearDown(self):
        self.temporary.cleanup()

    def validate(self, gate=None, manifest=None):
        gate = copy.deepcopy(self.gate if gate is None else gate)
        manifest = copy.deepcopy(self.manifest if manifest is None else manifest)
        path = self.root / "gate.json"
        path.write_text(json.dumps(gate, sort_keys=True))
        manifest["validation_gate_sha256"] = diagnostic_gate.sha256(path)
        return diagnostic_gate.validate(manifest, path, self.root)

    def rewrite_stage_results(self, gate, mutate):
        self.result_index += 1
        results_path = self.root / f"mutated-stage-results-{self.result_index}.json"
        results = {"source_commit": SOURCE, "source_archive_sha256": HASH,
                   "remote_root": "/opt/source", "binary_sha256": HASH,
                   "stages": copy.deepcopy(self.stages)}
        mutate(results)
        results_path.write_text(json.dumps(results, sort_keys=True))
        gate["pinned_linux"]["stage_results_path"] = results_path.name
        gate["pinned_linux"]["stage_results_sha256"] = diagnostic_gate.sha256(results_path)

    def test_valid_exact_single_diagnostic(self):
        self.assertEqual(self.validate()["schema"], "diagnostic-validation-v1")

    def test_rejects_failed_missing_stale_source_and_hash(self):
        for mutation, message in (
            (lambda gate, manifest: gate.update(status="failed"), "status"),
            (lambda gate, manifest: gate.pop("pinned_linux"), "pinned Linux"),
            (lambda gate, manifest: manifest.update(source_commit="9" * 40), "source mismatch"),
            (lambda gate, manifest: gate.update(evaluator_binary_sha256="b" * 64), "identity mismatch"),
        ):
            with self.subTest(message=message):
                gate, manifest = copy.deepcopy(self.gate), copy.deepcopy(self.manifest)
                mutation(gate, manifest)
                with self.assertRaisesRegex(RuntimeError, message):
                    self.validate(gate, manifest)

    def test_rejects_absent_required_stage_and_bad_actual_results(self):
        cases = []
        missing = copy.deepcopy(self.gate)
        self.rewrite_stage_results(missing, lambda result: result["stages"].pop(0))
        cases.append((missing, "missing, duplicated, or reordered"))
        for key, value, message in (
            ("exit_code", 1, "must be integer zero"),
            ("matched_tests", 0, "lacks required executed tests"),
            ("passed_tests", 0, "named passes malformed"),
            ("actual_skips", ["TestUnexpected"], "skips differ"),
            ("failures", ["TestFailed"], "failures present"),
        ):
            gate = copy.deepcopy(self.gate)
            self.rewrite_stage_results(gate, lambda result, key=key, value=value: result["stages"][0].__setitem__(key, value))
            cases.append((gate, message))
        for gate, message in cases:
            with self.subTest(message=message):
                with self.assertRaisesRegex(RuntimeError, message):
                    self.validate(gate)

    def test_hash_bound_results_cannot_change_command_or_counts_with_gate(self):
        gate = copy.deepcopy(self.gate)
        gate["pinned_linux"]["expected_stages"][0]["command"] = "go test -v ./internal/sem -run '^TestOther$'"
        with self.assertRaisesRegex(RuntimeError, "commands differ"):
            self.validate(gate)
        gate = copy.deepcopy(self.gate)
        self.rewrite_stage_results(gate, lambda result: result["stages"][0].update(
            matched_tests=2, passed_tests=2, named_passes=["TestOne"]))
        with self.assertRaisesRegex(RuntimeError, "named passes malformed"):
            self.validate(gate)
        gate = copy.deepcopy(self.gate)
        gate["pinned_linux"]["expected_stages"][0]["required_tests"] = ["TestRequiredButMissing"]
        with self.assertRaisesRegex(RuntimeError, "missing a required test"):
            self.validate(gate)

    def test_rejects_bool_as_integer(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["total_attempts"] = True
        with self.assertRaisesRegex(RuntimeError, "scope mismatch"):
            self.validate(manifest=manifest)
        gate = copy.deepcopy(self.gate)
        self.rewrite_stage_results(gate, lambda result: result["stages"][0].__setitem__("exit_code", False))
        with self.assertRaisesRegex(RuntimeError, "integer zero"):
            self.validate(gate)

    def test_rejects_wrong_scope_full_approval_and_dual_gate(self):
        for key, value in (("cache", "on"), ("profile", "fast"), ("verb", "search"),
                           ("derived_invocations", 2), ("preparatory_invocations", 1),
                           ("no_on_arm", False), ("admission_eligible", True)):
            with self.subTest(key=key):
                manifest = copy.deepcopy(self.manifest)
                manifest[key] = value
                with self.assertRaises(RuntimeError):
                    self.validate(manifest=manifest)
        manifest = copy.deepcopy(self.manifest)
        manifest["full_check_gate"] = "old.json"
        with self.assertRaisesRegex(RuntimeError, "cannot coexist"):
            self.validate(manifest=manifest)

    def test_rejects_path_escape_symlink_and_changed_log(self):
        outside = self.root.parent / "outside-diagnostic.log"
        outside.write_text("PASS\n")
        self.addCleanup(lambda: outside.unlink(missing_ok=True))
        for path_value, message in (("../outside-diagnostic.log", "escapes"), ("linked.log", "non-symlink")):
            gate = copy.deepcopy(self.gate)
            if path_value == "linked.log":
                (self.root / path_value).symlink_to(self.root / "affected_normal.log")
            def mutate(result):
                result["stages"][0]["log_path"] = path_value
                if path_value == "linked.log":
                    result["stages"][0]["log_sha256"] = diagnostic_gate.sha256(self.root / "affected_normal.log")
                else:
                    result["stages"][0]["log_sha256"] = hashlib.sha256(b"PASS\n").hexdigest()
            self.rewrite_stage_results(gate, mutate)
            with self.subTest(path=path_value):
                with self.assertRaisesRegex(RuntimeError, message):
                    self.validate(gate)
        gate = copy.deepcopy(self.gate)
        self.rewrite_stage_results(gate, lambda result: result["stages"][0].__setitem__("log_sha256", "f" * 64))
        with self.assertRaisesRegex(RuntimeError, "hash changed"):
            self.validate(gate)

    def test_failed_preflight_has_no_claim_side_effect(self):
        claim = self.root / "dispatch-claim.json"
        gate = copy.deepcopy(self.gate)
        self.rewrite_stage_results(gate, lambda result: result["stages"][0].__setitem__("matched_tests", 0))
        with self.assertRaises(RuntimeError):
            self.validate(gate)
        self.assertFalse(claim.exists())


if __name__ == "__main__":
    unittest.main()
