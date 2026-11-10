import json
import os
import re
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import plan_shards  # noqa: E402
import prepare  # noqa: E402
import run_shard  # noqa: E402


def normalized_inventory(package, binary, roots):
    return {
        "importPath": package,
        "packageArgument": "./heavy",
        "packageDirectoryRelative": "internal/heavy",
        "binaryName": binary,
        "binarySha256": "a" * 64,
        "binarySizeBytes": 123,
        "repositorySha": "b" * 40,
        "goVersion": "go version go1.26.7 windows/amd64",
        "targetEnvironment": dict(plan_shards.TARGET_ENVIRONMENT),
        "listingContract": {
            "arguments": ["-test.paniconexit0", "-test.list=."],
            "paniconexit0": True,
            "pwdMatchesPackageDirectory": True,
            "goToolDirectoryPrependedToPath": True,
        },
        "testMainDeclarations": [],
        "roots": roots,
        "excludedBenchmarks": ["BenchmarkNotRun"],
    }


def settings(package="example/heavy", binary="heavy.test.exe", shard_count=2):
    return {
        "schema": plan_shards.SETTINGS_SCHEMA,
        "shardCount": shard_count,
        "defaultWeightSeconds": 1.0,
        "timeout": "30m",
        "commandLineLimit": 30000,
        "shuffle": "off",
        "testParallel": None,
        "goMaxProcs": None,
        "heavyPackages": [
            {
                "argument": "./heavy",
                "importPath": package,
                "binaryName": binary,
                "expectedTestMainDeclarations": [],
            }
        ],
    }


