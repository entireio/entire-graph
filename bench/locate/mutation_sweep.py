"""MUTATION SWEEP. Break each guard on purpose; a suite that stays green has an unfailable
test. Any GREEN row below is a guard nothing is actually holding."""
import subprocess, os, sys

_HERE = os.path.dirname(os.path.abspath(__file__))
H = os.path.join(_HERE, "head_to_head.py")
R = os.path.join(_HERE, "replay_qualify.py")

D = os.path.join(_HERE, "doccomment_bench.py")
B = os.path.join(_HERE, "budget_falsifier.py")
Z = os.path.join(_HERE, "seal_run.py")

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
  (H, "receipts: decode and cap stderr again",
   '            with open(base + ".stderr", "wb") as fh:\n                fh.write(meta["stderr_bytes"])',
   '            with open(base + ".stderr", "w") as fh:\n                fh.write(meta["stderr_bytes"].decode("utf8","replace")[:4000])'),
  (H, "evaluator: print an unsealed table again",
   "    if not outdir and not smoke:", "    if False:"),
  (Z, "preflight: accept an unknown build as clean",
   '    if not build.get("known", False):', "    if False:"),
  (H, "seal: record a false arm-entry observation but still say VALID",
   '    if seal_observed_at_arm_entry is True:', "    if True:"),
  (H, "identity: check the build only after selection",
   '    if not _early["known"]:', "    if False:"),
  (H, "tests: stop cleaning up temp directories",
   "    for d in _TEMPDIRS:\n        shutil.rmtree(d, ignore_errors=True)\n        if not os.path.exists(d):",
   "    for d in []:\n        shutil.rmtree(d, ignore_errors=True)\n        if not os.path.exists(d):"),
  (H, "tests: report attempted removals as completed ones",
   "        if not os.path.exists(d):\n            gone += 1", "        if True:\n            gone += 1"),
  (H, "output: drop the budget-asymmetry caveat",
   'print(f"\\n  !! BYTES ARE NOT COMPARABLE ACROSS ARMS', 'print(f"\\n  !! bytes'),
  (H, "build: a garbled vcs.modified reads as clean",
   '    if kv["vcs.modified"] not in ("true", "false"):', '    if kv["vcs.modified"] in ("",):'),
  (H, "oracle: tally below the admission gate again",
   "    admitted = record_attempt(outdir, cid, q, name, fp, lo, hi, arms)\n    by = {a[0]: a for a in arms}\n    o = by[\"oracle\"]\n    tally_oracle(ostate, o[4], o[1], o[2], o[3])\n    return admitted",
   "    admitted = record_attempt(outdir, cid, q, name, fp, lo, hi, arms)\n    if not admitted:\n        return admitted\n    by = {a[0]: a for a in arms}\n    o = by[\"oracle\"]\n    tally_oracle(ostate, o[4], o[1], o[2], o[3])\n    return admitted"),
  (H, "receipts: charge stderr to the arm",
   '"cost_bytes": cost,', '"cost_bytes": cost + len(meta.get("stderr_bytes") or b""),'),
  (H, "seal: treat an unknown build as clean",
   '    if not _early["known"]:', '    if False:'),
  (H, "seal: stop binding the seal into the manifest",
   '"schema": 2, "seal_sha256": seal_sha,', '"schema": 2, "seal_sha256": None,'),
  (H, "arms: drop the early-return diagnostic note",
   '"note": meta.get("note"),', '"note": None,'),
  (Z, "sealer: let a dirty build through",
   'if build.get("vcs_modified") and not allow_dirty:', 'if False and not allow_dirty:'),
  (Z, "sealer: accept an existing output directory",
   "    if outdir_exists:\n        return (", "    if False:\n        return ("),
  (Z, "sealer: accept a dirty fixture",
   'if not fixture.get("clean"):', 'if False:'),
  (H, "scorer: test only the range start again",
   "                current_is_target = ok and (not lo or (line <= hi and end >= lo))",
   "                current_is_target = ok and (not lo or lo <= line <= hi)"),
  (D, "siblings: search the suffix instead of the symbol token",
   "    token = rest[0].strip(\"(),;:\")\n    return token == name or token.endswith(\".\" + name)",
   "    return bool(re.search(r'\\b' + re.escape(name) + r'\\b', ' '.join(rest)))"),
  (B, "siblings: budget_falsifier stops sharing the implementation",
   "RANK, ranked_hit = D.RANK, D.ranked_hit",
   "RANK, ranked_hit = D.RANK, (lambda line, name, tf, repo: True)"),
  (D, "siblings: stop consuming the range end before the symbol",
   "RANK = re.compile(r'^\\s*(\\d+)\\.\\s+(\\S+?):(\\d+)(?:-(\\d+))?')",
   "RANK = re.compile(r'^\\s*(\\d+)\\.\\s+(\\S+?):(\\d+)')"),
  (D, "siblings: drop the target-file check again",
   "    if os.path.normpath(path) != os.path.normpath(target_file):\n        return False",
   "    if False:\n        return False"),
  (H, "arms: drop the rg option terminator",
   '"-g", "!node_modules", "--", phrase, repo]', '"-g", "!node_modules", phrase, repo]'),
  (H, "oracle: let a failed run count as a miss",
   '    if ok:\n        state["valid"] += 1', '    if True:\n        state["valid"] += 1'),
  (H, "scorer: credit a declaration from a regex, not the source",
   "        return bool(decl_text) and text.strip() == decl_text",
   "        return bool(d.search(text))"),
  (H, "scorer: accept the source line with anything appended",
   "        return bool(decl_text) and text.strip() == decl_text",
   "        return bool(decl_text) and decl_text in text"),
  (H, "scorer: read the declaration from the wrong span",
   "    for num in range(max(1, lo), min(hi, len(lines)) + 1):",
   "    for num in range(1, len(lines) + 1):"),
  (H, "receipts: write only admitted attempts",
   '    if outdir:\n        write_receipts(outdir, cid, q, name, fp, lo, hi,',
   '    if outdir and all(a[4] for a in arms if a[0] != "oracle"):\n        write_receipts(outdir, cid, q, name, fp, lo, hi,'),
  (H, "receipts: let an oracle failure exclude the case",
   '    if outdir:\n        write_receipts(outdir, cid, q, name, fp, lo, hi,\n                       all(a[4] for a in arms if a[0] != "oracle"), arms)\n    return all(a[4] for a in arms if a[0] != "oracle")',
   '    if outdir:\n        write_receipts(outdir, cid, q, name, fp, lo, hi,\n                       all(a[4] for a in arms if a[0] != "oracle"), arms)\n    return all(a[4] for a in arms)'),
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
    # doccomment_bench and budget_falsifier carry no --test of their own; their shared hit
    # predicate is guarded from head_to_head's suite, so mutations to them are checked there.
    runner = f if f in (H, R, Z) else H
    return subprocess.run([sys.executable, runner, "--test"], capture_output=True, text=True, timeout=1800)

