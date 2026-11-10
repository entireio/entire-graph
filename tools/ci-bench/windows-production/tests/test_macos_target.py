import copy
import hashlib
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import plan_shards  # noqa: E402
import prepare  # noqa: E402
import run_other  # noqa: E402
import run_shard  # noqa: E402
import targets  # noqa: E402
import test_verify_results as verify_fixtures  # noqa: E402


write_json = verify_fixtures.write_json


DARWIN = targets.DARWIN_ARM64
WINDOWS = targets.WINDOWS_AMD64
DARWIN_SETTINGS = SCRIPT_DIR / "settings.darwin-arm64.json"
DARWIN_WEIGHTS = SCRIPT_DIR / "historical-weights.darwin-arm64.json"
WINDOWS_SETTINGS = SCRIPT_DIR / "settings.json"
REPOSITORY_SHA = "a" * 40
GO_VERSION = "go version go1.27.1 darwin/arm64"


def settings_payload(target=DARWIN, binary="heavy.test", limit=262144):
    value = {
        "schema": plan_shards.SETTINGS_SCHEMA,
        "target": target.name,
        "shardCount": 2,
        "defaultWeightSeconds": 1.0,
        "timeout": "30m",
        "commandLineLimit": limit,
        "shuffle": "off",
        "testParallel": None,
        "goMaxProcs": None,
        "heavyPackages": [
            {
                "argument": "./heavy",
                "importPath": "example/heavy",
                "binaryName": binary,
                "expectedTestMainDeclarations": [],
            }
        ],
    }
    return value


def load_payload(payload):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "settings.json"
        path.write_text(json.dumps(payload), encoding="utf-8")
        return plan_shards.load_settings(path)


def normalized_inventory(target, binary, roots):
    return {
        "importPath": "example/heavy",
        "packageArgument": "./heavy",
        "packageDirectoryRelative": "internal/heavy",
        "binaryName": binary,
        "binarySha256": "a" * 64,
        "binarySizeBytes": 123,
        "repositorySha": "b" * 40,
        "goVersion": GO_VERSION,
        "targetEnvironment": target.environment,
        "listingContract": {
            "arguments": ["-test.paniconexit0", "-test.list=."],
            "paniconexit0": True,
            "pwdMatchesPackageDirectory": True,
            "goToolDirectoryPrependedToPath": True,
        },
        "testMainDeclarations": [],
        "roots": roots,
        "excludedBenchmarks": [],
    }


