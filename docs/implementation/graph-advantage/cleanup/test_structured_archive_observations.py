import hashlib
import json
from pathlib import Path
import sys
import unittest

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
sys.path.insert(0, str(HERE))

import structured_archive_observations as subject


class TestStructuredArchiveObservations(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path = HERE / "structured-archive-observations.json"
        cls.saved = json.loads(cls.path.read_text())

    def test_generated_evidence_matches_frozen_archive_members(self):
        self.assertEqual(subject.build(REPO), self.saved)
        self.assertEqual(self.saved["record_count"], 37)

    def test_all_ordered_records_and_source_fields_are_retained(self):
        ordered = [
            record
            for record in self.saved["records"]
            if record["preservation"] == "full_sanitized_ordered_records"
        ]
        self.assertEqual(sum(record["record_count"] for record in ordered), 1569)
        campaigns = [record for record in ordered if record["member"] == "results/campaign.ndjson"]
        self.assertEqual([record["record_count"] for record in campaigns], [520, 520, 510])
        self.assertEqual(
            sum(row.get("status") == "unrun" for record in campaigns for row in record["data"]),
            1434,
        )
        # Every original row remains in source order, including fields that identify
        # the source, scenario, mode, profile, trial, reuse, and partial outcome.
        for record in ordered:
            self.assertEqual(record["record_count"], len(record["data"]))
            self.assertEqual(record["projection_sha256"], subject.canonical_sha(record["data"]))

    def test_compiler_and_query_objects_keep_complete_structured_values(self):
        full = [record for record in self.saved["records"] if record["preservation"].startswith("full_sanitized_")]
        compiler = [record for record in full if record["kind"] == "compiler result"]
        self.assertEqual(len(compiler), 6)
        for record in compiler:
            self.assertEqual(set(record["data"]), {"failed", "fixture_origin", "responses", "sources_by_stage"} if record["member"] == "compiler-advantage.json" else {"failed", "fixture_origin", "responses", "sources"})
            self.assertEqual(record["projection_sha256"], subject.canonical_sha(record["data"]))

    def test_machine_local_paths_are_normalized_and_declared(self):
        text = self.path.read_text()
        self.assertNotIn("/Users/", text)
        self.assertNotIn("/opt/p1/", text)
        self.assertNotIn("/opt/graph-validation/", text)
        self.assertNotIn("/tmp/Test", text)
        self.assertGreater(sum(len(record.get("redactions", [])) for record in self.saved["records"]), 0)

    def test_unique_runner_source_is_retained_byte_for_byte(self):
        record = next(record for record in self.saved["records"] if record["member"] == "run_remote.py")
        retained = REPO / record["retained_path"]
        self.assertTrue(retained.is_file())
        self.assertEqual(hashlib.sha256(retained.read_bytes()).hexdigest(), record["source_sha256"])


if __name__ == "__main__":
    unittest.main()
