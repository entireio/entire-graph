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
SPLIT = re.compile(r'\|\||&&|[|;&\n]')


def _mask_shell(command):
    r"""Blank quoted spans, escaped characters and trailing comments.

    Peer review defeated the quote-only version with `# entire graph query` inside a comment
    and with `\;` escaped separators. Length is preserved so offsets stay valid.
    """
    cmd = command or ""
    out, quote, k = [], None, 0
    while k < len(cmd):
        ch = cmd[k]
        if quote:
            out.append(ch if ch == quote else " ")
            if ch == quote:
                quote = None
            k += 1
            continue
        if ch == "\\":
            # an escaped character is literal text, never a separator
            out.append("  " if k + 1 < len(cmd) else " ")
            k += 2
            continue
        if ch == "#":
            # A shell comment ends at the NEWLINE. The first repair blanked everything to the
            # end of the string, so `# note\nentire graph query ...` lost a command the shell
            # really runs -- a false negative introduced by the fix for a false positive.
            # It also appended spaces without advancing the read cursor, which desynchronised
            # the mask from the source and silently corrupted every later offset.
            nl = cmd.find("\n", k)
            if nl < 0:
                out.append(" " * (len(cmd) - k)); break
            out.append(" " * (nl - k)); k = nl
            continue
        if ch in "'\"":
            quote = ch; out.append(ch); k += 1; continue
        out.append(ch); k += 1
    return "".join(out).ljust(len(cmd))[:len(cmd)]


def _mask_quoted(command):
    """Blank out quoted spans so separators INSIDE them cannot create a fake statement.

    Peer review fixture: `echo 'graph search; entire graph query --repo .'` split on the
    quoted semicolon and the tail became an "invocation". Length is preserved so offsets
    stay valid for the caller.
    """
    out, quote = [], None
    for ch in command or "":
        if quote:
            out.append(" " if ch != quote else ch)
            if ch == quote: quote = None
        elif ch in "'\"":
            quote = ch; out.append(ch)
        else:
            out.append(ch)
    return "".join(out)


def stages(command):
    """Leading stage of each statement -- the only position a real invocation occupies."""
    out = []
    masked = _mask_shell(command or "")
    for lo, hi in _spans(masked):
        part = (command or "")[lo:hi]
        part = part.strip()
        if part:
            out.append(part)
    return out


def _spans(masked):
    """Statement boundaries computed on the MASKED text, applied to the original."""
    spans, start = [], 0
    for m in SPLIT.finditer(masked):
        spans.append((start, m.start())); start = m.end()
    spans.append((start, len(masked)))
    return spans


def argv_of(stage):
    try:
        return shlex.split(stage)
    except ValueError:
        return stage.split()


def graph_invocation(command):
    """(verb, argv) when a stage really invokes the graph, else None.

    Returns None for an event whose execution or output the transcript cannot settle. Peer
    review's guidance, and it is the right one: REJECT the ambiguous event rather than invent
    an execution model for it. A transcript records the command text and the combined result,
    never which stage produced which bytes or whether a guarded stage ran at all.
    """
    for stage in stages(command):
        argv = argv_of(stage)
        i = 0
        while i < len(argv) and ("=" in argv[i] and not argv[i].startswith("-")):
            i += 1  # leading VAR=value assignments
        # `env` and `env FOO=1` wrap the real command; the head was being read as `env` and
        # the invocation missed entirely.
        while i < len(argv) and os.path.basename(argv[i]) == "env":
            i += 1
            while i < len(argv) and ("=" in argv[i] and not argv[i].startswith("-")):
                i += 1
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


def output_is_attributable(command):
    """Can the recorded result be credited to the graph stage at all?

    It could not, and this was the most persistent defect in the classifier: the whole Bash
    result was handed to whichever stage was detected first. Two counterexamples from review,
    both of which scored as clean graph renders:

        entire graph query ... >/dev/null; printf '1. a/b.go:1 F\n2. c/d.go:2 G'

    -- the graph's output was DISCARDED and printf produced every byte that was scored. And
    any multi-stage pipeline at all, where the tail transforms what the graph emitted.

    So attribution now requires exactly one statement whose stdout goes nowhere else.
    """
    masked = _mask_shell(command or "")
    # CONDITIONAL EXECUTION. `false && entire graph query ...` never runs, and nothing in the
    # record says whether the guard passed. A first repair refused such commands outright,
    # which also threw away `cd /r && entire graph impact` -- a real invocation, in one of the
    # commonest shapes there is. The uncertainty is about WHETHER IT RAN and what produced the
    # bytes, so it belongs here, in attribution, and the event lands in the unknown tier
    # instead of being deleted or credited.
    if "&&" in masked or "||" in masked:
        return False
    if len([x for x in stages(command) if x.strip()]) != 1:
        return False
    # A redirect or a pipe means the bytes in the record came from somewhere else.
    return not re.search(r"(?<![0-9<>])>|\||\btee\b", masked)


