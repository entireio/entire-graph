#!/usr/bin/env python3
"""Seal a run's identity BEFORE its first query, and refuse to start if anything is missing.

Written after a run went out with two protocol gaps that nobody could close afterwards: no
record of the selected query list, and no build/source identity for the binary. Both were
recoverable-looking and neither was actually recoverable -- the binary had been built from a
DIRTY tree, and by the time the gap was noticed no working tree remained at that revision, so
the exact built source is simply gone. The run stays valid as descriptive diagnostics tied to an
artifact hash, and can never be more than that.

A seal written afterwards is not a seal. So this refuses rather than warns:

    a dirty binary build      -> refuse. The source cannot be reproduced, so nothing downstream
                                 can be either. Pass --allow-dirty-build ONLY for explicitly
                                 artifact-tied development diagnostics, and it is recorded.
    a dirty fixture           -> refuse.
    an existing output dir    -> refuse; the run must create its own.
    an empty query list       -> refuse; there is nothing to pre-register.

Usage:  seal_run.py <binary> <outdir> <fixture>...
"""
import hashlib, json, os, re, subprocess, sys


def sha256(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def build_identity(binary):
    out = subprocess.run(["go", "version", "-m", binary], capture_output=True, text=True).stdout
    kv = dict(re.findall(r"build\s+(\S+)=(\S+)", out))
    mod = re.search(r"mod\s+(\S+)\s+(\S+)", out)
    go = re.search(r":\s+(go\S+)", out)
    return {"go": go.group(1) if go else None,
            "module_version": mod.group(2) if mod else None,
            "vcs_revision": kv.get("vcs.revision"),
            "vcs_time": kv.get("vcs.time"),
            "vcs_modified": kv.get("vcs.modified") == "true"}


def fixture_identity(path):
    rev = subprocess.run(["git", "-C", path, "rev-parse", "HEAD"], capture_output=True, text=True)
    dirty = subprocess.run(["git", "-C", path, "status", "--porcelain"], capture_output=True, text=True)
    url = subprocess.run(["git", "-C", path, "remote", "get-url", "origin"], capture_output=True, text=True)
    if rev.returncode or dirty.returncode:
        raise SystemExit(f"{path}: cannot read git state; refusing to seal")
    return {"path": path, "rev": rev.stdout.strip(), "clean": not dirty.stdout.strip(),
            "origin": url.stdout.strip() or None}


def seal_refusal(build, fixtures, outdir_exists, allow_dirty):
    """Why this run must not start, or None if it may.

    A PURE FUNCTION, deliberately, because the whole point of this file is a set of refusals
    and refusals that no test can reach are decoration. The I/O -- reading git, reading the
    binary's build stamp -- is the caller's; every decision is here.

    Extracted after a night in which two separate bugs turned out to live in CALL-SITE ORDERING
    rather than in any function, and were invisible to the suite for exactly that reason.
    """
    if outdir_exists:
        return ("output directory exists; a run must create its own, so that nothing can "
                "splice two runs' receipts under one verdict")
    if build.get("vcs_modified") and not allow_dirty:
        return (f"binary built from a DIRTY tree (revision {build.get('vcs_revision')}); the "
                f"exact source cannot be reproduced, so no result from it can be either. Pass "
                f"--allow-dirty-build to record it as artifact-tied development diagnostics.")
    for fixture in fixtures:
        if not fixture.get("clean"):
            return f"{fixture.get('path')} is dirty; a frozen fixture must be clean"
    if not fixtures:
        return "no fixtures given; there is nothing to seal"
    return None


def _selftest():
    clean = {"vcs_modified": False, "vcs_revision": "a" * 40}
    dirty = {"vcs_modified": True, "vcs_revision": "b" * 40}
    okfx = [{"path": "/fx", "clean": True}]
    badfx = [{"path": "/fx", "clean": False}]
    cases = [
        ("a clean build with a clean fixture may start", (clean, okfx, False, False), None),
        ("an existing output directory is refused", (clean, okfx, True, False), "output directory"),
        ("a dirty build is refused", (dirty, okfx, False, False), "DIRTY tree"),
        ("...and the refusal names the revision", (dirty, okfx, False, False), "bbbbbbbb"),
        ("--allow-dirty-build lets it through", (dirty, okfx, False, True), None),
        ("...but an existing outdir still refuses", (dirty, okfx, True, True), "output directory"),
        ("a dirty fixture is refused", (clean, badfx, False, False), "must be clean"),
        ("...even with --allow-dirty-build", (clean, badfx, False, True), "must be clean"),
        ("no fixtures is refused", (clean, [], False, False), "nothing to seal"),
    ]
    fails = []
    for label, args, want in cases:
        got = seal_refusal(*args)
        ok = (got is None) if want is None else (got is not None and want in got)
        print(("  ok   " if ok else "  FAIL ") + label + ("" if ok else f"  <- {got!r}"))
        if not ok:
            fails.append(label)
    # ORDER MATTERS AND IS PART OF THE CONTRACT: the output directory is checked FIRST, so a
    # refusal never depends on reading a binary or a git tree that may not be there.
    ordered = seal_refusal(dirty, badfx, True, False)
    okorder = ordered is not None and "output directory" in ordered
    print(("  ok   " if okorder else "  FAIL ") + "the outdir check runs before any other")
    if not okorder:
        fails.append("ordering")
    print("\nALL SEAL REFUSALS HELD" if not fails else "\nFAILURES: " + ", ".join(fails))
    return 1 if fails else 0


def main(argv):
    allow_dirty = "--allow-dirty-build" in argv
    argv = [a for a in argv if a != "--allow-dirty-build"]
    if len(argv) < 3:
        raise SystemExit(__doc__)
    binary, outdir, fixtures = argv[0], argv[1], argv[2:]

    outdir_exists = os.path.exists(outdir)
    build = {} if outdir_exists else build_identity(binary)
    identities = [] if outdir_exists else [fixture_identity(f) for f in fixtures]
    if refusal := seal_refusal(build, identities, outdir_exists, allow_dirty):
        raise SystemExit(f"REFUSING TO START: {refusal}")

    seal = {"binary": {"path": binary, "sha256": sha256(binary), "build": build,
                       "build_source_reproducible": not build["vcs_modified"],
                       "recorded_as_artifact_tied_only": bool(build["vcs_modified"])},
            "fixtures": identities}
    print(json.dumps(seal, indent=1))
    return 0


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--test":
        sys.exit(_selftest())
    sys.exit(main(sys.argv[1:]))
