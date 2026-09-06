"""Graphify prose-memory bridge (runs inside the Graphify venv).

Self-contained port of ``graphify_parity/graphify_bridge.py`` for the mem0
harness, with ONE deliberate difference: retrieval is NODE-level, not
file-level.  The parity bridge de-duplicated hits by ``source_file`` because
that benchmark scored *file locators*; a memory benchmark scores *evidence
items*, and file de-duplication would structurally cap Graphify at
(n_sessions) memories no matter what ``top_k`` the harness asks for.  Ranking,
seeding, traversal and node text are all Graphify's own
(``graphify.serve._score_query`` / ``_pick_seeds`` / ``_bfs``); nothing here
re-ranks or rewrites them.

Both actions read their request from a JSON file (never argv) and write a
single JSON object to stdout.
"""

from __future__ import annotations

import json
import os
import sys
import time
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import Any


@contextmanager
def _working_directory(path: Path) -> Iterator[None]:
    previous = Path.cwd()
    os.chdir(path)
    try:
        yield
    finally:
        os.chdir(previous)


def _activate_graphify(source: Path) -> None:
    source_string = str(source.resolve())
    if source_string not in sys.path:
        sys.path.insert(0, source_string)


def _write_graph(path: Path, graph: Any) -> None:
    from networkx.readwrite import json_graph

    path.parent.mkdir(parents=True, exist_ok=True)
    document = json_graph.node_link_data(graph, edges="links")
    path.write_text(
        json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        + "\n",
        encoding="utf-8",
    )


def _read_graph(path: Path) -> Any:
    from networkx.readwrite import json_graph

    document = json.loads(path.read_text(encoding="utf-8"))
    if "links" not in document and "edges" in document:
        document = {**document, "links": document["edges"]}
    return json_graph.node_link_graph(document, edges="links")


def build_structural(graphify_source: Path, corpus: Path, graph_path: Path) -> dict:
    _activate_graphify(graphify_source)
    import networkx as nx
    from graphify.extractors.markdown import extract_markdown

    started = time.perf_counter()
    graph = nx.Graph()
    markdown_paths = sorted(
        p.relative_to(corpus) for p in corpus.rglob("*.md") if p.is_file()
    )
    with _working_directory(corpus):
        for relative_path in markdown_paths:
            extracted = extract_markdown(relative_path)
            if extracted.get("error"):
                raise RuntimeError(f"markdown extraction failed for {relative_path}")
            for node in extracted.get("nodes", []):
                attributes = dict(node)
                node_id = attributes.pop("id")
                graph.add_node(node_id, **attributes)
            for edge in extracted.get("edges", []):
                attributes = dict(edge)
                source = attributes.pop("source")
                target = attributes.pop("target")
                graph.add_edge(source, target, **attributes)
    _write_graph(graph_path, graph)
    return {
        "files": len(markdown_paths),
        "nodes": graph.number_of_nodes(),
        "edges": graph.number_of_edges(),
        "output_bytes": graph_path.stat().st_size,
        "build_seconds": time.perf_counter() - started,
    }


def _line_number(value: Any) -> int:
    import re

    text = str(value or "L1")
    match = re.match(r"^L?(\d+)", text)
    return int(match.group(1)) if match else 1


def retrieve(
    graphify_source: Path, corpus: Path, graph_path: Path, query: str, *, top_k: int
) -> dict:
    started = time.perf_counter()
    graph = _read_graph(graph_path)
    _activate_graphify(graphify_source)
    from graphify.serve import _bfs, _pick_seeds, _query_terms, _score_query

    scores = _score_query(graph, _query_terms(query), collect_per_term_seeds=True)
    seeds = _pick_seeds(
        scores.ranked, G=graph, best_seed_by_term=scores.best_seed_by_term
    )
    traversed: set = set()
    if seeds:
        nodes, _ = _bfs(graph, seeds, 3)
        traversed = set(nodes)

    results: list[dict] = []
    for score, node_id in scores.ranked:
        if traversed and node_id not in traversed:
            continue
        data = graph.nodes[node_id]
        source_file = str(data.get("source_file") or "")
        label = str(data.get("label", "") or "")
        results.append(
            {
                "rank": len(results) + 1,
                "score": float(score),
                "file_path": source_file,
                "line": _line_number(data.get("source_location")),
                "node_id": str(node_id),
                "label": label,
                "kind": str(data.get("kind", data.get("type", "")) or ""),
            }
        )
        if len(results) >= top_k:
            break
    return {
        "results": results,
        "stats": {
            "nodes_considered": len(scores.ranked),
            "nodes_traversed": len(traversed),
            "query_latency_ms": round((time.perf_counter() - started) * 1000, 3),
        },
    }


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if len(argv) != 2:
        print(json.dumps({"error": "usage: bridge <build|retrieve> <request.json>"}))
        return 2
    action, request_path = argv
    request = json.loads(Path(request_path).read_text(encoding="utf-8"))
    graphify_source = Path(request["graphify_source"])
    corpus = Path(request["corpus"])
    graph_path = Path(request["graph"])
    if action == "build":
        payload = build_structural(graphify_source, corpus, graph_path)
    elif action == "retrieve":
        payload = retrieve(
            graphify_source,
            corpus,
            graph_path,
            str(request["query"]),
            top_k=int(request.get("top_k", 200)),
        )
    else:
        print(json.dumps({"error": f"unknown action {action}"}))
        return 2
    json.dump(payload, sys.stdout, ensure_ascii=False)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
