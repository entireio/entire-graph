#!/usr/bin/env python3
"""Per-case budget comparison. Answers ONE question: which target does a smaller budget LOSE?

An aggregate hit@5 that is equal at two budgets does NOT mean the same targets were found.
Peer review caught exactly that in my own table: 4096 and 24576 both showed hit@5 = 25/30,
but 4096 had 4 misses against 24576's 3 -- so at least one target is found by one and not
the other, and the stated falsifier ("loses ANY target at ANY rank") was already violated by
a table I presented as passing it.

This prints the per-case set difference so the claim is checkable rather than aggregate.
"""
import sys, os, subprocess, re
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import doccomment_bench as D

# ONE implementation, imported rather than copied. The copy that used to live here was
# neither called by a test nor reached by the mutation sweep -- both targeted the sibling -- so
# it could have drifted silently while every guard reported green.
RANK, ranked_hit = D.RANK, D.ranked_hit

def found(binary, repo, q, name, target_file, budget):
    hits, _ = D.run(binary, repo, q, budget)
    ranks = [r for r, _, line in hits if ranked_hit(line, name, target_file, repo)]
    top = hits[0][1] if hits else None
    return (min(ranks) if ranks else None), top

if __name__ == "__main__":
    binary, repo, n, lo, hi = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5])
    rev = subprocess.run(["git","-C",repo,"rev-parse","HEAD"],capture_output=True,text=True).stdout.strip()
    cases = D.build_cases(binary, repo, n)
    print(f"rev {rev}  n={len(cases)}  comparing {lo} vs {hi}\n")
    lost = kept = same = 0
    for q, name, fp in cases:
        rl, tl = found(binary, repo, q, name, fp, lo)
        rh, th = found(binary, repo, q, name, fp, hi)
        if rl == rh:
            same += 1; continue
        if rh is not None and rl is None:
            lost += 1
            print(f"  LOST at {lo}: {name}  ({fp})  rank@{hi}={rh}  top@{lo}={tl}")
        elif rl is not None and rh is None:
            kept += 1
            print(f"  GAINED at {lo}: {name}  rank@{lo}={rl}")
        else:
            print(f"  RANK MOVED: {name}  {lo}->rank {rl}   {hi}->rank {rh}")
    print(f"\nsame={same}  lost_at_{lo}={lost}  gained_at_{lo}={kept}")
    print(f"FALSIFIER: a budget that loses ANY target fails. {lo} " +
          ("FAILS" if lost else "passes") + f" against {hi}.")
