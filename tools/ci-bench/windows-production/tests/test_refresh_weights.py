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
import refresh_weights  # noqa: E402
import targets  # noqa: E402


COMMIT = "c" * 40


def write_shard(
    root, index, events, target=targets.DARWIN_ARM64, shard_count=2, processes=(("p", 10.0),), **overrides
):
    directory = root / f"{target.goos}-test-shard-{index}"
    directory.mkdir(parents=True)
    metadata = {
        "schema": refresh_weights.SHARD_RUN_SCHEMA,
        "exitCode": 0,
        "errors": [],
        "repositorySha": COMMIT,
        "targetEnvironment": target.environment,
        "shardIndex": index,
        "shardCount": shard_count,
        "invocations": [
            {"package": package, "durationSeconds": seconds} for package, seconds in processes
        ],
    }
    metadata.update(overrides)
    (directory / "shard-metadata.json").write_text(json.dumps(metadata), encoding="utf-8")
    (directory / "shard-events.jsonl").write_text(
        "".join(json.dumps(event) + "\n" for event in events), encoding="utf-8"
    )
    return directory


def terminal(package, test, seconds, action="pass"):
    return {"Action": action, "Package": package, "Test": test, "Elapsed": seconds}


class RefreshWeightsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.evidence = self.root / "evidence"
        self.output = self.root / "weights.json"

    def run_script(self, *extra):
        arguments = [
            "--evidence", str(self.evidence),
            "--output", str(self.output),
            "--target", "darwin/arm64",
            *extra,
        ]
        with redirect_stdout(StringIO()), redirect_stderr(StringIO()) as stderr:
            code = refresh_weights.main(arguments)
        return code, stderr.getvalue()

    def complete_evidence(self):
        write_shard(
            self.evidence,
            0,
            [
                {"Action": "start", "Package": "example/heavy"},
                {"Action": "run", "Package": "example/heavy", "Test": "TestSlow"},
                terminal("example/heavy", "TestSlow/sub", 99.0),
                terminal("example/heavy", "TestSlow", 12.345),
                terminal("example/heavy", "TestQuick", 0.02),
                {"Action": "pass", "Package": "example/heavy", "Elapsed": 13.0},
            ],
        )
        write_shard(
            self.evidence,
            1,
            [
                terminal("example/heavy", "ExampleThing", 0.5, action="skip"),
                terminal("example/other", "TestSlow", 3.0),
            ],
        )

    def test_weights_are_top_level_elapsed_at_or_above_the_threshold(self):
        self.complete_evidence()
        code, stderr = self.run_script("--run", "123")
        self.assertEqual(code, 0, stderr)
        weights, metadata = plan_shards.load_weights(self.output)
        self.assertEqual(
            dict(weights),
            {
                ("example/heavy", "TestSlow"): 12.35,
                ("example/heavy", "ExampleThing"): 0.5,
                ("example/other", "TestSlow"): 3.0,
            },
        )
        self.assertEqual(metadata["sourceCommit"], COMMIT)
        self.assertIn("CI run 123", metadata["source"])
        self.assertIn("darwin/arm64", metadata["source"])

    def test_parallel_roots_are_flagged_and_parallelism_is_fitted_to_process_times(self):
        # Shard 0: 10 s serial, then 90 s of parallel roots (longest 30) in a 40 s
        # process: 30 s for the parallel phase, so three ran at a time. Shard 1: one
        # parallel root alone takes its own elapsed whatever the parallelism.
        write_shard(
            self.evidence,
            0,
            [
                terminal("p", "TestSerial", 10.0),
                {"Action": "pause", "Package": "p", "Test": "TestParallelA"},
                {"Action": "cont", "Package": "p", "Test": "TestParallelA"},
                terminal("p", "TestParallelA", 30.0),
                {"Action": "pause", "Package": "p", "Test": "TestParallelB"},
                terminal("p", "TestParallelB", 30.0),
                {"Action": "pause", "Package": "p", "Test": "TestParallelD"},
                terminal("p", "TestParallelD", 30.0),
                {"Action": "pause", "Package": "p", "Test": "TestSerial/sub"},
            ],
            processes=(("p", 40.0), ("q", 5.0)),
        )
        write_shard(
            self.evidence,
            1,
            [
                {"Action": "pause", "Package": "p", "Test": "TestParallelC"},
                terminal("p", "TestParallelC", 15.0),
            ],
            processes=(("p", 15.0),),
        )
        code, stderr = self.run_script()
        self.assertEqual(code, 0, stderr)
        weights, metadata = plan_shards.load_weights(self.output)
        # Elapsed is recorded as measured; the planner applies the overlap.
        self.assertEqual(
            dict(weights),
            {
                ("p", "TestSerial"): 10.0,
                ("p", "TestParallelA"): 30.0,
                ("p", "TestParallelB"): 30.0,
                ("p", "TestParallelD"): 30.0,
                ("p", "TestParallelC"): 15.0,
            },
        )
        self.assertEqual(
            weights.parallel, {("p", f"TestParallel{letter}") for letter in "ABCD"}
        )
        self.assertEqual(weights.parallelism, 3.0)
        self.assertEqual(metadata["parallelism"], 3.0)
        self.assertEqual(
            plan_shards.process_seconds(10.0, 90.0, 30.0, weights.parallelism), 40.0
        )

    def test_evidence_without_parallel_roots_plans_serially(self):
        write_shard(self.evidence, 0, [terminal("p", "TestA", 4.0)], shard_count=1, processes=(("p", 60.0),))
        self.assertEqual(self.run_script()[0], 0)
        weights, _ = plan_shards.load_weights(self.output)
        self.assertEqual(dict(weights), {("p", "TestA"): 4.0})
        self.assertEqual(weights.parallel, set())
        self.assertEqual(weights.parallelism, 1.0)

    def test_output_is_deterministic_and_one_row_per_line(self):
        self.complete_evidence()
        self.assertEqual(self.run_script("--minimum-seconds", "1")[0], 0)
        first = self.output.read_text(encoding="utf-8")
        self.assertEqual(self.run_script("--minimum-seconds", "1")[0], 0)
        self.assertEqual(self.output.read_text(encoding="utf-8"), first)
        rows = [line for line in first.splitlines() if line.startswith("    {")]
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows, sorted(rows))

    def test_incomplete_failed_or_mixed_evidence_is_refused(self):
        cases = {
            "missing shard": lambda: write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)]),
            "failed shard": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)], exitCode=1),
                write_shard(self.evidence, 1, [terminal("p", "TestB", 1.0)]),
            ),
            "failed root": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0, action="fail")]),
                write_shard(self.evidence, 1, [terminal("p", "TestB", 1.0)]),
            ),
            "two commits": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)]),
                write_shard(self.evidence, 1, [terminal("p", "TestB", 1.0)], repositorySha="d" * 40),
            ),
            "duplicate root": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)]),
                write_shard(self.evidence, 1, [terminal("p", "TestA", 1.0)]),
            ),
            "another target": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)], target=targets.WINDOWS_AMD64),
                write_shard(self.evidence, 1, [terminal("p", "TestB", 1.0)], target=targets.WINDOWS_AMD64),
            ),
            "no process duration": lambda: (
                write_shard(self.evidence, 0, [terminal("p", "TestA", 1.0)], processes=()),
                write_shard(self.evidence, 1, [terminal("p", "TestB", 1.0)]),
            ),
            "no evidence": lambda: self.evidence.mkdir(),
        }
        for label, build in cases.items():
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                self.evidence = Path(directory) / "evidence"
                self.output = Path(directory) / "weights.json"
                build()
                code, stderr = self.run_script()
                self.assertEqual(code, 2)
                self.assertIn("error:", stderr)
                self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
