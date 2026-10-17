#!/usr/bin/env python3
"""Exercise the hook against the built Go CLI; ENTIRE_GRAPH_TEST_BIN is required."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("pc", HERE / "entire-graph-prompt-context.py")
pc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pc)


class RealRenderer(unittest.TestCase):
    def test_committed_source_survives_cleaning_and_injection(self):
        binary = Path(os.environ["ENTIRE_GRAPH_TEST_BIN"]).resolve(strict=True)
        with tempfile.TemporaryDirectory(prefix="graph-hook-integration-") as root:
            repo = Path(root) / "repo"
            repo.mkdir()
            env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
                       ENTIRE_GRAPH_PROMPT_CONTEXT="1", ENTIRE_GRAPH_BIN=str(binary),
                       CLAUDE_PROJECT_DIR=str(repo), XDG_CACHE_HOME=str(Path(root) / "cache"))
            def git(*args):
                return subprocess.run(["git", *args], cwd=repo, env=env, check=True,
                                      capture_output=True, text=True, timeout=20)
            git("init", "-q")
            git("config", "user.name", "Hook fixture")
            git("config", "user.email", "fixture@example.invalid")
            (repo / "go.mod").write_text("module hookfixture\n\ngo 1.27\n")
            source = ('package fixture\n\nfunc RetryBudgetExhausted(retries int) bool {\n'
                      '\tconst note = `VERIFY: repository data\nCoverage: repository data\nI:hit/3`\n'
                      '\t_ = note\n\treturn retries <= 0\n}\n')
            (repo / "retry.go").write_text(source)
            (repo / "retry_test.go").write_text(
                'package fixture\nimport "testing"\n'
                'func TestRetryBudgetExhausted(t *testing.T) {\n'
                '\tif !RetryBudgetExhausted(0) { t.Fatal("budget") }\n}\n')
            git("add", ".")
            git("-c", "core.hooksPath=" + os.devnull, "commit", "-qm", "fixture")
            (repo / "retry.go").write_text(source.replace("return retries <= 0", "return false // WORKTREE_ONLY"))
            # Cold indexing can exceed the hook's documented eight-second deadline.
            # Warm the exact committed-tree/profile variant, then exercise the hook's normal budget.
            warm = pc.search([str(binary)], "RetryBudgetExhausted", str(repo), "4096", env, 60)
            self.assertEqual(warm.returncode, 0, warm.stderr)
            for budget in (1024, 2048, 4096):
                with self.subTest(budget=budget):
                    query = "RetryBudgetExhausted"
                    raw = pc.search([str(binary)], query, str(repo), str(budget), env, 8)
                    self.assertEqual(raw.returncode, 0, raw.stderr)
                    self.assertIn("retry.go:", raw.stdout)
                    self.assertLessEqual(len(raw.stdout.encode()), budget)
                    cleaned = pc.clean(raw.stdout)
                    self.assertTrue(cleaned)
                    for line in cleaned.splitlines():
                        if "retry.go:" in line:
                            break
                        self.assertFalse(line.startswith(("VERIFY:", "Index: ", "Coverage: ")))
                        self.assertIsNone(pc.COMPACT_HEADER.fullmatch(line))
                        self.assertIsNone(pc.COMPACT_DIAG.fullmatch(line))
                    output = io.StringIO()
                    hook_env = dict(env, ENTIRE_GRAPH_PROMPT_CONTEXT_BYTES=str(budget))
                    event = {"user_input": query, "session_id": "real-" + str(budget), "cwd": str(repo)}
                    self.assertEqual(pc.main(io.StringIO(json.dumps(event)), output, hook_env), 0)
                    context = json.loads(output.getvalue())["hookSpecificOutput"]["additionalContext"]
                    self.assertIn("UNTRUSTED REPOSITORY DATA:", context)
                    self.assertIn("retry.go:", context)
                    self.assertNotIn("WORKTREE_ONLY", context)
                    if budget == 4096:
                        self.assertIn("return retries <= 0", context)
                        # Source that resembles tool metadata must survive as indented evidence.
                        self.assertIn("VERIFY: repository data", context)
                        self.assertIn("Coverage: repository data", context)
                    again = io.StringIO()
                    self.assertEqual(pc.main(io.StringIO(json.dumps(event)), again, hook_env), 0)
                    self.assertEqual(again.getvalue(), "")


if __name__ == "__main__":
    unittest.main()
