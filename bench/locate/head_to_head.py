#!/usr/bin/env python3
"""Cost to locate a known target: graph vs grep. Deterministic, free, no agent.

WHY THIS ESCAPES THE COUNTERFACTUAL. Every savings estimate in this project has died on
the same rock: you cannot observe what an agent WOULD have read, so "displacement" is
unmeasurable from transcripts. That objection is about agent BEHAVIOUR. It does not apply
to the question actually being asked -- "does the graph cost less than grep" -- because
cost-to-locate-a-known-target is a property of the TOOLS, and both arms can be executed.

THE MODEL, v2. The first version charged each arm for reading ranked files until the target
appeared. That simulation dominated every result (graph 169 kB, prose-grep 2.6 MB per case)
and was speculative in both directions -- no agent reads 30 files, and none pastes 2.6 MB of
grep output. Discarded.

What is left is the part that needs no speculation:
    arm_cost = BYTES THE TOOL PUTS IN CONTEXT FOR ONE CALL
    located   = is the target's DEFINITION visible in that output
One call, one payload, measured. No modelling of what happens next. An arm that does not
locate the target is reported as a miss rather than assigned an invented follow-up cost.
Cost and success are reported separately and never combined into a single score, because
combining them is exactly where the invented modelling crept in.

GREP ARM: the query an agent would actually type. Labels are prose (a doc comment), so the
arm greps the most distinctive content word -- longest non-stopword -- exactly as an agent
reaching for ripgrep would. Reported alongside is an ORACLE grep on the target's own name,
which is the most generous possible grep and a ceiling on that arm.

Read-only. Fixture revision printed; must be clean.
"""
import subprocess, sys, os, re, json, random, hashlib

# The END of a ranked header's range is captured, not just its start. A header reading
# `file.go:142-143` was read as line 142 alone, so a target beginning at 143 scored a MISS even
# though the rendered range contains it and the focus line is 143 -- found by an independent
# receipt audit, one case in twenty.
RANK = re.compile(r'^\s*(\d+)\.\s+(\S+?):(\d+)(?:-(\d+))?')
WORD = re.compile(r'[A-Za-z]{3,}')

STOP = set("""the and for that with this from into when what which whether such been were are
was has have had not but its it's only also then than they them their there here does doing
done any all can may must should would could each other some more most less use used using
via per off out over under above below before after while during about against between""".split())


def DEFN(name):
    """Does this line DECLARE `name`, in any language under test?

    A bare mention must not count, or the oracle scores a hit on every call site.

    The TypeScript arms were extended after the oracle scored 17/20 on a TS fixture while
    scoring 20/20 everywhere else -- a gap that looked like a finding about TypeScript and
    was a gap in this function. The three misses were an accessor (`get timeOrigin(): number`)
    and class methods carrying visibility or static modifiers, which the original
    start-of-line pattern could not reach.
    """
    n = re.escape(name)
    mods = r'(?:(?:public|private|protected|static|readonly|override|abstract|async)\s+)*'
    return re.compile(
        r'\bfunc\b[^/]*\b' + n + r'\b'                          # Go func / method
        r'|\b(?:function|class|interface|type|enum)\s+' + n + r'\b'  # TS/JS declarations
        r'|\b(?:const|let|var)\s+' + n + r'\s*[:=]'               # const Name = (...) =>
        r'|\b(?:get|set)\s+' + n + r'\s*\('                       # accessor
        r'|^\s*' + mods + n + r'\s*[(<]'                          # class method, any modifiers
        r'|^\s*' + mods + n + r'\s*[:=]\s*(?:async\s*)?[(<]'       # property holding a function
    )


def doc_comment_lines(path, start_line, language="Go"):
    """The doc comment as its PHYSICAL LINES, un-joined.

    doc_comment() space-joins the lines, and peer review showed why that silently breaks the
    strongest baseline: a phrase built from the joined text can straddle a newline, so `rg -F`
    searches for a string that occurs in NO file. The old phrase arm did exactly that, and its
    20/100 coverage was partly an artifact of searching for text that cannot exist.
    """
    try: lines = open(path, errors="ignore").read().splitlines()
    except OSError: return []
    out, i = [], start_line - 2
    while i >= 0 and lines[i].strip().startswith("//"):
        out.append(lines[i].strip().lstrip("/").strip()); i -= 1
    if out:
        return list(reversed(out))
    if language in ("TypeScript", "JavaScript", "TSX"):
        j = start_line - 2
        while j >= 0 and not lines[j].strip():
            j -= 1
        if j >= 0 and lines[j].strip().endswith("*/"):
            block = []
            while j >= 0:
                t = lines[j].strip()
                block.append(t.lstrip("/*").rstrip("*/").lstrip("*").strip())
                if t.startswith("/**"): break
                j -= 1
            return [w for w in reversed(block) if w and not w.startswith("@")]
    return []

def doc_comment(path, start_line, language="Go"):
    """Author-written description immediately above a declaration.

    Go/TS take contiguous // lines; TS also accepts a JSDoc /** ... */ block, which is where
    TypeScript authors actually write. Extending past Go was required to test whether the
    head-to-head result is a Go artifact -- a single-language benchmark cannot tell you that.
    """
    try: lines = open(path, errors="ignore").read().splitlines()
    except OSError: return ""
    out, i = [], start_line - 2
    while i >= 0 and lines[i].strip().startswith("//"):
        out.append(lines[i].strip().lstrip("/").strip()); i -= 1
    if out:
        return " ".join(reversed(out))
    if language in ("TypeScript", "JavaScript", "TSX"):
        j = start_line - 2
        while j >= 0 and not lines[j].strip():
            j -= 1
        if j >= 0 and lines[j].strip().endswith("*/"):
            block = []
            while j >= 0:
                t = lines[j].strip()
                block.append(t.lstrip("/*").rstrip("*/").lstrip("*").strip())
                if t.startswith("/**"): break
                j -= 1
            return " ".join(w for w in reversed(block) if w and not w.startswith("@"))
    return ""

def build_cases(binary, repo, want, seed=7):
    sp = subprocess.run([binary,"symbols","--repo",repo,"--format","ndjson"],
                        capture_output=True,text=True,timeout=1800)
    # A failed or signal-killed `symbols` still prints what it managed. Building the case list
    # from a truncated stream silently samples whatever happened to be emitted first.
    if sp.returncode != 0:
        raise SystemExit(f"symbols failed in {repo} (exit {sp.returncode}); refusing partial cases")
    raw = sp.stdout
    out=[]
    for line in raw.splitlines():
        try: d=json.loads(line)
        except Exception: continue
        if d.get("record_type")!="symbol": continue
        lang=d.get("language")
        # Extended past Go deliberately: a single-language benchmark cannot tell you whether
        # its own result is a language artifact.
        if lang not in ("Go","TypeScript","JavaScript","TSX"): continue
        if d.get("kind") not in ("function","method","class"): continue
        name=d.get("name") or ""
        if len(name)<6: continue
        src_path=os.path.join(repo,d["file_path"])
        doc=doc_comment(src_path, d["start_line"], lang)
        dlines=doc_comment_lines(src_path, d["start_line"], lang)
        if len(WORD.findall(doc))<10: continue
        # THE CANONICAL QUERY. One byte-identical string handed to every arm.
        #
        # Peer ruling, after this benchmark was caught giving the grep baseline the original
        # doc while the graph got a stripped one: every evaluated arm receives IDENTICAL
        # stripped bytes, and an arm may derive its search FROM THOSE BYTES ONLY -- never from
        # the original doc, the target name, its span, or a hit. Line breaks are preserved
        # because a lexical arm needs to know where contiguous source text ends, and they are
        # preserved IDENTICALLY for everyone rather than handed to one arm as side knowledge.
        # The hole a removed name leaves is a real discontinuity in the source text, so it
        # becomes a LINE BREAK rather than whitespace. The previous version blanked it to a
        # space and asked the lexical arm to infer the gap from a run of two spaces -- which
        # fails the moment the name touches punctuation: `the (ApplyRetryBudget) helper`
        # became `the ( ) helper`, single-spaced, and the arm searched the source for a
        # string that is not in it. Every arm sees the same breaks.
        q = "\n".join(strip_name(l, name).replace("\x00", "\n").rstrip() for l in dlines)
        q = "\n".join(l for l in (x.strip() for x in q.splitlines()) if l)[:240]
        if len(WORD.findall(q))<8: continue
        out.append((q,name,d["file_path"],int(d.get("start_line") or 0),int(d.get("end_line") or 0)))
    random.Random(seed).shuffle(cases:=out)
    return cases[:want]

def fsize(repo,rel):
    try: return os.path.getsize(os.path.join(repo,rel))
    except OSError: return 0


def declaration_source_line(repo, target_file, name, lo, hi):
    """The EXACT text of the target's declaration line, read from the file.

    The scorer used to credit `declaration` on a REGEX match against whatever an arm rendered,
    and never looked at the source. Three things therefore scored a declaration: a whole
    declaration, a declaration CLIPPED mid-line (`func TargetAlpha(`), and text that was never
    in any file at all. Only the first is a declaration a reader can act on.

    That was not a symmetric error either, which is what makes it serious. rg emits whole
    lines; the graph's snippets are truncated to fit a byte budget, so the clipped form arises
    for ONE ARM ONLY and inflated exactly the arm this benchmark is meant to scrutinise.

    It is also the same defect class that killed an entire PR of mine on the product side: a
    bounds- or shape-check standing in for a source check. Grounding the metric in the file is
    the fix in both places.
    """
    d = DEFN(name)
    try:
        with open(os.path.join(repo, target_file), errors="ignore") as fh:
            lines = fh.read().splitlines()
    except OSError:
        return ""
    for num in range(max(1, lo), min(hi, len(lines)) + 1):
        text = lines[num - 1]
        if d.search(text):
            return text.strip()
    return ""


def score(out_bytes, repo, name, target_file, lo, hi, ranked_only=False, decl_text=None):
    r"""Two metrics, computed IDENTICALLY for every arm. Returns (locator, declaration).

    locator      the arm named the target's file at a line inside the target's span
    declaration  the arm's output contains the target's declaration, IN THE TARGET'S FILE,
                 and inside its span

    Three defects peer review found in the previous version, all of which inflated
    `declaration`:
      * body text was scanned for a declaration without checking WHICH result it belonged
        to, so a declaration inside another file's rendered body scored a hit. Body lines
        are now attributed to the header above them.
      * in the grep path the declaration check was not gated on the span at all, so a
        same-named declaration elsewhere in the right file counted.
      * DEFN was applied to whole `path:line:text` lines, and its start-anchored TypeScript
        patterns (`^\s*private async m(`) can never match once a path prefix is present.
        The text is now split off before matching.
    """
    tgt = os.path.normpath(target_file)
    locator = declaration = False
    d = DEFN(name)
    # Read from the file unless the caller already has it. A declaration is credited only when
    # the arm rendered the SOURCE LINE, not when it rendered something the pattern likes.
    if decl_text is None:
        decl_text = declaration_source_line(repo, target_file, name, lo, hi)

    def shows_declaration(text):
        # EQUALITY, after the framing is stripped -- not containment. `decl_text in text`
        # accepted the exact source line PLUS anything appended to it, so
        #     func TargetAlpha() {} AND SOME FABRICATION
        # scored a declaration. Containment was a weaker check wearing the name of a stronger
        # one, which is the third time that exact substitution has appeared in this file.
        #
        # Body lines render as raw source with their original indentation and no gutter, and
        # the grep path has already split the path:line framing off, so both arrive as a bare
        # source line and .strip() is the whole of the normalisation needed.
        return bool(decl_text) and text.strip() == decl_text

    def in_target(path, num):
        if os.path.isabs(path):
            path = os.path.relpath(path, repo)
        if os.path.normpath(path) != tgt:
            return False, 0
        try:
            line = int(num)
        except ValueError:
            return True, 0
        return True, line

    current_is_target = False          # which result the following body lines belong to
    for ln in out_bytes.decode("utf8", "replace").splitlines():
        if ranked_only:
            m = RANK.match(ln)
            if m:
                ok, line = in_target(m.group(2), m.group(3))
                end = int(m.group(4)) if m.group(4) else line
                # A RANGE OVERLAPS A SPAN; a start point does not. Testing only the start meant
                # a header whose rendered range covers the target still scored a miss whenever
                # the range began one line above it -- a doc comment, an annotation, a brace.
                current_is_target = ok and (not lo or (line <= hi and end >= lo))
                if current_is_target:
                    locator = True
                # A HEADER IS NOT A DECLARATION. The header names the symbol, so for a target
                # called TargetAlpha the line `1. a.go:1-5 TargetAlpha [complete] s=1` matched
                # DEFN and earned a declaration credit with no source body rendered at all --
                # exactly the locator/declaration conflation the split metric exists to
                # prevent. Only body lines beneath a target header can establish it.
                continue
            # body text: only creditable when the header above it was the target
            if current_is_target and shows_declaration(ln):
                declaration = True
            continue
        parts = ln.split(":", 2)
        if len(parts) < 3:
            continue
        path, num, text = parts[0], parts[1], parts[2]
        ok, line = in_target(path, num)
        if not ok:
            continue
        if lo and not (lo <= line <= hi):
            continue                    # outside the target's span: neither metric
        locator = True
        # The TEXT, never the path-prefixed line -- and it must be the SOURCE declaration,
        # not merely something DEFN likes. See declaration_source_line.
        if shows_declaration(text):
            declaration = True
    return locator, declaration

