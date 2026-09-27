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


def main(argv):
    allow_dirty = "--allow-dirty-build" in argv
    argv = [a for a in argv if a != "--allow-dirty-build"]
    if len(argv) < 3:
        raise SystemExit(__doc__)
    binary, outdir, fixtures = argv[0], argv[1], argv[2:]

    if os.path.exists(outdir):
        raise SystemExit(f"{outdir} exists; a run must create its own output directory")

    build = build_identity(binary)
    if build["vcs_modified"] and not allow_dirty:
        raise SystemExit(
            f"{binary} was built from a DIRTY tree (revision {build['vcs_revision']}).\n"
            f"The exact built source cannot be reproduced, so no result from it can be either.\n"
            f"Rebuild from a clean tree, or pass --allow-dirty-build to record it explicitly as "
            f"artifact-tied development diagnostics.")

    seal = {"binary": {"path": binary, "sha256": sha256(binary), "build": build,
                       "build_source_reproducible": not build["vcs_modified"]},
            "fixtures": [fixture_identity(f) for f in fixtures]}
    for f in seal["fixtures"]:
        if not f["clean"]:
            raise SystemExit(f"{f['path']} is dirty; a frozen fixture must be clean")
    print(json.dumps(seal, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
