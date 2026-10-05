#!/usr/bin/env python3
"""Hermetic tests for entire-graph-prompt-context.py (fake graph binary, temp cache; no network, no real index)."""
import importlib.util, io, json, os, stat, subprocess, sys, tempfile, unittest
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("pc", os.path.join(HERE, "entire-graph-prompt-context.py"))
pc = importlib.util.module_from_spec(spec); spec.loader.exec_module(pc)

FAKE = """#!/bin/sh
[ "$1" = graph ] && shift
printf '%s\\n' "$*" >> "$FAKE_ARGS.calls"
if [ "$1" = version ]; then
  [ -n "$FAKE_PROBE_FAIL" ] && { echo "unknown command graph" >&2; exit 1; }
  echo '{"identity_revision":"2","provider":"entire-graph","version":"v0.0.0-test"}'
  exit 0
fi
if [ -n "$FAKE_REJECT_HEAD" ]; then
  for a in "$@"; do
    if [ "$a" = --head ]; then
      echo "search does not accept --head in entire-graph v0.3.0: this binary may be older than the caller" >&2
      exit 2
    fi
  done
fi
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

# A failure fake must still answer the capability probe, or it would test the probe instead of the search path.
PROBE_OK = "#!/bin/sh\n[ \"$1\" = version ] && { echo '{\"provider\":\"entire-graph\"}'; exit 0; }\n"

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
        bad = os.path.join(self.d, "bad"); open(bad, "w").write(PROBE_OK + "echo '1. src/a.go:1-2 X s=1'\necho 'partial output'\nexit 3\n"); os.chmod(bad, 0o755)
        env = dict(self.env, ENTIRE_GRAPH_BIN=bad)
        self.assertEqual(self.run_hook(self.ev(), env), (0, ""))
        self.assertEqual(pc.main(stdin=io.StringIO("not json"), stdout=io.StringIO(), env=self.env), 0)
    def calls(self):
        return open(self.args + ".calls").read().splitlines()
    def test_searches_committed_tree_with_head(self):
        rc, out = self.run_hook(self.ev())
        self.assertTrue(out)
        a = open(self.args).read().split("\n"); self.assertIn("--head", a)
        self.assertEqual(len([c for c in self.calls() if c.startswith("search ")]), 1)
    def test_falls_back_once_without_head_when_rejected(self):
        rc, out = self.run_hook(self.ev(), dict(self.env, FAKE_REJECT_HEAD="1"))
        self.assertTrue(out, "the working-tree retry should still inject the answer")
        searches = [c for c in self.calls() if c.startswith("search ")]
        self.assertEqual(len(searches), 2)
        self.assertIn("--head", searches[0].split()); self.assertNotIn("--head", searches[1].split())
    def test_no_retry_on_other_failures(self):
        bad = os.path.join(self.d, "bad"); open(bad, "w").write(
            PROBE_OK + "echo x >> \"$FAKE_ARGS.calls\"\necho 'search does not accept --headx' >&2\nexit 2\n")
        os.chmod(bad, 0o755)
        self.assertEqual(self.run_hook(self.ev(), dict(self.env, ENTIRE_GRAPH_BIN=bad)), (0, ""))
        self.assertEqual(self.calls(), ["x"], "only a --head rejection earns a retry")
    def test_head_rejection_pattern(self):
        for msg in ("search does not accept --head in entire-graph v0.3.0: ...", "Error: unknown flag: --head",
                    "flag provided but not defined: -head"):
            self.assertTrue(pc.HEAD_REJECTED.search(msg), msg)
        for msg in ("search does not accept --headx in entire-graph", "index missing; try --head", "unknown flag: --ahead"):
            self.assertFalse(pc.HEAD_REJECTED.search(msg), msg)
    def test_search_timeout_is_8s_and_hook_timeout_above_it(self):
        self.assertEqual(pc.TIMEOUT_S, 8)
        seen, real = [], pc.subprocess.run
        def spy(*a, **k):
            seen.append(k.get("timeout")); return real(*a, **k)
        pc.subprocess.run = spy
        try:
            self.assertTrue(self.run_hook(self.ev())[1])
        finally:
            pc.subprocess.run = real
        self.assertTrue(seen and all(t is not None and t <= 8 for t in seen), seen)
        hooks = json.load(open(os.path.join(HERE, "..", "hooks", "hooks.json")))
        cmd = [h for e in hooks["hooks"]["UserPromptSubmit"] for h in e["hooks"]
               if "entire-graph-prompt-context.py" in h["command"]][0]
        self.assertGreater(cmd["timeout"], pc.TIMEOUT_S + pc.PROBE_TIMEOUT_S)
    def test_timeout_fails_open(self):
        slow = os.path.join(self.d, "slow"); open(slow, "w").write(PROBE_OK + "exec sleep 5\n"); os.chmod(slow, 0o755)
        old = pc.TIMEOUT_S; pc.TIMEOUT_S = 0.3
        try:
            self.assertEqual(self.run_hook(self.ev(), dict(self.env, ENTIRE_GRAPH_BIN=slow)), (0, ""))
        finally:
            pc.TIMEOUT_S = old
    def marker(self, session="s1"):
        return json.load(open(pc.once_marker(self.env, session)))
    def test_probe_runs_once_before_search(self):
        self.assertTrue(self.run_hook(self.ev())[1])
        c = self.calls(); self.assertEqual(c[0], "version --json"); self.assertTrue(c[1].startswith("search "))
        self.assertEqual(self.marker(), {"probe": "ok", "version": "v0.0.0-test"})
    def test_probe_failure_skips_search_and_marks_done(self):
        env = dict(self.env, FAKE_PROBE_FAIL="1")
        self.assertEqual(self.run_hook(self.ev(), env), (0, ""))
        self.assertEqual(self.calls(), ["version --json"], "no search after a failed probe")
        self.assertEqual(self.marker()["probe"], "error")
    def test_probe_result_cached_for_session(self):
        env = dict(self.env, FAKE_PROBE_FAIL="1")
        self.run_hook(self.ev(), env); self.run_hook(self.ev(prompt="second request"), env)
        self.run_hook(self.ev(prompt="third request"), self.env)
        self.assertEqual(self.calls(), ["version --json"], "the probe runs once per session, its result is reused")
    def test_absent_binary_marks_done(self):
        env = dict(self.env, ENTIRE_GRAPH_BIN=os.path.join(self.d, "missing"))
        self.assertEqual(self.run_hook(self.ev(), env), (0, ""))
        self.assertEqual(self.marker()["probe"], "error")
        env = dict(self.env, PATH=self.d); env.pop("ENTIRE_GRAPH_BIN")
        self.assertEqual(self.run_hook(self.ev(session="s9"), env), (0, ""))
        self.assertEqual(self.marker("s9"), {"probe": "absent"})
    def test_probe_rejects_other_provider_and_slow_probe(self):
        other = os.path.join(self.d, "other"); open(other, "w").write(
            "#!/bin/sh\necho \"$*\" >> \"$FAKE_ARGS.calls\"\necho '{\"provider\":\"something-else\"}'\n")
        os.chmod(other, 0o755)
        self.assertEqual(self.run_hook(self.ev(), dict(self.env, ENTIRE_GRAPH_BIN=other)), (0, ""))
        self.assertEqual(self.calls(), ["version --json"])
        self.assertEqual(pc.PROBE_TIMEOUT_S, 3)
        slow = os.path.join(self.d, "slowprobe"); open(slow, "w").write("#!/bin/sh\nexec sleep 5\n"); os.chmod(slow, 0o755)
        old = pc.PROBE_TIMEOUT_S; pc.PROBE_TIMEOUT_S = 0.3
        try:
            self.assertEqual(self.run_hook(self.ev(session="s3"), dict(self.env, ENTIRE_GRAPH_BIN=slow)), (0, ""))
        finally:
            pc.PROBE_TIMEOUT_S = old
        self.assertEqual(self.marker("s3"), {"probe": "error", "reason": "TimeoutExpired"})
    def test_malformed_event_shapes_fail_open_without_raising(self):
        # handle() directly: main() would also swallow an exception, which would hide a missing type guard.
        for event in ([], "text", 5, None, {"user_input": ["Fix it"], "session_id": "m1"},
                      {"user_input": {"t": 1}, "prompt": 7, "session_id": "m2"},
                      {"user_input": "Fix the crash", "session_id": 42},
                      {"user_input": "Fix the crash", "session_id": ["m3"]}):
            out = io.StringIO()
            self.assertEqual(pc.handle(io.StringIO(json.dumps(event)), out, self.env), 0, event)
            self.assertEqual(out.getvalue(), "", event)
        self.assertFalse(os.path.exists(self.args + ".calls"), "a malformed event must not reach the graph")
    def test_non_string_field_falls_back_to_next_string(self):
        env = dict(self.env); env.pop("CLAUDE_PROJECT_DIR")
        out = io.StringIO()
        pc.handle(io.StringIO(json.dumps({"user_input": 3, "prompt": "Fix the crash", "session_id": "m4",
                                          "cwd": ["x"]})), out, env)
        self.assertTrue(out.getvalue())
        a = open(self.args).read().split("\n"); self.assertEqual(a[a.index("--query") + 1], "Fix the crash")
    def test_script_exits_zero_on_unexpected_error(self):
        blocker = os.path.join(self.d, "cache-is-a-file"); open(blocker, "w").close()
        env = dict(self.env, XDG_CACHE_HOME=blocker)  # makedirs under a regular file raises
        with self.assertRaises(OSError):
            pc.handle(io.StringIO(json.dumps(self.ev())), io.StringIO(), env)
        r = subprocess.run([sys.executable, os.path.join(HERE, "entire-graph-prompt-context.py")],
                           input=json.dumps(self.ev()), capture_output=True, text=True, env=env, timeout=30)
        self.assertEqual((r.returncode, r.stdout, r.stderr), (0, "", ""))
    def test_bin_override_naming_entire_runs_graph_subcommand(self):
        for name in ("entire", "entire.exe", "Entire.EXE"):
            sub = os.path.join(self.d, name.lower() + "-dir"); os.makedirs(sub, exist_ok=True)
            entire = os.path.join(sub, name)
            with open(entire, "w") as f:
                f.write("#!/bin/sh\n[ \"$1\" = graph ] || { echo 'unknown command' >&2; exit 1; }\nshift\n"
                        "exec \"$FAKE_GRAPH\" \"$@\"\n")
            os.chmod(entire, 0o755)
            self.assertEqual(pc.graph_argv({"ENTIRE_GRAPH_BIN": entire}), [entire, "graph"])
        env = dict(self.env, ENTIRE_GRAPH_BIN=os.path.join(self.d, "entire-dir", "entire"), FAKE_GRAPH=self.fake)
        self.assertTrue(self.run_hook(self.ev(), env)[1], "ENTIRE_GRAPH_BIN=/path/to/entire must reach `entire graph`")
        for override, want in (("entire graph", ["entire", "graph"]), ("/opt/entire-graph", ["/opt/entire-graph"]),
                               (self.fake, [self.fake])):
            self.assertEqual(pc.graph_argv({"ENTIRE_GRAPH_BIN": override}), want)
    def test_budget_override(self):
        self.run_hook(self.ev(), dict(self.env, ENTIRE_GRAPH_PROMPT_CONTEXT_BYTES="4096"))
        a = open(self.args).read().split("\n"); self.assertEqual(a[a.index("--max-context-bytes") + 1], "4096")

if __name__ == "__main__":
    unittest.main()