def flag_value(argv, name):
    for i, a in enumerate(argv):
        if a == name and i + 1 < len(argv):
            return argv[i + 1]
        if a.startswith(name + "="):
            return a.split("=", 1)[1]
    return None


def recorded_source_identity(argv, body=""):
    """Does this recorded call carry the identity of the tree it ran against?

    TWICE WRONG BEFORE THIS. First it looked for --rev/--revision/--commit/--at, none of which
    exist -- the binary rejects each, so a command carrying one never ran. Then it was
    hardcoded False on the reasoning that no CLI flag binds a revision, and "exact replay is
    unreachable by construction" was reported as a finding.

    That was wrong too, and peer review caught it with output rather than argument: the JSON
    payload ALREADY EMITS commit and tree. Verified directly --
        commit db90c908f928565d...  tree fb0c6dfb62bbc9a0...
    So the identity was never in the argv, it was in the RESULT, and I had been auditing the
    wrong half of the record. Absence of a flag is not absence of provenance.

    Still NOT sufficient for full-execution replay on its own: the binary version, the option
    set and the working directory remain unqualified, and a text/agent-format call carries no
    envelope at all. So this reports the identity as PRESENT and the tier stays conservative
    until the rest is qualified -- a candidate, not a certification.
    """
    if not body:
        return False
    stripped = body.lstrip()
    if not stripped.startswith("{"):
        return False
    try:
        d = json.loads(stripped)
    except Exception:
        return False
    if not isinstance(d, dict):
        return False
    commit, tree = d.get("commit"), d.get("tree")
    # BOTH must be real object names. The first repair validated only `commit` and accepted
    # any truthy `tree`, so a fabricated `tree: "b"` qualified. Checking one half of a pair
    # and trusting the other is how the original always-false bug got replaced by a
    # false positive.
    return bool(HEX40.match(str(commit or "")) and HEX40.match(str(tree or "")))


def payload_has_structure(body):
    """Is this a graph payload we could re-render, or merely rank-SHAPED text?

    Peer review defeated the first version with `printf '1. a.go:1\n2. b.go:2'` and with
    `{"results":[{"rank":1}]}` -- both passed a check that only counted shapes. A payload we
    can re-render has to carry the fields a renderer consumes, so:
      text  >=2 ranked entries that each name a plausible source path AND a line number
      json  a results array whose first entry carries file_path and a line
    An explicitly truncated payload is refused outright: it is not the artifact the call
    produced, so re-rendering it compares against something that never existed.
    """
    if not body:
        return False
    low = body.lower()
    # Case-insensitive: the harness emits "[Output truncated]" capitalised, which walked
    # straight past the lowercase-only test.
    if "[truncated" in low or "(truncated" in low or "output truncated" in low:
        return False
    hits = [m for m in RANK.finditer(body)]
    good = 0
    for m in hits:
        line = body[m.start():body.find("\n", m.end()) if body.find("\n", m.end()) > 0 else len(body)]
        path = line.split(".", 1)[1].strip().split(":")[0] if "." in line else ""
        base = os.path.basename(path or "")
        # a real source path has a directory or an extension; `1. one` and `printf`-shaped
        # text have neither, and peer review passed both through the old check
        if ("/" in path and "." in base) or re.match(r"^[\w.-]+\.[A-Za-z]{1,5}$", base):
            good += 1
    if good >= 2:
        return True
    stripped = body.lstrip()
    if stripped.startswith("{"):
        try:
            d = json.loads(stripped)
        except Exception:
            return False
        rs = d.get("results") if isinstance(d, dict) else None
        if not isinstance(rs, list) or not rs or not isinstance(rs[0], dict):
            return False
        r0 = rs[0]
        # file_path + start_line is a POINTER, not a payload. Re-rendering needs what the
        # renderer consumes; a two-field stub is indistinguishable from something hand-written
        # and was accepted as a re-renderable artifact.
        if not (r0.get("file_path") and r0.get("start_line") is not None):
            return False
        return any(r0.get(k) not in (None, "", [], {}) for k in
                   ("snippet", "body", "symbol_name", "end_line", "signals", "score"))
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
        if not isinstance(d, dict):
            continue                      # a top-level JSON list reached .get and crashed
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
    if not isinstance(block, dict):
        return ""
    con = block.get("content")
    if isinstance(con, str):
        return con
    if isinstance(con, list):
        # join with newline, not space: space-joined blocks merge adjacent rank lines into
        # one and the ranked-entry count silently halves
        return "\n".join(str(x.get("text", "")) for x in con if isinstance(x, dict))
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
            binp = b.get("input")
            if not isinstance(binp, dict):
                continue                  # a string-valued Bash input reached .get and crashed
            cmdtext = str(binp.get("command", ""))
            inv = graph_invocation(cmdtext)
            if not inv:
                continue
            _, argv = inv
            rblock = res.get(uid, {})
            # An errored result is not an artifact: it records a failure, not a payload.
            body = "" if rblock.get("is_error") else result_text(rblock)
            if not output_is_attributable(cmdtext):
                # The command ran, but the transcript cannot say which stage produced the
                # recorded bytes. Unknown is the honest tier; crediting the graph is how
                # printf-shaped text became a graph render.
                tiers["unattributable-output"] += 1
                reasons["result cannot be attributed to the graph stage"] += 1
                continue
            if recorded_source_identity(argv, body):
                # A CANDIDATE, not a certification, and the previous version promoted it
                # straight to the top tier while its own docstring said it must not.
                # Exact replay additionally needs the binary identity, the build, the working
                # directory, the full option set and the source's clean status -- none of
                # which a transcript records. So full-execution is not zero here, it is
                # UNKNOWN, and nothing in a transcript can raise it.
                tiers["source-identity-candidate"] += 1
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
            if not recorded_source_identity(argv, body):
                reasons["no source revision bound to the tree"] += 1
            if not flag_value(argv, "--repo"):
                reasons["--repo absent (cwd-inherited; tree not recoverable)"] += 1
    return tiers, reasons, len(files)



