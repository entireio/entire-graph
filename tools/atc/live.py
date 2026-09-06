#!/usr/bin/env python3
"""ATC Live — watch the agents that are actually flying, right now.

Every other part of ATC reasons about commits. This one watches work that
has not been committed yet, which is when collisions are still cheap to
avoid. For each worktree of a repository it answers three questions:

  what is this agent TRYING to do   the live prompt from that session's
                                    Claude Code transcript (tier 0 intent —
                                    richer than any commit message, because
                                    the commit does not exist yet)
  what has it CHANGED so far        `git stash create` snapshot of the
                                    uncommitted tree (never touches the
                                    agent's files, index or stash stack)
  will it COLLIDE                   pairwise collide over those snapshots

    python3 tools/atc/live.py --repo <path> --serve     # UI at :8787
    python3 tools/atc/live.py --repo <path> --once      # print JSON

Transcripts are read-only and never leave the machine.
"""

import argparse
import http.server
import json
import os
import shutil
import socketserver
import subprocess
import sys
import threading
import time
import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import collide  # noqa: E402  (same directory)
import terminal  # noqa: E402

CLAUDE_PROJECTS = os.path.expanduser("~/.claude/projects")


def transcript_dir(worktree):
    """Claude Code stores a session per cwd, slugged by path."""
    return os.path.join(CLAUDE_PROJECTS, os.path.abspath(worktree).replace("/", "-"))


def live_prompt(worktree):
    """The prompt this agent is working on right now, from its transcript.

    Prefers the `last-prompt` record Claude Code maintains; falls back to the
    most recent human turn. Tool results and system reminders are skipped so
    the flight plan shows what the human asked for, not machine chatter.
    """
    d = transcript_dir(worktree)
    if not os.path.isdir(d):
        return None
    files = [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".jsonl")]
    if not files:
        return None
    path = max(files, key=os.path.getmtime)
    last_prompt, last_human, session_id, turns = None, None, None, 0
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                kind = row.get("type")
                if kind == "last-prompt":
                    last_prompt = row.get("lastPrompt") or last_prompt
                    session_id = row.get("sessionId") or session_id
                elif kind == "user":
                    text = _human_text(row)
                    if text:
                        last_human, turns = text, turns + 1
    except OSError:
        return None
    text = (last_prompt or last_human or "").strip()
    if not text:
        return None
    return {
        "text": text,
        "session_id": session_id or os.path.basename(path)[:8],
        "turns": turns,
        "source": "live session" if last_prompt else "session transcript",
        "updated": datetime.datetime.fromtimestamp(
            os.path.getmtime(path)).astimezone().isoformat(timespec="seconds"),
        "idle_seconds": int(time.time() - os.path.getmtime(path)),
    }


def trajectory(worktree, limit=40):
    """What this agent has actually been doing, from its own transcript.

    Returns the ordered tool activity and, separately, the files it has
    WRITTEN (Edit/Write), which is the half that can collide with another
    agent. Reads are noise for our purposes and are counted, not listed.
    """
    d = transcript_dir(worktree)
    if not os.path.isdir(d):
        return {"steps": [], "wrote": [], "read_count": 0, "tool_counts": {}}
    files = [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".jsonl")]
    if not files:
        return {"steps": [], "wrote": [], "read_count": 0, "tool_counts": {}}
    path = max(files, key=os.path.getmtime)
    steps, wrote, reads, counts = [], {}, 0, {}
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if row.get("type") == "user":
                    text = _human_text(row)
                    if text:
                        steps.append({"kind": "prompt", "text": text[:180],
                                      "at": (row.get("timestamp") or "")[11:19]})
                    continue
                if row.get("type") != "assistant":
                    continue
                for b in (row.get("message") or {}).get("content") or []:
                    if not isinstance(b, dict) or b.get("type") != "tool_use":
                        continue
                    name = b.get("name") or "?"
                    counts[name] = counts.get(name, 0) + 1
                    inp = b.get("input") or {}
                    fp = inp.get("file_path") or inp.get("notebook_path")
                    at = (row.get("timestamp") or "")[11:19]
                    if name in ("Edit", "Write", "NotebookEdit") and fp:
                        rel = os.path.relpath(fp, worktree) if fp.startswith("/") else fp
                        entry = wrote.setdefault(rel, {"path": rel, "edits": 0, "last": at})
                        entry["edits"] += 1
                        entry["last"] = at
                        steps.append({"kind": "write", "text": rel, "at": at, "tool": name})
                    elif name == "Read":
                        reads += 1
                    elif name == "Bash":
                        cmd = (inp.get("command") or "")[:90]
                        steps.append({"kind": "run", "text": cmd, "at": at})
    except OSError:
        pass
    return {
        "steps": steps[-limit:],
        "wrote": sorted(wrote.values(), key=lambda w: -w["edits"]),
        "read_count": reads,
        "tool_counts": dict(sorted(counts.items(), key=lambda x: -x[1])[:6]),
    }