def strip_name(text, name):
    """Blank `name` and its case-split parts, marking each removal so callers can tell where
    the text is still CONTIGUOUS. The graph is asked with a name-stripped query; giving the
    grep baseline the original doc hands it the answer, which is the arm-asymmetric-input
    failure this whole benchmark exists to avoid."""
    t = re.sub(re.escape(name), "\x00", text, flags=re.I)
    for part in re.findall(r'[A-Z]?[a-z]{3,}', name):
        t = re.sub(r'\b' + re.escape(part) + r'\b', "\x00", t, flags=re.I)
    return t

def phrase_for(q, min_words=3):
    """The longest run of consecutive words inside ONE LINE OF THE CANONICAL QUERY.

    Derived from q alone -- the same bytes every other arm receives -- which is what makes
    this a fair baseline rather than a second channel into the source. Because q's lines are
    the doc comment's physical lines with the name blanked, a surviving run within one of them
    is still verbatim source text, so `rg -F` can match it.

    Two defects this replaces: the arm dropped words shorter than four characters and joined
    the survivors with spaces, across a comment whose lines had ALREADY been space-joined, so
    it searched for a fixed string occurring in no file; and it read those lines from the
    original doc, which the graph never sees."""
    best = ""
    for line in q.splitlines():
        seg = line.strip()
        # Every line of q is contiguous source text by construction: comment line breaks and
        # name removals are both encoded as breaks, so no heuristic is needed to find the
        # discontinuities -- which is what the two-space version got wrong around punctuation.
        if len(seg.split()) >= min_words and len(seg) > len(best):
            best = seg
    return best

def phrase_arm(repo, q, name, target_file, lo, hi, follow=12):
    """THE STRONG GREP BASELINE. Search the author's own description, then read what follows.

    Added after peer review destroyed the previous framing: the description IS IN THE FILE,
    immediately above the declaration, so grep a phrase from it and read on. Beating an
    OR-of-longest-words had been presented as proof that lexical search cannot bridge
    description to identifier; it proved only that a weak baseline is weak.

    REBUILT after a second review found three defects that all flattered the graph:

      1. It was handed the UNSTRIPPED doc, which usually contains the target name, while the
         graph got a name-stripped query. Different inputs -- the exact asymmetry this
         benchmark's fairness rule forbids. It now receives the byte-identical canonical q,
         and derives its phrase from that alone.
      2. Its phrase was non-adjacent words joined by spaces, across a space-joined multi-line
         comment. As a fixed string that matches nothing. See phrase_for.
      3. It scored itself, and set locator and declaration from ONE condition, so the two
         columns could never differ. I then read that forced equality off the results table
         and published it as a mechanism ("its weakness is coverage, not precision"). It was a
         property of this function. It now returns evidence and score() judges it, exactly as
         for every other arm -- which is what makes the shared-scoring claim true.
    """
    phrase = phrase_for(q)
    if not phrase:
        return 0, False, False, True, b"", {"argv": [], "rc": None, "stderr_bytes": b"", "note": "no phrase in q"}
    # `--` BEFORE THE PATTERN. Without it a pattern beginning with a dash is parsed as an rg
    # option: case 012 of the fx-cli run drew the phrase "--dry-run ..." out of a doc comment,
    # rg exited 2, and the whole case was dropped from EVERY arm. A literal taken from user text
    # has to be passed as a literal.
    p = subprocess.run(["rg", "-n", "--no-heading", "-F", "-g", "!node_modules", "--", phrase, repo],
                       capture_output=True, timeout=300)
    # rg exits 1 for "no matches", a real empty result. Above that is a broken run; BELOW zero
    # is signal death (SIGKILL is -9), which the original `> 1` guard admitted as a valid run
    # with empty output -- scoring a killed process as a legitimate miss.
    meta = {"argv": ["rg","-n","--no-heading","-F","-g","!node_modules","--",phrase,repo],
            "rc": p.returncode, "stderr_bytes": p.stderr}
    if p.returncode < 0 or p.returncode > 1:
        return 0, False, False, False, b"", meta
    cost = len(p.stdout)
    # Reconstruct exactly what a reader receives -- the hit line plus the next `follow` lines --
    # as path:line:text, so the SHARED scorer can judge it under the same span binding as
    # every other arm. Bytes are counted as bytes; the previous version added len() of decoded
    # str to len() of bytes and called the sum a byte count.
    seen = []
    for ln in p.stdout.decode("utf8", "replace").splitlines():
        parts = ln.split(":", 2)
        if len(parts) < 3: continue
        path, num = parts[0], parts[1]
        rel = os.path.relpath(path, repo) if os.path.isabs(path) else path
        try: start = int(num)
        except ValueError: continue
        try:
            with open(os.path.join(repo, rel), errors="ignore") as fh:
                src = fh.read().splitlines()
        except OSError:
            continue
        for off, body in enumerate(src[start - 1:start - 1 + follow]):
            seen.append(f"{rel}:{start + off}:{body}")
    # Charge what a reader actually receives, framing included. Counting only the raw source
    # bytes under-charged this arm by the `path:line:` prefix on every line it reads -- and
    # that prefix is part of the payload, not an artefact of the harness.
    ev = "\n".join(seen).encode("utf8")
    cost += len(ev)
    loc, dec = score(ev, repo, name, target_file, lo, hi)
    meta["evidence"] = ev
    return cost, loc, dec, True, p.stdout, meta

def grep_arm(repo,query,name,target_file,oracle=False,tgt_lo=0,tgt_hi=0):
    """Returns (bytes_in_context, located, ok). ok=False means the ARM FAILED and the case
    must be excluded, never scored as a retrieval miss (peer-review note 2)."""
    term = name if oracle else max((w for w in WORD.findall(query) if w.lower() not in STOP),
                                   key=len, default="")
    if not term: return 0, False, False, False, b"", {"argv": [], "rc": None, "stderr_bytes": b"", "note": "no term"}
    # `--` for the same reason as the phrase arm: the term comes from the query text.
    p=subprocess.run(["rg","-n","--no-heading","-g","!node_modules","--",term,repo],capture_output=True,timeout=300)
    # rg exits 1 for "no matches", which is a real empty result, not a failure. Anything
    # above that is a broken run and the case is dropped rather than counted as a miss.
    # rg exits 1 for "no matches", which is a real empty result. Anything ABOVE that is a
    # broken run -- and anything BELOW zero is a signal death (SIGKILL is -9), which the
    # original `> 1` guard let through as a valid run with empty output, scoring a killed
    # process as a legitimate miss. A timeout or OOM would have silently become evidence.
    meta = {"argv": ["rg","-n","--no-heading","-g","!node_modules","--",term,repo],
            "rc": p.returncode, "stderr_bytes": p.stderr,
            "oracle": bool(oracle)}
    if p.returncode < 0 or p.returncode > 1:
        return 0, False, False, False, b"", meta
    out=p.stdout
    loc, dec = score(out, repo, name, target_file, tgt_lo, tgt_hi)
    return len(out), loc, dec, True, out, meta

def graph_arm(binary,repo,query,name,target_file,budget,tgt_lo=0,tgt_hi=0):
    """Returns (bytes_in_context, located, ok). `located` is DISPLAYED-SYMBOL-NAME recall OR
    a header whose file and line span contain the preregistered target -- peer-review note 3:
    a locator-only or name-elided rendering must not read as zero retrieval."""
    p=subprocess.run([binary,"query","--repo",repo,"--query",query,"--format","agent",
                      "--max-context-bytes",str(budget),"--no-cache"],
                     capture_output=True,timeout=900)
    meta = {"argv": [binary,"query","--repo",repo,"--query",query,"--format","agent",
                     "--max-context-bytes",str(budget),"--no-cache"],
            "rc": p.returncode, "stderr_bytes": p.stderr}
    # != 0 already covers signal death here (negative codes), unlike the grep guards above.
    if p.returncode != 0:
        return 0, False, False, False, b"", meta
    out=p.stdout
    loc, dec = score(out, repo, name, target_file, tgt_lo, tgt_hi, ranked_only=True)
    return len(out), loc, dec, True, out, meta


# ---------------------------------------------------------------------------
# SYNTHETIC FALSIFIERS for the phrase arm. Each targets one defect peer review
# found, and each FAILS on the code as it stood before this commit. Run: --test
# ---------------------------------------------------------------------------
# EVERY temp directory this suite makes, so the run can remove them. Without it each --test
# leaked its fixtures: a single sweep runs the suite dozens of times, the seal cases BUILD A GO
# BINARY into a fresh directory each pass, and by the end of one session 15,946 directories and
# 1.1 GB were sitting in TMPDIR. Nothing failed, which is why nobody noticed.
_TEMPDIRS = []


def _mkdtemp(prefix):
    import tempfile
    d = tempfile.mkdtemp(prefix=prefix)
    _TEMPDIRS.append(d)
    return d


def _cleanup_tempdirs():
    """Remove the tracked directories and report how many ACTUALLY went.

    The first version returned len(_TEMPDIRS) whether or not anything was removed, so a broken
    cleanup reported "removed 25" beside 25 survivors. A count of what was attempted, presented
    as a count of what was done, is the same defect this whole directory keeps turning up.
    """
    import shutil
    gone = 0
    for d in _TEMPDIRS:
        shutil.rmtree(d, ignore_errors=True)
        if not os.path.exists(d):
            gone += 1
    _TEMPDIRS.clear()
    return gone


def _mkrepo(files):
    import tempfile
    d = _mkdtemp(prefix="pharm-")
    for rel, body in files.items():
        fp = os.path.join(d, rel)
        os.makedirs(os.path.dirname(fp), exist_ok=True)
        with open(fp, "w") as fh: fh.write(body)
    return d

def _canonical_q(dlines, name):
    """Exactly what build_cases produces, so the tests exercise the real query bytes."""
    q = "\n".join(strip_name(l, name).replace("\x00", "\n").rstrip() for l in dlines)
    return "\n".join(l for l in (x.strip() for x in q.splitlines()) if l)[:240]

