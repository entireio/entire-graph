#!/usr/bin/env python3
"""A PREFLIGHT. It does not seal a run, and must never be described as doing so.

Peer review's point, and it is correct: a tool that checks a path and then hands control to
another process has bound nothing. The directory it approved is created by someone else, the
selected queries are chosen by someone else, and the manifest is written by someone else. THE
EVALUATOR OWNS THE SEAL -- head_to_head.py creates the output directory, writes SEAL.json after
selection and before the first arm, and carries the seal's hash in its final manifest.

What this is good for: checking a binary and a set of fixtures BEFORE committing to a run, so a
dirty build or a dirty fixture is discovered in a second rather than after an hour of queries.
Its output is a report, not a seal.

Original docstring follows.

Seal a run's identity BEFORE its first query, and refuse to start if anything is missing.

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


def _git(path, *args):
    subprocess.run(["git", "-C", path] + list(args), capture_output=True, check=False)


def _temp_repo(dirty=False):
    """A real git repository, optionally with an uncommitted change."""
    import tempfile
    d = tempfile.mkdtemp(prefix="seal-fx-")
    _git(d, "init", "-q")
    with open(os.path.join(d, "a.txt"), "w") as fh:
        fh.write("one\n")
    _git(d, "add", "-A")
    _git(d, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x")
    if dirty:
        with open(os.path.join(d, "a.txt"), "w") as fh:
            fh.write("two\n")
    return d


def _temp_go_binary(dirty):
    """A REAL Go binary carrying real vcs stamps, built clean or from a dirty tree.

    Peer review's point: testing the extracted predicate does not show the CLI wires it up.
    So these drive the actual entrypoint against actual build stamps.
    """
    import tempfile
    d = tempfile.mkdtemp(prefix="seal-bin-")
    with open(os.path.join(d, "go.mod"), "w") as fh:
        fh.write("module sealprobe\n\ngo 1.24\n")
    with open(os.path.join(d, "main.go"), "w") as fh:
        fh.write("package main\n\nfunc main() {}\n")
    _git(d, "init", "-q")
    _git(d, "add", "-A")
    _git(d, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x")
    if dirty:
        with open(os.path.join(d, "main.go"), "w") as fh:
            fh.write("package main\n\nfunc main() { _ = 1 }\n")
    out = os.path.join(d, "probe")
    build = subprocess.run(["go", "build", "-o", out, "."], cwd=d, capture_output=True)
    return out if build.returncode == 0 else None


def _cli(*args):
    """Run seal_run.py as the production entrypoint."""
    return subprocess.run([sys.executable, os.path.abspath(__file__)] + list(args),
                          capture_output=True, text=True, timeout=600)


def _e2e(ck):
    """The PRODUCTION PATH, not the predicate: real repos, real build stamps, real exit codes."""
    import json as _j, tempfile
    clean_fx, dirty_fx = _temp_repo(), _temp_repo(dirty=True)
    fresh = lambda: os.path.join(tempfile.mkdtemp(prefix="seal-out-"), "new")

    for label, dirty, expect_refusal in (("a clean build is sealed", False, False),
                                         ("a dirty build is refused", True, True)):
        binary = _temp_go_binary(dirty)
        if binary is None:
            print(f"  skip  {label} (no working Go toolchain)")
            continue
        r = _cli(binary, fresh(), clean_fx)
        refused = r.returncode != 0 and "REFUSING TO START" in (r.stdout + r.stderr)
        ck(refused is expect_refusal, "CLI: " + label, f"rc={r.returncode} {r.stderr.strip()[:70]}")
        if not expect_refusal and not refused:
            seal = _j.loads(r.stdout)
            ck(seal["binary"]["build_source_reproducible"] is True,
               "CLI: a clean seal records reproducible source")
            ck(seal["fixtures"][0]["rev"] and seal["fixtures"][0]["clean"],
               "CLI: the seal carries the fixture's real revision")
        if expect_refusal:
            r2 = _cli("--allow-dirty-build", binary, fresh(), clean_fx)
            ok = r2.returncode == 0 and _j.loads(r2.stdout)["binary"]["recorded_as_artifact_tied_only"]
            ck(bool(ok), "CLI: --allow-dirty-build seals and records the limitation")

    binary = _temp_go_binary(False)
    if binary:
        r = _cli(binary, fresh(), dirty_fx)
        ck("must be clean" in (r.stdout + r.stderr), "CLI: a dirty fixture is refused")
        r = _cli("--allow-dirty-build", binary, fresh(), dirty_fx)
        ck("must be clean" in (r.stdout + r.stderr),
           "CLI: ...and --allow-dirty-build does not excuse it")
        out = fresh(); os.makedirs(out)
        before = sorted(os.listdir(out))
        r = _cli(binary, out, clean_fx)
        ck("output directory" in (r.stdout + r.stderr), "CLI: an existing directory is refused")
        ck(sorted(os.listdir(out)) == before, "CLI: ...and the refusal wrote nothing into it")


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
    _e2e(lambda cond, label, detail="": (
        print(("  ok   " if cond else "  FAIL ") + label + (("  <- " + detail) if detail and not cond else "")),
        None if cond else fails.append(label)))
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