def _human_text(row):
    content = (row.get("message") or {}).get("content")
    if isinstance(content, str):
        text = content
    elif isinstance(content, list):
        if any(isinstance(b, dict) and b.get("type") == "tool_result" for b in content):
            return None
        text = " ".join(b.get("text", "") for b in content
                        if isinstance(b, dict) and b.get("type") == "text")
    else:
        return None
    text = text.strip()
    # system reminders and command wrappers are not something a human typed
    if not text or text.startswith("<"):
        return None
    return text


def uncommitted_stat(worktree):
    """Files and line counts the agent has changed but not committed."""
    try:
        out = collide.sh(["git", "-C", worktree, "diff", "HEAD", "--numstat"])
    except RuntimeError:
        return [], 0
    files, lines = [], 0
    for row in out.strip().splitlines():
        parts = row.split("\t")
        if len(parts) == 3:
            add, dele, path = parts
            files.append(path)
            lines += sum(int(x) for x in (add, dele) if x.isdigit())
    return files, lines


def snapshot_sides(repo):
    """Every worktree in flight, with its live intent and uncommitted work.

    The repository you are sitting in counts as a side too — an agent editing
    the main worktree is as much a collision risk as one in a linked worktree.
    It is included only when it has something in flight (uncommitted work or a
    live session), so a clean checkout does not clutter the board.
    """
    candidates = list(collide.discover_sides(repo))
    if uncommitted_stat(repo)[0] or live_prompt(repo):
        candidates.insert(0, repo)
    sides = []
    for path in candidates:
        try:
            branch = collide.sh(
                ["git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD"]).strip()
        except RuntimeError:
            continue
        files, lines = uncommitted_stat(path)
        sides.append({
            "path": path,
            "branch": branch,
            "label": os.path.basename(path.rstrip("/")),
            "prompt": live_prompt(path),
            "trajectory": trajectory(path),
            "uncommitted_files": files,
            "uncommitted_lines": lines,
        })
    return sides


