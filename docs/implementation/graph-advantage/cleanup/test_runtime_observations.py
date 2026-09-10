import hashlib
import json
import re
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent


class RuntimeObservationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path = HERE / "runtime-observations.json"
        cls.value = json.loads(cls.path.read_text())

    def test_schema_counts_and_source_bindings_are_complete(self):
        value = self.value
        self.assertEqual(value["schema"], "graph-advantage-runtime-observations-v1")
        self.assertEqual(value["source_count"], 181)
        self.assertEqual(value["source_bytes"], 1_294_649)
        self.assertEqual(value["record_count"], 111)
        aliases = [alias for record in value["records"] for alias in record["aliases"]]
        self.assertEqual(len(aliases), value["source_count"])
        self.assertEqual(len({alias["source_path"] for alias in aliases}), value["source_count"])
        for alias in aliases:
            self.assertRegex(alias["source_sha256"], r"^[0-9a-f]{64}$")
            self.assertRegex(alias["source_git_blob"], r"^[0-9a-f]{40,64}$")
            self.assertGreaterEqual(alias["source_bytes"], 0)

    def test_record_ids_bind_full_typed_values_and_order(self):
        for record in self.value["records"]:
            payload = json.dumps(
                {"category": record["category"], "data": record["data"]},
                sort_keys=True,
                separators=(",", ":"),
                ensure_ascii=False,
            ).encode()
            self.assertEqual(record["record_id"], "sha256:" + hashlib.sha256(payload).hexdigest())
            self.assertIsInstance(record["data"], (dict, list))
        categories = {record["category"] for record in self.value["records"]}
        self.assertEqual(categories, {
            "bounded-build-or-setup-failure",
            "compiler-correctness-response-set",
            "correctness-response-set",
            "diagnostic-claim-and-budget",
            "diagnostic-control-and-admission",
            "diagnostic-profile-status",
            "measurement-process-outcome",
            "measurement-request",
            "measurement-semantic-outcome",
            "runtime-environment-contract",
            "source-and-binary-identity",
            "source-input-fingerprint",
        })

    def test_meaningful_failures_and_correctness_responses_are_retained(self):
        data = [record["data"] for record in self.value["records"]]
        self.assertIn({"marker": "claim_missing", "claim_created": False}, data)
        self.assertTrue(any(item.get("exception_type") == "subprocess.CalledProcessError" and item.get("exit_status") == 128 for item in data if isinstance(item, dict)))
        self.assertTrue(any(item.get("diagnostic") == "C compiler warning" and item.get("source_line") == 254 for item in data if isinstance(item, dict)))
        correctness = [record for record in self.value["records"] if record["category"] == "correctness-response-set"]
        self.assertEqual(len(correctness), 4)
        self.assertTrue(all(set(record["data"]) == {"failed", "fixture_origin", "responses", "sources_by_stage"} for record in correctness))
        self.assertTrue(all(record["data"]["responses"] for record in correctness))

    def test_public_record_has_no_local_or_cloud_identifiers(self):
        text = self.path.read_text()
        for pattern in (
            r"/Users/", r"/home/", r"/tmp/", r"/opt/", r"/subscriptions/",
            r"blob\.core\.windows\.net", r"rg-entire-graph-advantage",
            r"graph-validation-linux", r"graph-p1-worker-",
        ):
            with self.subTest(pattern=pattern):
                self.assertIsNone(re.search(pattern, text, re.IGNORECASE))
        self.assertTrue(any(record["redactions"] for record in self.value["records"]))


if __name__ == "__main__":
    unittest.main()
