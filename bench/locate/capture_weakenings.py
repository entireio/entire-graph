#!/usr/bin/env python3
"""Capture RAW command output for every weakening claim, so a reviewer reads bytes not prose.

Written because a review recorded my results as "peer-reported rather than independently
verified": I had been retyping `ok`/`FAIL` lines into messages. A transcription is not evidence
-- the reviewer cannot tell a real run from a confident summary of one, and correctly declines
to treat them the same.

For each mutation in mutation_sweep.py this runs the suite twice -- unmutated and mutated --
and writes both stdout/stderr streams verbatim, with exit codes, to files under an output
directory, plus a manifest of sha256 sums. Nothing is summarised and nothing is reformatted.

    python3 capture_weakenings.py <outdir> [--only SUBSTRING]

The output directory must not exist: a capture that overwrites a previous one cannot be trusted
to describe either.
"""
import hashlib, json, os, subprocess, sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)


def sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run(target, env):
    p = subprocess.run([sys.executable, target, "--test"], capture_output=True, timeout=3600,
                       env=env, cwd=os.path.dirname(HERE) or ".")
    return p.returncode, p.stdout, p.stderr


def main(argv):
    if not argv:
        raise SystemExit(__doc__)
    outdir, only = argv[0], None
    if "--only" in argv:
        only = argv[argv.index("--only") + 1]
    if os.path.exists(outdir):
        raise SystemExit(f"{outdir} exists; a capture must create its own directory, or it "
                         f"cannot be trusted to describe either run")
    os.makedirs(outdir)

    import mutation_sweep as MS
    env = dict(os.environ)
    manifest = {"schema": 1, "mutations": []}

    for target, label, anchor, replacement in MS.MUT:
        if only and only not in label:
            continue
        slug = "".join(c if c.isalnum() else "_" for c in label)[:70]
        src = open(target, encoding="utf8").read()
        entry = {"label": label, "target": os.path.basename(target),
                 "anchor_present": anchor in src}
        if not entry["anchor_present"]:
            # A stale anchor is recorded, never silently skipped -- it means the sweep is no
            # longer testing this guard, which is a different fact from the guard being weak.
            entry["status"] = "STALE ANCHOR — nothing was mutated"
            manifest["mutations"].append(entry)
            continue
        for phase, text in (("before", src), ("mutated", src.replace(anchor, replacement, 1))):
            open(target, "w", encoding="utf8").write(text)
            try:
                rc, out, err = run(target, env)
            finally:
                open(target, "w", encoding="utf8").write(src)
            base = os.path.join(outdir, f"{slug}.{phase}")
            with open(base + ".stdout", "wb") as fh:
                fh.write(out)
            with open(base + ".stderr", "wb") as fh:
                fh.write(err)
            entry[phase] = {"exit": rc,
                            "stdout": os.path.basename(base) + ".stdout",
                            "stdout_sha256": sha256(base + ".stdout"),
                            "stderr_sha256": sha256(base + ".stderr")}
        entry["status"] = ("CAUGHT" if entry["mutated"]["exit"] != entry["before"]["exit"]
                           else "NOT CAUGHT — the mutation changed no outcome")
        manifest["mutations"].append(entry)
        print(f"  {entry['status']:<44} {label}")

    with open(os.path.join(outdir, "MANIFEST.json"), "w") as fh:
        json.dump(manifest, fh, indent=1)
    caught = sum(1 for m in manifest["mutations"] if m.get("status") == "CAUGHT")
    print(f"\n{caught}/{len(manifest['mutations'])} mutations changed the suite's exit code.")
    print(f"Raw streams and MANIFEST.json in {outdir}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
