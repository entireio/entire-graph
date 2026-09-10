#!/usr/bin/env python3
"""Extract public-safe facts from the residual VM collector and statusline logs."""

from __future__ import annotations

from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import subprocess


SOURCE_PATHS = (
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-12574522-r2/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-12574522/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-25887f69/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-450bede9/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-8689fc3d/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-90ac3e16/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-950567a3/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/diagnostic-dispatch-effa358f/controller/raw/collector.log",
    "docs/implementation/graph-advantage/evidence/statusline-linux-actual-task-6f23da0a-r1/raw/mise-test-statusline.raw.log",
    "docs/implementation/graph-advantage/evidence/statusline-linux-trace-6f23da0a/raw/clean.stdout.txt",
    "docs/implementation/graph-advantage/evidence/statusline-linux-trace-6f23da0a/raw/clean.trace.txt",
    "docs/implementation/graph-advantage/evidence/statusline-linux-trace-6f23da0a/raw/inherited.stdout.txt",
    "docs/implementation/graph-advantage/evidence/statusline-linux-trace-6f23da0a/raw/inherited.trace.txt",
)
ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def git(*args: str, cwd: Path) -> bytes:
    return subprocess.check_output(["git", *args], cwd=cwd)


def normalize(path: str, data: bytes) -> tuple[str, dict[str, object]]:
    text = data.decode("utf-8", "replace")
    basename = Path(path).name
    if basename == "collector.log":
        stripped = text.strip()
        if stripped == "request timed out":
            state = "request-timeout"
        elif stripped == "post-fix collector pins are placeholders; refuse execution":
            state = "configuration-placeholder-refusal"
        else:
            raise ValueError(f"unreviewed collector message: {path}")
        return "collector-terminal-message", {"state": state, "line_count": len(text.splitlines())}

    if basename == "mise-test-statusline.raw.log":
        plain = ANSI.sub("", text)
        summaries = re.findall(r"(\d+) passed, (\d+) failed", plain)
        if len(summaries) != 1:
            raise ValueError("statusline test summary not found exactly once")
        duration = re.findall(r"in ([0-9.]+)s", plain)
        return "statusline-test-failure", {
            "passed": int(summaries[0][0]),
            "failed": int(summaries[0][1]),
            "duration_seconds": float(duration[-1]) if duration else None,
            "terminal_error": "ERROR" in plain,
            "line_count": len(text.splitlines()),
            "ansi_sequence_count": len(ANSI.findall(text)),
        }

    if basename.endswith(".stdout.txt"):
        plain = ANSI.sub("", text)
        return "statusline-render", {
            "environment_case": "inherited" if basename.startswith("inherited") else "clean",
            "rendered_sha256": sha256(plain.encode()),
            "rendered_bytes": len(plain.encode()),
            "line_count": len(text.splitlines()),
            "ansi_sequence_count": len(ANSI.findall(text)),
        }

    if basename.endswith(".trace.txt"):
        commands: Counter[str] = Counter()
        for line in text.splitlines():
            if not line.startswith("+ "):
                continue
            token = line[2:].strip().split(maxsplit=1)[0] if line[2:].strip() else ""
            command = Path(token).name
            if command in {"[", "awk", "basename", "dirname", "find", "printf", "return", "tr"}:
                commands[command] += 1
            elif command:
                commands["other"] += 1
        return "statusline-shell-trace", {
            "environment_case": "inherited" if basename.startswith("inherited") else "clean",
            "line_count": len(text.splitlines()),
            "xtrace_command_count": sum(commands.values()),
            "command_counts": dict(sorted(commands.items())),
            "home_reference_count": len(re.findall(r"\bHOME\b", text)),
            "ansi_sequence_count": len(ANSI.findall(text)),
        }

    raise ValueError(f"unsupported residual log: {path}")


def generate(repo: Path, revision: str) -> dict[str, object]:
    observations = []
    for path in SOURCE_PATHS:
        data = git("show", f"{revision}:{path}", cwd=repo)
        kind, facts = normalize(path, data)
        observations.append(
            {
                "source_path": path,
                "source_sha256": sha256(data),
                "source_git_blob": git("rev-parse", f"{revision}:{path}", cwd=repo).decode().strip(),
                "source_bytes": len(data),
                "kind": kind,
                "facts": facts,
            }
        )
    return {
        "schema": "graph-advantage-vm-statusline-observations-v1",
        "source_revision": revision,
        "observation_count": len(observations),
        "source_bytes": sum(item["source_bytes"] for item in observations),
        "redactions": [
            "raw collector messages are reduced to two reviewed lifecycle states",
            "statusline rendering and traces retain results and command counts without raw paths or environment values",
        ],
        "observations": observations,
    }


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path("."))
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.write_text(json.dumps(generate(args.repo, args.revision), indent=2, sort_keys=True) + "\n")
