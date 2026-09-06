from .contracts import AssertionResult, digest
from .scenarios import matches


def evaluate(requirements, cases, observations, side, run_id):
    lookup = {o.scenario_id: o for o in observations}
    results = []
    for r in requirements:
        for case in cases:
            if not matches(r, case):
                continue
            observation = lookup.get(case.scenario_id)
            if side not in r.applies_to:
                status, reason = "not_applicable", "Requirement does not apply to this version"
            elif r.status != "confirmed_active":
                status, reason = "unresolved", "Requirement is not confirmed"
            elif observation is None:
                status, reason = "not_run", "No observation returned"
            elif observation.status != "ok":
                status, reason = "unresolved", observation.error_message or observation.status
            else:
                status = "pass" if observation.allowed == r.expected_allowed else "fail"
                reason = "Confirmed permission predicate evaluated against observed output"
            applicable = side in r.applies_to
            results.append(AssertionResult(run_id=run_id, requirement_id=r.requirement_id,
                           requirement_revision=r.revision, scenario_id=case.scenario_id, side=side,
                           expected_allowed=r.expected_allowed, actual_allowed=observation.allowed if observation and applicable else None,
                           status=status, applicability_reason=reason,
                           observation_ref=f"{run_id}/{side}/{case.scenario_id}" if observation and applicable else None))
    return results


def classify(results, requirements, paths):
    lookup = {(a.requirement_id, a.requirement_revision, a.scenario_id, a.side): a for a in results}
    findings = []
    for head in (a for a in results if a.side == "head" and a.status not in ("pass", "not_applicable")):
        base = lookup.get((head.requirement_id, head.requirement_revision, head.scenario_id, "base"))
        if head.status != "fail":
            kind = "inconclusive"
        elif base and base.status == "pass":
            kind = "confirmed_regression"
        elif base and base.status == "fail":
            kind = "pre_existing_violation"
        elif base and base.status == "not_applicable":
            kind = "candidate_violation"
        else:
            kind = "inconclusive"
        key = f"{head.requirement_id}@{head.requirement_revision}"
        req = next(r for r in requirements if r.key == key)
        row = {"requirement_ref": key, "scenario_ref": head.scenario_id, "classification": kind,
               "base_result": base.model_dump() if base else None, "head_result": head.model_dump(),
               "path_refs": [p["path_id"] for p in paths if p["requirement_ref"] == key],
               "source_refs": [s.model_dump() for s in req.source_refs],
               "provenance_verified": bool(req.source_refs) and all(s.association_status == "verified" for s in req.source_refs)}
        row["evidence_hash"] = digest(row)
        row["finding_id"] = row["evidence_hash"][:20]
        findings.append(row)
    return findings