class PlanShardTests(unittest.TestCase):
    def test_listing_includes_default_roots_and_excludes_benchmarks(self):
        roots, benchmarks = prepare.classify_listing(
            ["TestAlpha", "ExampleThing", "FuzzBytes", "BenchmarkParser"], "fixture"
        )
        self.assertEqual(
            roots,
            [
                {"name": "ExampleThing", "kind": "Example"},
                {"name": "FuzzBytes", "kind": "Fuzz"},
                {"name": "TestAlpha", "kind": "Test"},
            ],
        )
        self.assertEqual(benchmarks, ["BenchmarkParser"])

    def test_listing_rejects_testmain_noise_and_duplicates(self):
        with self.assertRaisesRegex(ValueError, "unexpected"):
            prepare.classify_listing(["TestMain", "TestAlpha"], "fixture")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            prepare.classify_listing(["TestAlpha", "TestAlpha"], "fixture")

    def test_plan_is_deterministic_complete_and_defaults_new_roots(self):
        inventory = normalized_inventory(
            "example/heavy",
            "heavy.test.exe",
            [
                {"name": "TestSlow", "kind": "Test"},
                {"name": "ExampleCurrent", "kind": "Example"},
                {"name": "FuzzCurrent", "kind": "Fuzz"},
                {"name": "TestNew", "kind": "Test"},
            ],
        )
        weights = {("example/heavy", "TestSlow"): 10.0}
        metadata = {"path": "weights.json", "sha256": "c" * 64, "entryCount": 1}
        first = plan_shards.create_plan([inventory], settings(), weights, metadata)
        second = plan_shards.create_plan([inventory], settings(), weights, metadata)
        self.assertEqual(first, second)
        assigned = {
            (assignment["importPath"], root["name"]): root
            for shard in first["shards"]
            for assignment in shard["assignments"]
            for root in assignment["roots"]
        }
        self.assertEqual(len(assigned), 4)
        self.assertEqual(assigned[("example/heavy", "TestSlow")]["weightSource"], "historical-windows")
        self.assertEqual(assigned[("example/heavy", "TestNew")]["weightSource"], "default-current-inventory")
        self.assertEqual(first["historicalWeights"]["defaultedCount"], 3)

    def plan_two_shards(self, roots, weights):
        inventory = normalized_inventory(
            "example/heavy", "heavy.test.exe", [{"name": name, "kind": "Test"} for name in roots]
        )
        metadata = {"path": "weights.json", "sha256": "c" * 64, "entryCount": len(weights)}
        plan = plan_shards.create_plan([inventory], settings(), weights, metadata)
        return plan, [
            sorted(root["name"] for assignment in shard["assignments"] for root in assignment["roots"])
            for shard in plan["shards"]
        ]

    def test_plain_weights_plan_serially_by_longest_processing_time_first(self):
        roots = ["TestA", "TestB", "TestC", "TestD"]
        weights = {
            ("example/heavy", "TestA"): 9.0,
            ("example/heavy", "TestB"): 6.0,
            ("example/heavy", "TestC"): 5.0,
            ("example/heavy", "TestD"): 4.0,
        }
        plan, shards = self.plan_two_shards(roots, weights)
        self.assertEqual(shards, [["TestA", "TestD"], ["TestB", "TestC"]])
        self.assertEqual([shard["estimatedWeightSeconds"] for shard in plan["shards"]], [13.0, 11.0])

    def test_parallel_roots_are_planned_by_process_wall_time(self):
        self.assertEqual(plan_shards.process_seconds(10.0, 90.0, 20.0, 3.0), 40.0)
        self.assertEqual(plan_shards.process_seconds(10.0, 90.0, 80.0, 3.0), 90.0)
        self.assertEqual(plan_shards.process_seconds(10.0, 0.0, 0.0, 3.0), 10.0)
        # One 60 s parallel root, three 20 s parallel roots and 60 s of serial work.
        # Summed elapsed would pair the long parallel root with nothing; by wall time
        # the short parallel roots run beside it for free and the serial root is alone.
        roots = ["TestLongParallel", "TestP1", "TestP2", "TestP3", "TestSerial"]
        weights = plan_shards.Weights(
            {
                ("example/heavy", "TestLongParallel"): 60.0,
                ("example/heavy", "TestP1"): 20.0,
                ("example/heavy", "TestP2"): 20.0,
                ("example/heavy", "TestP3"): 20.0,
                ("example/heavy", "TestSerial"): 60.0,
            }
        )
        weights.parallel = {key for key in weights if key[1] != "TestSerial"}
        weights.parallelism = 2.0
        plan, shards = self.plan_two_shards(roots, weights)
        self.assertEqual(shards, [["TestLongParallel", "TestP1", "TestP2", "TestP3"], ["TestSerial"]])
        self.assertEqual([shard["estimatedWeightSeconds"] for shard in plan["shards"]], [60.0, 60.0])
        self.assertEqual(plan, self.plan_two_shards(roots, weights)[0])
        flags = {
            root["name"]: root.get("parallel", False)
            for shard in plan["shards"]
            for assignment in shard["assignments"]
            for root in assignment["roots"]
        }
        self.assertEqual(flags["TestLongParallel"], True)
        self.assertEqual(flags["TestSerial"], False)

    def test_weight_file_parallel_fields_are_validated(self):
        base = {"schema": plan_shards.WEIGHTS_SCHEMA, "sourceCommit": "a" * 40, "source": "fixture"}
        row = {"package": "p", "name": "TestA", "seconds": 1.0}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "weights.json"
            path.write_text(json.dumps({**base, "weights": [row]}), encoding="utf-8")
            weights, metadata = plan_shards.load_weights(path)
            self.assertEqual((weights.parallel, weights.parallelism), (set(), 1.0))
            self.assertEqual(metadata["parallelism"], 1.0)
            path.write_text(
                json.dumps({**base, "parallelism": 2.5, "weights": [{**row, "parallel": True}]}),
                encoding="utf-8",
            )
            weights, _ = plan_shards.load_weights(path)
            self.assertEqual((weights.parallel, weights.parallelism), ({("p", "TestA")}, 2.5))
            for payload in (
                {**base, "parallelism": 0.5, "weights": [row]},
                {**base, "parallelism": "3", "weights": [row]},
                {**base, "parallelism": True, "weights": [row]},
                {**base, "weights": [{**row, "parallel": "yes"}]},
            ):
                path.write_text(json.dumps(payload), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "parallel"):
                    plan_shards.load_weights(path)

    def test_compressed_regex_selects_exact_roots(self):
        names = ["TestParser", "TestParserDepth", "ExampleParser", "FuzzParser"]
        expression = plan_shards.compressed_run_regex(names)
        compiled = re.compile(expression)
        self.assertEqual({name for name in names if compiled.fullmatch(name)}, set(names))
        for name in ("BenchmarkParser", "Test", "TestParserExtra", "Fuzz"):
            self.assertIsNone(compiled.fullmatch(name))

    def test_settings_fail_closed_on_semantic_overrides(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "settings.json"
            value = settings()
            value["shuffle"] = "on"
            path.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "shuffle"):
                plan_shards.load_settings(path)

    def test_utf16_command_budget_counts_non_bmp_units_and_nul(self):
        units = run_shard.command_line_utf16_units("C:\\go.exe", ["arg", "😀"])
        serialized = 'C:\\go.exe arg 😀'
        self.assertEqual(units, len(serialized.encode("utf-16-le")) // 2 + 1)

    def test_direct_binary_arguments_include_paniconexit0_and_cmd_go_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            package = root / "package"
            tool = root / "tool"
            package.mkdir()
            tool.mkdir()
            binary = root / "heavy.test.exe"
            listing = prepare.direct_listing_arguments(binary)
            shard = run_shard.shard_command_arguments(
                "example/heavy", binary, "30m", "^TestA$", None
            )
            self.assertIn("-test.paniconexit0", listing)
            self.assertIn("-test.paniconexit0", shard)
            self.assertNotIn("-test.testlogfile", listing)
            self.assertNotIn("-test.testlogfile", shard)
            for build_environment in (
                prepare.direct_test_environment,
                run_shard.direct_test_environment,
            ):
                environment = build_environment({"PATH": "original"}, package, tool)
                self.assertEqual(environment["PWD"], str(package))
                self.assertEqual(environment["PATH"].split(os.pathsep)[0], str(tool))


if __name__ == "__main__":
    unittest.main()
