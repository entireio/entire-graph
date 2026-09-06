"""Version-scoped structural evidence and conservative registered-check fallback."""
from collections import defaultdict, deque

from .contracts import digest


def binding(symbol):
    return f"{symbol['file_path']}:{symbol.get('qualified_name', symbol['name'])}"


def confirmed_edge(edge):
    # Entire Graph 0.4 distinguishes bound resolution from name-only heuristics.
    # Confidence is retained as reported; it is not a probability of runtime use.
    return (edge.get("resolution") in {"exact", "package", "import_resolved"}
            and not edge.get("warning_codes") and bool(edge.get("evidence")))


def select(requirements, analysis, strategy="graph", max_depth=8):
    selected, reasons, paths, unresolved = set(), defaultdict(list), [], set()
    diagnostics = []
    active = [r for r in requirements if r.status == "confirmed_active"]
    unresolved.update(r.key for r in requirements if r.status in ("proposed", "unresolved"))
    changed_files = {f["path"] for f in analysis["diff"].get("files", [])}
    partial = analysis["partial"]

    def diagnostic(code, detail, side=None, graph=None, **extra):
        nonlocal partial
        partial = True
        diagnostics.append({"code": code, "detail": detail, "side": side,
                            "commit_sha": graph["commit_sha"] if graph else None, **extra})

    for error in analysis.get("errors", []):
        diagnostic("graph_unavailable", error, origin="graph")
    for warning in analysis["diff"].get("warnings", []):
        diagnostic("semantic_diff_warning", warning, origin="graph")
    for r in active:
        if "base" not in r.applies_to or r.policy_changed:
            selected.add(r.key)
            reasons[r.key].append("New or revised requirement needs verification")
        if strategy == "all" or (strategy == "changed_file" and any(e.rsplit(":", 1)[0] in changed_files for e in r.entrypoints)):
            selected.add(r.key)
            reasons[r.key].append("All registered checks" if strategy == "all" else "Entrypoint definition file changed")
        if any(new.requirement_id == r.requirement_id and new.policy_changed and new.applies_to == ["head"]
               for new in active) and r.applies_to == ["base"]:
            selected.add(r.key)
            reasons[r.key].append("Baseline policy retained for an explicit candidate policy revision")
    for side, graph in analysis["versions"].items():
        for item in graph.get("diagnostics", []):
            diagnostic(item["code"], item["detail"], side, graph,
                       **{k: v for k, v in item.items() if k not in {"code", "detail"}})
        if graph.get("partial"):
            diagnostic("partial_snapshot", "Graph reports potentially incomplete parsing", side, graph,
                       origin="graph", provider_summary=graph.get("summary", {}))
        symbols, reverse = graph["symbols"], defaultdict(list)
        by_binding = defaultdict(list)
        for sid, symbol in symbols.items():
            by_binding[binding(symbol)].append(sid)
        for edge in graph["calls"]:
            internal = edge.get("from_id") in symbols and edge.get("to_id") in symbols
            if not internal or not confirmed_edge(edge):
                sites = edge.get("evidence", [])
                site = next((s for s in sites if s.get("kind") == "call_site"), {})
                diagnostic("unresolved_relationship" if not internal else "heuristic_relationship",
                           "Relationship needs source/test verification", side, graph, origin="graph",
                           file_path=site.get("file_path", symbols.get(edge.get("from_id"), {}).get("file_path")),
                           line=site.get("start_line"), resolution=edge.get("resolution"),
                           confidence=edge.get("confidence"), from_id=edge.get("from_id"), to_id=edge.get("to_id"))
            if internal:
                reverse[edge["to_id"]].append(edge)
        if strategy != "graph":
            continue
        roots = []
        for file in analysis["diff"].get("files", []):
            if not file.get("changes"):
                diagnostic("unmapped_file_change", "Changed file has no mapped semantic definitions", side, graph,
                           origin="graph", file_path=file["path"])
            for change in file.get("changes", []):
                name = change.get("name")
                line = change.get("before_start_line" if side == "base" else "after_start_line")
                candidates = [sid for sid, symbol in symbols.items() if symbol["file_path"] == file["path"]
                              and (symbol["name"] == name or symbol.get("qualified_name") == name)
                              and (line is None or symbol.get("start_line") == line)]
                if len(candidates) == 1:
                    roots.extend(candidates)
                else:
                    diagnostic("unmapped_symbol", "Changed definition is absent or ambiguous on this version", side, graph,
                               origin="graph", file_path=file["path"], line=line, symbol=name)
        targets = defaultdict(list)
        for r in active:
            if side not in r.applies_to:
                continue
            for entrypoint in r.entrypoints:
                matches = by_binding.get(entrypoint, [])
                if len(matches) == 1:
                    targets[matches[0]].append(r.key)
                else:
                    unresolved.add(r.key)
                    diagnostic("unresolved_binding", "Requirement entrypoint is absent or ambiguous", side, graph,
                               origin="registry", requirement_ref=r.key, entrypoint=entrypoint)
        for root in sorted(set(roots)):
            queue = deque([(root, [root], [])])
            seen = {root}
            while queue:
                node, nodes, edges = queue.popleft()
                for key in targets[node]:
                    selected.add(key)
                    quality = "confirmed_structural" if all(confirmed_edge(e) for e in edges) else "heuristic"
                    reasons[key].append(f"{'Resolved structural' if quality == 'confirmed_structural' else 'Heuristic'} impact on {side} at {graph['commit_sha'][:8]}")
                    record = {"commit_sha": graph["commit_sha"], "side": side,
                              "requirement_ref": key, "changed_symbol": root, "entrypoint": node,
                              "symbols": [symbols[s] for s in nodes], "edges": edges,
                              "status": "structural_path" if quality == "confirmed_structural" else "heuristic_path",
                              "evidence_quality": quality, "runtime_reachability": "requires_verification"}
                    record["path_id"] = digest(record)[:20]
                    paths.append(record)
                if len(edges) >= max_depth:
                    if any(e["from_id"] not in seen for e in reverse[node]):
                        diagnostic("traversal_truncated", "Call traversal reached its configured depth limit", side, graph,
                                   origin="selector", symbol=node)
                    continue
                for edge in sorted(reverse[node], key=lambda e: (not confirmed_edge(e), e["from_id"])):
                    caller = edge["from_id"]
                    if caller not in seen:
                        seen.add(caller)
                        queue.append((caller, nodes + [caller], edges + [edge]))
    if analysis.get("errors"):
        unresolved.update(r.key for r in active)
    if partial and not diagnostics:
        diagnostic("partial_analysis", "Analysis has an unspecified completeness gap", origin="graph")
    partial = partial or bool(unresolved)
    before_fallback = sorted(selected)
    fallback = strategy == "graph" and partial
    added = sorted({r.key for r in active} - selected) if fallback else []
    if fallback:
        for r in active:
            selected.add(r.key)
            reasons[r.key].append("Conservative fallback: run all registered checks because selection may be incomplete")
    return {"evidence_schema_version": "1.0", "selected_requirement_ids": sorted(selected),
            "selection_reasons": dict(reasons), "paths": paths, "path_ids": [p["path_id"] for p in paths],
            "unresolved_ids": sorted(unresolved), "not_selected_ids": sorted({r.key for r in active} - selected),
            "partial_analysis": partial, "traversal_limit": max_depth,
            "diagnostics": diagnostics, "selected_before_fallback": before_fallback,
            "fallback": {"mode": "all_registered" if fallback else "none", "added_requirement_ids": added,
                         "scope": "confirmed registered assertions only; not complete program coverage"},
            "verification": {"analysis": "potentially_partial" if partial else "resolved_for_registered_scope",
                             "runtime_reachability": "requires_source_or_test_verification",
                             "next_steps": ["Inspect the pinned source at diagnostic locations.",
                                            "Replay the bundled registered checks and inspect their observed results.",
                                            "Add approved checks for behavior outside the registered scenarios."]},
            "snapshot_hashes": {s: g["hash"] for s, g in analysis["versions"].items()}}
