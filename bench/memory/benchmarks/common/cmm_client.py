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
  charitable version of the product; ``CMM_BIN`` selects the other.

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
  CMM_BIN              binary (default: the patched build)
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

_DEFAULT_BIN = (
    "/home/suhaan_entire_io/memarms/inputs/bin/cmm-patched/codebase-memory-mcp"
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
        self.binary = os.getenv("CMM_BIN", _DEFAULT_BIN)
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
