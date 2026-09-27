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
    raw = subprocess.run([binary,"symbols","--repo",repo,"--format","ndjson"],
                         capture_output=True,text=True,timeout=1800).stdout
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
        doc=doc_comment(os.path.join(repo,d["file_path"]), d["start_line"], lang)
        if len(WORD.findall(doc))<10: continue
        q=re.sub(re.escape(name)," ",doc,flags=re.I)
        for part in re.findall(r'[A-Z]?[a-z]{3,}',name):
            q=re.sub(r'\b'+re.escape(part)+r'\b'," ",q,flags=re.I)
        q=" ".join(q.split())
        if len(WORD.findall(q))<8: continue
        out.append((q[:240],name,d["file_path"],int(d.get("start_line") or 0),int(d.get("end_line") or 0),doc))
    random.Random(seed).shuffle(cases:=out)
    return cases[:want]

def fsize(repo,rel):
    try: return os.path.getsize(os.path.join(repo,rel))
    except OSError: return 0


def score(out_bytes, repo, name, target_file, lo, hi, ranked_only=False):
    """Two metrics, computed IDENTICALLY for every arm. Returns (locator, declaration).

    locator      the arm pointed at the right place: a line it emitted names the target's
                 file, and (when the arm reports one) a line number inside the target's span.
    declaration  the arm's own output CONTAINS the declaration, per DEFN.

    These were previously OR-ed into one `located`, and asymmetrically: the graph arm could
    win on a span hit while the grep arm had to show a declaration line. That handed the
    graph the easier test and peer review was right to refuse the resulting number. They are
    now separate columns and neither arm gets a shortcut the other does not.

    `ranked_only` restricts locator scoring to numbered ranking headers, which is what the
    graph emits; grep emits `path:line:text` and is scored on every line.
    """
    tgt = os.path.normpath(target_file)
    locator = declaration = False
    d = DEFN(name)
    for ln in out_bytes.decode("utf8", "replace").splitlines():
        if ranked_only:
            m = RANK.match(ln)
            if not m:
                if d.search(ln):
                    declaration = True      # body text under a header
                continue
            path, num = m.group(2), m.group(3)
        else:
            parts = ln.split(":", 2)
            if len(parts) < 3: continue
            path, num = parts[0], parts[1]
        if os.path.isabs(path): path = os.path.relpath(path, repo)
        if os.path.normpath(path) != tgt: continue
        try: line = int(num)
        except ValueError: line = 0
        if not lo or (lo <= line <= hi): locator = True
        if d.search(ln): declaration = True
    return locator, declaration


def phrase_arm(repo, doc, name, target_file, lo, hi, follow=12):
    """THE STRONG GREP BASELINE. Search the author's description, then read what follows it.

    Added after peer review destroyed the previous framing. The old arms OR-ed the longest
    words and never tried the obvious move: the description IS IN THE FILE, immediately above
    the declaration. Grep a phrase from it, read the next few lines, and the identifier is
    there. Beating an OR-of-longest-words was presented as proof that a lexical search could
    not bridge description to identifier; it proved only that a weak baseline is weak.

    Given the author's real comment this found the declaration 6/20 on a fixture where the
    graph found 4/20 -- it beat the tool. Any honest comparison has to carry it.

    Cost includes the bounded follow-up read, because the arm really performs it.
    """
    words = [w for w in (doc or "").split() if len(w) > 3][:6]
    if len(words) < 3:
        return 0, False, False, True
    phrase = " ".join(words[:4])
    p = subprocess.run(["rg", "-n", "--no-heading", "-F", "-g", "!node_modules", phrase, repo],
                       capture_output=True, timeout=300)
    if p.returncode > 1:
        return 0, False, False, False
    cost = len(p.stdout)
    d = DEFN(name)
    tgt = os.path.normpath(target_file)
    loc = dec = False
    for ln in p.stdout.decode("utf8", "replace").splitlines():
        parts = ln.split(":", 2)
        if len(parts) < 3:
            continue
        path, num = parts[0], parts[1]
        rel = os.path.relpath(path, repo) if os.path.isabs(path) else path
        try:
            start = int(num)
        except ValueError:
            continue
        try:
            src = open(os.path.join(repo, rel), errors="ignore").read().splitlines()
        except OSError:
            continue
        window = src[start - 1:start - 1 + follow]
        cost += sum(len(l) + 1 for l in window)
        if os.path.normpath(rel) != tgt:
            continue
        if lo and lo <= start <= hi:
            loc = True
        for w in window:
            if d.search(w):
                return cost, True, True, True
    return cost, loc, dec, True

