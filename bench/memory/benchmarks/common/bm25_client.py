"""
BM25 (lexical-only) Memory Client
=================================

Duck-typed stand-in for ``Mem0Client`` backed by **standard BM25 over the raw
conversation turns** -- no graph, no embeddings, no extraction, no LLM.

Why this arm exists.  Two recent results argue that the structure other arms
build is unnecessary:

  * *Better Call Grep* (ISSTA 2026, arXiv:2601.23254) -- naive lexical
    retrieval is "comparable to sophisticated graph-based baselines" for
    repository-level code completion.
  * arXiv:2608.12888 (13 Aug 2026) -- a lexical, TURN-granularity index over the
    *unmodified* conversation archive, building no semantic structure at all,
    tops MemoryAgentBench (58.2 vs HippoRAG 2's 53.2) and reaches 93.2 on
    LongMemEval-S.

This client is the honest strong form of that claim, not a strawman:
  * STORE    the same session ``<session>.md`` documents every other arm
             ingests (byte-identical to the entire-graph arm's corpus; see
             ``_materialize_and_index``), then split at ``## `` headings into
             ONE INDEXED UNIT PER CONVERSATIONAL TURN -- the granularity
             arXiv:2608.12888 found effective. Units are parsed back OFF DISK
             from the materialized corpus, so the indexed text is provably the
             same bytes, never a privately-prepared variant.
  * RETRIEVE ``rank_bm25.BM25Okapi`` (the reference Python implementation),
             k1=1.2, b=0.75, Okapi IDF floor epsilon=0.25 (library default).
             Tokenisation is the conventional Lucene/Anserini pipeline:
             lowercase -> ``[a-z0-9]+`` -> NLTK English stopword removal ->
             Snowball(english) stemming via PyStemmer, the same stemmer Lucene
             ships. No re-ranking, no query expansion, no fusion, no tuning.

Fairness contract (identical to the entire / mem0 / cognee / graphify / cmm arms):
  * ingest bytes IDENTICAL to the entire-graph arm (same session grouping, same
    ``<session>.md`` layout, md5-verifiable);
  * 0 LLM and 0 network at ingest -- this arm is in the deterministic class;
  * ``search`` returns ``{"memory","score","id","created_at"}``;
  * ``search`` RAISES on a missing buffer (never a silent ``[]`` -- that defect
    cost a competitor 13.77 points earlier in this campaign);
  * returned text is the turn line with its markdown ``#`` prefix stripped,
    i.e. the same surface form ``cmm_client`` returns, so retrieved-context
    char counts are comparable across arms;
  * state roots under ``$HOME`` (``/tmp`` is wiped by systemd on boot).

Ranking is BM25's alone. Nothing here re-orders, rewrites or truncates it, with
two conventional exceptions, both stated up front and both standard IR practice:
  1. documents sharing no tokenized query term are not returned -- Lucene never
     places them in a result set either;
  2. if stopword removal empties the query, the un-stopworded tokens are used,
     so a question like "who is he?" still retrieves.

Env:
  BM25_STATE_ROOT   per-run state root (default: $HOME/memarms/state/bm25_corpora/<pid>)
  BM25_K1           BM25 k1 (default 1.2)
  BM25_B            BM25 b  (default 0.75)
  BM25_STEM         "1" (default) Snowball stemming, "0" disables
  BM25_STOPWORDS    "1" (default) NLTK English stopword removal, "0" disables
"""

from __future__ import annotations

import asyncio
import hashlib
import logging
import os
import re
import shutil
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from rank_bm25 import BM25Okapi

logger = logging.getLogger(__name__)

try:  # Snowball(english) == Lucene's EnglishStemmer. Optional, never fatal.
    import Stemmer as _pystemmer
except ImportError:  # pragma: no cover
    _pystemmer = None


# The NLTK English stopword list, flattened to the alphanumeric fragments this
# tokeniser actually produces (``you're`` -> ``you`` + ``re``). Verbatim list;
# nothing added or removed for this benchmark.
_STOPWORDS = frozenset("""
i me my myself we our ours ourselves you your yours yourself yourselves he him
his himself she her hers herself it its itself they them their theirs
themselves what which who whom this that these those am is are was were be been
being have has had having do does did doing a an the and but if or because as
until while of at by for with about against between into through during before
after above below to from up down in out on off over under again further then
once here there when where why how all any both each few more most other some
such no nor not only own same so than too very s t can will just don should now
d ll m o re ve y ain aren couldn didn doesn hadn hasn haven isn ma mightn mustn
needn shan shouldn wasn weren won wouldn
""".split())

_TOKEN_RE = re.compile(r"[a-z0-9]+")


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


