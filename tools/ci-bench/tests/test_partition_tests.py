import importlib.util
import json
import re
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parents[1] / "partition-tests.py"
SPEC = importlib.util.spec_from_file_location("partition_tests", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


def inventory(package, directory, binary, tests):
    return {
        "importPath": package,
        "packageDirectoryRelative": directory,
        "binaryName": binary,
        "tests": tests,
        "sourceInventory": "fixture.json",
        "listExitCode": 0,
    }


class PartitionTests(unittest.TestCase):
    def test_compressed_regex_matches_exactly_the_requested_tests(self):
        tests = [
            "TestParser",
            "TestParserDepth",
            "TestParserDepthLimit",
            "TestProvider",
            "TestProviderCache",
        ]
        expression = MODULE.compressed_run_regex(tests)
        compiled = re.compile(expression)

        self.assertEqual({name for name in tests if compiled.fullmatch(name)}, set(tests))
        for nonmember in ["Test", "TestParse", "TestParserDepthLimitExtra", "BenchmarkParser"]:
            self.assertIsNone(compiled.fullmatch(nonmember))
        naive = "^(" + "|".join(tests) + ")$"
        self.assertLess(len(expression), len(naive))

    def test_lpt_partition_is_deterministic_balanced_and_complete(self):
        inventories = [
            inventory("example/sem", "internal/sem", "sem.test.exe", ["TestA", "TestB"]),
            inventory("example/cli", "internal/cli", "cli.test.exe", ["TestC", "TestD"]),
        ]
        timings = {
            ("example/sem", "TestA"): [8.0],
            ("example/sem", "TestB"): [7.0],
            ("example/cli", "TestC"): [6.0],
            ("example/cli", "TestD"): [5.0],
        }

        first = MODULE.create_plan(inventories, timings, 2, 1.0, [])
        second = MODULE.create_plan(inventories, timings, 2, 1.0, [])

        self.assertEqual(first, second)
        self.assertEqual([shard["estimatedWeightSeconds"] for shard in first["shards"]], [13.0, 13.0])
        assigned = {
            (assignment["importPath"], test["name"])
            for shard in first["shards"]
            for assignment in shard["assignments"]
            for test in assignment["tests"]
        }
        self.assertEqual(assigned, set(timings))

    def test_timing_jsonl_uses_median_top_level_terminal_elapsed_only(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "timings.jsonl"
            events = [
                {"Action": "pass", "Package": "example/p", "Test": "TestA", "Elapsed": 9.0},
                {"Action": "pass", "Package": "example/p", "Test": "TestA/sub", "Elapsed": 99.0},
                {"Action": "pass", "Package": "example/p", "Test": "TestA", "Elapsed": 1.0},
                {"Action": "run", "Package": "example/p", "Test": "TestA"},
            ]
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            timings = MODULE.load_timings([path])
            plan = MODULE.create_plan(
                [inventory("example/p", "p", "p.test.exe", ["TestA", "TestMissing"])],
                timings,
                1,
                2.5,
                [path],
            )

        tests = plan["shards"][0]["assignments"][0]["tests"]
        by_name = {test["name"]: test for test in tests}
        self.assertEqual(by_name["TestA"]["weightSeconds"], 5.0)
        self.assertEqual(by_name["TestA"]["weightSource"], "median-observed-elapsed")
        self.assertEqual(by_name["TestMissing"]["weightSeconds"], 2.5)
        self.assertEqual(plan["diagnostics"]["defaultedTestCount"], 1)

    def test_zero_weight_tests_balance_by_count_without_changing_weight_totals(self):
        names = [f"TestZero{index}" for index in range(17)]
        inventories = [inventory("example/p", "p", "p.test.exe", names)]
        timings = {("example/p", name): [0.0] for name in names}

        plan = MODULE.create_plan(inventories, timings, 4, 1.0, [])

        counts = [shard["testCount"] for shard in plan["shards"]]
        self.assertLessEqual(max(counts) - min(counts), 1)
        self.assertEqual([shard["estimatedWeightSeconds"] for shard in plan["shards"]], [0.0] * 4)


if __name__ == "__main__":
    unittest.main()