def _finish_tempdirs():
    """Remove everything allocated and return the OWNED paths that survived.

    Called from a `finally`, so a suite that raises -- an exception, a timeout, a SystemExit
    from a refusal under test -- still cleans up. Cleanup at the normal tail only meant the
    failure paths, which are the ones that allocate most and are hit most during a mutation
    sweep, leaked every time.
    """
    owned = list(_TEMPDIRS)
    _cleanup_tempdirs()
    return [d for d in owned if os.path.exists(d)]


def _selftest_body(require_orchestration=False):
    fails = []
    skipped = []
    def ck(cond, label, detail=""):
        print(("  ok   " if cond else "  FAIL ") + label + (("  <- " + detail) if detail and not cond else ""))
        if not cond: fails.append(label)

    dl = ["ApplyRetryBudget decides whether a request may be", "retried after a transient failure."]
    q = _canonical_q(dl, "ApplyRetryBudget")

    # 0. EQUAL INPUTS. The ruling this instrument broke: every arm gets the same bytes and may
    #    derive its search from those bytes only. phrase_for's signature is the structural
    #    proof -- it cannot reach the name, the doc, the span or a hit, because it is not
    #    given them.
    import inspect
    ck(list(inspect.signature(phrase_for).parameters) == ["q", "min_words"],
       "the phrase arm can see only the canonical query", str(inspect.signature(phrase_for)))
    ck(name_absent := not any(p_.lower() in q.lower() for p_ in ("apply", "retry", "budget")),
       "the canonical query carries no part of the target name", repr(q))

    # 1. CONTIGUITY. The phrase must occur verbatim in a source line. The old arm dropped words
    #    under four characters and space-joined the rest, so it searched for a string in no file.
    ph = phrase_for(q)
    ck(ph and any(ph in l for l in dl), "phrase is a verbatim substring of one source line", repr(ph))
    old = " ".join([w for w in (" ".join(dl)).split() if len(w) > 3][:4])
    ck(not any(old in l for l in dl), "the OLD construction was indeed unmatchable", repr(old))

    # 2. THE TAUTOLOGY. locator and declaration came from one condition, so they could never
    #    differ -- and I read that forced equality off the table and published it as the
    #    mechanism. Here the phrase sits far above the declaration, so a bounded window
    #    locates without ever showing it.
    far = "package p\n// decides whether a request may be\n" + ("//\n" * 40) + \
          "// tail\nfunc ApplyRetryBudget() bool { return true }\n"
    r = _mkrepo({"a.go": far})
    n = far[:far.index("func ApplyRetryBudget")].count("\n") + 1
    _, l, d, ok, _, _ = phrase_arm(r, _canonical_q(["decides whether a request may be"], "ApplyRetryBudget"),
                                "ApplyRetryBudget", "a.go", 2, n)
    ck(ok and l and not d, "locator TRUE and declaration FALSE is now reachable", f"loc={l} dec={d}")

    # 3. SPAN BINDING. A same-name declaration in the right file but outside the registered
    #    span was credited.
    two = "package p\n// decides whether a request may be\nfunc ApplyRetryBudget() bool { return true }\n" \
          + ("\n" * 30) + "func ApplyRetryBudget2() bool { return false }\n"
    r2 = _mkrepo({"b.go": two})
    _, _, d2, ok2, _, _ = phrase_arm(r2, _canonical_q(["decides whether a request may be"], "ApplyRetryBudget"),
                                  "ApplyRetryBudget", "b.go", 34, 40)
    ck(ok2 and not d2, "declaration outside the registered span is refused", f"dec={d2}")

    # 4. BYTES, not characters. The old cost added len() of a decoded str to len() of bytes.
    acc = "package p\n// decides whether a request may be\nfunc ApplyRetryBudget() bool { return \"" + ("\u00e9" * 10) + "\" != \"\" }\n"
    qq = _canonical_q(["decides whether a request may be"], "ApplyRetryBudget")
    c3, _, _, _, _, _ = phrase_arm(_mkrepo({"c.go": acc}), qq, "ApplyRetryBudget", "c.go", 3, 3)
    c4, _, _, _, _, _ = phrase_arm(_mkrepo({"c.go": acc.replace("\u00e9", "x")}), qq, "ApplyRetryBudget", "c.go", 3, 3)
    ck(c3 > c4, "multi-byte source costs more than its ASCII twin", f"{c3} vs {c4}")

    # 5. SHARED SCORING. The footer claimed identical scoring while this arm scored itself.
    ck("score(" in inspect.getsource(phrase_arm), "phrase arm delegates to the shared scorer")

    # 6. A removed name BREAKS contiguity rather than being silently bridged: text either side
    #    of the hole is not adjacent in the file, and joining it would recreate defect 1.
    mid = _canonical_q(["the ApplyRetryBudget helper decides whether a request may be retried"], "ApplyRetryBudget")
    pm = phrase_for(mid)
    ck("the" not in pm.split(), "text across a stripped name is not joined into one phrase", repr(pm))

    # 7. PUNCTUATION ADJACENT TO THE NAME. The gap was inferred from a run of two spaces, which
    #    a bracketed name defeats: `the (ApplyRetryBudget) helper` blanks to `the ( ) helper`,
    #    single-spaced, and the arm then searched for a string not present in any file.
    src_line = "the (ApplyRetryBudget) helper decides whether a request may be retried"
    pp = phrase_for(_canonical_q([src_line], "ApplyRetryBudget"))
    ck(pp and pp in src_line, "a phrase beside a bracketed name still occurs in the source", repr(pp))

    # 8. EVERY candidate line of the canonical query is real source text, not just the longest.
    multi = _canonical_q(["the (ApplyRetryBudget) helper decides whether a request may be",
                          "retried after ApplyRetryBudget observes a transient failure."],
                         "ApplyRetryBudget")
    bad = [l.strip() for l in multi.splitlines()
           if l.strip() and l.strip() not in
           "the (ApplyRetryBudget) helper decides whether a request may be "
           "retried after ApplyRetryBudget observes a transient failure."]
    ck(not bad, "every line of the canonical query is contiguous source text", repr(bad))

    # 9-12. GUARDS A MUTATION SWEEP FOUND UNHELD. Each of these repairs shipped with no test
    #       at all -- including the ranked-header one, which peer review had just caught. A
    #       fix nothing can falsify is a fix on trust.
    # These are the reviewer's EXACT byte strings, not paraphrases of them. My own first
    # attempt at the header case used `1. src/right.go:20-30 TargetAlpha [complete] s=21.9`,
    # which DEFN cannot match at all -- so the test passed while the guard it was written for
    # stayed unheld, and the mutation sweep is the only reason I noticed.
    hdr = b"1. src/right.ts:20 function TargetAlpha\n"
    hl, hd = score(hdr, "/repo", "TargetAlpha", "src/right.ts", 20, 30, ranked_only=True)
    ck(not hd, "a ranked header alone is not a declaration", f"dec={hd}")
    ck(hl, "...but it is still a locator", f"loc={hl}")

    # body text under someone ELSE's header must not be credited to the target.
    # REAL REPO, deliberately: with a path that does not exist the source line reads empty and
    # the check cannot fire either way, so this test went vacuous the moment the metric started
    # reading files. The mutation sweep is what caught that it had.
    wrongrepo = _mkrepo({"src/right.go": "package p\n" * 24 + "func TargetAlpha() {}\n",
                         "src/wrong.go": "package p\nfunc WrongSymbol() {}\n"})
    other = b"1. src/wrong.go:10 WrongSymbol\nfunc TargetAlpha() {}\n"
    _, od = score(other, wrongrepo, "TargetAlpha", "src/right.go", 20, 30, ranked_only=True)
    ck(not od, "a body under a non-target header is not credited", f"dec={od}")

    # THE SPAN BOUNDS THE SOURCE READ. A second declaration of the same name earlier in the
    # file must not become the text we accept: an arm rendering THAT line has not shown the
    # target's declaration, it has shown a homonym's.
    twin = _mkrepo({"src/twin.go": "package p\nfunc TargetAlpha() {}\n" + "\n" * 22
                                   + "func TargetAlpha() int { return 1 }\n"})
    _, twd = score(b"1. src/twin.go:20-30 X\nfunc TargetAlpha() {}\n",
                   twin, "TargetAlpha", "src/twin.go", 20, 30, ranked_only=True)
    ck(not twd, "an earlier homonym's declaration is not the target's", f"dec={twd}")
    _, twd2 = score(b"1. src/twin.go:20-30 X\nfunc TargetAlpha() int { return 1 }\n",
                    twin, "TargetAlpha", "src/twin.go", 20, 30, ranked_only=True)
    ck(twd2, "the in-span declaration still counts", f"dec={twd2}")

    # a declaration in the right file but outside the registered span, via the grep path
    outside = b"/repo/src/right.go:90:func TargetAlpha() {}\n"
    ol, od2 = score(outside, "/repo", "TargetAlpha", "src/right.go", 20, 30)
    ck(not ol and not od2, "a hit outside the span is neither locator nor declaration",
       f"loc={ol} dec={od2}")

    # ...and the SAME declaration inside the span must still count, or the guard above is
    # just a blanket refusal wearing a span check.
    #
    # THESE FIXTURES NOW WRITE REAL FILES, because the metric now reads them. Asserting a
    # declaration against a repo path that does not exist tested the regex and nothing else --
    # which is precisely the defect being fixed here, reproduced inside its own test.
    gorepo = _mkrepo({"src/right.go": "package p\n" * 24 + "func TargetAlpha() {}\n"})
    inl, ind = score(b"src/right.go:25:func TargetAlpha() {}\n",
                     gorepo, "TargetAlpha", "src/right.go", 20, 30)
    ck(inl and ind, "the same declaration inside the span does count", f"loc={inl} dec={ind}")

    # the reviewer's prefixed-TypeScript case, which the path prefix used to hide
    tsrepo = _mkrepo({"src/right.ts": "// x\n" * 19 + "    public TargetAlpha(): void {}\n"})
    tl, td = score(b"src/right.ts:20:    public TargetAlpha(): void {}\n",
                   tsrepo, "TargetAlpha", "src/right.ts", 20, 30)
    ck(tl and td, "a TypeScript method behind a path prefix is found", f"loc={tl} dec={td}")

    # THE CLASS THAT KILLED PR287, now checked here. A regex match is not a declaration: a
    # body clipped mid-line, and text that was never in any file, both satisfied DEFN. The
    # error was not symmetric either -- rg emits whole lines while the graph's snippets are
    # truncated to a byte budget, so the clipped form arose for one arm only, and inflated
    # exactly the arm this benchmark exists to scrutinise.
    for label, rendered, want in (
        ("whole",      b"1. src/right.go:20-30 X\nfunc TargetAlpha() {}\n",        True),
        ("clipped",    b"1. src/right.go:20-30 X\nfunc TargetAlpha(\n",            False),
        ("fabricated", b"1. src/right.go:20-30 X\nfunc TargetAlpha() FICTION\n",   False),
        # the exact source line with anything appended is not the source line
        ("suffixed",   b"1. src/right.go:20-30 X\nfunc TargetAlpha() {} AND FABRICATION\n", False),
        ("prefixed",   b"1. src/right.go:20-30 X\nFAKE func TargetAlpha() {}\n",       False),
        ("indented",   b"1. src/right.go:20-30 X\n\tfunc TargetAlpha() {}\n",           True),
    ):
        _, dd = score(rendered, gorepo, "TargetAlpha", "src/right.go", 20, 30, ranked_only=True)
        ck(dd is want, f"declaration from source, not regex: {label}", f"dec={dd}")

    # signal death: a killed arm is an excluded case, never a retrieval miss
    class _Killed:
        returncode, stdout, stderr = -9, b"", b""
    import unittest.mock as _m
    with _m.patch.object(subprocess, "run", lambda *a, **k: _Killed()):
        _, _, _, kok, _, _ = phrase_arm("/repo", "decides whether a request may be",
                                        "ApplyRetryBudget", "a.go", 1, 9)
    ck(not kok, "a signal-killed arm reports failure, not a miss", f"ok={kok}")

    # A PATTERN BEGINNING WITH A DASH, at the production seam. Case 012 of the fx-cli run drew
    # "--dry-run ..." out of a doc comment, rg read it as a flag, exited 2, and the case was
    # dropped from EVERY arm -- one arm's quoting bug silently shrinking the population.
    dashrepo = _mkrepo({"d.go": "package p\n// --dry-run prints what would change\nfunc DryRunPlan() {}\n"})
    dq = _canonical_q(["--dry-run prints what would change"], "DryRunPlan")
    dcost, dloc, ddec, dok, _, dmeta = phrase_arm(dashrepo, dq, "DryRunPlan", "d.go", 3, 3)
    ck(dok, "a phrase starting with a dash does not fail the arm", f"rc={dmeta.get('rc')}")
    ck("--" in dmeta.get("argv", []), "the pattern is passed after an option terminator")
    ck(dloc and ddec, "...and it still finds the declaration", f"loc={dloc} dec={ddec}")

    # THE SEAL BINDING and the writer's diagnostic note -- both were verified by hand and
    # neither had a test, which the sweep reported as two real gaps.
    import tempfile as _tfa, subprocess as _spa
    # A REAL Go binary with REAL vcs stamps. sys.executable has none, so seal_run correctly
    # refused it and took the test process with it -- the refusal working exactly as designed.
    _bd = _mkdtemp(prefix="seal-bin-")
    with open(os.path.join(_bd, "go.mod"), "w") as fh: fh.write("module sealprobe\n\ngo 1.24\n")
    with open(os.path.join(_bd, "main.go"), "w") as fh: fh.write("package main\n\nfunc main() {}\n")
    for _a in (["init", "-q"], ["add", "-A"],
               ["-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"]):
        _spa.run(["git", "-C", _bd] + _a, capture_output=True)
    _probe = os.path.join(_bd, "probe")
    _built = _spa.run(["go", "build", "-o", _probe, "."], cwd=_bd, capture_output=True).returncode == 0
    _sd = _mkdtemp(prefix="seal-u-")
    if not _built:
        print("  skip  seal payload cases (no working Go toolchain)")
        skipped.append("seal payload: no Go toolchain")
        _sha = None
    else:
        _sha = seal_run(_sd, _probe, "deadbeef", "/r", "a" * 40, "b" * 40, 4096, 2,
                        [("q", "Sym", "f.go", 1, 2)], True)
    if _built:
        ck(_sha == hashlib.sha256(open(os.path.join(_sd, "SEAL.json"), "rb").read()).hexdigest(),
           "the returned seal hash is the hash of the seal actually written")
        ck(json.load(open(os.path.join(_sd, "SEAL.json")))["class"].startswith("reproducible"),
           "a CLEAN build is sealed as a reproducible-source measurement")
    ck(new_manifest("SEALHASH", "/r", "a" * 40, "", "b", "bin", "s", "e", 4096, 2)["seal_sha256"]
       == "SEALHASH", "the manifest names the seal it ran under")
    if _built:
        ck(json.load(open(os.path.join(_sd, "SEAL.json")))["selected_queries"][0]["symbol"] == "Sym",
           "the seal carries the selected queries, not just their count")

    _nd = _mkdtemp(prefix="note-")
    write_receipts(_nd, "001-Sym-f_go", "q", "Sym", "f.go", 1, 2, False,
                   (("phrase", 0, False, False, True, b"",
                     {"argv": [], "rc": None, "stderr_bytes": b"", "note": "no phrase in q"}),))
    ck(json.load(open(os.path.join(_nd, "001-Sym-f_go.phrase.json")))["note"] == "no phrase in q",
       "an arm that produced nothing records WHY")

    # NON-EMPTY STDERR, and the two shapes that break a decode-and-cap writer. The previous
    # version stored stderr decoded with replacement characters and truncated at 4000, so an
    # invalid byte became indistinguishable from a real replacement character and a long tail
    # vanished. Both were argued rather than tested, and the run that motivated the change
    # happened to have empty stderr throughout.
    _bad = b"warn: \xff\xfe not utf8\n" + b"x" * 9000
    write_receipts(_nd, "002-Sym-f_go", "q", "Sym", "f.go", 1, 2, False,
                   (("grep", 0, False, False, False, b"",
                     {"argv": ["rg"], "rc": 2, "stderr_bytes": _bad}),))
    _sp_path = os.path.join(_nd, "002-Sym-f_go.grep.stderr")
    ck(os.path.exists(_sp_path), "non-empty stderr is written to its own file")
    with open(_sp_path, "rb") as fh:
        _got = fh.read()
    ck(_got == _bad, "...byte for byte, invalid UTF-8 included, with no cap",
       f"{len(_got)} bytes vs {len(_bad)}")
    _j = json.load(open(os.path.join(_nd, "002-Sym-f_go.grep.json")))
    ck(_j["stderr_bytes_len"] == len(_bad) and _j["stderr_file"].endswith(".stderr"),
       "...and the receipt records its length and names the file")
    ck(_j["stderr_not_charged"] is True, "...and says stderr is not charged to the arm")

    # THE SEAL, through the production evaluator rather than a predicate. Peer review's point:
    # a preflight that checks a path and hands control to another process has sealed nothing.
    import subprocess as _sp
    _tf2 = _tfa
    _me = os.path.abspath(__file__)
    def _run_eval(*a):
        return _sp.run([sys.executable, _me] + list(a), capture_output=True, text=True, timeout=1800)
    _fresh = lambda: os.path.join(_mkdtemp(prefix="seal-e2e-"), "out")

    _nb = os.path.join(_mkdtemp(prefix="seal-nb-"), "notgo")
    os.makedirs(os.path.dirname(_nb), exist_ok=True)
    with open(_nb, "w") as fh:
        fh.write("#!/bin/sh\necho hi\n")
    os.chmod(_nb, 0o755)
    _gitfx = _mkrepo({"a.go": "package p\n\n// A does a thing worth describing in prose here.\nfunc A() {}\n"})
    for _a in (["init", "-q"], ["add", "-A"],
               ["-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"]):
        _sp.run(["git", "-C", _gitfx] + _a, capture_output=True)
    _r = _run_eval(_nb, _gitfx, "1", "4096", _fresh())
    ck("cannot establish the binary's build identity" in (_r.stdout + _r.stderr),
       "an UNKNOWN build is refused, not defaulted to clean", _r.stderr.strip()[:80])
    ck(binary_build_identity(_nb)["known"] is False,
       "...and build identity reports unknown rather than vcs_modified=False")

    # A missing stamp must never resolve to the reassuring answer.
    ck("vcs_modified" not in binary_build_identity(_nb),
       "an unknown build carries no vcs_modified at all")

    # GATE 1. A malformed vcs.modified is UNKNOWN, not clean.
    _mb = os.path.join(_mkdtemp(prefix="mal-"), "b")
    with open(_mb, "w") as fh: fh.write("x")
    ck(binary_build_identity(_mb)["known"] is False, "a non-Go file has no build identity")
    # PRESENT BUT MALFORMED, which is the case that mattered: the stamp is there and unreadable,
    # and `== "true"` turned every unreadable value into False, meaning CLEAN.
    _stamps = lambda m: ("\tpath\tx/y\n\tmod\tx/y\tv0.0.1\n"
                         "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + "a" * 40 +
                         "\n\tbuild\tvcs.modified=" + m + "\n")
    for _v in ("garbage", "TRUE", "1", "yes", ""):
        _bi = build_identity_from_stamps(_stamps(_v)) if _v else build_identity_from_stamps(
            "\tbuild\tvcs.revision=" + "a" * 40 + "\n\tbuild\tvcs.modified=\n")
        ck(_bi["known"] is False, f"GATE1: vcs.modified={_v!r} is unknown, not clean", str(_bi))
    ck(build_identity_from_stamps(_stamps("false"))["vcs_modified"] is False,
       "GATE1: a literal false still reads as a clean tree")
    ck(build_identity_from_stamps(_stamps("true"))["vcs_modified"] is True,
       "GATE1: a literal true still reads as a dirty tree")

    # GATE 3. A comparable REJECTION with a VALID oracle -- the combination nothing forced.
    _g3 = _mkdtemp(prefix="g3-")
    _st3 = {"valid": 0, "na": 0, "bytes": [], "loc": 0, "dec": 0}
    _meta3 = {"argv": [], "rc": 1, "stderr_bytes": b""}
    _arms3 = (("graph", 0, False, False, False, b"", _meta3),   # comparable arm FAILS
              ("grep", 0, False, False, True, b"", _meta3),
              ("phrase", 0, False, False, True, b"", _meta3),
              ("oracle", 77, True, True, True, b"", _meta3))    # control is VALID
    _adm3 = settle_attempt(_g3, "001-S-f_go", "q", "S", "f.go", 1, 2, _arms3, _st3)
    ck(_adm3 is False, "GATE3: a failed comparable arm rejects the attempt")
    ck(_st3["valid"] == 1 and _st3["bytes"] == [77] and _st3["loc"] == 1,
       "GATE3: ...and the VALID oracle on that rejected attempt is still counted", str(_st3))

    # GATE 4. The receipt's cost is the cost SUPPLIED, not inflated by stderr.
    _g4 = _mkdtemp(prefix="g4-")
    _big = b"e" * 9000
    for _cid, _cost in (("001-Z-f_go", 0), ("002-Z-f_go", 1234)):
        write_receipts(_g4, _cid, "q", "Z", "f.go", 1, 2, False,
                       (("grep", _cost, False, False, True, b"",
                         {"argv": [], "rc": 2, "stderr_bytes": _big}),))
        _jj = json.load(open(os.path.join(_g4, _cid + ".grep.json")))
        ck(_jj["cost_bytes"] == _cost,
           f"GATE4: cost_bytes stays {_cost} with 9k of stderr beside it", str(_jj["cost_bytes"]))

    # NO SILENTLY UNSEALED AGGREGATE. Omitting the output directory used to print the whole
    # table with no seal, no receipts and no verdict -- output indistinguishable from a
    # measurement. The refusal fires before any work, so this costs nothing to check.
    _nr = _spa.run([sys.executable, os.path.abspath(__file__), "/nonexistent", "/nonexistent",
                    "2", "4096"], capture_output=True, text=True, timeout=120)
    ck("no output directory" in (_nr.stdout + _nr.stderr),
       "a run with no output directory is refused before it starts", _nr.stderr.strip()[:70])
    ck(_nr.returncode != 0, "...and it exits non-zero")

    _cav = budget_caveat(4096).lower()
    ck("not comparable" in _cav and "4096" in _cav,
       "the byte column carries a non-comparability warning naming the budget")
    ck("budget" not in inspect.signature(grep_arm).parameters,
       "...and the warning is TRUE: grep_arm still takes no budget")

    # The caveat must be IN THE PRINTED OUTPUT. My first version grepped this module's SOURCE
    # for the warning text -- and the assertion line itself contains that text, so the guard
    # satisfied itself: deleting the real warning left one occurrence, its own, and the test
    # stayed green. A test that can be satisfied by its own body is the purest form of the
    # defect this whole file keeps finding. Asserted against a real run's stdout instead.

    # PRODUCTION ORCHESTRATION, observed through the artifacts a real run leaves. Peer review's
    # outstanding question, and the right one: a correct record_attempt or tally_oracle does not
    # prove the LOOP calls them before the admission gate. Stub arms cannot reach this loop -- it
    # lives in __main__ -- but the ordering is observable from what lands on disk and when.
    _egbin = os.environ.get("ENTIRE_GRAPH_BIN") or os.path.join(
        os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))), "entire-graph")
    if not os.path.exists(_egbin):
        # A SKIP MUST NOT BE ABLE TO LOOK LIKE A PASS. Under --require-orchestration this is a
        # failure: the orchestration guards are the only evidence that the seal precedes the
        # arms, and a green line that means "did not check" is worse than a red one.
        print("  skip  production-ordering cases (set ENTIRE_GRAPH_BIN to run them)")
        skipped.append("production-ordering: no binary")
    else:
        # The doc comments must clear build_cases' own filters -- at least eight words survive
        # after the name is stripped -- or the run yields NO cases and every ordering assertion
        # below passes over an empty list. The first draft did exactly that: all five were green
        # and a deliberate seal-after-the-arms mutation did not move them.
        _ofx = _mkrepo({"p/a.go": "package p\n\n"
                        "// RetryBudgetForAttempt reports the ceiling of further work permitted\n"
                        "// for one request, so a caller can decide whether another try is wise.\n"
                        "func RetryBudgetForAttempt(attempt int) int { return attempt + 1 }\n\n"
                        "// DrainPendingWork empties the queue that the scheduler has filled and\n"
                        "// returns how many items were removed before the queue became empty.\n"
                        "func DrainPendingWork(n int) int { return n }\n"})
        for _a in (["init", "-q"], ["add", "-A"],
                   ["-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"]):
            _spa.run(["git", "-C", _ofx] + _a, capture_output=True)
        _oout = os.path.join(_mkdtemp(prefix="order-"), "out")
        _rr = _spa.run([sys.executable, os.path.abspath(__file__), "--diagnostic-dirty-build",
                        _egbin, _ofx, "2", "4096", _oout], capture_output=True, text=True, timeout=1800)
        if not os.path.exists(os.path.join(_oout, "run-manifest.json")):
            print(f"  skip  production-ordering cases (run produced no manifest: {_rr.stderr.strip()[:70]})")
            skipped.append("production-ordering: no manifest")
        else:
            _man = json.load(open(os.path.join(_oout, "run-manifest.json")))
            _sealp = os.path.join(_oout, "SEAL.json")
            _rcpts = [f for f in os.listdir(_oout) if f.endswith(".txt")]
            # NON-VACUITY FIRST. Every assertion below quantifies over the receipts, so an
            # empty run makes all of them true.
            ck(len(_rcpts) > 0, "ORDER: the run produced receipts to reason about",
               f"{len(_rcpts)} receipts; the fixture yielded no cases")
            ck(os.path.exists(_sealp), "ORDER: the seal exists")
            ck(all(os.path.getmtime(_sealp) <= os.path.getmtime(os.path.join(_oout, f))
                   for f in _rcpts),
               "ORDER: the seal was written before every receipt")
            ck(_man["seal_sha256"] == hashlib.sha256(open(_sealp, "rb").read()).hexdigest(),
               "ORDER: the manifest binds the seal on disk")
            ck(len(_rcpts) == len(_man["cases"]),
               "ORDER: one receipt per ATTEMPT recorded in the manifest",
               f"{len(_rcpts)} receipts vs {len(_man['cases'])} attempts")
            ck(len(json.load(open(_sealp))["selected_queries"]) == len(_man["cases"]),
               "ORDER: the seal's population is the population that ran")
            # GATE 2. mtimes cannot show this: record_attempt runs after all four arms, so a
            # seal written between the arms and the receipt still predates every receipt. The
            # evaluator checks the seal on disk AT ARM ENTRY and records what it saw.
            # ...and the verdict must DEPEND on it, not merely record it.
            ck(_man["run_status"] == "VALID",
               "ORDER: a run whose seal was present at arm entry is VALID")
            ck(_man.get("seal_present_at_first_arm") is True,
               "ORDER: the sealed population was on disk, with the right hash, AT ARM ENTRY",
               str(_man.get("seal_present_at_first_arm")))

    # RANGE OVERLAP, with the negatives that keep it from becoming "anything nearby counts".
    for header, want, label in (
        (b"1. a.go:142-143 Sym s=9 [focus:143]\n", True,  "a range that starts one line before the span"),
        (b"1. a.go:143 Sym s=9\n",                 True,  "a bare start line inside the span"),
        (b"1. a.go:100-110 Sym s=9\n",             False, "a range entirely before the span"),
        (b"1. a.go:130-142 Sym s=9\n",             False, "an adjacent function ending one line short"),
        (b"1. a.go:160-170 Sym s=9\n",             False, "a range entirely after the span"),
        (b"1. b.go:142-143 Sym s=9\n",             False, "the right range in the WRONG FILE"),
    ):
        gl, _ = score(header, "/repo", "Sym", "a.go", 143, 150, ranked_only=True)
        ck(gl is want, "rank range: " + label, f"loc={gl}")

    # THE SIBLING BENCHMARKS' HIT PREDICATE, tested here because this is the suite the mutation
    # sweep runs. doccomment_bench and budget_falsifier matched the symbol name against the
    # WHOLE ranked line, path included, and never checked the file -- so a directory called
    # `resolve/` scored as retrieving a symbol called `resolve`, and so did a same-named symbol
    # in any other file. head_to_head had the identical bug, was fixed, and the fix was never
    # carried across. Both siblings measure the graph ALONE, so both inflated it.
    import importlib.util as _ilu
    _spec = _ilu.spec_from_file_location("_dcb", os.path.join(os.path.dirname(
        os.path.abspath(__file__)), "doccomment_bench.py"))
    _dcb = _ilu.module_from_spec(_spec); _spec.loader.exec_module(_dcb)
    for line, want, label in (
        ("1. internal/resolve/cache.go:12 loadCache s=9.1", False, "a directory named like the symbol"),
        ("2. internal/sem/other.go:40 resolve s=8.0",       False, "the right name in the wrong file"),
        ("3. internal/sem/target.go:40 resolve s=8.0",      True,  "the right name in the right file"),
        # THE CASE THE FILE CHECK CANNOT COVER: the path IS the target and the name appears
        # only inside it, so only matching against the text AFTER the path:line prefix rejects
        # this. Without it, a directory named like the symbol scores in its own file.
        ("4. internal/resolve/target.go:40 loadCache s=7.0", False, "name only in the target's own path"),
        # A CALLER rendered in the target's own file is not the target, and neither is the name
        # appearing in trailing metadata -- a score, a signal word, a focus annotation.
        ("5. internal/sem/target.go:40 loadCache s=9 [focus:40]", False, "a caller in the right file"),
        ("6. internal/sem/target.go:40 loadCache s=9 resolve",    False, "the name in trailing metadata"),
        ("7. internal/sem/target.go:40 resolveRule s=9",          False, "a longer name sharing the prefix"),
        ("8. internal/sem/target.go:40 m.resolve s=9",            True,  "the target rendered as a method"),
        # RANGED HEADERS. A fix-induced regression: tightening identity to the first field after
        # the location exposed a parser that stopped at the range START, so `39-40 resolve` left
        # `-40` as that field and the correct symbol was rejected in BOTH consumers.
        ("9. internal/sem/target.go:39-40 resolve s=8 [focus:40]", True,  "ranged, bare name"),
        ("10. internal/sem/target.go:39-40 m.resolve s=8",         True,  "ranged, qualified"),
        ("11. internal/sem/target.go:39-40 loadCache s=9 resolve", False, "ranged, trailing mention"),
        ("12. internal/sem/other.go:39-40 resolve s=8",            False, "ranged, wrong file"),
        ("13. internal/sem/target.go:39-40 resolveRule s=9",       False, "ranged, longer name"),
    ):
        tf = "internal/resolve/target.go" if "internal/resolve" in line else "internal/sem/target.go"
        ck(_dcb.ranked_hit(line, "resolve", tf, "/r") is want, "sibling hit predicate: " + label)

    # BOTH CONSUMERS, not just the one this test imports. budget_falsifier used to carry its own
    # copy of the predicate, which no test called and no mutation reached -- it could have
    # drifted silently while every guard reported green. It binds the shared one now, and this
    # exercises ITS binding rather than assuming the import.
    _spec_b = _ilu.spec_from_file_location("_bfz", os.path.join(os.path.dirname(
        os.path.abspath(__file__)), "budget_falsifier.py"))
    _bfz = _ilu.module_from_spec(_spec_b); _spec_b.loader.exec_module(_bfz)
    ck(_bfz.ranked_hit("1. internal/sem/target.go:40 resolve s=8.0",
                       "resolve", "internal/sem/target.go", "/r") is True,
       "budget_falsifier's predicate accepts the target")
    ck(_bfz.ranked_hit("1. internal/sem/target.go:40 loadCache s=9 [focus:40]",
                       "resolve", "internal/sem/target.go", "/r") is False,
       "budget_falsifier's predicate refuses a caller in the right file")
    # A RANGED case through the second consumer's binding too -- the regression hit both, so
    # both have to be shown clear of it.
    ck(_bfz.ranked_hit("1. internal/sem/target.go:39-40 resolve s=8.0 [focus:40]",
                       "resolve", "internal/sem/target.go", "/r") is True,
       "budget_falsifier's predicate accepts a RANGED header")

    # 13-16. THE RECEIPT VERDICT. Peer review's point was that receipts said PROVISIONAL
    #        forever and nothing finalised them, so a completed run and one killed halfway
    #        were indistinguishable on disk. These pin the lifecycle rather than the demo.
    import tempfile as _tf, json as _j
    rd = _mkdtemp(prefix="rcpt-")
    meta = {"argv": ["rg", "x"], "rc": 1, "stderr": "", "oracle": False}
    write_receipts(rd, "001-Sym-f_go", "q", "Sym", "f.go", 1, 2, False,
                   (("graph", 0, False, False, False, b"", meta),))
    ck(os.path.exists(os.path.join(rd, "001-Sym-f_go.txt")),
       "a FAILED attempt still leaves a receipt")
    ck(_j.load(open(os.path.join(rd, "001-Sym-f_go.graph.json")))["returncode"] == 1,
       "the failed arm's exit code is recorded")
    ck(not os.path.exists(os.path.join(rd, "run-manifest.json")),
       "receipts alone never imply a verdict")

    # THE WIRING, not just the writer. The original defect was the ORDER of two call sites --
    # gate first, write second -- so a unit test of write_receipts could never have caught it.
    rd2 = _mkdtemp(prefix="rcpt-")
    failing = (("graph", 0, False, False, False, b"", meta),
               ("grep", 0, False, False, True, b"", meta),
               ("phrase", 0, False, False, True, b"", meta),
               ("oracle", 0, False, False, True, b"", meta))
    adm = record_attempt(rd2, "002-Sym-f_go", "q", "Sym", "f.go", 1, 2, failing)
    ck(not adm, "a failed comparable arm refuses admission")
    ck(os.path.exists(os.path.join(rd2, "002-Sym-f_go.txt")),
       "...and the refused attempt is on disk anyway")
    rd3 = _mkdtemp(prefix="rcpt-")
    oracle_only = tuple((lbl, 0, False, False, lbl != "oracle", b"", meta)
                        for lbl in ("graph", "grep", "phrase", "oracle"))
    ck(record_attempt(rd3, "003-Sym-f_go", "q", "Sym", "f.go", 1, 2, oracle_only),
       "an oracle failure alone does not exclude the case")

    # THE CONTROL'S DENOMINATOR IS ITS OWN. A valid oracle run on a case the comparable arms
    # rejected must still count: counting it after the admission gate made the denominator
    # "valid oracle runs among paired-admitted cases", which moves whenever another arm fails.
    st = {"valid": 0, "na": 0, "bytes": [], "loc": 0, "dec": 0}
    tally_oracle(st, True, 100, True, True)      # an attempt the comparable arms will reject
    ck(st["valid"] == 1 and st["loc"] == 1,
       "a valid oracle run counts even when the case is not admitted", str(st))
    tally_oracle(st, False, 0, False, False)
    ck(st["valid"] == 1 and st["na"] == 1, "a failed oracle run is N/A, not a miss", str(st))

    mani = {"schema": 1, "cases": [{"case_id": "001-Sym-f_go", "admitted": False}]}
    finalize_manifest(rd, mani, "VOID", "killed")
    got = _j.load(open(os.path.join(rd, "run-manifest.json")))
    ck(got["run_status"] == "VOID", "a voided run says so", got.get("run_status"))
    ck(not os.path.exists(os.path.join(rd, ".run-manifest.json.tmp")),
       "the atomic write leaves no partial manifest behind")

    # CROSS-RUN, which is the case a single-run test cannot reach: a second run must not write
    # into a directory that already carries a verdict, or a crash mid-rerun leaves the OLD
    # manifest certifying a mixture of two runs' bytes.
    before = {f: open(os.path.join(rd, f), "rb").read() for f in sorted(os.listdir(rd))}
    refused = False
    try:
        claim_outdir(rd)
    except SystemExit:
        refused = True
    ck(refused, "a rerun into a directory holding a verdict is refused")
    after = {f: open(os.path.join(rd, f), "rb").read() for f in sorted(os.listdir(rd))}
    ck(before == after, "...and the refusal touched none of the existing receipt bytes",
       f"{len(before)} -> {len(after)} files")
    fresh = os.path.join(_mkdtemp(prefix="rcpt-"), "new")
    claim_outdir(fresh)
    ck(os.path.isdir(fresh), "a fresh directory is created and claimed")
    # An EMPTY existing directory is refused too: two runs would both find it empty, both
    # proceed, and interleave their receipts under one verdict.
    empty = _mkdtemp(prefix="rcpt-empty-")
    refused_empty = False
    try:
        claim_outdir(empty)
    except SystemExit:
        refused_empty = True
    ck(refused_empty, "an existing EMPTY directory is refused as well")
    refused_twice = False
    try:
        claim_outdir(fresh)
    except SystemExit:
        refused_twice = True
    ck(refused_twice, "a second run cannot claim the directory the first created")

    if skipped:
        print("\nSKIPPED (checked nothing): " + "; ".join(skipped))
    if require_orchestration and skipped:
        print("REQUIRED ORCHESTRATION PROOF WAS SKIPPED -- treating as failure")
        fails.extend(skipped)
    # THE SUITE MUST NOT LEAK ITS OWN FIXTURES. Each --test used to leave them behind, and the
    # seal cases BUILD A GO BINARY into a fresh directory every pass; one session left 15,946
    # directories and 1.1 GB in TMPDIR with nothing ever failing.
    #
    # The assertion is about THIS PROCESS, not about TMPDIR. A first version counted every
    # directory in the shared temp root carrying one of these prefixes, so leftovers from any
    # other run -- a concurrent sweep, an earlier session, a manual experiment -- failed a suite
    # that had cleaned up perfectly. A guard that fails on someone else's mess is a guard people
    # learn to ignore.
    _tracked = list(_TEMPDIRS)
    _removed = _cleanup_tempdirs()
    _survivors = [d for d in _tracked if os.path.exists(d)]
    ck(not _survivors, "the suite removes every temp directory it created",
       f"{len(_survivors)} of {len(_tracked)} survived; removed {_removed}")
    # ...and the REPORTED count must be the real one. Asserting only the survivors leaves the
    # number beside them free to lie, which is how "25 left of 25; removed 25" happened.
    ck(_removed == len(_tracked) - len(_survivors),
       "...and reports the number it actually removed",
       f"reported {_removed}, really removed {len(_tracked) - len(_survivors)}")

    # _finish_tempdirs IS WHAT THE finally CALLS, so these two are the whole lifecycle contract:
    # a removable path goes and is not reported; an unremovable one survives and IS reported, so
    # a leak cannot pass a normal return. Cheap, and they need no subprocess.
    _fa = _mkdtemp(prefix="pharm-fin-")
    ck(not _finish_tempdirs(), "a removable allocation leaves no survivor")
    ck(not os.path.exists(_fa), "...and is actually gone")
    _fro = _mkdtemp(prefix="pharm-fro-")
    _fstuck = os.path.join(_fro, "stuck")
    os.makedirs(_fstuck, exist_ok=True)
    os.chmod(_fro, 0o500)
    try:
        _TEMPDIRS.append(_fstuck)
        # The read-only PARENT is tracked too and also survives, so the check is membership,
        # not equality -- an exact-list assertion here would be asserting the fixture's shape
        # rather than the contract.
        _surv = _finish_tempdirs()
        ck(_fstuck in _surv,
           "an unremovable allocation is REPORTED as a survivor, not silently dropped", str(_surv))
    finally:
        os.chmod(_fro, 0o700)
        import shutil as _sh2
        _sh2.rmtree(_fro, ignore_errors=True)

    # A REMOVAL THAT FAILS MUST NOT BE COUNTED. With cleanup working every directory goes, so
    # counting attempts and counting successes agree and neither test can tell them apart. This
    # forces a genuine failure: a child inside a read-only parent cannot be unlinked.
    _ro = _mkdtemp(prefix="pharm-ro-")
    _stuck = os.path.join(_ro, "stuck")
    os.makedirs(_stuck, exist_ok=True)
    os.chmod(_ro, 0o500)
    try:
        _TEMPDIRS.append(_stuck)
        _n = _cleanup_tempdirs()
        ck(os.path.exists(_stuck), "the fixture really is unremovable")
        ck(_n == 0, "a removal that FAILED is not counted as done", f"reported {_n}")
    finally:
        os.chmod(_ro, 0o700)
        import shutil as _sh
        _sh.rmtree(_ro, ignore_errors=True)

    for _obs, _want in ((True, "VALID"), (False, "VOID"), (None, "VOID")):
        ck(run_verdict(_obs)[0] == _want,
           f"a seal observation of {_obs!r} at arm entry gives {_want}")
    ck("frozen before it was measured" in run_verdict(False)[1],
       "...and a VOID says why")
    print("\nALL PHRASE-ARM FALSIFIERS PASS" if not fails else "\nFAILURES: " + ", ".join(fails))
    return 1 if fails else 0



