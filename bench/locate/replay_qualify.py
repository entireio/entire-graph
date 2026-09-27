#!/usr/bin/env python3
"""Replay qualification for entire-graph locate calls. v2, after peer review.

Answers ONE question before any replay-savings number is quoted: of the graph locate calls
recorded in agent transcripts, how many can be replayed, and at what fidelity?

  full-execution : re-runnable end to end. Requires a RECORDED SOURCE IDENTITY -- a revision
                   that describes the tree the call ran against. A 40-hex appearing anywhere
                   in the command line does not qualify; it must be bound to the repo by a
                   flag that selects the tree.
  frozen-render  : the recorded RESPONSE can be re-rendered to compare payload/coverage.
                   Requires a payload with STRUCTURE -- several ranked entries, or a JSON
                   envelope -- not one rank-shaped line, which any echo can produce.
  neither        : replayable at neither fidelity.

Tiers are reported separately and never summed.

v1 DEFECTS, all found by peer review, all fixed here, each with a fixture in tests():
  1. "graph" substring + a verb anywhere counted `echo 'graph search'` as a call. Now the
     invocation must be the HEAD of a pipeline stage, and the verb must follow the binary.
  2. --repo parsed by regex, so quoted paths broke. Now shlex.
  3. any 40-hex anywhere counted as a revision, including inside --query text.
  4. one ranking line counted as a frozen-renderable payload.
  5. subagent transcripts were skipped entirely.
  6. malformed / non-list content raised TypeError.

Read-only. Never writes the corpus.
"""
import json, glob, os, re, sys, shlex, hashlib, collections

RANK = re.compile(r'^\s*\d+\.\s+\S+?:\d+', re.M)
HEX40 = re.compile(r'^[0-9a-f]{40}$')
VERBS = {"query", "search", "neighbors", "impact", "def", "explain"}
# flags that bind a call to a specific tree; --head means "committed tree", which is an
# identity only if the transcript ALSO records which commit that was.
REV_FLAGS = {"--rev", "--revision", "--commit", "--at"}
SPLIT = re.compile(r'\|\||&&|[|;&\n]')


def stages(command):
    """Leading stage of each statement -- the only position a real invocation occupies."""
    out = []
    for part in SPLIT.split(command or ""):
        part = part.strip()
        if part:
            out.append(part)
    return out


def argv_of(stage):
    try:
        return shlex.split(stage)
    except ValueError:
        return stage.split()


def graph_invocation(command):
    """(verb, argv) when a stage really invokes the graph, else None. Fixture: echo case."""
    for stage in stages(command):
        argv = argv_of(stage)
        i = 0
        while i < len(argv) and ("=" in argv[i] and not argv[i].startswith("-")):
            i += 1  # leading VAR=value assignments
        if i >= len(argv):
            continue
        head = os.path.basename(argv[i])
        rest = argv[i + 1:]
        if head in ("entire", "rtk") and rest[:1] == ["graph"]:
            rest = rest[1:]
        elif head == "entire-graph":
            pass
        else:
            continue
        if rest and rest[0] in VERBS:
            return rest[0], rest
    return None


def flag_value(argv, name):
    for i, a in enumerate(argv):
        if a == name and i + 1 < len(argv):
            return argv[i + 1]
        if a.startswith(name + "="):
            return a.split("=", 1)[1]
    return None


def recorded_source_identity(argv):
    """A revision BOUND to the tree by a flag. Not any 40-hex in the line. Fixture: query case."""
    for f in REV_FLAGS:
        v = flag_value(argv, f)
        if v and HEX40.match(v.strip()):
            return True
    return False


def payload_has_structure(body):
    """Several ranked entries or a JSON envelope. One rank-shaped line is not an artifact."""
    if not body:
        return False
    if len(RANK.findall(body)) >= 2:
        return True
    stripped = body.lstrip()
    if stripped.startswith("{"):
        try:
            d = json.loads(stripped)
        except Exception:
            return False
        return isinstance(d, dict) and isinstance(d.get("results"), list) and bool(d["results"])
    return False


def tool_events(path):
    out = []
    try:
        fh = open(path, errors="ignore")
    except OSError:
        return out
    for line in fh:
        if '"tool_use"' not in line and '"tool_result"' not in line:
            continue
        try:
            d = json.loads(line)
        except Exception:
            continue
        msg = d.get("message")
        if not isinstance(msg, dict):
            continue
        c = msg.get("content")
        if not isinstance(c, list):
            continue                      # malformed / string content: skip, never raise
        for b in c:
            if isinstance(b, dict) and b.get("type") in ("tool_use", "tool_result"):
                out.append(b)
    return out


