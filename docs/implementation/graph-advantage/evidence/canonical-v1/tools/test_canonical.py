#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import convert
import compat
import validate


class CanonicalEvidenceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.evidence = self.root / "evidence"
        self.output = self.evidence / "canonical-v1"
        self.evidence.mkdir()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_json(self, relative: str, value: object) -> None:
        path = self.evidence / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))

    def test_explicit_failure_partial_and_unknowns_are_not_promoted(self) -> None:
        self.write_json("check-failed/result.json", {
            "status": "failed", "source_commit": "abc", "full_check_pass": False,
            "go_test_skips": [{"name": "live compiler", "reason": "opt-in"}],
            "product_or_corpus_invocations": 0,
        })
        self.write_json("diagnostic-timeout/controller/runtime-manifest.json", {
            "source_commit": "def", "process": {"timed_out": True},
            "partial_failures_count": 194, "peak_rss_bytes": None,
            "repository": "/Users/example/private/repo",
            "claim_counts": {"effective_attempts": 1, "product": 1},
        })
        converted = convert.convert(self.evidence, self.output)
        self.assertEqual(converted, ["check-failed", "diagnostic-timeout"])
        failed = json.loads((self.output / "runs/check-failed.json").read_text())
        timeout = json.loads((self.output / "runs/diagnostic-timeout.json").read_text())
        self.assertEqual(failed["execution"]["status"], "failed")
        self.assertEqual(failed["coverage"]["skips"][0]["name"], "live compiler")
        self.assertEqual(timeout["execution"]["status"], "timeout")
        self.assertEqual(timeout["semantics"]["partial_failures"]["count"], 194)
        self.assertIsNone(timeout["execution"]["peak_rss_bytes"]["value"])
        self.assertTrue(timeout["execution"]["peak_rss_bytes"]["unavailable_reason"])
        self.assertIsNone(timeout["source_records"]["controller/runtime-manifest.json"]["repository"])
        self.assertTrue(timeout["redactions"])
        self.assertEqual(validate.validate_tree(self.output, self.evidence), 2)

    def test_cases_preserve_pairing_order_and_failure_rows(self) -> None:
        rows = [
            {"size": 12, "profile": "full", "scenario": "one-edit", "trial": 0, "reuse": True, "elapsed_ns": 20, "equal": False, "status": "partial"},
            {"size": 12, "profile": "full", "scenario": "one-edit", "trial": 0, "reuse": False, "elapsed_ns": 10, "equal": False, "status": "failed"},
        ]
        path = self.evidence / "extraction-paired-v1.ndjson"
        path.write_text("".join(json.dumps(row) + "\n" for row in rows))
        convert.convert(self.evidence, self.output)
        run = json.loads((self.output / "runs/extraction-paired-v1.json").read_text())
        cases = [json.loads(line) for line in (self.output / "cases/extraction-paired-v1.ndjson").read_text().splitlines()]
        self.assertEqual(run["cases"]["count"], 2)
        self.assertEqual(sorted(case["observed_order"] for case in cases), [0, 1])
        self.assertEqual({case["status"] for case in cases}, {"failed", "partial"})
        self.assertTrue(all(case["observed"]["equal"] is False for case in cases))
        self.assertEqual(compat.load_legacy_rows(self.output / "runs/extraction-paired-v1.json"), rows)
        self.assertEqual(validate.validate_tree(self.output, self.evidence), 1)

    def test_adverse_status_wins_over_successful_transport(self) -> None:
        self.write_json("diagnostic-conflict/controller/runtime-manifest.json", {
            "transport_exit": 0,
            "controller_exit": 0,
            "collector_exit": 1,
            "process": {"timed_out": True, "exit_code": 1},
        })
        self.write_json("check-identity/result.json", {
            "status": "passed",
            "mise_check_exit": 0,
            "before_after_identity_equal": False,
        })
        convert.convert(self.evidence, self.output)
        timeout = json.loads((self.output / "runs/diagnostic-conflict.json").read_text())
        identity = json.loads((self.output / "runs/check-identity.json").read_text())
        self.assertEqual(timeout["execution"]["status"], "timeout")
        self.assertEqual(identity["execution"]["status"], "passed")
        self.assertEqual(identity["gates"][0]["status"], "failed")

    def test_only_authoritative_run_outcomes_set_status(self) -> None:
        self.write_json("diagnostic-dispatch-prep/build-manifest.json", {
            "exit_code": 0, "product_invocations": 0, "corpus_invocations": 0,
        })
        self.write_json("check-transport/result.json", {"transport_exit": 0})
        self.write_json("check-suite/verification.json", {
            "status": "passed", "expected_negative": {"exit_code": 1, "status": "failed"},
        })
        self.write_json("check-terminal/result.json", {"status": "passed"})
        self.write_json("check-terminal/controller/runtime-manifest.json", {"status": "prepared; not run"})
        self.write_json("diagnostic-issue/raw/output/outcome.json", {"issue": "collector lost its durable status"})
        self.write_json("diagnostic-issue/raw/output/process.json", {"exit_code": 0})
        convert.convert(self.evidence, self.output)
        prep = json.loads((self.output / "runs/diagnostic-dispatch-prep.json").read_text())
        transport = json.loads((self.output / "runs/check-transport.json").read_text())
        suite = json.loads((self.output / "runs/check-suite.json").read_text())
        terminal = json.loads((self.output / "runs/check-terminal.json").read_text())
        issue = json.loads((self.output / "runs/diagnostic-issue.json").read_text())
        self.assertEqual(prep["execution"]["status"], "not_run")
        self.assertEqual(transport["execution"]["status"], "unknown")
        self.assertEqual(suite["execution"]["status"], "passed")
        self.assertEqual(terminal["execution"]["status"], "passed")
        self.assertEqual(issue["execution"]["status"], "failed")

    def test_malformed_structured_source_is_retained_as_parse_error(self) -> None:
        source = self.evidence / "check-malformed/result.json"
        source.parent.mkdir(parents=True)
        source.write_text('{"status":')
        convert.convert(self.evidence, self.output)
        run = json.loads((self.output / "runs/check-malformed.json").read_text())
        self.assertEqual(run["execution"]["status"], "unknown")
        error = run["source_records"]["result.json"]["source_parse_error"]
        self.assertEqual(error["kind"], "json_decode_error")
        self.assertEqual(error["source_ref"], "result.json")
        self.assertEqual(error["source_sha256"], convert.sha256_file(source))
        self.assertEqual(error["source_size_bytes"], source.stat().st_size)
        self.assertEqual(validate.validate_tree(self.output, self.evidence), 1)

    def test_non_finite_measurement_is_unavailable_and_never_emitted(self) -> None:
        measurement = convert.nullable_measurement(float("nan"), "non-finite source value")
        self.assertIsNone(measurement["value"])
        self.assertEqual(measurement["unavailable_reason"], "non-finite source value")
        with self.assertRaises(ValueError):
            convert.canonical_bytes({"invalid": float("inf")})

    def test_duplicate_rows_get_occurrence_discriminators(self) -> None:
        path = self.evidence / "relation-profile-v1.ndjson"
        row = {"language": "go", "phase": "relations", "status": "ok", "elapsed_ns": 1}
        path.write_text(json.dumps(row) + "\n" + json.dumps(row) + "\n")
        convert.convert(self.evidence, self.output)
        cases = [json.loads(line) for line in (self.output / "cases/relation-profile-v1.ndjson").read_text().splitlines()]
        self.assertEqual(len({case["case_id"] for case in cases}), 2)
        self.assertEqual(sorted(case["observed_order"] for case in cases), [0, 1])
        self.assertTrue(all("occurrence=" in case["case_id"] for case in cases))

    def test_absent_legacy_source_requires_exact_admitted_replacement(self) -> None:
        self.write_json("check-pass/result.json", {"status": "passed"})
        convert.convert(self.evidence, self.output)
        source = self.evidence / "check-pass/result.json"
        source_sha = convert.sha256_file(source)
        source.unlink()
        with self.assertRaisesRegex(ValueError, "lacks an admitted exact replacement"):
            validate.validate_tree(self.output, self.evidence)
        run = self.output / "runs/check-pass.json"
        self.write_json("../cleanup/removal-map.json", {
            "schema": "graph-advantage-public-artifact-removal-map-v2",
            "candidate_count": 1,
            "candidates": [{
                "path": "docs/implementation/graph-advantage/evidence/check-pass/result.json",
                "source_sha256": source_sha,
                "disposition": "applied",
                "replacement": {
                    "path": "canonical-v1/runs/check-pass.json",
                    "retained_sha256": convert.sha256_file(run),
                },
            }],
        })
        self.assertEqual(validate.validate_tree(self.output, self.evidence), 1)
        removal_map = self.evidence.parent / "cleanup/removal-map.json"
        value = json.loads(removal_map.read_text())
        value["candidates"][0]["source_sha256"] = "0" * 64
        removal_map.write_text(json.dumps(value))
        with self.assertRaisesRegex(ValueError, "lacks an admitted exact replacement"):
            validate.validate_tree(self.output, self.evidence)

    def test_complete_validation_requires_auxiliary_indices(self) -> None:
        self.write_json("check-pass/result.json", {"status": "passed"})
        convert.convert(self.evidence, self.output)
        (self.output / "index.json").unlink()
        with self.assertRaisesRegex(ValueError, "index.json is required"):
            validate.validate_tree(self.output, self.evidence, complete=True)

    def test_sanitizer_removes_windows_and_unix_environment_paths(self) -> None:
        clean, redactions = convert.sanitize({"environment": r"C:\\Users\\person\\tool.exe", "command": ["/usr/local/bin/go"], "version": "go1.26.1"})
        self.assertIsNone(clean["environment"])
        self.assertEqual(clean["command"], ["go"])
        self.assertEqual(clean["version"], "go1.26.1")
        self.assertEqual(len(redactions), 2)

    def test_validator_rejects_unknown_measurement_without_reason(self) -> None:
        self.write_json("check-pass/result.json", {"status": "passed"})
        convert.convert(self.evidence, self.output)
        path = self.output / "runs/check-pass.json"
        run = json.loads(path.read_text())
        run["execution"]["peak_rss_bytes"] = {"value": None, "unavailable_reason": None}
        path.write_text(json.dumps(run))
        with self.assertRaisesRegex(ValueError, "exactly one"):
            validate.validate_tree(self.output, self.evidence)

    def test_validator_rejects_duplicate_cases(self) -> None:
        path = self.evidence / "relation-profile-v1.ndjson"
        path.write_text(json.dumps({"language": "go", "phase": "relations", "trial": 0, "elapsed_ns": 1, "result_count": 2}) + "\n")
        convert.convert(self.evidence, self.output)
        cases = self.output / "cases/relation-profile-v1.ndjson"
        payload = cases.read_text()
        cases.write_text(payload + payload)
        run_path = self.output / "runs/relation-profile-v1.json"
        run = json.loads(run_path.read_text())
        run["cases"]["sha256"] = convert.sha256_file(cases)
        run["cases"]["count"] = 2
        run_path.write_bytes(convert.canonical_bytes(run))
        with self.assertRaisesRegex(ValueError, "duplicate case_id"):
            validate.validate_tree(self.output, self.evidence)


if __name__ == "__main__":
    unittest.main()
