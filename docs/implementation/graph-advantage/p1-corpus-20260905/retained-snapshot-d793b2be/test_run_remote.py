import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("retry", HERE / "run_remote.py")
retry = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retry)


class RetryRunnerTests(unittest.TestCase):
    def test_observation_must_preserve_prior_partial_and_warning_identities(self):
        observation = json.loads(
            (HERE.parent / "retained-snapshot-05ad9842-retry" / "raw" / "observation-off.ndjson").read_text()
        )
        self.assertIs(
            retry.validate_observation(
                observation,
                "off",
                observation["binary_sha256"],
                observation["source_digest"],
            ),
            observation,
        )
        observation["warnings_sha256"] = "changed"
        with self.assertRaises(RuntimeError):
            retry.validate_observation(
                observation,
                "off",
                observation["binary_sha256"],
                observation["source_digest"],
            )

    def test_fake_process_plumbing_retains_null_rss(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            fake = root / "fake-binary"
            fake.write_text(
                "#!" + sys.executable + "\n"
                "import json, os\n"
                "c=json.load(open(os.environ['ENTIRE_GRAPH_EXTRACTION_CORPUS_CONFIG']))\n"
                "a=c['cache']\n"
                "o={'repository':'kubernetes-kubernetes','operation':'snapshot',"
                "'profile':'syntax-only','provider_version':'p1-corpus-20260905',"
                "'status':'partial','source_digest':'s'*64,'binary_sha256':'b'*64,"
                "'semantic_sha256':'fa08ae3464a63c71db89f5755062ac76b3a8960e5bccd2f536c1491d8543b4f7',"
                "'semantic_digest':'fa08ae3464a63c71db89f5755062ac76b3a8960e5bccd2f536c1491d8543b4f7',"
                "'partial_failures_count':194,"
                "'partial_failures_sha256':'846649bc1925c607b91b3f41014408938c37232d0a12d86f71569776b46819ef',"
                "'warnings_count':1,'warnings_sha256':'e0ce85fefeba137c4e41fcfa3bc5f1d62d461bc1f4fc7eff5587bbd52cf50468',"
                "'cache_mode':a,'reuse':a=='on'}\n"
                "json.dump(o, open(os.environ['ENTIRE_GRAPH_EXTRACTION_CORPUS_OUTPUT'],'w'))\n"
            )
            fake.chmod(0o700)
            observation = retry.run_arm(
                root, fake, fake, root, "b" * 64, "s" * 64, "off"
            )
            self.assertEqual(observation["partial_failures_count"], 194)
            process = json.loads((root / "process-off.json").read_text())
            self.assertIsNone(process["peak_rss_bytes"])
            self.assertIn("unavailable", process["rss_status"])


if __name__ == "__main__":
    unittest.main()
