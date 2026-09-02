import json
import re
import unittest
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[4]
WORKFLOW = REPOSITORY / ".github" / "workflows" / "test.yml"
SETTINGS = Path(__file__).resolve().parents[1] / "settings.json"


class WorkflowContractTests(unittest.TestCase):
    def test_windows_monolith_is_replaced_but_native_build_remains(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        test_block = workflow.split("  test:\n", 1)[1].split("  windows-test-prepare:\n", 1)[0]
        build_block = workflow.split("  build:\n", 1)[1].split("  test:\n", 1)[0]
        self.assertIn("os: [ubuntu-latest, macos-latest]", test_block)
        self.assertNotIn("windows-latest", test_block)
        self.assertIn("os: [ubuntu-latest, macos-latest, windows-latest]", build_block)

    def test_dynamic_matrix_and_stable_aggregate_check_are_present(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        settings = json.loads(SETTINGS.read_text(encoding="utf-8"))
        self.assertEqual(settings["shardCount"], 8)
        self.assertIn("fromJSON(needs.windows-test-prepare.outputs.shard-matrix)", workflow)
        self.assertIn("name: test (windows-latest)", workflow)
        self.assertIn("windows-test-verify.result", workflow)

    def test_workflow_uses_only_ephemeral_hosted_runners_and_no_baseline(self):
        workflow = WORKFLOW.read_text(encoding="utf-8").lower()
        self.assertNotIn("self-hosted", workflow)
        self.assertNotIn("monolithic-baseline", workflow)
        self.assertNotIn("baseline-events", workflow)
        self.assertIn("permissions:\n  contents: read", workflow)
        self.assertNotIn("-test.bench", workflow)

    def test_shards_restore_cache_budget_and_verifier_runs_after_prepare(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        shard_block = workflow.split("  windows-test-shards:\n", 1)[1].split(
            "  windows-test-other:\n", 1
        )[0]
        verify_block = workflow.split("  windows-test-verify:\n", 1)[1].split(
            "  windows-test:\n", 1
        )[0]
        self.assertIn("timeout-minutes: 105", shard_block)
        self.assertIn("cache-dependency-path: go.sum", shard_block)
        self.assertNotIn("cache: false", shard_block)
        self.assertIn("needs.windows-test-prepare.result == 'success'", verify_block)
        self.assertNotIn("needs.windows-test-shards.result == 'success'", verify_block)

    def test_every_workflow_python_command_disables_bytecode_writes(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        commands = [
            line.strip()
            for line in workflow.splitlines()
            if re.search(r"(?:^|[& ])python(?: |$)", line)
        ]
        self.assertTrue(commands)
        for command in commands:
            self.assertRegex(command, r"(?:^|[& ])python -B(?: |$)")


if __name__ == "__main__":
    unittest.main()
