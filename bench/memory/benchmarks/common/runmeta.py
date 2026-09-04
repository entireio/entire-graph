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
# Both the option NAME and its VALUE are allowlisted. Names are matched against
# the set the runners accept; values are matched against the closed domain of
# their own option -- never trusted because the name is known. Trusting a value
# by association is how four consecutive reviews each found a new way to smuggle
# a secret through an "allowlisted" option (URL userinfo, then query, fragment,
# path segment, then an authority-less `file:` URI). The partition below removes
# the category instead of patching its members:
#
#   (a) recorded verbatim -- integers, integer lists, closed enums; the value is
#       validated at write time, so nothing outside the domain is ever written
#   (b) recorded only in derived form -- a URL's location, or a fingerprint
#   (c) never recorded -- credentials, and the identity options that `metadata`
#       already carries as typed fields, so dropping them costs no provenance
_ARGV_REDACTED = "<redacted>"

_INT_RE = re.compile(r"-?\d+$")
_INT_LIST_RE = re.compile(r"\d+(,\d+)*$")

# Enum domains. These MUST equal the runners' argparse `choices=`:
# test_runmeta.py re-derives the backend list from patch 0003 and fails on
# drift, because a backend added to run.py but not here would silently record
# as `<redacted>` -- the artifact going quiet exactly when the config is unusual.
BACKEND_CHOICES = frozenset({
    "oss", "cloud", "entire", "cognee", "supermemory", "graphiti", "letta",
    "graphify", "cmm", "bm25",
})
MODE_CHOICES = frozenset({"retrieval", "answerer"})


def _enum_rule(choices):
    def rule(value: str) -> str:
        return value if value in choices else _ARGV_REDACTED
    return rule


def _pattern_rule(regex):
    def rule(value: str) -> str:
        return value if regex.fullmatch(value) else _ARGV_REDACTED
    return rule


def _fingerprint_rule(value: str) -> str:
    """Class (b): comparable across runs, not readable."""
    return _fingerprint(value)


def _url_location_rule(value: str) -> str:
    """Class (b): keep only where a service lives, drop everything else.

    Credentials ride in every part of a URL but its location -- userinfo
    (`https://user:pass@host`), a path segment (`/hooks/<token>`), a query
    parameter (`?token=...`), a fragment -- and in an authority-less URI
    (`file:///hooks/<token>`) there is no location at all. Only the scheme, host
    and port survive; every other component is dropped wholesale rather than
    inspected. A value that is not a URL is dropped entirely: for a host option
    it is not provenance, and it is the one shape that could still be free text.
    """
    try:
        parts = urlsplit(value)
        scheme, netloc, path = parts.scheme, parts.netloc, parts.path
        query, fragment = parts.query, parts.fragment
    except ValueError:
        return _ARGV_REDACTED
    if not scheme:
        return _ARGV_REDACTED
    if not netloc:
        return scheme + ":" + _ARGV_REDACTED
    if "@" in netloc:
        # Split on the LAST "@": userinfo may not contain an unescaped one, so
        # a value that does is malformed and stripping more is the safe way to
        # be wrong.
        netloc = _ARGV_REDACTED + "@" + netloc.rpartition("@")[2]
    scrubbed = urlunsplit((scheme, netloc, "", "", ""))
    scrubbed += "/" + _ARGV_REDACTED if path not in ("", "/") else path
    if query:
        scrubbed += "?" + _ARGV_REDACTED
    if fragment:
        scrubbed += "#" + _ARGV_REDACTED
    return scrubbed


