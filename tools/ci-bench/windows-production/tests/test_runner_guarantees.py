"""One test per refusal of the prepare, shard and non-heavy runners, with every subprocess faked."""

import argparse
import hashlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import plan_shards  # noqa: E402
import prepare  # noqa: E402
import run_other  # noqa: E402
import run_shard  # noqa: E402
import targets  # noqa: E402


SHA = "a" * 40
OTHER_SHA = "c" * 40
TARGETS = (targets.WINDOWS_AMD64, targets.DARWIN_ARM64)


def go_version(target):
    return f"go version go1.27.1 {target.name}"


class Workspace:
    """A temporary repository, bundle and fake toolchain for one target."""

    def __init__(self, test, target):
        self.target = target
        temporary = tempfile.TemporaryDirectory()
        test.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.repository = self.root / "repo"
        (self.repository / "heavy").mkdir(parents=True)
        self.tool_directory = self.root / "gotool"
        self.tool_directory.mkdir()
        go = self.root / "bin" / "go"
        go.parent.mkdir()
        go.write_bytes(b"fake go")
        self.go = str(go)
        self.binary_name = "heavy" + target.binary_suffix
        self.limit = 30000 if target is targets.WINDOWS_AMD64 else 262144

    def go_environment(self):
        return json.dumps({**self.target.environment, "GOTOOLDIR": str(self.tool_directory)})

    def patches(self, module, fake, patch_run_command=False):
        stack = [
            mock.patch.object(module.sys, "platform", self.target.host_platform),
            mock.patch.object(module.shutil, "which", return_value=self.go),
            mock.patch.object(targets, "argument_area_maximum", return_value=targets.DARWIN_ARG_MAX),
        ]
        if patch_run_command:
            stack.append(mock.patch.object(module, "run_command", side_effect=fake))
        else:
            stack.append(mock.patch.object(module.subprocess, "run", side_effect=fake))
        return stack


# --------------------------------------------------------------------------
# prepare.py
# --------------------------------------------------------------------------
class FakePrepareCommands:
    """Answers prepare.run_command for one heavy package; fields can be overridden per test."""

    def __init__(self, workspace):
        self.w = workspace
        self.head = SHA
        self.status = ""
        self.listing = "TestA\nExampleB\nFuzzC\nBenchmarkD\n"
        self.listing_stderr = ""
        self.test_mains = []
        self.environment = workspace.go_environment()
        self.packages = ["example/heavy", "example/other"]

    def __call__(self, arguments, *, cwd, environment, stdout_path=None, stderr_path=None, stdin_text=None):
        def done(stdout="", stderr="", code=0):
            for path, value in ((stdout_path, stdout), (stderr_path, stderr)):
                if path is not None:
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(value, encoding="utf-8")
            return subprocess.CompletedProcess(arguments, code, stdout, stderr), 0.01

        if arguments[0] == "git" and "status" in arguments:
            return done(self.status)
        if arguments[0] == "git" and "rev-parse" in arguments:
            return done(self.head + "\n")
        if arguments[1:] == ["version"]:
            return done(go_version(self.w.target) + "\n")
        if arguments[1:3] == ["env", "-json"]:
            return done(self.environment)
        if arguments[1:] == ["list", "-json", "./..."]:
            rows = [
                {"ImportPath": name, "Dir": str(self.w.repository / "heavy"), "TestGoFiles": ["a_test.go"]}
                for name in self.packages
            ]
            return done("\n".join(json.dumps(row) for row in rows))
        if arguments[1:3] == ["list", "-json"]:
            return done(json.dumps({"ImportPath": "example/heavy", "Dir": str(self.w.repository / "heavy")}))
        if arguments[1] == "run":
            return done(json.dumps({"schema": prepare.TESTMAIN_SCHEMA, "declarations": self.test_mains}))
        if arguments[1] == "test" and "-c" in arguments:
            Path(arguments[arguments.index("-o") + 1]).write_bytes(b"compiled test binary")
            return done()
        if arguments[1:] == ["-test.paniconexit0", "-test.list=."]:
            return done(self.listing, self.listing_stderr)
        raise AssertionError(f"unexpected command {arguments!r}")


