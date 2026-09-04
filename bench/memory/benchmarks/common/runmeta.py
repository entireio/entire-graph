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
from urllib.parse import urlsplit, urlunsplit

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
#
# Keep this list complete: a knob that changes behaviour but escapes the guard
# lets a run claim FAIR_MODE while not being fair, which is worse than having no
# guard at all. `test_runmeta.py` re-derives the arm-scoped knobs the kit
# actually reads and fails when one is neither listed here nor declared
# infrastructure in SYMMETRIC_ARM_SETTINGS below.
ASYMMETRY_FLAGS = (
    # entire-graph retrieval / prompt augmentation
    "EG_SESSION_EXPAND",
    "EG_SESSION_EXPAND_CAP",
    "EG_ANSWER_ENUM",
    "EG_ANSWER_ENUM_R",
    "EG_USER_PROFILE",
    "EG_PROFILE",
    "EG_PROFILE_FACT_CAP",
    "EG_PROFILE_TIMELINE_CAP",
    # entire-graph ingest shape
    "EG_INGEST_GRANULARITY",
    "EG_CONSOLIDATE",
    "EG_DEEP",
    "EG_CHRONO_ORDER",
    "ENTIRE_MAX_CONTEXT_BYTES",
    # mem0: rewrites the first ingest message with a date preamble, changing
    # what mem0's extractor sees (patch 0002; off in the published runs).
    "MEM0_DATE_INJECT",
    # bm25 scoring / tokenisation
    "BM25_B",
    "BM25_K1",
    "BM25_STEM",
    "BM25_STOPWORDS",
    # per-arm capacity and deadlines: a truncated budget or a short deadline
    # changes what exactly one arm can retrieve or return.
    "CMM_MEM_BUDGET_MB",
    "CMM_TIMEOUT",
    "GRAPHIFY_TIMEOUT",
)

# `EG_` is the entire-graph adapter's private namespace -- every variable under
# it is by construction our own arm's knob, so an unrecognised one is a
# violation too and the guard does not go stale when a new one is added. The
# other arm prefixes are shared with unrelated tooling (`ENTIRE_TOKEN`,
# `MEM0_HOST`, ...) and are enumerated explicitly instead.
ASYMMETRIC_PREFIXES = ("EG_",)

# Arm-scoped variables that only say WHERE a backend lives or WHICH arm runs.
# They cannot change what an arm ingests, retrieves, or says, so they are legal
# under FAIR_MODE -- `run_locomo.sh` sets several of them on every fair run.
SYMMETRIC_ARM_SETTINGS = frozenset({
    "ENTIRE_CORPUS_ROOT",
    "ENTIRE_GRAPH_BIN",
    "MEM0_HOST",
    "MEM0_BACKEND",
    "BM25_STATE_ROOT",
    "CMM_BIN",
    "CMM_STATE_ROOT",
    "GRAPHIFY_BRIDGE",
    "GRAPHIFY_PYTHON",
    "GRAPHIFY_SOURCE",
    "GRAPHIFY_STATE_ROOT",
})


# --- argv redaction -------------------------------------------------------
# The provenance block below is committed alongside published numbers, and
# credentials do reach the command line: `--mem0-api-key` is a documented option
# of the patched runner (patch 0003), launchers pass `NAME=value` prefixes, and
# a stray positional can be anything. argv is therefore redacted before it is
# persisted, never after.
#
# The filter is an allowlist, not a denylist: only option names known to this
# harness survive, and only the value-taking subset keeps its value. A denylist
# of secret-looking tokens is defeated by the first credential shape nobody
# thought of; an allowlist fails closed on it.
_ARGV_REDACTED = "<redacted>"

# Value-taking options of benchmarks/{locomo,longmemeval}/run.py whose value is
# configuration rather than a credential. `--backend` is load-bearing:
# ci/summarize_run.py reads the running arm back out of the captured argv.
_ARGV_SAFE_VALUE_OPTS = frozenset({
    "--answerer-model", "--backend", "--categories", "--conversations",
    "--dataset-path", "--judge-model", "--judge-provider", "--max-questions",
    "--max-workers", "--mem0-host", "--mode", "--output-dir", "--per-type",
    "--project-name", "--provider", "--question-types", "--question-workers",
    "--rpm", "--run-id", "--seed", "--top-k", "--top-k-cutoffs",
})
# Flags that take no value at all.
_ARGV_SAFE_TOGGLES = frozenset({
    "--all-questions", "--debug", "--evaluate-only", "--predict-only",
    "--rejudge", "--resume", "--score-debug", "--user-profile",
    "--with-evidence",
})
# Known options whose value is a credential: the name is useful provenance
# ("a key was passed on the CLI"), the value never is.
_ARGV_SECRET_OPTS = frozenset({"--mem0-api-key"})
_ARGV_SAFE_OPTS = _ARGV_SAFE_VALUE_OPTS | _ARGV_SAFE_TOGGLES | _ARGV_SECRET_OPTS