def write_receipts(outdir, cid, q, name, fp, lo, hi, admitted, arms):
    """One directory entry per ATTEMPT, written before the admission gate.

    A summary cannot be re-scored and this scorer has now been wrong six times, so the raw
    bytes are the record; and an attempt that FAILED is the one most worth inspecting, which
    is exactly the one the previous version threw away by writing receipts after the drop.

    Nothing written here is final. The run manifest decides that: until it says VALID, every
    receipt on disk belongs to a run that may yet be voided, and a crash leaves them that way.
    """
    for arm, cost, loc, dec, ok, raw, meta in arms:
        base = os.path.join(outdir, cid + "." + arm)
        with open(base + ".raw", "wb") as fh:
            fh.write(raw or b"")
        if meta.get("evidence") is not None:
            # what was actually SCORED, which for the phrase arm is the windows it read and
            # was absent from the record entirely
            with open(base + ".scored", "wb") as fh:
                fh.write(meta["evidence"])
        # ORIGINAL BYTES, in their own file. Decoding and capping stderr into the JSON made it
        # unverifiable: a replacement character is indistinguishable from a byte that was really
        # there, and a cap silently drops the tail. Its cost scope is named too -- stderr is NOT
        # part of the payload charged to the arm.
        if meta.get("stderr_bytes"):
            with open(base + ".stderr", "wb") as fh:
                fh.write(meta["stderr_bytes"])
        with open(base + ".json", "w") as fh:
            json.dump({"case_id": cid, "arm": arm, "ok": ok, "cost_bytes": cost,
                       "locator": loc, "declaration": dec,
                       "argv": meta.get("argv"), "returncode": meta.get("rc"),
                       # The early-return arms record WHY they produced nothing. These used a
                       # key the writer never read, so "no phrase in q" and "no term" -- the
                       # only explanation a zero-byte arm ever gives -- were written nowhere.
                       "note": meta.get("note"),
                       "stderr_bytes_len": len(meta.get("stderr_bytes") or b""),
                       "stderr_file": (cid + "." + arm + ".stderr") if meta.get("stderr_bytes") else None,
                       "stderr_not_charged": True,
                       "oracle": meta.get("oracle", False)}, fh, indent=1)
    with open(os.path.join(outdir, cid + ".txt"), "w") as fh:
        fh.write(f"case_id: {cid}\nquery: {q!r}\ntarget: {name} {fp}:{lo}-{hi}\n"
                 f"admitted: {admitted}   (a failed comparable arm excludes the case; "
                 f"it is never scored as a miss)\n"
                 f"run_status: see run-manifest.json -- PROVISIONAL until that says VALID\n")
        for arm, cost, loc, dec, ok, _, meta in arms:
            tag = "   CAPABILITY CONTROL: given the target name; not a comparator" if arm == "oracle" else ""
            fh.write(f"{arm:7s} ok={ok} bytes {cost}  locator {loc}  declaration {dec}{tag}\n")


