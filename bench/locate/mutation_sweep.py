"""MUTATION SWEEP. Break each guard on purpose; a suite that stays green has an unfailable
test. Any GREEN row below is a guard nothing is actually holding."""
import subprocess, os, sys

_HERE = os.path.dirname(os.path.abspath(__file__))
H = os.path.join(_HERE, "head_to_head.py")
R = os.path.join(_HERE, "replay_qualify.py")

MUT = [
  # (file, label, find, replace)
  (H, "scorer: drop the span check on grep arms",
   "        if lo and not (lo <= line <= hi):\n            continue", "        if False:\n            continue"),
  (H, "scorer: credit a declaration from the ranked header",
   "                if current_is_target:\n                    locator = True",
   "                if current_is_target:\n                    locator = True\n                    if d.search(ln): declaration = True"),
  (H, "scorer: attribute body text with no header above it",
   "            if current_is_target and shows_declaration(ln):", "            if shows_declaration(ln):"),
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
  (H, "scorer: credit a declaration from a regex, not the source",
   "    def shows_declaration(text):\n        return bool(decl_text) and decl_text in text",
   "    def shows_declaration(text):\n        return bool(d.search(text))"),
  (H, "scorer: read the declaration from the wrong span",
   "    for num in range(max(1, lo), min(hi, len(lines)) + 1):",
   "    for num in range(1, len(lines) + 1):"),
  (H, "receipts: write only admitted attempts",
   '    if outdir:\n        write_receipts(outdir, cid, q, name, fp, lo, hi,',
   '    if outdir and all(a[4] for a in arms if a[0] != "oracle"):\n        write_receipts(outdir, cid, q, name, fp, lo, hi,'),
  (H, "receipts: let an oracle failure exclude the case",
   '    return all(a[4] for a in arms if a[0] != "oracle")\n\n\ndef finalize_manifest',
   '    return all(a[4] for a in arms)\n\n\ndef finalize_manifest'),
  (H, "manifest: never stamp a verdict",
   "    manifest[\"run_status\"] = status", "    manifest[\"run_status\"] = \"PROVISIONAL\""),
  # DROPPED, and the reason is a property worth recording rather than a gap: removing "1"
  # from the stdout-redirect rule changes nothing, because the fail-closed unmodelled-fd
  # branch below it already refuses fd 1. A design where deleting a specific rule cannot open
  # a hole is working as intended, and a sweep that reported this as UNHELD would be
  # manufacturing work.
  (R, "replay: let the splitter break 2>&1",
   "    masked = _neutralise_redirect_amps(masked)\n", "\n"),
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
