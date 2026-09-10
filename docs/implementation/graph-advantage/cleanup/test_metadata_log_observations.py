#!/usr/bin/env python3

import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import metadata_log_observations as metadata


class MetadataLogObservationTest(unittest.TestCase):
    def facts(self, path: str, text: str) -> dict:
        raw = text.encode()
        general = {"sha256": __import__("hashlib").sha256(raw).hexdigest()}
        return metadata.parse(path, raw, general)

    def test_environment_identity_and_time_are_structured(self) -> None:
        env = self.facts("environment.txt", "source_commit=" + "a" * 40 + "\ngo version go1.26.1 linux/amd64\nLinux graph-validation-linux 6.8\n/opt/private/source.tar.gz: OK\n")
        self.assertTrue(env["complete_for_semantic_removal"])
        self.assertNotIn("/opt/", str(env))
        self.assertNotIn("graph-validation-linux", str(env))
        timing = self.facts("time-full-off.txt", '\tCommand being timed: "/opt/bin/tool -test.timeout=130s"\n\tMaximum resident set size (kbytes): 308124\n\tExit status: 0\n')
        self.assertTrue(timing["complete_for_semantic_removal"])
        self.assertEqual(len(timing["facts"]["gnu_time"]), 3)

    def test_mixed_check_preserves_commands_dependencies_and_general_reference(self) -> None:
        record = self.facts("mise-check.raw.log", "[fmt] $ gofmt -s -w .\ngo: downloading example/mod v1.2.3\ngithub.com/example/pkg\nFinished in 1.25s\n--- SKIP: TestLive (0.00s)\n")
        self.assertTrue(record["complete_for_semantic_removal"])
        self.assertEqual(record["facts"]["task_commands"][0]["task"], "fmt")
        self.assertEqual(record["facts"]["module_downloads"][0]["version"], "v1.2.3")

    def test_unknown_mixed_line_blocks_removal(self) -> None:
        record = self.facts("mise-check.raw.log", "[fmt] $ gofmt -s -w .\nunique unexplained output\n")
        self.assertFalse(record["complete_for_semantic_removal"])
        self.assertEqual(record["unclassified_line_count"], 1)


if __name__ == "__main__":
    unittest.main()
