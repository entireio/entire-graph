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