def tally_oracle(state, ok, cost, loc, dec):
    """Record the capability control's result, for EVERY attempt, independent of admission.

    This was inline and after the comparable-admission gate, which quietly redefined the
    oracle's denominator as "valid oracle runs among paired-admitted cases". An independent
    audit of the fx-cli run found all 20 oracle receipts valid while the manifest reported 19 --
    one case had been dropped for an unrelated arm's failure. A control whose denominator moves
    when another arm fails is not independent of that arm, which is the only thing a control is
    for. It is a function so a test can reach it; the ordering bug lived in a call site, and a
    call site is not testable from a suite.
    """
    if ok:
        state["valid"] += 1
        state["bytes"].append(cost)
        state["loc"] += bool(loc)
        state["dec"] += bool(dec)
    else:
        state["na"] += 1
    return state


def record_attempt(outdir, cid, q, name, fp, lo, hi, arms):
    """Write the attempt's receipts, then say whether the COMPARABLE arms admit it.

    Recording and admitting are one function on purpose. They were two steps in the main loop
    with the admission gate FIRST, so a dropped case -- the one most worth inspecting -- left
    nothing on disk at all. Keeping them apart is what let that happen, and a unit test of the
    writer alone cannot notice it: the defect was in the order of the call sites, not in either
    of them. Anything that decides admission now has to walk past the write.

    `arms` is (label, cost, locator, declaration, ok, raw, meta). The oracle is present in the
    receipts and absent from the verdict: it is handed the target's name, so it is a capability
    control rather than a comparator.
    """
    if outdir:
        write_receipts(outdir, cid, q, name, fp, lo, hi,
                       all(a[4] for a in arms if a[0] != "oracle"), arms)
    return all(a[4] for a in arms if a[0] != "oracle")


