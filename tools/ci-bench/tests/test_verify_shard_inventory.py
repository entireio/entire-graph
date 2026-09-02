import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


PARTITION_PATH = Path(__file__).resolve().parents[1] / "partition-tests.py"
PARTITION_SPEC = importlib.util.spec_from_file_location("partition_for_verify", PARTITION_PATH)
PARTITION = importlib.util.module_from_spec(PARTITION_SPEC)
assert PARTITION_SPEC.loader is not None
PARTITION_SPEC.loader.exec_module(PARTITION)

VERIFY_PATH = Path(__file__).resolve().parents[1] / "verify-shard-inventory.py"
VERIFY_SPEC = importlib.util.spec_from_file_location("verify_shard_inventory", VERIFY_PATH)
VERIFY = importlib.util.module_from_spec(VERIFY_SPEC)
assert VERIFY_SPEC.loader is not None
VERIFY_SPEC.loader.exec_module(VERIFY)


def write_json(path, value):
    path.write_text(json.dumps(value) + "\n", encoding="utf-8")


def write_jsonl(path, values):
    path.write_text("".join(json.dumps(value) + "\n" for value in values), encoding="utf-8")


def inventory(package, directory, binary, tests):
    return {
        "schema": VERIFY.INVENTORY_SCHEMA,
        "importPath": package,
        "packageDirectoryRelative": directory,
        "binaryName": binary,
        "tests": tests,
        "exitCode": 0,
    }


def events(package, tests):
    result = [{"Action": "start", "Package": package}]
    for test, action in tests:
        result.extend(
            [
                {"Action": "run", "Package": package, "Test": test},
                {"Action": action, "Package": package, "Test": test, "Elapsed": 0.1},
            ]
        )
    result.append({"Action": "pass", "Package": package, "Elapsed": 0.2})
    return result


