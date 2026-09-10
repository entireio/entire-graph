#!/usr/bin/env python3

import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import residual_log_observations as residual


class ResidualLogObservationTest(unittest.TestCase):
    def test_goroutine_summary_preserves_states_top_frames_and_function_counts(self) -> None:
        result = residual.goroutines(b"panic: test timed out\n\ngoroutine 1 [chan receive]:\nexample.wait()\n\t/local/a.go:1\n\ngoroutine 2 [running]:\nexample.work()\n\t/local/b.go:2\n")
        self.assertEqual(result["goroutine_state_counts"], {"chan receive": 1, "running": 1})
        self.assertEqual([item["function"] for item in result["top_frames"]], ["example.wait", "example.work"])
        self.assertEqual(len(result["function_occurrences"]), 2)

    def test_process_tree_discards_ids_and_paths_but_keeps_observed_shape(self) -> None:
        result = residual.process_tree(b"=== elapsed=0.001s pgid=1 ===\n1 2 1 00:00 mise run check\n=== elapsed=5.0s pgid=1 ===\n1 2 1 Sl 01:02:03 /tmp/go test\n")
        self.assertEqual(result["snapshot_count"], 2)
        self.assertEqual(result["max_processes_observed"], 1)
        self.assertEqual(result["executable_observation_counts"], {"go": 1, "mise": 1})
        self.assertEqual(result["unparsed_numeric_rows"], 0)

    def test_process_tree_reports_unknown_instead_of_inventing_zero(self) -> None:
        result = residual.process_tree(b"=== elapsed=0.0s pgid=1 ===\nnot a process row\n")
        self.assertIsNone(result["max_processes_observed"])
        self.assertEqual(result["max_processes_unavailable_reason"], "no recognized process rows")

    def test_apple_sample_preserves_memory_threads_and_symbol_weights(self) -> None:
        result = residual.apple_sample(b"Identifier: sem.test\nCode Type: ARM64\nPlatform: macOS\nPhysical footprint: 277.3M\nPhysical footprint (peak): 423.4M\n  741 Thread_1\n  + 124 openat  (in kernel)\n  + 10 ???  [0x1]\n")
        self.assertEqual(result["memory_footprint"]["peak"], {"value": 423.4, "unit": "MiB"})
        self.assertEqual(result["thread_header_count"], 1)
        self.assertEqual(result["known_symbol_sample_weights"], [{"symbol": "openat", "weight": 124}])


if __name__ == "__main__":
    unittest.main()
