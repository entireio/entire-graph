#!/usr/bin/env python3
"""Hermetic tests for entire-graph-prompt-context.py (fake graph binary, temp cache; no network, no real index)."""
import importlib.util, io, json, os, stat, sys, tempfile, unittest
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("pc", os.path.join(HERE, "entire-graph-prompt-context.py"))
pc = importlib.util.module_from_spec(spec); spec.loader.exec_module(pc)

FAKE = """#!/bin/sh
printf '%s\\n' "$@" > "$FAKE_ARGS"
printf 'VERIFY=%s\\n' "$ENTIRE_GRAPH_VERIFY_BLOCK" >> "$FAKE_ARGS"
cat <<'OUT'
Index: cache-hit (3ms) | Query: 2ms | Total: 5ms
Coverage: degraded (2 languages/10 files; 1 warning)
- warning W_WORKTREE_SNAPSHOT
VERIFY: go test ./src -run '^TestHandler$'
  run it ONCE after editing; if it fails, fix the code and re-run THIS command.
1. src/a.go:10-20 Handler s=9.1 [focus:12]
func Handler() {}
LOW CONFIDENCE: verify before editing
OUT
"""

class T(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.fake = os.path.join(self.d, "fake-graph"); open(self.fake, "w").write(FAKE)
        os.chmod(self.fake, os.stat(self.fake).st_mode | stat.S_IEXEC)
        self.args = os.path.join(self.d, "args")
        self.env = {"ENTIRE_GRAPH_PROMPT_CONTEXT": "1", "ENTIRE_GRAPH_BIN": self.fake, "FAKE_ARGS": self.args,
                    "XDG_CACHE_HOME": os.path.join(self.d, "cache"), "PATH": os.environ.get("PATH", ""),
                    "CLAUDE_PROJECT_DIR": self.d}
    def run_hook(self, event, env=None):
        out = io.StringIO()
        rc = pc.main(stdin=io.StringIO(json.dumps(event)), stdout=out, env=env or self.env)
        return rc, out.getvalue()
    def ev(self, prompt="Fix the crash in Handler when input is empty\nmore detail", session="s1"):
        return {"prompt": prompt, "session_id": session, "cwd": self.d}

    def test_reads_current_user_input_field(self):
        rc, out = self.run_hook({"user_input": "Fix the crash in Handler", "session_id": "u1", "cwd": self.d})
        self.assertTrue(out)
        a = open(self.args).read().split("\n"); self.assertEqual(a[a.index("--query") + 1], "Fix the crash in Handler")
    def test_user_input_preferred_over_legacy_prompt(self):
        self.run_hook({"user_input": "new field", "prompt": "old field", "session_id": "u2", "cwd": self.d})
        a = open(self.args).read().split("\n"); self.assertEqual(a[a.index("--query") + 1], "new field")
    def test_compact_tight_budget_forms_stripped(self):
        answer = ("I:miss/1349 Q:374 P:276 T:2001\n!D W1 F13 L9/72 X2\n!LOW s=8.4\n"
                  "1. src/a.go:10-20 Handler s=9.1\nfunc Handler() {}\nI:x = 1  // code line, kept\n")
        got = pc.clean(answer)
        self.assertEqual(got, "1. src/a.go:10-20 Handler s=9.1\nfunc Handler() {}\nI:x = 1  // code line, kept")
        for header in ("I:hit/3", "I:miss/12 T:40", "I:miss/1 Q:2 P:3 T:4"):
            self.assertEqual(pc.clean(header + "\n1. a.go:1 X s=1"), "1. a.go:1 X s=1")
        self.assertEqual(pc.clean("!N W0 F0 L3/10\n1. a.go:1 X s=1"), "1. a.go:1 X s=1")
    def test_low_confidence_only_as_column0_metadata(self):
        code = '    log("LOW CONFIDENCE: retry")  # repository content, kept'
        got = pc.clean("LOW CONFIDENCE: top score 8.1 (weak); verify before editing.\n1. a.go:1 X s=1\n" + code)
        self.assertEqual(got, "1. a.go:1 X s=1\n" + code)
    def test_off_by_default(self):
        env = dict(self.env); env.pop("ENTIRE_GRAPH_PROMPT_CONTEXT")
        self.assertEqual(self.run_hook(self.ev(), env), (0, ""))
    def test_injects_cleaned_answer_with_heading(self):
        rc, out = self.run_hook(self.ev())
        self.assertEqual(rc, 0)
        ctx = json.loads(out)["hookSpecificOutput"]
        self.assertEqual(ctx["hookEventName"], "UserPromptSubmit")
        self.assertTrue(ctx["additionalContext"].startswith(pc.HEADING + "\n1. src/a.go:10-20"))
        self.assertNotIn("LOW CONFIDENCE", ctx["additionalContext"])
        self.assertNotIn("Index: ", ctx["additionalContext"])
        for banned in ("Coverage:", "W_WORKTREE_SNAPSHOT", "VERIFY", "run it ONCE", "go test"):
            self.assertNotIn(banned, ctx["additionalContext"])
    def test_query_is_first_nonempty_line_truncated_and_flags_pinned(self):
        self.run_hook(self.ev(prompt="\n\n  " + "x" * 300 + "  \nsecond"))
        a = open(self.args).read().split("\n")
        self.assertEqual(a[a.index("--query") + 1], "x" * 160)
        for flag, val in (("--format", "agent"), ("--max-context-bytes", "2048"), ("--profile", "full"), ("--repo", self.d)):
            self.assertEqual(a[a.index(flag) + 1], val)
        self.assertIn("VERIFY=off", a)
    def test_once_per_session(self):
        self.assertTrue(self.run_hook(self.ev())[1])
        self.assertEqual(self.run_hook(self.ev(prompt="another request"))[1], "")
        self.assertTrue(self.run_hook(self.ev(session="s2"))[1])
    def test_slash_command_and_empty_prompt_skipped(self):
        self.assertEqual(self.run_hook(self.ev(prompt="/compact"))[1], "")
        self.assertEqual(self.run_hook(self.ev(prompt="   \n "))[1], "")
    def test_fails_open_on_error_and_bad_json(self):
        bad = os.path.join(self.d, "bad"); open(bad, "w").write("#!/bin/sh\necho '1. src/a.go:1-2 X s=1'\necho 'partial output'\nexit 3\n"); os.chmod(bad, 0o755)
        env = dict(self.env, ENTIRE_GRAPH_BIN=bad)
        self.assertEqual(self.run_hook(self.ev(), env), (0, ""))
        self.assertEqual(pc.main(stdin=io.StringIO("not json"), stdout=io.StringIO(), env=self.env), 0)
    def test_budget_override(self):
        self.run_hook(self.ev(), dict(self.env, ENTIRE_GRAPH_PROMPT_CONTEXT_BYTES="4096"))
        a = open(self.args).read().split("\n"); self.assertEqual(a[a.index("--max-context-bytes") + 1], "4096")

if __name__ == "__main__":
    unittest.main()
