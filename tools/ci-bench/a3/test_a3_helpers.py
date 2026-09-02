from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent


def load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


partition_tests = load_module("a3_partition_tests", ROOT / "partition-tests.py")
compare_evidence = load_module("a3_compare_evidence", ROOT / "compare-evidence.py")


class WeightedPartitionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.baseline = self.root / "baseline.jsonl"
        self.inventory = self.root / "inventory.json"
        self.full_inventory = self.root / "full-inventory.json"
        self.heavy = "example.test/heavy"
        self.nonheavy = "example.test/nonheavy"
        events = [
            {"Action": "run", "Package": self.heavy, "Test": "TestSlow"},
            {"Action": "pass", "Package": self.heavy, "Test": "TestSlow", "Elapsed": 4.0},
            {"Action": "run", "Package": self.heavy, "Test": "TestFast"},
            {"Action": "run", "Package": self.heavy, "Test": "TestFast/child"},
            {"Action": "pass", "Package": self.heavy, "Test": "TestFast/child", "Elapsed": 0.5},
            {"Action": "pass", "Package": self.heavy, "Test": "TestFast", "Elapsed": 1.0},
            {"Action": "run", "Package": self.heavy, "Test": "Example"},
            {"Action": "pass", "Package": self.heavy, "Test": "Example", "Elapsed": 0.25},
            {"Action": "pass", "Package": self.heavy, "Elapsed": 5.25},
            {"Action": "run", "Package": self.nonheavy, "Test": "TestOnly"},
            {"Action": "pass", "Package": self.nonheavy, "Test": "TestOnly", "Elapsed": 2.0},
            {"Action": "pass", "Package": self.nonheavy, "Elapsed": 2.5},
        ]
        self.baseline.write_text(
            "".join(json.dumps(event) + "\n" for event in events), encoding="utf-8"
        )
        self.inventory.write_text(
            json.dumps({self.heavy: ["TestFast", "TestSlow"], self.nonheavy: ["TestOnly"]}),
            encoding="utf-8",
        )
        self.full_inventory.write_text(
            json.dumps(
                {
                    self.heavy: ["BenchmarkIgnored", "Example", "TestFast", "TestSlow"],
                    self.nonheavy: ["TestOnly"],
                }
            ),
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def args(self, output: pathlib.Path) -> argparse.Namespace:
        return argparse.Namespace(
            baseline=self.baseline,
            inventory=self.inventory,
            full_inventory=self.full_inventory,
            output=output,
            shards=4,
            heavy_package=[self.heavy],
        )

    def test_heavy_roots_exactly_once_and_nonheavy_package_once(self) -> None:
        output = self.root / "plan.json"
        self.assertEqual(partition_tests.build_plan(self.args(output)), 0)
        plan = json.loads(output.read_text(encoding="utf-8"))
        heavy_counts: dict[str, int] = {}
        nonheavy_count = 0
        for shard in plan["bins"]:
            self.assertTrue(shard["packages"])
            for package in shard["packages"]:
                if package["package"] == self.heavy:
                    for test in package["tests"]:
                        heavy_counts[test] = heavy_counts.get(test, 0) + 1
                if package["package"] == self.nonheavy:
                    nonheavy_count += 1
                    self.assertEqual(package["mode"], "full")
        self.assertEqual(heavy_counts, {"Example": 1, "TestFast": 1, "TestSlow": 1})
        self.assertEqual(nonheavy_count, 1)
        audit = plan["compiledInventoryAudit"][self.heavy]
        self.assertEqual(audit["benchmarksExcludedFromDefaultExecution"], ["BenchmarkIgnored"])
        self.assertEqual(audit["supplementalDefaultRunnableRoots"], ["Example"])

    def test_baseline_inventory_mismatch_is_rejected(self) -> None:
        value = json.loads(self.inventory.read_text(encoding="utf-8"))
        value[self.heavy].append("TestNotInBaseline")
        self.inventory.write_text(json.dumps(value), encoding="utf-8")
        full = json.loads(self.full_inventory.read_text(encoding="utf-8"))
        full[self.heavy].append("TestNotInBaseline")
        self.full_inventory.write_text(json.dumps(full), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "compiled inventory/native baseline mismatch"):
            partition_tests.build_plan(self.args(self.root / "bad.json"))

    def test_output_is_deterministic(self) -> None:
        first = self.root / "first.json"
        second = self.root / "second.json"
        partition_tests.build_plan(self.args(first))
        partition_tests.build_plan(self.args(second))
        self.assertEqual(first.read_bytes(), second.read_bytes())


class DynamicMultisetTests(unittest.TestCase):
    def test_duplicate_event_is_not_hidden(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            event = {"Action": "pass", "Package": "example.test/p", "Test": "TestOne"}
            baseline = root / "baseline.jsonl"
            candidate = root / "candidate.jsonl"
            output = root / "comparison.json"
            baseline.write_text(json.dumps(event) + "\n", encoding="utf-8")
            candidate.write_text(
                json.dumps(event) + "\n" + json.dumps(event) + "\n", encoding="utf-8"
            )
            args = argparse.Namespace(baseline=[baseline], candidate=[candidate], output=output)
            self.assertEqual(compare_evidence.compare_dynamic(args), 1)
            result = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(result["equivalent"])
            self.assertEqual(result["unexpected"][0]["count"], 1)


if __name__ == "__main__":
    unittest.main()
