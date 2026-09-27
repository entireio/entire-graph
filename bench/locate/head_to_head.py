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

def DEFN(name):
    """Does this line DECLARE `name`, in any of the languages under test?

    Go        func Name(  |  func (r T) Name(
    TS/JS     function Name(  export function Name(  class Name  const Name = (  Name(...) {
    A bare mention must not count, or the oracle scores a hit on every call site.
    """
    n = re.escape(name)
    return re.compile(
        r'(?:\bfunc\b[^/]*\b' + n + r'\b'              # Go func / method
        r'|\b(?:function|class|interface|type)\s+' + n + r'\b'
        r'|\b(?:const|let|var)\s+' + n + r'\s*[:=]'     # const Name = (...) =>
        r'|^\s*(?:export\s+)?(?:async\s+)?' + n + r'\s*\\([^)]*\\)\s*[:{]'  # method shorthand
        r')')
STOP = set("""the and for that with this from into when what which whether such been were are
was has have had not but its it's only also then than they them their there here does doing
done any all can may must should would could each other some more most less use used using
via per off out over under above below before after while during about against between""".split())

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
        out.append((q[:240],name,d["file_path"],int(d.get("start_line") or 0),int(d.get("end_line") or 0)))
    random.Random(seed).shuffle(cases:=out)
    return cases[:want]

def fsize(repo,rel):
    try: return os.path.getsize(os.path.join(repo,rel))
    except OSError: return 0

def grep_arm(repo,query,name,target_file,oracle=False):
    """Returns (bytes_in_context, located, ok). ok=False means the ARM FAILED and the case
    must be excluded, never scored as a retrieval miss (peer-review note 2)."""
    term = name if oracle else max((w for w in WORD.findall(query) if w.lower() not in STOP),
                                   key=len, default="")
    if not term: return None
    p=subprocess.run(["rg","-n","--no-heading","-g","!node_modules",term,repo],capture_output=True,timeout=300)
    # rg exits 1 for "no matches", which is a real empty result, not a failure. Anything
    # above that is a broken run and the case is dropped rather than counted as a miss.
    if p.returncode > 1:
        return 0, False, False
    out=p.stdout
    for ln in out.decode("utf8","replace").splitlines():
        if DEFN(name).search(ln):
            return len(out), True, True
    return len(out), False, True

def graph_arm(binary,repo,query,name,target_file,budget,tgt_lo=0,tgt_hi=0):
    """Returns (bytes_in_context, located, ok). `located` is DISPLAYED-SYMBOL-NAME recall OR
    a header whose file and line span contain the preregistered target -- peer-review note 3:
    a locator-only or name-elided rendering must not read as zero retrieval."""
    p=subprocess.run([binary,"query","--repo",repo,"--query",query,"--format","agent",
                      "--max-context-bytes",str(budget),"--no-cache"],
                     capture_output=True,timeout=900)
    if p.returncode != 0:
        return 0, False, False
    out=p.stdout
    for ln in out.decode("utf8","replace").splitlines():
        m=RANK.match(ln)
        if not m: continue
        if re.search(r'\b'+re.escape(name)+r'\b', ln):
            return len(out), True, True
        # span fallback: the target's own file, with the header's line inside the symbol range
        if os.path.normpath(m.group(2))==os.path.normpath(target_file):
            try: line=int(m.group(3))
            except ValueError: continue
            if tgt_lo and tgt_lo<=line<=tgt_hi:
                return len(out), True, True
    return len(out), False, True

if __name__=="__main__":
    binary,repo,n,budget=sys.argv[1],sys.argv[2],int(sys.argv[3]),int(sys.argv[4])
    outdir=sys.argv[5] if len(sys.argv)>5 else None
    def state():
        rev=subprocess.run(["git","-C",repo,"rev-parse","HEAD"],capture_output=True,text=True).stdout.strip()
        dirty=subprocess.run(["git","-C",repo,"status","--porcelain"],capture_output=True,text=True).stdout.strip()
        return rev, bool(dirty)
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
    tg=tp=to=0; lg=lp=lo=0; k=0; dropped=0
    gvals=[]; pvals=[]; ovals=[]
    for q,name,fp,s_lo,s_hi in cases:
        g,gl,gok=graph_arm(binary,repo,q,name,fp,budget,s_lo,s_hi)
        pr,pl,pok=grep_arm(repo,q,name,fp)
        oc,ol,ook=grep_arm(repo,q,name,fp,oracle=True)
        if not (gok and pok and ook):
            dropped+=1      # a failed arm is excluded, never scored as a miss
            continue
        k+=1; tg+=g; tp+=pr; to+=oc; lg+=gl; lp+=pl; lo+=ol
        gvals.append(g); pvals.append(pr); ovals.append(oc)
        if outdir:
            with open(os.path.join(outdir,re.sub(r"\W+","_",name)[:80]+".txt"),"w") as fh:
                fh.write(f"query: {q}\ntarget: {name} {fp}:{s_lo}-{s_hi}\n"
                         f"graph bytes {g} located {gl}\ngrep bytes {pr} located {pl}\n"
                         f"oracle bytes {oc} located {ol}\n")
    rev1,dirty1=state()
    if (rev0,dirty0)!=(rev1,dirty1):
        print("!! SOURCE STATE CHANGED DURING THE RUN -- results void\n")
    if not k:
        print("no scorable cases"); sys.exit(1)
    def med(a):
        a=sorted(a); return a[len(a)//2] if a else 0
    print(f"{'arm':24s} {'median B':>10s} {'mean B':>12s} {'located':>10s}")
    print(f"{'graph (prose)':24s} {med(gvals):10,.0f} {tg/k:12,.0f} {lg:6d}/{k}")
    print(f"{'grep (prose)':24s} {med(pvals):10,.0f} {tp/k:12,.0f} {lp:6d}/{k}")
    print(f"{'grep (oracle: name)':24s} {med(ovals):10,.0f} {to/k:12,.0f} {lo:6d}/{k}")
    print("  quote the MEDIAN: the grep arms are heavy-tailed on common symbol names.")
    print(f"\nscored {k}, dropped {dropped} (arm failure, not scored as miss)")
    print(f"state before/after: {rev0} dirty={dirty0}  ->  {rev1} dirty={dirty1}")
    if outdir: print(f"per-case receipts: {outdir}")
    me=os.path.abspath(__file__)
    print(f"script sha256 {hashlib.sha256(open(me,'rb').read()).hexdigest()}")