class PrepareGuaranteeTests(unittest.TestCase):
    def run_prepare(self, target, adjust=None, expected_sha=SHA, test_mains=None):
        w = Workspace(self, target)
        settings = {
            "schema": plan_shards.SETTINGS_SCHEMA,
            "shardCount": 2,
            "defaultWeightSeconds": 1.0,
            "timeout": "30m",
            "commandLineLimit": w.limit,
            "shuffle": "off",
            "testParallel": None,
            "goMaxProcs": None,
            "heavyPackages": [
                {
                    "argument": "./heavy",
                    "importPath": "example/heavy",
                    "binaryName": w.binary_name,
                    "expectedTestMainDeclarations": test_mains or [],
                }
            ],
        }
        if target is not targets.WINDOWS_AMD64:
            settings["target"] = target.name
        (w.root / "settings.json").write_text(json.dumps(settings), encoding="utf-8")
        (w.root / "weights.json").write_text(
            json.dumps({"schema": plan_shards.WEIGHTS_SCHEMA, "weights": []}), encoding="utf-8"
        )
        fake = FakePrepareCommands(w)
        if adjust:
            adjust(fake)
        arguments = prepare.parse_args(
            [
                "--repository", str(w.repository),
                "--output", str(w.root / "bundle"),
                "--settings", str(w.root / "settings.json"),
                "--weights", str(w.root / "weights.json"),
                "--expected-repository-sha", expected_sha,
                "--target", target.name,
            ]
        )
        patches = w.patches(prepare, fake, patch_run_command=True)
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)
        prepare.prepare(arguments)
        return w

    def for_targets(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                yield target

    def test_a_clean_matching_checkout_is_inventoried_and_planned(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                w = self.run_prepare(target)
                plan = json.loads((w.root / "bundle" / "plan.json").read_text(encoding="utf-8"))
                self.assertEqual(plan["rootCount"], 3)
                self.assertEqual(plan["repositorySha"], SHA)
                inventory = json.loads(
                    (w.root / "bundle" / "inventories" / f"{w.binary_name}.inventory.json").read_text("utf-8")
                )
                self.assertEqual(inventory["excludedBenchmarks"], ["BenchmarkD"])

    def refused(self, error, pattern, **keywords):
        for target in TARGETS:
            with self.subTest(target=target.name), self.assertRaisesRegex(error, pattern):
                self.run_prepare(target, **keywords)

    def test_checkout_at_another_commit_is_refused(self):
        self.refused(RuntimeError, "does not match expected", expected_sha=OTHER_SHA)

    def test_malformed_expected_commit_is_refused(self):
        self.refused(ValueError, "40 lowercase hexadecimal", expected_sha="main")

    def test_modified_tracked_worktree_is_refused(self):
        self.refused(
            RuntimeError, "tracked worktree precondition", adjust=lambda fake: setattr(fake, "status", " M file.go\n")
        )

    def test_toolchain_for_another_target_is_refused(self):
        def other_target(fake):
            fake.environment = json.dumps({"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1", "GOTOOLDIR": "/x"})

        self.refused(RuntimeError, "unexpected target Go environment", adjust=other_target)

    def test_testmain_declaration_drift_is_refused(self):
        drift = [{"file": "new_test.go", "normalizedSourceSha256": "d" * 64}]
        self.refused(RuntimeError, "TestMain declaration drift", adjust=lambda fake: setattr(fake, "test_mains", drift))

    def test_listing_that_writes_to_stderr_is_refused(self):
        self.refused(
            RuntimeError, "unexpected listing stderr", adjust=lambda fake: setattr(fake, "listing_stderr", "warning\n")
        )

    def test_listing_with_an_unclassifiable_name_is_refused(self):
        self.refused(ValueError, "unexpected -test.list output", adjust=lambda fake: setattr(fake, "listing", "TestA\nmain\n"))

    def test_listing_with_a_subtest_path_is_refused(self):
        self.refused(ValueError, "unexpected -test.list output", adjust=lambda fake: setattr(fake, "listing", "TestA/sub\n"))

    def test_configured_heavy_package_missing_from_go_list_is_refused(self):
        self.refused(
            ValueError, "configured heavy packages absent", adjust=lambda fake: setattr(fake, "packages", ["example/other"])
        )


# --------------------------------------------------------------------------
# run_shard.py
# --------------------------------------------------------------------------
class FakeShardProcesses:
    def __init__(self, workspace):
        self.w = workspace
        self.head = SHA
        self.listing = b"TestA\nTestB\nBenchmarkC\n"
        self.list_code = 0
        self.list_stderr = b""
        self.test_code = 0
        self.environment = workspace.go_environment()
        self.calls = []

    def __call__(self, argv, **kwargs):
        self.calls.append(list(argv))
        if argv[0] == "git" and "rev-parse" in argv:
            return subprocess.CompletedProcess(argv, 0, self.head + "\n", "")
        if argv[0] == "git" and "status" in argv:
            return subprocess.CompletedProcess(argv, 0, "", "")
        if argv[1:] == ["version"]:
            return subprocess.CompletedProcess(argv, 0, go_version(self.w.target) + "\n", "")
        if argv[1:3] == ["env", "-json"]:
            return subprocess.CompletedProcess(argv, 0, self.environment, "")
        if argv[1:] == ["clean", "-testcache"]:
            return subprocess.CompletedProcess(argv, 0, b"", b"")
        if argv[1:] == ["-test.paniconexit0", "-test.list=."]:
            return subprocess.CompletedProcess(argv, self.list_code, self.listing, self.list_stderr)
        if argv[1:3] == ["tool", "test2json"]:
            return subprocess.CompletedProcess(argv, self.test_code)
        raise AssertionError(f"unexpected subprocess.run call: {argv!r}")


class ShardGuaranteeTests(unittest.TestCase):
    def run_shard(self, target, adjust=None, expected_sha=SHA, plan_change=None, tamper=False):
        w = Workspace(self, target)
        bundle = w.root / "bundle"
        (bundle / "binaries").mkdir(parents=True)
        binary = bundle / "binaries" / w.binary_name
        binary.write_bytes(b"compiled test binary")
        plan = {
            "schema": run_shard.PLAN_SCHEMA,
            "targetEnvironment": target.environment,
            "shardCount": 1,
            "repositorySha": SHA,
            "goVersion": go_version(target),
            "settings": {
                "shuffle": "off", "timeout": "30m", "commandLineLimit": w.limit,
                "testParallel": None, "goMaxProcs": None,
            },
            "shards": [
                {
                    "index": 0,
                    "assignments": [
                        {
                            "importPath": "example/heavy",
                            "binaryName": w.binary_name,
                            "binarySha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                            "runRegex": "^(TestA|TestB)$",
                            "roots": ["TestA", "TestB"],
                            "packageDirectoryRelative": "heavy",
                        }
                    ],
                }
            ],
        }
        if plan_change:
            plan_change(plan)
        (bundle / "plan.json").write_text(json.dumps(plan), encoding="utf-8")
        if tamper:
            binary.write_bytes(b"another test binary")
        fake = FakeShardProcesses(w)
        if adjust:
            adjust(fake)
        arguments = run_shard.parse_args(
            [
                "--repository", str(w.repository), "--bundle", str(bundle), "--output", str(w.root / "out"),
                "--shard-index", "0", "--expected-repository-sha", expected_sha, "--target", target.name,
            ]
        )
        for patch in w.patches(run_shard, fake):
            patch.start()
            self.addCleanup(patch.stop)
        metadata = {"invocations": []}
        with redirect_stderr(io.StringIO()), redirect_stdout(io.StringIO()):
            code = run_shard.run(arguments, metadata, heartbeat_interval=60.0)
        return code, metadata, fake

    def refused(self, error, pattern, **keywords):
        for target in TARGETS:
            with self.subTest(target=target.name), self.assertRaisesRegex(error, pattern):
                self.run_shard(target, **keywords)

    def test_shard_records_what_its_own_binary_lists(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                code, metadata, fake = self.run_shard(target)
                self.assertEqual(code, 0)
                self.assertEqual(metadata["invocations"][0]["listedNames"], ["BenchmarkC", "TestA", "TestB"])
                # The listing is taken before the tests are launched, from the same binary.
                listing = next(i for i, call in enumerate(fake.calls) if call[1:] == ["-test.paniconexit0", "-test.list=."])
                launch = next(i for i, call in enumerate(fake.calls) if call[1:3] == ["tool", "test2json"])
                self.assertLess(listing, launch)
                self.assertEqual(fake.calls[listing][0], fake.calls[launch][6])

    def test_test_process_exit_code_becomes_the_shard_exit_code(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                code, metadata, _ = self.run_shard(target, adjust=lambda fake: setattr(fake, "test_code", 23))
                self.assertEqual(code, 23)
                self.assertEqual(metadata["invocations"][0]["exitCode"], 23)

    def test_checkout_at_another_commit_is_refused(self):
        self.refused(RuntimeError, "repository SHA mismatch", adjust=lambda fake: setattr(fake, "head", OTHER_SHA))

    def test_workflow_commit_that_differs_from_the_checkout_is_refused(self):
        self.refused(RuntimeError, "repository SHA mismatch", expected_sha=OTHER_SHA)

    def test_plan_for_another_commit_is_refused(self):
        self.refused(
            RuntimeError, "repository SHA mismatch", plan_change=lambda plan: plan.__setitem__("repositorySha", OTHER_SHA)
        )

    def test_plan_for_another_toolchain_is_refused(self):
        self.refused(
            RuntimeError, "Go toolchain mismatch", plan_change=lambda plan: plan.__setitem__("goVersion", "go version go1.0 x/y")
        )

    def test_binary_whose_hash_is_not_the_plans_is_refused(self):
        self.refused(RuntimeError, "binary hash mismatch", tamper=True)

    def test_command_line_over_the_budget_is_refused(self):
        def long_expression(plan):
            plan["settings"]["commandLineLimit"] = 1024
            plan["shards"][0]["assignments"][0]["runRegex"] = "^(" + "|".join(f"TestRoot{i:04d}" for i in range(200)) + ")$"

        self.refused(RuntimeError, "refusing implicit batching", plan_change=long_expression)

    def test_listing_that_fails_is_refused(self):
        self.refused(RuntimeError, "listing the compiled roots", adjust=lambda fake: setattr(fake, "list_code", 1))

    def test_listing_that_writes_to_stderr_is_refused(self):
        self.refused(RuntimeError, "listing the compiled roots", adjust=lambda fake: setattr(fake, "list_stderr", b"x"))

    def test_listing_that_repeats_a_root_is_refused(self):
        self.refused(
            RuntimeError, "lists a root more than once", adjust=lambda fake: setattr(fake, "listing", b"TestA\nTestA\n")
        )

    def test_plan_that_shuffles_is_refused(self):
        self.refused(
            ValueError, "shuffle=off", plan_change=lambda plan: plan["settings"].__setitem__("shuffle", "on")
        )


# --------------------------------------------------------------------------
# run_other.py
# --------------------------------------------------------------------------
class FakeOtherProcesses:
    def __init__(self, workspace):
        self.w = workspace
        self.head = SHA
        self.test_code = 0
        self.listed = "example/heavy\nexample/other\nexample/third\n"
        self.test_calls = []

    def __call__(self, argv, **kwargs):
        if argv[0] == "git" and "rev-parse" in argv:
            return subprocess.CompletedProcess(argv, 0, self.head + "\n", "")
        if argv[0] == "git" and "status" in argv:
            return subprocess.CompletedProcess(argv, 0, "", "")
        if argv[1:] == ["version"]:
            return subprocess.CompletedProcess(argv, 0, go_version(self.w.target) + "\n", "")
        if argv[1:3] == ["env", "-json"]:
            return subprocess.CompletedProcess(argv, 0, self.w.go_environment(), "")
        if argv[1:] == ["list", "./..."]:
            return subprocess.CompletedProcess(argv, 0, self.listed, "")
        if argv[1:] == ["clean", "-testcache"]:
            return subprocess.CompletedProcess(argv, 0, b"", b"")
        if argv[1:3] == ["test", "-json"]:
            self.test_calls.append(list(argv))
            kwargs["stdout"].write(b'{"Action":"start","Package":"example/other"}\n')
            return subprocess.CompletedProcess(argv, self.test_code)
        raise AssertionError(f"unexpected subprocess.run call: {argv!r}")


class NonHeavyGuaranteeTests(unittest.TestCase):
    def run_other(self, target, adjust=None, expected_sha=SHA, limit=None, packages=("example/other", "example/third")):
        w = Workspace(self, target)
        bundle = w.root / "bundle"
        bundle.mkdir()
        plan = {
            "schema": run_other.PLAN_SCHEMA,
            "targetEnvironment": target.environment,
            "repositorySha": SHA,
            "goVersion": go_version(target),
            "settings": {
                "shuffle": "off", "timeout": "30m", "commandLineLimit": limit or w.limit,
                "testParallel": None, "goMaxProcs": None,
            },
            "packages": [{"importPath": "example/heavy"}],
        }
        inventory = {
            "schema": run_other.PACKAGE_SCHEMA,
            "repositorySha": SHA,
            "goVersion": go_version(target),
            "goos": target.goos,
            "goarch": target.goarch,
            "cgoEnabled": "1",
            "packages": [{"importPath": "example/heavy", "heavy": True}]
            + [{"importPath": name, "heavy": False} for name in packages],
        }
        (bundle / "plan.json").write_text(json.dumps(plan), encoding="utf-8")
        (bundle / "package-inventory.json").write_text(json.dumps(inventory), encoding="utf-8")
        fake = FakeOtherProcesses(w)
        if adjust:
            adjust(fake)
        arguments = run_other.parse_args(
            [
                "--repository", str(w.repository), "--bundle", str(bundle), "--output", str(w.root / "out"),
                "--expected-repository-sha", expected_sha, "--target", target.name,
            ]
        )
        for patch in w.patches(run_other, fake):
            patch.start()
            self.addCleanup(patch.stop)
        metadata = {}
        code = run_other.run(arguments, metadata)
        return code, metadata, fake

    def test_every_non_heavy_package_is_launched_once_with_go_test_semantics(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                code, metadata, fake = self.run_other(target)
                self.assertEqual(code, 0)
                (call,) = fake.test_calls
                self.assertEqual(
                    call[1:], ["test", "-json", "-vet=off", "-timeout=30m", "-shuffle=off", "example/other", "example/third"]
                )
                self.assertEqual(metadata["testExitCode"], 0)
                self.assertEqual(metadata["expectedPackages"], ["example/other", "example/third"])

    def test_failing_tests_fail_the_run(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                code, metadata, _ = self.run_other(target, adjust=lambda fake: setattr(fake, "test_code", 1))
                self.assertEqual(code, 1)
                self.assertEqual(metadata["testExitCode"], 1)

    def test_job_records_its_own_package_listing(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                _, metadata, _ = self.run_other(
                    target, adjust=lambda fake: setattr(fake, "listed", "example/other\nexample/new\nexample/heavy\n")
                )
                self.assertEqual(metadata["listedPackages"], ["example/heavy", "example/new", "example/other"])

    def test_command_line_over_the_budget_is_refused(self):
        many = tuple(f"example/a-rather-long-package-path-number-{index:03d}" for index in range(40))
        for target in TARGETS:
            with self.subTest(target=target.name), self.assertRaisesRegex(RuntimeError, "refusing implicit package batching"):
                self.run_other(target, limit=1024, packages=many)

    def test_checkout_or_workflow_commit_that_differs_is_refused(self):
        for target in TARGETS:
            with self.subTest(target=target.name):
                with self.assertRaisesRegex(RuntimeError, "repository SHA differs"):
                    self.run_other(target, adjust=lambda fake: setattr(fake, "head", OTHER_SHA))
                with self.assertRaisesRegex(RuntimeError, "repository SHA differs"):
                    self.run_other(target, expected_sha=OTHER_SHA)


if __name__ == "__main__":
    unittest.main()