def build_state(repo, profile_label=None):
    """One full sweep: sides in flight, pairwise verdicts, unique codes."""
    sides = snapshot_sides(repo)
    pairs = []
    for i in range(len(sides)):
        for j in range(i + 1, len(sides)):
            a, b = sides[i], sides[j]
            entry = {"id": f"{a['label']}|{b['label']}",
                     "a": a["label"], "b": b["label"]}
            try:
                result = collide.collide(repo, a["path"], b["path"])
                entry["result"] = result
                entry["exit_code"] = collide.EXIT_BY_VERDICT.get(
                    result["verdict"], 1) if hasattr(collide, "EXIT_BY_VERDICT") \
                    else (2 if result["reds"] else (1 if result["advisories"] else 0))
            except RuntimeError as e:
                entry["error"] = str(e)[:200]
                entry["exit_code"] = 3
            pairs.append(entry)

    codes = terminal.assign_codes(
        [f for s in sides for f in s["uncommitted_files"]])
    flights = []
    for n, s in enumerate(sides, 1):
        prompt = s["prompt"]
        flights.append({
            "branch": s["branch"],
            "label": s["label"],
            "callsign": terminal.callsign(s["branch"], n),
            "files": s["uncommitted_files"],
            "codes": [codes[f] for f in s["uncommitted_files"]],
            "intent": prompt["text"] if prompt else "(no live session found)",
            "intent_source": prompt["source"] if prompt else "none",
            "session_id": prompt["session_id"] if prompt else None,
            "idle_seconds": prompt["idle_seconds"] if prompt else None,
            "uncommitted_lines": s["uncommitted_lines"],
            "trajectory": s["trajectory"],
            "commits": 0,
        })
    return {
        "live": True,
        "generated": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "repo": repo,
        "profile_label": profile_label or os.path.basename(repo.rstrip("/")),
        "flights": flights,
        "pairs": pairs,
        "sides_in_flight": len(sides),
    }


def launch_agent(repo, prompt, branch=None, headless=True):
    """Put a real agent in the air: new worktree + a live Claude session.

    Headless (`claude -p`) keeps the demo self-contained — the session edits
    the worktree and the next sweep picks it up like any other flight. With
    headless off we open a Terminal window instead, so a human can watch and
    take over. Either way the session is real; nothing here is simulated.
    """
    prompt = (prompt or "").strip()
    if not prompt:
        raise ValueError("a flight plan is required")
    slug = "".join(c if c.isalnum() or c in "-_" else "-"
                   for c in (branch or prompt.lower())[:28]).strip("-") or "agent"
    branch = branch or f"agent-{slug}"
    wt = os.path.join(os.path.dirname(os.path.abspath(repo)), f"wt-{slug}")
    if os.path.exists(wt):
        raise ValueError(f"{os.path.basename(wt)} already exists — pick another name")

    collide.sh(["git", "-C", repo, "worktree", "add", "-q", "-b", branch, wt])
    claude = os.environ.get("ATC_CLAUDE_BIN") or shutil.which("claude")
    if not claude:
        # No CLI on this machine: the worktree is real and the plan is filed,
        # so the flight is ready — it just needs a human to start the engine.
        with open(os.path.join(wt, ".atc-flightplan"), "w", encoding="utf-8") as f:
            f.write(prompt + "\n")
        return {"branch": branch, "worktree": wt, "prompt": prompt, "pid": None,
                "mode": "manual",
                "command": f'cd {wt} && claude "{prompt.splitlines()[0]}"'}
    if headless:
        log = open(os.path.join(wt, ".atc-agent.log"), "wb")
        proc = subprocess.Popen(
            [claude, "-p", prompt, "--permission-mode", "acceptEdits"],
            cwd=wt, stdout=log, stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL, start_new_session=True)
        pid, how = proc.pid, "headless"
    else:
        script = (f'cd {json.dumps(wt)[1:-1]} && '
                  f'{claude} {json.dumps(prompt)}')
        subprocess.run(["osascript", "-e",
                        f'tell app "Terminal" to do script {json.dumps(script)}'],
                       capture_output=True)
        pid, how = None, "terminal"
    return {"branch": branch, "worktree": wt, "prompt": prompt,
            "pid": pid, "mode": how}


class State:
    """Shared latest sweep, refreshed by a background thread."""

    def __init__(self, repo, interval, label, known=()):
        self.repo, self.interval, self.label = repo, interval, label
        self.known = list(dict.fromkeys([repo, *known]))
        self.lock = threading.Lock()
        self.data = {"live": True, "flights": [], "pairs": [],
                     "generated": None, "sides_in_flight": 0,
                     "status": "starting"}
        self.stop = threading.Event()

    def sweep(self):
        try:
            fresh = build_state(self.repo, self.label)
            fresh["status"] = "ok"
        except Exception as e:  # a watcher must never take the page down
            fresh = dict(self.data, status=f"error: {e}"[:200],
                         generated=datetime.datetime.now().astimezone()
                         .isoformat(timespec="seconds"))
        with self.lock:
            self.data = fresh
        return fresh

    def run(self):
        while not self.stop.wait(0 if self.data.get("status") == "starting" else
                                 self.interval):
            self.sweep()

    def snapshot(self):
        with self.lock:
            return json.dumps(self.data)


