import hashlib
import importlib.util
import json
from pathlib import Path
import unittest


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
SUMMARY = HERE / "vm-statusline-observations.json"
spec = importlib.util.spec_from_file_location(
    "vm_statusline_observations", HERE / "vm_statusline_observations.py"
)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class VMStatuslineObservationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.record = json.loads(SUMMARY.read_text())

    def test_summary_reprojects_all_thirteen_checkpointed_logs(self) -> None:
        self.assertEqual(module.generate(REPO, self.record["source_revision"]), self.record)
        self.assertEqual(self.record["observation_count"], 13)
        states = [
            item["facts"]["state"]
            for item in self.record["observations"]
            if item["kind"] == "collector-terminal-message"
        ]
        self.assertEqual(states.count("request-timeout"), 7)
        self.assertEqual(states.count("configuration-placeholder-refusal"), 1)

    def test_failed_statusline_attempt_and_render_equivalence_are_explicit(self) -> None:
        failed = next(
            item for item in self.record["observations"] if item["kind"] == "statusline-test-failure"
        )
        self.assertEqual((failed["facts"]["passed"], failed["facts"]["failed"]), (62, 78))
        self.assertTrue(failed["facts"]["terminal_error"])
        renders = [
            item["facts"]
            for item in self.record["observations"]
            if item["kind"] == "statusline-render"
        ]
        self.assertEqual(len(renders), 2)
        self.assertEqual(renders[0]["rendered_sha256"], renders[1]["rendered_sha256"])

    def test_removed_sources_are_hash_bound_without_raw_environment(self) -> None:
        removal = json.loads((HERE / "removal-map.json").read_text())
        candidates = {item["path"]: item for item in removal["candidates"]}
        retained_sha = hashlib.sha256(SUMMARY.read_bytes()).hexdigest()
        for item in self.record["observations"]:
            self.assertFalse((REPO / item["source_path"]).exists())
            candidate = candidates[item["source_path"]]
            self.assertEqual(candidate["source_sha256"], item["source_sha256"])
            self.assertEqual(candidate["replacement"]["retained_sha256"], retained_sha)
        data = SUMMARY.read_bytes()
        for forbidden in (b"/Users/", b"/home/", b"/subscriptions/", b".blob.core.windows.net"):
            self.assertNotIn(forbidden, data)


if __name__ == "__main__":
    unittest.main()
