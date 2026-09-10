import hashlib
import json
import re
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]


class PrivacyRedactionTest(unittest.TestCase):
    def test_records_bind_current_sanitized_files_without_relabeling_outcomes(self):
        value = json.loads((HERE / "privacy-redactions.json").read_text())
        self.assertEqual(value["schema"], "graph-advantage-privacy-redactions-v1")
        self.assertEqual(value["record_count"], len(value["records"]))
        self.assertEqual(len({item["path"] for item in value["records"]}), value["record_count"])
        for item in value["records"]:
            path = REPO / item["path"]
            self.assertTrue(path.is_file(), item["path"])
            self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), item["retained_sha256"])
            self.assertRegex(item["source_sha256"], r"^[0-9a-f]{64}$")
            self.assertIn("measured source commit and outcome fields unchanged", item["claim"])

    def test_sanitized_files_do_not_retain_private_checkout_or_infrastructure_ids(self):
        value = json.loads((HERE / "privacy-redactions.json").read_text())
        forbidden = re.compile(
            r"/Users/thomi/|/home/[^<\s]+|entiregraphadv20260905|"
            r"rg-entire-graph-advantage-20260905|graph-validation-linux|graph-p1-worker-"
        )
        for item in value["records"]:
            text = (REPO / item["path"]).read_text()
            with self.subTest(path=item["path"]):
                self.assertIsNone(forbidden.search(text))

    def test_active_review_harness_requires_public_configuration(self):
        path = REPO / "docs/implementation/graph-advantage/probes/run_review_linux.py"
        text = path.read_text()
        for name in (
            "GRAPH_ADVANTAGE_AZURE_RESOURCE_GROUP",
            "GRAPH_ADVANTAGE_AZURE_STORAGE_ACCOUNT",
            "GRAPH_ADVANTAGE_AZURE_STORAGE_CONTAINER",
            "GRAPH_ADVANTAGE_VALIDATION_VM",
        ):
            self.assertIn(f"required_config('{name}')", text)
        self.assertNotIn("entiregraphadv20260905", text)
        self.assertNotIn("rg-entire-graph-advantage-20260905", text)
        self.assertNotIn("graph-validation-linux", text)


if __name__ == "__main__":
    unittest.main()
