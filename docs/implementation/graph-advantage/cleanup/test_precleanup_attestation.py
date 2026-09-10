import json, tempfile, unittest
from pathlib import Path
from validate_precleanup_attestation import verify

HERE=Path(__file__).resolve().parent
REPO=HERE.parents[3]

class TestPrecleanupAttestation(unittest.TestCase):
    def test_complete_attestation(self):
        self.assertEqual(verify(REPO,HERE/"precleanup-parity-attestation.json"),[])

    def test_missing_attestation_fails(self):
        self.assertEqual(verify(REPO,HERE/"missing-attestation.json"),["missing precleanup parity attestation"])

    def test_order_mutation_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            data=json.loads((HERE/"precleanup-parity-attestation.json").read_text())
            data["correctness_queries"]["ordered_case_ids"].reverse()
            path=Path(tmp)/"attestation.json"; path.write_text(json.dumps(data))
            self.assertIn("correctness query order",verify(REPO,path))

    def test_missing_public_output_is_reported_without_exception(self):
        with tempfile.TemporaryDirectory() as tmp:
            data=json.loads((HERE/"precleanup-parity-attestation.json").read_text())
            data["structured"]["public_path"]="missing-public.json"
            path=Path(tmp)/"attestation.json"; path.write_text(json.dumps(data))
            self.assertIn("missing attested public output: missing-public.json",verify(REPO,path))

    def test_combination_order_mutation_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            data=json.loads((HERE/"precleanup-parity-attestation.json").read_text())
            data["correctness_combinations"]["records"][0]["response_order"].reverse()
            path=Path(tmp)/"attestation.json"; path.write_text(json.dumps(data))
            self.assertTrue(any(error.startswith("correctness combination projection:") for error in verify(REPO,path)))

    def test_platform_projection_mutation_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            data=json.loads((HERE/"precleanup-parity-attestation.json").read_text())
            data["platform_observations"]["records"][0]["projection_sha256"]="0"*64
            path=Path(tmp)/"attestation.json"; path.write_text(json.dumps(data))
            self.assertIn("platform observations source-to-projection mapping",verify(REPO,path))

    def test_vm_historical_revision_mutation_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            data=json.loads((HERE/"precleanup-parity-attestation.json").read_text())
            data["vm_statusline_observations"]["source_revision"]="rewritten"
            path=Path(tmp)/"attestation.json"; path.write_text(json.dumps(data))
            self.assertIn("vm statusline observations source-to-projection mapping",verify(REPO,path))

if __name__=="__main__": unittest.main()
