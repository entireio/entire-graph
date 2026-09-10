import hashlib, json, unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent

class TestArchiveProfileSummaries(unittest.TestCase):
    def test_six_profiles_reference_verified_summaries_and_six_sidecars_remain(self):
        data = json.loads((HERE / "archive-profile-summaries.json").read_text())
        profiles = [x for x in data["records"] if x["kind"] == "profile_summary_reference"]
        sidecars = [x for x in data["records"] if x["kind"] == "profile_metadata"]
        self.assertEqual(len(profiles), 6)
        self.assertEqual(len(sidecars), 6)
        self.assertEqual(len({x["source_sha256"] for x in profiles}), 6)
        self.assertTrue(all(x["profile_summary_ref"] == "cleanup/profile-summaries.json" for x in profiles))
        self.assertTrue(all(x["no_performance_claim"] for x in profiles))

    def test_profile_text_sources_reference_the_live_summary_without_raw_samples(self):
        data = json.loads((HERE / "archive-profile-summaries.json").read_text())
        text = [x for x in data["records"] if x["kind"] == "profile_text_summary_reference"]
        self.assertEqual(len(text), 24)
        self.assertEqual(len({(x["source_path"], x["source_sha256"]) for x in text}), 24)
        summary = HERE / "profile-summaries.json"
        digest = hashlib.sha256(summary.read_bytes()).hexdigest()
        self.assertTrue(all(x["profile_summary_sha256"] == digest for x in text))
        reconstructed = [x for x in text if "reconstruction" in x]
        preserved = [x for x in text if "preserved_lines" in x]
        self.assertEqual(len(reconstructed), 20)
        self.assertEqual(len(preserved), 4)
        self.assertTrue(all(x["external_copy_verified"] and x["source_commit"] and x["reconstruction"]["selector"] for x in reconstructed))
        self.assertTrue(all(x["raw_profile_sha256"] in x["external_content_id"] for x in reconstructed))
        self.assertTrue(all(x["reconstruction"]["argv_template"] for x in reconstructed))
        listed = [x for x in reconstructed if x["reconstruction"]["selector"].startswith("list ")]
        self.assertTrue(all(x["reconstruction"]["source_materialization"]["git_commit"] == x["source_commit"] for x in listed))
        self.assertTrue(all(x["preserved_lines"] for x in preserved))
        proof = data["representative_reconstruction_proof"]
        self.assertEqual(proof["status"], "passed")
        self.assertEqual(proof["source_sha256"], proof["reconstructed_sha256"])

if __name__ == "__main__":
    unittest.main()