def binary_build_identity(binary):
    """The binary's own build stamps, or UNKNOWN. Never a confident default.

    `go version -m` failing, or the stamps being absent, previously produced vcs_modified=false
    and therefore build_source_reproducible=TRUE -- a missing answer silently became the
    reassuring one. Absence is reported as unknown and the caller refuses it.
    """
    try:
        out = subprocess.run(["go", "version", "-m", binary], capture_output=True, text=True,
                             timeout=120)
    except Exception:
        return {"known": False, "reason": "go version -m could not be run"}
    if out.returncode != 0:
        return {"known": False, "reason": f"go version -m exited {out.returncode}"}
    return build_identity_from_stamps(out.stdout)


def build_identity_from_stamps(text):
    """Decide build identity from `go version -m` OUTPUT. Pure, so a malformed stamp is
    reachable from a test without manufacturing a binary that carries one.

    The decision, not the subprocess, is what was wrong: `kv["vcs.modified"] == "true"` mapped
    every other value -- a garbled one included -- to False, and False means CLEAN.
    """
    kv = dict(re.findall(r"build\s+(\S+)=(\S+)", text))
    if "vcs.revision" not in kv or "vcs.modified" not in kv:
        return {"known": False, "reason": "binary carries no vcs stamps"}
    if kv["vcs.modified"] not in ("true", "false"):
        return {"known": False,
                "reason": f"vcs.modified is {kv['vcs.modified']!r}, neither true nor false"}
    mod = re.search(r"mod\s+(\S+)\s+(\S+)", text)
    path = re.search(r"path\s+(\S+)", text)
    go = re.search(r":\s+(go\S+)", text)
    return {"known": True, "vcs_revision": kv["vcs.revision"], "vcs_time": kv.get("vcs.time"),
            "vcs_modified": kv["vcs.modified"] == "true",
            "module_path": mod.group(1) if mod else None,
            "module_version": mod.group(2) if mod else None,
            "main_path": path.group(1) if path else None,
            "go": go.group(1) if go else None,
            "build_settings": kv}


