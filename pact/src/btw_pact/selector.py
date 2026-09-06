"""Reverse traversal of actual CALLS edges to registered policy entrypoints."""
from collections import defaultdict, deque

from .contracts import digest


def binding(symbol):
    return f"{symbol['file_path']}:{symbol.get('qualified_name', symbol['name'])}"


def select(requirements, analysis, strategy="graph", max_depth=8):
    selected, reasons, paths, unresolved = set(), defaultdict(list), [], set()
    active = [r for r in requirements if r.status == "confirmed_active"]
    unresolved.update(r.key for r in requirements if r.status in ("proposed", "unresolved"))
    changed_files = {f["path"] for f in analysis["diff"].get("files", [])}
    partial = analysis["partial"]
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
    if strategy == "graph":
        for side, graph in analysis["versions"].items():
            symbols, reverse = graph["symbols"], defaultdict(list)
            by_binding = defaultdict(list)
            for sid, symbol in symbols.items():
                by_binding[binding(symbol)].append(sid)
            for edge in graph["calls"]:
                if edge["from_id"] in symbols and edge["to_id"] in symbols:
                    reverse[edge["to_id"]].append(edge)
            roots = []
            for file in analysis["diff"].get("files", []):
                for change in file.get("changes", []):
                    name = change.get("name")
                    line = change.get("before_start_line" if side == "base" else "after_start_line")
                    candidates = [sid for sid, symbol in symbols.items() if symbol["file_path"] == file["path"]
                                  and (symbol["name"] == name or symbol.get("qualified_name") == name)
                                  and (line is None or symbol.get("start_line") == line)]
                    if len(candidates) == 1:
                        roots.extend(candidates)
                    elif len(candidates) != 1:
                        # Deleted/renamed/unsupported symbols must not create a false clean report.
                        partial = True
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
                        partial = True
            for root in sorted(set(roots)):
                queue = deque([(root, [root], [])])
                seen = {root}
                while queue:
                    node, nodes, edges = queue.popleft()
                    for key in targets[node]:
                        selected.add(key)
                        reasons[key].append(f"Structural impact on {side} at {graph['commit_sha'][:8]}")
                        record = {"commit_sha": graph["commit_sha"], "side": side,
                                  "requirement_ref": key, "changed_symbol": root, "entrypoint": node,
                                  "symbols": [symbols[s] for s in nodes], "edges": edges,
                                  "status": "structural_path"}
                        record["path_id"] = digest(record)[:20]
                        paths.append(record)
                    if len(edges) >= max_depth:
                        if any(e["from_id"] not in seen for e in reverse[node]):
                            partial = True
                        continue
                    for edge in sorted(reverse[node], key=lambda e: e["from_id"]):
                        caller = edge["from_id"]
                        if caller not in seen:
                            seen.add(caller)
                            queue.append((caller, nodes + [caller], edges + [edge]))
        # Broken/unsupported graph analysis cannot silently report a clean selection.
        if analysis.get("errors"):
            unresolved.update(r.key for r in active)
            partial = True
    return {"selected_requirement_ids": sorted(selected), "selection_reasons": dict(reasons),
            "paths": paths, "path_ids": [p["path_id"] for p in paths],
            "unresolved_ids": sorted(unresolved), "not_selected_ids": sorted({r.key for r in active} - selected),
            "partial_analysis": partial or bool(unresolved), "traversal_limit": max_depth,
            "snapshot_hashes": {s: g["hash"] for s, g in analysis["versions"].items()}}