class Bm25Client:
    def __init__(self, rpm: int = 60, **kwargs: Any) -> None:
        self.k1 = float(os.getenv("BM25_K1", "1.2"))
        self.b = float(os.getenv("BM25_B", "0.75"))
        self.use_stopwords = os.getenv("BM25_STOPWORDS", "1") != "0"
        self.use_stem = os.getenv("BM25_STEM", "1") != "0" and _pystemmer is not None
        if os.getenv("BM25_STEM", "1") != "0" and _pystemmer is None:
            logger.warning("PyStemmer unavailable -- BM25 arm running WITHOUT stemming")
        self._stemmer = _pystemmer.Stemmer("english") if self.use_stem else None

        root = os.getenv("BM25_STATE_ROOT") or os.path.join(
            os.path.expanduser("~"), "memarms", "state",
            "bm25_corpora", str(os.getpid()),
        )
        self._root = Path(root)
        self._root.mkdir(parents=True, exist_ok=True)

        self._buffers: dict[str, list[tuple[int | None, list[dict]]]] = {}
        self._built: set[str] = set()
        self._index: dict[str, BM25Okapi] = {}
        self._units: dict[str, list[dict]] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    # -- context manager ---------------------------------------------------

    async def __aenter__(self) -> "Bm25Client":
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

    def _lock(self, user_id: str) -> asyncio.Lock:
        lk = self._locks.get(user_id)
        if lk is None:
            lk = asyncio.Lock()
            self._locks[user_id] = lk
        return lk

    def _tokenize(self, text: str) -> list[str]:
        toks = _TOKEN_RE.findall(text.lower())
        if self.use_stopwords:
            kept = [t for t in toks if t not in _STOPWORDS]
            # A query made only of stopwords ("who is he?") must still retrieve.
            toks = kept if kept else toks
        if self._stemmer is not None:
            toks = self._stemmer.stemWords(toks)
        return toks

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
        """Write buffered sessions as ``<session>.md`` (BYTE-IDENTICAL to the
        entire-graph arm's corpus), then read those files back off disk and
        index one BM25 document per conversational turn."""
        corpus = self._corpus_dir(user_id)
        if corpus.exists():
            shutil.rmtree(corpus, ignore_errors=True)
        corpus.mkdir(parents=True, exist_ok=True)

        buf = self._buffers.get(user_id, [])
        sessions: list[tuple[str, int | None, list[dict]]] = []
        prev_ts: object = object()
        for ts, msgs in buf:
            if ts != prev_ts or not sessions:
                sessions.append((f"session_{len(sessions):04d}", ts, []))
                prev_ts = ts
            sessions[-1][2].extend(msgs)

        session_dates: dict[str, str] = {}
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

        # TURN GRANULARITY: parse the units back out of the files just written,
        # so the indexed text is the corpus bytes and cannot drift from them.
        units: list[dict] = []
        for path in sorted(corpus.glob("*.md")):
            stem = path.stem
            lines = path.read_text(encoding="utf-8").split("\n")
            turn_idx = 0
            for ln in lines:
                if not ln.startswith("## "):
                    continue  # frontmatter, blank lines, the `# Session` title
                text = ln[3:].strip()
                if not text:
                    continue
                units.append({
                    "text": text,
                    "id": f"{stem}:t{turn_idx:04d}",
                    "created_at": session_dates.get(stem, ""),
                })
                turn_idx += 1

        if not units:
            raise RuntimeError(
                f"BM25_EMPTY_INDEX: 0 indexable turns for user_id={user_id} "
                f"(files={len(list(corpus.glob('*.md')))})"
            )

        tokenized = [self._tokenize(u["text"]) for u in units]
        self._index[user_id] = BM25Okapi(tokenized, k1=self.k1, b=self.b)
        self._units[user_id] = units
        logger.info(
            "bm25 index user=%s sessions=%s units(turns)=%s avgdl=%.1f k1=%s b=%s "
            "stem=%s stopwords=%s",
            user_id, len(sessions), len(units),
            sum(len(t) for t in tokenized) / max(1, len(tokenized)),
            self.k1, self.b, self.use_stem, self.use_stopwords,
        )
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
            # INVARIANT (see cognee/letta/entire/graphify/cmm clients): never
            # return [] for missing in-memory state -- a silent empty context
            # scores as a capability miss instead of the infrastructure error
            # it is.
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

        bm25 = self._index[user_id]
        units = self._units[user_id]
        tokens = self._tokenize(query)
        scores = await asyncio.to_thread(bm25.get_scores, tokens)

        # A BM25 score's sign does not indicate lexical overlap. Okapi IDF can
        # be negative when a term appears in most documents, so valid matches
        # can rank below the zero assigned to documents with no matching term.
        # Select candidates by token overlap before ranking and applying top_k.
        query_terms = frozenset(tokens)
        candidates = (
            i for i, frequencies in enumerate(bm25.doc_freqs)
            if query_terms.intersection(frequencies)
        )
        order = sorted(candidates, key=lambda i: float(scores[i]), reverse=True)
        results: list[dict] = []
        for i in order[: int(top_k)]:
            score = float(scores[i])
            u = units[i]
            entry: dict[str, Any] = {
                "memory": u["text"],
                "score": score,
                "id": u["id"],
            }
            if u.get("created_at"):
                entry["created_at"] = u["created_at"]
            results.append(entry)
        if not results:
            logger.warning(
                "BM25_ZERO_HITS user=%s query=%r -- no turn shares a query term",
                user_id, query[:120],
            )
        return results

    # -- delete ------------------------------------------------------------

    async def delete_user(self, user_id: str) -> bool:
        self._buffers.pop(user_id, None)
        self._built.discard(user_id)
        self._index.pop(user_id, None)
        self._units.pop(user_id, None)
        base = self._base(user_id)
        if base.exists():
            await asyncio.to_thread(shutil.rmtree, base, True)
        return True
