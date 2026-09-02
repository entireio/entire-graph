#!/usr/bin/env python3

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "parse-go-test-json.py"
SPEC = importlib.util.spec_from_file_location("parse_go_test_json", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def event(time, action, package, test=None, elapsed=None, output=None):
    value = {"Time": time, "Action": action, "Package": package}
    if test is not None:
        value["Test"] = test
    if elapsed is not None:
        value["Elapsed"] = elapsed
    if output is not None:
        value["Output"] = output
    return json.dumps(value, separators=(",", ":")) + "\n"


class ParserTests(unittest.TestCase):
    def test_packages_and_nested_subtests_are_summarized_deterministically(self):
        lines = [
            event("2026-08-01T12:00:00Z", "start", "example/b"),
            event("2026-08-01T12:00:00.100Z", "start", "example/a"),
            event("2026-08-01T12:00:00.200Z", "run", "example/a", "TestParent"),
            event(
                "2026-08-01T12:00:00.300Z",
                "run",
                "example/a",
                "TestParent/child",
            ),
            event(
                "2026-08-01T12:00:00.500Z",
                "pass",
                "example/a",
                "TestParent/child",
                0.2,
            ),
            event(
                "2026-08-01T12:00:00.800Z",
                "pass",
                "example/a",
                "TestParent",
                0.6,
            ),
            event("2026-08-01T12:00:01Z", "pass", "example/a", elapsed=0.9),
            event("2026-08-01T12:00:01.100Z", "run", "example/b", "TestSkipped"),
            event(
                "2026-08-01T12:00:01.200Z",
                "skip",
                "example/b",
                "TestSkipped",
                0.1,
            ),
            event("2026-08-01T12:00:01.300Z", "skip", "example/b", elapsed=1.3),
        ]

        result = MODULE.parse_lines(lines, "synthetic.json")

        self.assertEqual(result["suite"]["status"], "pass")
        self.assertTrue(result["suite"]["complete"])
        self.assertEqual(result["suite"]["wall_duration_seconds"], 1.3)
        self.assertEqual(
            [package["package"] for package in result["packages"]],
            ["example/a", "example/b"],
        )
        package = result["packages"][0]
        self.assertEqual(package["duration_seconds"], 0.9)
        self.assertEqual(package["duration_source"], "reported_elapsed")
        parent = package["top_level_tests"][0]
        self.assertEqual(parent["name"], "TestParent")
        self.assertEqual(parent["duration_seconds"], 0.6)
        self.assertEqual(parent["subtest_count"], 1)
        self.assertEqual(parent["subtests"][0]["name"], "TestParent/child")
        self.assertEqual(parent["subtests"][0]["parent"], "TestParent")
        self.assertEqual(parent["subtests"][0]["duration_seconds"], 0.2)
        self.assertEqual(result["diagnostics"], [])

        summary = MODULE.render_summary(result, top=1)
        self.assertIn("Status: PASS (complete)", summary)
        self.assertIn("example/a :: TestParent (1 subtests)", summary)
        self.assertLess(summary.index("example/b"), summary.index("example/a"))

    def test_failure_wins_over_incomplete_or_skipped_results(self):
        lines = [
            event("2026-08-01T12:00:00Z", "start", "example/fail"),
            event("2026-08-01T12:00:00.1Z", "run", "example/fail", "TestBoom"),
            event(
                "2026-08-01T12:00:00.2Z",
                "run",
                "example/fail",
                "TestBoom/sub",
            ),
            event(
                "2026-08-01T12:00:00.5Z",
                "fail",
                "example/fail",
                "TestBoom/sub",
                0.3,
            ),
            event(
                "2026-08-01T12:00:00.6Z",
                "fail",
                "example/fail",
                "TestBoom",
                0.5,
            ),
            event("2026-08-01T12:00:00.7Z", "fail", "example/fail", elapsed=0.7),
            event("2026-08-01T12:00:00.8Z", "start", "example/incomplete"),
        ]

        result = MODULE.parse_lines(lines)

        self.assertEqual(result["suite"]["status"], "fail")
        self.assertFalse(result["suite"]["complete"])
        self.assertEqual(result["suite"]["package_status_counts"]["fail"], 1)
        self.assertEqual(result["suite"]["package_status_counts"]["incomplete"], 1)
        failed_test = result["packages"][0]["top_level_tests"][0]
        self.assertEqual(failed_test["status"], "fail")

    def test_malformed_and_truncated_lines_become_diagnostics(self):
        lines = [
            event("2026-08-01T12:00:00Z", "start", "example/truncated"),
            "not json\n",
            event(
                "2026-08-01T12:00:00.1Z",
                "run",
                "example/truncated",
                "TestStillVisible",
            ),
            event(
                "2026-08-01T12:00:00.2Z",
                "run",
                "example/truncated",
                "TestStillVisible/sub",
            ),
            '{"Time":"2026-08-01T12:00:00.3Z","Action":"pass"',
        ]

        result = MODULE.parse_lines(lines)

        self.assertEqual(result["suite"]["status"], "incomplete")
        self.assertFalse(result["suite"]["complete"])
        self.assertEqual(result["source"]["event_count"], 3)
        self.assertEqual(result["source"]["malformed_line_count"], 2)
        self.assertEqual(
            [item["code"] for item in result["diagnostics"]],
            ["invalid_json", "truncated_json"],
        )
        top = result["packages"][0]["top_level_tests"][0]
        self.assertEqual(top["name"], "TestStillVisible")
        self.assertEqual(top["status"], "incomplete")
        self.assertEqual(top["duration_seconds"], 0.1)
        self.assertEqual(top["duration_source"], "event_timestamps")
        self.assertEqual(top["subtests"][0]["status"], "incomplete")

    def test_root_duration_falls_back_to_event_timestamps(self):
        lines = [
            event("2026-08-01T12:00:00Z", "start", "example/timestamps"),
            event(
                "2026-08-01T12:00:00.25Z",
                "run",
                "example/timestamps",
                "TestTiming",
            ),
            event(
                "2026-08-01T12:00:02Z",
                "pass",
                "example/timestamps",
                "TestTiming",
            ),
            event("2026-08-01T12:00:02.5Z", "pass", "example/timestamps"),
        ]

        result = MODULE.parse_lines(lines)
        package = result["packages"][0]
        test = package["top_level_tests"][0]

        self.assertEqual(test["duration_seconds"], 1.75)
        self.assertEqual(test["duration_source"], "event_timestamps")
        self.assertEqual(package["duration_seconds"], 2.5)
        self.assertEqual(package["duration_source"], "event_timestamps")

    def test_cli_writes_repeatable_json_and_separate_summary(self):
        stream = "".join(
            [
                event("2026-08-01T12:00:00Z", "start", "example/cli"),
                event("2026-08-01T12:00:00.1Z", "run", "example/cli", "TestCLI"),
                event(
                    "2026-08-01T12:00:00.2Z",
                    "pass",
                    "example/cli",
                    "TestCLI",
                    0.1,
                ),
                event("2026-08-01T12:00:00.3Z", "pass", "example/cli", elapsed=0.3),
            ]
        )
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_path = root / "events.json"
            first_path = root / "first.json"
            second_path = root / "second.json"
            summary_path = root / "summary.txt"
            input_path.write_text(stream, encoding="utf-8")
            command = [
                sys.executable,
                str(SCRIPT),
                "--input",
                str(input_path),
                "--output",
                str(first_path),
                "--summary-output",
                str(summary_path),
                "--top",
                "5",
            ]
            subprocess.run(command, check=True, capture_output=True, text=True)
            command[command.index(str(first_path))] = str(second_path)
            subprocess.run(command, check=True, capture_output=True, text=True)

            self.assertEqual(first_path.read_bytes(), second_path.read_bytes())
            parsed = json.loads(first_path.read_text(encoding="utf-8"))
            self.assertEqual(parsed["suite"]["status"], "pass")
            self.assertIn("Status: PASS (complete)", summary_path.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