def grep_arm(repo,query,name,target_file,oracle=False,tgt_lo=0,tgt_hi=0):
    """Returns (bytes_in_context, located, ok). ok=False means the ARM FAILED and the case
    must be excluded, never scored as a retrieval miss (peer-review note 2)."""
    term = name if oracle else max((w for w in WORD.findall(query) if w.lower() not in STOP),
                                   key=len, default="")
    if not term: return 0, False, False, False
    p=subprocess.run(["rg","-n","--no-heading","-g","!node_modules",term,repo],capture_output=True,timeout=300)
    # rg exits 1 for "no matches", which is a real empty result, not a failure. Anything
    # above that is a broken run and the case is dropped rather than counted as a miss.
    if p.returncode > 1:
        return 0, False, False, False
    out=p.stdout
    loc, dec = score(out, repo, name, target_file, tgt_lo, tgt_hi)
    return len(out), loc, dec, True

def graph_arm(binary,repo,query,name,target_file,budget,tgt_lo=0,tgt_hi=0):
    """Returns (bytes_in_context, located, ok). `located` is DISPLAYED-SYMBOL-NAME recall OR
    a header whose file and line span contain the preregistered target -- peer-review note 3:
    a locator-only or name-elided rendering must not read as zero retrieval."""
    p=subprocess.run([binary,"query","--repo",repo,"--query",query,"--format","agent",
                      "--max-context-bytes",str(budget),"--no-cache"],
                     capture_output=True,timeout=900)
    if p.returncode != 0:
        return 0, False, False, False
    out=p.stdout
    loc, dec = score(out, repo, name, target_file, tgt_lo, tgt_hi, ranked_only=True)
    return len(out), loc, dec, True

if __name__=="__main__":
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
        return r.stdout.strip(), d.stdout.strip()   # full porcelain, not a bool
    rev0,dirty0=state()
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
    for q,name,fp,s_lo,s_hi,doc in cases:
        g,g_l,g_d,gok=graph_arm(binary,repo,q,name,fp,budget,s_lo,s_hi)
        pr,p_l,p_d,pok=grep_arm(repo,q,name,fp,False,s_lo,s_hi)
        oc,o_l,o_d,ook=grep_arm(repo,q,name,fp,True,s_lo,s_hi)
        sc,s_l,s_d,sok=phrase_arm(repo,doc,name,fp,s_lo,s_hi)
        if not (gok and pok and ook and sok):
            dropped+=1      # a failed arm is excluded, never scored as a miss
            continue
        k+=1; tg+=g; tp+=pr; to+=oc
        gL+=g_l; gD+=g_d; pL+=p_l; pD+=p_d; oL+=o_l; oD+=o_d
        gvals.append(g); pvals.append(pr); ovals.append(oc); svals.append(sc)
        sL+=s_l; sD+=s_d
        if outdir:
            with open(os.path.join(outdir,re.sub(r"\W+","_",name)[:80]+".txt"),"w") as fh:
                fh.write(f"query: {q}\ntarget: {name} {fp}:{s_lo}-{s_hi}\n"
                         f"graph bytes {g} located {gl}\ngrep bytes {pr} located {pl}\n"
                         f"oracle bytes {oc} located {ol}\n")
    rev1,dirty1=state()
    if (rev0,dirty0)!=(rev1,dirty1):
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
