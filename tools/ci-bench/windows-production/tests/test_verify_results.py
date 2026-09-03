import json
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import plan_shards  # noqa: E402
import verify_results  # noqa: E402


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def write_jsonl(path, values):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(value) + "\n" for value in values), encoding="utf-8")


class VerifyResultsTests(unittest.TestCase):
    def run_verifier(self, arguments):
        with redirect_stdout(StringIO()), redirect_stderr(StringIO()):
            return verify_results.main(arguments)

    def make_fixture(self, root):
        bundle = root / "bundle"
        shards_root = root / "shards"
        other_root = root / "other"
        output = root / "verification.json"
        binary = bundle / "binaries" / "heavy.test.exe"
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b"compiled-windows-test-binary")
        binary_hash = plan_shards.sha256_file(binary)
        repository_sha = "b" * 40
        go_version = "go version go1.26.7 windows/amd64"
        settings = {
            "schema": plan_shards.SETTINGS_SCHEMA,
            "shardCount": 2,
            "defaultWeightSeconds": 1.0,
            "timeout": "30m",
            "commandLineLimit": 30000,
            "shuffle": "off",
            "testParallel": None,
            "goMaxProcs": None,
            "heavyPackages": [
                {
                    "argument": "./heavy",
                    "importPath": "example/heavy",
                    "binaryName": "heavy.test.exe",
                    "expectedTestMainDeclarations": [],
                }
            ],
        }
        write_json(bundle / "settings.json", settings)
        weights_payload = {
            "schema": plan_shards.WEIGHTS_SCHEMA,
            "sourceCommit": "a" * 40,
            "source": "fixture",
            "weights": [
                {"package": "example/heavy", "name": "TestA", "seconds": 2.0}
            ],
        }
        write_json(bundle / "historical-weights.json", weights_payload)
        inventory_payload = {
            "schema": plan_shards.INVENTORY_SCHEMA,
            "repositorySha": repository_sha,
            "goVersion": go_version,
            "goos": "windows",
            "goarch": "amd64",
            "cgoEnabled": "1",
            "listingContract": {
                "arguments": ["-test.paniconexit0", "-test.list=."],
                "paniconexit0": True,
                "pwdMatchesPackageDirectory": True,
                "goToolDirectoryPrependedToPath": True,
            },
            "testMainDeclarations": [],
            "packageArgument": "./heavy",
            "importPath": "example/heavy",
            "packageDirectoryRelative": "heavy",
            "binaryName": "heavy.test.exe",
            "binarySha256": binary_hash,
            "binarySizeBytes": binary.stat().st_size,
            "listExitCode": 0,
            "roots": [
                {"name": "TestA", "kind": "Test"},
                {"name": "ExampleB", "kind": "Example"},
                {"name": "FuzzC", "kind": "Fuzz"},
            ],
            "excludedBenchmarks": ["BenchmarkNo"],
        }
        inventory_path = bundle / "inventories" / "heavy.test.exe.inventory.json"
        write_json(inventory_path, inventory_payload)
        inventories = plan_shards.load_inventories([inventory_path])
        loaded_settings = plan_shards.load_settings(bundle / "settings.json")
        weights, weight_metadata = plan_shards.load_weights(bundle / "historical-weights.json")
        plan = plan_shards.create_plan(inventories, loaded_settings, weights, weight_metadata)
        write_json(bundle / "plan.json", plan)
        package_inventory = {
            "schema": verify_results.PACKAGE_SCHEMA,
            "repositorySha": repository_sha,
            "goVersion": go_version,
            "goos": "windows",
            "goarch": "amd64",
            "cgoEnabled": "1",
            "packages": [
                {"importPath": "example/heavy", "packageDirectoryRelative": "heavy", "heavy": True},
                {"importPath": "example/other", "packageDirectoryRelative": "other", "heavy": False},
                {"importPath": "example/no-tests", "packageDirectoryRelative": "none", "heavy": False},
            ],
        }
        write_json(bundle / "package-inventory.json", package_inventory)
        write_json(
            bundle / "prepare-metadata.json",
            {
                "schema": verify_results.PREPARE_SCHEMA,
                "repositorySha": repository_sha,
                "repositoryShaAfter": repository_sha,
                "goVersion": go_version,
                "targetEnvironment": dict(plan_shards.TARGET_ENVIRONMENT),
                "exitCode": 0,
                "goToolDirectory": "C:\\Go\\pkg\\tool\\windows_amd64",
                "trackedWorktreeCleanBefore": True,
                "trackedWorktreeCleanAfter": True,
                "operations": [
                    {"phase": "worktree-clean-before", "exitCode": 0},
                    {"phase": "repository-sha", "exitCode": 0},
                    {"phase": "go-version", "exitCode": 0},
                    {"phase": "go-environment", "exitCode": 0},
                    {"phase": "go-list-all", "exitCode": 0},
                    {"phase": "go-list-package", "package": "example/heavy", "exitCode": 0},
                    {"phase": "testmain-inventory", "package": "example/heavy", "exitCode": 0},
                    {"phase": "compile", "package": "example/heavy", "exitCode": 0},
                    {"phase": "list", "package": "example/heavy", "exitCode": 0},
                    {"phase": "worktree-clean-after", "exitCode": 0},
                    {"phase": "repository-sha-after", "exitCode": 0},
                ],
            },
        )
        plan_hash = plan_shards.sha256_file(bundle / "plan.json")
        for shard in plan["shards"]:
            index = shard["index"]
            directory = shards_root / f"windows-test-shard-{index}"
            invocations = []
            events = []
            for assignment in shard["assignments"]:
                package = assignment["importPath"]
                events.append({"Action": "start", "Package": package})
                for root_entry in assignment["roots"]:
                    name = root_entry["name"]
                    events.append({"Action": "run", "Package": package, "Test": name})
                    action = "skip" if name.startswith("Example") else "pass"
                    events.append({"Action": action, "Package": package, "Test": name})
                events.append({"Action": "pass", "Package": package})
                invocations.append(
                    {
                        "package": package,
                        "binaryName": assignment["binaryName"],
                        "binarySha256": assignment["binarySha256"],
                        "rootCount": len(assignment["roots"]),
                        "commandLineUtf16Units": 1000,
                        "exitCode": 0,
                        "paniconexit0": True,
                        "workingDirectoryRelative": assignment["packageDirectoryRelative"],
                        "pwdMatchesPackageDirectory": True,
                        "goToolDirectoryPrependedToPath": True,
                    }
                )
            write_jsonl(directory / "shard-events.jsonl", events)
            write_json(
                directory / "shard-metadata.json",
                {
                    "schema": verify_results.SHARD_RUN_SCHEMA,
                    "exitCode": 0,
                    "errors": [],
                    "repositorySha": repository_sha,
                    "repositoryShaAfter": repository_sha,
                    "goVersion": go_version,
                    "targetEnvironment": dict(plan_shards.TARGET_ENVIRONMENT),
                    "goToolDirectory": "C:\\Go\\pkg\\tool\\windows_amd64",
                    "trackedWorktreeCleanBefore": True,
                    "trackedWorktreeCleanAfter": True,
                    "cleanTestCacheExitCode": 0,
                    "planSha256": plan_hash,
                    "shardIndex": index,
                    "shardCount": 2,
                    "commandLineLimit": 30000,
                    "timeout": "30m",
                    "shuffle": "off",
                    "testParallel": None,
                    "goMaxProcs": None,
                    "invocations": invocations,
                },
            )
        package_hash = plan_shards.sha256_file(bundle / "package-inventory.json")
        write_jsonl(
            other_root / "other-events.jsonl",
            [
                {"Action": "start", "Package": "example/other"},
                {"Action": "run", "Package": "example/other", "Test": "TestOther"},
                {"Action": "pass", "Package": "example/other", "Test": "TestOther"},
                {"Action": "pass", "Package": "example/other"},
                {"Action": "start", "Package": "example/no-tests"},
                {"Action": "skip", "Package": "example/no-tests"},
            ],
        )
        write_json(
            other_root / "other-metadata.json",
            {
                "schema": verify_results.OTHER_RUN_SCHEMA,
                "exitCode": 0,
                "errors": [],
                "repositorySha": repository_sha,
                "repositoryShaAfter": repository_sha,
                "goVersion": go_version,
                "targetEnvironment": dict(plan_shards.TARGET_ENVIRONMENT),
                "trackedWorktreeCleanBefore": True,
                "trackedWorktreeCleanAfter": True,
                "planSha256": plan_hash,
                "packageInventorySha256": package_hash,
                "expectedPackages": ["example/no-tests", "example/other"],
                "packageCount": 2,
                "timeout": "30m",
                "shuffle": "off",
                "testParallel": None,
                "goMaxProcs": None,
                "commandLineLimit": 30000,
                "commandLineUtf16Units": 500,
                "cleanTestCacheExitCode": 0,
                "testExitCode": 0,
            },
        )
        arguments = [
            "--bundle",
            str(bundle),
            "--shard-results",
            str(shards_root),
            "--other-results",
            str(other_root),
            "--output",
            str(output),
        ]
        return arguments, output, bundle, shards_root, other_root

    def test_complete_fixture_passes_without_a_monolithic_baseline(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, _, _, _ = self.make_fixture(Path(directory))
            self.assertEqual(self.run_verifier(arguments), 0)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(report["passed"])
            self.assertEqual(report["inventory"]["runnableRootCount"], 3)
            self.assertEqual(report["inventory"]["excludedBenchmarkCount"], 1)

    def test_package_less_build_output_is_accepted_and_counted(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, _, _, other_root = self.make_fixture(Path(directory))
            events_path = other_root / "other-events.jsonl"
            events = [json.loads(line) for line in events_path.read_text().splitlines()]
            events.insert(
                0,
                {
                    "ImportPath": "example/dependency",
                    "Action": "build-output",
                    "Output": "compiler warning\n",
                },
            )
            write_jsonl(events_path, events)
            self.assertEqual(self.run_verifier(arguments), 0)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(report["otherPackages"]["buildOutputCount"], 1)

    def test_missing_root_terminal_and_duplicate_run_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, _, shards_root, _ = self.make_fixture(Path(directory))
            paths = sorted(shards_root.rglob("shard-events.jsonl"))
            payloads = [json.loads(line) for line in paths[0].read_text().splitlines()]
            top = next(event for event in payloads if event.get("Action") == "run" and event.get("Test"))
            payloads.append(dict(top))
            terminal_index = next(
                index
                for index, event in enumerate(payloads)
                if event.get("Test") == top["Test"] and event.get("Action") in {"pass", "skip"}
            )
            payloads.pop(terminal_index)
            write_jsonl(paths[0], payloads)
            self.assertEqual(self.run_verifier(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(any("planned roots" in failure for failure in report["failures"]))

    def test_unexpected_root_nonzero_metadata_and_binary_mismatch_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, bundle, shards_root, _ = self.make_fixture(Path(directory))
            events_path = sorted(shards_root.rglob("shard-events.jsonl"))[0]
            with events_path.open("a", encoding="utf-8") as handle:
                handle.write(json.dumps({"Action": "run", "Package": "example/heavy", "Test": "TestSurprise"}) + "\n")
            metadata_path = sorted(shards_root.rglob("shard-metadata.json"))[0]
            metadata = json.loads(metadata_path.read_text())
            metadata["exitCode"] = 7
            write_json(metadata_path, metadata)
            with (bundle / "binaries" / "heavy.test.exe").open("ab") as handle:
                handle.write(b"tampered")
            self.assertEqual(self.run_verifier(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            failures = "\n".join(report["failures"])
            self.assertIn("absent from compiled inventories", failures)
            self.assertIn("nonzero", failures)
            self.assertIn("binary", failures)

    def test_nonheavy_missing_package_terminal_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, _, _, other_root = self.make_fixture(Path(directory))
            events_path = other_root / "other-events.jsonl"
            payloads = [json.loads(line) for line in events_path.read_text().splitlines()]
            write_jsonl(
                events_path,
                [event for event in payloads if event.get("Package") != "example/no-tests"],
            )
            self.assertEqual(self.run_verifier(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(any("non-heavy packages" in failure for failure in report["failures"]))

    def test_each_schema_mandated_exit_field_is_required(self):
        mutations = (
            ("prepare top", "prepare", "exitCode"),
            ("prepare operation", "prepare-operation", "exitCode"),
            ("shard top", "shard", "exitCode"),
            ("shard cache clean", "shard", "cleanTestCacheExitCode"),
            ("shard invocation", "invocation", "exitCode"),
            ("other top", "other", "exitCode"),
            ("other cache clean", "other", "cleanTestCacheExitCode"),
            ("other test", "other", "testExitCode"),
        )
        for label, artifact, key in mutations:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                arguments, output, bundle, shards_root, other_root = self.make_fixture(
                    Path(directory)
                )
                if artifact.startswith("prepare"):
                    path = bundle / "prepare-metadata.json"
                elif artifact in {"shard", "invocation"}:
                    path = sorted(shards_root.rglob("shard-metadata.json"))[0]
                else:
                    path = other_root / "other-metadata.json"
                payload = json.loads(path.read_text(encoding="utf-8"))
                if artifact == "prepare-operation":
                    payload["operations"][0].pop(key)
                elif artifact == "invocation":
                    payload["invocations"][0].pop(key)
                else:
                    payload.pop(key)
                write_json(path, payload)
                self.assertEqual(self.run_verifier(arguments), 1)
                report = json.loads(output.read_text(encoding="utf-8"))
                self.assertTrue(
                    any("missing required exit field" in failure for failure in report["failures"]),
                    report["failures"],
                )

    def test_linux_arm64_and_disabled_cgo_targets_fail_closed(self):
        mutations = (("GOOS", "linux"), ("GOARCH", "arm64"), ("CGO_ENABLED", "0"))
        for key, value in mutations:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                arguments, output, bundle, _, _ = self.make_fixture(Path(directory))
                path = bundle / "plan.json"
                payload = json.loads(path.read_text(encoding="utf-8"))
                payload["targetEnvironment"][key] = value
                write_json(path, payload)
                self.assertEqual(self.run_verifier(arguments), 1)
                report = json.loads(output.read_text(encoding="utf-8"))
                self.assertTrue(any("target" in failure for failure in report["failures"]))

    def test_target_identity_is_checked_at_every_artifact_boundary(self):
        mutations = (
            ("compiled inventory", "inventory", "goos", "linux"),
            ("package inventory", "package", "goarch", "arm64"),
            ("prepare", "prepare", "CGO_ENABLED", "0"),
            ("shard", "shard", "GOOS", "linux"),
            ("non-heavy", "other", "GOARCH", "arm64"),
        )
        for label, artifact, key, value in mutations:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                arguments, output, bundle, shards_root, other_root = self.make_fixture(
                    Path(directory)
                )
                if artifact == "inventory":
                    path = next((bundle / "inventories").glob("*.inventory.json"))
                    payload = json.loads(path.read_text(encoding="utf-8"))
                    payload[key] = value
                elif artifact == "package":
                    path = bundle / "package-inventory.json"
                    payload = json.loads(path.read_text(encoding="utf-8"))
                    payload[key] = value
                elif artifact == "prepare":
                    path = bundle / "prepare-metadata.json"
                    payload = json.loads(path.read_text(encoding="utf-8"))
                    payload["targetEnvironment"][key] = value
                elif artifact == "shard":
                    path = sorted(shards_root.rglob("shard-metadata.json"))[0]
                    payload = json.loads(path.read_text(encoding="utf-8"))
                    payload["targetEnvironment"][key] = value
                else:
                    path = other_root / "other-metadata.json"
                    payload = json.loads(path.read_text(encoding="utf-8"))
                    payload["targetEnvironment"][key] = value
                write_json(path, payload)
                self.assertEqual(self.run_verifier(arguments), 1)
                report = json.loads(output.read_text(encoding="utf-8"))
                self.assertTrue(any("target" in failure for failure in report["failures"]))

    def test_source_and_direct_execution_contract_mutations_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, bundle, shards_root, other_root = self.make_fixture(Path(directory))
            prepare_path = bundle / "prepare-metadata.json"
            prepare_payload = json.loads(prepare_path.read_text(encoding="utf-8"))
            prepare_payload["trackedWorktreeCleanAfter"] = False
            write_json(prepare_path, prepare_payload)

            shard_path = sorted(shards_root.rglob("shard-metadata.json"))[0]
            shard_payload = json.loads(shard_path.read_text(encoding="utf-8"))
            shard_payload["trackedWorktreeCleanBefore"] = False
            shard_payload["invocations"][0]["paniconexit0"] = False
            shard_payload["invocations"][0]["pwdMatchesPackageDirectory"] = False
            shard_payload["invocations"][0]["goToolDirectoryPrependedToPath"] = False
            write_json(shard_path, shard_payload)

            other_path = other_root / "other-metadata.json"
            other_payload = json.loads(other_path.read_text(encoding="utf-8"))
            other_payload["trackedWorktreeCleanAfter"] = False
            write_json(other_path, other_payload)
            self.assertEqual(self.run_verifier(arguments), 1)
            failures = "\n".join(json.loads(output.read_text(encoding="utf-8"))["failures"])
            self.assertIn("clean tracked worktree", failures)
            self.assertIn("paniconexit0", failures)
            self.assertIn("PWD", failures)
            self.assertIn("PATH", failures)

    def test_testmain_declaration_drift_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, bundle, _, _ = self.make_fixture(Path(directory))
            settings_path = bundle / "settings.json"
            payload = json.loads(settings_path.read_text(encoding="utf-8"))
            payload["heavyPackages"][0]["expectedTestMainDeclarations"] = [
                {"file": "new_testmain_test.go", "normalizedSourceSha256": "d" * 64}
            ]
            write_json(settings_path, payload)
            self.assertEqual(self.run_verifier(arguments), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(any("TestMain" in failure for failure in report["failures"]))


if __name__ == "__main__":
    unittest.main()