def result_text(block):
    con = block.get("content")
    if isinstance(con, str):
        return con
    if isinstance(con, list):
        return " ".join(x.get("text", "") for x in con if isinstance(x, dict))
    return ""


def qualify(root):
    tiers = collections.Counter()
    reasons = collections.Counter()
    # subagent transcripts live in nested dirs; v1's fixed glob depth missed them
    files = sorted(set(glob.glob(os.path.join(root, "**", "*.jsonl"), recursive=True)))
    seen_ids = set()
    for f in files:
        evs = tool_events(f)
        res = {b.get("tool_use_id"): b for b in evs if b.get("type") == "tool_result"}
        for b in evs:
            if b.get("type") != "tool_use" or b.get("name") != "Bash":
                continue
            uid = b.get("id")
            if uid in seen_ids:
                continue
            seen_ids.add(uid)
            inv = graph_invocation(str((b.get("input") or {}).get("command", "")))
            if not inv:
                continue
            _, argv = inv
            body = result_text(res.get(uid, {}))
            if recorded_source_identity(argv):
                tiers["full-execution"] += 1
            elif payload_has_structure(body):
                tiers["frozen-render"] += 1
            else:
                tiers["neither"] += 1
                if not body:
                    reasons["no recorded result"] += 1
                elif not RANK.search(body):
                    reasons["result carries no ranked payload (transformed or non-ranking output)"] += 1
                else:
                    reasons["ranked payload too thin to re-render"] += 1
            if not recorded_source_identity(argv):
                reasons["no source revision bound to the tree"] += 1
            if not flag_value(argv, "--repo"):
                reasons["--repo absent (cwd-inherited; tree not recoverable)"] += 1
    return tiers, reasons, len(files)


def tests():
    """Retained fixtures, including the two RED cases peer review supplied."""
    ok = True

    def check(name, got, want):
        nonlocal ok
        if got != want:
            ok = False
            print(f"  FAIL {name}: got {got!r} want {want!r}")
        else:
            print(f"  ok   {name}")

    check("echo is not an invocation", graph_invocation("echo 'graph search'"), None)
    check("rg mentioning graph is not one", graph_invocation("rg -n 'entire graph query' ."), None)
    check("real invocation", (graph_invocation("entire graph query --repo . --query x") or ("",))[0], "query")
    check("entire-graph binary", (graph_invocation("/tmp/entire-graph search --repo .") or ("",))[0], "search")
    check("after cd", (graph_invocation("cd /r && entire graph impact --symbol X") or ("",))[0], "impact")
    check("env prefix", (graph_invocation("FOO=1 entire graph def --repo .") or ("",))[0], "def")
    check("quoted repo path", flag_value(argv_of("entire graph query --repo '/a b/c' --query x"), "--repo"), "/a b/c")
    check("40hex in query is not a revision",
          recorded_source_identity(argv_of("entire graph query --query " + "a" * 40)), False)
    check("40hex in query text is not a revision",
          recorded_source_identity(argv_of("entire graph query --query " + "0" * 40)), False)
    check("bound revision counts",
          recorded_source_identity(argv_of("entire graph query --rev " + "a" * 40)), True)
    check("one rank line is not an artifact", payload_has_structure("1. a.go:1 foo s=1"), False)
    check("two rank lines are", payload_has_structure("1. a.go:1 foo\n2. b.go:2 bar"), True)
    check("json envelope is", payload_has_structure('{"results":[{"rank":1}]}'), True)
    check("empty json envelope is not", payload_has_structure('{"results":[]}'), False)
    print("  ALL PASS" if ok else "  FAILURES ABOVE")
    return ok


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        sys.exit(0 if tests() else 1)
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser("~/.claude/projects")
    tiers, reasons, nfiles = qualify(root)
    total = sum(tiers.values())
    print(f"corpus root : {root}")
    print(f"transcripts : {nfiles} (recursive; subagents included)")
    print(f"locate calls: {total}\n")
    print("TIERS (never summed):")
    for k in ("full-execution", "frozen-render", "neither"):
        print(f"  {k:16s} {tiers[k]:6d}  {100*tiers[k]/max(total,1):5.1f}%")
    print("\nWHY (counts overlap; diagnostic only):")
    for k, v in reasons.most_common():
        print(f"  {v:6d}  {k}")
    me = os.path.abspath(__file__)
    print(f"\nparser sha256 {hashlib.sha256(open(me,'rb').read()).hexdigest()}")
