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

RANK = re.compile(r'^\s*(\d+)\.\s+(\S+?):(\d+)')
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


def score(out_bytes, repo, name, target_file, lo, hi, ranked_only=False):
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
                current_is_target = ok and (not lo or lo <= line <= hi)
                if current_is_target:
                    locator = True
                # A HEADER IS NOT A DECLARATION. The header names the symbol, so for a target
                # called TargetAlpha the line `1. a.go:1-5 TargetAlpha [complete] s=1` matched
                # DEFN and earned a declaration credit with no source body rendered at all --
                # exactly the locator/declaration conflation the split metric exists to
                # prevent. Only body lines beneath a target header can establish it.
                continue
            # body text: only creditable when the header above it was the target
            if current_is_target and d.search(ln):
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
        if d.search(text):              # match the TEXT, never the path-prefixed line
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
        return 0, False, False, True, b"", {"argv": [], "rc": None, "stderr": "no phrase in q"}
    p = subprocess.run(["rg", "-n", "--no-heading", "-F", "-g", "!node_modules", phrase, repo],
                       capture_output=True, timeout=300)
    # rg exits 1 for "no matches", a real empty result. Above that is a broken run; BELOW zero
    # is signal death (SIGKILL is -9), which the original `> 1` guard admitted as a valid run
    # with empty output -- scoring a killed process as a legitimate miss.
    meta = {"argv": ["rg","-n","--no-heading","-F","-g","!node_modules",phrase,repo],
            "rc": p.returncode, "stderr": p.stderr.decode("utf8","replace")[:4000]}
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
    if not term: return 0, False, False, False, b"", {"argv": [], "rc": None, "stderr": "no term"}
    p=subprocess.run(["rg","-n","--no-heading","-g","!node_modules",term,repo],capture_output=True,timeout=300)
    # rg exits 1 for "no matches", which is a real empty result, not a failure. Anything
    # above that is a broken run and the case is dropped rather than counted as a miss.
    # rg exits 1 for "no matches", which is a real empty result. Anything ABOVE that is a
    # broken run -- and anything BELOW zero is a signal death (SIGKILL is -9), which the
    # original `> 1` guard let through as a valid run with empty output, scoring a killed
    # process as a legitimate miss. A timeout or OOM would have silently become evidence.
    meta = {"argv": ["rg","-n","--no-heading","-g","!node_modules",term,repo],
            "rc": p.returncode, "stderr": p.stderr.decode("utf8","replace")[:4000],
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
            "rc": p.returncode, "stderr": p.stderr.decode("utf8","replace")[:4000]}
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
def _mkrepo(files):
    import tempfile
    d = tempfile.mkdtemp(prefix="pharm-")
    for rel, body in files.items():
        fp = os.path.join(d, rel)
        os.makedirs(os.path.dirname(fp), exist_ok=True)
        with open(fp, "w") as fh: fh.write(body)
    return d

def _canonical_q(dlines, name):
    """Exactly what build_cases produces, so the tests exercise the real query bytes."""
    q = "\n".join(strip_name(l, name).replace("\x00", "\n").rstrip() for l in dlines)
    return "\n".join(l for l in (x.strip() for x in q.splitlines()) if l)[:240]

def _selftest():
    fails = []
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

    print("\nALL PHRASE-ARM FALSIFIERS PASS" if not fails else "\nFAILURES: " + ", ".join(fails))
    return 1 if fails else 0


