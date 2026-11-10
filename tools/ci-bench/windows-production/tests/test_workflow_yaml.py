import json
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import workflow_yaml  # noqa: E402


REPOSITORY = Path(__file__).resolve().parents[4]
WORKFLOW = REPOSITORY / ".github" / "workflows" / "test.yml"


class WorkflowYamlTests(unittest.TestCase):
    def test_real_workflow_parses_into_the_expected_shape(self):
        workflow = workflow_yaml.load(WORKFLOW.read_text(encoding="utf-8"))
        self.assertEqual(workflow["name"], "test")
        # Keys are never typed, so `on` stays a string.
        self.assertEqual(sorted(workflow["on"]), ["pull_request", "push", "workflow_dispatch"])
        self.assertIsNone(workflow["on"]["pull_request"])
        self.assertEqual(workflow["on"]["push"], {"branches": ["main"]})
        jobs = workflow["jobs"]
        self.assertEqual(
            jobs["build"]["strategy"]["matrix"]["os"], ["ubuntu-latest", "macos-latest", "windows-latest"]
        )
        self.assertEqual(jobs["lint"]["steps"][1]["with"], {"go-version": "1.27.x"})
        self.assertIs(jobs["macos-test-shards"]["strategy"]["fail-fast"], False)
        self.assertIsInstance(jobs["macos-test-shards"]["timeout-minutes"], int)
        script = jobs["macos-test"]["steps"][-1]["run"]
        self.assertTrue(script.startswith("for result in "))
        self.assertTrue(script.endswith("done\n"))
        self.assertNotIn("#", jobs["macos-test-shards"]["steps"][3]["uses"])

    @unittest.skipUnless(shutil.which("ruby"), "needs Ruby's YAML as the reference parser")
    def test_real_workflow_parses_exactly_as_a_full_yaml_parser_does(self):
        completed = subprocess.run(
            ["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(WORKFLOW)],
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
        if completed.returncode != 0:
            self.skipTest(f"reference parser unavailable: {completed.stderr.strip()[:200]}")

        def with_string_keys(value):
            # YAML 1.1 reads the bare key `on` as the boolean true.
            if isinstance(value, dict):
                return {("on" if key == "true" else key): with_string_keys(item) for key, item in value.items()}
            if isinstance(value, list):
                return [with_string_keys(item) for item in value]
            return value

        self.assertEqual(
            workflow_yaml.load(WORKFLOW.read_text(encoding="utf-8")),
            with_string_keys(json.loads(completed.stdout)),
        )

    def test_supported_subset(self):
        text = (
            "# comment\n"
            "top:\n"
            "  plain: some value # trailing comment\n"
            "  quoted: \"1.27.x\"\n"
            "  single: 'it''s'\n"
            "  number: 30\n"
            "  truth: false\n"
            "  nothing:\n"
            "  expression: ${{ always() && needs.a.result == 'success' }}\n"
            "  flow: [a, \"b, c\", 3]\n"
            "  map: { key: value, other: \"x\" }\n"
            "  below:\n"
            "    [x, z]\n"
            "  block: |\n"
            "    first # not a comment\n"
            "\n"
            "      indented\n"
            "  items:\n"
            "    - name: one\n"
            "      run: |\n"
            "        echo one\n"
            "    - plain item\n"
            "  compact:\n"
            "  - a\n"
            "  - b\n"
        )
        self.assertEqual(
            workflow_yaml.load(text),
            {
                "top": {
                    "plain": "some value",
                    "quoted": "1.27.x",
                    "single": "it's",
                    "number": 30,
                    "truth": False,
                    "nothing": None,
                    "expression": "${{ always() && needs.a.result == 'success' }}",
                    "flow": ["a", "b, c", 3],
                    "map": {"key": "value", "other": "x"},
                    "below": ["x", "z"],
                    "block": "first # not a comment\n\n  indented\n",
                    "items": [{"name": "one", "run": "echo one\n"}, "plain item"],
                    "compact": ["a", "b"],
                }
            },
        )

    def test_everything_outside_the_subset_is_refused_with_its_line(self):
        cases = {
            "tab": "a:\n\tb: 1\n",
            "anchor": "a: &x 1\n",
            "alias": "a: *x\n",
            "tag": "a: !!str 1\n",
            "merge key": "a:\n  <<: b\n",
            "document marker": "---\na: 1\n",
            "folded scalar": "a: >\n  text\n",
            "chomping indicator": "a: |-\n  text\n",
            "multi-line plain scalar": "a:\n  first\n  second\n",
            "continued plain scalar": "a: first\n  second\n",
            "multi-line flow": "a: [x,\n  y]\n",
            "nested flow": "a: [[x]]\n",
            "duplicate key": "a: 1\na: 2\n",
            "duplicate key in a step": "a:\n  - b: 1\n    b: 2\n",
            "quoted key": "\"a\": 1\n",
            "complex key": "? a\n: 1\n",
            "float": "a: 1.5\n",
            "ambiguous boolean": "a: yes\n",
            "escape": "a: \"x\\n\"\n",
            "unterminated quote": "a: \"x\n",
            "colon in plain scalar": "a: b: c\n",
            "nested sequence": "a:\n  - - x\n",
            "bad indentation": "a:\n    b: 1\n  c: 2\n",
            "empty document": "# nothing\n",
        }
        for label, text in cases.items():
            with self.subTest(construct=label):
                with self.assertRaises(workflow_yaml.UnsupportedYaml) as raised:
                    workflow_yaml.load(text)
                if label != "empty document":
                    self.assertRegex(str(raised.exception), r"^line \d+: ")


if __name__ == "__main__":
    unittest.main()
