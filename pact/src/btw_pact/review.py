"""One review orchestrator; both backends return the same observation contract."""
import time
import subprocess
import json
import re
import uuid
from pathlib import Path

from . import __version__
from .contracts import ReviewRequest, digest, now
from .evaluator import classify, evaluate
from .evidence import seal, write_json
from .gitutil import fixture_files, resolve
from .graph_adapter import analyse
from .runners.local import execute
from .scenarios import matches, matrix
from .selector import select


def review(request: ReviewRequest, output: Path, *, remote=None, run_id=None):
    started, run_id = time.perf_counter(), run_id or uuid.uuid4().hex
    if not re.fullmatch(r"[0-9a-f]{32}", run_id):
        raise ValueError("Invalid review identity")
    repo = Path(request.repo_path).resolve()
    commits = {"base": resolve(repo, request.base_sha), "head": resolve(repo, request.head_sha)}
    request = request.model_copy(update={"base_sha": commits["base"], "head_sha": commits["head"]})
    fixtures = {side: fixture_files(repo, sha) for side, sha in commits.items()}
    graph_start = time.perf_counter()
    try:
        analysis = analyse(repo, commits["base"], commits["head"])
    except (RuntimeError, ValueError, OSError, subprocess.TimeoutExpired) as error:
        analysis = {"diff": {"files": []}, "versions": {}, "partial": True, "errors": [str(error)[:2000]]}
    graph_ms = (time.perf_counter() - graph_start) * 1000
    selection = select(request.requirements, analysis, request.strategy)
    selected = [r for r in request.requirements if r.key in selection["selected_requirement_ids"]]
    cases = matrix()
    chosen = {side: cases if request.execution_scope == "full_scenario_matrix" else [s for s in cases if any(
        side in r.applies_to and matches(r, s) for r in selected)] for side in ("base", "head")}
    source_gaps = [r.key for r in selected if not r.source_refs or any(s.association_status in ("unresolved", "synthetic") for s in r.source_refs)]
    evidence_context = {"schema_version": "1.0", "selection": selection, "source_gaps": source_gaps,
                        "verification_scope": "registered assertions only; static paths are not runtime traces"}
    bundle_payload = {"evidence_context": evidence_context, "commits": commits, "fixtures": fixtures,
                      "requirements": [r.model_dump() for r in selected], "scenarios": [s.model_dump() for s in cases]}
    input_hash = digest(bundle_payload)
    execution_start = time.perf_counter()
    backend_meta, observations = {}, {}
    if request.runner == "databricks":
        pending = Path(output) / "pending"
        pending.mkdir(parents=True, exist_ok=True)
        request_file = pending / f"{run_id}-request.json"
        if request_file.exists():
            if digest(json.loads(request_file.read_text())) != digest(request.model_dump()):
                raise ValueError("Recovery request differs from the original review")
        else:
            write_json(request_file, request.model_dump())
        if remote is None:
            from .runners.databricks import execute_remote
            remote = lambda bundle, cases, identity: execute_remote(bundle, cases, identity, pending_dir=pending)
        observations, backend_meta = remote(bundle_payload, chosen, run_id)
    else:
        for side in ("base", "head"):
            observations[side] = execute(fixtures[side], chosen[side], run_id, side, commits[side])
    results = []
    for side in ("base", "head"):
        expected_ids = {s.scenario_id for s in chosen[side]}
        rows = observations[side]
        if (len(rows) != len(expected_ids) or {o.scenario_id for o in rows} != expected_ids
                or any(o.run_id != run_id or o.commit_sha != commits[side] or o.side != side
                       or o.execution_backend != request.runner for o in rows)):
            raise ValueError("Backend result identity or cardinality mismatch")
        results.extend(evaluate(selected, cases, rows, side, run_id))
    execution_ms = (time.perf_counter() - execution_start) * 1000
    findings = classify(results, selected, selection["paths"])
    incomplete = selection["partial_analysis"] or bool(source_gaps) or any(a.status in ("unresolved", "not_run") for a in results)
    counts = {}
    for side in ("base", "head"):
        subset = [a for a in results if a.side == side]
        counts[side] = {status: sum(a.status == status for a in subset) for status in ("pass", "fail", "not_applicable", "unresolved", "not_run")}
        counts[side]["observations"] = len(observations[side])
    report = {"schema_version": "1.0", "run_id": run_id, "created_at": now(), "version": __version__,
              "request": request.model_dump(), "commits": commits, "requirement_set_hash": digest([r.model_dump() for r in request.requirements]),
              "scenario_set_hash": digest([s.model_dump() for s in cases]), "bundle_hash": input_hash,
              "execution_scope": request.execution_scope, "comparison_id": request.comparison_id,
              "cache_mode": "disabled", "backend": request.runner, "backend_metadata": backend_meta,
              "selection": selection, "evidence_context": evidence_context, "counts": counts, "findings": findings,
              "observations": {s: [o.model_dump() for o in obs] for s, obs in observations.items()},
              "assertions": [a.model_dump() for a in results], "source_gaps": source_gaps,
              "completion_state": "partial" if incomplete else "complete", "errors": analysis.get("errors", []),
              "timing_ms": {"graph": graph_ms, "execution": execution_ms, "total": (time.perf_counter() - started) * 1000},
              "limitations": ["Synthetic, seeded pilot; 24 cases are not complete program coverage.",
                              "Static paths show possible influence, not proven runtime execution.",
                              "Runtime is for the registered trusted fixture, not hostile repositories.",
                              "Checkpoint attachment mode and source gaps are reported explicitly."]}
    run_dir = Path(output) / run_id
    run_dir.mkdir(parents=True, exist_ok=False)
    write_json(run_dir / "reproducer.json", seal(bundle_payload))
    write_json(run_dir / "report.json", report)
    if "diff_raw" in analysis:
        (run_dir / "graph-diff.json").write_text(analysis["diff_raw"])
    for side, graph in analysis["versions"].items():
        (run_dir / f"graph-{side}.ndjson").write_text(graph["raw"])
    return report


def recover(run_id: str, output: Path):
    if not re.fullmatch(r"[0-9a-f]{32}", run_id):
        raise ValueError("Invalid review identity")
    completed = Path(output) / run_id / "report.json"
    if completed.exists():
        return json.loads(completed.read_text())
    request_file = Path(output) / "pending" / f"{run_id}-request.json"
    if not request_file.exists() or not (Path(output) / "pending" / f"{run_id}.json").exists():
        raise ValueError("No recoverable remote receipt/request pair; inspect the job before submitting again")
    return review(ReviewRequest.model_validate_json(request_file.read_text()), output, run_id=run_id)


def benchmark(request: ReviewRequest, output: Path):
    comparison_id = uuid.uuid4().hex
    reports = [review(request.model_copy(update={"strategy": strategy, "comparison_id": comparison_id,
                     "execution_scope": "selected_applicable_scenarios"}), output) for strategy in ("changed_file", "graph", "all")]
    reference = review(request.model_copy(update={"strategy": "all", "comparison_id": comparison_id,
                       "execution_scope": "full_scenario_matrix"}), output)
    return {"comparison_id": comparison_id, "strategies": reports, "reference": reference}
