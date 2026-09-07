"""Shared evaluator on Databricks; five Delta tables are the execution ledger."""
import json
import re

from .contracts import Requirement, Scenario, canonical, digest, now
from .evaluator import evaluate
from .runners.local import execute


def run(envelope, spark, remote_run_id=None):
    payload = envelope["payload"]
    if digest(payload) != envelope["payload_hash"]:
        raise ValueError("Remote input hash mismatch")
    ns, run_id = payload["namespace"], payload["run_id"]
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*", ns):
        raise ValueError("Invalid table namespace")
    if not re.fullmatch(r"[0-9a-f]{32}", run_id):
        raise ValueError("Invalid run identity")
    spark.sql(f"CREATE SCHEMA IF NOT EXISTS {ns}")
    schema = "record_id string, run_id string, side string, requirement_ref string, scenario_id string, role string, status string, payload string, created_at string"
    spark.sql(f"CREATE TABLE IF NOT EXISTS {ns}.pact_runs ({schema}) USING DELTA")
    existing = spark.sql(f"SELECT payload FROM {ns}.pact_runs WHERE record_id='{run_id}'").collect()
    if existing:
        if len(existing) != 1:
            raise ValueError("Duplicate completed run identity")
        receipt = json.loads(existing[0]["payload"])
        if receipt["payload_hash"] != envelope["payload_hash"] or receipt["code_hash"] != envelope["code_hash"]:
            raise ValueError("A run identity cannot be reused with changed input or runtime")
        return receipt
    bundle = payload["bundle"]
    requirements = [Requirement.model_validate(r) for r in bundle["requirements"]]
    scenarios = [Scenario.model_validate(s) for s in bundle["scenarios"]]
    scenario_map = {s.scenario_id: s for s in scenarios}
    observations, assertions = {}, []
    for side in ("base", "head"):
        cases = [Scenario.model_validate(s) for s in payload["chosen"][side]]
        observations[side] = execute(bundle["fixtures"][side], cases, run_id, side, bundle["commits"][side], backend="databricks")
        assertions.extend(evaluate(requirements, scenarios, observations[side], side, run_id))

    def persist(table, rows):
        spark.sql(f"CREATE TABLE IF NOT EXISTS {ns}.{table} ({schema}) USING DELTA")
        if not rows:
            return
        view = "pact_input_" + table
        frame = spark.createDataFrame(rows, schema=schema)
        frame.createOrReplaceTempView(view)
        # Existing identities are immutable; retries may only repeat identical payloads.
        conflicts = spark.sql(f"SELECT count(*) AS n FROM {ns}.{table} t JOIN {view} s ON t.record_id=s.record_id WHERE t.payload <> s.payload").first()["n"]
        if conflicts:
            raise ValueError(f"Immutable Delta identity collision: {table}")
        spark.sql(f"MERGE INTO {ns}.{table} t USING {view} s ON t.record_id=s.record_id WHEN NOT MATCHED THEN INSERT *")
        duplicate = spark.sql(f"SELECT record_id FROM {ns}.{table} WHERE record_id IN (SELECT record_id FROM {view}) GROUP BY record_id HAVING count(*) <> 1 LIMIT 1").collect()
        if duplicate:
            raise ValueError(f"Delta duplicate identity: {table}")

    def row(identity, item, side="", requirement="", scenario="", role="", status="", rid=run_id):
        return (identity, rid, side, requirement, scenario, role, status, canonical(item), now())

    scenario_hash = digest(bundle["scenarios"])
    persist("pact_scenarios", [row(digest([scenario_hash,s.scenario_id]),s.model_dump() | {
        "scenario_set_id": scenario_hash, "provenance": "team-owned synthetic permission fixture"},
        scenario=s.scenario_id,role=s.role,rid="") for s in scenarios])
    req_hash = digest(bundle["requirements"])
    persist("pact_requirement_revisions", [row(digest([req_hash,r.key]),r.model_dump() | {
        "requirement_set_hash": req_hash},requirement=r.key,status=r.status,rid="") for r in requirements])
    persist("pact_observations", [row(digest([run_id,side,o.scenario_id]),o.model_dump(),side=side,
        scenario=o.scenario_id,role=scenario_map[o.scenario_id].role,status=o.status)
        for side,rows in observations.items() for o in rows])
    persist("pact_assertion_results", [row(digest([run_id,a.side,a.requirement_id,a.requirement_revision,a.scenario_id]),
        a.model_dump(),side=a.side,requirement=f"{a.requirement_id}@{a.requirement_revision}",
        scenario=a.scenario_id,role=scenario_map[a.scenario_id].role,status=a.status) for a in assertions])
    # Read the rows back from Delta before making a completion claim.
    saved_obs = spark.sql(f"SELECT payload FROM {ns}.pact_observations WHERE run_id='{run_id}'").collect()
    saved_assertions = spark.sql(f"SELECT payload FROM {ns}.pact_assertion_results WHERE run_id='{run_id}'").collect()
    saved = [json.loads(r["payload"]) for r in saved_assertions]
    order = lambda a: (a["side"],a["requirement_id"],a["requirement_revision"],a["scenario_id"])
    expected = [a.model_dump() for a in assertions]
    if (len(saved_obs) != sum(len(v) for v in observations.values()) or
            digest(sorted(saved,key=order)) != digest(sorted(expected,key=order))):
        raise ValueError("Persisted result cardinality/hash mismatch")
    actual_obs = {s: [] for s in ("base", "head")}
    for r in saved_obs:
        o = json.loads(r["payload"])
        actual_obs[o["side"]].append(o)
    grouped = spark.sql(f"SELECT requirement_ref, role, count(*) AS violations FROM {ns}.pact_assertion_results WHERE run_id='{run_id}' AND side='head' AND status='fail' GROUP BY requirement_ref, role ORDER BY requirement_ref, role").collect()
    receipt = {"run_id": run_id, "remote_run_id": remote_run_id, "payload_hash": envelope["payload_hash"], "code_hash": envelope["code_hash"],
               "original_bundle_hash": payload["original_bundle_hash"], "commits": bundle["commits"],
               "assertion_hash": digest(expected), "observations": actual_obs,
               "assertions": expected, "grouped_violations": [r.asDict() for r in grouped],
               "observation_count": len(saved_obs), "assertion_count": len(saved),
               "namespace": ns, "completion_state": "partial" if any(a.status in ("unresolved","not_run") for a in assertions) else "complete",
               "synthetic": True, "data_source": "team-owned 24-case permission fixture"}
    # Execution completion never upgrades the saved analysis or source context.
    if "evidence_context" in bundle:
        context = bundle["evidence_context"]
        receipt["evidence_context"] = context
        receipt["evidence_context_hash"] = digest(context)
        receipt["review_completion_state"] = "partial" if (
            receipt["completion_state"] == "partial" or context["selection"]["partial_analysis"]
            or context["source_gaps"]) else "complete"
    persist("pact_runs", [row(run_id,receipt,status=receipt["completion_state"])])
    return receipt
