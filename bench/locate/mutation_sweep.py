"""MUTATION SWEEP. Break each guard on purpose; a suite that stays green has an unfailable
test. Any GREEN row below is a guard nothing is actually holding."""
import subprocess, os, sys

H = "os.path.join(os.path.dirname(os.path.abspath(__file__)), 'head_to_head.py')"
R = "os.path.join(os.path.dirname(os.path.abspath(__file__)), 'replay_qualify.py')"

MUT = [
  # (file, label, find, replace)
  (H, "scorer: drop the span check on grep arms",
   "        if lo and not (lo <= line <= hi):\n            continue", "        if False:\n            continue"),
  (H, "scorer: credit a declaration from the ranked header",
   "                if current_is_target:\n                    locator = True",
   "                if current_is_target:\n                    locator = True\n                    if d.search(ln): declaration = True"),
  (H, "scorer: attribute body text with no header above it",
   "            if current_is_target and d.search(ln):", "            if d.search(ln):"),
  (H, "phrase: set both columns from one condition",
   "    loc, dec = score(ev, repo, name, target_file, lo, hi)\n    meta",
   "    loc, dec = score(ev, repo, name, target_file, lo, hi)\n    loc = dec = (loc or dec)\n    meta"),
  (H, "phrase: hand it the target name (oracle input)",
   '    t = re.sub(re.escape(name), "\\x00", text, flags=re.I)', '    t = text'),
  (H, "cost: count characters, not bytes",
   '    cost += len(ev)', '    cost += len(ev.decode("utf8","replace"))'),
  (H, "arms: let a killed process score as a miss",
   "    if p.returncode < 0 or p.returncode > 1:\n        return 0, False, False, False, b\"\", meta\n    cost = len(p.stdout)",
   "    if p.returncode > 1:\n        return 0, False, False, False, b\"\", meta\n    cost = len(p.stdout)"),
  (R, "replay: accept any truthy tree",
   '    return bool(HEX40.match(str(commit or "")) and HEX40.match(str(tree or "")))',
   '    return bool(HEX40.match(str(commit or "")) and tree)'),
  (R, "replay: attribute every command",
   "def output_is_attributable(command):", "def output_is_attributable(command):\n    return True"),
  (R, "replay: drop the graph-marker requirement",
   "    if good >= 2 and marked:", "    if good >= 2:"),
  (R, "replay: mask a comment to end of string",
   '            nl = cmd.find("\\n", k)', '            nl = -1 if True else cmd.find("\\n", k)'),
  (R, "replay: accept a file_path+start_line pointer",
   '        return any(r0.get(k) not in (None, "", [], {}) for k in',
   '        return True or any(r0.get(k) not in (None, "", [], {}) for k in'),
  (R, "replay: promote identity to full-execution",
   '                tiers["source-identity-candidate"] += 1', '                tiers["full-execution"] += 1'),
  (R, "replay: case-sensitive truncation again",
   '    low = body.lower()', '    low = body'),
]

def run(f):
    return subprocess.run([sys.executable, f, "--test"], capture_output=True, text=True, timeout=1800)

base = {f: run(f).returncode for f in (H, R)}
if any(v != 0 for v in base.values()):
    sys.exit("baseline is not green; fix that first")

print(f"{'guard mutated':<52} {'suite':<8} verdict")
print("-" * 78)
unfailable = []
for f, label, a, b in MUT:
    src = open(f).read()
    if a not in src:
        print(f"{label:<52} {'--':<8} ANCHOR MISSING"); unfailable.append(label); continue
    open(f, "w").write(src.replace(a, b, 1))
    try:
        rc = run(f).returncode
    finally:
        open(f, "w").write(src)
    ok = rc != 0
    print(f"{label:<52} {'RED' if ok else 'green':<8} {'caught' if ok else 'UNFAILABLE <<<'}")
    if not ok: unfailable.append(label)
print("-" * 78)
print(f"{len(MUT)-len(unfailable)}/{len(MUT)} guards are actually held by a test")
if unfailable:
    print("NOT HELD:"); [print("  -", u) for u in unfailable]