if __name__=="__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        sys.exit(_selftest())

    binary,repo,n,budget=sys.argv[1],sys.argv[2],int(sys.argv[3]),int(sys.argv[4])
    outdir=sys.argv[5] if len(sys.argv)>5 else None
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
    cases=build_cases(binary,repo,n)
    if outdir: os.makedirs(outdir,exist_ok=True)
    print(f"repo {repo}\nrev {rev0} dirty={'YES' if dirty0 else 'no'}")
    print(f"binary {binary}\nbinary sha256 {bsha}\nbudget {budget}  cases {len(cases)}\n")
    # MEANS ARE NOT REPORTABLE HERE and medians are. The grep arms are heavy-tailed: one
    # sampled symbol in cli is literally named `resolve`, which rg matches on 4,492 lines for
    # 1.37 MB, single-handedly setting the mean. That is a property of the NAME, not of grep
    # or of the repository, and a mean lets one such case decide the headline. Both are
    # printed; the median is the one to quote.
    tg=tp=to=0; k=0; dropped=0
    gvals=[]; pvals=[]; ovals=[]; svals=[]
    gL=gD=pL=pD=oL=oD=sL=sD=0
    for q,name,fp,s_lo,s_hi in cases:
        g,g_l,g_d,gok,g_raw,g_m=graph_arm(binary,repo,q,name,fp,budget,s_lo,s_hi)
        pr,p_l,p_d,pok,p_raw,p_m=grep_arm(repo,q,name,fp,False,s_lo,s_hi)
        oc,o_l,o_d,ook,o_raw,o_m=grep_arm(repo,q,name,fp,True,s_lo,s_hi)
        sc,s_l,s_d,sok,s_raw,s_m=phrase_arm(repo,q,name,fp,s_lo,s_hi)
        # THE ORACLE IS NOT AN ARM. It is handed the target's NAME, which no other arm gets,
        # so it is not a same-input comparator and cannot sit in a comparative denominator.
        # It was also inside the shared success gate, which is worse than cosmetic: a case
        # where only the oracle failed was DROPPED FROM EVERY ARM, letting a non-comparable
        # control decide which cases the real comparison is computed over.
        # It stays as an explicitly labelled capability ceiling and nothing else.
        if not (gok and pok and sok):
            dropped+=1      # a failed comparable arm excludes the case, never scores as a miss
            continue
        k+=1; tg+=g; tp+=pr; to+=oc
        gL+=g_l; gD+=g_d; pL+=p_l; pD+=p_d; oL+=o_l; oD+=o_d
        gvals.append(g); pvals.append(pr); ovals.append(oc); svals.append(sc)
        sL+=s_l; sD+=s_d
        if outdir:
            # RAW per-arm stdout plus full provenance. A summary cannot be re-scored, and this
            # scorer has now been wrong six times; only the original bytes allow re-derivation
            # without re-running a cohort.
            #
            # The case id is UNIQUE per case, not the symbol name: a repository contains many
            # symbols with the same name, and keying on the name silently overwrote one
            # homonym's receipts with another's -- so the file on disk described a different
            # case than the row it was filed under.
            cid = "%03d-%s-%s" % (k, re.sub(r"\W+","_",name)[:48],
                                  re.sub(r"\W+","_",os.path.basename(fp))[:32])
            base=os.path.join(outdir,cid)
            for arm,blob,m in (("graph",g_raw,g_m),("grep",p_raw,p_m),
                               ("phrase",s_raw,s_m),("oracle",o_raw,o_m)):
                with open(base+"."+arm+".raw","wb") as rf: rf.write(blob or b"")
                # Everything scored, not only what rg printed: the phrase arm scores the
                # windows it reads, and those bytes were absent from the record entirely.
                if m.get("evidence") is not None:
                    with open(base+"."+arm+".scored","wb") as rf: rf.write(m["evidence"])
                with open(base+"."+arm+".json","w") as jf:
                    json.dump({"argv": m.get("argv"), "returncode": m.get("rc"),
                               "stderr": m.get("stderr"), "oracle": m.get("oracle", False)},
                              jf, indent=1)
            with open(base+".txt","w") as fh:
                fh.write(f"case_id: {cid}\nquery: {q!r}\n"
                         f"target: {name} {fp}:{s_lo}-{s_hi}\n"
                         f"run_status: PROVISIONAL until the whole run exits 0\n"
                         f"graph  bytes {g}  locator {g_l}  declaration {g_d}\n"
                         f"grep   bytes {pr} locator {p_l}  declaration {p_d}\n"
                         f"phrase bytes {sc} locator {s_l}  declaration {s_d}\n"
                         f"oracle bytes {oc} locator {o_l}  declaration {o_d}"
                         f"   (CAPABILITY CONTROL: given the target name; not a comparator)\n")
    rev1,dirty1,cont1=state()
    if (rev0,dirty0,cont0)!=(rev1,dirty1,cont1):
        # Exit non-zero. Printing "void" and then printing the table anyway is how a voided
        # run gets quoted later by someone reading only the numbers.
        raise SystemExit("SOURCE STATE CHANGED DURING THE RUN -- results void, refusing to report")
    if not k:
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
    print(f"{'grep (oracle: name)':24s} {med(ovals):10,.0f} {oL:6d}/{k} {oD:10d}/{k}")
    print("  locator = pointed at the right file+span.  declaration = showed the decl line.")
    print("  Both computed identically for every arm; medians, because grep is heavy-tailed.")
    print(f"\nscored {k}, dropped {dropped} (arm failure, not scored as miss)")
    print(f"state before/after: {rev0} dirty={dirty0}  ->  {rev1} dirty={dirty1}")
    if outdir: print(f"per-case receipts: {outdir}")
    me=os.path.abspath(__file__)
    print(f"script sha256 {hashlib.sha256(open(me,'rb').read()).hexdigest()}")