def serve(state, page, port):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def _send(self, body, ctype):
            body = body.encode() if isinstance(body, str) else body
            self.send_response(200)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path.startswith("/repos"):
                self._send(json.dumps({"repos": state.known,
                                       "current": state.repo}), "application/json")
            elif self.path.startswith("/live.json"):
                self._send(state.snapshot(), "application/json")
            elif self.path in ("/", "/index.html", "/console", "/console.html"):
                with open(os.path.join(HERE, "console.html"), encoding="utf-8") as f:
                    self._send(f.read(), "text/html; charset=utf-8")
            elif self.path in ("/terminal", "/terminal.html"):
                with open(page, encoding="utf-8") as f:
                    self._send(f.read(), "text/html; charset=utf-8")
            else:
                self.send_error(404)

        def do_POST(self):
            if self.path.startswith("/switch"):
                n = int(self.headers.get("Content-Length") or 0)
                repo = (json.loads(self.rfile.read(n) or b"{}")).get("repo")
                if repo in state.known:
                    state.repo = repo
                    state.label = os.path.basename(repo.rstrip("/"))
                    threading.Thread(target=state.sweep, daemon=True).start()
                    return self._send(json.dumps({"ok": True, "repo": repo}),
                                      "application/json")
                return self._send(json.dumps({"ok": False, "error": "unknown repo"}),
                                  "application/json")
            if not self.path.startswith("/launch"):
                return self.send_error(404)
            try:
                n = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(n) or b"{}")
                info = launch_agent(state.repo, body.get("prompt"),
                                    body.get("branch"),
                                    headless=body.get("headless", True))
                threading.Thread(target=state.sweep, daemon=True).start()
                self._send(json.dumps({"ok": True, **info}), "application/json")
            except Exception as e:
                self._send(json.dumps({"ok": False, "error": str(e)[:300]}),
                           "application/json")

    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("127.0.0.1", port), Handler) as httpd:
        print(f"[atc-live] watching {state.repo}")
        print(f"[atc-live] open http://127.0.0.1:{port}  (Ctrl-C to stop)")
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print("\n[atc-live] stopped")


def main():
    ap = argparse.ArgumentParser(description="ATC live watcher")
    ap.add_argument("--repo", default=".")
    ap.add_argument("--interval", type=float, default=6.0)
    ap.add_argument("--port", type=int, default=8787)
    ap.add_argument("--page", default=os.path.join(HERE, "terminal.html"))
    ap.add_argument("--label", default=None, help="repo name shown in the UI")
    ap.add_argument("--also", default="", help="comma-separated repos for the selector")
    ap.add_argument("--once", action="store_true", help="print one sweep as JSON")
    ap.add_argument("--serve", action="store_true", help="serve the live UI")
    args = ap.parse_args()
    repo = os.path.abspath(args.repo)

    if args.once:
        print(json.dumps(build_state(repo, args.label), indent=2))
        return

    known = [os.path.abspath(x) for x in args.also.split(",") if x.strip()]
    state = State(repo, args.interval, args.label, known)
    print("[atc-live] first sweep ...")
    first = state.sweep()
    print(f"[atc-live] {first['sides_in_flight']} side(s) in flight, "
          f"{len(first['pairs'])} pair(s)")
    for f in first["flights"]:
        print(f"    {f['callsign']:<10} {f['label'] if 'label' in f else ''} "
              f"— {f['intent'][:60]}")
    if not args.serve:
        return
    threading.Thread(target=state.run, daemon=True).start()
    serve(state, args.page, args.port)


if __name__ == "__main__":
    main()