def _scrub_value(value: str) -> str:
    """Keep a URL value's location; drop every component that can carry a secret.

    Credentials ride in every part of a URL but its location: userinfo
    (`https://user:pass@host`), a path segment (`/hooks/<token>`), a query
    parameter (`?token=...`), and a fragment. Only scheme, host and port are
    provenance, so everything else is dropped wholesale rather than
    pattern-matched -- for the same reason the option filter is an allowlist.
    Non-URL values (filesystem paths, model names) are returned unchanged.
    """
    try:
        parts = urlsplit(value)
        scheme, netloc, path = parts.scheme, parts.netloc, parts.path
        query, fragment = parts.query, parts.fragment
    except ValueError:
        return _ARGV_REDACTED
    if not scheme or not netloc:
        return value
    if "@" in netloc:
        # Split on the LAST "@": userinfo may not contain an unescaped one, so
        # a value that does is malformed and stripping more of it is the safe
        # direction.
        netloc = _ARGV_REDACTED + "@" + netloc.rpartition("@")[2]
    scrubbed = urlunsplit((scheme, netloc, "", "", ""))
    if path not in ("", "/"):
        scrubbed += "/" + _ARGV_REDACTED
    else:
        scrubbed += path
    if query:
        scrubbed += "?" + _ARGV_REDACTED
    if fragment:
        scrubbed += "#" + _ARGV_REDACTED
    return scrubbed


def redact_argv(argv=None) -> list[str]:
    """The command line with every token that is not known-safe removed.

    argv[0] keeps only its basename. After it, a token survives verbatim only if
    it is an allowlisted option name, or the value of an allowlisted
    value-taking option. Everything else -- unknown flags and their values,
    `NAME=value` environment prefixes, bare positionals -- becomes
    ``<redacted>``.
    """
    argv = list(sys.argv if argv is None else argv)
    if not argv:
        return []
    out = [os.path.basename(argv[0])]
    pending_safe_value = False
    for token in argv[1:]:
        if token.startswith("-") and token not in ("-", "--"):
            name, sep, value = token.partition("=")
            if name not in _ARGV_SAFE_OPTS:
                out.append(_ARGV_REDACTED)
                pending_safe_value = False
            elif sep:
                keep = name in _ARGV_SAFE_VALUE_OPTS
                out.append(f"{name}={_scrub_value(value) if keep else _ARGV_REDACTED}")
                pending_safe_value = False
            else:
                out.append(name)
                pending_safe_value = name in _ARGV_SAFE_VALUE_OPTS
            continue
        out.append(_scrub_value(token) if pending_safe_value else _ARGV_REDACTED)
        pending_safe_value = False
    return out


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


def _reported_value(name: str, value: str) -> str:
    """Fingerprint a secret-named value, exactly as env_snapshot does.

    This report is persisted in the artifact *and* interpolated into the
    FAIR_MODE exception text, so a credential-bearing knob caught by the `EG_*`
    catch-all (`EG_API_KEY`, say) would otherwise reach CI logs in cleartext.
    """
    return _fingerprint(value) if _SECRET_RE.search(name) else value


def asymmetry_report() -> dict:
    """Which arm-asymmetric knobs are active right now."""
    active = {
        k: _reported_value(k, os.environ[k])
        for k in ASYMMETRY_FLAGS
        if os.environ.get(k)
    }
    for k, v in os.environ.items():
        if not v or k in active or k in SYMMETRIC_ARM_SETTINGS:
            continue
        if k.startswith(ASYMMETRIC_PREFIXES):
            active[k] = _reported_value(k, v)
    return dict(sorted(active.items()))


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
        "argv": redact_argv(),
    }
    if extra:
        block.update(extra)
    return block
