import argparse
import hashlib
import io
import json
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import run_shard  # noqa: E402


REPOSITORY_SHA = "a" * 40
GO_VERSION = "go version go1.26.0 windows/amd64"
PACKAGES = ["example/heavy", 'example/quo"te']
STDOUT_SENTINEL = b'{"Action":"output","Output":"CHILD-STDOUT-SENTINEL \\u00e9\\n"}\r\n\x00tail'
STDERR_SENTINEL = b"CHILD-STDERR-SENTINEL \xff\xfe raw bytes\r\n"
CONSOLE_MARKERS = ("heartbeat", "assignment ", "elapsed=")


class Console(io.StringIO):
    """Thread-safe capture of the runner's own stderr that signals heartbeats."""

    def __init__(self):
        super().__init__()
        self.lock = threading.Lock()
        self.heartbeat_seen = threading.Event()

    def write(self, text):
        with self.lock:
            if " heartbeat " in text:
                self.heartbeat_seen.set()
            return super().write(text)


def heartbeat_threads():
    return [
        thread
        for thread in threading.enumerate()
        if thread.name.startswith("run-shard-heartbeat")
    ]


class JoinRecordingThread(threading.Thread):
    started_threads = []

    def start(self):
        JoinRecordingThread.started_threads.append(self)
        self.join_calls = 0
        super().start()

    def join(self, timeout=None):
        self.join_calls += 1
        super().join(timeout)


class FakeGo:
    """Stands in for every subprocess.run call made by run_shard.run."""

    def __init__(self, go, tool_directory, child):
        self.go = go
        self.tool_directory = tool_directory
        self.child = child
        self.test_calls = []

    def __call__(self, argv, **kwargs):
        if argv[:1] == ["git"] and "rev-parse" in argv:
            return subprocess.CompletedProcess(argv, 0, REPOSITORY_SHA + "\n", "")
        if argv[:1] == ["git"] and "status" in argv:
            return subprocess.CompletedProcess(argv, 0, "", "")
        if argv == [self.go, "version"]:
            return subprocess.CompletedProcess(argv, 0, GO_VERSION + "\n", "")
        if argv[:3] == [self.go, "env", "-json"]:
            value = dict(run_shard.TARGET_ENVIRONMENT)
            value["GOTOOLDIR"] = str(self.tool_directory)
            return subprocess.CompletedProcess(argv, 0, json.dumps(value), "")
        if argv == [self.go, "clean", "-testcache"]:
            return subprocess.CompletedProcess(argv, 0, b"", b"")
        if argv[:3] == [self.go, "tool", "test2json"]:
            self.test_calls.append((list(argv), dict(kwargs)))
            return self.child(argv, kwargs, len(self.test_calls) - 1)
        raise AssertionError(f"unexpected subprocess.run call: {argv!r}")


def child_writing_sentinels(returncodes):
    def child(argv, kwargs, index):
        kwargs["stdout"].write(STDOUT_SENTINEL)
        kwargs["stderr"].write(STDERR_SENTINEL)
        return subprocess.CompletedProcess(argv, returncodes[index])

    return child


class RunShardProgressTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name).resolve()
        self.repository = root / "repo"
        self.bundle = root / "bundle"
        self.output = root / "out"
        self.tool_directory = root / "gotool"
        self.tool_directory.mkdir()
        go = root / "bin" / "go.exe"
        go.parent.mkdir()
        go.write_bytes(b"fake go")
        self.go = str(go.resolve())
        (self.bundle / "binaries").mkdir(parents=True)
        assignments = []
        for position, package in enumerate(PACKAGES):
            relative = f"pkg{position}"
            (self.repository / relative).mkdir(parents=True)
            binary = self.bundle / "binaries" / f"pkg{position}.test.exe"
            binary.write_bytes(f"binary {position}".encode())
            assignments.append(
                {
                    "importPath": package,
                    "binaryName": binary.name,
                    "binarySha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                    "runRegex": "^(TestA|TestB)$",
                    "roots": ["TestA", "TestB", "TestC"][: position + 2],
                    "packageDirectoryRelative": relative,
                }
            )
        plan = {
            "schema": run_shard.PLAN_SCHEMA,
            "targetEnvironment": dict(run_shard.TARGET_ENVIRONMENT),
            "shardCount": 3,
            "repositorySha": REPOSITORY_SHA,
            "goVersion": GO_VERSION,
            "settings": {
                "shuffle": "off",
                "timeout": "25m",
                "commandLineLimit": 32767,
                "testParallel": 4,
                "goMaxProcs": 2,
            },
            "shards": [{"index": 1, "assignments": assignments}],
        }
        (self.bundle / "plan.json").write_text(json.dumps(plan), encoding="utf-8")
        self.assignments = assignments
        self.argv = [
            "--repository", str(self.repository),
            "--bundle", str(self.bundle),
            "--output", str(self.output),
            "--shard-index", "1",
            "--expected-repository-sha", REPOSITORY_SHA,
        ]

    def patched(self, fake):
        stack = [
            mock.patch.object(run_shard.sys, "platform", "win32"),
            mock.patch.object(run_shard.shutil, "which", return_value=self.go),
            mock.patch.object(run_shard.subprocess, "run", side_effect=fake),
        ]
        for patcher in stack:
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_direct(self, child, interval=60.0):
        fake = FakeGo(self.go, self.tool_directory, child)
        self.patched(fake)
        console = Console()
        metadata = {"invocations": []}
        args = run_shard.parse_args(self.argv)
        with redirect_stderr(console), redirect_stdout(io.StringIO()):
            exit_code = run_shard.run(args, metadata, heartbeat_interval=interval)
        return fake, console.getvalue(), metadata, exit_code

    def artifact_files(self):
        return sorted(path for path in self.output.rglob("*") if path.is_file())

    def assert_no_console_markers_in_artifacts(self):
        files = self.artifact_files()
        self.assertTrue(files)
        for path in files:
            content = path.read_bytes()
            for marker in CONSOLE_MARKERS:
                self.assertNotIn(marker.encode(), content, f"{marker!r} leaked into {path.name}")

    def test_start_and_end_lines_identify_each_assignment_on_the_console(self):
        _, console, _, exit_code = self.run_direct(child_writing_sentinels([0, 7]))
        self.assertEqual(exit_code, 7)
        lines = [line for line in console.splitlines() if " assignment " in line]
        starts = [line for line in lines if " start " in line]
        ends = [line for line in lines if " end " in line]
        self.assertEqual(len(starts), 2)
        self.assertEqual(len(ends), 2)
        for ordinal, (package, start, end) in enumerate(zip(PACKAGES, starts, ends), 1):
            label = f"shard 1 assignment {ordinal}/2"
            roots = len(self.assignments[ordinal - 1]["roots"])
            for line in (start, end):
                self.assertIn(label, line)
                self.assertIn(f"package={json.dumps(package)}", line)
                self.assertIn(f"roots={roots}", line)
            self.assertRegex(end, r"elapsed=\d+\.\d{3}s")
        self.assertIn("exit=0", ends[0])
        self.assertIn("exit=7", ends[1])
        self.assertLess(console.index(starts[0]), console.index(ends[0]))
        self.assertLess(console.index(ends[0]), console.index(starts[1]))
        self.assertEqual(heartbeat_threads(), [])

    def test_heartbeat_reports_the_running_assignment_and_stops_afterwards(self):
        observed = []

        def child(argv, kwargs, index):
            observed.append(bool(heartbeat_threads()))
            self.assertTrue(console.heartbeat_seen.wait(5), "no heartbeat while child ran")
            kwargs["stdout"].write(STDOUT_SENTINEL)
            kwargs["stderr"].write(STDERR_SENTINEL)
            return subprocess.CompletedProcess(argv, 0)

        fake = FakeGo(self.go, self.tool_directory, child)
        self.patched(fake)
        console = Console()
        args = run_shard.parse_args(self.argv)
        with redirect_stderr(console):
            exit_code = run_shard.run(args, {"invocations": []}, heartbeat_interval=0.001)
        self.assertEqual(exit_code, 0)
        self.assertEqual(observed, [True, True])
        text = console.getvalue()
        beats = [line for line in text.splitlines() if " heartbeat " in line]
        self.assertTrue(beats)
        self.assertTrue(any("shard 1 assignment 1/2" in line for line in beats))
        for line in beats:
            self.assertRegex(line, r"^shard 1 assignment [12]/2 heartbeat package=\"[^\n]*\" roots=\d+ elapsed=\d+\.\d+s$")
        self.assertEqual(heartbeat_threads(), [])
        self.assert_no_console_markers_in_artifacts()

    def test_child_bytes_land_only_in_artifacts(self):
        fake, console, _, _ = self.run_direct(child_writing_sentinels([0, 0]))
        for position, package in enumerate(PACKAGES):
            stem = f"{position:02d}-{package.replace('/', '_').replace(chr(34), '_')}"
            self.assertEqual((self.output / f"{stem}.jsonl").read_bytes(), STDOUT_SENTINEL)
            self.assertEqual((self.output / f"{stem}.stderr.log").read_bytes(), STDERR_SENTINEL)
        self.assertEqual(
            (self.output / "shard-events.jsonl").read_bytes(),
            (STDOUT_SENTINEL + b"\n") * 2,
        )
        self.assertNotIn("SENTINEL", console)
        self.assertEqual(len(fake.test_calls), 2)
        self.assert_no_console_markers_in_artifacts()

    def test_child_invocation_arguments_are_unchanged(self):
        fake, _, _, _ = self.run_direct(child_writing_sentinels([0, 0]))
        environment = dict(run_shard.os.environ)
        environment.update({"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "1", "GOMAXPROCS": "2"})
        for position, (argv, kwargs) in enumerate(fake.test_calls):
            assignment = self.assignments[position]
            package_directory = self.repository / assignment["packageDirectoryRelative"]
            binary = (self.bundle / "binaries" / assignment["binaryName"]).resolve()
            expected_argv = [
                self.go, "tool", "test2json", "-t", "-p", assignment["importPath"], str(binary),
                "-test.paniconexit0", "-test.timeout=25m", "-test.run=^(TestA|TestB)$",
                "-test.shuffle=off", "-test.parallel=4", "-test.v=test2json",
            ]
            self.assertEqual(argv, expected_argv)
            self.assertEqual(sorted(kwargs), ["check", "cwd", "env", "stderr", "stdout"])
            self.assertIs(kwargs["check"], False)
            self.assertEqual(kwargs["cwd"], package_directory)
            self.assertEqual(
                kwargs["env"],
                run_shard.direct_test_environment(environment, package_directory, self.tool_directory),
            )
            stem = f"{position:02d}-{assignment['importPath'].replace('/', '_').replace(chr(34), '_')}"
            self.assertEqual(Path(kwargs["stdout"].name), self.output / f"{stem}.jsonl")
            self.assertEqual(Path(kwargs["stderr"].name), self.output / f"{stem}.stderr.log")
            self.assertEqual(kwargs["stdout"].mode, "wb")
            self.assertEqual(kwargs["stderr"].mode, "wb")
            self.assertTrue(kwargs["stdout"].closed)
            self.assertTrue(kwargs["stderr"].closed)

    def test_nonzero_child_exit_propagates_with_unchanged_metadata(self):
        fake = FakeGo(self.go, self.tool_directory, child_writing_sentinels([23, 0]))
        self.patched(fake)
        console = Console()
        with redirect_stderr(console), redirect_stdout(io.StringIO()):
            exit_code = run_shard.main(self.argv)
        self.assertEqual(exit_code, 23)
        metadata = json.loads((self.output / "shard-metadata.json").read_text(encoding="utf-8"))
        self.assertEqual(metadata["exitCode"], 23)
        self.assertEqual(metadata["errors"], [])
        self.assertEqual([item["exitCode"] for item in metadata["invocations"]], [23, 0])
        self.assertEqual(
            sorted(metadata["invocations"][0]),
            sorted(
                [
                    "package", "binaryName", "binarySha256", "rootCount", "commandLineUtf16Units",
                    "durationSeconds", "exitCode", "paniconexit0", "workingDirectoryRelative",
                    "pwdMatchesPackageDirectory", "goToolDirectoryPrependedToPath",
                ]
            ),
        )
        self.assertNotIn("heartbeat", json.dumps(metadata))
        self.assertIn("exit=23", console.getvalue())
        self.assertEqual(heartbeat_threads(), [])
        self.assert_no_console_markers_in_artifacts()

    def test_heartbeat_thread_is_joined_when_child_raises(self):
        for error in (
            subprocess.TimeoutExpired(["go"], 1),
            RuntimeError("child launcher exploded"),
        ):
            with self.subTest(error=type(error).__name__):
                alive_during = []

                def child(argv, kwargs, index, error=error):
                    alive_during.append(len(heartbeat_threads()))
                    raise error

                fake = FakeGo(self.go, self.tool_directory, child)
                with mock.patch.object(run_shard.sys, "platform", "win32"), mock.patch.object(
                    run_shard.shutil, "which", return_value=self.go
                ), mock.patch.object(run_shard.subprocess, "run", side_effect=fake):
                    args = run_shard.parse_args(self.argv)
                    with redirect_stderr(Console()):
                        with self.assertRaises(type(error)):
                            run_shard.run(args, {"invocations": []}, heartbeat_interval=60.0)
                self.assertEqual(alive_during, [1])
                self.assertEqual(heartbeat_threads(), [])

    def test_heartbeat_thread_is_joined_after_nonzero_exit(self):
        alive_during = []

        def child(argv, kwargs, index):
            alive_during.append(len(heartbeat_threads()))
            return subprocess.CompletedProcess(argv, 5)

        _, _, _, exit_code = self.run_direct(child, interval=60.0)
        self.assertEqual(exit_code, 5)
        self.assertEqual(alive_during, [1, 1])
        self.assertEqual(heartbeat_threads(), [])

    def test_every_heartbeat_thread_is_explicitly_joined(self):
        def raising(argv, kwargs, index):
            raise subprocess.TimeoutExpired(argv, 1)

        def failing(argv, kwargs, index):
            return subprocess.CompletedProcess(argv, 5)

        for name, child, expected_starts in (("raises", raising, 1), ("nonzero", failing, 2)):
            with self.subTest(child=name):
                JoinRecordingThread.started_threads = []
                fake = FakeGo(self.go, self.tool_directory, child)
                with mock.patch.object(run_shard.sys, "platform", "win32"), mock.patch.object(
                    run_shard.shutil, "which", return_value=self.go
                ), mock.patch.object(run_shard.subprocess, "run", side_effect=fake), mock.patch.object(
                    run_shard.threading, "Thread", JoinRecordingThread
                ):
                    args = run_shard.parse_args(self.argv)
                    with redirect_stderr(Console()):
                        try:
                            run_shard.run(args, {"invocations": []}, heartbeat_interval=60.0)
                        except subprocess.TimeoutExpired:
                            pass
                started = JoinRecordingThread.started_threads
                self.assertEqual(len(started), expected_starts)
                for thread in started:
                    self.assertGreaterEqual(thread.join_calls, 1)
                    self.assertFalse(thread.is_alive())


if __name__ == "__main__":
    unittest.main()