# How each value-taking option of benchmarks/{locomo,longmemeval}/run.py is
# recorded. `--backend` is load-bearing: ci/summarize_run.py reads the running
# arm back out of the captured argv, and it is the one value with no `metadata`
# twin.
_ARGV_VALUE_RULES = {
    # (a) integers
    "--top-k": _pattern_rule(_INT_RE),
    "--max-workers": _pattern_rule(_INT_RE),
    "--question-workers": _pattern_rule(_INT_RE),
    "--max-questions": _pattern_rule(_INT_RE),
    "--rpm": _pattern_rule(_INT_RE),
    "--seed": _pattern_rule(_INT_RE),
    "--per-type": _pattern_rule(_INT_RE),
    # (a) comma-separated integer lists
    "--conversations": _pattern_rule(_INT_LIST_RE),
    "--categories": _pattern_rule(_INT_LIST_RE),
    "--top-k-cutoffs": _pattern_rule(_INT_LIST_RE),
    # (a) closed enums
    "--backend": _enum_rule(BACKEND_CHOICES),
    "--mode": _enum_rule(MODE_CHOICES),
    # (b) derived, non-reversible
    "--mem0-host": _url_location_rule,
    "--dataset-path": _fingerprint_rule,
    "--output-dir": _fingerprint_rule,
    # (b) argv is the only carrier of the judge provider -- `metadata` records
    # `provider` but never `judge_provider` -- so keep it comparable across runs
    # rather than dropping the fact that the judge ran somewhere else.
    "--judge-provider": _fingerprint_rule,
}

# (c) the name is provenance, the value never is. Every identity option here is
# recorded by `metadata` as a typed field -- project_name, run_id,
# answerer_model, judge_model, provider, question_types -- so an auditor reads
# it there; see FAIR-CONFIG.md B7.
_ARGV_DROP_VALUE_OPTS = frozenset({
    "--mem0-api-key",
    "--project-name", "--run-id", "--answerer-model", "--judge-model",
    "--provider", "--question-types",
})

# Flags that take no value at all.
_ARGV_SAFE_TOGGLES = frozenset({
    "--all-questions", "--debug", "--evaluate-only", "--predict-only",
    "--rejudge", "--resume", "--score-debug", "--user-profile",
    "--with-evidence",
})
_ARGV_SAFE_OPTS = (
    frozenset(_ARGV_VALUE_RULES) | _ARGV_SAFE_TOGGLES | _ARGV_DROP_VALUE_OPTS
)

# argparse's own `_negative_number_matcher`: a token like `-1` is the *value* of
# the preceding option, not a new option, so `--seed -1` must reach that
# option's domain check instead of being read as an unknown flag. Deliberately
# narrow -- consuming any `-`-leading token as a pending value would let
# `--top-k --mem0-api-key=SECRET` record the credential, and argparse rejects
# that command line anyway.
_NEGATIVE_NUMBER_RE = re.compile(r"-\d+$|-\d*\.\d+$")


def redact_argv(argv=None) -> list[str]:
    """The command line with every value reduced to its option's closed domain.

    argv[0] keeps only its basename. After it, a token survives verbatim only if
    it is an allowlisted option name, or a value that validates against the
    domain of the allowlisted option it follows. Everything else -- unknown
    flags and their values, `NAME=value` environment prefixes, bare positionals,
    credentials, and any value outside its option's domain -- becomes
    ``<redacted>`` or a ``sha256:`` fingerprint.
    """
    argv = list(sys.argv if argv is None else argv)
    if not argv:
        return []
    out = [os.path.basename(argv[0])]
    pending_rule = None
    for token in argv[1:]:
        if pending_rule is not None and _NEGATIVE_NUMBER_RE.fullmatch(token):
            out.append(pending_rule(token))
            pending_rule = None
            continue
        if token.startswith("-") and token not in ("-", "--"):
            name, sep, value = token.partition("=")
            if name not in _ARGV_SAFE_OPTS:
                out.append(_ARGV_REDACTED)
            elif sep:
                rule = _ARGV_VALUE_RULES.get(name)
                out.append(f"{name}={rule(value) if rule else _ARGV_REDACTED}")
            else:
                out.append(name)
                pending_rule = _ARGV_VALUE_RULES.get(name)
                continue
            pending_rule = None
            continue
        out.append(pending_rule(token) if pending_rule else _ARGV_REDACTED)
        pending_rule = None
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