def _tmpjsonl(line):
    import tempfile
    d = tempfile.mkdtemp(prefix="rq-")
    p = os.path.join(d, "t.jsonl")
    with open(p, "w") as fh:
        fh.write('{"type":"x","tool_use":1,"tool_result":1}\n' + line + "\n")
    return p

def _qualify_str_input():
    """A Bash block whose `input` is a bare string reached .get and raised."""
    import tempfile
    d = tempfile.mkdtemp(prefix="rq-")
    with open(os.path.join(d, "t.jsonl"), "w") as fh:
        fh.write(json.dumps({"message": {"content": [
            {"type": "tool_use", "id": "u1", "name": "Bash", "input": "entire graph query --repo ."}]}}) + "\n")
    try:
        qualify(d); return True
    except Exception:
        return False

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
    # Superseded, and the correction is the finding: --rev/--revision/--commit/--at DO NOT
    # EXIST. The binary rejects each with "query does not accept --rev", so a command carrying
    # one never ran. No CLI flag binds a call to a revision, which makes full-execution replay
    # unreachable by construction rather than merely unobserved.
    check("phantom --rev does not count",
          recorded_source_identity(argv_of("entire graph query --rev " + "a" * 40)), False)
    # CORRECTED. This asserted that a one-character tree is an identity, which is precisely
    # the false positive found later: only `commit` was validated and any truthy `tree` passed.
    check("json commit+tree IS an identity",
          recorded_source_identity([], '{"commit":"'+"a"*40+'","tree":"'+"b"*40+'"}'), True)
    check("json without commit is not",
          recorded_source_identity([], '{"tree":"b"}'), False)
    check("--head alone is not an identity",
          recorded_source_identity(argv_of("entire graph query --head --repo .")), False)
    check("one rank line is not an artifact", payload_has_structure("1. a.go:1 foo s=1"), False)
    check("two rank lines are", payload_has_structure("1. a.go:1 foo\n2. b.go:2 bar"), True)
    # Superseded: a results array whose entries carry no file_path/start_line is rank-SHAPED,
    # not re-renderable. The stricter `real json accepted` / `bare rank json refused` pair below
    # replaces this, and the two directly contradicted until this one was corrected.
    # CORRECTED. "real fields" meant file_path + start_line, a pointer rather than a payload;
    # the shape below is what the binary actually emits (see go-role-a0c-head-baseline.raw.json).
    check("json envelope with real fields is", payload_has_structure(
        '{"results":[{"rank":1,"file_path":"a.go","start_line":1,"end_line":4,'
        '"symbol_name":"F","snippet":"func F() {}"}]}'), True)
    check("empty json envelope is not", payload_has_structure('{"results":[]}'), False)
    # END-TO-END, because the first version's fixtures only ever called the small helpers and
    # never qualify/tool_events/result_text -- so a corpus-level regression could not fail it.
    import tempfile, json as _j
    with tempfile.TemporaryDirectory() as td:
        proj = os.path.join(td, "proj"); os.makedirs(proj)
        def rec(t, **kw):
            return _j.dumps({"message": {"content": [dict(type=t, **kw)]}})
        rows = [
            rec("tool_use", id="u1", name="Bash", input={"command": "echo 'graph query'"}),
            rec("tool_use", id="u2", name="Bash", input={"command": "entire graph query --repo ."}),
            rec("tool_result", tool_use_id="u2",
                content="1. a/b.go:10 Foo s=1\n2. c/d.go:20 Bar s=2\n"),
            rec("tool_use", id="u3", name="Bash", input={"command": "entire graph query --repo ."}),
            rec("tool_result", tool_use_id="u3", is_error=True, content="boom"),
            '{"message": {"content": "not-a-list"}}',
            "{ malformed",
        ]
        open(os.path.join(proj, "s.jsonl"), "w").write("\n".join(rows))
        tiers, _, nfiles = qualify(td)
        check("e2e: files discovered", nfiles, 1)
        check("e2e: echo excluded, 2 real calls", sum(tiers.values()), 2)
        check("e2e: frozen-render", tiers["frozen-render"], 1)
        check("e2e: errored result not frozen", tiers["neither"], 1)
        check("e2e: full-execution unreachable", tiers["full-execution"], 0)
        # Corrected, not extended: this asserted a SPACE join, which merges adjacent rank
        # lines into one and silently halves the ranked-entry count that frozen-render
        # depends on. The fixture was pinning the defect.
        check("result_text on list content",
              result_text({"content": [{"text": "x"}, {"text": "y"}]}), "x\ny")
    check("printf ranks refused", payload_has_structure("1. one\n2. two"), False)
    check("shell comment is not an invocation",
          graph_invocation("# entire graph query --repo ."), None)
    check("escaped separator makes no statement",
          graph_invocation("echo a\\; entire graph query --repo ."), None)
    check("malformed block does not crash", result_text("not-a-dict"), "")
    check("list content joins on newline",
          result_text({"content": [{"text": "1. a/b.go:1 F"}, {"text": "2. c/d.go:2 G"}]}).count("\n"), 1)
    check("bare words are not source paths", payload_has_structure("1. alpha\n2. beta"), False)
    check("truncated payload refused",
          payload_has_structure("1. a/b.go:1 F\n2. c/d.go:2 G\n[truncated]"), False)
    check("bare rank json refused", payload_has_structure('{"results":[{"rank":1}]}'), False)
    # CORRECTED, not relaxed. This fixture used to assert that
    #   {"results":[{"rank":1,"file_path":"a.go","start_line":3}]}
    # is a "real" payload. It is a POINTER -- three fields, indistinguishable from something
    # typed by hand -- and calling it real is what let thin JSON qualify as a re-renderable
    # artifact. The authorized capture go-role-a0c-head-baseline.raw.json shows what the
    # binary actually emits per result: 18 fields including snippet, symbol_name, end_line,
    # score and signals. The fixture now uses that shape, and the thin one is a falsifier.
    check("real json accepted", payload_has_structure(
        '{"results":[{"rank":1,"file_path":"a.go","start_line":3,"end_line":9,'
        '"symbol_name":"F","snippet":"func F() {}","score":21.9,"signals":["complete-symbol"]}]}'), True)
    check("a file_path+start_line pointer is not a payload",
          payload_has_structure('{"results":[{"rank":1,"file_path":"a.go","start_line":3}]}'), False)
    check("capitalised truncation is refused",
          payload_has_structure("1. a/b.go:1 F\n2. c/d.go:2 G\n[Output truncated]"), False)
    # `false && ...` NAMES an invocation -- the text is right there -- but whether the shell
    # reached it is unknowable from a transcript. So it is detected and then refused
    # attribution, rather than being counted as a clean render or silently dropped.
    check("false && still names an invocation",
          (graph_invocation("false && entire graph query --repo /synthetic") or ("",))[0], "query")
    check("false && is not attributable",
          output_is_attributable("false && entire graph query --repo /synthetic"), False)
    check("cd && graph is a real invocation",
          (graph_invocation("cd /r && entire graph impact --symbol X") or ("",))[0], "impact")
    check("env wrapper still names an invocation",
          (graph_invocation("env FOO=1 entire graph query --repo .") or (None,))[0], "query")
    check("a comment line does not hide the next command",
          (graph_invocation("# note\nentire graph query --repo .") or (None,))[0], "query")
    check("redirected graph output is not attributable",
          output_is_attributable("entire graph query --repo . >/dev/null; printf '1. a/b.go:1 F'"), False)
    check("a lone invocation is attributable",
          output_is_attributable("entire graph query --repo ."), True)
    check("a piped invocation is not", output_is_attributable("entire graph query --repo . | head"), False)
    check("a one-character tree is not an identity",
          recorded_source_identity([], '{"commit":"' + "a"*40 + '","tree":"b"}'), False)
    check("two real object names are",
          recorded_source_identity([], '{"commit":"' + "a"*40 + '","tree":"' + "b"*40 + '"}'), True)
    check("a top-level JSON list does not crash", tool_events(_tmpjsonl("[1,2,3]")), [])
    check("a string Bash input does not crash", _qualify_str_input(), True)
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
