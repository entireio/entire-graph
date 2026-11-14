"""Read original excerpts through the installed Entire export interface."""
import json
import re
import subprocess

from .contracts import Source, digest
from .gitutil import git, resolve


def export(repo, *args):
    p = subprocess.run(["entire", "checkpoint", "explain", *args], cwd=repo,
                       capture_output=True, text=True, timeout=30)
    if p.returncode:
        raise RuntimeError(p.stderr[:1000] or "Checkpoint export unavailable")
    return p.stdout


def read_sources(repo, commit):
    sha = resolve(repo, commit)
    match = re.search(r"^Entire-Checkpoint:\s*([0-9a-f]{12,64})\s*$", git(repo, "show", "-s", "--format=%B", sha), re.M)
    if not match:
        return {"commit_sha": sha, "sources": [], "warnings": ["This commit has no Entire Checkpoint trailer"]}
    checkpoint = match.group(1)
    envelope = json.loads(export(repo, checkpoint, "--json"))
    if envelope.get("checkpoint_id") != checkpoint:
        raise ValueError("Checkpoint export identity mismatch")
    sources, seen, warnings = [], set(), []
    for session in envelope.get("sessions", []):
        index = session["index"]
        raw = export(repo, checkpoint, "--transcript", "--session-index", str(index))
        for number, line in enumerate(raw.splitlines(), 1):
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                warnings.append(f"Unparsed transcript line {number}")
                continue
            payload = record.get("payload", {})
            if record.get("type") != "response_item" or payload.get("type") != "message":
                continue
            role = payload.get("role")
            if role not in ("user", "assistant"):
                continue
            content = payload.get("content", [])
            text = "\n".join(c.get("text", "") for c in content if isinstance(c, dict) and c.get("type") in ("input_text", "output_text"))
            if not text.strip():
                continue
            # Bounded display excerpt. Locator/hash describe precisely this displayed text.
            excerpt = text[:6000]
            key = (session["session_id"], digest(excerpt))
            if key in seen:
                continue
            seen.add(key)
            sources.append(Source(checkpoint_id=checkpoint, session_id=session["session_id"],
                                  associated_commit=sha, association_status="manual_attachment",
                                  message_role=role, excerpt=excerpt, excerpt_hash=digest(excerpt),
                                  excerpt_locator=f"session-index:{index}/line:{number}/chars:0-{len(excerpt)}",
                                  source_uri=f"entire://checkpoint/{checkpoint}?session={index}"))
    if not sources:
        warnings.append("No supported Codex message excerpts were found; do not infer intent")
    return {"commit_sha": sha, "checkpoint_id": checkpoint, "metadata": envelope,
            "sources": [s.model_dump() for s in sources], "warnings": warnings}
