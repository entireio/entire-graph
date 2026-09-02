import json
from pathlib import Path
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[3]
VERIFY = REPO / "tools" / "ci-bench" / "a4" / "verify-test-events.py"


def write_events(path, values):
    path.write_text("".join(json.dumps(value) + "\n" for value in values), encoding="utf-8")


class VerifyA4TestEventsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def run_verify(self, *arguments):
        return subprocess.run(
            ["python3", str(VERIFY), *map(str, arguments)],
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def test_dynamic_multiset_is_order_independent_and_fully_qualified(self):
        baseline = self.root / "baseline.jsonl"
        shard1 = self.root / "shard1.jsonl"
        shard2 = self.root / "shard2.jsonl"
        output = self.root / "proof.json"
        first = {"Package": "p", "Test": "TestA", "Action": "run"}
        second = {"Package": "p", "Test": "TestA", "Action": "pass", "Elapsed": 1}
        other = {"Package": "q", "Test": "TestA", "Action": "skip"}
        write_events(baseline, [first, second, other])
        write_events(shard1, [other])
        write_events(shard2, [second, first])
        completed = self.run_verify(
            "compare-events",
            "--baseline",
            baseline,
            "--candidate",
            shard1,
            "--candidate",
            shard2,
            "--output",
            output,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertTrue(json.loads(output.read_text())["exactDynamicEventMultiset"])

    def test_dynamic_multiset_rejects_one_missing_run_event(self):
        baseline = self.root / "baseline.jsonl"
        candidate = self.root / "candidate.jsonl"
        output = self.root / "proof.json"
        write_events(
            baseline,
            [
                {"Package": "p", "Test": "TestA", "Action": "run"},
                {"Package": "p", "Test": "TestA", "Action": "pass"},
            ],
        )
        write_events(candidate, [{"Package": "p", "Test": "TestA", "Action": "pass"}])
        completed = self.run_verify(
            "compare-events",
            "--baseline",
            baseline,
            "--candidate",
            candidate,
            "--output",
            output,
        )
        self.assertEqual(completed.returncode, 1)
        self.assertEqual(json.loads(output.read_text())["differenceCount"], 1)

    def test_list_multiset_rejects_duplicate_assignment(self):
        baseline = self.root / "baseline.txt"
        shard1 = self.root / "shard1.txt"
        shard2 = self.root / "shard2.txt"
        output = self.root / "proof.json"
        baseline.write_text("TestA\nTestB\n")
        shard1.write_text("TestA\nTestB\n")
        shard2.write_text("TestB\n")
        completed = self.run_verify(
            "compare-lists",
            "--baseline",
            baseline,
            "--candidate",
            shard1,
            "--candidate",
            shard2,
            "--output",
            output,
        )
        self.assertEqual(completed.returncode, 1)
        self.assertEqual(json.loads(output.read_text())["candidateCount"], 3)

    def test_weights_use_top_level_terminal_elapsed_only(self):
        source = self.root / "events.jsonl"
        output = self.root / "weights.json"
        write_events(
            source,
            [
                {"Package": "p", "Test": "TestA", "Action": "run"},
                {"Package": "p", "Test": "TestA/sub", "Action": "pass", "Elapsed": 2},
                {"Package": "p", "Test": "TestA", "Action": "pass", "Elapsed": 3},
                {"Package": "q", "Test": "TestOther", "Action": "pass", "Elapsed": 4},
            ],
        )
        completed = self.run_verify(
            "weights", "--input", source, "--package", "p", "--output", output
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(json.loads(output.read_text())["tests"], {"TestA": 3.0})


if __name__ == "__main__":
    unittest.main()