base = {f: run(f).returncode for f in (H, R, Z)}
if any(v != 0 for v in base.values()):
    sys.exit("baseline is not green; fix that first")

print(f"{'guard mutated':<52} {'suite':<8} verdict")
print("-" * 78)
unfailable, stale = [], []
for f, label, a, b in MUT:
    src = open(f).read()
    if a not in src:
        # A MISSING ANCHOR IS NOT A FINDING, it is the sweep silently no longer testing this
        # guard. It has happened twice, both times because a predicate was renamed, and both
        # times it presented as "guard not held" -- which sends you looking for a missing test
        # that already exists. Different diagnosis, reported differently, and fatal.
        print(f"{label:<52} {'--':<8} ANCHOR MISSING -- sweep is stale, not the code")
        stale.append(label); continue
    open(f, "w").write(src.replace(a, b, 1))
    try:
        rc = run(f).returncode
    finally:
        open(f, "w").write(src)
    ok = rc != 0
    print(f"{label:<52} {'RED' if ok else 'green':<8} {'caught' if ok else 'UNFAILABLE <<<'}")
    if not ok: unfailable.append(label)
print("-" * 78)
print(f"{len(MUT)-len(unfailable)-len(stale)}/{len(MUT)} guards are actually held by a test")
if unfailable:
    print("NOT HELD (a real gap -- the code can break and nothing fails):")
    [print("  -", u) for u in unfailable]
if stale:
    print("STALE ANCHORS (the sweep is broken, fix these before believing any row above):")
    [print("  -", u) for u in stale]
sys.exit(1 if (unfailable or stale) else 0)