class VerifyShardInventoryTests(unittest.TestCase):
    def make_fixture(self, root):
        inventories = [
            inventory("example/heavy", "heavy", "heavy.test.exe", ["TestA", "TestB"]),
        ]
        inventory_path = root / "heavy-inventory.json"
        write_json(inventory_path, inventories[0])
        plan = PARTITION.create_plan(
            [
                {
                    "importPath": "example/heavy",
                    "packageDirectoryRelative": "heavy",
                    "binaryName": "heavy.test.exe",
                    "tests": ["TestA", "TestB"],
                    "sourceInventory": str(inventory_path),
                    "listExitCode": 0,
                }
            ],
            {("example/heavy", "TestA"): [2.0], ("example/heavy", "TestB"): [1.0]},
            2,
            1.0,
            [],
        )
        plan_path = root / "plan.json"
        write_json(plan_path, plan)
        package_path = root / "packages.json"
        write_json(
            package_path,
            {
                "schema": VERIFY.PACKAGE_SCHEMA,
                "packages": [
                    {"importPath": "example/heavy", "heavy": True},
                    {"importPath": "example/other", "heavy": False},
                    {"importPath": "example/no-tests", "heavy": False},
                ],
            },
        )
        baseline_path = root / "baseline.jsonl"
        baseline = events("example/heavy", [("TestA", "pass"), ("TestB", "skip")])
        baseline += events("example/other", [("TestOther", "pass")])
        baseline += [
            {"Action": "start", "Package": "example/no-tests"},
            {"Action": "skip", "Package": "example/no-tests", "Elapsed": 0},
        ]
        write_jsonl(baseline_path, baseline)
        candidate_paths = []
        for index, test in enumerate([("TestA", "pass"), ("TestB", "skip")]):
            path = root / f"shard-{index}.jsonl"
            write_jsonl(path, events("example/heavy", [test]))
            candidate_paths.append(path)
        other_path = root / "other.jsonl"
        write_jsonl(other_path, events("example/other", [("TestOther", "pass")]))
        candidate_paths.append(other_path)
        no_tests_path = root / "no-tests.jsonl"
        write_jsonl(
            no_tests_path,
            [
                {"Action": "start", "Package": "example/no-tests"},
                {"Action": "skip", "Package": "example/no-tests", "Elapsed": 0},
            ],
        )
        candidate_paths.append(no_tests_path)
        metadata_paths = []
        for index in range(4):
            path = root / f"metadata-{index}.json"
            write_json(path, {"exitCode": 0, "nested": [{"phaseExitCode": 0}]})
            metadata_paths.append(path)
        return inventory_path, plan_path, package_path, baseline_path, candidate_paths, metadata_paths

    def test_full_multiset_inventory_package_and_exit_checks_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inventory_path, plan_path, package_path, baseline, candidates, metadata = self.make_fixture(root)
            output = root / "report.json"
            arguments = [
                "--baseline-events", str(baseline),
                "--plan", str(plan_path),
                "--inventory", str(inventory_path),
                "--package-inventory", str(package_path),
                "--output", str(output),
            ]
            for path in candidates:
                arguments.extend(["--candidate-events", str(path)])
            for path in metadata:
                arguments.extend(["--metadata", str(path)])

            self.assertEqual(VERIFY.main(arguments), 0)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(report["equivalent"])
            self.assertEqual(report["dynamicEvents"]["baselineCount"], 6)

    def test_package_level_skip_is_a_successful_matched_outcome(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inventory_path, plan_path, package_path, baseline, candidates, metadata = self.make_fixture(root)
            for path in [baseline, *candidates]:
                payloads = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
                for payload in payloads:
                    if "Test" not in payload and payload.get("Action") == "pass":
                        payload["Action"] = "skip"
                write_jsonl(path, payloads)
            output = root / "report.json"
            arguments = [
                "--baseline-events", str(baseline),
                "--plan", str(plan_path),
                "--inventory", str(inventory_path),
                "--package-inventory", str(package_path),
                "--output", str(output),
            ]
            for path in candidates:
                arguments.extend(["--candidate-events", str(path)])
            for path in metadata:
                arguments.extend(["--metadata", str(path)])

            self.assertEqual(VERIFY.main(arguments), 0)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(report["equivalent"])
            self.assertFalse(report["packages"]["baselineFailedResults"])

    def test_duplicate_no_test_package_terminal_fails_multiplicity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inventory_path, plan_path, package_path, baseline, candidates, metadata = self.make_fixture(root)
            with candidates[-1].open("a", encoding="utf-8") as handle:
                handle.write(json.dumps({"Action": "skip", "Package": "example/no-tests", "Elapsed": 0}) + "\n")
            output = root / "report.json"
            arguments = [
                "--baseline-events", str(baseline),
                "--plan", str(plan_path),
                "--inventory", str(inventory_path),
                "--package-inventory", str(package_path),
                "--output", str(output),
            ]
            for path in candidates:
                arguments.extend(["--candidate-events", str(path)])
            for path in metadata:
                arguments.extend(["--metadata", str(path)])

            self.assertEqual(VERIFY.main(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(report["equivalent"])
            self.assertTrue(report["packages"]["candidateMultiplicityMismatches"])
            self.assertTrue(any("multiplicity" in failure for failure in report["failures"]))

    def test_duplicate_dynamic_test_and_nonzero_exit_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inventory_path, plan_path, package_path, baseline, candidates, metadata = self.make_fixture(root)
            with candidates[0].open("a", encoding="utf-8") as handle:
                handle.write(json.dumps({"Action": "run", "Package": "example/heavy", "Test": "TestA"}) + "\n")
            write_json(metadata[0], {"exitCode": 7})
            output = root / "report.json"
            arguments = [
                "--baseline-events", str(baseline),
                "--plan", str(plan_path),
                "--inventory", str(inventory_path),
                "--package-inventory", str(package_path),
                "--output", str(output),
            ]
            for path in candidates:
                arguments.extend(["--candidate-events", str(path)])
            for path in metadata:
                arguments.extend(["--metadata", str(path)])

            self.assertEqual(VERIFY.main(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(report["equivalent"])
            self.assertTrue(report["dynamicEvents"]["extra"])
            self.assertTrue(any("exit code" in failure for failure in report["failures"]))


if __name__ == "__main__":
    unittest.main()
