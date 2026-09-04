"""
CMM (codebase-memory-mcp) Memory Client
=======================================

Duck-typed stand-in for ``Mem0Client`` backed by **codebase-memory-mcp**
("cmm", DeusData, v0.9.0) -- the tree-sitter knowledge-graph engine that was an
arm of the superseded graphify-parity campaign but was never ported here.

Is cmm a memory system?  It has both halves, and they do work on prose:
  * STORE    ``index_repository`` walks a directory and persists a knowledge
             graph into a SQLite store under ``CBM_CACHE_DIR``.  Markdown
             headings become ``Section`` nodes (verified live: a 2-file prose
             corpus indexed to 13 nodes / 12 edges).  Deterministic, 0 LLM,
             0 network, 0 API keys.
  * RETRIEVE ``search_graph`` runs BM25 (SQLite FTS5) over node names /
             qualified names and returns ``name``/``file_path``/``start_line``/
             ``end_line``/``rank``.

  CAVEAT, load-bearing: the SHIPPED v0.9.0 binary excludes ``Section`` from the
  BM25 result set (``src/mcp/mcp.c::bm25_search``, the two
  ``n.label NOT IN ('File','Folder','Module','Section','Variable','Project')``
  predicates around lines 1705 and 1738).  On a prose corpus the shipped build
  therefore indexes everything and retrieves NOTHING -- verified live:
  ``{"total":0,"search_mode":"bm25","results":[]}``.  The graphmark #64 patch
  (``~/memarms/inputs/cmm-v0.9.0-markdown-sections.patch``) drops ``Section``
  from that exclusion list and nothing else; the patched build returns the
  section text.  This client defaults to the PATCHED binary, i.e. the most
  charitable version of the product; ``CMM_BUILD=stock`` selects the other.

  Because the two builds differ only in a string constant, and a stock build
  scores a structural zero on prose, the arm must never run a binary it has not
  identified: the resolved binary is fingerprinted against the declared build
  at construction and an unrecognised or mismatched build ABORTS the run. There
  is no fallback -- attributing an unknown build's score to the published
  ``cmm (patched, Markdown-Section)`` row is the silent-wrong-number failure
  this kit exists to prevent.

Fairness contract (identical to the entire / mem0 / cognee / graphify arms):
  * ingest bytes IDENTICAL to the entire-graph arm (same session grouping, same
    ``<session>.md`` layout);
  * 0 LLM at ingest; answerer + judge are the harness's (``gpt-5.6-sol``);
  * ``search`` returns ``{"memory","score","id","created_at"}``;
  * ``search`` RAISES on a missing buffer (never a silent ``[]``);
  * every tool request is passed through ``--args-file`` (never argv: ARG_MAX);
  * per-user ``CBM_CACHE_DIR`` (two arms/users sharing a cache would silently
    query one another's index);
  * state roots under ``$HOME`` (``/tmp`` is wiped by systemd on boot).

Env:
  CMM_BIN              REQUIRED. The cmm binary: a path, or a bare name resolved
                       on PATH. There is deliberately no default -- an implicit
                       PATH lookup silently picks up whichever build happens to
                       be installed, and the two builds do not score alike.
  CMM_BUILD            which build is being declared: ``patched`` (default; the
                       published ``cmm (patched, Markdown-Section)`` row) or
                       ``stock`` (upstream v0.9.0, which retrieves nothing on
                       prose). The binary is fingerprinted and must BE the
                       declared build -- see ``_verify_build``.
  CMM_STATE_ROOT       state root (default: $HOME/memarms/state/cmm_corpora/<pid>)
  CMM_TIMEOUT          per-subprocess timeout seconds (default 900)
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import re
import shutil
import subprocess
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

# The upstream executable name, used only in operator-facing messages. It is
# NOT a default: resolving it implicitly is exactly how a stock build would get
# published under the patched build's label.
_UPSTREAM_BIN = "codebase-memory-mcp"

# The BM25 exclusion list of ``src/mcp/mcp.c::bm25_search``, before and after
# ``patches/0005-cmm-v0.9.0-markdown-sections.patch``. Both occurrences in that
# function are adjacent C string literals, so each form is concatenated at
# compile time and survives verbatim in the binary's read-only data: the build
# is therefore identifiable from the file alone, with no execution, no version
# flag to trust, and no network.
_STOCK_MARKER = b"'File','Folder','Module','Section','Variable','Project'"
_PATCHED_MARKER = b"'File','Folder','Module','Variable','Project'"
_MARKERS = {"stock": _STOCK_MARKER, "patched": _PATCHED_MARKER}
_DEFAULT_BUILD = "patched"

_PATCH_REF = "patches/0005-cmm-v0.9.0-markdown-sections.patch"
_PUBLISHED_LABEL = "cmm (patched, Markdown-Section)"


def _resolve_binary(value: str | None) -> str:
    """Resolve and validate the cmm binary once, at construction.

    ``CMM_BIN`` is required: there is no default to fall back to, because a
    bare-name PATH lookup resolves to whichever build is installed and the two
    builds score differently on this corpus. A value containing a path
    separator must exist and be executable; a bare name is looked up on PATH.
    Either way the failure is one clear error naming ``CMM_BIN``, not a
    ``FileNotFoundError`` raised per subprocess once the run is under way.
    """
    value = (value or "").strip()
    if not value:
        raise RuntimeError(
            "CMM_BIN is required and was not set. Point it at the cmm binary "
            f"(a path, or the bare name {_UPSTREAM_BIN!r} to resolve on PATH). "
            "This arm has no default on purpose: the published row is the "
            f"{_PATCH_REF} build, and an implicit PATH lookup would silently "
            "score whichever build happened to be installed."
        )
    separators = [os.sep] + ([os.altsep] if os.altsep else [])
    if any(sep in value for sep in separators):
        path = Path(value).expanduser()
        if not (path.is_file() and os.access(path, os.X_OK)):
            raise RuntimeError(
                f"CMM_BIN={value!r} is not an executable file."
            )
        return str(path)
    found = shutil.which(value)
    if not found:
        raise RuntimeError(
            f"the cmm binary {value!r} named by CMM_BIN was not found on PATH. "
            "Put it on PATH, or export CMM_BIN=/path/to/codebase-memory-mcp."
        )
    return found


def _fingerprints(path: str) -> set[str]:
    """Which build fingerprints ``path`` carries, scanned without executing it."""
    overlap = max(len(m) for m in _MARKERS.values()) - 1
    found: set[str] = set()
    try:
        with open(path, "rb") as handle:
            carry = b""
            while True:
                chunk = handle.read(1 << 20)
                if not chunk:
                    break
                window = carry + chunk
                for build, marker in _MARKERS.items():
                    if marker in window:
                        found.add(build)
                if len(found) == len(_MARKERS):
                    break
                carry = window[-overlap:]
    except OSError as exc:
        raise RuntimeError(
            f"CMM_BIN={path!r} could not be read to verify which build it is: {exc}"
        ) from exc
    return found


def _verify_build(path: str, declared: str) -> str:
    """Refuse any binary that is not positively identified as ``declared``.

    Fail-closed in every direction: the shipped build under the patched label
    would publish a structural zero as a real score, and an unidentifiable
    build would publish an unknown one. Neither is recoverable after the fact,
    so both abort here rather than at analysis time.
    """
    if declared not in _MARKERS:
        raise RuntimeError(
            f"CMM_BUILD={declared!r} is not a known cmm build. Use "
            f"{_DEFAULT_BUILD!r} (the published {_PUBLISHED_LABEL} row, built "
            f"with {_PATCH_REF}) or 'stock' (upstream v0.9.0)."
        )
    found = _fingerprints(path)
    if found == {declared}:
        return declared
    if found == {"stock"} and declared == "patched":
        raise RuntimeError(
            f"CMM_UNPATCHED_BINARY: CMM_BIN={path!r} is the SHIPPED cmm build -- "
            "its BM25 queries still exclude 'Section', so Markdown headings are "
            "indexed and never retrieved and this corpus scores a structural "
            f"zero. The published row is {_PUBLISHED_LABEL} and requires "
            f"{_PATCH_REF}. Rebuild the binary with that patch, or set "
            "CMM_BUILD=stock to score the shipped build deliberately."
        )
    if found == {"patched"} and declared == "stock":
        raise RuntimeError(
            f"CMM_BUILD=stock was declared but CMM_BIN={path!r} carries the "
            f"{_PATCH_REF} exclusion list, i.e. it is the patched build. A run "
            "must be labelled with the build it actually used; unset CMM_BUILD "
            f"to score it as {_PUBLISHED_LABEL}."
        )
    raise RuntimeError(
        f"CMM_UNVERIFIED_BINARY: could not identify the build of CMM_BIN={path!r} "
        f"(fingerprints found: {sorted(found) or 'none'}). Exactly one of the "
        "shipped and patched BM25 exclusion lists must be present, so this "
        f"binary is refused rather than have its score attributed to "
        f"{_PUBLISHED_LABEL}. Build cmm v0.9.0 with {_PATCH_REF} and point "
        "CMM_BIN at the resulting executable."
    )


def _safe_id(value: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]", "_", str(value))
    return cleaned[:180] or "x"


def _one_line(value: str) -> str:
    return " ".join(str(value).split())


def _iso(timestamp: int | None) -> str:
    if timestamp is None:
        return ""
    try:
        return datetime.fromtimestamp(int(timestamp), tz=timezone.utc).isoformat()
    except (OverflowError, OSError, ValueError):
        return ""


class CmmClient:
    def __init__(self, rpm: int = 60, **kwargs: Any) -> None:
        self.binary = _resolve_binary(os.getenv("CMM_BIN"))
        self.build = _verify_build(
            self.binary,
            (os.getenv("CMM_BUILD") or _DEFAULT_BUILD).strip().lower(),
        )
        logger.info("cmm binary=%s build=%s", self.binary, self.build)
        self.timeout = int(os.getenv("CMM_TIMEOUT", "900"))
        root = os.getenv("CMM_STATE_ROOT") or os.path.join(
            os.path.expanduser("~"), "memarms", "state",
            "cmm_corpora", str(os.getpid()),
        )
        self._root = Path(root)
        self._root.mkdir(parents=True, exist_ok=True)

        self._buffers: dict[str, list[tuple[int | None, list[dict]]]] = {}
        self._built: set[str] = set()
        self._session_dates: dict[str, dict[str, str]] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    # -- context manager ---------------------------------------------------

    async def __aenter__(self) -> "CmmClient":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        return None

    async def close(self) -> None:
        return None

    async def get_user_profile(self, user_id: str):
        return None

    # -- helpers -----------------------------------------------------------

    def _user_key(self, user_id: str) -> str:
        return hashlib.sha256(str(user_id).encode("utf-8")).hexdigest()[:16]

    def _base(self, user_id: str) -> Path:
        return self._root / self._user_key(user_id)

    def _corpus_dir(self, user_id: str) -> Path:
        return self._base(user_id) / "corpus"

    def _cache_dir(self, user_id: str) -> Path:
        return self._base(user_id) / "cbm"

    def _project(self, user_id: str) -> str:
        return f"mem-{self._user_key(user_id)}"

    def _lock(self, user_id: str) -> asyncio.Lock:
        lk = self._locks.get(user_id)
        if lk is None:
            lk = asyncio.Lock()
            self._locks[user_id] = lk
        return lk

    def _call(self, user_id: str, tool: str, arguments: dict, tag: str) -> dict:
        """Invoke one cmm CLI tool. Arguments go through --args-file, never
        argv (a LoCoMo question in argv is fine, but the same code path also
        carries paths + future payloads and ARG_MAX kills at ~2MB)."""
        cache = self._cache_dir(user_id)
        cache.mkdir(parents=True, exist_ok=True)
        args_path = self._base(user_id) / f".args_{tool}_{tag}_{os.getpid()}_{uuid.uuid4().hex[:8]}.json"
        args_path.write_text(
            json.dumps(arguments, ensure_ascii=False), encoding="utf-8"
        )
        env = {
            **os.environ,
            "CBM_CACHE_DIR": str(cache),
            "CBM_ALLOWED_ROOT": str(self._corpus_dir(user_id).resolve()),
            # cmm's RAM-first pipeline defaults to HALF OF SYSTEM RAM (~60GB
            # here) per process. With several workers plus other arms on the
            # box that reservation is what fails a search outright, so cap it:
            # these corpora are <2MB, so the cap is generous, and it is a
            # resource setting, not a capability change.
            "CBM_MEM_BUDGET_MB": os.getenv("CMM_MEM_BUDGET_MB", "4096"),
        }
        try:
            proc = subprocess.run(
                [self.binary, "cli", "--json", tool, "--args-file", str(args_path)],
                capture_output=True,
                text=True,
                timeout=self.timeout,
                check=False,
                env=env,
            )
        finally:
            args_path.unlink(missing_ok=True)
        if proc.returncode != 0:
            # The harness truncates exception text at 200 chars; log the whole
            # thing here so a drop is diagnosable after the fact.
            logger.error(
                "cmm %s FAILED rc=%s\n--stderr--\n%s\n--stdout--\n%s",
                tool, proc.returncode,
                (proc.stderr or "")[:4000], (proc.stdout or "")[:2000],
            )
            raise RuntimeError(
                f"cmm {tool} rc={proc.returncode}: "
                f"{(proc.stderr or '').strip()[:800]}"
            )
        stdout = proc.stdout or ""
        # The binary logs plain-text lines (level=info …) before the JSON body.
        start = stdout.find("{")
        if start < 0:
            raise RuntimeError(f"cmm {tool} produced no JSON: {stdout[:300]}")
        try:
            payload = json.loads(stdout[start:])
        except json.JSONDecodeError as exc:
            raise RuntimeError(
                f"cmm {tool} produced non-JSON output: {str(exc)[:200]} :: "
                f"{stdout[start:start + 300]}"
            ) from exc
        if payload.get("isError"):
            raise RuntimeError(f"cmm {tool} returned isError: {str(payload)[:500]}")
        inner = payload.get("structuredContent")
        if isinstance(inner, dict):
            return inner
        content = payload.get("content")
        if isinstance(content, list) and content:
            first = content[0]
            if isinstance(first, dict) and isinstance(first.get("text"), str):
                try:
                    nested = json.loads(first["text"])
                except json.JSONDecodeError:
                    nested = None
                if isinstance(nested, dict):
                    return nested
        return payload

    # -- add ---------------------------------------------------------------

    async def add(
        self,
        messages: list[dict[str, str]],
        user_id: str,
        observation_date: str | None = None,
        timestamp: int | None = None,
        custom_instructions: str | None = None,
        metadata: dict | None = None,
    ) -> dict | None:
        ts = timestamp
        if ts is None and observation_date:
            try:
                d = datetime.strptime(observation_date, "%Y-%m-%d").replace(
                    tzinfo=timezone.utc
                )
                ts = int(d.timestamp())
            except ValueError:
                ts = None
        norm = [
            {"role": str(m.get("role", "user")), "content": str(m.get("content", ""))}
            for m in (messages or [])
        ]
        self._buffers.setdefault(user_id, []).append((ts, norm))
        self._built.discard(user_id)
        return {
            "results": [
                {"id": f"{self._user_key(user_id)}-{i}", "memory": m["content"],
                 "event": "ADD"}
                for i, m in enumerate(norm)
            ]
        }

    # -- materialize + index ----------------------------------------------

    def _materialize_and_index(self, user_id: str) -> None:
        corpus = self._corpus_dir(user_id)
        cache = self._cache_dir(user_id)
        if corpus.exists():
            shutil.rmtree(corpus, ignore_errors=True)
        if cache.exists():
            shutil.rmtree(cache, ignore_errors=True)
        corpus.mkdir(parents=True, exist_ok=True)
        cache.mkdir(parents=True, exist_ok=True)

        session_dates: dict[str, str] = {}
        buf = self._buffers.get(user_id, [])
        sessions: list[tuple[str, int | None, list[dict]]] = []
        prev_ts: object = object()
        for ts, msgs in buf:
            if ts != prev_ts or not sessions:
                sessions.append((f"session_{len(sessions):04d}", ts, []))
                prev_ts = ts
            sessions[-1][2].extend(msgs)

        for session_id, ts, msgs in sessions:
            stem = _safe_id(session_id)
            date_str = _iso(ts)
            body = [
                "---",
                f"corpus_id: {_one_line(user_id)}",
                f"session_id: {_one_line(session_id)}",
                f"date: {_one_line(date_str)}",
                "---",
                "",
                f"# Session {_one_line(session_id)}",
                "",
            ]
            for m in msgs:
                body.append(f"## {_one_line(m['role'])}: {_one_line(m['content'])}")
                body.append("")
            (corpus / f"{stem}.md").write_text("\n".join(body), encoding="utf-8")
            session_dates[stem] = date_str

        payload = self._call(
            user_id,
            "index_repository",
            {
                "repo_path": str(corpus.resolve()),
                "mode": "full",
                "name": self._project(user_id),
                "persistence": False,
            },
            tag=self._user_key(user_id),
        )
        nodes = int(payload.get("nodes", 0) or 0)
        if nodes <= 0:
            raise RuntimeError(
                f"CMM_EMPTY_INDEX: index_repository produced 0 nodes for "
                f"user_id={user_id} :: {str(payload)[:400]}"
            )
        logger.info(
            "cmm index user=%s nodes=%s edges=%s skipped=%s",
            user_id, nodes, payload.get("edges"), payload.get("skipped_count"),
        )
        self._session_dates[user_id] = session_dates
        self._built.add(user_id)

    # -- search ------------------------------------------------------------

    def _node_text(self, corpus: Path, result: dict) -> str:
        """Section body from the corpus (cmm returns locators, not text);
        falls back to cmm's own ``name`` field."""
        name = str(result.get("name", "") or "").strip()
        file_path = str(result.get("file_path", "") or "")
        try:
            start = int(result.get("start_line", 0) or 0)
            end = int(result.get("end_line", start) or start)
        except (TypeError, ValueError):
            return name
        if file_path and start > 0:
            try:
                lines = (corpus / file_path).read_text(
                    encoding="utf-8", errors="replace"
                ).split("\n")
            except OSError:
                lines = []
            if lines:
                end = max(start, min(end, len(lines)))
                chunk = [
                    re.sub(r"^#+\s*", "", ln).strip()
                    for ln in lines[start - 1:end]
                ]
                text = "\n".join(x for x in chunk if x).strip()
                if text:
                    return text
        return name

    async def search(
        self,
        query: str,
        user_id: str,
        top_k: int = 200,
        rerank: bool = False,
        score_debug: bool = False,
    ) -> list[dict]:
        if user_id not in self._buffers:
            # INVARIANT (see cognee/letta/entire clients): never silently
            # return [] for missing in-memory state.
            raise RuntimeError(
                f"BUFFER_MISSING: search() called for user_id={user_id} with "
                f"no buffered content in this process. Likely a resumed run "
                f"whose ingestion checkpoint is complete but whose in-memory "
                f"buffer was never repopulated. Re-ingest this conversation "
                f"before searching it."
            )

        async with self._lock(user_id):
            if user_id not in self._built:
                await asyncio.to_thread(self._materialize_and_index, user_id)

        payload = await asyncio.to_thread(
            self._call,
            user_id,
            "search_graph",
            {
                "project": self._project(user_id),
                "query": query,
                "limit": int(top_k),
            },
            hashlib.sha256(f"{user_id}:{query}".encode("utf-8")).hexdigest()[:12],
        )

        corpus = self._corpus_dir(user_id)
        session_dates = self._session_dates.get(user_id, {})
        results: list[dict] = []
        for i, r in enumerate(payload.get("results", [])):
            if not isinstance(r, dict):
                continue
            text = self._node_text(corpus, r)
            if not text:
                continue
            raw = r.get("score", r.get("rank"))
            try:
                # BM25 ``rank`` is negative-better; negate so bigger == better
                # and cmm's own ordering is preserved exactly.
                score = -float(raw) if r.get("score") is None else float(raw)
            except (TypeError, ValueError):
                score = float(len(payload.get("results", [])) - i)
            entry: dict[str, Any] = {
                "memory": text,
                "score": score,
                "id": str(
                    r.get("qualified_name")
                    or f"{r.get('file_path', '')}:{r.get('start_line', 0)}"
                ),
            }
            created = session_dates.get(Path(str(r.get("file_path", ""))).stem)
            if created:
                entry["created_at"] = created
            results.append(entry)
        results.sort(key=lambda x: x.get("score", 0.0), reverse=True)
        return results

    # -- delete ------------------------------------------------------------

    async def delete_user(self, user_id: str) -> bool:
        self._buffers.pop(user_id, None)
        self._built.discard(user_id)
        self._session_dates.pop(user_id, None)
        base = self._base(user_id)
        if base.exists():
            await asyncio.to_thread(shutil.rmtree, base, True)
        return True
