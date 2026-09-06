"""Portable JSON replay bundles contain only the registered fixture and oracle."""
import json
import uuid
from pathlib import Path

from .contracts import Requirement, Scenario, canonical, digest
from .evaluator import classify, evaluate
from .runners.local import execute


def seal(payload):
    return {"format": "pact-reproducer-1", "payload": payload, "sha256": digest(payload)}


def unseal(bundle):
    if bundle.get("format") != "pact-reproducer-1" or bundle.get("sha256") != digest(bundle.get("payload")):
        raise ValueError("Reproducer integrity mismatch")
    return bundle["payload"]


def replay(bundle):
    payload = unseal(bundle)
    requirements = [Requirement.model_validate(r) for r in payload["requirements"]]
    cases = [Scenario.model_validate(s) for s in payload["scenarios"]]
    run_id, results = uuid.uuid4().hex, []
    for side in ("base", "head"):
        observations = execute(payload["fixtures"][side], cases, run_id, side, payload["commits"][side])
        results.extend(evaluate(requirements, cases, observations, side, run_id))
    findings = classify(results, requirements, [])
    incomplete = any(a.status in ("unresolved", "not_run") for a in results)
    failed = any(a.status == "fail" and a.side == "head" for a in results)
    return {"findings": findings, "assertions": [a.model_dump() for a in results],
            "exit_code": 2 if incomplete else 1 if failed else 0}


def write_json(path: Path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(canonical(value) + "\n")
