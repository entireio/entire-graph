#!/usr/bin/env python3
"""Reject generated or environment-bound Graph Advantage evidence artifacts."""

from __future__ import annotations

import argparse
import pathlib
import re
import subprocess
import sys


BASELINE = "3a2a715fad1948e83dc7ebe0d307377ba29e065a"
SCOPE = pathlib.PurePosixPath("docs/implementation/graph-advantage")
PROTECTED_LOCAL_UNTRACKED = {
    pathlib.PurePosixPath("docs/implementation/graph-advantage/evidence/check-25887f69-linux-full/review/local-prep-dir.txt"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/evidence/check-6f23da0a-linux-full/source.tar.gz"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/p1-corpus-20260905/frozen-baseline-initial.json"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/p1-corpus-20260905/frozen-baseline-pre-counts.json"),
}
SAFE_EXPOSURE_FIXTURE_FILES = {
    pathlib.PurePosixPath("docs/implementation/graph-advantage/cleanup/test_correctness_archive_mappings.py"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/cleanup/test_log_observations.py"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/cleanup/test_privacy_redactions.py"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/cleanup/test_validate_archive_operational.py"),
    pathlib.PurePosixPath("docs/implementation/graph-advantage/evidence/canonical-v1/tools/test_canonical.py"),
}

FORBIDDEN_NAMES = {
    "transport.json": "cloud transport response",
    "transport.stderr.txt": "cloud transport stderr",
    "transport.exit.txt": "cloud transport exit",
    "vm-terminal.json": "full cloud VM record",
}
FORBIDDEN_SUFFIXES = {
    ".bin": "binary status artifact",
    ".log": "raw log",
    ".pprof": "binary profile",
    ".tar.gz": "compressed artifact bundle",
    ".gz": "compressed artifact",
}
EXPOSURE_PATTERNS = {
    "absolute macOS home path": re.compile(rb"/" rb"Users/[^/\s\"']+"),
    "absolute Linux home path": re.compile(rb"/" rb"home/[^/\s\"']+"),
    "Azure subscription resource ID": re.compile(
        rb"/subscriptions/[0-9a-fA-F-]{32,36}(?:/|\b)"
    ),
    "Azure blob endpoint": re.compile(
        rb"https?://[A-Za-z0-9.-]+\.blob\.core\.windows\.net"
    ),
}


def added_paths(repo: pathlib.Path, baseline: str) -> list[pathlib.PurePosixPath]:
    tracked = subprocess.check_output(
        [
            "git",
            "diff",
            "--name-only",
            "--diff-filter=A",
            baseline,
            "--",
            str(SCOPE),
        ],
        cwd=repo,
        text=True,
    )
    staged = subprocess.check_output(
        ["git", "diff", "--cached", "--name-only", "--diff-filter=A", "--", str(SCOPE)],
        cwd=repo,
        text=True,
    )
    untracked = subprocess.check_output(
        ["git", "ls-files", "--others", "--exclude-standard", "--", str(SCOPE)],
        cwd=repo,
        text=True,
    )
    tracked_paths = {
        pathlib.PurePosixPath(line)
        for output in (tracked, staged)
        for line in output.splitlines()
        if line
    }
    untracked_paths = {
        pathlib.PurePosixPath(line)
        for line in untracked.splitlines()
        if line
    }
    return sorted(tracked_paths | (untracked_paths - PROTECTED_LOCAL_UNTRACKED))


def path_violation(path: pathlib.PurePosixPath) -> str | None:
    if path.name in FORBIDDEN_NAMES:
        return FORBIDDEN_NAMES[path.name]
    name = path.name.lower()
    for suffix, reason in FORBIDDEN_SUFFIXES.items():
        if name.endswith(suffix):
            return reason
    if "raw" in path.parts or "raw-evidence" in path.parts:
        return "raw evidence tree"
    return None


def exposure_violations(path: pathlib.PurePosixPath, data: bytes) -> list[tuple[str, int]]:
    if b"\0" in data[:8192]:
        return [("unsupported binary content", 1)]
    findings = []
    for reason, pattern in EXPOSURE_PATTERNS.items():
        for match in pattern.finditer(data):
            findings.append((reason, data.count(b"\n", 0, match.start()) + 1))
    return findings


def check(repo: pathlib.Path, paths: list[pathlib.PurePosixPath]) -> list[str]:
    violations = []
    for path in sorted(paths):
        reason = path_violation(path)
        if reason:
            violations.append(f"{path}: {reason}")
            continue
        absolute = repo / path
        if not absolute.is_file():
            violations.append(f"{path}: added artifact is missing or not a regular file")
            continue
        if path in SAFE_EXPOSURE_FIXTURE_FILES:
            continue
        for exposure, line in exposure_violations(path, absolute.read_bytes()):
            violations.append(f"{path}:{line}: {exposure}")
    return violations


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    parser.add_argument("--baseline", default=BASELINE)
    args = parser.parse_args()
    repo = args.repo.resolve()
    violations = check(repo, added_paths(repo, args.baseline))
    if violations:
        print("public artifact guard rejected branch-added outputs:", file=sys.stderr)
        for violation in violations:
            print("  " + violation, file=sys.stderr)
        return 1
    print("public artifact guard passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
