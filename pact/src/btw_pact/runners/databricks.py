"""Authenticated serverless Jobs execution, verified receipts and Delta history."""
import io
import json
import os
import re
import time
import zipfile
from pathlib import Path

from ..contracts import Observation, canonical, digest


def client():
    from databricks.sdk import WorkspaceClient
    return WorkspaceClient(profile=os.environ.get("PACT_DATABRICKS_PROFILE", "PACT"))


def namespace():
    name = os.environ.get("PACT_DATABRICKS_SCHEMA", "workspace.pact")
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*", name):
        raise ValueError("PACT_DATABRICKS_SCHEMA must be catalog.schema")
    return name


def package():
    root = Path(__file__).resolve().parents[1]
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
        for name in ("__init__.py", "contracts.py", "scenarios.py", "evaluator.py", "gitutil.py",
                     "fixture_worker.py", "remote_worker.py", "runners/__init__.py", "runners/local.py"):
            info = zipfile.ZipInfo("btw_pact/" + name, date_time=(2026, 9, 6, 0, 0, 0))
            archive.writestr(info, (root / name).read_bytes())
    return buffer.getvalue()


def execute_remote(bundle, chosen, run_id, *, workspace=None, wait_seconds=180, pending_dir=None):
    from databricks.sdk.service.jobs import NotebookTask, SubmitTask
    from databricks.sdk.service.workspace import ImportFormat, Language
    w = workspace or client()
    user = w.current_user.me().user_name
    folder = f"/Users/{user}/PACT/runs/{run_id}"
    # Oracle parameters and excerpt hashes travel; private transcript text does not.
    remote_bundle = json.loads(canonical(bundle))
    for requirement in remote_bundle["requirements"]:
        for source in requirement["source_refs"]:
            source["excerpt"] = ""
    payload = {"bundle": remote_bundle, "chosen": {s: [c.model_dump() for c in cases] for s, cases in chosen.items()},
               "run_id": run_id, "namespace": namespace(), "original_bundle_hash": digest(bundle)}
    payload_hash = digest(payload)
    code = package()
    import hashlib
    code_hash = hashlib.sha256(code).hexdigest()
    input_bytes = canonical({"payload": payload, "payload_hash": payload_hash, "code_hash": code_hash}).encode()
    notebook = '''# Databricks notebook source
# MAGIC %pip install pydantic==2.13.5

# COMMAND ----------
dbutils.library.restartPython()

# COMMAND ----------
import hashlib, json, sys, tempfile, zipfile
from pathlib import Path
folder = dbutils.widgets.get("artifact_folder")
root = Path("/Workspace" + folder)
envelope = json.loads((root / "input.json").read_text())
code = (root / "runtime.zip").read_bytes()
assert hashlib.sha256(code).hexdigest() == envelope["code_hash"], "Runtime artifact hash mismatch"
with tempfile.TemporaryDirectory(prefix="pact-runtime-") as td:
    with zipfile.ZipFile(root / "runtime.zip") as archive:
        archive.extractall(td)
    sys.path.insert(0, td)
    from btw_pact.remote_worker import run
    receipt = run(envelope, spark, dbutils.widgets.get("job_run_id"))
    repeated = run(envelope, spark, dbutils.widgets.get("job_run_id"))
    assert receipt == repeated, "Completed run replay changed the immutable receipt"
    receipt["idempotent_replay_verified"] = True
dbutils.notebook.exit(json.dumps(receipt))
'''
    receipt_file = Path(pending_dir or os.environ.get("PACT_PENDING_DIR", "pact/runs/pending")) / f"{run_id}.json"
    if receipt_file.exists():
        saved = json.loads(receipt_file.read_text())
        if saved["payload_hash"] != payload_hash or saved["code_hash"] != code_hash:
            raise ValueError("Recovery input/runtime differs from the submitted artifact; use its original implementation version")
        remote_id = saved["remote_run_id"]
    else:
        w.workspace.mkdirs(folder)
        w.workspace.upload(folder + "/input.json", input_bytes, format=ImportFormat.RAW, overwrite=False)
        w.workspace.upload(folder + "/runtime.zip", code, format=ImportFormat.RAW, overwrite=False)
        w.workspace.upload(folder + "/execute", notebook.encode(), format=ImportFormat.SOURCE,
                           language=Language.PYTHON, overwrite=False)
        submitted = w.jobs.submit(run_name=f"PACT {run_id[:8]}", idempotency_token=run_id,
                             timeout_seconds=900, tasks=[SubmitTask(task_key="verify_pact",
                             notebook_task=NotebookTask(notebook_path=folder + "/execute",
                                                       base_parameters={"artifact_folder": folder, "job_run_id": "{{job.run_id}}"}),
                             timeout_seconds=840, max_retries=0)])
        remote_id = submitted.response.run_id
        receipt_file.parent.mkdir(parents=True, exist_ok=True)
        receipt_file.write_text(canonical({"run_id": run_id, "remote_run_id": remote_id, "artifact_folder": folder,
                                       "payload_hash": payload_hash, "code_hash": code_hash}))
    deadline = time.monotonic() + wait_seconds
    while time.monotonic() < deadline:
        remote = w.jobs.get_run(remote_id)
        state = remote.state
        if state and state.life_cycle_state and state.life_cycle_state.value in ("TERMINATED", "SKIPPED", "INTERNAL_ERROR"):
            if not state.result_state or state.result_state.value != "SUCCESS":
                raise RuntimeError(f"Databricks run {remote_id} failed: {state.state_message or state.result_state}")
            task = next(t for t in remote.tasks if t.task_key == "verify_pact")
            result = w.jobs.get_run_output(task.run_id)
            if not result.notebook_output or result.notebook_output.truncated or not result.notebook_output.result:
                raise RuntimeError(f"Databricks run {remote_id} returned no complete result")
            receipt = json.loads(result.notebook_output.result)
            if (receipt["run_id"] != run_id or receipt["payload_hash"] != payload_hash
                    or receipt["code_hash"] != code_hash or receipt["original_bundle_hash"] != digest(bundle)):
                raise ValueError("Databricks receipt identity/hash mismatch")
            if "evidence_context" in bundle and (
                    receipt.get("evidence_context_hash") != digest(bundle["evidence_context"])
                    or digest(receipt.get("evidence_context")) != digest(bundle["evidence_context"])):
                raise ValueError("Databricks receipt lost or changed selection evidence")
            observations = {s: [Observation.model_validate(o) for o in rows] for s, rows in receipt["observations"].items()}
            from ..evaluator import evaluate
            from ..contracts import Requirement, Scenario
            requirements = [Requirement.model_validate(r) for r in bundle["requirements"]]
            cases = [Scenario.model_validate(s) for s in bundle["scenarios"]]
            expected = [a.model_dump() for side in ("base", "head") for a in evaluate(requirements, cases, observations[side], side, run_id)]
            if digest(expected) != receipt["assertion_hash"]:
                raise ValueError("Local evaluator and remote persisted assertions disagree")
            return observations, {k: v for k, v in receipt.items() if k != "observations"} | {
                "remote_run_id": remote_id, "run_url": remote.run_page_url, "artifact_folder": folder,
                "assertion_parity": "verified against returned observations"}
        time.sleep(5)
    raise RuntimeError(f"Databricks run {remote_id} remains pending after {wait_seconds}s; receipt preserved at {receipt_file}. Recover PACT run {run_id} instead of submitting a duplicate.")


def remote_history():
    """Read saved remote receipts from Delta, independent of local SQLite history."""
    from databricks.sdk.service.sql import StatementState
    w = client()
    warehouse = os.environ.get("PACT_DATABRICKS_WAREHOUSE") or next(iter(w.warehouses.list())).id
    result = w.statement_execution.execute_statement(warehouse_id=warehouse,
             statement=f"SELECT payload FROM {namespace()}.pact_runs ORDER BY created_at DESC LIMIT 30", wait_timeout="50s")
    if result.status.state != StatementState.SUCCEEDED or not result.result:
        raise RuntimeError(f"Remote history query incomplete: {result.statement_id}")
    return {"statement_id": result.statement_id, "source": namespace() + ".pact_runs",
            "runs": [json.loads(row[0]) for row in result.result.data_array or []]}
