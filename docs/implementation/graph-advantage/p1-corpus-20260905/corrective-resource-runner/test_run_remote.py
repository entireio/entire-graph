import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("runner", HERE / "run_remote.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ResourceRunnerTests(unittest.TestCase):
    def test_parse_peak_rss_requires_one_kbyte_line(self):
        self.assertEqual(
            runner.parse_peak_rss_bytes(
                "Command being timed: x\nMaximum resident set size (kbytes): 123\n"
            ),
            123 * 1024,
        )
        for raw in (
            "",
            "Maximum resident set size (bytes): 123\n",
            "Maximum resident set size (kbytes): 0\n",
            "Maximum resident set size (kbytes): -1\n",
            "Maximum resident set size (kbytes): 1\nMaximum resident set size (kbytes): 2\n",
        ):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                runner.parse_peak_rss_bytes(raw)

    def test_retained_partial_identity_fixture_still_validates(self):
        observation = json.loads(
            (
                HERE.parent / "retained-snapshot-05ad9842-retry" / "raw" /
                "observation-off.ndjson"
            ).read_text()
        )
        validated = runner.validate_observation(
            observation,
            "off",
            observation["binary_sha256"],
            observation["source_digest"],
        )
        self.assertEqual(validated["partial_failures_count"], 194)

    def fake_binary(self, root):
        binary = root / "fake-binary"
        binary.write_text(
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
        binary.chmod(0o700)
        return binary

    def fake_time(self, root, rss="7"):
        timer = root / "fake-time"
        timer.write_text(
            "#!" + sys.executable + "\n"
            "import pathlib, subprocess, sys\n"
            "args=sys.argv[1:]; output=pathlib.Path(args[args.index('-o')+1])\n"
            "code=subprocess.run(args[args.index('--')+1:]).returncode\n"
            f"output.write_text('Maximum resident set size (kbytes): {rss}\\n')\n"
            "raise SystemExit(code)\n"
        )
        timer.chmod(0o700)
        return timer

    def test_fake_process_records_external_rss(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = self.fake_binary(root)
            timer = self.fake_time(root)
            with mock.patch.object(runner, "TIME_BINARY", str(timer)):
                observation = runner.run_arm(
                    root, binary, binary, root, "b" * 64, "s" * 64, "off"
                )
            self.assertEqual(observation["partial_failures_count"], 194)
            process = json.loads((root / "process-off.json").read_text())
            self.assertGreater(process["peak_rss_bytes"], 0)
            self.assertEqual(process["rss_status"], "measured by /usr/bin/time -v")
            self.assertIn("-test.timeout=130s", process["command"])
            self.assertIn("Maximum resident set size (kbytes):", (root / "time-off.txt").read_text())

    def test_invalid_off_rss_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = self.fake_binary(root)
            timer = self.fake_time(root)
            with mock.patch.object(runner, "TIME_BINARY", str(timer)), mock.patch.object(
                runner, "parse_peak_rss_bytes", side_effect=ValueError("bad RSS")
            ):
                with self.assertRaisesRegex(RuntimeError, "stop before ON"):
                    runner.run_arm(root, binary, binary, root, "b" * 64, "s" * 64, "off")
            process = json.loads((root / "process-off.json").read_text())
            self.assertIsNone(process["peak_rss_bytes"])
            self.assertEqual(process["rss_status"], "invalid")
            self.assertTrue((root / "time-off.txt").exists())


if __name__ == "__main__":
    unittest.main()
