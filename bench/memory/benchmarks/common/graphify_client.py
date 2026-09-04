"""
Graphify Memory Client
======================

Duck-typed stand-in for ``Mem0Client`` backed by **Graphify**
(https://github.com/…/graphify), the graph-extraction + graph-query tool that
was an arm of the superseded graphify-parity campaign but was never ported to
this harness.

Is Graphify a memory system?  For prose it has both halves:
  * STORE    ``graphify.extractors.markdown.extract_markdown`` turns each
             Markdown document into heading/section nodes + ``contains`` edges;
             the graph is persisted as ``graph.json``.  Deterministic, 0 LLM,
             0 network (this is Graphify's own ``structural`` mode -- the same
             mode the parity harness sealed; ``graphify extract --backend …``
             semantic mode is the LLM variant and is NOT used here).
  * RETRIEVE ``graphify.serve._score_query`` ranks nodes for a natural-language
             query, ``_pick_seeds`` + ``_bfs`` (depth 3) restrict to the
             connected neighbourhood, and each surviving node carries its own
             text (``label``), source file and line.

Fairness contract (identical to the entire / mem0 / cognee arms):
  * The ingest bytes are IDENTICAL to the entire-graph arm: the same session
    grouping and the same ``<session>.md`` layout (``entire_client.py::
    _materialize_and_index``), so no arm is handed a different corpus.
  * 0 LLM at ingest, so no extractor model is involved; answerer + judge are
    the harness's (``gpt-5.6-sol``), unchanged.
  * ``search`` returns ``list[dict]`` with ``{"memory","score","id",
    "created_at"}`` -- the exact keys the harness reads.
  * ``search`` RAISES on a missing buffer instead of returning ``[]`` (the
    silent-empty-context defect that corrupted 500+ answers on two other
    arms).
  * Bridge requests go through a FILE, never argv (ARG_MAX).
  * State roots live under ``$HOME`` (``/tmp`` is wiped by systemd on boot).

Retrieval granularity note: the parity bridge de-duplicated hits by source
file because that benchmark scored *file locators*.  A memory benchmark scores
*evidence items*, and file de-duplication would cap Graphify at (n_sessions)
memories regardless of ``top_k``.  This client therefore returns Graphify's own
ranked NODES (up to ``top_k``).  Ranking, seeding, traversal and node text are
Graphify's; nothing here re-ranks or rewrites them.

Env (GRAPHIFY_PYTHON and GRAPHIFY_SOURCE are REQUIRED -- graphify has no
discoverable default on a fresh machine, so both are validated at construction
instead of being defaulted to one contributor's directory layout):
  GRAPHIFY_PYTHON        interpreter with graphify + networkx importable (required)
  GRAPHIFY_SOURCE        graphify source checkout, added to sys.path (required)
  GRAPHIFY_BRIDGE        override bridge script path
  GRAPHIFY_STATE_ROOT    per-run state root (default: $HOME/memarms/state/graphify_corpora/<pid>)
  GRAPHIFY_TIMEOUT       per-subprocess timeout seconds (default 900)
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

_PYTHON_ENV = "GRAPHIFY_PYTHON"
_SOURCE_ENV = "GRAPHIFY_SOURCE"


def _require_configured_path(env: str, kind: str, what: str) -> str:
    """Read ``env``, and fail loudly if it is missing or does not resolve.

    There is deliberately no default. A default that only resolves on the
    machine the published runs happened to use makes the arm unreproducible
    while looking configured, and the failure then surfaces mid-ingest as an
    opaque subprocess error. Fail here, once, with the variable to set.
    """
    value = os.getenv(env, "").strip()
    if not value:
        raise RuntimeError(
            f"{env} is not set. The graphify arm needs {what}, and there is no "
            f"portable default for it. Export {env}=<path> before running this arm."
        )
    path = Path(value).expanduser()
    if kind == "exe":
        ok, expected = path.is_file() and os.access(path, os.X_OK), "an executable file"
    else:
        ok, expected = path.is_dir(), "a directory"
    if not ok:
        raise RuntimeError(
            f"{env}={value!r} is not {expected}. It must point at {what}."
        )
    return str(path)


def _default_bridge_path() -> str:
    return str(Path(__file__).with_name("graphify_mem_bridge.py"))


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


class GraphifyClient:
    def __init__(self, rpm: int = 60, **kwargs: Any) -> None:
        self.python = _require_configured_path(
            _PYTHON_ENV, "exe",
            "a Python interpreter with graphify and networkx importable",
        )
        self.source = _require_configured_path(
            _SOURCE_ENV, "dir",
            "the graphify source checkout that is added to sys.path",
        )
        self.bridge = os.getenv("GRAPHIFY_BRIDGE", _default_bridge_path())
        self.timeout = int(os.getenv("GRAPHIFY_TIMEOUT", "900"))

        root = os.getenv("GRAPHIFY_STATE_ROOT") or os.path.join(
            os.path.expanduser("~"), "memarms", "state",
            "graphify_corpora", str(os.getpid()),
        )
        self._root = Path(root)
        self._root.mkdir(parents=True, exist_ok=True)

        self._buffers: dict[str, list[tuple[int | None, list[dict]]]] = {}
        self._built: set[str] = set()
        self._session_dates: dict[str, dict[str, str]] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    # -- context manager ---------------------------------------------------

    async def __aenter__(self) -> "GraphifyClient":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        return None

    async def close(self) -> None:
        return None

    async def get_user_profile(self, user_id: str):
        # Only invoked under --user-profile; this arm synthesizes none.
        return None

    # -- helpers -----------------------------------------------------------

    def _user_key(self, user_id: str) -> str:
        return hashlib.sha256(str(user_id).encode("utf-8")).hexdigest()[:16]

    def _base(self, user_id: str) -> Path:
        return self._root / self._user_key(user_id)

    def _corpus_dir(self, user_id: str) -> Path:
        return self._base(user_id) / "corpus"

    def _graph_path(self, user_id: str) -> Path:
        return self._base(user_id) / "graph.json"

    def _lock(self, user_id: str) -> asyncio.Lock:
        lk = self._locks.get(user_id)
        if lk is None:
            lk = asyncio.Lock()
            self._locks[user_id] = lk
        return lk

    def _bridge_call(self, action: str, request: dict, tag: str) -> dict:
        """Run the bridge with the request passed as a FILE (never argv)."""
        req_dir = self._base(request["_user"]) if "_user" in request else self._root
        req_dir.mkdir(parents=True, exist_ok=True)
        payload = {k: v for k, v in request.items() if not k.startswith("_")}
        req_path = req_dir / f".req_{action}_{tag}_{os.getpid()}_{uuid.uuid4().hex[:8]}.json"
        req_path.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        try:
            proc = subprocess.run(
                [self.python, self.bridge, action, str(req_path)],
                capture_output=True,
                text=True,
                timeout=self.timeout,
                check=False,
            )
        finally:
            req_path.unlink(missing_ok=True)
        if proc.returncode != 0:
            logger.error(
                "graphify bridge %s FAILED rc=%s\n--stderr--\n%s",
                action, proc.returncode, (proc.stderr or "")[:4000],
            )
            # The harness truncates exception text at 200 chars and this
            # module's logger may have no handler attached, so persist the
            # whole failure next to the state root -- a drop must be
            # diagnosable after the run, never a mystery.
            try:
                fdir = self._root / "failures"
                fdir.mkdir(parents=True, exist_ok=True)
                (fdir / f"{action}_{tag}.txt").write_text(
                    json.dumps(payload, ensure_ascii=False)[:2000]
                    + "\n--stderr--\n" + (proc.stderr or "")
                    + "\n--stdout--\n" + (proc.stdout or "")[:2000],
                    encoding="utf-8",
                )
            except OSError:
                pass
            raise RuntimeError(
                f"graphify bridge {action} rc={proc.returncode}: "
                f"{(proc.stderr or '').strip()[:800]}"
            )
        try:
            return json.loads(proc.stdout or "{}")
        except json.JSONDecodeError as exc:
            raise RuntimeError(
                f"graphify bridge {action} produced non-JSON output: "
                f"{str(exc)[:200]} :: {(proc.stdout or '')[:300]}"
            ) from exc

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

    # -- materialize + build ----------------------------------------------

    def _materialize_and_build(self, user_id: str) -> None:
        """Write buffered sessions as ``<session>.md`` (BYTE-IDENTICAL to the
        entire-graph arm's corpus) and build Graphify's structural graph."""
        corpus = self._corpus_dir(user_id)
        if corpus.exists():
            shutil.rmtree(corpus, ignore_errors=True)
        corpus.mkdir(parents=True, exist_ok=True)

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

        stats = self._bridge_call(
            "build",
            {
                "_user": user_id,
                "graphify_source": self.source,
                "corpus": str(corpus),
                "graph": str(self._graph_path(user_id)),
            },
            tag=self._user_key(user_id),
        )
        if int(stats.get("nodes", 0) or 0) <= 0:
            raise RuntimeError(
                f"GRAPHIFY_EMPTY_GRAPH: build produced 0 nodes for "
                f"user_id={user_id} (files={stats.get('files')})"
            )
        logger.info(
            "graphify build user=%s files=%s nodes=%s edges=%s %.2fs",
            user_id, stats.get("files"), stats.get("nodes"), stats.get("edges"),
            float(stats.get("build_seconds", 0.0) or 0.0),
        )
        self._session_dates[user_id] = session_dates
        self._built.add(user_id)

    # -- search ------------------------------------------------------------

    async def search(
        self,
        query: str,
        user_id: str,
        top_k: int = 200,
        rerank: bool = False,
        score_debug: bool = False,
    ) -> list[dict]:
        if user_id not in self._buffers:
            # INVARIANT (see cognee/letta/entire clients): never return [] for
            # missing in-memory state -- a silent empty context scores as a
            # capability miss instead of the infrastructure error it is.
            raise RuntimeError(
                f"BUFFER_MISSING: search() called for user_id={user_id} with "
                f"no buffered content in this process. Likely a resumed run "
                f"whose ingestion checkpoint is complete but whose in-memory "
                f"buffer was never repopulated. Re-ingest this conversation "
                f"before searching it."
            )

        async with self._lock(user_id):
            if user_id not in self._built:
                await asyncio.to_thread(self._materialize_and_build, user_id)

        payload = await asyncio.to_thread(
            self._bridge_call,
            "retrieve",
            {
                "_user": user_id,
                "graphify_source": self.source,
                "corpus": str(self._corpus_dir(user_id)),
                "graph": str(self._graph_path(user_id)),
                "query": query,
                "top_k": int(top_k),
            },
            hashlib.sha256(f"{user_id}:{query}".encode("utf-8")).hexdigest()[:12],
        )

        session_dates = self._session_dates.get(user_id, {})
        results: list[dict] = []
        for r in payload.get("results", []):
            if not isinstance(r, dict):
                continue
            text = str(r.get("label", "") or "").strip()
            if not text:
                continue
            entry: dict[str, Any] = {
                "memory": text,
                "score": float(r.get("score", 0.0) or 0.0),
                "id": f"{r.get('file_path', '')}:{r.get('line', 0)}",
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
