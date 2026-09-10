#!/usr/bin/env python3

import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import special_log_observations as special


class SpecialLogObservationTest(unittest.TestCase):
    def parse(self, name: str, text: str) -> dict:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / name
            path.write_text(text)
            return special.extract(path)

    def test_extracts_benchmark_and_pprof_tables(self) -> None:
        benchmark = self.parse("bench.txt", "goos: linux\ngoarch: amd64\npkg: example\ncpu: x\nBenchmarkThing-8 1 12 ns/op 40 B/op\nPASS\nok example 0.1s\n")
        self.assertTrue(benchmark["complete_for_removal"])
        self.assertEqual(benchmark["facts"]["benchmark"]["rows"][0]["measurements"][1], {"value": 40.0, "unit": "B/op"})
        profile = self.parse("cpu.txt", "File: test\nType: cpu\nTime: now\nDuration: 2s, Total samples = 1s (50%)\nShowing top 1 nodes out of 1\n flat flat% sum% cum cum%\n 1s 100% 100% 1s 100% fn\n")
        self.assertTrue(profile["complete_for_removal"])
        self.assertEqual(profile["facts"]["pprof_text"]["rows"][0]["function"], "fn")

    def test_extracts_build_info_and_process_memory_without_claims(self) -> None:
        build = self.parse("build.txt", "tool: go1.26.1\n\tpath\texample/tool\n\tdep\texample/dep\tv1.2.3\th1:sum\n\tbuild\tGOOS=linux\n")
        self.assertTrue(build["complete_for_removal"])
        self.assertEqual(build["facts"]["go_build_info"]["modules"][0]["version"], "v1.2.3")
        memory = self.parse("process-memory-observation.txt", "Observed:\n- process ended with signal: killed\n- OOM is an inference, not proven\n")
        self.assertTrue(memory["complete_for_removal"])
        self.assertIn("not proven", memory["facts"]["process_memory_observations"][1])
        self.assertNotIn("status", memory)

    def test_extracts_jsonl_and_unittest_summary_with_sanitization(self) -> None:
        record = self.parse("test.log", '{"status":"failed","vm":"worker-a","url":"https://example.test/?sig=secret"}\n....\n----\nRan 4 tests in 1.25s\nOK\n')
        self.assertTrue(record["complete_for_removal"])
        output = record["facts"]["test_or_status_output"]
        self.assertEqual(output["suite"]["tests"], 4)
        self.assertEqual(output["events"][0]["status"], "failed")
        serialized = json.dumps(record)
        self.assertNotIn("worker-a", serialized)
        self.assertNotIn("sig=secret", serialized)

    def test_extracts_named_go_and_python_outcomes_with_skip_details(self) -> None:
        record = self.parse("tests.txt", "=== RUN   TestLive\n    live_test.go:17: requires compiler\n--- SKIP: TestLive (0.00s)\ntest_ok (suite.Case) ... ok\nRan 2 tests in 0.01s\nOK\n")
        self.assertTrue(record["complete_for_removal"])
        outcomes = record["facts"]["test_or_status_output"]["named_outcomes"]
        self.assertEqual(outcomes[0], {"outcome": "skip", "name": "TestLive", "details": ["requires compiler"]})
        self.assertEqual(outcomes[1]["observed_marker"], "ok")

    def test_unparsed_content_blocks_lossless_classification(self) -> None:
        record = self.parse("mixed.log", "Ran 1 test in 0.1s\nunknown unique detail\nOK\n")
        self.assertFalse(record["complete_for_removal"])
        self.assertEqual(record["unparsed_nonempty_line_count"], 1)

    def test_retention_map_distinguishes_structured_semantic_and_empty_removals(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log_index = root / "logs.json"
            empty_sha = __import__("hashlib").sha256(b"").hexdigest()
            complete_sha = __import__("hashlib").sha256(b"Ran 1 test in 0.1s\nOK\n").hexdigest()
            partial_sha = __import__("hashlib").sha256(b"Ran 1 test in 0.1s\nunique\nOK\n").hexdigest()
            log_index.write_text(json.dumps({
                "precleanup_head": "a" * 40,
                "content_records": [
                    {"sha256": empty_sha, "named_tests": [], "scalar_observations": [], "scalar_observation_count": 0, "scalar_observations_truncated": False, "source_hashes": [], "source_commits": []},
                    {"sha256": complete_sha, "named_tests": [], "scalar_observations": [], "scalar_observation_count": 0, "scalar_observations_truncated": False, "source_hashes": [], "source_commits": []},
                    {"sha256": partial_sha, "named_tests": [], "scalar_observations": [], "scalar_observation_count": 0, "scalar_observations_truncated": False, "source_hashes": [], "source_commits": []},
                ],
                "files": [
                    {"path": "empty.txt", "content_id": empty_sha, "bytes": 0, "git_blob": "1" * 40},
                    {"path": "complete.txt", "content_id": complete_sha, "bytes": 24, "git_blob": "2" * 40},
                    {"path": "partial.txt", "content_id": partial_sha, "bytes": 31, "git_blob": "3" * 40},
                ],
            }))
            special_record = {
                "source_log_index_sha256": "4" * 64,
                "content_records": [
                    {"source_sha256": complete_sha, "unparsed_nonempty_line_count": 0},
                    {"source_sha256": partial_sha, "unparsed_nonempty_line_count": 1},
                ],
                "aliases": [
                    {"path": "complete.txt", "content_id": complete_sha, "complete_for_removal": True},
                    {"path": "partial.txt", "content_id": partial_sha, "complete_for_removal": False},
                ],
            }
            result = special.build_retention_map(log_index, special_record, "special.json")
            self.assertEqual(result["disposition_counts"], {"remove_empty": 1, "remove_after_structured_extraction": 1, "retain_unclassified_nonempty": 1})


if __name__ == "__main__":
    unittest.main()