def seal_present_at_arm_entry(outdir, seal_sha):
    """Is the sealed population already on disk, with the expected hash, before the first arm?

    Peer review's catch, and it is exact: receipt mtimes cannot show this. record_attempt runs
    AFTER all four arms, so a seal written between the arms and the receipt still predates every
    receipt and every mtime assertion still passes. The only moment that proves the population
    was frozen before it was measured is ARM ENTRY, so that is where this is checked.
    """
    if not outdir:
        return None
    path = os.path.join(outdir, "SEAL.json")
    if not os.path.exists(path):
        return False
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest() == seal_sha


def seal_run(outdir, binary, bsha, repo, rev, content_sha, budget, want, cases, diagnostic):
    """Create the seal, in the directory this run owns, BEFORE any arm runs.

    A separate preflight tool cannot do this. It can check a path and then hand control to
    something else that creates the directory, runs the arms and writes its own manifest --
    and nothing binds the three together. The evaluator is the only process that knows the
    selected list, so it is the only one that can seal it before measuring it.

    Returns the seal's sha256, which the final manifest carries: a manifest that names the seal
    it ran under cannot be paired with a different one afterwards.
    """
    build = binary_build_identity(binary)
    if not build["known"]:
        raise SystemExit(f"REFUSING TO START: cannot establish the binary's build identity "
                         f"({build['reason']}). An unknown build is not a clean one.")
    if build["vcs_modified"] and not diagnostic:
        raise SystemExit(
            f"REFUSING TO START: {binary} was built from a DIRTY tree "
            f"(revision {build['vcs_revision']}); its source cannot be reproduced, so no result "
            f"from it can be either. Pass --diagnostic-dirty-build to record it explicitly as "
            f"artifact-tied development diagnostics.")
    me = os.path.abspath(__file__)
    seal = {"schema": 1,
            "class": "artifact-tied development diagnostics" if build["vcs_modified"]
                     else "reproducible-source measurement",
            "binary": {"path": binary, "sha256": bsha, "build": build,
                       "build_source_reproducible": not build["vcs_modified"]},
            "evaluator": {"path": os.path.basename(me), "sha256": file_sha256(me)},
            "options": {"cases_requested": want, "max_context_bytes": budget, "seed": 7,
                        "format": "agent", "no_cache": True},
            "fixture": {"repo": repo, "rev": rev, "content_sha256": content_sha},
            "selected_queries": [{"symbol": c[1], "file": c[2], "span": [c[3], c[4]],
                                  "query": c[0]} for c in cases]}
    blob = json.dumps(seal, indent=1, sort_keys=True).encode()
    with open(os.path.join(outdir, "SEAL.json"), "wb") as fh:
        fh.write(blob)
    return hashlib.sha256(blob).hexdigest()


def file_sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def settle_attempt(outdir, cid, q, name, fp, lo, hi, arms, ostate):
    """The whole post-arm transition, in one place: record, tally the control, then admit.

    Peer review: nothing forced a comparable REJECTION with a VALID oracle, so no test could
    show the tally happens before the gate. Receipt counts and population sizes say nothing
    about it. As one function the transition is a single thing a test can drive with exactly
    that combination.
    """
    admitted = record_attempt(outdir, cid, q, name, fp, lo, hi, arms)
    by = {a[0]: a for a in arms}
    o = by["oracle"]
    tally_oracle(ostate, o[4], o[1], o[2], o[3])
    return admitted


def budget_caveat(budget):
    """The warning that must sit beside the byte column, as a VALUE.

    It was inlined prints, and the test grepped this module's source for its text -- but the
    assertion line contained that same text, so the guard satisfied itself: deleting the real
    warning left one occurrence, its own, and the suite stayed green. A test satisfiable by its
    own body is the purest form of the defect this file keeps finding. Returning a string makes
    the check a runtime value, which cannot be faked by the checker.

    The content: only the graph runs under --max-context-bytes; the grep arms are charged their
    full output. The byte ratio is therefore a capped tool against an uncapped one with the
    operator holding the cap, and lowering the budget improves it without changing the tool.
    """
    return (f"\n  !! BYTES ARE NOT COMPARABLE ACROSS ARMS: only the graph is capped, at "
            f"{budget} B.\n"
            f"     The grep arms are charged their full output. Lowering the budget improves\n"
            f"     the ratio without changing the tool. Quote the graph's bound, never the ratio.\n"
            f"     The lexical arm is also charged a bounded read after EVERY hit, while the\n"
            f"     graph is charged one rendered payload -- a second asymmetry the same way.\n")


def run_verdict(seal_observed_at_arm_entry):
    """VALID only if the seal was actually observed on disk at arm entry.

    A value, not a branch buried in the main block: recording the observation and then stamping
    VALID anyway is a manifest contradicting itself, and the branch that prevents it was
    unreachable from any test until it became a function.
    """
    if seal_observed_at_arm_entry is True:
        return "VALID", ""
    return "VOID", (f"seal not observed on disk at arm entry "
                    f"(saw {seal_observed_at_arm_entry!r}); the population cannot be shown to "
                    f"have been frozen before it was measured")


def new_manifest(seal_sha, repo, rev, dirty, content_sha, binary, bsha, script_sha, budget, want):
    """The run manifest, with the seal it ran under named in it.

    A function for the same reason the oracle tally and the receipt writer became functions:
    built inline, the binding was a line in a call site and no test could reach it. Three
    defects tonight lived in exactly that position.
    """
    return {"schema": 2, "seal_sha256": seal_sha, "repo": repo, "rev": rev,
            "dirty": bool(dirty), "content_sha256": content_sha, "binary": binary,
            "binary_sha256": bsha, "script_sha256": script_sha, "budget": budget,
            "requested_cases": want, "run_status": "PROVISIONAL", "cases": []}


def claim_outdir(outdir):
    """Take an EXCLUSIVE, empty output directory, or refuse without touching anything.

    `os.makedirs(outdir, exist_ok=True)` let a second run reuse a directory that already held a
    VALID manifest. The rerun would overwrite case 001's receipts and then crash, and the
    earlier manifest -- still saying VALID -- would be left certifying a mixture of two runs'
    bytes. The verdict file is the whole point of the receipt lifecycle, so a stale one
    vouching for spliced evidence is worse than having none.

    Refusal happens BEFORE any write, so a rejected rerun leaves the existing evidence exactly
    as it found it.
    """
    if not outdir:
        return
    try:
        os.makedirs(outdir)
        return
    except FileExistsError:
        pass
    # ONLY A SUCCESSFUL EXCLUSIVE CREATE OWNS THE DIRECTORY. Accepting an existing EMPTY one
    # looked harmless and is not: two runs starting together both find it empty, both proceed,
    # and their receipts interleave under whichever manifest is written last. Emptiness is a
    # property of the instant you looked, not a claim on the directory.
    raise SystemExit(
        f"output directory {outdir} already exists.\n"
        f"Refusing it, empty or not: only a directory this run creates is exclusively owned, "
        f"and two runs that both accept an existing one will splice their receipts together "
        f"under a single verdict. Nothing was modified. Use a path that does not exist yet.")


def finalize_manifest(outdir, manifest, status, note=""):
    """Atomically stamp the run VALID or VOID.

    Receipts were previously written with the word PROVISIONAL and nothing ever replaced it,
    so a completed run and a run killed halfway were indistinguishable on disk -- and the
    reader had no way to tell which. The rename is atomic so a crash mid-write cannot leave a
    half-written manifest claiming VALID; a run that never reached this call stays provisional,
    which is to say invalid.
    """
    if not outdir:
        return
    manifest["run_status"] = status
    manifest["note"] = note
    manifest["admitted_cases"] = sum(1 for c in manifest["cases"] if c["admitted"])
    tmp = os.path.join(outdir, ".run-manifest.json.tmp")
    with open(tmp, "w") as fh:
        json.dump(manifest, fh, indent=1)
    os.replace(tmp, os.path.join(outdir, "run-manifest.json"))

def _selftest(require_orchestration=False):
    """Run the suite, and REMOVE WHAT IT ALLOCATED whether or not it finishes.

    Residual owned paths fail a normal return; an in-flight exception is preserved and simply
    reported alongside, because losing the real error to a cleanup complaint would be worse
    than the leak.
    """
    raised = None
    try:
        rc = _selftest_body(require_orchestration)
    except BaseException as exc:
        raised, rc = exc, 1
    survivors = _finish_tempdirs()
    if survivors:
        print(f"  FAIL the suite left {len(survivors)} owned temp path(s) behind: "
              f"{survivors[0]}")
        rc = 1
    if raised is not None:
        raise raised
    return rc


