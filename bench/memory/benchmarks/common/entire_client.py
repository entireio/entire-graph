"""
Entire-Graph Memory Client
===========================

A drop-in, duck-typed replacement for ``Mem0Client`` (see ``mem0_client.py``)
that plugs the **entire-graph** prose-retrieval engine into mem0's benchmark
harness WITHOUT changing any other stage of the pipeline.

Design contract (fairness):
  * Exposes the *exact* async surface the harness drives Mem0 through:
        async add(messages, user_id, observation_date=None, timestamp=None,
                  custom_instructions=None, metadata=None) -> dict | None
        async search(query, user_id, top_k=200, rerank=False,
                     score_debug=False) -> list[dict]
        async delete_user(user_id) -> bool
        async close()
        async with EntireMemoryClient(...) as c: ...
  * The ONLY thing that differs from the Mem0 arm is *which memories come back*
    from ``search``. The answerer model+prompt, the judge model+prompt, the
    top-k count cap, the cutoff slicing, the metric computation, and the dataset
    / question set are all owned by the harness and are untouched.
  * ``search`` returns ``list[dict]`` with the same keys the harness reads via
    ``format_search_results`` / the answerer prompt builders:
        {"memory": <text>, "score": <float>, "id": <str>, "created_at": <iso>}
    ``created_at`` is supplied so LOCOMO's chronological memory ordering behaves
    identically to the Mem0 arm.

How entire-graph is invoked (mirrors the sealed graphify-parity adapter,
``graphify_parity/adapters.py::EntireGraphAdapter`` + ``datasets.py::
materialize_memory_dataset``):
  * INGEST: buffered ``add`` calls are grouped into sessions (one session per
    distinct ``timestamp``), written as ``<session_id>.md`` files with YAML
    frontmatter into a per-user git repo, then indexed once (lazily, on first
    search) with ``entire-graph index --repo <corpus> --profile full``.
  * RETRIEVE: ``entire-graph search --repo <corpus> --query <q> --format json
    --top-k <k> --profile full --head --index-all-files
    --max-context-bytes <cap> --cache-dir <cache>``; each JSON result
    (``file_path`` / ``start_line`` / ``score`` / ``snippet``) becomes one
    memory item.

The entire-graph engine is a standalone Go binary. Point the client at it with
``ENTIRE_GRAPH_BIN`` (or the ``binary=`` kwarg).
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
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


def _safe_id(value: str) -> str:
    """Filesystem-safe id (mirrors graphify_parity.datasets._safe_id spirit)."""
    cleaned = re.sub(r"[^A-Za-z0-9_.-]", "_", str(value))
    return cleaned[:180] or "x"


def _one_line(value: str) -> str:
    """Collapse to a single line (frontmatter / heading safety)."""
    return " ".join(str(value).split())


def _iso(timestamp: int | None) -> str:
    if timestamp is None:
        return ""
    try:
        return datetime.fromtimestamp(int(timestamp), tz=timezone.utc).isoformat()
    except (OverflowError, OSError, ValueError):
        return ""


class EntireMemoryClient:
    """Duck-typed stand-in for ``Mem0Client`` backed by entire-graph.

    Args:
        binary: Path to the ``entire-graph`` binary. Falls back to
            ``ENTIRE_GRAPH_BIN`` env var, else ``"entire-graph"`` on PATH.
        corpus_root: Root dir holding per-user corpora + caches. Falls back to
            ``ENTIRE_CORPUS_ROOT`` env var, else a fresh temp dir.
        profile: entire-graph index/search profile (default "full").
        max_context_bytes: ``--max-context-bytes`` cap. Defaults high so the
            top_k items are returned un-clipped — the harness caps by COUNT
            (top_k), identical to the Mem0 arm, so we do not impose an
            entire-specific byte budget the other arms never see.
        timeout_seconds: per-subprocess timeout.
        git_binary: git executable (default "git").
        keep_corpus: if False (default), the temp corpus root is removed on
            ``close()`` when we created it.
    """

    def __init__(
        self,
        *,
        binary: str | os.PathLike[str] | None = None,
        corpus_root: str | os.PathLike[str] | None = None,
        profile: str = "full",
        max_context_bytes: int | None = None,
        timeout_seconds: int = 600,
        git_binary: str = "git",
        rpm: int | None = None,  # accepted for signature-compat; unused
        keep_corpus: bool = False,
        **_ignored: Any,
    ) -> None:
        self.binary = str(binary or os.getenv("ENTIRE_GRAPH_BIN", "entire-graph"))
        self.profile = profile
        self.max_context_bytes = int(
            max_context_bytes
            if max_context_bytes is not None
            else os.getenv("ENTIRE_MAX_CONTEXT_BYTES", 100_000_000)
        )
        self.timeout_seconds = timeout_seconds
        self.git_binary = git_binary

        env_root = corpus_root or os.getenv("ENTIRE_CORPUS_ROOT")
        if env_root:
            self._root = Path(env_root)
            self._root.mkdir(parents=True, exist_ok=True)
            self._owns_root = False
        else:
            self._root = Path(tempfile.mkdtemp(prefix="entire-mem-"))
            self._owns_root = not keep_corpus

        # Per-user ingest buffers: user_id -> list[(timestamp, [messages])]
        self._buffers: dict[str, list[tuple[int | None, list[dict]]]] = {}
        # Users whose corpus has been materialized + indexed.
        self._built: set[str] = set()
        # session stem -> created_at ISO, per user
        self._session_dates: dict[str, dict[str, str]] = {}
        # Per-user build lock so concurrent first-searches don't double-build.
        self._locks: dict[str, asyncio.Lock] = {}

    # -- context manager --------------------------------------------------

    async def __aenter__(self) -> "EntireMemoryClient":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    async def close(self) -> None:
        if self._owns_root and self._root.exists():
            shutil.rmtree(self._root, ignore_errors=True)

    # -- helpers ----------------------------------------------------------

    def _user_key(self, user_id: str) -> str:
        return hashlib.sha256(str(user_id).encode("utf-8")).hexdigest()[:16]

    def _corpus_dir(self, user_id: str) -> Path:
        return self._root / self._user_key(user_id) / "corpus"

    def _cache_dir(self, user_id: str) -> Path:
        return self._root / self._user_key(user_id) / "cache"

    def _lock(self, user_id: str) -> asyncio.Lock:
        lock = self._locks.get(user_id)
        if lock is None:
            lock = asyncio.Lock()
            self._locks[user_id] = lock
        return lock

    def _run(self, args: list[str], *, cwd: Path | None = None) -> subprocess.CompletedProcess:
        return subprocess.run(
            args,
            cwd=str(cwd) if cwd else None,
            capture_output=True,
            text=True,
            timeout=self.timeout_seconds,
            check=False,
        )

    # -- add --------------------------------------------------------------

    async def add(
        self,
        messages: list[dict[str, str]],
        user_id: str,
        observation_date: str | None = None,
        timestamp: int | None = None,
        custom_instructions: str | None = None,
        metadata: dict | None = None,
    ) -> dict | None:
        """Buffer a conversation chunk. Materialization + indexing is deferred
        to the first ``search`` for this user (the ingest→search phase seam),
        mirroring how the Mem0 arm does all its extraction work at add-time —
        just batched. Returns a Mem0-shaped ``{"results": [...]}`` ack."""
        ts = timestamp
        if ts is None and observation_date:
            try:
                d = datetime.strptime(observation_date, "%Y-%m-%d").replace(tzinfo=timezone.utc)
                ts = int(d.timestamp())
            except ValueError:
                ts = None

        norm = [
            {"role": str(m.get("role", "user")), "content": str(m.get("content", ""))}
            for m in (messages or [])
        ]
        self._buffers.setdefault(user_id, []).append((ts, norm))
        # New content invalidates any prior build.
        self._built.discard(user_id)

        return {
            "results": [
                {"id": f"{self._user_key(user_id)}-{i}", "memory": m["content"], "event": "ADD"}
                for i, m in enumerate(norm)
            ]
        }

    # -- materialize + index ---------------------------------------------

    def _materialize_and_index(self, user_id: str) -> None:
        """Write buffered sessions to a git corpus and run `entire-graph index`.
        Synchronous; call inside asyncio.to_thread."""
        corpus = self._corpus_dir(user_id)
        cache = self._cache_dir(user_id)
        if corpus.exists():
            shutil.rmtree(corpus, ignore_errors=True)
        corpus.mkdir(parents=True, exist_ok=True)
        cache.mkdir(parents=True, exist_ok=True)

        session_dates: dict[str, str] = {}
        buf = self._buffers.get(user_id, [])

        # Group into sessions: a new session starts whenever the timestamp
        # changes from the previous chunk (chunks within a session share the
        # session epoch in every mem0 benchmark ingest loop).
        sessions: list[tuple[str, int | None, list[dict]]] = []
        prev_ts: object = object()  # sentinel distinct from any timestamp
        for ts, msgs in buf:
            if ts != prev_ts or not sessions:
                sessions.append((f"session_{len(sessions):04d}", ts, []))
                prev_ts = ts
            sessions[-1][2].extend(msgs)

        # INGEST GRANULARITY (env-gated; default "session" = shipped bytes).
        #   session          one document per session (baseline)
        #   turn             one document per add() payload
        #   wN               one document per N consecutive add() payloads
        #   <fine>+session   MULTI-RESOLUTION: the fine documents AND the whole
        #                    session document, i.e. parent-document indexing.
        # eg's ranker returns at most one hit per file, so the baseline merge of
        # a session's add() payloads back into a single .md structurally caps eg
        # at (n_sessions) evidence items. Multi-resolution lifts that cap while
        # KEEPING the coarse session document, so the retrieved set is a strict
        # superset of the baseline's and no query can lose evidence.
        # Question-blind, deterministic, 0-LLM, derived only from add() payloads.
        gran = os.getenv("EG_INGEST_GRANULARITY", "session")
        for session_id, ts, msgs in sessions:
            safe_session_id = _safe_id(session_id)
            date_str = _iso(ts)
            _hyb = (gran or "").endswith("+session")
            _fine = gran[:-len("+session")] if _hyb else gran
            if _fine == "turn":
                groups = [(f"{safe_session_id}_p{i:03d}", [m])
                          for i, m in enumerate(msgs)]
            elif re.fullmatch(r"w\d+", _fine or ""):
                _n = max(1, int(_fine[1:]))
                groups = [(f"{safe_session_id}_p{i // _n:03d}", msgs[i:i + _n])
                          for i in range(0, len(msgs), _n)]
            else:
                groups = [(safe_session_id, msgs)]
            if _hyb:
                groups = groups + [(safe_session_id, msgs)]
            for _stem, _msgs in groups:
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
                for m in _msgs:
                    body.append(f"## {_one_line(m['role'])}: {_one_line(m['content'])}")
                    body.append("")
                (corpus / f"{_stem}.md").write_text(
                    "\n".join(body), encoding="utf-8"
                )
                session_dates[_stem] = date_str

        # Make it a git repo so `--head` resolves (entire-graph indexes HEAD).
        env = {
            **os.environ,
            "GIT_AUTHOR_NAME": "entire-bench",
            "GIT_AUTHOR_EMAIL": "bench@entire.local",
            "GIT_COMMITTER_NAME": "entire-bench",
            "GIT_COMMITTER_EMAIL": "bench@entire.local",
        }
        subprocess.run([self.git_binary, "init", "-q"], cwd=str(corpus), env=env, check=True)
        subprocess.run([self.git_binary, "add", "-A"], cwd=str(corpus), env=env, check=True)
        subprocess.run(
            [self.git_binary, "commit", "-q", "-m", "ingest", "--allow-empty"],
            cwd=str(corpus), env=env, check=True,
        )

        # Optional PURE-DETERMINISTIC consolidation layer (0 LLM / 0 network):
        # writes derived _timeline / _current_facts / _superseded markdown into
        # the corpus so eg's EXISTING retrieval surfaces current + time-ordered
        # facts. Committed BEFORE the index step so the artifacts get indexed.
        # Gated by EG_CONSOLIDATE=1; when unset the corpus + index are
        # BYTE-IDENTICAL to the current baseline (this arm is untouched).
        if os.getenv("EG_CONSOLIDATE") == "1":
            try:
                from .consolidate import consolidate

                sessions_payload = [
                    {"session_id": sid, "date": _iso(ts), "messages": msgs}
                    for (sid, ts, msgs) in sessions
                ]
                if consolidate(corpus, sessions_payload):
                    subprocess.run(
                        [self.git_binary, "add", "-A"],
                        cwd=str(corpus), env=env, check=True,
                    )
                    subprocess.run(
                        [self.git_binary, "commit", "-q", "-m", "consolidate",
                         "--allow-empty"],
                        cwd=str(corpus), env=env, check=True,
                    )
            except Exception as exc:  # never break the baseline retrieval path
                logger.warning("consolidate failed: %s", str(exc)[:300])

        idx = self._run([
            self.binary, "index",
            "--repo", str(corpus),
            "--profile", self.profile,
            "--cache-dir", str(cache),
            "--format", "json",
        ])
        if idx.returncode != 0:
            logger.warning("entire-graph index rc=%s stderr=%s", idx.returncode, idx.stderr[:400])

        self._session_dates[user_id] = session_dates
        self._built.add(user_id)

    # -- search -----------------------------------------------------------

    async def search(
        self,
        query: str,
        user_id: str,
        top_k: int = 200,
        rerank: bool = False,
        score_debug: bool = False,
    ) -> list[dict]:
        """Retrieve up to ``top_k`` memory snippets for ``query``. Returns a
        Mem0-shaped list of ``{"memory","score","id","created_at"}`` sorted by
        score descending."""
        if user_id not in self._buffers:
            # INVARIANT: mirrors cognee/graphiti/letta/supermemory_client.py.
            # Silently returning [] turns a missing buffer into an empty
            # context, which scores as a capability miss instead of the
            # infrastructure error it is. This client was the only one still
            # failing silently, so the same fault was loud for every
            # competitor arm and mute for this one.
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

        corpus = self._corpus_dir(user_id)
        cache = self._cache_dir(user_id)

        proc = await asyncio.to_thread(
            self._run,
            [
                self.binary, "search",
                "--repo", str(corpus),
                "--query", query,
                "--format", "json",
                "--top-k", str(top_k),
                "--profile", self.profile,
                "--head",
                "--index-all-files",
                "--max-context-bytes", str(self.max_context_bytes),
                "--cache-dir", str(cache),
            ] + (["--deep"] if os.getenv("EG_DEEP") == "1" else []),
        )
        if proc.returncode != 0:
            logger.warning("entire-graph search rc=%s stderr=%s", proc.returncode, proc.stderr[:400])
            return []
        try:
            payload = json.loads(proc.stdout or "{}")
        except json.JSONDecodeError as exc:
            logger.warning("entire-graph search non-JSON output: %s", str(exc)[:200])
            return []

        session_dates = self._session_dates.get(user_id, {})

        mode = os.getenv("EG_SESSION_EXPAND", "")
        if mode == "1":
            # SESSION-EXPANSION (env-gated): eg's lexical ranking selects WHICH
            # sessions are relevant (one top chunk per session), but a single
            # chunk buries the atomic user statements a counting/aggregation
            # question needs (query "doctors" never lexically matches the user's
            # "ENT specialist"/"dermatologist" turns). So for every session eg
            # surfaced ANY hit from, emit the FULL session text as one memory,
            # scored by that session's best hit. Retrieval granularity = session;
            # question-blind, deterministic, no answer-key leakage. Unset =>
            # byte-identical to the per-snippet baseline path.
            results = self._session_expand(payload, corpus, session_dates)
        elif mode in ("2", "hybrid"):
            # HYBRID granularity (env-gated): return BOTH the precise atomic
            # chunks (the baseline retrieval -- keeps the single most-relevant/
            # latest sentence per hit intact, which knowledge-update & temporal
            # questions need) AND the full-session memories (recall, which the
            # counting/aggregation questions need). Pure expansion REPLACED the
            # precise chunk with the whole session, reintroducing superseded
            # values ($350k vs $400k) and planned-vs-actual noise -> KU/temporal
            # regressions. Hybrid keeps the clean chunk as its OWN memory AND
            # adds the session, so neither signal is lost. Atomic chunks lead
            # (highest relevance first); sessions follow for completeness.
            # Question-blind, deterministic, no answer-key leakage.
            results = self._atomic_results(payload, corpus, session_dates)
            results += self._session_expand(payload, corpus, session_dates)
        else:
            results = self._atomic_results(payload, corpus, session_dates)
        if os.getenv("EG_CHRONO_ORDER") == "1":
            results.sort(key=lambda x: x.get("created_at") or "")  # present chronologically (stable: score within date)
        _pd = payload.get("preference_directive")
        if isinstance(_pd, dict):
            _hdr = (_pd.get("header") or "").strip()
            if _hdr:
                results.insert(0, {"memory": _hdr, "score": float("inf"), "id": "preference_directive"})
        return results

    def _atomic_results(self, payload: dict, corpus: Path,
                        session_dates: dict) -> list[dict]:
        """Per-chunk memories (the baseline retrieval path): one memory per
        eg hit, using the engine snippet (or reconstructed from source),
        sorted by score descending."""
        results: list[dict] = []
        for r in payload.get("results", []):
            if not isinstance(r, dict):
                continue
            file_path = r.get("file_path") or r.get("path") or ""
            start_line = r.get("start_line", r.get("snippet_start_line", 0))
            score = float(r.get("score", 0.0) or 0.0)
            memory_text = r.get("snippet")
            if not memory_text:
                memory_text = self._read_snippet(corpus, file_path, r)
            memory_text = (memory_text or "").strip()
            if not memory_text:
                continue
            stem = Path(str(file_path)).stem
            entry: dict[str, Any] = {
                "memory": memory_text,
                "score": score,
                "id": f"{file_path}:{start_line}",
            }
            created = session_dates.get(stem)
            if created:
                entry["created_at"] = created
            results.append(entry)
        results.sort(key=lambda x: x.get("score", 0.0), reverse=True)
        return results

    def _session_expand(self, payload: dict, corpus: Path,
                        session_dates: dict) -> list[dict]:
        """Collapse eg's per-chunk hits to one FULL-session memory per hit
        session, scored by that session's best-scoring chunk (score-desc)."""
        best: dict[str, float] = {}
        order: list[str] = []
        for r in payload.get("results", []):
            if not isinstance(r, dict):
                continue
            fp = r.get("file_path") or r.get("path") or ""
            stem = Path(str(fp)).stem
            if not stem:
                continue
            sc = float(r.get("score", 0.0) or 0.0)
            if stem not in best:
                best[stem] = sc
                order.append(stem)
            elif sc > best[stem]:
                best[stem] = sc
        ranked = sorted(order, key=lambda s: best[s], reverse=True)
        # Optional selectivity cap: keep only the top-N ranked sessions, so the
        # arm stays genuinely SELECTIVE (not a full-history dump) on small
        # haystacks. Unset => keep every hit session.
        try:
            cap = int(os.getenv("EG_SESSION_EXPAND_CAP", "0"))
        except ValueError:
            cap = 0
        if cap > 0:
            ranked = ranked[:cap]
        results: list[dict] = []
        for stem in ranked:
            text = self._full_session_text(corpus, stem)
            if not text:
                continue
            entry: dict[str, Any] = {
                "memory": text, "score": best[stem], "id": f"{stem}.md",
            }
            created = session_dates.get(stem)
            if created:
                entry["created_at"] = created
            results.append(entry)
        return results

    @staticmethod
    def _full_session_text(corpus: Path, stem: str) -> str:
        """Full session body (## user/assistant turns), YAML frontmatter and the
        '# Session' heading stripped, blank lines collapsed."""
        try:
            raw = (corpus / f"{stem}.md").read_text(encoding="utf-8", errors="replace")
        except OSError:
            return ""
        lines = raw.split("\n")
        i = 0
        if lines and lines[0].strip() == "---":
            i = 1
            while i < len(lines) and lines[i].strip() != "---":
                i += 1
            i += 1  # skip closing '---'
        out = [
            ln.strip() for ln in lines[i:]
            if ln.strip() and not ln.strip().startswith("# Session")
        ]
        return "\n".join(out).strip()

    @staticmethod
    def _read_snippet(corpus: Path, file_path: str, r: dict) -> str:
        try:
            lines = (corpus / file_path).read_text(encoding="utf-8", errors="replace").split("\n")
        except OSError:
            return ""
        start = r.get("snippet_start_line", r.get("start_line", 1))
        end = r.get("snippet_end_line", start)
        try:
            start = max(1, int(start))
            end = max(start, int(end))
        except (TypeError, ValueError):
            return ""
        return "\n".join(lines[start - 1 : end])

    # -- delete -----------------------------------------------------------

    async def delete_user(self, user_id: str) -> bool:
        self._buffers.pop(user_id, None)
        self._built.discard(user_id)
        self._session_dates.pop(user_id, None)
        base = self._root / self._user_key(user_id)
        if base.exists():
            await asyncio.to_thread(shutil.rmtree, base, True)
        return True

    # -- user profile (v2: sidecar, env-gated by EG_PROFILE) --------------

    def _compute_profile(self, user_id: str) -> dict:
        """Deterministically derive a COMPACT current-facts profile from the
        buffered sessions for ``user_id`` (0 LLM / 0 network / 0 randomness).

        Reuses the supersession logic in ``consolidate.py`` (``_extract`` +
        ``_supersede``) but returns the result as a **dict** instead of writing
        derived markdown into the indexed corpus. That is the whole point of v2:
        the answerer gets resolved current facts through the harness's SEPARATE
        ``user_profile`` prompt section, so retrieval (which groups by file) is
        never polluted by new "parent" files -- the v1 regression cause."""
        from .consolidate import _coerce_sessions, _extract, _supersede

        buf = self._buffers.get(user_id, [])
        # Group buffered chunks into sessions exactly as _materialize_and_index:
        # a new session starts whenever the timestamp changes.
        sessions: list[list] = []
        prev_ts: object = object()
        for ts, msgs in buf:
            if ts != prev_ts or not sessions:
                sessions.append([f"session_{len(sessions):04d}", ts, []])
                prev_ts = ts
            sessions[-1][2].extend(msgs)

        payload = [
            {"session_id": sid, "date": _iso(ts), "messages": msgs}
            for (sid, ts, msgs) in sessions
        ]
        try:
            sess = _coerce_sessions(payload)
            _tl_lines, facts = _extract(sess)
            current_lines, superseded_lines = _supersede(facts)
        except Exception as exc:  # never break the run over profile derivation
            logger.warning("profile derivation failed: %s", str(exc)[:300])
            return {}

        def _clean(line: str) -> str:
            return line[2:].strip() if line.startswith("- ") else line.strip()

        fact_cap = int(os.getenv("EG_PROFILE_FACT_CAP", "40"))
        tl_cap = int(os.getenv("EG_PROFILE_TIMELINE_CAP", "30"))
        # current_facts: latest value per (entity, attribute) -- the synthesis
        # lever (each line already encodes "supersedes <old> from <date>").
        current_facts = [_clean(x) for x in current_lines][:fact_cap]
        # timeline_summary: the COMPACT change-log (only attributes that were
        # updated over time) -- the knowledge-update / temporal signal, minus
        # the raw-sentence noise that would just bloat single-session prompts.
        changes = [_clean(x) for x in superseded_lines][:tl_cap]

        profile: dict[str, Any] = {}
        if current_facts:
            profile["current_facts"] = current_facts
        if changes:
            profile["timeline_summary"] = " | ".join(changes)
        return profile

    async def get_user_profile(self, user_id: str) -> dict | None:
        """Return a COMPACT consolidated profile dict for ``user_id`` (or None).

        Wired to the harness's arm-neutral ``--user-profile`` hook
        (``run.py`` -> ``get_answer_generation_prompt(..., user_profile=...)``),
        which renders it as its OWN prompt section, SEPARATE from the retrieved
        memories. Env-gated by ``EG_PROFILE=1``; unset => returns None =>
        byte-identical to the current baseline retrieval path. The profile is
        also persisted to a SIDECAR json OUTSIDE the indexed git corpus (a
        sibling of ``corpus/``; never ``git add``ed), so the code graph / index
        never sees a derived file. Pure-deterministic; 0 LLM / 0 network."""
        if os.getenv("EG_PROFILE") != "1":
            return None
        if user_id not in self._buffers:
            return None

        profile = await asyncio.to_thread(self._compute_profile, user_id)

        # Persist to a sidecar OUTSIDE corpus/ (sibling of corpus/ + cache/).
        try:
            base = self._root / self._user_key(user_id)
            base.mkdir(parents=True, exist_ok=True)
            (base / "profile.json").write_text(
                json.dumps(profile, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
        except OSError as exc:
            logger.warning("profile sidecar write failed: %s", str(exc)[:200])

        return profile or None


def make_memory_client(
    backend: str,
    *,
    host: str | None = None,
    api_key: str | None = None,
    rpm: int = 60,
    **kwargs: Any,
):
    """Factory that preserves the exact Mem0 path for oss/cloud and swaps in
    entire-graph only for ``backend == "entire"``. Used at the three run.py
    client-construction sites so every other harness stage is byte-identical
    across arms."""
    if backend == "cognee":
        from .cognee_client import CogneeClient
        return CogneeClient(rpm=rpm, **kwargs)
    if backend == "graphiti":
        from .graphiti_client import GraphitiClient
        return GraphitiClient(rpm=rpm, **kwargs)
    if backend == "letta":
        from .letta_client import LettaClient
        return LettaClient(rpm=rpm, **kwargs)
    if backend == "supermemory":
        from .supermemory_client import SupermemoryClient
        return SupermemoryClient(rpm=rpm, **kwargs)
    if backend == "graphify":
        from .graphify_client import GraphifyClient
        return GraphifyClient(rpm=rpm, **kwargs)
    if backend == "cmm":
        from .cmm_client import CmmClient
        return CmmClient(rpm=rpm, **kwargs)
    if backend == "bm25":
        from .bm25_client import Bm25Client
        return Bm25Client(rpm=rpm, **kwargs)
    if backend == "entire":
        return EntireMemoryClient(rpm=rpm, **kwargs)
    # Import lazily so entire-only runs don't require the mem0 client.
    from .mem0_client import Mem0Client

    return Mem0Client(mode=backend, host=host, api_key=api_key, rpm=rpm)
