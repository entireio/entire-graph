#!/usr/bin/env python3
"""Doc-comment retrieval benchmark: does author-written prose find the symbol it describes?

The labels are not mine. Every case is a symbol plus the doc comment its own author wrote
above it, so the tool is not graded against queries I invented after seeing its output.

The symbol's name is STRIPPED from the query. Without that the query contains the answer
verbatim and the test degenerates into an identifier match -- which is the shape the tool
is already good at, and not the shape agents actually send. This measures the prose case
(cf. open issue #247: prose queries lose to code-spelled terms).

Reported per budget:
  hit@1 / hit@5   strict: the target symbol itself was retrieved
  samefile@1      the top hit is in the target's own file -- one hop away, an agent gets there
  miss            neither

Deterministic, free, read-only. Fixture revision is printed and must be clean.
"""
import subprocess, sys, os, re, json, random, hashlib, collections

RANK = re.compile(r'^\s*(\d+)\.\s+(\S+?):(\d+)')
WORD = re.compile(r'[A-Za-z]{3,}')

def doc_comment(path, start_line):
    """Go doc comment: contiguous // lines immediately above the declaration."""
    try:
        lines = open(path, errors="ignore").read().splitlines()
    except OSError:
        return ""
    out, i = [], start_line - 2
    while i >= 0 and lines[i].strip().startswith("//"):
        out.append(lines[i].strip().lstrip("/").strip()); i -= 1
    return " ".join(reversed(out))

def build_cases(binary, repo, want, seed=7):
    raw = subprocess.run([binary, "symbols", "--repo", repo, "--format", "ndjson"],
                         capture_output=True, text=True, timeout=1800).stdout
    cands = []
    for line in raw.splitlines():
        try: d = json.loads(line)
        except Exception: continue
        if d.get("record_type") != "symbol" or d.get("language") != "Go": continue
        if d.get("kind") not in ("function", "method"): continue
        name = d.get("name") or ""
        if len(name) < 6: continue
        doc = doc_comment(os.path.join(repo, d["file_path"]), d["start_line"])
        if len(WORD.findall(doc)) < 10: continue
        # strip the symbol name (and its case-split words) so the query is prose, not the answer
        q = re.sub(re.escape(name), " ", doc, flags=re.I)
        for part in re.findall(r'[A-Z]?[a-z]{3,}', name):
            q = re.sub(r'\b'+re.escape(part)+r'\b', " ", q, flags=re.I)
        q = " ".join(q.split())
        if len(WORD.findall(q)) < 8: continue
        cands.append((q[:240], name, d["file_path"]))
    random.Random(seed).shuffle(cands)
    return cands[:want]

def run(binary, repo, q, budget):
    p = subprocess.run([binary, "query", "--repo", repo, "--query", q, "--format", "agent",
                        "--max-context-bytes", str(budget), "--no-cache"],
                       capture_output=True, text=True, timeout=900)
    hits = []
    for line in p.stdout.splitlines():
        m = RANK.match(line)
        if m: hits.append((int(m.group(1)), m.group(2), line))
    return hits, len(p.stdout)

if __name__ == "__main__":
    binary, repo, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
    budgets = [int(x) for x in sys.argv[4].split(",")]
    rev = subprocess.run(["git","-C",repo,"rev-parse","HEAD"],capture_output=True,text=True).stdout.strip()
    dirty = subprocess.run(["git","-C",repo,"status","--porcelain"],capture_output=True,text=True).stdout.strip()
    cases = build_cases(binary, repo, n)
    print(f"repo {repo}\nrev  {rev} dirty={'YES' if dirty else 'no'}\ncases {len(cases)} (Go func/method, >=10-word doc, name stripped)\n")
    print(f"{'budget':>7s} {'bytes':>8s} {'hit@1':>7s} {'hit@5':>7s} {'samefile@1':>11s} {'miss':>6s}")
    for b in budgets:
        h1=h5=sf=0; tot=0
        for q, name, fp in cases:
            hits, size = run(binary, repo, q, b); tot += size
            ranks = [r for r, _, line in hits if re.search(r'\b'+re.escape(name)+r'\b', line)]
            if 1 in ranks: h1 += 1
            if any(r <= 5 for r in ranks): h5 += 1
            elif hits and hits[0][1] == fp: sf += 1
        m = len(cases) - h5 - sf
        print(f"{b:7d} {tot/len(cases):8,.0f} {h1:4d}/{len(cases)} {h5:4d}/{len(cases)} {sf:8d}/{len(cases)} {m:6d}")
    me=os.path.abspath(__file__)
    print(f"\nsha256 {hashlib.sha256(open(me,'rb').read()).hexdigest()}")