class TargetSettingsTests(unittest.TestCase):
    def test_committed_settings_name_one_target_each(self):
        windows = plan_shards.load_settings(WINDOWS_SETTINGS)
        darwin = plan_shards.load_settings(DARWIN_SETTINGS)
        self.assertIs(plan_shards.settings_target(windows), WINDOWS)
        self.assertIs(plan_shards.settings_target(darwin), DARWIN)
        # The Windows file predates the target key and must keep meaning Windows.
        self.assertNotIn("target", json.loads(WINDOWS_SETTINGS.read_text(encoding="utf-8")))
        self.assertEqual(
            [item["importPath"] for item in darwin["heavyPackages"]],
            [item["importPath"] for item in windows["heavyPackages"]],
        )
        for item in windows["heavyPackages"]:
            self.assertTrue(item["binaryName"].endswith(".test.exe"))
        for item in darwin["heavyPackages"]:
            self.assertTrue(item["binaryName"].endswith(".test"))
            self.assertNotIn(".exe", item["binaryName"])
            # Go selects no *_windows_test.go file on darwin, and no other test
            # file of these packages declares TestMain.
            self.assertEqual(item["expectedTestMainDeclarations"], [])
        for key in ("timeout", "shuffle", "testParallel", "goMaxProcs", "defaultWeightSeconds"):
            self.assertEqual(darwin[key], windows[key], key)

    def test_macos_shard_count_respects_hosted_runner_concurrency(self):
        # GitHub-hosted macOS concurrency can be as low as five jobs per account
        # and this workflow runs other macOS jobs; see the workflow comment.
        self.assertEqual(plan_shards.load_settings(DARWIN_SETTINGS)["shardCount"], 4)

    def test_committed_macos_weights_load_and_name_only_configured_packages(self):
        weights, metadata = plan_shards.load_weights(DARWIN_WEIGHTS)
        configured = {
            item["importPath"] for item in plan_shards.load_settings(DARWIN_SETTINGS)["heavyPackages"]
        }
        self.assertEqual(metadata["entryCount"], len(weights))
        self.assertLessEqual({package for package, _ in weights}, configured)

    def test_binary_suffix_is_bound_to_the_target(self):
        self.assertEqual(load_payload(settings_payload())["target"], "darwin/arm64")
        with self.assertRaisesRegex(ValueError, "binaryName"):
            load_payload(settings_payload(binary="heavy.test.exe"))
        with self.assertRaisesRegex(ValueError, "binaryName"):
            load_payload(settings_payload(target=WINDOWS, binary="heavy.test", limit=30000))
        with self.assertRaisesRegex(ValueError, "binaryName"):
            load_payload(settings_payload(binary="heavy"))

    def test_unknown_target_fails_closed(self):
        for name in ("linux/amd64", "darwin/amd64", "", None, 7):
            payload = settings_payload()
            payload["target"] = name
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "unsupported target"):
                load_payload(payload)

    def test_command_line_limit_range_is_per_target(self):
        self.assertEqual(DARWIN.command_line_limit_maximum, targets.DARWIN_ARG_MAX // 2)
        self.assertEqual(WINDOWS.command_line_limit_maximum, 32767)
        committed = plan_shards.load_settings(DARWIN_SETTINGS)["commandLineLimit"]
        self.assertEqual(committed, targets.DARWIN_ARG_MAX // 4)
        load_payload(settings_payload(limit=DARWIN.command_line_limit_maximum))
        with self.assertRaisesRegex(ValueError, "commandLineLimit"):
            load_payload(settings_payload(limit=DARWIN.command_line_limit_maximum + 1))
        with self.assertRaisesRegex(ValueError, "commandLineLimit"):
            load_payload(settings_payload(limit=targets.DARWIN_ARG_MAX))
        # The POSIX figure is never acceptable as a Windows budget.
        with self.assertRaisesRegex(ValueError, "commandLineLimit"):
            load_payload(settings_payload(target=WINDOWS, binary="heavy.test.exe", limit=committed))


class PosixCommandBudgetTests(unittest.TestCase):
    def test_argv_cost_counts_encoded_bytes_and_one_nul_per_argument(self):
        cost = DARWIN.command_line_cost("/go/bin/go", ["arg", "\U0001f600", ""])
        self.assertEqual(cost, len(b"/go/bin/go") + 1 + 3 + 1 + 4 + 1 + 0 + 1)
        self.assertEqual(DARWIN.command_line_metric, "commandLineArgvBytes")

    def test_windows_cost_is_still_serialized_utf16_units(self):
        units = WINDOWS.command_line_cost("C:\\go.exe", ["arg", "\U0001f600"])
        self.assertEqual(units, len("C:\\go.exe arg \U0001f600".encode("utf-16-le")) // 2 + 1)
        self.assertEqual(units, run_shard.command_line_utf16_units("C:\\go.exe", ["arg", "\U0001f600"]))
        self.assertEqual(units, run_other.command_line_utf16_units("C:\\go.exe", ["arg", "\U0001f600"]))
        self.assertEqual(WINDOWS.command_line_metric, "commandLineUtf16Units")

    def test_argument_area_counts_environment_and_pointers(self):
        pointer = targets.struct.calcsize("P")
        used = targets.argument_area_bytes(["go", "test"], {"A": "b", "PATH": "/bin"})
        strings = (2 + 1) + (4 + 1) + (1 + 1 + 1 + 1) + (4 + 1 + 4 + 1)
        self.assertEqual(used, strings + pointer * (2 + 2 + 2))

    def test_argument_area_above_the_kernel_limit_fails_closed(self):
        environment = {"BIG": "x" * 4096}
        with mock.patch.object(targets, "argument_area_maximum", return_value=4096):
            with self.assertRaisesRegex(RuntimeError, "exec argument area"):
                targets.checked_argument_area(DARWIN, ["go"], environment, "fixture")
            # Windows has no such area; its budget is the serialized command line.
            self.assertEqual(targets.checked_argument_area(WINDOWS, ["go"], environment, "fixture"), {})
        with mock.patch.object(targets, "argument_area_maximum", return_value=targets.DARWIN_ARG_MAX):
            record = targets.checked_argument_area(DARWIN, ["go"], environment, "fixture")
        self.assertEqual(record["execArgumentAreaLimit"], targets.DARWIN_ARG_MAX)
        self.assertTrue(targets.valid_argument_area(DARWIN, record))
        self.assertFalse(targets.valid_argument_area(DARWIN, {}))
        self.assertFalse(
            targets.valid_argument_area(
                DARWIN, {"execArgumentAreaBytes": 9, "execArgumentAreaLimit": 8}
            )
        )
        self.assertTrue(targets.valid_argument_area(WINDOWS, {}))

    @unittest.skipUnless(hasattr(os, "sysconf"), "POSIX host required")
    def test_host_kernel_limit_is_readable_on_posix_hosts(self):
        self.assertGreaterEqual(targets.argument_area_maximum(), 4096)

    @unittest.skipUnless(sys.platform == "darwin", "macOS host required")
    def test_documented_darwin_arg_max_matches_this_kernel(self):
        self.assertEqual(targets.argument_area_maximum(), targets.DARWIN_ARG_MAX)


class DarwinPlanTests(unittest.TestCase):
    ROOTS = [
        {"name": "TestSlow", "kind": "Test"},
        {"name": "ExampleCurrent", "kind": "Example"},
        {"name": "FuzzCurrent", "kind": "Fuzz"},
        {"name": "TestNew", "kind": "Test"},
    ]

    def test_plan_carries_the_darwin_identity_and_partitions_every_root(self):
        inventory = normalized_inventory(DARWIN, "heavy.test", self.ROOTS)
        weights = {("example/heavy", "TestSlow"): 10.0}
        metadata = {"path": "weights.json", "sha256": "c" * 64, "entryCount": 1}
        loaded = load_payload(settings_payload())
        plan = plan_shards.create_plan([inventory], loaded, weights, metadata)
        self.assertEqual(plan, plan_shards.create_plan([inventory], loaded, weights, metadata))
        self.assertEqual(
            plan["targetEnvironment"], {"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "1"}
        )
        self.assertEqual(plan["settings"]["commandLineLimit"], 262144)
        assigned = [
            (root["name"], root["weightSource"], assignment["binaryName"])
            for shard in plan["shards"]
            for assignment in shard["assignments"]
            for root in assignment["roots"]
        ]
        self.assertEqual(sorted(name for name, _, _ in assigned), sorted(r["name"] for r in self.ROOTS))
        self.assertEqual({binary for _, _, binary in assigned}, {"heavy.test"})
        sources = dict((name, source) for name, source, _ in assigned)
        self.assertEqual(sources["TestSlow"], "historical-darwin")
        self.assertEqual(sources["TestNew"], "default-current-inventory")

    def test_inventory_of_another_target_cannot_be_planned(self):
        loaded = load_payload(settings_payload())
        foreign = normalized_inventory(WINDOWS, "heavy.test", self.ROOTS)
        with self.assertRaisesRegex(ValueError, "darwin/arm64"):
            plan_shards.create_plan([foreign], loaded, {}, {})
        windows_settings = load_payload(
            settings_payload(target=WINDOWS, binary="heavy.test.exe", limit=30000)
        )
        native = normalized_inventory(DARWIN, "heavy.test.exe", self.ROOTS)
        with self.assertRaisesRegex(ValueError, "windows/amd64"):
            plan_shards.create_plan([native], windows_settings, {}, {})

    def test_compiled_inventory_loader_binds_target_and_binary_suffix(self):
        payload = {
            "schema": plan_shards.INVENTORY_SCHEMA,
            "repositorySha": "b" * 40,
            "goVersion": GO_VERSION,
            "goos": "darwin",
            "goarch": "arm64",
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
            "binaryName": "heavy.test",
            "binarySha256": "a" * 64,
            "binarySizeBytes": 1,
            "listExitCode": 0,
            "roots": [{"name": "TestA", "kind": "Test"}],
            "excludedBenchmarks": [],
        }
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "heavy.test.inventory.json"
            write_json(path, payload)
            loaded = plan_shards.load_inventories([path], DARWIN)
            self.assertEqual(loaded[0]["targetEnvironment"], DARWIN.environment)
            with self.assertRaisesRegex(ValueError, "unsafe binaryName"):
                plan_shards.load_inventories([path])
            for key, value in (("goos", "windows"), ("goarch", "amd64"), ("cgoEnabled", "0")):
                mutated = dict(payload)
                mutated[key] = value
                write_json(path, mutated)
                with self.subTest(key=key), self.assertRaisesRegex(ValueError, "target"):
                    plan_shards.load_inventories([path], DARWIN)
            mutated = dict(payload)
            mutated["binaryName"] = "heavy.test.exe"
            write_json(path, mutated)
            with self.assertRaisesRegex(ValueError, "unsafe binaryName"):
                plan_shards.load_inventories([path], DARWIN)


class FakeDarwinGo:
    """Stands in for every subprocess.run call made by run_shard.run."""

    def __init__(self, go, tool_directory, environment):
        self.go = go
        self.tool_directory = tool_directory
        self.environment = environment
        self.test_calls = []

    def __call__(self, argv, **kwargs):
        if argv[:1] == ["git"] and "rev-parse" in argv:
            return subprocess.CompletedProcess(argv, 0, REPOSITORY_SHA + "\n", "")
        if argv[:1] == ["git"] and "status" in argv:
            return subprocess.CompletedProcess(argv, 0, "", "")
        if argv == [self.go, "version"]:
            return subprocess.CompletedProcess(argv, 0, GO_VERSION + "\n", "")
        if argv[:3] == [self.go, "env", "-json"]:
            value = dict(self.environment)
            value["GOTOOLDIR"] = str(self.tool_directory)
            return subprocess.CompletedProcess(argv, 0, json.dumps(value), "")
        if argv == [self.go, "clean", "-testcache"]:
            return subprocess.CompletedProcess(argv, 0, b"", b"")
        if argv[:3] == [self.go, "tool", "test2json"]:
            self.test_calls.append((list(argv), dict(kwargs)))
            return subprocess.CompletedProcess(argv, 0)
        raise AssertionError(f"unexpected subprocess.run call: {argv!r}")


class DarwinRunShardTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name).resolve()
        self.repository = root / "repo"
        self.bundle = root / "bundle"
        self.output = root / "out"
        self.tool_directory = root / "gotool"
        self.tool_directory.mkdir()
        go = root / "bin" / "go"
        go.parent.mkdir()
        go.write_bytes(b"fake go")
        self.go = str(go.resolve())
        (self.bundle / "binaries").mkdir(parents=True)
        (self.repository / "heavy").mkdir(parents=True)
        self.binary = self.bundle / "binaries" / "heavy.test"
        self.binary.write_bytes(b"compiled-darwin-test-binary")
        # actions/upload-artifact drops POSIX modes, so the bundle arrives like this.
        self.binary.chmod(0o644)
        self.plan = {
            "schema": run_shard.PLAN_SCHEMA,
            "targetEnvironment": DARWIN.environment,
            "shardCount": 1,
            "repositorySha": REPOSITORY_SHA,
            "goVersion": GO_VERSION,
            "settings": {
                "shuffle": "off",
                "timeout": "30m",
                "commandLineLimit": 262144,
                "testParallel": None,
                "goMaxProcs": None,
            },
            "shards": [
                {
                    "index": 0,
                    "assignments": [
                        {
                            "importPath": "example/heavy",
                            "binaryName": "heavy.test",
                            "binarySha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
                            "runRegex": "^(TestA|TestB)$",
                            "roots": ["TestA", "TestB"],
                            "packageDirectoryRelative": "heavy",
                        }
                    ],
                }
            ],
        }

    def run_direct(self, target_name=DARWIN.name, platform="darwin", environment=None, plan=None):
        (self.bundle / "plan.json").write_text(json.dumps(plan or self.plan), encoding="utf-8")
        fake = FakeDarwinGo(self.go, self.tool_directory, environment or DARWIN.environment)
        argv = [
            "--repository", str(self.repository),
            "--bundle", str(self.bundle),
            "--output", str(self.output),
            "--shard-index", "0",
            "--expected-repository-sha", REPOSITORY_SHA,
        ]
        if target_name is not None:
            argv += ["--target", target_name]
        metadata = {"invocations": []}
        with mock.patch.object(run_shard.sys, "platform", platform), mock.patch.object(
            run_shard.shutil, "which", return_value=self.go
        ), mock.patch.object(run_shard.subprocess, "run", side_effect=fake), mock.patch.object(
            targets, "argument_area_maximum", return_value=targets.DARWIN_ARG_MAX
        ), redirect_stderr(io.StringIO()), redirect_stdout(io.StringIO()):
            exit_code = run_shard.run(run_shard.parse_args(argv), metadata, heartbeat_interval=60.0)
        return fake, metadata, exit_code

    def test_darwin_shard_launches_with_cmd_go_lifecycle_and_posix_budget(self):
        fake, metadata, exit_code = self.run_direct()
        self.assertEqual(exit_code, 0)
        self.assertEqual(metadata["targetEnvironment"], DARWIN.environment)
        self.assertEqual(metadata["commandLineLimit"], 262144)
        (argv, kwargs), = fake.test_calls
        self.assertEqual(
            argv,
            [
                self.go, "tool", "test2json", "-t", "-p", "example/heavy", str(self.binary),
                "-test.paniconexit0", "-test.timeout=30m", "-test.run=^(TestA|TestB)$",
                "-test.shuffle=off", "-test.v=test2json",
            ],
        )
        package_directory = self.repository / "heavy"
        self.assertEqual(kwargs["cwd"], package_directory)
        self.assertEqual(kwargs["env"]["PWD"], str(package_directory))
        self.assertEqual(kwargs["env"]["PATH"].split(os.pathsep)[0], str(self.tool_directory))
        self.assertEqual(kwargs["env"]["GOOS"], "darwin")
        self.assertEqual(kwargs["env"]["GOARCH"], "arm64")
        self.assertEqual(kwargs["env"]["CGO_ENABLED"], "1")
        if "GOMAXPROCS" not in os.environ:
            self.assertNotIn("GOMAXPROCS", kwargs["env"])
        invocation, = metadata["invocations"]
        self.assertNotIn("commandLineUtf16Units", invocation)
        self.assertEqual(invocation["commandLineArgvBytes"], DARWIN.command_line_cost(argv[0], argv[1:]))
        self.assertEqual(
            invocation["execArgumentAreaBytes"], targets.argument_area_bytes(argv, kwargs["env"])
        )
        self.assertEqual(invocation["execArgumentAreaLimit"], targets.DARWIN_ARG_MAX)
        self.assertTrue(invocation["paniconexit0"])
        self.assertTrue(invocation["pwdMatchesPackageDirectory"])
        self.assertTrue(invocation["goToolDirectoryPrependedToPath"])

    @unittest.skipUnless(os.name == "posix", "POSIX file modes required")
    def test_execute_bit_lost_in_artifact_transport_is_restored_after_hash_check(self):
        self.assertFalse(os.access(self.binary, os.X_OK))
        _, metadata, exit_code = self.run_direct()
        self.assertEqual(exit_code, 0)
        self.assertTrue(os.access(self.binary, os.X_OK))
        self.assertTrue(metadata["invocations"][0]["executableBitRestored"])

    @unittest.skipUnless(os.name == "posix", "POSIX file modes required")
    def test_tampered_binary_is_refused_before_it_is_made_executable(self):
        with self.binary.open("ab") as handle:
            handle.write(b"tampered")
        with self.assertRaisesRegex(RuntimeError, "binary hash mismatch"):
            self.run_direct()
        self.assertFalse(os.access(self.binary, os.X_OK))

    def test_argv_above_the_settings_budget_refuses_implicit_batching(self):
        plan = copy.deepcopy(self.plan)
        plan["settings"]["commandLineLimit"] = 1024
        plan["shards"][0]["assignments"][0]["runRegex"] = "^(" + "|".join(
            f"TestRoot{index:04d}" for index in range(200)
        ) + ")$"
        with self.assertRaisesRegex(RuntimeError, "argv bytes, above 1024"):
            self.run_direct(plan=plan)

    def test_target_argument_plan_host_and_toolchain_must_all_agree(self):
        with self.assertRaisesRegex(ValueError, "windows/amd64"):
            self.run_direct(target_name=None, platform="win32")
        with self.assertRaisesRegex(RuntimeError, "native macOS"):
            self.run_direct(platform="linux")
        with self.assertRaisesRegex(RuntimeError, "native Windows"):
            self.run_direct(target_name=None, platform="darwin")
        with self.assertRaisesRegex(RuntimeError, "unexpected target Go environment"):
            self.run_direct(environment={"GOOS": "darwin", "GOARCH": "amd64", "CGO_ENABLED": "1"})
        windows_plan = copy.deepcopy(self.plan)
        windows_plan["targetEnvironment"] = WINDOWS.environment
        with self.assertRaisesRegex(ValueError, "darwin/arm64"):
            self.run_direct(plan=windows_plan)
        with self.assertRaises(SystemExit), redirect_stderr(io.StringIO()):
            run_shard.parse_args(["--target", "linux/amd64"])

    def test_prepare_and_non_heavy_runner_refuse_a_foreign_host(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arguments = [
                "--repository", str(root),
                "--output", str(root / "out"),
                "--settings", str(DARWIN_SETTINGS),
                "--weights", str(DARWIN_WEIGHTS),
                "--expected-repository-sha", REPOSITORY_SHA,
                "--target", DARWIN.name,
            ]
            with mock.patch.object(prepare.sys, "platform", "win32"), self.assertRaisesRegex(
                RuntimeError, "native macOS"
            ):
                prepare.prepare(prepare.parse_args(arguments))
            other = run_other.parse_args(
                [
                    "--repository", str(root),
                    "--bundle", str(root),
                    "--output", str(root / "out"),
                    "--expected-repository-sha", REPOSITORY_SHA,
                    "--target", DARWIN.name,
                ]
            )
            with mock.patch.object(run_other.sys, "platform", "win32"), self.assertRaisesRegex(
                RuntimeError, "native macOS"
            ):
                run_other.run(other, {})

    def test_prepare_refuses_settings_of_another_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for target, settings_path in ((DARWIN, WINDOWS_SETTINGS), (WINDOWS, DARWIN_SETTINGS)):
                arguments = prepare.parse_args(
                    [
                        "--repository", str(root),
                        "--output", str(root / "out"),
                        "--settings", str(settings_path),
                        "--weights", str(DARWIN_WEIGHTS),
                        "--expected-repository-sha", REPOSITORY_SHA,
                        "--target", target.name,
                    ]
                )
                with self.subTest(target=target.name), mock.patch.object(
                    prepare.sys, "platform", target.host_platform
                ), self.assertRaisesRegex(ValueError, "differs from the requested target"):
                    prepare.prepare(arguments)


class DarwinVerifyTests(unittest.TestCase):
    """The verifier's coverage and identity checks, replayed for darwin/arm64."""

    def setUp(self):
        self.fixtures = verify_fixtures.VerifyResultsTests()
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.arguments, self.output, self.bundle, self.shards_root, self.other_root = (
            self.fixtures.make_fixture(Path(self.temporary.name), DARWIN)
        )

    def verify(self, arguments=None):
        code = self.fixtures.run_verifier(self.arguments if arguments is None else arguments)
        return code, json.loads(self.output.read_text(encoding="utf-8"))

    def mutate(self, path, change):
        payload = json.loads(path.read_text(encoding="utf-8"))
        change(payload)
        write_json(path, payload)

    def first_shard_metadata(self):
        return sorted(self.shards_root.rglob("shard-metadata.json"))[0]

    def test_complete_darwin_fixture_passes(self):
        self.assertIn("--target", self.arguments)
        self.assertTrue((self.bundle / "binaries" / "heavy.test").is_file())
        code, report = self.verify()
        self.assertEqual(code, 0, report["failures"])
        self.assertTrue(report["passed"])
        self.assertEqual(report["inventory"]["runnableRootCount"], 3)
        self.assertEqual(
            report["inventory"]["targetEnvironment"],
            {"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "1"},
        )

    def test_darwin_evidence_does_not_verify_as_windows_or_the_reverse(self):
        without_target = self.arguments[: self.arguments.index("--target")]
        code, report = self.verify(without_target)
        self.assertEqual(code, 1)
        self.assertFalse(report["passed"])
        with tempfile.TemporaryDirectory() as directory:
            arguments, output, _, _, _ = self.fixtures.make_fixture(Path(directory))
            code = self.fixtures.run_verifier([*arguments, "--target", DARWIN.name])
            self.assertEqual(code, 1)
            self.assertFalse(json.loads(output.read_text(encoding="utf-8"))["passed"])

    def test_target_identity_is_checked_at_every_darwin_artifact_boundary(self):
        def environment(key, value):
            return lambda payload: payload["targetEnvironment"].__setitem__(key, value)

        mutations = (
            ("plan", self.bundle / "plan.json", environment("GOARCH", "amd64")),
            (
                "compiled inventory",
                next((self.bundle / "inventories").glob("*.inventory.json")),
                lambda payload: payload.__setitem__("goos", "linux"),
            ),
            (
                "package inventory",
                self.bundle / "package-inventory.json",
                lambda payload: payload.__setitem__("goarch", "amd64"),
            ),
            ("prepare", self.bundle / "prepare-metadata.json", environment("CGO_ENABLED", "0")),
            ("shard", self.first_shard_metadata(), environment("GOOS", "linux")),
            ("non-heavy", self.other_root / "other-metadata.json", environment("GOARCH", "amd64")),
            (
                "settings",
                self.bundle / "settings.json",
                lambda payload: payload.__setitem__("target", "linux/arm64"),
            ),
        )
        for label, path, change in mutations:
            original = path.read_bytes()
            with self.subTest(label=label):
                self.mutate(path, change)
                code, report = self.verify()
                self.assertEqual(code, 1)
                self.assertTrue(any("target" in failure for failure in report["failures"]))
            path.write_bytes(original)
        self.assertEqual(self.verify()[0], 0)

    def test_missing_root_and_duplicate_run_fail_closed_on_darwin(self):
        events_path = sorted(self.shards_root.rglob("shard-events.jsonl"))[0]
        lines = events_path.read_text(encoding="utf-8").splitlines()
        run_line = next(line for line in lines if '"run"' in line)
        events_path.write_text("\n".join([*lines, run_line]) + "\n", encoding="utf-8")
        code, report = self.verify()
        self.assertEqual(code, 1)
        self.assertTrue(any("planned roots" in failure for failure in report["failures"]))

    def test_posix_budget_evidence_is_required_and_bounded(self):
        def drop_argv(payload):
            del payload["invocations"][0]["commandLineArgvBytes"]

        def windows_metric(payload):
            invocation = payload["invocations"][0]
            invocation["commandLineUtf16Units"] = invocation.pop("commandLineArgvBytes")

        def over_argv(payload):
            payload["invocations"][0]["commandLineArgvBytes"] = 262145

        def drop_area(payload):
            del payload["invocations"][0]["execArgumentAreaBytes"]

        def over_area(payload):
            invocation = payload["invocations"][0]
            invocation["execArgumentAreaBytes"] = invocation["execArgumentAreaLimit"] + 1

        shard = self.first_shard_metadata()
        for label, change, fragment in (
            ("argv omitted", drop_argv, "command line exceeded or omitted"),
            ("argv in the Windows unit", windows_metric, "command line exceeded or omitted"),
            ("argv over budget", over_argv, "command line exceeded or omitted"),
            ("area omitted", drop_area, "exec argument area"),
            ("area over limit", over_area, "exec argument area"),
        ):
            original = shard.read_bytes()
            with self.subTest(label=label):
                self.mutate(shard, change)
                code, report = self.verify()
                self.assertEqual(code, 1)
                self.assertTrue(any(fragment in failure for failure in report["failures"]))
            shard.write_bytes(original)

        other = self.other_root / "other-metadata.json"
        for label, change, fragment in (
            (
                "argv omitted",
                lambda payload: payload.pop("commandLineArgvBytes"),
                "non-heavy command line exceeded or omitted",
            ),
            (
                "area omitted",
                lambda payload: payload.pop("execArgumentAreaLimit"),
                "non-heavy exec argument area",
            ),
        ):
            original = other.read_bytes()
            with self.subTest(runner="non-heavy", label=label):
                self.mutate(other, change)
                code, report = self.verify()
                self.assertEqual(code, 1)
                self.assertTrue(any(fragment in failure for failure in report["failures"]))
            other.write_bytes(original)

    def test_binary_and_lifecycle_mutations_fail_closed_on_darwin(self):
        with (self.bundle / "binaries" / "heavy.test").open("ab") as handle:
            handle.write(b"mutated")

        def lifecycle(payload):
            invocation = payload["invocations"][0]
            invocation["paniconexit0"] = False
            invocation["pwdMatchesPackageDirectory"] = False
            invocation["goToolDirectoryPrependedToPath"] = False

        self.mutate(self.first_shard_metadata(), lifecycle)
        code, report = self.verify()
        self.assertEqual(code, 1)
        failures = "\n".join(report["failures"])
        for fragment in ("binary", "paniconexit0", "PWD", "PATH"):
            self.assertIn(fragment, failures)


if __name__ == "__main__":
    unittest.main()
