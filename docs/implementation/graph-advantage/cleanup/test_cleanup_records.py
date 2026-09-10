#!/usr/bin/env python3
"""Validate compact cleanup records that replace removed runtime artifacts."""

import json
import unittest
from pathlib import Path


HERE = Path(__file__).resolve().parent


class CleanupRecordTest(unittest.TestCase):
    def test_profile_summaries_bind_external_raw_objects(self) -> None:
        record = json.loads((HERE / "profile-summaries.json").read_text())
        self.assertEqual(record["schema"], "graph-advantage-profile-summary-v1")
        self.assertEqual(len(record["profiles"]), 8)
        self.assertEqual(sum(len(profile["pprof"]["hotspot_rows"]) for profile in record["profiles"]), 3758)
        for profile in record["profiles"]:
            self.assertEqual(profile["external_content_id"], "sha256:" + profile["raw_profile_sha256"])
            self.assertTrue(profile["external_copy_verified"])
            self.assertTrue(profile["diagnostic_only"])
            self.assertTrue(profile["no_performance_claim"])
            self.assertGreater(profile["raw_profile_bytes"], 0)
            self.assertGreater(len(profile["pprof"]["hotspot_rows"]), 0)

    def test_consumer_parity_record_is_complete(self) -> None:
        record = json.loads((HERE / "consumer-parity.json").read_text())
        self.assertEqual(record["status"], "passed")
        self.assertEqual(len(record["summaries"]), 4)
        self.assertTrue(all(item["byte_equal"] for item in record["summaries"]))
        self.assertEqual(len(record["correctness_projections"]), 5)
        self.assertTrue(
            all(item["legacy_equal_after_declared_redaction"] for item in record["correctness_projections"])
        )


    def test_removal_map_only_contains_explicitly_replaced_files(self) -> None:
        record = json.loads((HERE / "removal-map.json").read_text())
        self.assertEqual(record["schema"], "graph-advantage-public-artifact-removal-map-v2")
        self.assertEqual(record["candidate_count"], len(record["candidates"]))
        self.assertEqual(record["candidate_bytes"], sum(item["bytes"] for item in record["candidates"]))
        self.assertEqual(len({item["path"] for item in record["candidates"]}), record["candidate_count"])
        self.assertEqual(
            {item["reason"] for item in record["candidates"]},
            {
                "normalized structured/case source",
                "raw profile archived outside Git and fully summarized",
                "exact Git source subset is reconstructible",
            },
        )
        repo = HERE.parents[3]
        for item in record["candidates"]:
            self.assertRegex(item["source_sha256"], r"^[0-9a-f]{64}$")
            self.assertIn(item["disposition"], {"applied", "approved-pending-strict-validation"})
            source = repo / item["path"]
            self.assertEqual(source.exists(), item["disposition"] != "applied")
            for replacement in [item["replacement"], *item.get("additional_replacements", [])]:
                retained = repo / replacement["path"]
                self.assertTrue(retained.is_file())
                self.assertEqual(
                    __import__("hashlib").sha256(retained.read_bytes()).hexdigest(),
                    replacement["retained_sha256"],
                )


if __name__ == "__main__":
    unittest.main()