if __name__=="__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        sys.exit(_selftest("--require-orchestration" in sys.argv))

    # Recorded in the seal as a CLASS, not swallowed as a flag: a dirty build produces
    # artifact-tied diagnostics and can never produce a reproducible-source measurement.
    # Stripped before the positionals are read, or it would be taken for the output directory.
    diagnostic_dirty = "--diagnostic-dirty-build" in sys.argv
    smoke = "--smoke-not-a-measurement" in sys.argv
    # EVERY flag comes out before the positionals are read, or it is taken for one. The first
    # version stripped one of the two and the other was parsed as the case count.
    FLAGS = ("--diagnostic-dirty-build", "--smoke-not-a-measurement")
    argv = [a for a in sys.argv if a not in FLAGS]
    binary,repo,n,budget=argv[1],argv[2],int(argv[3]),int(argv[4])
    outdir=argv[5] if len(argv)>5 else None
    # NO SILENTLY UNSEALED AGGREGATE. Without an output directory there is no seal, no receipts
    # and no verdict -- yet the table printed all the same and looked exactly like a measurement.
    # An omitted directory is now an error, and the only way to run without one says so in its
    # name and in every line it prints.
    if not outdir and not smoke:
        raise SystemExit(
            "REFUSING TO START: no output directory, so nothing would be sealed and no receipts "
            "or verdict written -- and the table would still print. Give a directory, or pass "
            "--smoke-not-a-measurement to run unsealed and have every line say so.")
    def state():
        """Exact fixture state. Three things the first version got wrong, all peer-reviewed:

        it reduced the worktree to bool(dirty), so a tree dirty in DIFFERENT ways before and
        after compared equal; it ignored git's exit status, so a failed command produced an
        empty string that compared equal to a clean tree; and a detected change only printed
        a warning while the run carried on and reported numbers anyway.
        """
        r = subprocess.run(["git","-C",repo,"rev-parse","HEAD"],capture_output=True,text=True)
        d = subprocess.run(["git","-C",repo,"status","--porcelain"],capture_output=True,text=True)
        if r.returncode != 0 or d.returncode != 0:
            raise SystemExit(f"git failed in {repo}: cannot establish fixture state; refusing to score")
        # Porcelain names WHICH paths are dirty, never what is in them -- so a tree edited
        # mid-run compares equal to itself as long as the same files stay dirty. Hash the
        # content of every dirty path so an edit during the run is actually visible.
        dirty = d.stdout.strip()
        dig = hashlib.sha256()
        for line in dirty.splitlines():
            rel = line[3:].strip().strip('"')
            dig.update(rel.encode())
            try:
                with open(os.path.join(repo, rel), "rb") as fh:
                    dig.update(fh.read())
            except OSError:
                dig.update(b"<unreadable>")
        return r.stdout.strip(), dirty, dig.hexdigest()
    rev0,dirty0,cont0=state()
    # REFUSE A DIRTY FIXTURE. The content hash catches a tracked file edited mid-run, but it
    # is built from `status --porcelain`, which reports an untracked DIRECTORY as one entry
    # and says nothing about the files inside it, and renders a rename as a single path pair.
    # Rather than grow a second, subtler reimplementation of git's own state tracking, refuse
    # to measure anything but a clean tree -- which is what a frozen fixture is supposed to be.
    if dirty0:
        raise SystemExit(f"fixture {repo} is DIRTY; a frozen fixture must be clean\n{dirty0}")
    bsha=hashlib.sha256(open(binary,"rb").read()).hexdigest()
    # IDENTITY FIRST, THEN SELECTION. The check sat after build_cases, so a binary that would be
    # refused had already chosen the population -- and on a dirty or unidentifiable build, the
    # selection is exactly as unreproducible as the measurement would have been.
    _early = binary_build_identity(binary)
    if not _early["known"]:
        raise SystemExit(f"REFUSING TO START: cannot establish the binary's build identity "
                         f"({_early['reason']}). An unknown build is not a clean one.")
    if _early["vcs_modified"] and not diagnostic_dirty:
        raise SystemExit(
            f"REFUSING TO START: {binary} was built from a DIRTY tree "
            f"(revision {_early['vcs_revision']}); its source cannot be reproduced, so neither "
            f"the selection nor the measurement can be. Pass --diagnostic-dirty-build to record "
            f"it explicitly as artifact-tied development diagnostics.")
    cases=build_cases(binary,repo,n)
    claim_outdir(outdir)
    # ONE OWNER. This process created the directory, it is the only one that knows the selected
    # list, and it writes the seal here -- after selection, before the first arm. A separate
    # preflight cannot do this: it can check a path and hand control to something that creates
    # the directory, measures, and writes its own manifest, with nothing binding the three.
    seal_sha = seal_run(outdir, binary, bsha, repo, rev0, cont0, budget, n, cases,
                        diagnostic_dirty) if outdir else None
    if smoke and not outdir:
        print("=== SMOKE, NOT A MEASUREMENT: unsealed, no receipts, no verdict. "
              "Nothing printed below may be quoted. ===")
    print(f"repo {repo}\nrev {rev0} dirty={'YES' if dirty0 else 'no'}")
    print(f"binary {binary}\nbinary sha256 {bsha}\nbudget {budget}  cases {len(cases)}\n")
    # MEANS ARE NOT REPORTABLE HERE and medians are. The grep arms are heavy-tailed: one
    # sampled symbol in cli is literally named `resolve`, which rg matches on 4,492 lines for
    # 1.37 MB, single-handedly setting the mean. That is a property of the NAME, not of grep
    # or of the repository, and a mean lets one such case decide the headline. Both are
    # printed; the median is the one to quote.
    tg=tp=to=0; k=0; dropped=0; attempt=0
    ostate={"valid":0,"na":0,"bytes":[],"loc":0,"dec":0}   # the control's OWN tally
    # The manifest NAMES THE SEAL it ran under, so a run's results cannot be paired with a
    # different seal after the fact.
    manifest=new_manifest(seal_sha, repo, rev0, dirty0, cont0, binary, bsha,
                          file_sha256(os.path.abspath(__file__)), budget, n)
    gvals=[]; pvals=[]; ovals=[]; svals=[]
    gL=gD=pL=pD=oL=oD=sL=sD=0
    seal_seen = {"at_first_arm": None}
    def real_arms(q,name,fp,s_lo,s_hi):
        if seal_seen["at_first_arm"] is None:
            seal_seen["at_first_arm"] = seal_present_at_arm_entry(outdir, seal_sha)
        return (("graph",)  + graph_arm(binary,repo,q,name,fp,budget,s_lo,s_hi),
                ("grep",)   + grep_arm(repo,q,name,fp,False,s_lo,s_hi),
                ("phrase",) + phrase_arm(repo,q,name,fp,s_lo,s_hi),
                ("oracle",) + grep_arm(repo,q,name,fp,True,s_lo,s_hi))
    for q,name,fp,s_lo,s_hi in cases:
        _armed = {a[0]: a[1:] for a in real_arms(q,name,fp,s_lo,s_hi)}
        g,g_l,g_d,gok,g_raw,g_m = _armed["graph"]
        pr,p_l,p_d,pok,p_raw,p_m = _armed["grep"]
        oc,o_l,o_d,ook,o_raw,o_m = _armed["oracle"]
        sc,s_l,s_d,sok,s_raw,s_m = _armed["phrase"]
        # THE ORACLE IS NOT AN ARM. It is handed the target's NAME, which no other arm gets,
        # so it is not a same-input comparator and cannot sit in a comparative denominator.
        # It was also inside the shared success gate, which is worse than cosmetic: a case
        # where only the oracle failed was DROPPED FROM EVERY ARM, letting a non-comparable
        # control decide which cases the real comparison is computed over.
        # It stays as an explicitly labelled capability ceiling and nothing else.
        attempt += 1
        # THE CASE ID IS THE ATTEMPT, not the admission. Numbering by admitted cases meant a
        # failure had no id at all, so the receipts on disk could not be lined up against the
        # attempts that produced them.
        cid = "%03d-%s-%s" % (attempt, re.sub(r"\W+","_",name)[:48],
                              re.sub(r"\W+","_",os.path.basename(fp))[:32])
        # RECORD, THEN ADMIT -- in that order, enforced by record_attempt doing both.
        admitted = settle_attempt(outdir, cid, q, name, fp, s_lo, s_hi,
                                  (("graph",g,g_l,g_d,gok,g_raw,g_m), ("grep",pr,p_l,p_d,pok,p_raw,p_m),
                                   ("phrase",sc,s_l,s_d,sok,s_raw,s_m), ("oracle",oc,o_l,o_d,ook,o_raw,o_m)),
                                  ostate)
        manifest["cases"].append({"case_id":cid,"symbol":name,"file":fp,"span":[s_lo,s_hi],
                                  "admitted":admitted,
                                  "arm_ok":{"graph":gok,"grep":pok,"phrase":sok,"oracle":ook}})
        # THE ORACLE IS COUNTED FIRST, before the comparable-admission gate. Counting it after
        # meant its denominator silently became "valid oracle runs among PAIRED-ADMITTED cases",
        # not oracle validity -- the fx-cli audit found all 20 oracle receipts valid while the
        # manifest said 19, because one case was dropped for an unrelated arm. A control with a
        # denominator that moves when another arm fails is not independent of that arm.
        if not admitted:
            dropped+=1      # a failed comparable arm excludes the case, never scores as a miss
            continue
        k+=1; tg+=g; tp+=pr
        gL+=g_l; gD+=g_d; pL+=p_l; pD+=p_d
        gvals.append(g); pvals.append(pr); svals.append(sc)
        sL+=s_l; sD+=s_d

    rev1,dirty1,cont1=state()
    if (rev0,dirty0,cont0)!=(rev1,dirty1,cont1):
        finalize_manifest(outdir, manifest, "VOID", "source state changed during the run")
        # Exit non-zero. Printing "void" and then printing the table anyway is how a voided
        # run gets quoted later by someone reading only the numbers.
        raise SystemExit("SOURCE STATE CHANGED DURING THE RUN -- results void, refusing to report")
    if not k:
        finalize_manifest(outdir, manifest, "VOID", "no scorable cases")
        print("no scorable cases"); sys.exit(1)
    def med(a):
        """True median. The first version returned a[len(a)//2], which on an even-length list
        is the UPPER of the two middle values -- with n=20 per fixture that is the 11th, not
        the median, and every figure it produced was slightly high. Peer review caught it as
        'upper-middle, not median'."""
        if not a: return 0
        a = sorted(a); n = len(a)
        return a[n//2] if n % 2 else (a[n//2 - 1] + a[n//2]) / 2
    print(f"{'arm':24s} {'median B':>10s} {'locator':>9s} {'declaration':>13s}")
    print(f"{'graph (prose)':24s} {med(gvals):10,.0f} {gL:6d}/{k} {gD:10d}/{k}")
    print(f"{'grep (prose)':24s} {med(pvals):10,.0f} {pL:6d}/{k} {pD:10d}/{k}")
    print(f"{'grep doc-phrase+read':24s} {med(svals):10,.0f} {sL:6d}/{k} {sD:10d}/{k}")
    print(budget_caveat(budget))
    print("  locator = pointed at the right file+span.  declaration = showed the decl line.")
    print("  Computed identically for the three arms above, which receive byte-identical")
    print("  canonical query text and derive their search from it alone.")
    # BELOW THE LINE, AND OUT OF THE DENOMINATOR. The oracle is handed the target's NAME. It is
    # not a same-input comparator and putting it in the same block invited exactly the reading
    # the rest of this file exists to prevent.
    print(f"\n{'-- capability ceiling, NOT a comparator (given the target name) --':60s}")
    ovalid, ona, ovals, oL, oD = (ostate["valid"], ostate["na"], ostate["bytes"],
                                  ostate["loc"], ostate["dec"])
    print(f"{'grep (oracle: name)':24s} {med(ovals):10,.0f} {oL:6d}/{ovalid} {oD:10d}/{ovalid}")
    print(f"  its own denominator is {ovalid} of {attempt} ATTEMPTS, not {k} paired-admitted cases:")
    print(f"  {ona} oracle run(s) failed and are N/A. A control whose denominator moves when")
    print(f"  another arm fails is not independent of that arm.")
    print(f"  A failed oracle run is not an oracle miss, the same rule the other arms get.")
    print(f"\nscored {k}, dropped {dropped} (arm failure, not scored as miss)")
    print(f"state before/after: {rev0} dirty={dirty0}  ->  {rev1} dirty={dirty1}")
    if outdir: print(f"per-case receipts: {outdir}  (run-manifest.json carries the verdict)")
    me=os.path.abspath(__file__)
    print(f"script sha256 {hashlib.sha256(open(me,'rb').read()).hexdigest()}")
    # LAST THING THE RUN DOES. Everything above is already on disk as PROVISIONAL; this is what
    # makes the receipts quotable, and a run that dies before here stays provisional -- which
    # reads as invalid, deliberately.
    manifest["seal_present_at_first_arm"] = seal_seen["at_first_arm"]
    # A FALSE OBSERVATION MUST VOID THE RUN. Recording that the seal was absent at arm entry and
    # then stamping VALID is a manifest contradicting itself: the verdict is exactly the claim
    # that the population was frozen before it was measured. Recorded-and-ignored is how a
    # warning becomes a number somebody quotes.
    _verdict, _why = run_verdict(seal_seen["at_first_arm"]) if outdir else ("VALID", "")
    if _verdict != "VALID":
        finalize_manifest(outdir, manifest, _verdict, _why)
        raise SystemExit("SEAL WAS NOT PRESENT AT ARM ENTRY -- run voided, results not reported")
    finalize_manifest(outdir, manifest, "VALID",
                      f"scored {k}, dropped {dropped}, oracle valid {ostate['valid']} n/a {ostate['na']} of {attempt} attempts")
