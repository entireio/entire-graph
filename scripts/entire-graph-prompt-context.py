#!/usr/bin/env python3
"""Claude Code UserPromptSubmit hook: put the code graph's answer for the user's request into the session context,
once per session, with no tool call.

Why this shape (pilot8 evidence, entirehq/graphmark pilot8-prep, CHANGES-fairness §11/§12):
  - A graph SEARCH TOOL call costs a whole agent turn (~20K cached tokens on Claude Code), so making the agent call the
    graph cost +26% tokens (Stage L) without improving resolution.
  - The same answer injected into the prompt (Stages P and A) was token-neutral (A: +0.7%, CI ±11%) and resolved more
    tasks in both stages (P 25 vs 23, A 26 vs 23 of 35 known pairs; b/c = 2/0 and 4/1). Not statistically significant.
    That is why this hook is OPT-IN and labelled experimental.

Opt in:  ENTIRE_GRAPH_PROMPT_CONTEXT=1   (in the environment Claude Code is started from, or settings.json "env").
Tuning:  ENTIRE_GRAPH_PROMPT_CONTEXT_BYTES (default 2048, the measured setting), ENTIRE_GRAPH_BIN (default: `entire`
         on PATH, invoked as `entire graph ...`).
Fails open: any error, timeout, non-repo or empty answer adds nothing and never blocks the prompt.
"""
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time

HEADING = "Code locations for this request (prompt input from the local code graph, entire-graph):"
QUERY_CHARS = 160
# Search budget. The hook runs before the prompt reaches the model, so it must never noticeably delay it: past this
# the search is abandoned and nothing is injected (fail open). hooks/hooks.json's timeout must stay above it.
TIMEOUT_S = 8
# A binary that predates --head rejects it ("search does not accept --head in entire-graph ...", or a generic
# unknown-flag error). Only that rejection earns the one retry without --head; any other failure fails open.
HEAD_REJECTED = re.compile(r"(does not accept|unknown flag|flag provided but not defined)[^\n]*?(?<![\w-])-{1,2}head\b")


def enabled(env):
    return env.get("ENTIRE_GRAPH_PROMPT_CONTEXT", "").strip().lower() in ("1", "true", "on", "yes")


def first_line(prompt):
    for line in prompt.splitlines():
        if line.strip():
            return line.strip()[:QUERY_CHARS]
    return ""


def once_marker(env, session_id):
    base = env.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")
    d = os.path.join(base, "entire-graph", "prompt-context")
    os.makedirs(d, exist_ok=True)
    return os.path.join(d, hashlib.sha256(session_id.encode()).hexdigest()[:32])


def graph_argv(env):
    override = env.get("ENTIRE_GRAPH_BIN", "").strip()
    if override:
        return override.split()
    entire = shutil.which("entire", path=env.get("PATH"))
    return [entire, "graph"] if entire else None


# Compact forms the agent renderer degrades to under tight budgets (internal/cli/search.go):
# the "I:<state>/<ms>[ Q:n][ P:n][ T:n]" header, the "!N|!D W.. F.. L../..[ X..]" coverage diagnostic,
# and the "!LOW s=.." low-confidence marker. Anchored whole-line matches, so code lines never match.
COMPACT_HEADER = re.compile(r"^I:[a-z-]+/\d+(?: [QPT]:\d+)*$")
COMPACT_DIAG = re.compile(r"^![ND] W\d+ F\d+ L\d+/\d+(?: X\d+)?$")
COMPACT_LOW = re.compile(r"^!LOW s=-?\d+(?:\.\d+)?$")


def clean(answer):
    """Keep the ranked regions only. Drop, whatever the binary version:
    - the timing line ("Index: ..." or its compact "I:..." form) and the Coverage diagnostics block ("Coverage: ..."
      plus its "- ..." lines, or the compact "!N W.. F.. L../.." line);
    - LOW CONFIDENCE advisories (or the compact "!LOW s=.." marker);
    - the VERIFY block ("VERIFY: ..." plus its indented continuation lines). It carries test-running advice, i.e.
      directive text, which the measured bundle never contained. Older binaries ignore ENTIRE_GRAPH_VERIFY_BLOCK=off
      and reject --verify-block, so it is removed here rather than requested off."""
    keep, block = [], None
    for line in answer.splitlines():
        if block == "coverage" and line.startswith("- "):
            continue
        if block == "verify" and (line.startswith("  ") or line.startswith("\t")):
            continue
        block = None
        if (line.startswith("Index: ") or line.startswith("LOW CONFIDENCE:") or COMPACT_HEADER.match(line)
                or COMPACT_DIAG.match(line) or COMPACT_LOW.match(line)):
            continue
        if line.startswith("Coverage: "):
            block = "coverage"; continue
        if line.startswith("VERIFY:"):
            block = "verify"; continue
        keep.append(line)
    return "\n".join(keep).strip()


def search(argv, query, repo, budget, env, timeout):
    """Search the committed tree (--head): it is served from the tree-hash cache once warm, while working-tree mode
    rebuilds the index on every call (10-30 s cold) and would usually exceed the time budget. Falls back to the
    working tree, once, only when the binary rejects --head. Both attempts share one deadline."""
    base = argv + ["search", "--query", query, "--repo", repo, "--profile", "full",
                   "--format", "agent", "--max-context-bytes", budget]
    deadline = time.monotonic() + timeout
    r = subprocess.run(base + ["--head"], capture_output=True, text=True, timeout=timeout, env=env, cwd=repo,
                       stdin=subprocess.DEVNULL)
    if r.returncode != 0 and HEAD_REJECTED.search(r.stderr or ""):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return None
        r = subprocess.run(base, capture_output=True, text=True, timeout=remaining, env=env, cwd=repo,
                           stdin=subprocess.DEVNULL)
    return r


def main(stdin=sys.stdin, stdout=sys.stdout, env=os.environ):
    if not enabled(env):
        return 0
    try:
        event = json.load(stdin)
    except ValueError:
        return 0
    # current Claude Code sends the prompt as "user_input"; earlier versions used "prompt"
    prompt = event.get("user_input") or event.get("prompt") or ""
    session = event.get("session_id") or ""
    repo = env.get("CLAUDE_PROJECT_DIR") or event.get("cwd") or os.getcwd()
    query = first_line(prompt)
    if not query or query.startswith("/") or not session:
        return 0
    marker = once_marker(env, session)
    if os.path.exists(marker):
        return 0
    argv = graph_argv(env)
    if not argv:
        return 0
    try:
        budget = str(int(env.get("ENTIRE_GRAPH_PROMPT_CONTEXT_BYTES", "2048")))
    except ValueError:
        budget = "2048"
    # Mark first, so a slow or failing search is never retried on later prompts of the same session.
    open(marker, "w").close()
    run_env = dict(env, ENTIRE_GRAPH_VERIFY_BLOCK="off")
    try:
        r = search(argv, query, repo, budget, run_env, TIMEOUT_S)
    except (OSError, subprocess.SubprocessError):
        return 0
    if r is None or r.returncode != 0:
        return 0
    answer = clean(r.stdout)
    if not answer:
        return 0
    json.dump({"hookSpecificOutput": {"hookEventName": "UserPromptSubmit",
                                      "additionalContext": HEADING + "\n" + answer}}, stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
