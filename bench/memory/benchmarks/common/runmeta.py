"""Run-metadata capture for benchmark fairness auditing.

Why this exists: a fairness audit of the 2026-08 memory-benchmark matrix could not
determine what configuration each arm actually ran under, because run artifacts
recorded only answerer/judge/provider/top_k/seed. The arm-asymmetric settings
(EG_SESSION_EXPAND, EG_ANSWER_ENUM, --user-profile) lived only in launcher shell
scripts and had to be reconstructed by hand. Every run artifact must now be
self-documenting: config + code identity travel with the numbers.
"""
from __future__ import annotations

import hashlib
import os
import sys
import re
import subprocess
from pathlib import Path

# Env vars that can change what an arm sees or says. Prefix match.
_CAPTURE_PREFIXES = (
    "EG_", "ENTIRE_", "MEM0_", "QDRANT_", "SUPERMEMORY_", "SM_", "LETTA_",
    "COGNEE_", "GRAPHITI_", "NEO4J_", "REDIS_", "EMBED_", "OPENAI_", "AZURE_",
    "ANTHROPIC_", "LLM_", "FAIR_", "BENCH_", "HARNESS_", "COLLECTION_",
)
# Never emit these values in cleartext; emit a stable fingerprint instead.
_SECRET_RE = re.compile(r"(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API)", re.I)

# Settings that make one arm's pipeline different from another's. Any of these
# being set is a fairness violation unless every arm sets it identically.
ASYMMETRY_FLAGS = (
    "EG_SESSION_EXPAND",
    "EG_SESSION_EXPAND_CAP",
    "EG_ANSWER_ENUM",
    "EG_ANSWER_ENUM_R",
    "EG_USER_PROFILE",
)


def _fingerprint(value: str) -> str:
    return "sha256:" + hashlib.sha256(value.encode()).hexdigest()[:12]


def env_snapshot() -> dict:
    """Every benchmark-relevant env var. Secret values become fingerprints."""
    out = {}
    for k, v in sorted(os.environ.items()):
        if not k.startswith(_CAPTURE_PREFIXES):
            continue
        out[k] = _fingerprint(v) if _SECRET_RE.search(k) else v
    return out


def code_hashes(root: Path | str | None = None) -> dict:
    """md5 of every file that can change a measured number.

    Arms are only comparable if they ran identical code. The 2026-08 LoCoMo
    matrix did not: a search-retry wrapper and a buffer-invariant guard landed
    mid-matrix, so the five arms ran three different versions of the harness.
    """
    root = Path(root) if root else Path(__file__).resolve().parent.parent
    targets = [
        root / "locomo" / "run.py",
        root / "locomo" / "prompts.py",
        root / "longmemeval" / "run.py",
        root / "longmemeval" / "prompts.py",
    ]
    targets += sorted((root / "common").glob("*_client.py"))
    targets += [
        root / "common" / "entra_auth.py",
        root / "common" / "llm_client.py",
        root / "common" / "metrics.py",
        root / "common" / "runmeta.py",
        root / "common" / "utils.py",
        root.parent / "requirements-lock-py312.txt",
    ]
    out = {}
    for p in targets:
        if not p.is_file():
            continue
        key = str(p.relative_to(root.parent)) if root.parent in p.parents else p.name
        out[key] = hashlib.md5(p.read_bytes()).hexdigest()
    return dict(sorted(out.items()))


def git_state(root: Path | str | None = None) -> dict:
    root = str(Path(root) if root else Path(__file__).resolve().parents[2])
    def _run(*a):
        try:
            return subprocess.run(a, cwd=root, capture_output=True, text=True,
                                  timeout=15).stdout.strip()
        except Exception:
            return ""
    return {"commit": _run("git", "rev-parse", "HEAD"),
            "dirty": bool(_run("git", "status", "--porcelain"))}


def asymmetry_report() -> dict:
    """Which arm-asymmetric knobs are active right now."""
    return {k: os.environ[k] for k in ASYMMETRY_FLAGS if os.environ.get(k)}


def assert_fair_mode(args=None) -> None:
    """Hard-fail when FAIR_MODE=1 and any arm-asymmetric knob is set.

    Fair mode is the published-numbers mode: no arm may carry retrieval
    augmentation or prompt blocks the other arms do not also carry.
    """
    if os.getenv("FAIR_MODE") != "1":
        return
    active = asymmetry_report()
    # --user-profile is a CLI flag, not an env var: it injected a "## User Profile"
    # prompt section that only the entire arm ever received (498/500 vs 0/500).
    if args is not None and getattr(args, "user_profile", False):
        active["--user-profile"] = "True"
    if active:
        raise SystemExit(
            "FAIR_MODE=1 but arm-asymmetric settings are active: "
            + ", ".join(f"{k}={v}" for k, v in active.items())
            + "\nUnset them, or run without FAIR_MODE=1 for an exploratory run."
        )


def capture(extra: dict | None = None) -> dict:
    """The block to embed under metadata in every run artifact."""
    block = {
        "env": env_snapshot(),
        "asymmetric_settings_active": asymmetry_report(),
        "fair_mode": os.getenv("FAIR_MODE") == "1",
        "code_md5": code_hashes(),
        "git": git_state(),
        "host": os.uname().nodename,
        "argv": list(sys.argv),
    }
    if extra:
        block.update(extra)
    return block
